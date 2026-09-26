# SFTPClient — DOWNLOAD 설계 v4

> v4 는 v3(`SFTPClient_DOWNLOAD_DESIGN_v3.md`, 보존)에 커밋 1~7 구현 결과를 반영한
> 정정판이다. 본문 구조와 절 번호는 v3 그대로이고 바뀐 곳에 `〔커밋 8 정정〕`을 남겼다.
>
> 작성 2026-09-24.
> `SFTPClient_DOWNLOAD_DESIGN_v1.md`은 최초 독립 모듈 설계, v2는 DB·소유권·이력까지 고려한 확장 설계로 보존한다.
> 이 문서는 촉박한 구현 일정과 측위원의 안정적인 내부 중간 서버 환경을 반영한 **DOWNLOAD v1 실제 구현 기준**이다.
> v2의 복잡한 구조를 기각하지 않는다. §12의 조건이 실제로 발생하면 v2를 다시 검토한다.
>
> 표기: **[지시]** 대표 지시 / **[결정]** 개발자 결정 / **[판단]** 개발 판단 / **[후속]** 이번 구현 뒤로 미룬 항목.
>
> **〔커밋 8 정정, 2026-09-26〕** DOWNLOAD 커밋 1~7 구현 뒤 이 문서를 코드와 대조해
> 어긋난 문장을 고쳤다. 고친 곳마다 `〔커밋 8 정정〕`과 이전 문구를 남긴다. 설계
> 방향(장부 없음, 파일시스템 상태, `.part → 크기 → Rename`)은 바뀌지 않았다.
> 코드와 이 문서가 다르면 코드가 기준이다. 정정 목록은 §14.

---

## 0. 한 줄 요약

**DOWNLOAD는 안정적인 내부 SFTP 서버의 완성 RINEX 파일을 로컬에 추가하는 기능이다.
기존 PUT의 검증된 나열·검증·병렬 처리·전송 감시 구조를 재사용하되, PUT의 장부·revision·재시도 상태는 복제하지 않는다.
로컬 최종 파일이 있으면 건너뛰고, 없으면 `.part`로 받은 뒤 크기를 확인하고 Rename한다.
BOTH의 coordinator는 DOWNLOAD 완료 후 PUT을 시작해 순서만 보장한다.**

---

## 1. 설계 선택

### 1.1 v3를 구현 기준으로 선택한 이유

- 측위원 DOWNLOAD는 외부 기관 서버로 보내는 PUT과 달리 **내부 중간 서버 → 내부 PC** 구간이다.
- 기존 `backup_RNX*.py`가 별도 장부 없이 운영될 만큼 환경이 안정적이다.
- 실패한 파일은 다음 정기·비정기 회차에서 다시 받으면 된다.
- 로컬 최종 파일의 존재 여부를 프로그램이 직접 확인할 수 있다.
- 일정 안에 GET을 실제 SFTP에서 검증하는 것이 DB 상태 모델을 먼저 완성하는 것보다 중요하다.
- PUT에서 이미 검증한 기계적 구조를 재사용하면 새 구현과 회귀 위험을 줄일 수 있다.

### 1.2 v2와의 관계

| 문서 | 위치 |
|---|---|
| v1 | 최초 독립 모듈 설계. 변경하지 않고 보존 |
| v2 | 소유권·수신 이력·별도 DB·운영 조회가 필요한 경우의 확장 설계. 변경하지 않고 보존 |
| **v3** | **현재 구현 기준. 파일시스템 상태를 사용한 단순한 DOWNLOAD** |

단순화는 기능 후퇴를 숨기지 않는다. v2가 필요해지는 조건을 §12에 명시한다.

---

## 2. 범위

### 2.1 이번 구현

| # | 항목 |
|---|---|
| 1 | `Mode=download`: 원격 SFTP → 로컬 GET |
| 2 | 날짜 토큰과 `(SITE)`·`(HH)` 패턴 폴더 나열을 사용한 원격 경로 전개와 원격 목록 조회 〔커밋 8 정정: 이전 "날짜 토큰을 사용한"〕 |
| 3 | 기존 RINEX 파일명·카테고리·Grace 검증 재사용 |
| 4 | `[DOWNLOAD] Sites`: 지정 관측소만 수신. 생략 시 전체 |
| 5 | 목적지 LocalPath의 `(SITE)`·`(HH)` 전개 |
| 6 | `.part → 크기 검증 → Rename` |
| 7 | bounded worker pool, ctx 취소, SFTP stall 감시 |
| 8 | 회차 집계·로그와 기존 PUT 전체 회귀 검증 |
| 9 | Ping-Pong 정적 설정 검증 |

### 2.2 설계만 남기고 운영 개방은 뒤로

