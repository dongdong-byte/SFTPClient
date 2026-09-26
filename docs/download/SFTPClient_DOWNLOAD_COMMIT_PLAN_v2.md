# SFTPClient — DOWNLOAD 구현 커밋 계획 v2

> v2 는 v1(`SFTPClient_DOWNLOAD_COMMIT_PLAN_v1.md`, 보존)에 커밋 1~8 의 실제와 다른
> 곳을 `〔커밋 8 기록〕`으로 덧붙인 판이다.
>
> 작성: 2026-09-25
> 구현 기준: `SFTPClient_DOWNLOAD_DESIGN_v3.md` (커밋 8 정정판은 `SFTPClient_DOWNLOAD_DESIGN_v4.md`)
> 경로 기준: `docs/path/SFTPClient_PATH_V4_FINAL.md`
> 범위: `Mode=download` 원격 SFTP → 로컬 수신. `Mode=both`는 이번 범위에서 제외한다.

---

## 0. 한 줄 요약

DOWNLOAD는 PUT 장부와 상태 머신을 사용하지 않는다. 원격 SFTP를 기존
`scan.Scanner`로 나열하고, 검증된 파일만 로컬 `.part`로 받은 뒤 크기를
확인하고 최종 이름으로 전환한다. 구현은 코드 7개와 문서 1개, 총 8개
커밋으로 나누며 모든 커밋에서 기존 PUT 동작과 테스트를 보존한다.

---

## 1. 공통 원칙과 완료 조건

### 1.1 공통 완료 조건

모든 커밋은 다음 조건을 만족해야 한다.

1. `go test ./...`가 전부 통과한다.
2. 기존 PUT 테스트의 기대값과 로그 문구를 DOWNLOAD 구현 편의를 위해
   수정하지 않는다.
3. 변경한 동시성 코드가 있는 커밋은 `go test -race ./...`도 통과한다.
4. `Mode=put`에서는 DOWNLOAD 접속·로그·파일 생성이 발생하지 않는다.
5. DOWNLOAD는 `common_ledger`·`put_ledger`를 읽거나 쓰지 않는다.
6. DOWNLOAD 실패는 같은 회차에서 즉시 재시도하지 않는다. 최종 파일이
   없으면 다음 예약 회차가 자연스럽게 다시 시도한다.

### 1.2 이번 계획에서 확정한 설정 결정

#### DOWNLOAD Grace는 별도 값으로 둔다

```ini
[DOWNLOAD]
GraceSeconds = 60
```

PUT의 로컬 파일 시계와 DOWNLOAD 원격 서버의 시계는 운영 조건이 다르므로
`[INGRESS] GraceSeconds`를 공유하지 않는다.

- 키 없음: `Mode=download`에서 시작 거부
- `0`: Grace·FutureMTime 검사를 명시적으로 끔
- 양수: 해당 시간을 DOWNLOAD 원격 파일 검증에 사용
- 음수: 시작 거부

28일 현장 확인 전 예시값은 `60`으로 두고, 실제 원격 시계 차이와 원본
생성 방식 확인 후 `0` 또는 적정 양수로 확정한다.

#### 날짜 탐색 범위는 기존 `[SCAN]`을 공유한다

`ScanRecentDays`, `ScanDays`, `DeepScanHour`는 전송 방향이 아니라 날짜
탐색 정책이므로 PUT과 DOWNLOAD가 공유한다. 실제 운영에서 서로 다른 범위가
필요하다는 근거가 생길 때만 분리한다.

#### lock 경로는 Ledger에서 분리한다

새 선택 키를 둔다.

```ini
[GENERAL]
LockPath = data\rinexclient.lock
```

호환 규칙은 다음과 같다.

| 조건 | 적용할 lock 경로 |
|---|---|
| `LockPath` 명시 | 해당 경로 |
| `LockPath` 생략 + `Mode=put` | 기존처럼 `LedgerPath + ".lock"` |
| `LockPath` 생략 + `Mode=download` | 시작 거부 |

