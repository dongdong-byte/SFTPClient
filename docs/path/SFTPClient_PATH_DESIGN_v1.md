# SFTPClient MVP2 — 경로 범용화 설계 v1 (초안)

> 작성 2026-09-22. MVP2 실행 범위 ③ 「`HourLayout` 제거와 범위 제한 재귀 탐색 기반
> 경로 범용화」의 구현 전 설계 초안이다.
> 상위 기준은 [PROJECT_GUIDELINES](../../SFTPClient_PROJECT_GUIDELINES.md)의
> 「MVP2 현재 실행 범위」와 [SCAN_DESIGN_DECISIONS](../SFTPClient_SCAN_DESIGN_DECISIONS.md),
> 원 설계는 `Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx` 8.2·9절이다.
>
> 표기: **[확정]** 사용자 확인 완료 / **[제안]** 초안 권고, 확인 필요 /
> **[미결]** 이번 초안에서 결정하지 않음.
>
> 이 문서는 개요 단계의 초안이다. 이 문서로 기존 문서(GUIDELINES, schema.sql,
> LEDGER_CONCEPT 등)를 아직 개정하지 않는다. 개정 대상은 §14에 모아 둔다.

---

## 0. 한 줄 요약

수신기(또는 다른 프로그램)가 기관마다 제각각인 폴더에 쌓아 둔 파일을
**재귀로 찾아 우리가 정한 표준 폴더로 복사(수신, DOWNLOAD)** 하고,
표준 폴더에서 **기존 PUT 흐름으로 송신** 한다.
**download·put·common 세 관측자**가 각 구간을 검증하고, 사실마다 기록의 주인은
하나로 둔다. 경로에 담긴 site·시각·DOY 유무는 무시하고 **파일명만 믿는다.**

```
원본 폴더(기관마다 다름)
  │  [download] 재귀 탐색 → Ingress → .part 복사 → Size·해시 검증 → Rename
  ▼
표준 폴더(우리가 정한 구조)
  │  [put] 기존 PUT 흐름 (.part 업로드 → Size 검증 → Rename)
  ▼
원격 SFTP

[common] 회차 끝에 download 장부 ↔ put 장부 대사(reconciliation)
```

---

## 1. 배경

### 1.1 `HourLayout`은 과도기였다

`HourLayout=dir/flat`과 Hourly `(HH)` 00~23 계산은 서울시의 평면 Hourly 경로를
급히 지원하려고 2026-09-04 추가한 과도기 설정이다(`internal/config/hourlayout.go`).
MVP2에서 제거하고 범위 제한 재귀 탐색으로 바꾸기로 2026-09-15 확정했다.

### 1.2 현장 경로는 규칙이 없다

`GNSS Server&Client 관련 정보 - 260527.docx`에 적힌 기관별 RINEX 경로다.

| 기관 | 경로 예 | 특징 |
|---|---|---|
| 기상위성센터 | `/vol03/.../RINEX/Hourly/2017/142/00/BOSN142a.17O` | (HH) 폴더 있음 |
| 지질자원연 | `/gnssdata/KIGAM/2018/281/HDBG/1s1h/hdbg281a.zip` | DOY 아래 관측소·주기 폴더 |
| 천문연 | `/gdcdata/pub/kasinet/daily/2019/002/19o/bhao0020.19o` | DOY 아래 파일종류 폴더 |
| 우주전파센터 | `/occ/data/gps/temp/2018/0829/SWIC241h.18o` | DOY 없음, MMDD |
| 공간정보연 | `E:\RINEX\1Hour\CHND\2018\05\18\chnd138b.18o.Z` | 관측소 폴더가 날짜 **앞** |
| 서울시 | `C:\RINEX-V2-H\2020\006\DBON006a.20o.Z` | 평면 Hourly |

공통점은 하나다. **관측일·시각(세션)은 모두 파일명에 있다.**

### 1.3 방식 결정 경위

