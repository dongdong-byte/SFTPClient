# SFTPClient — Unit 2 구현 설계·결정 기록 v3: 해시 기반 변경 판정 + schema v6

- **최초 작성:** 2026-09-17
- **구현 완료:** 2026-09-18
- **상태:** **Unit 2 구현 완료. 전체 후속 계획은 5개 Unit 중 2개 완료(2/5).**
  이 문서는 초기 제안서가 아니라 Unit 2의 네 개 커밋, 최종 동작,
  교차검증 결과와 채택·기각 근거를 기록한다.
- **선행:** FOLLOWUP_PLAN §2.1·§2.2, 인시던트 §6·§7-3·§8, LEDGER_CONCEPT 4.9
- **최종 구현:** SHA-256 안정 관측, Unchanged/MetadataChangedOnly/
  ContentChanged 3분화, schema v6, v4→v5→v6 연속 마이그레이션,
  guarded ledger API, 필수 해시/자연 백필 이원화, 원인별 계측.

---

## 0. 완료 범위와 핵심 결과

Unit 2 구현은 다음 네 커밋으로 완료됐다. Unit 1(logging)은 선행 완료됐고,
Unit 3~5는 이 문서의 완료 범위가 아니다.

1. `0c93b7f` — schema v6, `content_hash`, v5→v6 및 v4→v5→v6
2. `5f4b5a3` — `TouchCommonMTime`, `SetContentHash`, 전체 guard
3. `222c917` — `MaxHashBackfillPerRun` 설정과 4치 의미
4. `9257388` — Runner 판정 3분화, Hasher, 필수/백필, 계측

최종 결과:

- VERIFIED 파일의 **size와 내용은 같고 mtime만 바뀌면** revision과 후보가
  증가하지 않는다. mtime 기준선만 현재 관측값으로 이동한다.
- size가 달라졌거나 같은 size에서 해시가 달라졌으면 실제 변경으로 보고
  revision을 증가시켜 전송한다.
- v6 배포 직후처럼 기존 지문이 없으면 동일 내용으로 단정하지 않는다.
  mtime 변경 시 한 번 보수적으로 재전송하고 새 revision에 지문을 저장한다.
- 해시 중 파일이 변하면 해당 파일을 이번 회차에서 보류한다.
- 해시 읽기 실패는 WARN 후 지문 없이 신규/변경 처리를 계속한다.

따라서 Unit 2는 “배포 즉시 모든 과거 행의 mtime 드리프트를 0건으로 억제”가
아니라, **기준 지문이 있는 파일의 헛전송을 억제하고 기존 무지문 행은 자연
백필 또는 1회 보수 재전송으로 안전하게 전환하는 설계**다.

## 1. 최종 판정 규칙

| 장부/관측 상태 | 처리 | revision | 전송 후보 |
|---|---|---:|---:|
| 장부에 없음 | Ingress 검증 후 신규 등록, 가능한 경우 최초 지문 저장 | 1 생성 | 예 |
| size 같음, mtime 같음 | `Unchanged` | 유지 | 현재 revision의 PUT 상태에 따름 |
| size 같음, mtime 다름, 기존 지문 있음, 새 지문 같음 | `MetadataChangedOnly`; mtime 기준선만 갱신 | 유지 | 새 후보 없음 |
| size 같음, mtime 다름, 기존 지문 있음, 새 지문 다름 | `ContentChanged`; 새 지문과 함께 Upsert | 증가 | 예 |
| size 같음, mtime 다름, 기존 지문 없음 | 보수 변경 처리 | 증가 | 예, 1회 보수 재전송 |
| size 다름 | `ContentChanged`; 가능한 경우 새 지문 저장 | 증가 | 예 |
| 해시 전후 또는 스캔 관측과 파일 정보 불일치 | 불안정 관측으로 파일 단위 보류 | 변경 없음 | 아니오 |
| 해시 읽기 실패 | WARN 후 지문 없이 신규/변경 처리를 계속 | 신규/변경 규칙대로 | 예 |

`Unchanged`는 “이미 보냈다”는 뜻이 아니다. 현재 revision의 PUT 이력이 없거나
`PENDING`, 재시도 가능한 `FAILED`이면 기존과 같이 후보가 된다.