| 항목 | 이유 |
|---|---|
| `Mode=both` | 대표에게 구현 범위로 보고하지 않았고 GET 검증이 먼저다. 구조는 §8로 고정 |
| 로컬 Retention Cleanup | 30일 정책은 유지하되 삭제 소유 범위·경계 테스트를 확정한 뒤 연결 (§9) |
| PUT 로그 형식 변경 (한국어 필드·0 생략) | DOWNLOAD와 무관한 운영 PUT 출력 변경이다. PUT·resend 로그 테스트와 현장 로그 검색·모니터링에 영향이 있으므로 별도 과제로 분리한다 (§10.4) |
| 수동 강제 재수신 CLI | 기존 파일을 의도적으로 교체해야 할 때 후속 제공 |
| PUT `RemotePath`의 파일 토큰 | PATH v4 §8-3 후속. PUT `LocalPath`의 `(SITE)`·`(HH)`는 PATH v4에서 완료되었다 〔커밋 8 정정: 이전 "PUT 경로의 `(SITE)`·`(HH)` 및 PATH v4 전체 — GET 구현과 분리"〕 |
| 원격 Retention | 원격 원본은 읽기 전용 |
| 다운로드 이력 조회 DB | 실제 운영 요구가 확인되면 v2 재검토 |

---

## 3. 책임과 결합도

```
                 실행 순서만 조율
┌──────────┐       ┌─────────────┐       ┌──────────┐
│ download │ ────▶ │ coordinator │ ────▶ │   put    │
│ 원격→로컬 │ 결과  │  BOTH 전용   │ 호출  │ 로컬→원격│
└────┬─────┘       └─────────────┘       └────┬─────┘
     │                                         │
     └──── 로컬 최종 RINEX 파일 ────────────────┘
```

### 3.1 download

DOWNLOAD 유스케이스 전체의 주인이다.

- 원격 경로 전개와 목록 조회
- RINEX·SITE 검증
- 받을 파일과 로컬 목적지 결정
- `transport.DownloadPart` 호출
- 로컬 `.part` 크기 검증
- 최종 이름 Rename
- 파일별 실패 처리와 회차 집계

PUT 코드나 PUT DB를 읽거나 호출하지 않는다.

### 3.2 transport

SFTP 연결을 통한 바이트 이동의 주인이다.

- 원격 Open과 로컬 `.part` Open
- 청크 Read/Write
- ctx 취소 확인
- `progress.enter/beat/exit`
- Close 오류 보존
- stall 발생 시 연결 Abort가 블록을 깨울 수 있는 계측 제공

### 3.3 coordinator

BOTH에서 실행 순서와 결과 통합만 담당한다.

- `download.Run` 호출
- 인계 대기 정책 적용
- `put.Run` 호출
- 두 결과를 합쳐 종료 코드 반환

파일 목록·DB·서버 경로를 읽지 않고 Ping-Pong을 판정하지 않는다.

### 3.4 config.Validate

정적 설정 조합의 주인이다.

- Mode별 필수 설정
- 토큰과 Category의 호환성
- PUT·DOWNLOAD 원격 서버와 경로의 순환 가능성
- BOTH 인계 대기 값

PUT과 DOWNLOAD 설정이 모두 존재할 때는 Mode와 관계없이 명백하거나 의심스러운 순환을 보수적으로 거부한다.
서로 다른 config·PC·호스트 별칭·서버 심볼릭 링크를 통한 순환은 한 인스턴스의 설정 검사로 보장하지 않는다.

---

## 4. PUT에서 재사용하는 것

| PUT에서 검증된 요소 | DOWNLOAD 적용 |
|---|---|
| `scan.Scanner` 재귀 탐색 | `DirLister`의 원격 구현(`download.sftpLister`)을 끼워 원격 날짜 폴더 아래를 재귀 수집. 원본 RemotePath에도 `(SITE)`·`(HH)`를 쓸 수 있고, Scanner가 그 자리의 폴더를 나열해 찾는다 (PATH v4). `internal/scan`은 수정하지 않았다 〔커밋 8 정정: 이전 "`SFTPLister` … 원본 RemotePath에는 날짜 토큰만 둔다"〕 |
| `scan.Entry`와 나열 구조 | 원격 Entry를 같은 검증 입력 형태로 조립 |
| `verify.Verifier` | `.part`·0바이트·Grace·FutureMTime·Category 불일치 거부 |
| `domain.SiteFromName` | Sites 필터와 `(SITE)` 전개 |
| 파일명 파서 | `(HH)` 추출 |
| `pathpl` | 날짜·파일명 토큰 전개 |
| bounded worker pool 패턴 | `[DOWNLOAD] MaxWorkers` 제한 |
| `SFTPFS` progress와 `WatchStall` | GET 청크 진행 감시 |
| LocalFS Rename·Remove 규칙 | `download.OSLocalStore`: Stat 은 Lstat, Remove 는 일반 파일만 지운다(빈 폴더·링크 보호). 검증된 `.part` 공개와 실패 정리 〔커밋 8 정정: 구현 위치 명시〕 |
| 집계·로그 형식 | DOWNLOAD 회차 보고 |

### 4.1 재사용하지 않는 PUT 전용 정책