`LedgerPath`와 `[LEDGER]` 설정은 `Mode.DoesPut()`일 때만 필수다. 따라서
`Mode=download`는 Ledger DB 파일을 열거나 만들지 않는다. 이후 BOTH가
개방되면 두 방향은 같은 `LockPath` 하나를 사용한다.

### 1.3 CLI 조합

첫 DOWNLOAD 구현에서는 다음과 같이 제한한다.

| 조합 | 처리 |
|---|---|
| `Mode=download` + `--deep` | 허용. `ScanDays` 범위를 강제한다 |
| `Mode=download` + `--seed` | 시작 거부. seed는 PUT 장부 초기화 기능이다 |
| `Mode=download` + `--dry-run` | 이번 범위에서는 시작 거부 |
| `Mode=download` + `--transport=localfs` | 원격 `RemoteFS` 구현이 없으므로 시작 거부 |
| `Mode=both` | 계속 시작 거부 |

DOWNLOAD dry-run은 필요가 확인되면 “GET 없이 목적지·기존·충돌 판정만
수행”하는 별도 기능으로 설계한다.

---

## 2. 커밋 1 — config에 `[DOWNLOAD]` 추가

실행 게이트 `downloadImplemented = false`는 이 커밋에서 유지한다.

### 2.1 구현 내용

- `Config`에 `DownloadConfig`를 추가한다.
- `[DOWNLOAD]`, `[DOWNLOAD.SFTP]`, `[DOWNLOAD.<CATEGORY>]`를 파싱하고
  `knownKeys`에 등록한다.
- `[GENERAL] LockPath`를 선택 키로 추가하고 config 파일 기준 절대경로로
  해석한다.
- `[DOWNLOAD]`에서 다음 값을 읽는다.
  - `Sites`
  - `MaxWorkers`
  - `GraceSeconds`
- `Sites`는 `domain.ParseSiteList`로 검증한다. 생략하거나 빈 값이면 전체
  관측소를 뜻한다.
- `Mode=download`인데 활성 DOWNLOAD Category가 0개면 거부한다.
- `MaxWorkers`는 PUT과 같은 운영 범위인 `1..16`으로 제한한다.
- `GraceSeconds`의 존재 여부를 보존해, 미입력과 명시적 `0`을 구분한다.

### 2.2 경로 토큰 정책

PATH v4의 역할 정책을 적용한다.

| 설정 | 역할 | `(SITE)`·`(HH)` |
|---|---|---|
| DOWNLOAD `RemotePath` | 원격 원본 | 허용. `scan.Template.Steps` 나열 경로를 사용 |
| DOWNLOAD `LocalPath` | 로컬 목적지 | 허용. 파일별 `ExpandFile` 사용 |
| Daily `LocalPath`의 `(HH)` | 역할 불일치 | 시작 거부 |

DOWNLOAD `RemotePath`는 `Template.Expand`로 한 번에 펼치지 않는다. 원본의
파일 토큰은 부모 폴더를 나열해 실제 폴더를 찾는 `scan.Scanner` 경로를
반드시 사용한다.

### 2.3 Mode별 검증과 환경 검사

`Validate`와 `CheckEnvironment`를 다음처럼 나눈다.

```text
공통
  Mode, Transport, LockStale, LockPath, LOG, SCAN

Mode.DoesPut()
  LedgerPath, LEDGER, PUT.SFTP, PUT Category, SET

Mode.DoesDownload()
  DOWNLOAD.SFTP, DOWNLOAD Category, Sites, Grace, MaxWorkers
```

`Mode=download`에서는 PUT Ledger와 SFTP 필수값을 요구하지 않는다.
`Mode=put`의 기존 검증 순서와 오류 문구는 유지한다.

### 2.4 Ping-Pong 정적 검사

PUT과 DOWNLOAD 설정이 모두 존재하고, 다음 두 조건이 함께 성립하면
Mode와 관계없이 보수적으로 시작을 거부한다.

1. PUT과 DOWNLOAD SFTP가 같은 정규화된 `host:port`를 가리킨다.
2. PUT 목적지 `RemotePath`와 DOWNLOAD 원본 `RemotePath`의 정적 범위가
   같거나 한쪽이 다른 쪽을 포함할 가능성이 있다.