### 1.1 해시의 안정성 검사

`SetContentHash` 의 SQL 가드는 **DB 행**의 불변만 보장한다. 해시 계산
**도중** 디스크 파일이 변하는 것은 별개의 사실이므로 Hasher 가 직접
검사한다:

```
파일 open → 동일 핸들 fstat(전) → SHA-256 스트리밍 → 동일 핸들 fstat(후)
→ Hasher가 전후 사실을 반환 → Runner가 스캔 관측치까지 함께 대조
```

- 스캔 시점 관측치(e.Size/e.MTime)와 fstat(전)의 대조도 수행한다 —
  스캔과 해시 사이에 이미 변했으면 그 관측 자체가 stale 이다.
- `ErrUnstable` 대신 **관측/판정 분리**: Hasher 는 사실만 돌려주고
  (전후 stat, 읽은 바이트), 불안정 여부의 **판정은 Runner** 가 한다 —
  스캔 관측치와의 대조까지 포함해서. 장부가 기록만 하고 판정하지 않는
  것과 같은 계열의 분리다.
- Hasher 는 `context.Context` 를 받는다 — 지금은 취소원이 시그널뿐이지만
  순서 3(watchdog)이 취소를 도입할 때 시그니처 변경 없이 연결된다.

```go
// HashResult 는 해시 계산의 관측 사실이다. 판정하지 않는다.
type HashResult struct {
    Hash      string // SHA-256 hex 소문자 64자
    PreSize   int64  // 계산 직전 동일 핸들 stat
    PreMTime  int64  // Unix 초, UTC
    PostSize  int64  // 계산 직후 동일 핸들 stat
    PostMTime int64
    Bytes     int64  // 실제 읽은 바이트 (계측용)
}

type Hasher interface {
    HashFile(ctx context.Context, path string) (HashResult, error)
}
```

Runner 의 불안정 판정: `Pre != Post` **또는** `Pre != 스캔 관측치(e)` 이면
stale observation — §1.2의 "회차 보류" 경로. err != nil은 읽기 실패 —
§1.2의 보수 경로.
- `[기각]` Hasher 가 스캔 관측치를 입력받아 스스로 판정 — 관측기가
  판정까지 소유하면 통계(불안정 수)의 주인이 둘이 되고, 판정 규칙이
  바뀔 때마다 인터페이스가 흔들린다. 사실 반환이 테스트 스텁도 단순하다.

### 1.2 실패 정책

> **해시 읽기 실패는 전송을 막지 않는다 (누락 > 헛전송).
> 단, 불안정 관측은 회차 보류다 (작성 중 파일 취급).**

| 상황 | 판정 | 지문 | 비고 |
|---|---|---|---|
| 판정 해시(`size==·mtime≠`) 읽기 실패 | ContentChanged 간주 + WARN | 저장 안 함(`''` 유지) | 진짜 I/O 장애면 전송 단계가 FAILED 로 드러낸다 |
| 신규 최초 지문 읽기 실패 | New 그대로 등록·후보 + WARN | `''` | 등록·전송을 막지 않는다 |
| `size≠` 새 지문 읽기 실패 | ContentChanged 그대로 + WARN | `''` | size 가 이미 변경 증거 — 지문은 부가물 |
| 후보 필수 해시(§3) 읽기 실패 | 후보 유지 + WARN | `''` | 전송 예정대로 |
| 백필 해시 읽기 실패 | 건너뜀 + WARN | `''` | 예산 차감 (§3) |
| **불안정 관측 (Runner 판정: Pre≠Post 또는 Pre≠스캔 관측치, 모든 경로)** | **이번 회차 보류** — observed 미포함, 후보 미포함, rejected 계열 집계 | 저장 안 함 | 미완성 파일 보류와 동일 의미론. 세트 게이트가 미완성 세트를 보류하는 것은 의도된 동작 |

## 2. 스키마 v6과 ledger API

### 2.1 스키마