| 안 | 내용 | 결과 |
|---|---|---|
| A. 재귀 직접 PUT | 원본 루트를 재귀로 찾아 파일명으로 판정하고 바로 PUT | 기술적으로 가장 단순. **채택하지 않음** |
| B. 표준 폴더 수신 후 PUT | 원본을 표준 폴더로 복사(수신)한 뒤 표준 폴더에서 PUT | **[확정] 대표 지시로 채택** |

B는 복사가 한 번 더 들어가지만 다음을 얻는다.

- 기관별 차이를 **수신 단계 하나에 가둔다.** PUT·resend·scanwindow는 표준 폴더만 본다.
- Rev1.6의 **3장부 구조(Common·PUT·DOWNLOAD)** 가 원 설계대로 성립한다.
- 대표 계획인 **동료 프로그램(타 서버에서 받아 폴더에 넣는 프로그램) 흡수** 시
  원격 수신을 같은 download 장부·같은 검증 절차에 붙일 수 있다(§11).

### 1.4 "DOWNLOAD"의 의미 재정의 [확정]

Rev1.6의 DOWNLOAD 장부는 **수신기에서 데이터를 받은 사실**을 기록하는 장부였다.
현장 수신기는 스스로 특정 폴더로 파일을 내보내므로, 그 폴더에서 표준 폴더로
가져오는 로컬 → 로컬 수신이 원래 의도에 가장 부합한다.

- `download` = **우리가 관리하는 표준 폴더로 파일을 들여오는 행위.** 출처는 로컬 폴더
  (이번 범위) 또는 원격 서버(동료 프로그램 흡수 시)다.
- 현재 코드의 "DOWNLOAD = 원격 SFTP 수신, PUT 후보에서 제외"라는 의미와 충돌한다.
  정리는 §10.

---

## 2. 설계 원칙

1. **경로는 무시하고 파일명만 믿는다.** [확정] 탐색 경로의 site·시각·DOY 구조를
   해석하지 않는다. Category(주기)·관측일·세션은 파일명에서 얻는다.
2. **관측자는 셋, 사실의 주인은 하나씩.** [확정] 각 관측자는 자기 구간을 직접
   확인한다(이중 검사). 같은 사실을 둘 이상이 고쳐 쓰지 않는다(Rev1.6 9절
   "하나의 사실에는 하나의 주인"). 특히 **revision은 download만 올린다.**
3. **장치는 그대로, 설계만 가볍게.** [확정] 로컬 수신이라 접속·재연결·멈춤 감시·
   병렬 Worker는 두지 않는다. Ingress·`.part`·Size 검증·크래시 회수·장부 기록은
   PUT과 같은 수준으로 유지한다. `.part`는 네트워크가 아니라 **복사 도중 프로세스
   종료(재부팅·서비스 중지·정전·디스크 가득 참)** 에 대한 장치다.
4. **복사가 기본이다.** [확정] 하드링크는 쓰지 않는다. Size 검증이 형식에 그치고
   Rev1.6 8.2 절차와 맞지 않는다. 용량은 운영(대표) 판단 사항이다.
5. **원본은 읽기만 한다.** [확정] 원본을 이동·삭제·수정하지 않는다. 원본 폴더는
   기존 RinexServer·NOPS·기관 업무가 함께 쓴다.

---

## 3. 전체 흐름 (1회차)

```
시작
 ├─ lock 획득 (기존)
 ├─ Recover: download IN_PROGRESS → FAILED, 표준 폴더 잔여 .part 삭제   (신규)
 ├─ Recover: put IN_PROGRESS → FAILED, 원격 잔여 .part 정리             (기존)
 ├─ ① download : 원본 → 표준 폴더                                       (신규)
 ├─ ② put      : 표준 폴더 → 원격                                       (기존, 관측 규칙 변경)
 ├─ ③ resend   : 자동 resend (기존). 창에 대해 download 선행 (§9.3)
 ├─ ④ audit    : common 대사                                           (신규)
 ├─ Retention Cleanup (Deep 회차)                                       (MVP2 기존 계획)
 └─ lock 해제
```