토큰이 포함된 경로는 토큰 이전의 고정 prefix를 기준으로 판정한다.
서버 별칭·심볼릭 링크·서로 다른 config를 통한 순환은 한 인스턴스의
정적 검사로 보장하지 않는다.

### 2.5 완료 조건

- `[DOWNLOAD]`가 없는 기존 PUT config의 동작과 경고가 이전과 같다.
- `Mode=download`는 `LedgerPath`와 `[LEDGER]` 없이 로드·검증할 수 있다.
- `Mode=download`에서 `LockPath`가 없으면 명확한 오류로 거부한다.
- Sites 형식 위반, Grace 미입력·음수, Daily `LocalPath`의 `(HH)`를 각각
  정확한 오류 문구로 거부한다.
- DOWNLOAD `RemotePath`에 `(SITE)`·`(HH)`가 있어도 로드된다.
- PUT 목적지와 DOWNLOAD 원본의 명백한 순환 설정을 거부한다(D11).
- `downloadImplemented = false`이므로 실제 DOWNLOAD 실행은 아직 거부된다.

---

## 3. 커밋 2 — transport에 원격 나열과 다운로드 추가

### 3.1 읽기 전용 SFTP 접속

현재 `DialSFTP`는 PUT의 원격 덮어쓰기 계약 때문에 접속 시
`posix-rename@openssh.com` 지원을 강제한다. DOWNLOAD는 원격 Rename을
사용하지 않으므로 읽기 전용 접속 경로를 추가한다.

```go
DialSFTP(...)          // PUT: posix-rename 검사 유지
DialSFTPReadOnly(...)  // DOWNLOAD: posix-rename 검사 생략
```

TCP·SSH·known_hosts·키 인증·SFTP subsystem 초기화는 비공개 공통 함수로
공유한다. 기존 `DialSFTP`의 동작은 바꾸지 않는다.

### 3.2 `ReadDir`

```go
func (s *SFTPFS) ReadDir(
    ctx context.Context,
    dir string,
) ([]fs.FileInfo, error)
```

- 호출 전 ctx 취소를 확인한다.
- 원격 호출 전체를 `prog.enter/exit`로 감싼다.
- 없는 경로는 `wrapNotExist`로 감싸 `errors.Is(err, fs.ErrNotExist)`가
  성립하게 한다.
- 파일 종류 비트를 보존해 어댑터가 링크를 디렉터리로 오인하지 않게 한다.

### 3.3 `DownloadPart`

```go
func (s *SFTPFS) DownloadPart(
    ctx context.Context,
    remotePath string,
    localPartPath string,
) error
```

처리 순서는 다음과 같다.

1. 원격 파일을 읽기 전용으로 연다.
2. 로컬 `.part`를 `CREATE|TRUNC`로 연다.
3. 청크 경계마다 ctx 취소를 확인한다.
4. 읽은 바이트 전체가 기록될 때까지 short write를 처리한다.
5. 청크 기록 성공마다 `prog.beat()`를 호출한다.
6. Read·Write·Close 오류를 버리지 않고 반환한다. 앞선 주 오류가 있으면
   그 원인을 유지하고 Close 오류는 필요하면 `errors.Join`으로 보존한다.

최종 Rename, 크기 판정, `.part` 삭제는 transport가 하지 않는다.

### 3.4 in-process SFTP 테스트

vendor에 포함된 `pkg/sftp`의 `NewServer`와 `NewClientPipe`를 사용해 테스트
프로세스 안에서 SFTP 프로토콜을 실제로 통과시킨다. 새 의존성은 추가하지
않는다.

테스트 전용 공개 생성자는 production API에 추가하지 않는다. 테스트는
`package transport`에서 비공개 helper로 구성한다. `Abort`가 실제로 블록된
호출을 깨울 수 있도록 운영 SSH 연결과 테스트 pipe를 동일한 close 계약으로
주입한다. 단순히 `conn == nil`일 때 아무것도 하지 않는 분기는 두지 않는다.

커밋 초반에 Windows 임시 디렉터리와 SFTP 서버 경로의 매핑을 먼저
확인한다.