```sql
-- v6: content_hash 추가 (2026-09-17)
ALTER TABLE common_ledger ADD COLUMN content_hash TEXT NOT NULL DEFAULT ''
    CHECK (content_hash = ''
        OR (length(content_hash) = 64
            AND content_hash NOT GLOB '*[^0-9a-f]*'));
-- '' = 지문 없음(v6 이전 행 / 백필 미도달 / 해시 실패).
-- 의미: "이 revision 으로 최근 안정적으로 관측한 로컬 파일 전체 바이트의
-- 지문". 전송 완료 여부는 이 컬럼의 사실이 아니다 — 그것은 put_ledger
-- VERIFIED 의 소유다 (하나의 사실에 하나의 주인).
-- 용도: size= · mtime≠ 관측에서만 대조. 운영자용 VIEW 기본 미노출.
```

- NOT NULL DEFAULT '' 는 set_key/kind 전례 (nullable 기각 근거 동일).
- CHECK 는 state/revision 등 기존 CHECK 방어 관행을 따른다 — 프로그램
  검증과의 중복은 "코드 결함이 데이터를 오염시키기 전에 DB 가 막는다"는
  기존 원칙의 값이다.
- 신규 설치용 `CREATE TABLE` 정의에도 동일 컬럼·CHECK를 넣는다.
- **ALTER + CHECK 실물 확인:** 프로젝트의 `modernc.org/sqlite v1.57.0`
  내장 SQLite 3.53.3에서 마이그레이션 경로를 실행해 기존 행의 `''` 기본값,
  63자·대문자·비hex 차단, 64자 소문자 hex 허용을 확인했다. 신규 DB에만
  CHECK를 적용하는 fallback은 폐기했으며 신규·마이그레이션 DB가 같은
  제약조건을 가진다.

### 2.2 ledger API

| API | 계약 |
|---|---|
| `Known.ContentHash` | `''` = 지문 없음 |
| `UpsertCommon` + `CommonInput.ContentHash` | new/ContentChanged 경로가 지문 쓰기 주인. 해시 실패 시 `''` |
| `TouchCommonMTime(ctx, key, judged) (applied bool, err error)` | `UPDATE ... SET mtime=새값 WHERE category·file_name·revision·size·mtime·content_hash = 판정에 쓴 값 전부` — 판정 근거가 그대로일 때만 갱신 |
| `SetContentHash(ctx, key, judged, hash) (applied bool, err error)` | `WHERE ... AND content_hash=''` 포함 — "size=·mtime= 이면 내용 동일"의 안전 근거를 SQL 이 강제 |

**0행(`applied=false`) 정책:** WARN 기록 + 해당 파일만 candidate와
`observed`에서 제외 + 다음 회차 재관측. 입력 계약, SQL, DB, context,
`RowsAffected` 오류는 실제 오류로 반환한다. 채택·기각 근거는 §6에 모았다.

### 2.3 연속 마이그레이션

```go
// migrateIfNeeded — 명시 구현된 스텝의 순차 실행으로 일반화.
if got == "4" {
    if err := db.migrateV4toV5(ctx); err != nil { return err }
    got = "5"
}
if got == "5" && schemaVersion == "6" {
    return db.migrateV5toV6(ctx)
}
```

- **운영 필수 경로:** 운영 3개 기관(전부 v5)의 `v5→v6` 전환과 데이터 보존.
  이 분기가 없으면 정상 v5 DB가 `verifySchemaVersion`에서 거부된다.
- **호환 경로:** v4 DB는 같은 Open 안에서 `v4→v5→v6`을 순차 수행한다.
  schemaVersion 상승으로 기존 v4 경로가 죽는 회귀를 막고, 오프라인·Linux
  장기 미업데이트 설치본도 한 번의 실행으로 최신 세대에 도달한다.
- `migrateV5toV6` 는 전례(단일 트랜잭션: ALTER + 버전 갱신, 실패 시
  온전한 v5 로 롤백)를 따른다. **행 백필 없음** — 지문은 파일 바이트를
  읽어야 하는 값이라 DB 트랜잭션의 일이 아니다.
- v4→v5와 v5→v6은 서로 독립된 트랜잭션이다. 두 번째 단계가 실패하면
  온전한 v5가 남고 다음 실행이 v5→v6부터 재시도한다.