- 같은 프로세스·같은 lock 안에서 **download가 끝난 뒤 put** 이 돈다. [제안]
- download 단계의 오류는 put을 막지 않는다. put은 download VERIFIED인 것만
  보내므로 반쪽 수신분이 새어 나가지 않는다(§7.2).

---

## 4. 세 관측자

| 관측자 | 보는 대상 | 질문 | 기록하는 사실(주인) | 어긋나면 |
|---|---|---|---|---|
| **download** | 원본 ↔ 표준 폴더 | 잘 받았는가 | common 행(정체성·revision·size·mtime·content_hash), download_ledger | 다시 복사 |
| **put** | 표준 폴더 ↔ 원격 | 잘 보냈는가 | put_ledger | **revision을 올리지 않고** 전송 보류 + 경고 |
| **common(대사)** | download_ledger ↔ put_ledger | 받은 것을 제대로 보냈는가 | 대사 결과(로그) | 보고. 필요 시 재수신·재전송 표시(§8.3) |

### 4.1 common 관측자는 파일을 보지 않는다 [확정]

common이 원본 파일을 다시 보면 download와 같은 일을 두 번 하게 되고, 두 관측 사이에
파일이 바뀌면 오히려 불일치를 만든다. common의 관측 대상은 **두 장부**다.
입금 장부와 출금 장부를 맞춰 보는 대사와 같다.

### 4.2 common 행의 작성자는 download다 [제안]

현재는 PUT Scan이 common을 갱신한다(Scan → Lookup → Verify → Upsert). B안에서는
그 역할을 download로 옮긴다. PUT은 common을 **읽기만** 한다.

- 이유: revision의 기준은 **수신기가 만든 원본**이어야 한다. 표준 폴더는 우리가 만든
  사본이라 기준이 될 수 없다.
- 기존 해시 기반 변경 판정(v9, UNIT2 v3)은 그대로 download로 옮긴다.
  size·mtime 비교 → size 같고 mtime만 다르면 content_hash 대조 → 같으면 revision
  유지, mtime 기준선만 이동.

---

## 5. download (로컬 수신)

### 5.1 탐색

- 설정한 **원본 루트 아래를 재귀 탐색**한다. [확정]
- 최대 깊이를 둔다. 기본값은 **[미결]**. 현장 경로 중 가장 깊은 것은 지질자원연
  (`YYYY/DOY/SITE/1s1h/` = 루트 아래 4단)이다.
- symbolic link / junction은 **따라가지 않는다.** [제안] 순환·루트 이탈 방지.
- 권한 오류 디렉터리는 기존 `scan.DirError`처럼 기록하고 계속한다. [제안]
- 탐색은 순차다(기존 "Scan은 순차" 원칙).
- **표준 폴더와 DOWNLOAD 대상 폴더가 원본 루트 안에 들어가면 시작 시 거부**한다.
  [제안] 자기 사본을 다시 수신하는 루프를 막는다.

### 5.2 판정 — 파일명만 본다

| 항목 | 출처 | 비고 |
|---|---|---|
| RINEX 버전 | **원본 루트 설정** (§12) | RINEX3·RINEX4는 파일명이 같아 파일명으로 구분 불가(schema.sql v7) |
| 주기(Daily/Hourly) | 파일명 | RINEX2 세션 문자 `0`/`a~x`, RINEX3/4 `_01D_`/`_01H_` |
| 관측일(UTC) | 파일명 | RINEX2 `DDD`+`YY`, RINEX3/4 `YYYYDDDHHMM` |
| Category | 버전 + 주기 | 기존 6개 값 그대로 |