- `common_ledger`·`put_ledger`
- revision·content hash
- PENDING·IN_PROGRESS·attempts
- 세트 게이트
- 자동 resend
- `origin=DOWNLOAD`·`RepostDownloaded`
- 외부 목적지의 과거 전송 이력

기계적 구조는 재사용하지만 외부 기관 서버의 불안정성을 위해 만든 상태 모델은 복제하지 않는다.

---

## 5. DOWNLOAD 흐름

```
lock 획득 → 원격 읽기 전용 접속 → stall 감시 시작
  → 원격 날짜 경로 전개 ((SITE)·(HH) 자리는 폴더 나열)
  → SFTP 목록 조회
  → 원본 이름으로 IsPartFile 검사
  → NormalizeName
  → verify.Verifier
  → SiteFromName · Sites 필터 · HourFromName (필요할 때만, §5.1)
  → (SITE)/(HH) 목적지 계산
  → 회차 내 목적지 중복 제거 (먼저 발견한 원격 경로만)
  → 로컬 최종 경로 관측

      있음 ─────────────────────────────▶ 기존·충돌·비정상대상 (§5.2)

      없음 → EnsureDir(목적지 폴더)
           → DownloadPart(remote, final.part)
           → final.part 크기 == 원격 Entry 크기   (다르면 실패, .part 삭제)
           → Rename 직전 최종 경로 재확인         (생겼으면 덮어쓰지 않음, .part 삭제)
           → Rename(final.part, final)
           → 수신 집계
  → 보고 → 접속 종료 → lock 해제
```

〔커밋 8 정정: 이전 흐름에는 lock·접속 순서, 목적지 중복 제거, Rename 직전
재확인이 없었다. 실제 순서는 `cmd/rinexclient/download.go`의 `runDownload`와
`download.Runner.fetch`다.〕

### 5.1 원격 파일 이름 판정 순서

DOWNLOAD Runner는 원격 Entry의 **원본 이름**을 다음 순서로 처리한다.

```text
원격 Entry 원본 이름(rawName)
  → domain.IsPartFile(rawName)
      └─ true: 제외·집계하고 종료
  → domain.NormalizeName(rawName)
  → verify.Verifier (원본 이름을 넘긴다 — PUT 과 같은 입력)
  → Sites 가 있거나 LocalPath 에 (SITE) 가 있을 때만
      domain.SiteFromName(category, normalizedName)   식별 불가면 사이트식별불가
      → Sites 필터                                     목록 밖이면 사이트제외
  → LocalPath에 (HH)가 있을 때만
      domain.HourFromName(category, normalizedName)   식별 불가면 시각식별불가
  → 목적지 경로 전개
```

〔커밋 8 정정: `SiteFromName`은 필요할 때만 부른다. Sites 도 `(SITE)`도 없을 때
부르면 verify 를 통과한 파일이 "사이트식별불가"라는 새 제외 사유로 빠진다.
PUT 의 site 필터와 같은 규칙이다(SITE v1 §4). `시각식별불가`는 구현 중 추가한
사유다 — 예: `SOCH00KOR_R_20262689900_01H_…`처럼 종류·관측소는 맞는데 시각이
99인 이름.〕

`IsPartFile`은 반드시 `NormalizeName`보다 먼저 호출한다. `NormalizeName`은
프로그램이 사용하는 `.part` 접미사를 제거하므로 순서가 뒤집히면
`SUW1250a.26o.part`가 `SUW1250a.26o`로 바뀌어 완성 파일처럼 SITE·HH
추출에 성공할 수 있다. `.filepart`도 같은 첫 단계에서 제외한다.

이 순서는 `HourFromName`이나 `pathpl`의 책임이 아니다. 두 함수는 이미
후보 판정과 정규화가 끝난 입력만 받는 순수 함수로 유지하며, 원격 파일을
어떤 순서로 거를지는 DOWNLOAD Runner가 한곳에서 소유한다.

이 판정 순서는 DOWNLOAD 커밋 4(`download.planner.evaluate`)와 커밋 5(Runner)에서
배선되었고 테스트로 고정되어 있다(D18 포함). 〔커밋 8 정정: 이전 "이후 DOWNLOAD
Runner 커밋에서 구현한다 … 실행되고 있다고 간주하면 안 된다"〕

### 5.2 받을지 판정

| 로컬 최종 경로 | 처리 |
|---|---|
| 파일 없음 | 다운로드 |
| 파일 있음, 원격과 크기 같음 | 건너뜀 (`skipped_exists`) |
| 파일 있음, 원격과 크기 다름 | 덮어쓰지 않고 충돌 경고 (`conflict`) |
| 같은 이름의 디렉터리·링크·비정상 대상 | 받지 않음 (`irregular_target`, 비정상대상). 링크는 따라가지 않는다(Lstat) |
| 원격 여러 경로가 같은 로컬 최종 경로로 수렴 | 먼저 발견한 것만 받고 나머지는 `duplicate_destination`(목적지중복). 같은 `.part`를 두 worker 가 동시에 쓰지 않는다 |