- 자동 전환은 명시 구현된 v4와 v5만 지원한다. 향후 v7에는
  `migrateV6toV7`과 연속 분기를 추가해야 한다.

## 3. 필수 해시와 자연 백필

| 구분 | 대상 | 예산 |
|---|---|---|
| **필수 해시** | 신규 최초 지문 / ContentChanged 새 지문 / **Unchanged인데 현재 revision이 전송 후보**(`''`·PENDING·재시도 가능 FAILED)이며 지문 없음 | **무관** — 지금 보낼 바이트의 기준 지문 |
| **자연 백필** | Unchanged + VERIFIED + 지문 없음 | `MaxHashBackfillPerRun` 적용 |

- 필수 해시는 전송 안전 기능이므로 백필 예산으로 제한하지 않는다.
- 자연 백필은 전송과 무관한 유지보수 읽기이므로 회차별로 상각한다.
- 백필 시도는 성공 여부와 관계없이 예산을 1 소비한다. 실패한 읽기도 실제
  I/O 비용을 사용했기 때문이다. 지문은 비어 있으므로 다음 회차에 재시도된다.

### 3.1 `MaxHashBackfillPerRun`의 4치 의미

| 입력 | 의미 |
|---|---|
| **키 부재** | **500 (기본)** — 실행파일만 교체한 기존 현장 config 에서 백필이 즉시 작동해야 한다 |
| `0` | 백필 끔 |
| 양수 | 해당 상한 |
| 음수 | validate 오류 |

`optionalIntVal`은 이 키 한 곳에서만 사용해 부재와 명시적 0을 분리한다.
키가 실제로 없을 때만 500을
적용하며, 비정수·빈 값·정수 범위 초과는 `ErrBadValue`, 잘못된 섹션·키는
`ErrUnknownKey`, 음수는 Validate 오류다. `Load`와 `LoadFrom`은 map 이후
Validate를 반드시 호출한다. 코드가 `Config`를 직접 만들 때의 0값은 의도대로
“백필 끔”이다. helper 채택과 상한 정책의 근거는 §6에 모았다.

## 4. 실행 모드별 동작

### 4.1 live

판정 해시, 필수 해시, 자연 백필과 Ledger 쓰기를 모두 수행한다.

### 4.2 dry-run

- 기존 지문이 있는 mtime 드리프트 판정 해시는 실제로 계산한다.
- live와 같은 changed/metadata-only 분류를 보여준다.
- Touch, 지문 저장, Upsert, put_ledger 쓰기는 하지 않는다.
- 자연 백필은 수행하거나 별도로 보고하지 않는다.
- 신규·변경의 저장용 지문은 계산하지 않는다.

### 4.3 seed

- 후보 계산과 Ingress 검증은 기존 Runner를 재사용한다.
- 지문 계산·저장은 생략하고 이후 live 자연 백필에 맡긴다.
- `MaxFilesPerRun` 절단과 PENDING 등록을 생략한다.
- 원격 대조 성공분만 `SeedVerified`로 기록한다.

### 4.4 DOWNLOAD origin과 set gate

`Origin=DOWNLOAD && !RepostDownloaded` 제외는 해시 판정보다 먼저 적용해 기존
Ping-Pong 방지 정책을 유지한다.

`Unchanged`와 `MetadataChangedOnly`는 후보가 아니어도 실제 세트 구성원으로
`observed`에 포함한다. 이미 VERIFIED인 형제가 빠져 신규 형제의 세트가 영구
보류되는 회귀를 막는다. 해시 불안정 또는 guard 0행 파일은 observed에서도
제외한다.

## 5. 계측·과도기·운영 영향

- **판정·필수 해시에 상한을 두지 않는다.** 자연 백필만 설정 예산으로 제한한다.
- **집계(리포트 + 로그 요약):** 해시 원인 4분류(신규 기준 / 변경 새 지문
  / 드리프트 판정 / 백필) 각각의 **건수·바이트·소요시간**, 실패 수,
  unstable 수, 백필 유예(예산 소진) 수와 metadata_only 수.