- 기존 `verify`는 "Category는 config가 지정한 기대값이고 파일에서 추론하지 않는다"는
  전제다. B안에서는 **주기를 파일명에서 추론**하므로 이 전제가 바뀐다.
  파일명으로 주기·관측일을 확정할 수 없는 파일은 **수신하지 않고 집계·로그**만
  남긴다(`unknown=`). [제안]
- Ingress 판정(Grace, `.part`/`.filepart`, 0바이트, 미래 mtime)은 기존
  `verify.Verifier`를 그대로 쓴다. [확정]

### 5.3 날짜 창

- 원본에는 10년치가 있다. **파일명 관측일이 창 안인 것만** 수신한다. [확정]
- 창은 PUT과 같다: Hot = `ScanRecentDays`, Deep = `ScanDays`,
  resend = `internal/scanwindow` 계산 결과.
- 불변식 "**탐색 범위 ⊂ 장부 기억 범위**"(resend v5 §3)를 수신 쪽에도 적용한다.
  창 밖 파일을 수신하면 Retention이 지운 행이 신규로 되살아난다.

### 5.4 복사 절차 (Rev1.6 8.2의 로컬 판)

```
1. 원본 stat (size, mtime)                 ← Ingress 판정 입력
2. download_ledger PENDING → IN_PROGRESS   (part_path 기록, 같은 트랜잭션)
3. 표준 폴더에 <name>.part 로 복사하며 SHA-256 계산
4. .part size == 원본 size 확인
5. fsync 후 최종 이름으로 Rename           ← 같은 폴더 안이라 원자적
6. 최종 파일 존재·size 확인
7. download_ledger VERIFIED (staged_size, content_hash, 시각)
```

- 임시 파일을 **대상 폴더 안에** 만들어 원본과 다른 드라이브여도 Rename이
  같은 볼륨 안에서 일어나게 한다. [확정]
- 복사 중 원본 size·mtime이 바뀌면(아직 쓰는 중) 이번 회차는 FAILED로 두고 다음
  회차에 다시 판정한다. [제안]
- 해시는 복사하며 읽는 바이트로 계산하므로 추가 I/O가 없다. Rev1.6이 해시를
  선택 옵션으로 둔 이유(I/O 비용)가 로컬 수신에서는 사라진다. **기본 ON** [제안]

### 5.5 이미 받은 파일

| 상태 | 처리 |
|---|---|
| common 행 없음 | 신규. revision 1로 수신 |
| common 있음, 원본 size/mtime/해시 동일, download VERIFIED(R), 표준 폴더 파일 정상 | 건너뜀 |
| 위와 같으나 표준 폴더 파일이 없거나 size가 다름 | 같은 revision으로 **다시 복사** (download FAILED → 재시도) |
| 원본이 바뀜 (기존 변경 판정 규칙) | common revision R+1, download R+1 수신 |
| 원본 트리 안에 같은 file_name이 둘 이상 | 내용 같으면 하나만 수신. 다르면 **둘 다 보류 + 경고** [제안] |

---

## 6. 표준 폴더

- 경로는 우리가 정하므로 PUT 쪽 `LocalPath`는 **고정 템플릿**이 된다.
  `HourLayout`과 `(HH)` 00~23 계산은 필요 없다. Hourly도 평면이다. [확정]
- 구조 이름은 서울시·측위원과 비슷하게 둔다. **[미결]** 초안:

```
<StageRoot>\RINEX-V2-D\(YYYY)\(DOY)\
<StageRoot>\RINEX-V2-H\(YYYY)\(DOY)\
<StageRoot>\RINEX-V3-D\(YYYY)\(DOY)\
<StageRoot>\RINEX-V3-H\(YYYY)\(DOY)\
<StageRoot>\RINEX-V4-D\(YYYY)\(DOY)\
<StageRoot>\RINEX-V4-H\(YYYY)\(DOY)\
```