### 3.5 완료 조건

- 부분 파일이 정상적으로 로컬에 수신된다.
- 큰 파일 전송 중 ctx 취소가 청크 경계에서 반영된다.
- 원격에 없는 경로는 `fs.ErrNotExist`로 판별된다.
- Read·Write·Close 오류를 삼키지 않는다.
- `ReadDir`가 일반 파일·실제 디렉터리·링크의 종류를 구분한다.
- 테스트 pipe 종료가 블록된 원격 호출을 실제로 해제한다.
- 기존 PUT `DialSFTP`의 posix-rename 검사는 유지된다.

---

## 4. 커밋 3 — download의 RemoteFS와 SFTP 나열 어댑터

### 4.1 인터페이스

DOWNLOAD가 사용하는 최소 원격 인터페이스를 consumer 쪽에 선언한다.

```go
type RemoteFS interface {
    ReadDir(
        ctx context.Context,
        dir string,
    ) ([]fs.FileInfo, error)

    DownloadPart(
        ctx context.Context,
        remotePath string,
        localPartPath string,
    ) error
}
```

원격 크기는 목록의 `scan.Entry.Size`를 사용하므로 별도 `Size` 메서드를
두지 않는다.

### 4.2 `sftpLister` 어댑터

`sftpLister`는 `RemoteFS.ReadDir` 결과를 `scan.Entry`로 바꿔
`scan.DirLister`를 만족한다.

- Name, Size, MTime, IsDir, Type을 보존한다.
- 링크는 Type에 그대로 드러내며 실제 디렉터리로 바꾸지 않는다.
- 폴더 이름을 `00..23`으로 해석하거나 제한하지 않는다.
- 날짜 경로 아래 모든 실제 디렉터리는 기존 Scanner 재귀 정책을 따른다.
- `(SITE)`·`(HH)`가 원본 템플릿에 있으면 기존 `Steps`·`Match` 경로로
  부모를 나열한다.

### 4.3 완료 조건

- `internal/scan` 코드는 변경하지 않는다.
- `/RINEX2/(YYYY)/(DOY)/` 아래 시간 폴더의 파일을 재귀 수집한다.
- 없는 날짜 폴더는 오류가 아니라 `Missing`으로 집계된다.
- `/RINEX2/(YYYY)/(DOY)/(SITE)/`의 원본 토큰이 패턴 나열로 수집된다.
- 패턴에 맞지 않는 폴더는 `Unmatched`로 집계된다(PATH v4 §8-2).
- 링크·비정규 대상은 Scanner의 `Irregular` 정책을 그대로 따른다.

---

## 5. 커밋 4 — 순수 후보·기존 대상 판정

이 커밋은 네트워크나 실제 파일 I/O 없이 판정 규칙을 고정한다.

### 5.1 원격 Entry 판정 순서

순서는 다음과 같이 고정한다.

```text
rawName
  → IsPartFile(rawName)
  → NormalizeName(rawName)
  → verify.Verifier
  → SiteFromName
  → Sites 필터
  → LocalPath에 (HH)가 있을 때만 HourFromName
  → ExpandFile
```

`IsPartFile`은 반드시 `NormalizeName`보다 먼저 실행한다. `.part`와
`.filepart`가 완성 파일로 오인되어서는 안 된다.

### 5.2 로컬 대상 판정

로컬 저장소는 최소한 다음 계약을 제공한다.

```go
type LocalStore interface {
    EnsureDir(ctx context.Context, dir string) error
    Stat(ctx context.Context, path string) (fs.FileInfo, error)
    Size(ctx context.Context, path string) (int64, error)
    Rename(ctx context.Context, oldPath, newPath string) error
    Remove(ctx context.Context, path string) error
    Join(dir, name string) string
}
```

`decide(remoteSize, localStat)`의 결과는 다음 네 가지다.

| 로컬 최종 경로 상태 | 결과 |
|---|---|
| 없음 | 수신 |
| 일반 파일, 크기 같음 | 기존 |
| 일반 파일, 크기 다름 | 충돌 |
| 디렉터리·링크·비정규 대상 | 비정상 대상 |