〔커밋 8 정정: 이전 "같은 이름의 디렉터리·비정상 대상 — 오류·보류". 실제로는
오류가 아니라 받지 않는 사유로 센다. 목적지 중복 행은 구현 중 추가.〕

수신 직후 Rename 직전에 최종 경로를 한 번 더 관측한다. 전송 중 외부에서 최종
파일이 생겼으면 덮어쓰지 않고 `.part`를 지운 뒤 위 표의 사유로 센다.
재확인과 Rename 사이의 극단적 경합은 "DOWNLOAD LocalPath 는 이 프로그램 전용"
운영 전제로 남긴다(Go 의 `os.Rename`은 Windows·Linux 모두 대상을 덮어쓴다).

- 기존 최종 파일은 자동으로 덮어쓰지 않는다.
- 원격의 같은 이름 파일이 수정돼도 자동 갱신하지 않는다. 완성 RINEX는 불변이라는 현장 전제를 둔다.
- 교체가 필요한 예외는 후속 수동 재수신 CLI에서 명시적으로 수행한다.
- 다운로드 여부 판단에 DB를 사용하지 않는다.

### 5.3 실패

- 파일 하나 실패는 해당 파일과 원인을 집계하고 나머지 파일을 계속 처리한다.
- 같은 회차에서 즉시 재시도하지 않는다.
- 최종 파일이 없으므로 다음 회차가 자연스럽게 다시 시도한다.
- 실패는 로그와 회차 보고에 남기며 실행 제어용 DB에는 기록하지 않는다.

### 5.4 원격–로컬 시계 차이 [미결]

- 원격 Ingress의 Grace·FutureMTime은 **원격 파일의 mtime과 이 PC의 현재 시각**을 비교한다.
- 원격 시계가 앞서면 정상 파일이 `미래시각`·`유예중`으로 계속 거부될 수 있다. 원격 시계가 뒤지면 Grace가 실제보다 짧아져 아직 쓰는 중인 파일이 통과할 수 있다.
- 허용치와 기본 `GraceSeconds`는 측위원 실제 서버에 연결해 시계 차이를 확인한 뒤 정한다.
- 중계 서버에 쓰는 쪽이 임시 이름 → Rename 방식이면 `.part`/`.filepart` 제외만으로 충분하므로 Grace를 0으로 둘 수 있다. 이것도 현장 확인 항목이다.

---

## 6. `SFTPFS.DownloadPart` 계약

```go
DownloadPart(
    ctx context.Context,
    remotePath string,
    localPartPath string,
) error
```

### 6.1 책임

1. 원격 파일을 읽기 전용으로 연다. 원격에 없으면 로컬 `.part`를 만들지 않는다.
2. 이전 회차가 남긴 일반 파일 `.part`를 지우고 `O_CREATE|O_EXCL`로 새로 연다.
   `.part` 자리가 일반 파일이 아니면(디렉터리·링크) 거부한다.
   〔커밋 8 정정: 이전 "`CREATE|TRUNC`로 연다". 기존 `.part`가 다른 파일의 hard
   link 이면 `O_TRUNC`가 그 원본을 자른다 — 커밋 2 리뷰에서 바꿨다.
   `transport.openLocalPart` 주석 참고.〕
3. 청크 단위로 원격을 읽어 로컬에 쓴다.
4. 청크마다 ctx 취소를 확인한다.
5. 실제 청크 기록 성공마다 progress beat를 갱신한다.
6. 끝까지 받으면 로컬 `.part`를 Sync 해 디스크에 내린다. Sync 없이 Rename 하면
   정전 뒤 크기는 맞는데 내용이 비어 있는 최종 파일이 남을 수 있고, 다음 회차는
   크기가 같아 기존으로 건너뛴다.
7. Read·Write·Sync·Close 오류를 보존해 반환한다.

### 6.2 하지 않는 일

- 최종 파일 Rename
- 원격·로컬 크기 일치 판정
- `.part` 삭제
- RINEX·SITE 판정
- 로그·회차 집계

이 항목들은 download Runner가 담당한다.

### 6.3 실패 후 `.part`

- 전송 또는 크기 검증 실패 시 download Runner가 `.part` 삭제를 시도한다.
- 프로세스 강제 종료로 남은 `.part`는 다음 회차의 `DownloadPart`가 지우고 새로 만든다 〔커밋 8 정정: 이전 "처음부터 덮어쓴다"〕.
- `.part`는 기존 PUT Ingress에서 최종 RINEX 후보로 인정되지 않는다.
- IN_PROGRESS 장부나 시작 시 회수 절차는 두지 않는다.

---

## 7. 설정