- `(YYYY)`·`(DOY)` 는 **파일명 관측일**로 채운다(원본 경로와 무관).
- 표준 폴더 경로는 (category, file_name)만으로 항상 계산된다. 장부에 저장하지 않는다.
- 운영자가 표준 폴더를 손으로 고치는 것을 전제하지 않는다. 고쳐지면 put 관측과
  대사가 잡는다(§7.1, §8).

---

## 7. put (송신)

### 7.1 관측 규칙 변경

- put은 **표준 폴더 파일을 직접 관측**한다: size가 common.size(R)와 같은지,
  (해시 ON이면) content_hash가 common.content_hash(R)와 같은지. [제안]
- 다르면 **revision을 올리지 않는다.** 전송을 보류하고 `[PUT][WARN]` 경고를 남긴다.
  고치는 일은 주인인 download가 다음 회차에 한다(§5.5 재복사).
- 그 밖의 PUT 절차(.part 업로드 → Size 검증 → Rename → 존재 확인 → VERIFIED),
  세트 게이트, MaxRetries, MaxFilesPerRun, 정렬은 그대로 둔다.

### 7.2 후보 선정 — 두 가지 방식 [미결]

| 방식 | 내용 | 장점 | 단점 |
|---|---|---|---|
| **P1. Scan 주도 유지** | 표준 폴더를 기존 Scanner로 나열하고, 장부 조회는 **읽기 전용**. 후보 조건에 download VERIFIED(R) 추가 | 기존 PUT·resend·scanwindow 코드 변경 최소 | 표준 폴더를 한 번 더 나열 |
| **P2. 장부 주도** | 후보 = download VERIFIED(R) ∧ put 미VERIFIED(R) ∧ 관측일 ∈ 창 | 나열 불필요, 불변식 1을 구조로 보장 | v5에서 버린 DB 주도로 복귀, 변경 폭 큼 |

- v5가 DB 주도를 버린 이유는 "기관 경로가 자주 바뀌어 local_path를 믿을 수 없다"였다
  (schema.sql [v5 개정 주석]). 표준 폴더 경로는 우리 규칙으로 계산되므로 그 이유는
  B안에서 사라진다. 그래도 **MVP2에서는 P1을 권고**한다. [제안] 변경 폭이 작고,
  resend v5에서 검증한 경로를 그대로 쓴다. P2는 동료 프로그램 흡수 시 재검토한다.
- 어느 방식이든 **download VERIFIED(R)가 아니면 보내지 않는다**(불변식 1).

---

## 8. common 대사 (audit)

### 8.1 불변식

| # | 불변식 |
|---|---|
| I1 | **받지 않은 것은 보내지 않는다.** put VERIFIED(R) ⇒ download VERIFIED(R) |
| I2 | **크기가 한 줄로 이어진다.** common.size(R) = download.staged_size(R) = put.local_size(R) = put.remote_size(R) |
| I3 | **해시가 이어진다(해시 ON).** common.content_hash(R) = download.content_hash(R) |
| I4 | **revision은 한 방향.** put·download의 revision ≤ common.revision. 뒤 단계가 먼저 올리지 않는다 |
| I5 | **정리는 common 한 곳에서.** common 행 삭제 시 download·put 행 CASCADE |

### 8.2 대사 검사

회차 끝에 창(Deep 범위) 안의 행에 대해 SQL 조인으로 검사한다. 파일을 읽지 않는다.

| 검사 | 위반 예 | 수준 |
|---|---|---|
| I1 | put VERIFIED(R)인데 download VERIFIED(R) 없음 | ERROR |
| I2 | 크기 사슬 중 하나가 다름 | ERROR |
| I3 | 해시 불일치 | ERROR |
| I4 | put.revision > common.revision | ERROR |
| 적체 | download VERIFIED(R) 후 일정 시간 넘게 put 없음 | WARN |
| 수신 정체 | download FAILED가 MaxRetries 도달 | WARN |