- 지문 없는 현재 revision 전송 후보의 필수 해시는 방어 본체 계열인
  `HashDrift` 묶음에 포함된다.
- **최악 부하 산식(단정 수치 대신):**
  `총 읽기량 = 판정 대상 크기 합, 예상 시간 ≈ 총 읽기량 / 실측 순차 읽기
  처리량`. 실측은 배포 전 대상 서버에서 1회 기록한다.
- **lock 겹침:** 장시간 해시로 다음 회차와 겹치면 후속 실행은
  `lock.ErrHeld` 로 **정상 종료(0)** 한다 — 기존 설계가 이미 안전하게
  처리하므로 추가 장치 없음. 계측 로그가 장기화 추세를 드러낸다.
- **과도기 명시:** v6 배포 직후 기존 행은 지문이 없다. 자연 백필이 닫히기
  전에 mtime 폭풍이 재발하면 지문 없는 행은 §1의 무지문 드리프트 규칙에
  따라 **1회 보수 재전송**되며 그때 지문이 채워진다. 기본값 500/회에서
  약 10일이라는 추정은 매 회차 스캔에 계속 노출되는 대상에만 해당한다.
  완전한 억제는 기준 지문이 채워진 이후 작동한다.

## 6. 확정 결정과 기각안

### 6.1 revision의 주인

- 원격 SFTP 전송 실패는 common revision 사건이 아니다. 같은 revision의
  `put_ledger.status/attempts`가 재시도를 소유한다.
- 로컬 유입 중 끊긴 부분 파일이 나중에 완성되어 size가 변하면
  `UpsertCommon`이 정상적으로 revision을 증가시킨다.
- `TouchCommonMTime`은 기존 지문과 새 안정 해시가 같을 때만 호출하므로
  정당한 내용 변경의 revision을 막지 않는다.
- 빈 지문 드리프트를 Touch하는 안은 기각했다. 동일 내용이라는 증거가 없어
  같은 크기 보정 파일을 놓칠 수 있기 때문이다.

### 6.2 guard 불일치와 오류

- 두 refresh API는 판정에 사용한 `category·file_name·revision·size·mtime`을
  모두 guard로 사용한다. Touch는 기존 지문, Set은 빈 지문 조건도 강제한다.
- 0행을 조용히 무시하는 안은 기각했다. 단일 Writer/실행 lock 아래에서는
  예상 밖 상태이므로 WARN이 필요하다.
- 파일 하나 때문에 회차 전체를 중단하거나 재조회 후 즉시 재시도하는 안도
  기각했다. 최종 정책은 해당 파일 보류 + 다음 회차 재관측이다.
- SQL·DB·context·입력 오류까지 무시하지 않는다. 이런 오류는 상위로 반환한다.

### 6.3 Hasher와 Runner의 경계

Hasher가 스캔 관측치를 받아 불안정을 직접 판정하는 안은 기각했다. Hasher는
전후 stat·지문·읽은 바이트라는 사실만 반환하고, Runner가 스캔 관측치와
대조한다. 판정 규칙과 계측의 주인을 Runner 하나로 유지한다.

### 6.4 해시 부하 제한

판정·필수 해시에 상한을 두는 안은 기각했다. 상한 초과분을 변경으로 처리하면
mtime 폭풍 방어가 다시 뚫리고, 다음 회차로 미루면 실제 변경 전송이 지연된다.
자연 백필만 `MaxHashBackfillPerRun`으로 제한한다.

### 6.5 config helper와 값 범위

기존 `intVal`의 의미를 바꾸는 안은 필수 정수 키 전체에 파급되므로 기각했다.
부재와 명시적 0을 구분해야 하는 신규 키 하나에만 `optionalIntVal`을 적용한다.
양수의 별도 최대 상한은 두지 않는다. 명시한 값은 운영자가 선택한 I/O 예산이며
기본값 500과 계측으로 정상 운영 부하를 관리한다.

### 6.6 dry-run과 seed

- dry-run에서 판정 해시까지 생략하는 안은 실제 변경으로 잘못 예고하므로 기각했다.
- dry-run에서 자연 백필 목록을 보고하는 안은 전송 계획이라는 목적과 달라 기각했다.
- seed에서 전량 해시하는 안은 현장 설치 중 대량 읽기 비용 때문에 기각했다.

