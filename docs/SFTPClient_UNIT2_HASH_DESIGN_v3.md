# SFTPClient — 순서 2 설계안 v3 (초안 · 교차검증용): 해시 기반 변경 판정 + schema v6

- **작성일:** 2026-09-17 (v1 → v2: 1차 교차검증 11건 반영 → v3: 2차 교차검증 반영)
- **상태:** **초안.** 채택/기각은 제안과 근거이며 더 강한 근거로 뒤집을 수
  있다. §0 사실 기술은 HEAD 대조 결과로 반증 가능하다.
- **선행:** FOLLOWUP_PLAN §2.1·§2.2, 인시던트 §6·§7-3·§8, LEDGER_CONCEPT 4.9
- **v2 변경 요약:** ① 해시 안정성 검사(동일 핸들 전후 stat)와
  unstable/읽기실패 분리 ② 필수 해시 / 백필 이원화 ③ 연속 마이그레이션
  (v4 경로가 schemaVersion 상수에 묶여 있음을 실물 확인) ④ 실패 정책
  통일 원칙 ⑤ 예산 차감 근거 문구 정정 ⑥ UPDATE 전체 가드 + 0행 정책
  ⑦ content_hash CHECK ⑧ dry-run/seed 의미 확정 ⑨ config 부재≠0 함정
  ⑩ 지문 의미 문구 교체 ⑪ 계측·과도기 설명. (교차검증 지적 11건 전부 수용)
- **v3 변경 (2차 교차검증):** ① Hasher 를 사실 반환(HashResult)으로
  교체하고 불안정 판정을 Runner 로 이동 — 인터페이스가 스캔 관측치를
  받을 통로가 없다는 불일치 지적 수용 ② §0-7 재확인(v5 운영 DB 도
  거부되는 점 보강) ③ ALTER+CHECK 실측 확인(시스템 SQLite 3.45.1)으로
  fallback 폐기, modernc v1.57.0 최종 확인은 H1·H17 이 겸함.

---

## 0. 현황 `[확정 — 2026-09-17 HEAD 대조]`

v1 §0 의 1~6 유지. 이번 라운드 추가 확인:

7. **마이그레이션 분기는 상수에 묶여 있다.** `migrateIfNeeded` 는
   `got == "4" && schemaVersion == "5"` 단일 조건 — `schemaVersion` 을
   `"6"` 으로 올리면 v4 경로가 **소리 없이 죽고** `verifySchemaVersion`
   이 v4 DB 를 거부한다. 연속 전환은 명시적으로 넣어야 한다 (§2.3).
8. **schema.sql 은 마이그레이션보다 먼저 실행된다.** `CREATE TABLE IF NOT
   EXISTS` 이므로 신규 설치는 v6 정의로 태어나고, 기존 DB 에는 no-op 뒤
   마이그레이션이 컬럼을 추가한다 (기존 주석의 경고 관행 유지: 새 컬럼을
   쓰는 문장이 v4/v5 DB 에서도 실행 가능해야 한다).