- 결과는 `[AUDIT]` 요약 한 줄 + 위반 상세(상한 N건) 로그. [제안]
- 대사는 **보고만** 하고 장부를 고치지 않는다. [제안] 자동 수정은 대사가 기록의
  주인이 되는 것이라 원칙 2에 어긋난다. 필요한 재수신·재전송은 각 주인의 다음
  회차 판정이 처리한다.

### 8.3 불일치 처리 표

| 발견 | 누가 고치나 | 어떻게 |
|---|---|---|
| download VERIFIED인데 표준 폴더 파일 없음 | download | FAILED로 되돌리고 재복사 (§5.5) |
| 표준 폴더 파일 size/해시가 common과 다름 | put 보류, download 재복사 | §7.1, §5.5 |
| put은 있는데 download 없음 (I1) | 운영자 | 대사 ERROR. 기존 설치처 전환 직후는 §13.2 예외 |
| 원본이 바뀜 | download | common R+1 → download R+1 → put R+1 |

---

## 9. 장부

### 9.1 download_ledger 초안 [제안]

put_ledger와 같은 모양으로 둔다. schema.sql의 `[예정] download_ledger (MVP3)`를
앞당긴다.

| 컬럼 | 의미 |
|---|---|
| category, file_name, revision | PK. (category, file_name) → common FK, ON DELETE CASCADE |
| status | PENDING / IN_PROGRESS / VERIFIED / FAILED (domain 상수 재사용) |
| attempts | 누적 시도 횟수. MaxRetries 적용 |
| source_kind | `LOCAL` / `REMOTE` (§11). 이번 범위는 LOCAL만 |
| source_path | 수신한 원본 경로. **과거 사실의 기록**이다(put_ledger.remote_path와 같은 성격). 판정에 쓰지 않는다 |
| source_size | 복사 직전 원본 size |
| staged_size | 복사 후 표준 폴더 파일 size |
| content_hash | 복사하며 계산한 SHA-256 |
| part_path | 사용한 `.part` 경로. IN_PROGRESS 전환 시 같은 트랜잭션에서 기록 (크래시 회수용) |
| received_at, receive_verified_at | 복사 완료 시각, 검증 통과 시각 |
| error | 최근 실패 원인 |

- `source_path`는 v5가 지운 `local_path`와 다르다. local_path는 "지금 어디 있나"라는
  **현재 상태의 사본**이라 어긋날 수 있었고, source_path는 "그때 어디서 받았나"라는
  **변하지 않는 과거 사실**이다. 판정에 쓰지 않으므로 경로가 바뀌어도 중복 수신이
  생기지 않는다.
- schema_version 6 → 7, `migrateV6toV7`로 자동 마이그레이션(테이블 추가). [제안]

### 9.2 크래시 회수

- 시작 시 download IN_PROGRESS → FAILED, part_path의 표준 폴더 잔여 `.part` 삭제.
  PUT `Recover`와 같은 방식. [제안]

### 9.3 resend와의 관계

- 자동·수동 resend는 창에 대해 **download를 먼저** 실행한 뒤 put을 실행한다. [제안]
  표준 폴더를 정리했더라도 원본에 파일이 있으면 재수신 후 재전송할 수 있다.
- resend 창 한계(Retention·운영 시작일, `internal/scanwindow`)는 download에도
  같은 함수로 적용한다.

### 9.4 Retention

- 장부: 기존 계획대로 common 기준 삭제 + CASCADE (download 행 포함).
- 표준 폴더 파일: 별도 정리 필요. **[미결]** 기준안 — 관측일이 `RetentionDays`를
  넘었고 put VERIFIED(현재 R)인 파일만 삭제. 대사는 창 밖 행을 검사하지 않으므로
  "download VERIFIED인데 파일 없음" 오탐이 생기지 않는다.

---

## 10. Origin과 Ping-Pong 규칙 정리

현재 규칙(`internal/domain/origin.go`, schema.sql common_ledger.origin):