```ini
[GENERAL]
Mode = download                  ; put(기본) | download | both(후속)
Transport = sftp                 ; download 는 sftp 만
LockPath = data\rinexclient.lock ; Mode=download 에서 필수

[DOWNLOAD]
Sites = SOCH,PALM,SUWN           ; 생략 또는 빈 값이면 전체
MaxWorkers = 4
GraceSeconds = 60                ; 필수. 0 은 검사 끔. 현장 확정 전 (§5.4)

[DOWNLOAD.SFTP]
AuthMethod = publickey
Host = ...
Port = 22
User = ...
PrivateKey = ...
KnownHosts = ...
StallTimeoutSeconds = 30

[DOWNLOAD.RINEX2_HOURLY]
Enabled = true
RemotePath = /RINEX2/(YYYY)/(DOY)/
LocalPath = D:\RNX\(YYYY)\(DOY)\(SITE)\
; [DOWNLOAD.<CATEGORY>] 6개 섹션이 모두 있어야 한다 (끄려면 Enabled = false)

; ※ 아래는 BOTH 운영 개방 후에만 쓴다. 지금 적으면 알 수 없는 키로 시작이 거부된다.
; [BOTH]
; HandoffDelaySeconds = 60
```

〔커밋 8 정정: `Transport`·`LockPath`·`GraceSeconds`·`Enabled`가 빠져 있었다.
전체 예시와 DOWNLOAD 전용 인스턴스 구성은 저장소 루트 `config.example.ini`의 `[DOWNLOAD]`
머리 주석이 기준이며, 그 구성이 실제로 검증을 통과하는지는
`internal/config/example_download_test.go`가 고정한다.〕

### 7.1 시작 검증

1. `Mode=download`인데 활성 DOWNLOAD Category가 0개면 거부한다.
2. 원본 RemotePath에는 날짜 토큰과 `(SITE)`·`(HH)`를 허용한다. `\`는 거부한다(원격 경로는 `/`).
   〔커밋 8 정정: 이전 "v3에서 지원하는 날짜 토큰만 허용한다" — PATH v4 가 원본 토큰을 허용했다.〕
3. Daily 목적지(LocalPath)에 `(HH)`가 있으면 거부한다. Daily 의 원본 RemotePath `(HH)`는
   원격 폴더 나열 패턴이라 허용한다.
4. `Sites`는 `domain.ParseSiteList`로 검증한다.
5. PUT·DOWNLOAD가 같은 `host:port`이고 원격 경로 범위가 겹치면 보수적으로 거부한다
   (두 블록이 모두 있으면 Mode 와 관계없이).
6. `Mode=both` 운영 개방 전에는 기능 게이트(`bothImplemented`)로 거부한다.
7. BOTH 개방 시 `HandoffDelaySeconds >= PUT GraceSeconds`를 요구할지 §8에서 최종 확정한다.
8. 〔커밋 8 추가〕 구현 중 더해진 검증:
   - `Mode=download`이면 `LockPath` 필수. LedgerPath·`[LOG] Dir`과 같거나 그 부모이면 거부.
   - `[DOWNLOAD] GraceSeconds` 키 필수(부재를 기본값으로 접지 않는다), 음수 거부.
   - `[DOWNLOAD] MaxWorkers` 1~16.
   - `Mode=download`이면 `Transport=localfs` 거부.
   - `RepostDownloaded=true`는 `Mode=put`·`Mode=download`에서 거부(BOTH 개방 전).
   - 실행 인자: `Mode=download`에서 `--seed`·`--dry-run`·`--transport=localfs` 거부.
     `rinexclient resend`는 PUT 전용이라 거부.

Ping-Pong 검사는 coordinator가 아니라 `config.Validate`가 담당한다.

---

## 8. BOTH [후속]

### 8.1 의미

`Mode=both`는 DOWNLOAD로 로컬에 추가된 파일을 기존 PUT 스캔이 발견해 자동 중계하는 모드다.
자동중계가 필요 없으면 `Mode=download` 또는 `Mode=put`을 사용한다.

### 8.2 순서

```
공통 실행 lock 획득
  → DOWNLOAD SFTP 접속
  → download.Run
  → DOWNLOAD 연결 종료
  → HandoffDelay
  → PUT SFTP 접속
  → put.Run
  → PUT 연결 종료
  → 결과 통합
  → lock 해제