권한 오류 등 “없음” 이외의 Stat 오류는 판정값으로 숨기지 않고 파일 실패로
올린다.

### 5.3 회차 내 목적지 중복

서로 다른 원격 경로가 같은 로컬 최종 경로로 수렴할 수 있으므로 worker에
보내기 전에 최종 경로 기준으로 중복을 제거한다.

- Scanner가 먼저 발견한 항목이 승자다.
- 뒤의 항목은 `duplicate_destination`으로 집계한다.
- 같은 `.part`를 두 worker가 동시에 쓰는 상태를 허용하지 않는다.

### 5.4 완료 조건

- D3: `Sites=SOCH`이면 SOCH만 후보로 남고 나머지는 `사이트제외`다.
- D7: 기존 최종 일반 파일의 크기가 같으면 `기존`이다.
- D8: 크기가 다르면 `충돌`이며 덮어쓰지 않는다.
- D18: `*.part`와 `*.filepart`는 NormalizeName 전에 제외된다.
- 동일 목적지로 수렴한 후보 두 건 중 한 건만 작업 대상으로 남는다.
- 동일 이름의 디렉터리·링크는 기존 파일로 오인되지 않는다.

---

## 6. 커밋 5 — DOWNLOAD Runner

### 6.1 worker pool

- `MaxWorkers`개의 고정 worker를 사용한다.
- Scanner와 후보 판정은 순차로 진행한다.
- worker는 공용 Report map이나 카운터를 직접 수정하지 않는다.
- worker 결과는 채널로 보내고 단일 goroutine이 Report에 합산한다.
- 회차 ctx가 취소되면 producer와 worker는 새 다운로드를 시작하지 않는다.

### 6.2 파일 하나의 처리 순서

```text
초기 최종 경로 Stat
  → 기존/충돌/비정상이면 종료
  → 목적지 폴더 EnsureDir
  → DownloadPart(remote, final + ".part")
  → local .part Size == 원격 Entry.Size 확인
  → Rename 직전 최종 경로 Stat 재확인
  → 여전히 없을 때만 Rename(.part, final)
  → 최종 성공 집계
```

Rename 직전 재확인 결과는 다음처럼 처리한다.

- 같은 크기의 일반 파일이 생김: `.part` 삭제 후 `기존`
- 다른 크기의 일반 파일이 생김: `.part` 삭제 후 `충돌`
- 디렉터리·비정규 대상이 생김: `.part` 삭제 후 `비정상 대상`

공통 lock은 같은 프로그램의 중복 실행을 막고, Rename 직전 재확인은 전송
중 외부 파일 생성 가능성을 줄인다. 외부 프로세스가 재확인과 Rename 사이에
파일을 만드는 극단적인 TOCTOU는 전용 DOWNLOAD LocalPath 운영 전제로
남기고 문서화한다.

### 6.3 실패 후 `.part` 정리

취소된 회차에서도 정리를 시도하되 무기한 대기하지 않는다.

```go
cleanupCtx, cancel := context.WithTimeout(
    context.WithoutCancel(ctx),
    cleanupTimeout,
)
defer cancel()

removeErr := local.Remove(cleanupCtx, partPath)
```

- 전송·크기 검증·Rename 전 실패는 `.part` 삭제를 시도한다.
- cleanup 실패는 원래 실패 원인에 추가해 파일별 상세 로그에 남긴다.
- 다음 회차의 `DownloadPart`는 남은 `.part`를 TRUNC하고 처음부터 받는다.

### 6.4 완료 조건

- D1: 정상 파일이 `(SITE)`·`(HH)`가 전개된 목적지에 생긴다.
- D2: 같은 설정으로 재실행하면 수신 0이고 기존으로 집계된다.
- D4: `.part`·0바이트·Grace 미달·미래 mtime은 제외만 되고 다음 회차에서
  다시 평가된다.
- D5: 회차 ctx 취소 후 새 수신을 시작하지 않으며, 정상 cleanup 환경에서는
  최종 파일과 `.part`가 모두 남지 않는다.