- `LOCAL` — 이 서버에 원래 있던 파일(수신기·BNC·타 프로세스)
- `DOWNLOAD` — 이 프로그램이 원격 서버에서 받은 파일. **PUT 후보에서 기본 제외**
  (Ping-Pong 방지), `RepostDownloaded`로만 예외

B안에서의 정리 [제안]:

- 로컬 수신 파일의 origin은 **`LOCAL` 그대로**다. 출처는 여전히 수신기다.
  "download 장부에 행이 있다"와 "origin=DOWNLOAD"는 다른 사실이다.
- `origin=DOWNLOAD`와 Ping-Pong 제외는 **원격 수신(§11)에만** 쓴다. 그중에서도
  PUT 목적지와 같은 서버에서 받은 경우가 Ping-Pong이다. 동료 프로그램처럼 **다른
  서버에서 받아 중계**하는 구성은 `RepostDownloaded=true`가 정상 운영이 된다.
- 이 정리는 schema.sql·origin.go 주석 개정이 필요하다(§14).

---

## 11. 동료 프로그램 흡수 대비 (이번 범위 밖)

대표 계획은 타 서버에서 받아 폴더에 넣는 동료 프로그램을 이 프로그램으로 흡수하는
것이다. 그때 원격 수신이 추가된다.

```
download_ledger (수신 사실의 주인 — 하나)
  ├─ source_kind = LOCAL   ← 이번 경로 범용화 (가벼움)
  └─ source_kind = REMOTE  ← 흡수 시 (Rev1.6 DOWNLOAD: 접속·멈춤 감시·병렬 필요)
```

- 장부, `.part → Size → Rename → 기록` 절차, 대사는 두 출처가 공유한다.
  흡수 시 **원격에서 읽는 부분만 추가**한다.
- 원격 수신에만 필요한 것: 접속·재연결, 멈춤 감시(유닛 3), 병렬 Worker, 원격 권한.

---

## 12. config 초안 [제안]

```ini
[GENERAL]
Mode = put                ; 로컬 수신은 put 앞 단계로 본다. download/both 는 원격용으로 유보 [미결]

[DOWNLOAD]
StageRoot   = D:\RINEX-STAGE        ; 표준 폴더 루트
MaxDepth    = 6                     ; 재귀 최대 깊이 [미결]
VerifyHash  = true                  ; 복사 중 SHA-256 (§5.4)

[DOWNLOAD.SOURCE.RINEX2]
SourceRoot  = /gnssdata/KIGAM       ; 원본 루트. 버전은 섹션이 정한다(§5.2)

[DOWNLOAD.SOURCE.RINEX3]
SourceRoot  = /gnssdata/KIGAM_V3

[PUT.RINEX2_DAILY]
LocalPath  = D:\RINEX-STAGE\RINEX-V2-D\(YYYY)\(DOY)\   ; 고정. StageRoot 에서 유도 가능 [미결]
RemotePath = /RINEX2Outgoing/Daily/(YYYY)/(DOY)/
```

- `HourLayout` 키는 **제거**한다. 남아 있으면 시작 시 "제거된 키" 오류로 알린다. [제안]
- 시작 시 거부: SourceRoot가 StageRoot를 포함하거나 그 반대인 경우, SourceRoot 간
  중첩, 같은 버전 섹션 중복. [제안]

---

## 13. 기존 설치처 전환

### 13.1 첫 회차 동작

기존 설치처(서울시·지리원·측위원)는 common·put 장부가 이미 있고 download 장부는 없다.

- 원본은 그대로이므로 download가 원본을 관측하면 size·mtime·해시가 기존 common과
  같다 → **revision이 오르지 않는다.**
- download는 창 안 파일을 표준 폴더로 복사하고 download VERIFIED(R)을 기록한다.
- put은 put VERIFIED(R)이 이미 있으므로 **다시 보내지 않는다.**
- 결과: 창 크기만큼 한 번 복사가 일어나고 재전송은 없다.

### 13.2 대사 예외