```

- coordinator는 순서만 보장한다.
- download가 받은 파일 목록을 put에 전달하지 않는다.
- put은 기존 로컬 스캔으로 최종 파일을 신규 데이터로 발견한다.
- 파일시스템이 두 기능 사이의 유일한 데이터 계약이다.
- 받은 파일의 mtime을 보존하지 않으므로 즉시 PUT하면 `GraceSeconds`에 걸린다.
- `HandoffDelaySeconds`를 둘지 다음 예약 PUT 회차에 맡길지는 운영 개방 전에 확정한다.
- 부분 실패 때 PUT을 계속할지와 lock 범위도 운영 개방 전에 확정한다.

---

## 9. Retention [후속]

목표는 DOWNLOAD가 로컬에 생성한 RINEX 파일을 30일 후 삭제하는 것이다.

- 보존 기간 키는 `[DOWNLOAD] DownloadRetentionDays`로 따로 둔다. 기존 `[LEDGER] RetentionDays`는 **PUT 장부 행**의 보존 기간이므로
  같은 이름을 쓰지 않는다. 한쪽을 바꿀 때 다른 쪽이 딸려 가지 않게 하기 위해서다.
- `DownloadRetentionDays > ScanDays`를 시작 검증으로 강제한다.
- **삭제 기준은 파일의 관측 날짜(원격·로컬 날짜 폴더의 날짜)다.** 받은 시각(mtime)을 기준으로 하지 않는다.
  늦게 들어온 옛날 날짜 파일을 mtime 기준으로 지우면, 그 날짜가 아직 탐색 범위 안에 있을 때 다시 받게 된다.
- 위 두 조건이 함께 성립할 때만, 삭제 시점의 파일은 원격 탐색 범위 밖이므로 DB tombstone 없이도 다시 받지 않는다.
- Retention이 적용되는 LocalPath는 DOWNLOAD가 관리하는 전용 루트여야 한다.
- 인식 가능한 최종 RINEX 일반 파일만 삭제한다.
- `.part`, 디렉터리, 알 수 없는 파일을 같은 규칙으로 삭제하지 않는다.
- 정확한 삭제 기준과 경계일 테스트를 확정하기 전에는 Cleanup을 연결하지 않는다.

---

## 10. 보고·로그

### 10.1 표기 원칙

- `[PUT]`, `[DOWNLOAD]`, `[DRYRUN]` 같은 고정 태그는 로그 검색·필터와 기존 운영 식별을 위해 유지한다.
- 사람이 읽는 요약 필드명과 값의 의미는 한국어로 출력한다.
- **건수가 0인 항목은 출력하지 않는다.** 빈 제외·거부 묶음도 줄 자체를 출력하지 않는다.
- 종류(Category)·범위·모드·소요시간처럼 문맥을 정하는 값은 0 여부와 관계없이 항상 출력한다.
- 처리 건수가 모두 0이면 여러 `항목=0` 대신 `대상없음` 하나만 출력한다.
- map 기반 사유 집계는 한국어 표시명으로 변환한 뒤 고정 순서로 출력한다.
- 이번 구현은 **DOWNLOAD 보고에만** 이 규칙을 적용한다. 기존 PUT `RunReport.Print`는 바꾸지 않는다 (§10.4).

사유 표시 예시:

| 내부 값 | 로그 표시 |
|---|---|
| `part` | 임시파일 |
| `zero` | 0바이트 |
| `grace` | 유예중 |
| `future_mtime` | 미래시각 |
| `mismatch` | 종류불일치 |
| `site_mismatch` | 사이트제외 |
| `site_unknown` | 사이트식별불가 |
| `hour_unknown` | 시각식별불가 〔커밋 4 추가〕 |
| `downloaded` | 수신 |
| `skipped_exists` | 기존 |
| `conflict` | 충돌 |
| `irregular_target` | 비정상대상 〔커밋 4 추가〕 |
| `duplicate_destination` | 목적지중복 〔커밋 4 추가〕 |
| `failed` | 실패 |
| (취소로 착수 안 함) | 미착수 〔커밋 5 추가〕 |
| (수신했으나 이름으로 종류를 알 수 없음) | 종류미상 〔커밋 6 추가, 수신의 부분집합〕 |

실제 출력 순서: 원격 → 수신 → 종류미상 → 기존 → 사이트제외 → 사이트식별불가 →
시각식별불가 → 유예중 → 미래시각 → 임시파일 → 0바이트 → 종류불일치 → 충돌 →
비정상대상 → 목적지중복 → 실패 → 미착수. 표에 없는 사유는 증발하지 않고 끝에
`기타(키)=N`으로 나온다(`internal/download/report_print.go`).

원격 나열 이상은 요약 줄과 따로 나온다. `나열경고`(나열실패·원격폴더없음·
패턴폴더없음)는 화면과 파일, `나열참고`(비정규항목·폴더불일치·폴더밖파일)와
목적지중복·종류미상의 파일별 경로는 로그 파일에만 남는다. 근거와 경계 판단은
`SFTPClient_DOWNLOAD_LOG_ROUTING_DECISION_2026-09-26.md`.

### 10.2 DOWNLOAD 예시

```
[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 원격=412 수신=37 기존=357 사이트제외=12 사이트식별불가=2 유예중=3 실패=1 소요=2.4s
```

0인 `충돌`, `임시파일`, `0바이트`, `종류불일치`, `미래시각`은 보이지 않는다.

대상이 전혀 없으면 다음 한 줄이면 충분하다.

```text
[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 대상없음 소요=18ms
```

### 10.3 구현 규칙

- 실패 원인은 파일별 로그에 남긴다.
- 종료 코드는 0/1 이다. 0 은 정상 또는 lock 양보, 1 은 파일 수신 실패·원격 나열
  실패(`Report.Failed() > 0`, 부분 실패)와 설정·접속·lock 오류, stall·취소다.
  부분 실패는 보고를 모두 출력한 뒤 `download: partial failure: 실패=N`으로 끝난다
  (`cmd/rinexclient/download.go` `errDownloadPartial`).
  〔커밋 8 정정: 이전 "`실패>0`이면 기존 PUT과 같은 부분 실패 종료 코드를 사용한다".
  별도의 부분 실패 코드는 없고 1 을 쓴다. **PUT 과 다르다** — PUT 은 파일 실패에도
  0 이다(2026-09-22 확정). 작성 중 한때 PUT 과 같이 0 으로 하는 A안을 적었으나
  커밋 7 이 1 로 구현·테스트했다(`TestRunDownload_FileFailureReturnsPartialFailure`).〕
- 부분 실패는 main 이 `[FATAL]` 머리로 찍는다(PUT 과 같은 한 곳). 고객 화면에서는
  회차 전체 실패처럼 읽힐 수 있다 — 표기를 바꿀지는 현장 반응을 보고 정한다.
- 0 생략은 **표시 규칙만 바꾸며 집계 자체를 삭제하지 않는다.** 테스트와 내부 판단은 기존 숫자를 그대로 사용한다.
- 기존 PUT·resend 로그 문구와 그 테스트는 바꾸지 않는다.
- DOWNLOAD 이력 DB가 없으므로 운영 이력은 회차 로그가 담당한다.
- 한국어 로그가 Windows 콘솔·작업 스케줄러 로그 파일·Linux 로그에서 깨지지 않는지(인코딩) 실제 환경에서 확인한다.

### 10.4 PUT 로그 형식 변경 [후속]

같은 표기 원칙(한국어 필드, 0 생략, `대상없음`)을 PUT·DRYRUN 요약에도 적용하는 것은 별도 과제로 남긴다.

- 운영 중인 PUT의 출력이 바뀌므로 현장에서 영문 키를 검색하거나 모니터링에 쓰는지 먼저 확인한다.
- PUT·resend의 로그 문자열 테스트를 함께 바꿔야 한다.
- 0 생략은 표시만 바꾸고 집계값은 바꾸지 않는다는 계약은 그대로 적용한다.
- 착수 시점: DOWNLOAD 검증과 28일 시연 이후.

---

## 11. 테스트

| # | 시나리오 | 기대 |
|---|---|---|
| D1 | 원격 날짜/시간 폴더의 정상 파일 | `(SITE)` 목적지에 수신 |
| D2 | 같은 설정 재실행 | 다운로드 0, 기존 파일 건너뜀 |
| D3 | `Sites=SOCH` | SOCH만 수신 |
| D4 | `.part`·0바이트·Grace 미달 | 제외·집계, 다음 회차에서 재평가 |
| D5 | 전송 중 취소 | 최종 파일 없음, 다음 회차 재시도 |
| D6 | 로컬 `.part` 크기 불일치 | 최종 Rename 금지, 실패 집계 |
| D7 | 기존 최종 파일과 크기 같음 | 건너뜀, 파일 불변 |
| D8 | 기존 최종 파일과 크기 다름 | conflict, 자동 덮어쓰기 없음 |
| D9 | 남은 `.part` | 처음부터 덮어쓴 뒤 정상 완료 |
| D10 | stall | WatchStall 발화, 회차 취소와 SFTP Abort |
| D11 | Ping-Pong 설정 | `config.Validate`에서 시작 거부 |
| D12 | `Mode=put` 기존 설정 | DOWNLOAD 접속·로그·DB 생성 없이 기존 동작 |
| D13 | 기존 PUT 테스트 전체 | 전부 통과 |
| D14 | 실제 측위원형 SFTP | 목록·GET·크기 검증·Rename 확인 |
| D15 | DOWNLOAD 보고에 0인 항목 존재 | 0인 항목명과 빈 묶음 미출력, 한국어 필드만 출력 |
| D16 | `Mode=put` 실행 로그 | 기존 형식·문구 그대로 (PUT 로그 변경은 §10.4 후속) |
| D17 | 원격 시계가 로컬보다 앞섬/뒤짐 | 미래시각·유예중 집계로 드러나고 다른 파일 처리는 계속 |
| D18 | 이름만 보면 유효한 `*.part`·`*.filepart` | 원본 이름의 `IsPartFile`에서 먼저 제외되어 `NormalizeName`·`HourFromName`에 도달하지 않음 |

BOTH 운영 개방 시 순서·인계 대기·부분 실패 테스트를 별도 추가한다.

---

## 12. v2 재검토 조건

다음 중 하나가 실제 요구나 장애로 확인되면 v2의 별도 DB·소유권·이력 설계를 다시 검토한다.

1. 같은 파일명이 정상적으로 수정·재생성되어 자동 갱신이 필요하다.
2. `DownloadRetentionDays > ScanDays`를 유지할 수 없어 삭제 파일이 다시 탐색 범위에 들어온다.
3. 로컬 파일의 우발 삭제와 Retention 삭제를 구분해 자동 복구해야 한다.
4. 다른 프로그램이 DOWNLOAD LocalPath에 같은 이름의 파일을 만들며 소유권 구분이 필요하다.
5. 파일별 수신 성공·실패 이력을 DB로 장기간 조회해야 한다.
6. 감사·정산을 위해 수신 이력과 PUT 이력을 연결해야 한다.
7. 내부 서버나 회선이 불안정해 attempts·retry budget·IN_PROGRESS 회수가 필요하다.

이 조건이 없으면 v3의 파일시스템 기반 단순 모델을 유지한다.

---

## 13. 구현 순서

| 순서 | 작업 | 완료 조건 |
|---|---|---|
| 1 | domain·pathpl: `(SITE)`·`(HH)` 목적지 전개 | 기존 pathpl 테스트 + 신규 단위 테스트 |
| 2 | config: DOWNLOAD 설정과 정적 검증 | Mode=put 무영향, 오류 조합 시작 거부 |
| 3 | transport: 원격 목록과 `DownloadPart` | ctx·progress·부분 파일 테스트 |
| 4 | download Runner: 원본 임시파일 거부→정규화→검증→수신→크기→Rename | D1~D10, D18 |
| 5 | main 배선: `Mode=download`, 보고·종료 코드 | 실제 SFTP GET |
| 6 | 보고 포맷: DOWNLOAD 한국어 필드, 0 생략 (PUT은 그대로) | D15~D16 |
| 7 | 기존 PUT 전체 회귀 | `go test ./...` 통과 |
| 8 | 문서·config.example 갱신 | 실제 설정 예시와 운영 절차 일치 |
| 9 | 여유가 있고 범위가 확인되면 BOTH | §8 미결 확정 후 별도 검증 |

매 단계에서 기존 PUT 테스트 전체 통과를 유지한다.

---

## 14. 구현 반영 정정 목록 〔커밋 8, 2026-09-26〕

DOWNLOAD 커밋 1~7 뒤 코드와 대조해 고친 곳이다. 설계 방향은 바뀌지 않았고, 문장이
실제 동작과 어긋난 곳만 고쳤다.

| 절 | 이전 문구 | 실제 (코드) |
|---|---|---|
| §2.1-2, §4, §7.1-2 | 원본 RemotePath 에는 날짜 토큰만 | `(SITE)`·`(HH)` 허용, Scanner 가 폴더 나열 (PATH v4). `\` 는 거부 |
| §2.2 | PUT `(SITE)`·`(HH)` 및 PATH v4 전체는 후속 | PUT LocalPath 쪽은 PATH v4 에서 완료. PUT RemotePath 파일 토큰만 후속 |
| §4 | `SFTPLister` | `download.sftpLister`(비공개). 로컬은 `download.OSLocalStore` |
| §5 | lock·접속 순서, 목적지 중복, Rename 직전 재확인 없음 | 모두 있음 (`runDownload`, `Runner.fetch`) |
| §5.1 | SiteFromName 항상 호출 / 배선은 이후 커밋 | 필요할 때만 호출, `시각식별불가` 추가 / 커밋 4·5 에서 배선 |
| §5.2 | 디렉터리·비정상 대상은 오류·보류 | 받지 않는 사유(`비정상대상`). `목적지중복` 행 추가 |
| §6.1-2, §6.3 | `.part` 를 `CREATE\|TRUNC` | 기존 일반 `.part` 를 지우고 `O_EXCL`. 일반 파일이 아니면 거부 (hard link 보호) |
| §7 | 예시에 Transport·LockPath·GraceSeconds·Enabled 없음 | 추가. 기준은 저장소 루트 `config.example.ini` |
| §7.1 | 검증 7개 | LockPath·GraceSeconds·MaxWorkers·localfs·RepostDownloaded·CLI 조합 추가 |
| §10.1 | 사유 11개 | 시각식별불가·비정상대상·목적지중복·미착수·종류미상 추가, 실제 출력 순서 명시, 화면/파일 분리 |
| §10.3 | 실패>0 이면 부분 실패 종료 코드 | 별도 코드 없이 1. 파일·나열 실패가 있으면 exit 1 (PUT 은 0 — 다르다) |

**여전히 미확인 (이 정정으로 확정하지 않은 것)**

- §5.4 원격–로컬 시계 차이, 원본 생성 방식(임시 이름 → Rename 여부), `GraceSeconds` 확정값
- D14 실제 SFTPGo 대상 목록·GET·크기 검증·Rename
- §10.3 한국어 로그 인코딩(Windows 콘솔·작업 스케줄러 로그·Oracle Linux 9.4)
- SFTPGo 가 "폴더 자리에 파일"을 나열할 때 보내는 상태 코드 (transport `classifyReadDirFailure` 가 없음/있음을 다시 확인하지만 실서버 응답은 미확인)

이 항목들은 28일 사무실 검증 후 커밋 8b 에서 기록한다.