- D6: `.part` 크기가 원격 Entry.Size와 다르면 Rename하지 않고 `.part`를
  삭제하며 실패로 집계한다.
- D9: 이전 회차의 `.part`를 처음부터 덮어쓴 뒤 정상 완료한다.
- Rename 직전에 생긴 기존 파일을 자동 덮어쓰지 않는다.
- 같은 목적지에 대한 동시 DownloadPart 호출이 없다.
- `go test -race ./...`가 통과한다.

---

## 7. 커밋 6 — DOWNLOAD 보고 포맷

### 7.1 출력 원칙

- `[DOWNLOAD]` 고정 태그를 유지한다.
- 사람이 읽는 필드명은 한국어로 출력한다.
- 0인 항목은 출력하지 않는다.
- 처리 집계가 모두 0이면 여러 `항목=0` 대신 `대상없음`을 출력한다.
- Category·범위·소요시간은 항상 출력한다.
- 내부 사유를 표시명으로 바꾼 뒤 고정 순서로 출력한다.
- 표시에서 생략하더라도 Report의 원래 숫자는 유지한다.

표시 순서는 DOWNLOAD v3 §10.1을 따른다.

```text
원격 → 수신 → 기존 → 사이트제외 → 사이트식별불가 → 유예중
→ 미래시각 → 임시파일 → 0바이트 → 종류불일치 → 충돌
→ 비정상대상 → 목적지중복 → 실패
```

기존 PUT·resend 로그 형식과 문구는 바꾸지 않는다.

### 7.2 완료 조건

- D15: 0인 항목명과 빈 묶음은 출력하지 않지만 Report 숫자는 보존된다.
- 모든 처리 집계가 0이면 `대상없음`만 출력한다.
- 사유 출력 순서는 map 순회에 영향을 받지 않는다.
- D16: Mode=put의 기존 로그 기대값이 한 글자도 바뀌지 않는다.

---

## 8. 커밋 7 — main 배선과 DOWNLOAD 게이트 개방

### 8.1 게이트와 실행 분기

- `downloadImplemented = true`로 바꾼다.
- `Mode=both`는 계속 시작 거부한다.
- `--seed`, `--dry-run`, `--transport=localfs`의 DOWNLOAD 조합은 §1.3대로
  명시적으로 거부한다.
- PUT과 DOWNLOAD의 접속·watchdog 수명 배선은 공통 helper로 정리하되,
  PUT은 기존 `DialSFTP`, DOWNLOAD는 `DialSFTPReadOnly`를 사용한다.
  〔커밋 8 기록: 실제로는 공통 helper 를 만들지 않았다. PUT 코드(main.go)를 건드리지
  않기 위해 DOWNLOAD 배선과 Hot/Deep 판정을 `cmd/rinexclient/download.go`에 따로
  두었다(의도적 중복, 같은 규칙·같은 로그 문구). 규칙을 바꿀 때는 두 곳을 함께
  고친다. DOWNLOAD 를 되돌릴 때 이 파일과 main 의 분기만 빼면 되는 구조다.〕

### 8.2 DOWNLOAD 실행 순서

```text
config Load·Validate·CheckEnvironment
  → 공통 lock 획득
  → DOWNLOAD SFTP 읽기 전용 접속
  → runCtx와 WatchStall 시작
  → download.Runner.Run
  → DOWNLOAD 보고
  → SFTP Close
  → lock 해제
```

가능하면 원격 접속 전에 lock을 획득해, 이미 다른 회차가 실행 중일 때
불필요한 SSH 접속을 만들지 않는다. 기존 PUT 순서 변경이 필요하다면 별도
회귀 테스트로 고정한다.

stall 발화 순서는 기존 PUT 계약과 같다.

```text
cancelRun
  → Abort
  → 블록된 원격 호출 해제
  → worker 종료와 .part cleanup
  → 회차 반환
```

DOWNLOAD 실패가 1건 이상이면 기존 PUT과 같은 부분 실패 종료 코드를
사용한다. stall 회차는 성공으로 위장하지 않는다.