## 7. 구현 커밋

### 7.1 `0c93b7f` — ledger schema v6

- `content_hash` 컬럼·CHECK와 입력 검증
- `Known` 조회와 `CommonInput`/Upsert 저장 연결
- 운영 v5→v6 및 호환 v4→v5→v6 연속 마이그레이션
- 신규 DB와 마이그레이션 DB의 동일 제약 검증

### 7.2 `5f4b5a3` — guarded ledger refresh API

- `TouchCommonMTime`, `SetContentHash`
- 판정 사실 전체 optimistic guard
- 빈 지문 Touch 거부
- 0행 `applied=false`와 실제 error 분리

### 7.3 `222c917` — config

- `MaxHashBackfillPerRun`과 기본값 500
- `optionalIntVal`로 부재와 명시적 0 분리
- 음수·비정수·빈 값·오타 처리와 설정 예제
- H14 다섯 상태 테스트

### 7.4 `9257388` — Runner·Hasher 통합

- SHA-256 스트리밍 Hasher와 context 취소
- 판정 3분화와 mtime-only 재전송 억제
- 필수 해시와 예산형 자연 백필
- 해시 실패·불안정·guard 0행 정책 연결
- dry-run·seed·DOWNLOAD origin·set gate 통합
- 원인별 해시 계측과 핵심 회귀 테스트

## 8. 검증 상태

2026-09-18 현재 HEAD `9257388`에서 다음을 확인했다.

```text
go test ./... -count=1
```

모든 패키지가 통과했다. 주요 고정 계약은 다음과 같다.

- v5→v6과 v4→v5→v6 데이터 보존·재실행 안전성
- 신규/마이그레이션 DB의 content_hash CHECK
- Touch/SetContentHash 전체 guard와 빈 지문 Touch 거부
- config 부재·0·양수·음수·비정수 다섯 상태
- Hasher 전후 stat, 실제 읽은 바이트, context 취소
- 동일 해시 mtime 드리프트의 revision/후보 유지
- 빈 지문 mtime 드리프트의 1회 보수 재전송
- 해시 불안정 파일의 회차 보류
- VERIFIED Unchanged 형제의 set gate observed 유지

`-race`는 초기 ledger 단계에서 사용자 환경 통과가 확인됐다. 현재 샌드박스는
GCC 실행 권한 제한으로 전체 HEAD의 race 재검증을 수행하지 못했으므로 배포
빌드 환경에서 `go test ./... -race`를 최종 실행한다.

## 9. 배포·운영 주의사항

1. 운영 DB를 삭제하거나 재생성하지 않는다. `ledger.Open`이 v5→v6을 수행한다.
2. 배포 전 DB를 백업하고 최초 실행 로그의 v5→v6 완료를 확인한다.
3. 배포 직후 기존 행 대부분은 지문이 없다. 자연 백필 과도기에 해당 파일의
   mtime이 바뀌면 1회 보수 재전송될 수 있다.
4. `MetadataOnly`, 해시 건수·바이트·시간, 실패·불안정·백필 유예를 관찰한다.
5. 매우 큰 `MaxHashBackfillPerRun`은 한 회차 I/O를 늘리는 명시적 운영 선택이다.
6. 장시간 해시가 다음 회차와 겹치면 후속 실행은 기존 lock의 `ErrHeld`로
   정상 종료한다. 최초 배포 후 실행시간과 lock 겹침을 확인한다.

## 10. 이 유닛이 하지 않는 것

- 파일 내용 자체의 업무적 정합성 검사
- 원격 파일의 내용 해시 검증(현재 전송 검증은 기존 size 계약 유지)
- 자연 백필 실패 이력의 별도 영속화
- 판정 해시 이어하기·공정성 큐
- seed 전량 해시 opt-in
- SFTP 연결 stall·watchdog 방어(Unit 3 예정)
- mtime/CHANGED 폭주 임계치 경보(Unit 4 예정)
- `.filepart` 잔여 파일 인식·정리 보강(Unit 5 예정)