9. **config 의 키 부재는 0 이다.** `intVal` 은 부재 시 0 을 돌려주고
   기본값 메커니즘이 없다. 부재를 기본값으로 살리려면 `value()` 의 ok
   플래그로 분기해야 한다 (전례: HourLayout — "키 부재가 정상이며
   Default 를 사용"). §3 의 함정 항목.
10. **seed 실물:** `--seed` 는 설치 초기화 전용, `SeedVerified` 로
    put_ledger 에 VERIFIED 행을 직접 등록(전송 없음). `--dry-run`/`--deep`
    과 상호 배제.

## 1. 판정 3분화 (v1 표 유지 + 실패·불안정 열 보강)

v1 §1 의 표와 "MetadataChangedOnly = 후보 관점 Unchanged 등가",
§1.1(mtime 의미 재정의), §1.2(지문 대상·형식)는 유지한다. 보강:

### 1.3 해시의 안정성 검사 `[v2 — 교차검증 ①]`

`SetContentHash` 의 SQL 가드는 **DB 행**의 불변만 보장한다. 해시 계산
**도중** 디스크 파일이 변하는 것은 별개의 사실이므로 Hasher 가 직접
검사한다:

```
파일 open → 동일 핸들 fstat(전) → SHA-256 스트리밍 → 동일 핸들 fstat(후)
→ 전후 size·mtime 동일할 때만 지문 채택, 다르면 ErrUnstable
```

- 스캔 시점 관측치(e.Size/e.MTime)와 fstat(전)의 대조도 수행한다 —
  스캔과 해시 사이에 이미 변했으면 그 관측 자체가 stale 이다.
- `ErrUnstable` 대신 **관측/판정 분리**: Hasher 는 사실만 돌려주고
  (전후 stat, 읽은 바이트), 불안정 여부의 **판정은 Runner** 가 한다 —
  스캔 관측치와의 대조까지 포함해서. 장부가 기록만 하고 판정하지 않는
  것과 같은 계열의 분리다. `[v3 — 2차 교차검증: 인터페이스가 스캔
  관측치를 받을 통로가 없다는 불일치 지적 수용]`
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
stale observation — §1.4 의 "회차 보류" 경로. err != nil 은 읽기 실패 —
§1.4 의 보수 경로.
- `[기각]` Hasher 가 스캔 관측치를 입력받아 스스로 판정 — 관측기가
  판정까지 소유하면 통계(불안정 수)의 주인이 둘이 되고, 판정 규칙이
  바뀔 때마다 인터페이스가 흔들린다. 사실 반환이 테스트 스텁도 단순하다.

### 1.4 실패 정책 통일 원칙 `[v2 — 교차검증 ①·④]`

> **해시 실패는 절대 전송을 막지 않는다 (누락 > 헛전송).
> 단, ErrUnstable 만은 회차 보류다 (작성 중 파일 취급).**

| 상황 | 판정 | 지문 | 비고 |
|---|---|---|---|
| 판정 해시(`size==·mtime≠`) 읽기 실패 | ContentChanged 간주 + WARN | 저장 안 함(`''` 유지) | 진짜 I/O 장애면 전송 단계가 FAILED 로 드러낸다 |
| 신규 최초 지문 읽기 실패 | New 그대로 등록·후보 + WARN | `''` | 등록·전송을 막지 않는다 |
| `size≠` 새 지문 읽기 실패 | ContentChanged 그대로 + WARN | `''` | size 가 이미 변경 증거 — 지문은 부가물 |
| 후보 필수 해시(§3) 읽기 실패 | 후보 유지 + WARN | `''` | 전송 예정대로 |
| 백필 해시 읽기 실패 | 건너뜀 + WARN | `''` | 예산 차감 (§3) |
| **불안정 관측 (Runner 판정: Pre≠Post 또는 Pre≠스캔 관측치, 모든 경로)** | **이번 회차 보류** — observed 미포함, 후보 미포함, rejected 계열 집계 | 저장 안 함 | 미완성 파일 보류와 동일 의미론. 세트 게이트가 미완성 세트를 보류하는 것은 의도된 동작 |

## 2. 스키마 v6 과 ledger API

### 2.1 스키마 `[v2 — CHECK 추가, 의미 문구 교체]`

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
- 신규 설치용 `CREATE TABLE` 정의에도 동일 컬럼·CHECK 를 넣는다 (§0-8).
- **ALTER + CHECK 동작 확인 `[v3]`:** 2차 교차검증이 시스템 SQLite
  3.45.1 에서 마이그레이션 경로 그대로(ALTER ADD COLUMN + NOT NULL
  DEFAULT '' + CHECK, 기존 행 '' 통과, 63자/대문자/비hex 차단) 실측
  성공 — v2 가 남긴 "불가하면 CREATE 쪽에만" fallback 은 폐기한다.
  `modernc.org/sqlite v1.57.0` 실물 최종 확인은 별도 수동 절차가 아니라
  **H1·H17 이 그 자체로 수행한다** — 유닛 테스트가 저장소 모듈(=modernc)
  위에서 마이그레이션 경로를 돌기 때문이다. (샌드박스 재검은 시도했으나
  v1.57.0 이 go ≥ 1.25 를 요구해 불가 — go 1.22 상한 환경.)

### 2.2 ledger API `[v2 — 전체 가드 + Applied 반환]`

| API | 계약 |
|---|---|
| `Known.ContentHash` | `''` = 지문 없음 |
| `UpsertCommon` + `CommonInput.ContentHash` | new/ContentChanged 경로가 지문 쓰기 주인. 해시 실패 시 `''` |
| `TouchCommonMTime(ctx, key, judged) (applied bool, err error)` | `UPDATE ... SET mtime=새값 WHERE category·file_name·revision·size·mtime·content_hash = 판정에 쓴 값 전부` — 판정 근거가 그대로일 때만 갱신 |
| `SetContentHash(ctx, key, judged, hash) (applied bool, err error)` | `WHERE ... AND content_hash=''` 포함 — "size=·mtime= 이면 내용 동일"의 안전 근거를 SQL 이 강제 |

**0행(applied=false) 정책 `[v2 — 교차검증 ⑥]`:** WARN 기록 + **해당
파일만 이번 회차 보류**(후보 제외).
- `[기각]` 조용히 무시 — 단일 실행 lock 아래에서 0행은 자기 코드의
  불변식 이상 신호다. 은폐하면 다음 장애가 또 추적 불능이 된다.
- `[기각]` 실행 중단 — 파일 하나의 이상으로 회차 전체(수천 파일)를
  멈추는 것은 과잉이다. 보류는 다음 회차 재관측으로 자기 교정된다.
- `[기각]` 재조회 후 재시도 — 단일 Writer 구조에서 그 사이 행을 바꿀
  주체가 자기 자신뿐이므로, 재조회가 성공한다면 그것이 곧 코드 결함의
  증거다. 결함 위에서 진행하지 않는다.

### 2.3 연속 마이그레이션 `[v2 — 교차검증 ③ + 실물 확인]`

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

- **필수:** 운영 3개 기관(전부 v5)의 `v5→v6` 전환 + 데이터 보존.
  2차 교차검증이 정확히 짚었듯, 새 분기가 없으면 v4 만이 아니라
  **v5(현 운영 DB 전부)가 아무 분기도 못 타 verifySchemaVersion 에서
  거부**된다 — v5→v6 분기는 이번 배포의 성립 조건 그 자체다.
- **포함(호환):** `v4→v5→v6` 연속 — §0-7 확인 결과 기존 함수 순차 호출
  수 행 수준이므로 이번 유닛에 포함한다. 빼면 schemaVersion 상승만으로
  v4 경로가 죽는 회귀가 생긴다 (오프라인·Linux 일괄 상향 대비).
- `migrateV5toV6` 는 전례(단일 트랜잭션: ALTER + 버전 갱신, 실패 시
  온전한 v5 로 롤백)를 따른다. **행 백필 없음** — 지문은 파일 바이트를
  읽어야 하는 값이라 DB 트랜잭션의 일이 아니다.

## 3. 필수 해시와 백필의 이원화 `[v2 — 교차검증 ②]`

v1 은 "Unchanged + 지문 없음 = 전부 백필 예산 대상"으로 읽혔다. 정정:

| 구분 | 대상 | 예산 |
|---|---|---|
| **필수 해시** | 신규 최초 지문 / ContentChanged 새 지문 / **Unchanged 인데 현재 revision 이 전송 후보**(PutStatus `''`·PENDING·재시도 가능 FAILED)이며 지문 없음 | **무관** — 방어의 본체. 지금 보낼 바이트의 기준 지문을 남기지 않으면, 전송 직후의 드리프트(이번 인시던트의 7일 롤링이 정확히 이 패턴)가 방금 보낸 파일을 재전송시킨다 |
| **백필** | Unchanged + 비후보(VERIFIED 등) + 지문 없음 | `MaxHashBackfillPerRun` 적용 |

- 필수 해시 비용의 상한: 후보 수 자체가 정상 운영에서 작고, 폭주 시엔
  `MaxFilesPerRun` 절단 이전이라도 후보군 규모에 비례할 뿐이다(§6 계측).
- **예산 차감 근거 정정 `[교차검증 ⑤ — v1 문구 오류]`:** 백필 실패 시
  예산을 차감하는 이유는 "반복 잠식 방지"가 **아니다** — 실패해도 지문이
  비어 있으므로 다음 회차에 다시 시도되는 것이 맞다. 차감의 근거는
  "실패한 시도도 실제 읽기 비용을 냈으므로 이번 회차 예산을 소비한다"
  이며, 실패 이력 관리는 본 유닛 범위 밖이다.

### 3.1 `MaxHashBackfillPerRun` 의 4치 의미 `[v2 — 교차검증 ⑨, 함정 명시]`

| 입력 | 의미 |
|---|---|
| **키 부재** | **500 (기본)** — 실행파일만 교체한 기존 현장 config 에서 백필이 즉시 작동해야 한다 |
| `0` | 백필 끔 |
| 양수 | 해당 상한 |
| 음수 | validate 오류 |

**구현 함정:** `intVal` 은 부재를 0 으로 돌려준다 (§0-9). 그대로 쓰면
"키 없음 = 백필 끔"이 되어 **기존 현장 전부에서 백필이 조용히 꺼진다.**
반드시 `value()` 의 ok 플래그로 부재/0 을 분리한다 (HourLayout 전례).
`knownKeys` 갱신, config.example.ini 에 4치 의미 주석.

## 4. dry-run 과 seed `[v2 — 교차검증 ⑧]`

**dry-run:**
- 판정 해시를 **실제로 계산한다** (읽기 전용이므로 안전) — changed /
  metadata_only 분류가 live 와 동일하게 보고되어야 dry-run 의 존재
  이유("무엇을 전송할 것인가"의 예고)가 성립한다.
- 쓰기는 전무: Touch·지문 저장·Upsert 모두 없음 (기존 dry-run 계약 유지).
- **백필은 수행도 보고도 하지 않는다.** `[기각]` would_backfill 표시 —
  dry-run 의 질문은 전송 계획이지 정비 작업 목록이 아니며, 백필은 판정
  결과에 영향을 주지 않는다.

**seed:**
- **seed 는 지문을 저장하지 않는다.** 지문은 자연 백필의 몫이다.
- `[기각]` seed 시 전량 해시 — seed 는 현장 설치 절차 중 1회 유인
  실행이며, 11만 행 × 평균 수 MB ≈ 수십~수백 GB 읽기가 설치 체류시간을
  인질로 잡는다. 과도기 창(§6)은 어차피 존재하고 백필이 ~10일에 닫는다.
- 재론 조건: 설치 즉시 전량 방어를 요구하는 기관이 생기면 opt-in
  플래그로 별도 논의.
- SeedVerified(put_ledger)와 content_hash(common_ledger)는 서로 다른
  사실의 주인이므로 상호 참조하지 않는다.

## 5. DOWNLOAD-origin 분기 미적용 — v1 §5 유지 (재론 조건 포함)

## 6. 계측·과도기·운영 영향 `[v2 — 교차검증 ⑪]`

- **판정 해시에 상한을 두지 않는다.** `[기각]` 상한 초과분 ContentChanged
  처리 — 방어 목적 자체가 무너진다. `[기각]` 다음 회차 연기 — 이어하기·
  공정성 설계가 유닛 범위를 넘고, 실변경 전송이 지연된다. 대신 계측으로
  관리한다.
- **집계(리포트 + 로그 요약):** 해시 원인 4분류(신규 기준 / 변경 새 지문
  / 드리프트 판정 / 백필) 각각의 **건수·바이트·소요시간**, 실패 수,
  unstable 수, 백필 유예(예산 소진) 수. metadata_only 는 v1 §6 대로.
- **최악 부하 산식(단정 수치 대신):**
  `총 읽기량 = 판정 대상 크기 합, 예상 시간 ≈ 총 읽기량 / 실측 순차 읽기
  처리량`. 실측은 배포 전 대상 서버에서 1회 기록한다.
- **lock 겹침:** 장시간 해시로 다음 회차와 겹치면 후속 실행은
  `lock.ErrHeld` 로 **정상 종료(0)** 한다 — 기존 설계가 이미 안전하게
  처리하므로 추가 장치 없음. 계측 로그가 장기화 추세를 드러낸다.
- **과도기 명시:** v6 배포 직후 기존 행은 지문이 없다. 자연 백필이 닫히기
  전(기본값 기준 ~10일)에 mtime 폭풍이 재발하면 지문 없는 행은 표 §1
  4행에 따라 **1회 보수 재전송**되며(그때 지문이 채워진다), 완전한 억제는
  기준 지문이 채워진 이후 작동한다. "배포 즉시 완전 방어"가 아님을 운영
  커뮤니케이션에 포함한다.

## 7. 테스트 계획 (v1 H1~H9 유지 + 추가)

| # | 계약 |
|---|---|
| H10 | 연속 마이그레이션: v4 픽스처 → Open → v6 (set_key 백필 + content_hash `''` + 버전 '6') / v5→v6 / v6 무변경 |
| H11 | Hasher 관측/판정 분리: Pre≠Post 및 Pre≠스캔 관측치 각각 → 회차 보류(observed·후보 미포함). HashResult.Bytes 계측 일치 |
| H12 | 필수/백필 이원화: 후보(이력 없음)+지문 없음 → 예산 0 이어도 지문 저장 / 비후보(VERIFIED)+지문 없음 → 예산 소진 시 유예 |
| H13 | Touch/SetContentHash 전체 가드 + applied=false → WARN + 해당 파일 보류 |
| H14 | config 4치: 키 부재→500 / 0→끔 / 음수→validate 오류 |
| H15 | dry-run: 판정 해시 수행·분류 동일·쓰기 0건·백필 미수행 |
| H16 | seed: content_hash 미기록 |
| H17 | CHECK: 63자/대문자/비hex 삽입 시도 → 제약 위반. 마이그레이션 경로 포함 — 이 테스트가 modernc.org/sqlite v1.57.0 실물 검증을 겸한다 (§2.1) |

## 8. 커밋 계획 (논리 단위 4개 — v1 유지, 내용 갱신)

1. `ledger: schema v6 — content_hash(+CHECK) / v5→v6 + v4 연속 마이그레이션 / Known·Upsert 반영`
2. `ledger: TouchCommonMTime / SetContentHash — 판정 근거 전체 가드, applied 반환` (+테스트)
3. `config: [PUT] MaxHashBackfillPerRun — 부재=500·0=off·음수 거부 (intVal 함정 회피)`
4. `put: 판정 3분화 + Hasher(안정성 검사) + 필수/백필 이원화 + 계측` (+테스트)

## 9. 이 유닛이 하지 않는 것 (v1 유지 + 추가)

- v1 §9 전부 유지
- 백필 실패 이력 관리 (교차검증 ⑤ — 별도 판단 사항)
- 판정 해시 상한·이어하기 (§6 에서 기각, 계측으로 대체)
- seed 전량 해시 opt-in (§4 재론 조건으로만 예약)