〔커밋 8 기록, 2026-09-26〕 별도의 "부분 실패 종료 코드"는 없고 종료 코드는 0/1
뿐이다. 커밋 7 은 파일 수신 실패·원격 나열 실패(`Report.Failed() > 0`)가 있으면
보고를 모두 출력한 뒤 1 로 끝내도록 구현했다(`errDownloadPartial`,
`TestRunDownload_FileFailureReturnsPartialFailure`). 설정·접속·lock 오류와 stall·
취소도 1, 정상과 lock 양보는 0 이다.

이 점은 **PUT 과 다르다.** PUT 은 파일 실패에도 0 이다(2026-09-22 확정). 작성 중
PUT 과 같이 0 으로 두는 A안을 적은 적이 있으나 코드는 1 이다. 알려진 비용: 설정
오류와 파일 한 건 실패가 같은 코드가 되고, 기관이 권한을 막아 둔 원격 폴더 하나만
있어도 작업 스케줄러에 매시간 실패가 찍히며 화면에 `[FATAL]`이 뜬다. 현장 로그를
보고 재검토한다.

### 8.3 완료 조건

- D10: fake ProgressSource에서 stall이 발화하면 `cancelRun → Abort` 순서로
  회차가 끝난다.
- D12: Mode=put이면 DOWNLOAD 접속·로그·파일 생성이 없다.
- D12 역방향: Mode=download이면 PUT Ledger DB를 열거나 만들지 않는다.
- Mode=download의 금지 CLI 조합이 실행 전에 명확한 오류로 끝난다.
- D13: 기존 PUT 테스트 전체가 통과한다.
- D14: 실제 SFTPGo에서 원격 목록 → GET → 로컬 `.part` 크기 검증 → 로컬
  최종 Rename을 확인한다.

D14의 마지막 Rename은 원격 SFTP Rename이 아니라 로컬 파일시스템의
`.part → final` Rename이다.

---

## 9. 커밋 8 — 문서와 운영 예시

〔2026-09-26〕 커밋 8 은 둘로 나눈다. 아래 항목 중 현장 결과가 필요한 것은 미검증
사실을 확정 표현으로 남기지 않기 위해(§9.2) 28일 사무실 검증 뒤로 미룬다.

| 커밋 | 범위 | 상태 |
|---|---|---|
| 8a | `config.example.ini` 정리, DOWNLOAD v3 정정(§14 목록), PATH v3 D1 번복 기록, PATH v4 §8 상태, 이 계획서의 실제와 다른 곳 기록, 예제 설정 검증 테스트 | 완료 |
| 8b | D14·D17 결과, 원격 시계 차이·원본 생성 방식(v3 §5.4), `GraceSeconds` 확정값과 근거, 한국어 로그 인코딩 결과, 서울시 DOY 폴더 구조 | 28일 이후 |

### 9.1 변경 내용

- `config.example.ini`에 `[GENERAL] LockPath`, `[DOWNLOAD]`,
  `[DOWNLOAD.SFTP]`, `[DOWNLOAD.<CATEGORY>]` 예시를 추가한다.
- DOWNLOAD v3 §7.1-2의 “원본 RemotePath에는 날짜 토큰만 허용”을
  PATH v4의 원본 토큰 허용 정책으로 정정한다.
- PATH v4 §8-1의 남은 문서 정정을 반영한다.
- 실제 SFTPGo 검증 절차와 결과를 기록한다.
- 원격 시계 차이와 원본 생성 방식 확인 결과를 DOWNLOAD v3 §5.4에 기록한다.
- 현장 확정 Grace 값과 그 근거를 예제 설정에 반영한다.
- Windows 콘솔·작업 스케줄러 로그와 Linux 로그의 한국어 인코딩 결과를
  기록한다.

### 9.2 완료 조건

- 예제 config만 보고 DOWNLOAD 전용 설정을 구성할 수 있다.
- 문서의 토큰 허용 정책과 실제 config 검증이 일치한다.
- 문서의 실행 순서와 main 배선이 일치한다.
- 미검증 사실을 확정 표현으로 남기지 않는다.

---

## 10. 테스트 매핑