전환 전 put VERIFIED 행 중 창 밖이라 download 행이 생기지 않는 것은 I1 위반처럼
보인다. download 운영 시작일(`schema_meta.download_origin` 같은 값)보다 먼저 put된
행은 I1 검사에서 제외한다. [제안] resend의 `operation_origin`과 같은 방식이다.

---

## 14. 개정이 필요한 기존 문서·코드 (이 초안에서는 수정하지 않음)

| 대상 | 내용 |
|---|---|
| `SFTPClient_PROJECT_GUIDELINES.md` | MVP2 범위에 로컬 수신(download 장부 앞당김) 추가, MVP3 DOWNLOAD 정의 갱신 |
| `.cursor/rules/project-core.mdc` | 흐름도(download → put → audit), MVP3 정의 |
| `internal/ledger/schema.sql` | download_ledger 추가, origin 주석, v7 마이그레이션 |
| `docs/SFTPClient_LEDGER_CONCEPT.md` | 3관측자·대사·불변식 I1~I5 |
| `docs/SFTPClient_SCAN_DESIGN_DECISIONS.md` | 경로 방향을 표준 폴더 수신으로 갱신 |
| `internal/config/hourlayout.go` 외 | `HourLayout` 제거 |
| `internal/verify` | 파일명 기반 주기 판정 추가(현재는 config 기대값 전제) |
| `internal/domain/origin.go` | origin 의미 정리(§10) |
| `config.example.ini` | §12 반영 |
| 메시지 문서 | `[DOWNLOAD]`·`[AUDIT]` 메시지·종료 코드 |

---

## 15. 구현 순서 (잠정)

| # | 커밋 | 비고 |
|---|---|---|
| 1 | domain: 파일명 → 주기·관측일 판정 | 순수 함수, 표 기반 테스트 |
| 2 | ledger: download_ledger 스키마·v7 마이그레이션·CRUD | 호출처 없음 |
| 3 | download: 재귀 탐색(깊이·symlink·권한 오류) | 호출처 없음 |
| 4 | download: 복사(.part·Size·해시·Rename)·Recover | localfs 테스트 |
| 5 | config: `[DOWNLOAD]` 섹션·검증, `HourLayout` 제거 | |
| 6 | put: common 읽기 전용 관측, download VERIFIED 조건 (P1) | 기존 테스트 회귀 확인 |
| 7 | audit: 대사 검사·로그 | |
| 8 | main: download → put → resend → audit 배선 | 운영 시나리오 테스트 |
| 9 | 문서 정리 (§14) | |

---

## 16. 미결 질문

| # | 질문 | 초안 기본값 |
|---|---|---|
| Q1 | 표준 폴더 구조 이름(§6) | `RINEX-V{n}-{D\|H}\(YYYY)\(DOY)\` |
| Q2 | 재귀 최대 깊이 | 6 |
| Q3 | 매 회차 원본 트리 전체 탐색 비용. 10년치 트리를 매시간 도는 비용 실측 필요 | 실측 후 결정. 디렉터리 mtime 가지치기는 9.9절에서 기각된 안이므로 재론 시 근거 필요 |
| Q4 | put 후보 방식 P1/P2 (§7.2) | P1 |
| Q5 | 해시 기본 ON 여부 | ON |
| Q6 | 표준 폴더 파일 정리 기준(§9.4) | Retention 경과 + put VERIFIED |
| Q7 | `Mode` 값 (put 유지 vs both) | put 유지 |
| Q8 | 압축 파일(.zip/.Z/.gz) — 지질자원연 `hdbg2810.zip`처럼 타입 문자 없는 이름의 판정 | 받은 그대로 보냄. 판정 불가 이름은 `unknown` 집계 |
| Q9 | 같은 이름·다른 내용 충돌 시 처리(§5.5) | 둘 다 보류 + 경고 |
| Q10 | 대사 적체 WARN 기준 시간 | 미정 |