| 시나리오 | 주 커밋 | 기대 |
|---|---:|---|
| D1 정상 원격 파일 | 5 | SITE·HH 목적지에 최종 파일 생성 |
| D2 동일 설정 재실행 | 5 | 수신 0, 기존 집계 |
| D3 Sites 필터 | 4 | 지정 관측소만 후보 |
| D4 임시·0·Grace·Future | 5 | 제외 후 다음 회차 재평가 |
| D5 전송 중 취소 | 5 | 신규 착수 중단, 최종 파일 없음, cleanup 시도 |
| D6 `.part` 크기 불일치 | 5 | Rename 금지, cleanup, 실패 |
| D7 기존 파일 크기 같음 | 4 | 기존 |
| D8 기존 파일 크기 다름 | 4 | 충돌, 덮어쓰기 없음 |
| D9 잔여 `.part` | 5 | TRUNC 후 정상 완료 |
| D10 stall | 7 | cancel 후 Abort, 회차 종료 |
| D11 Ping-Pong | 1 | config 시작 거부 |
| D12 Mode 격리 | 7 | 반대 방향 자원 미사용 |
| D13 PUT 회귀 | 전체 | 기존 테스트 전부 통과 |
| D14 실제 SFTPGo | 7·8 | 목록·GET·크기·로컬 Rename 확인 |
| D15 DOWNLOAD 보고 | 6 | 0 생략·한국어·고정 순서 |
| D16 PUT 보고 | 6 | 기존 출력 불변 |
| D17 원격 시계 차이 | 8 | 미래·유예 집계 확인, Grace 확정 |
| D18 임시 접미사 판정 순서 | 4 | NormalizeName 전에 제외 |
| 추가: 목적지 중복 | 4·5 | 한 worker만 다운로드 |
| 추가: Rename 전 경합 | 5 | 새 최종 파일을 덮어쓰지 않음 |

---

## 11. 일정과 검증 장소

### 집에서 완료할 범위

- 커밋 1~6 전체
- 커밋 7의 main 배선과 fake/in-process 테스트
- 전체 단위·통합·race 테스트

in-process SFTP 서버로 실제 SFTP 패킷 경로를 통과시켜 다음 항목을 미리
닫는다.

- 원격 나열과 재귀 Scan
- GET과 `.part` 기록
- ctx 취소
- 크기 검증과 로컬 Rename
- 오류 정규화
- stall 시 pipe 종료

### 28일 회사에서 완료할 범위

1. 실제 측위원형 SFTPGo 인증과 known_hosts 확인
2. 실제 RemotePath 목록과 GET 확인
3. 파일·디렉터리 권한 확인
4. 원격 서버와 DOWNLOAD PC의 시계 차이 측정
5. 원본 생성 프로그램이 임시 이름 후 Rename하는지 확인
6. `GraceSeconds=0` 사용 가능 여부와 최종 값 확정
7. 실제 파일 크기와 수량에서 `MaxWorkers` 처리량 확인
8. 작업 스케줄러 로그의 한국어 인코딩 확인
9. D14·D17 결과를 커밋 8 문서에 기록

---

## 12. 커밋 목록

| # | 패키지 | 핵심 내용 |
|---:|---|---|
| 1 | config | DOWNLOAD 설정, Mode별 검증, Grace·LockPath, Ping-Pong |
| 2 | transport | 읽기 전용 Dial, ReadDir, DownloadPart, in-process SFTP |
| 3 | download | RemoteFS와 scan.DirLister 어댑터 |
| 4 | download | 순수 판정, LocalStore, 목적지 중복 제거 |
| 5 | download | worker pool, `.part → Size → 재확인 → Rename`, cleanup |
| 6 | download | 한국어·0 생략 보고 포맷 |
| 7 | config·main | 게이트 개방, 모드 분기, lock·stall·종료 코드 |
| 8 | docs | 예제 설정, 설계 정합, 현장 검증 결과 |

이 계획에서 BOTH, Retention, 수동 강제 재수신, DOWNLOAD 이력 DB는 구현하지
않는다. 해당 요구가 실제로 발생하면 DOWNLOAD v3 §8·§9·§12에 따라 별도
설계와 커밋 계획을 만든다.
