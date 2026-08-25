# SFTPClient 프로젝트 최종 개발 지침

> 이 문서는 SFTPClient 개발 시 ChatGPT, Claude Code, Cursor가 공통으로 따라야 하는 최종 개발 기준이다.
> `Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx`의 설계 내용은 기준 문서로 유지한다.
> 단, 실제 개발 순서는 원문과 달리 **PUT → DOWNLOAD → BOTH → PUT 성능 개선 → Linux 대응**으로 진행한다.
> 이 개발 순서의 차이 외에 원문 요구사항을 임의로 삭제·축소·대체하지 않는다.

### 관련 문서

| 문서 | 역할 |
|---|---|
| `Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx` | 설계 기준 문서 |
| 본 문서 | 개발 지침 |
| `internal/ledger/schema.sql` | Ledger 물리 스키마 **원본** |
| `docs/SFTPClient_LEDGER_CONCEPT.md` | Ledger 개념·논리 모델과 결정 근거 |

**용어 대응 — 설계안의 `file_id` ↔ 구현의 `file_name`.**
설계안 9.1은 식별자를 `SHA-256( Domain │ Category │ NormalizedName )` 해시로 정의하고 `file_id`라 명명했으나,
구현에서는 정규화한 파일명 자체를 식별자로 사용하며 컬럼명도 실체에 맞춰 `file_name`으로 둔다.
변경 근거는 `docs/SFTPClient_LEDGER_CONCEPT.md` 4.1에 기술한다.
설계안 원문을 참조할 때는 두 이름을 같은 것으로 읽는다.

**전송 상태 표기는 `VERIFIED`로 통일한다.**
설계안 8.1·13.2는 `SUCCESS`, 9.3은 `VERIFIED`로 표기가 엇갈린다.
상태 전이를 명시적으로 정의한 9.3을 따른다. 두 이름을 코드에 혼용하지 않는다.

## 1. 응답 언어

- 모든 응답, 설명, 코드 설명, 주석 제안, 오류 분석, 테스트 결과 설명은 한국어로 작성한다.
- 존댓말을 사용한다.
- 기술 용어(goroutine, channel, interface, Ledger 등)와 코드 식별자는 원어를 유지한다.
- 확인되지 않은 사실을 지어내지 않는다. 모르면 모른다고 명확히 말한다.

## 2. 개발 환경 및 Go 기준

- 개발 언어: **Go 1.27.0**
- IDE: **IntelliJ IDEA Ultimate + Go 플러그인**
- Windows 우선 개발 후 Linux로 확장한다.
- 현장 PC에는 Go를 설치하지 않고 빌드된 Binary를 배포한다.
- 폐쇄망 배포를 고려하여 가능하면 `CGO_ENABLED=0` 빌드를 유지한다.
- SQLite 드라이버는 설계안 기준 `modernc.org/sqlite`를 사용한다.
- 모든 코드는 `gofmt` 적용 상태를 유지한다.
- Go 표준 라이브러리와 표준적인 Go 관례를 우선한다.

### Go 코드 스타일

- Effective Go 및 Go Code Review Comments 관례를 따른다.
- 패키지명은 소문자 단수형을 사용하고 밑줄·대문자를 사용하지 않는다.
- 인터페이스는 가능하면 사용하는 쪽(consumer) 패키지에서 선언한다.
- 인터페이스는 필요한 메서드만 가지도록 작게 유지한다.
- Getter에 불필요한 `Get` 접두어를 붙이지 않는다.
- 취소와 타임아웃은 `context.Context`를 함수 첫 번째 인자로 전달한다.
- 로깅은 `log/slog`를 사용한다.
- 다중 에러 결합이 필요한 경우 `errors.Join`을 사용한다.
- 매직 넘버와 매직 문자열을 피하고 설정값 또는 명명된 상수로 관리한다.
- 기관별 차이는 Core 코드의 기관명 조건문이 아니라 `config.ini`로 처리한다.

## 3. SOLID 적용 원칙

- **SRP**: 탐색, 판정, 전송, 이력, 설정, 보안을 책임별 패키지로 분리한다.
- **OCP**: 기관별 경로 또는 RINEX Category 확장은 가능한 한 Config 추가로 처리한다.
- **LSP**: 동일 인터페이스 구현체를 교체해도 상위 흐름이 깨지지 않아야 한다.
- **ISP**: PUT과 DOWNLOAD가 필요하지 않은 메서드까지 강제로 의존하는 큰 인터페이스를 만들지 않는다.
- **DIP**: 상위 로직은 구체 구현체보다 작은 인터페이스에 의존한다. 실행 시점의 구현체 조립(wiring)은 `cmd/rinexclient/main.go`에서 담당한다.
- SOLID를 이유로 불필요한 추상 계층을 추가하지 않는다. MVP 구현 속도와 유지보수성 사이의 균형을 우선한다.

## 4. 최종 프로젝트 구조

```text
SFTPClient/
│
├─ cmd/
│   └─ rinexclient/
│       └─ main.go              프로그램 진입점. 설정 로드 → 구현체 생성 → 의존성 주입 → 실행.
│                               조립(wiring)만 담당하며 비즈니스 로직을 두지 않는다.
│                               실행 시점의 구체 구현체 조립을 담당한다. (DIP)
│
├─ internal/                    외부 모듈에서 import 불가. Go가 언어 차원에서 강제한다.
│   │
│   ├─ config/                  config.ini 파싱 및 시작 시 유효성 검사.
│   │                           Mode, MaxWorkers, Path Template 원문, 인증 경로, 필터값을 구조체로 변환.
│   │                           RepostDownloaded와 경로 겹침 같은 위험 조합을 시작 시 거부. (설계안 10, 10.1)
│   │
│   ├─ domain/                  프로젝트의 핵심 개념 정의. File, Category, Status, Origin, file_name.
│   │                           경로 비의존 식별자 계산이 여기 있다. (설계안 9.1, CONCEPT 4.1)
│   │                           파일명 정규화는 이 패키지의 함수 하나에서만 수행한다.
│   │                           상태 문자열(VERIFIED, READY, LOCAL 등)은 전부 여기 상수로 선언하고
│   │                           다른 패키지는 리터럴을 직접 쓰지 않는다.
│   │                           내부 패키지를 하나도 import하지 않는다. 의존 그래프의 최하단.
│   │
│   ├─ pathpl/                  (YYYY)/(DOY)/(HH)/(SITE) 토큰을 실제 경로로 확장. (설계안 10)
│   │                           입출력만 있는 순수 함수. PUT과 DOWNLOAD가 공유한다.
│   │                           단위 테스트가 가장 쉬운 패키지.
│   │
│   ├─ verify/                  판정 로직 전담. 전송도 기록도 하지 않고 "정상인가"만 답한다.
│   │                           Ingress  — size>0, mtime grace, 작성 중 파일 제외 (설계안 7)
│   │                           Transfer — 원본/목적지 Size 대조, 최종 파일 존재 확인 (설계안 8)
│   │
│   ├─ transport/               파일을 실제로 옮기는 계층. 설계안 4절의 SFTP Core.
│   │                           sftpfs.go  — SSH 공개키 접속, known_hosts, .part 업로드, Rename
│   │                           localfs.go — 로컬 디렉터리 구현. SFTP 없이 개발·테스트할 때 교체 투입
│   │                           put/download를 모른다. 방향을 알지 못하고 파일만 옮긴다.
│   │
│   ├─ logging/                 log/slog 설정. 출력 대상, 레벨, 보존 정책. (설계안 14)
│   │                           성공은 집계, 실패·재시도는 상세 원인 기록.
│   │
│   ├─ put/                     송신 흐름 조립. Scan → Ingress 검증 → 대상 선정 → 전송 → Transfer 검증.
│   │                           필요한 원격 동작을 인터페이스로 직접 선언한다(consumer-side).
│   │                           transport를 import하지 않으므로 fake 주입으로 전체 테스트 가능.
│   │
│   ├─ pipeline/       [다음 주] Worker Pool과 Global Limiter. (설계안 12.1)
│   │                           버퍼드 채널 세마포어로 전체 동시 SFTP 작업 수를 제한.
│   │                           PUT/DOWNLOAD가 같은 Limiter 인스턴스를 공유한다.
│   │                           이번 주는 worker=1 순차 루프로 대체.
│   │
│   ├─ ledger/                  common/put/download Ledger. (설계안 9)
│   │                           schema.sql — 물리 스키마 원본. go:embed로 실행파일에 포함하고
│   │                                        시작 시 실행한다. 스키마는 이 파일이 유일한 원본이며
│   │                                        Go 코드에 CREATE TABLE 문자열을 중복해 두지 않는다.
│   │                           [이번 주] common/put Ledger를 worker=1 순차 처리 기준으로 구현.
│   │                           [다음 주] 단일 Writer 고루틴 + 배치 커밋 + WAL + IN_PROGRESS 복구,
│   │                                     download Ledger까지 확장.
│   │
│   ├─ download/       [다음 주] 수신 흐름 조립. put과 대칭 구조.
│   │                           Origin=DOWNLOAD 기록으로 Ping-Pong 방지에 관여. (설계안 9.1)
│   │
│   └─ security/       [나중]   자격증명 및 설정값 보호. (설계안 15.1)
│                               protector.go       — 인터페이스 및 enc: 접두어 처리
│                               dpapi_windows.go   — Windows DPAPI 구현
│                               dpapi_other.go     — 비Windows 스텁. Linux 빌드 보호
│
├─ docs/
│   └─ SFTPClient_LEDGER_CONCEPT.md
│                               Ledger 개념·논리 모델. 엔티티 정의, 관계, 식별자 근거,
│                               의도적 비정규화 사유, 미결 항목, 설계안 대비 변경 요약.
│                               schema.sql과 짝이며 항상 함께 갱신한다.
│
├─ config.example.ini           설정 템플릿. 실제 config.ini는 커밋하지 않는다.
├─ .gitignore                   config.ini, keys/, logs/, data/, *.db, *.db-wal,
│                               *.db-shm, *.exe 등을 제외한다.
└─ go.mod                       모듈 경로, Go 버전 및 의존성 목록.
```

### 패키지 책임 및 의존 방향

- `domain`은 어떤 `internal` 패키지도 import하지 않는다.
- `transport`는 `put`과 `download`를 알지 못한다.
- `put`과 `download`는 필요한 인터페이스를 consumer-side에서 선언한다.
- PUT에만 필요한 로직은 `put`에 둔다.
- DOWNLOAD에만 필요한 로직은 `download`에 둔다.
- 양쪽에서 사용하는 기능은 `domain`, `pathpl`, `verify`, `transport`, `ledger`, `config`, `logging` 등 공통 패키지로 분리한다.
- 동일 기능을 PUT과 DOWNLOAD 양쪽에 복사하여 중복 구현하지 않는다.
- `utils`, `helper`, `common`처럼 책임이 불명확한 범용 패키지를 만들지 않는다.
- `internal/sftp`라는 패키지명은 사용하지 않는다. `github.com/pkg/sftp`와 이름 충돌을 피하기 위해 `transport`를 사용한다.

### Ledger 스키마 취급

- `internal/ledger/schema.sql`이 스키마의 유일한 원본이다. DB 파일은 그로부터 파생된 산출물로 취급한다.
- 스키마를 변경할 때는 `schema.sql`을 먼저 수정하고, 파일 상단의 개정 이력에 한 줄을 추가한다.
- 스키마 변경과 `docs/SFTPClient_LEDGER_CONCEPT.md` 갱신은 같은 커밋에서 처리한다.
  근거 없이 바뀐 스키마는 시간이 지나면 사고가 된다.
- `ALTER TABLE` 수행 시 SQLite가 저장된 `CREATE` 문을 재작성하면서 주석이 손실될 수 있으므로,
  DB 파일을 직접 고치지 않는다.
- 접속 시 다음 PRAGMA를 반드시 실행한다. `foreign_keys`는 기본값이 OFF이며,
  켜지 않으면 스키마의 FK 선언과 `ON DELETE CASCADE`가 아무 일도 하지 않는다.

```sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous  = NORMAL;
PRAGMA busy_timeout = 5000;
```

- 다음 두 항목은 스키마가 강제하지 못하므로 코드 리뷰에서 확인한다.
  - `remote_path` / `part_path`는 전송 완료 후가 아니라 `IN_PROGRESS` 전환 트랜잭션 안에서 기록한다.
    완료 후에 기록하면 중단된 항목이 NULL로 남아 잔여 `.part` 정리 대상을 찾지 못한다. (설계안 9.3, 13.2)
  - `put_ledger`의 FK는 `file_name`만 참조하므로 `revision` 정합성은 강제되지 않는다.
    `revision` 값은 항상 `common_ledger`에서 읽은 값을 그대로 사용하고 직접 구성하지 않는다.

### 아직 결정되지 않은 구조 항목

설계안에 요구사항이 있으나 위 구조에 자리가 정해지지 않은 항목이다. 착수 전에 확정한다.

| 항목 | 설계안 | 현황 |
|---|---|---|
| Hot Scan / Deep Scan 분리 | 11.1 (18.1 확정 항목) | `put/` 내부에 둘지 `scan/`으로 분리할지 미정 |
| 중복 실행 방지 + Stale Lock | 14 | 담당 패키지 미정 |
| `db status` / `db failed put` / `db query` 조회 명령 | 9.2 | 담당 패키지 미정 |
| 대량 유입 검증 기준 | 13 (18.1 확정 항목) | 7절이 단위 테스트만 다루고 있어 보완 필요 |

Scan은 PUT과 DOWNLOAD가 모두 사용하므로, `put/` 안에 두는 경우에도
Scanner 인터페이스를 consumer-side로 선언하여 나중에 분리할 수 있는 형태를 유지한다.

## 5. 실제 개발 순서

원문 설계안의 개발 순서는 DOWNLOAD → PUT이지만, 실제 개발은 아래 순서로 진행한다.

```text
MVP 1: PUT
MVP 2: DOWNLOAD
MVP 3: BOTH
MVP 4: PUT 성능 개선
MVP 5: Linux 대응
```

### 현재 MVP 1 PUT 구현 순서

```text
Local Scanner
    ↓
Ingress Verification
    ↓
Common Ledger
    ↓
PUT 처리(worker=1 순차)
    ↓
SFTP .part Upload
    ↓
Remote Size 검증
    ↓
Rename
    ↓
최종 파일 존재 확인
    ↓
put_ledger VERIFIED/FAILED 기록
```

- 이번 주에는 **정확성, 무결성, 중복 방지, 실패 추적**을 우선한다.
- 이번 주 PUT은 `worker=1` 순차 처리로 구현할 수 있다.
- 다음 주 `pipeline`에서 bounded Worker Pool과 Global Limiter를 추가한다.
- Worker Pool 추가 시 Ledger 쓰기는 Channel → 단일 Ledger Writer 고루틴으로 전환한다.
- 성능 튜닝은 MVP 4 이전에 과도하게 진행하지 않는다.

## 6. 오류 처리

- 반환된 `error`를 무시하지 않는다.
- 에러에 문맥을 추가할 때는 `fmt.Errorf("...: %w", err)`를 사용한다.
- 에러 메시지는 소문자로 시작하고 불필요한 마침표를 붙이지 않는다.
- 에러 판별은 `errors.Is` / `errors.As`를 사용한다.
- Retry 가능한 오류와 즉시 실패해야 하는 오류를 구분한다.
- 라이브러리 성격의 코드에서 `panic`을 사용하지 않는다.
- goroutine Worker 진입점에는 필요 시 `recover`를 적용하여 한 작업의 panic이 전체 프로세스를 종료시키지 않도록 한다.
- 파일 `Close()` 등 중요한 종료 오류를 무시하지 않는다.
- 전송 함수가 오류 없이 끝났다는 사실만으로 성공 처리하지 않는다.
- 목적지 파일 존재와 Size 검증 후 Ledger를 `VERIFIED`로 확정한다.
- 상태 문자열은 `domain` 패키지 상수로만 참조하고 리터럴을 직접 쓰지 않는다.

## 7. 테스트 실행

- 테스트 파일명은 `_test.go`로 작성한다.
- 테이블 드리븐 테스트를 기본으로 하고 `t.Run()`으로 케이스를 분리한다.
- 외부 SFTP 서버 의존은 인터페이스/fake/localfs 구현으로 격리한다.
- 테스트가 불필요하게 `time.Sleep`에 의존하지 않도록 한다.
- 다음 영역은 반드시 테스트한다.
  - `file_name` 정규화 규칙 (소문자 통일, `.part` 제거, 압축 확장자 유지, 경로 비의존)
  - RINEX 파일명 파싱 (필드 5개/6개 가변. 항법 파일에는 샘플링 필드가 없다)
  - Path Template 토큰 확장
  - Ingress Verification
  - Transfer Verification
  - Ledger 상태 전이 및 중복 방지
  - 후보 선정 쿼리 — 미전송 / 전송완료 / revision 상승 / 실패 재시도 4가지 경우
  - 스키마 CHECK 제약이 잘못된 값을 실제로 거부하는지

코드 변경 후 기본적으로 다음을 실행한다.

```bash
gofmt -l .
go vet ./...
go test ./...
```

동시성 코드(`pipeline`, Ledger Writer 등)를 추가·수정한 경우 반드시 다음도 실행한다.

```bash
go test -race ./...
```

- 테스트 또는 빌드가 실패했으면 성공했다고 보고하지 않는다.
- 실패 원인과 아직 검증하지 못한 범위를 명확히 설명한다.

## 8. 보안 및 설정

- 실제 `config.ini`, 개인키, 로그, DB 파일은 Git에 커밋하지 않는다.
- `config.example.ini`만 템플릿으로 커밋한다.
- SSH 공개키 인증을 기본으로 한다.
- `known_hosts` 검증을 사용한다.
- IP/User/Password 등 설정값 암호화 요구가 있는 경우 `security` 패키지에서 처리한다.
- Windows에서는 DPAPI를 사용할 수 있도록 `Protector` 인터페이스 뒤에 OS 종속 구현을 둔다.
- `put`, `download`, `transport`가 DPAPI를 직접 알지 않도록 한다.
- 암호화 대상 정보가 로그에서 평문으로 다시 노출되지 않도록 필요 시 마스킹한다.
- Linux의 자격증명 보호 방식은 MVP 5에서 확정한다.

## 9. Git 제외 권장 항목

```gitignore
config.ini
keys/
logs/
data/
*.db
*.db-wal
*.db-shm
*.exe
```

## 10. 프로젝트 별도 지침 원문

아래 내용은 기존 프로젝트 지침 파일의 원문이다.

```text
개발할때 SOLID 원칙 준수해줘 그리고 개발언어는 GO이고   Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx 이 문서를 참고해줘 이문서가 개발 초안이야 

차이점은 문서에는 개발순서가  download -> put 이라고 적혀있는데 실제 개발은 put -> download이렇게 개발할거야

Go 버젼은 1.27.0 

ide는 인텔리제이 얼티밋 버젼이야
```

---

# [참조] Go 기반 RINEX 통합 SFTP 프로그램 설계안 Rev1.6 원문

> 아래 내용은 원본 Word 문서에서 텍스트와 표 내용을 추출한 참조 원문이다.
> 문서의 내용 자체는 임의로 수정하지 않는다.
> 그림/도식 이미지는 MDC/Markdown에 바이너리로 직접 포함하지 않으며, 원문에 존재하는 캡션과 설명 텍스트를 유지한다.

Go 기반 RINEX 통합 SFTP
프로그램 설계안

Windows 우선 개발 · Linux 확장 · SFTPGo 기반

| 문서 구분 | 설계 초안 |
| --- | --- |
| 작성일 | 2026. 08. 20. |
| 작성자 | 김동현 |
| 대상 | RINEX2·RINEX3 / Daily·Hourly |
| 핵심 기능 | PUT · DOWNLOAD · BOTH · Ledger · 검증 · 성능개선 |

0. 앞단 요약

아래 도식은 세부 구현에 앞서 프로그램의 설계 방향을 빠르게 이해하기 위한 개요도이다. 구성요소 간의 연결 구조는 4절의 전체 처리 아키텍처에서 다룬다.

그림 0. 핵심 구성요소 요약

핵심 메시지는 세 가지이다. 첫째, Ledger는 Common / PUT / DOWNLOAD 관점으로 파일 상태를 기억하여 중복을 방지한다. 둘째, Scanner는 현재 데이터뿐 아니라 늦게 들어온 과거 파일까지 탐지해 누락 복구를 가능하게 한다. 셋째, Worker는 Queue 기반 제한 병렬 처리로 적체를 줄이고 특정 종류가 전체 흐름을 막지 않도록 한다.

1. 설계 개요

본 설계는 기관별로 상이한 GNSS/RINEX 파일 저장 구조와 송·수신 환경을 하나의 Go 기반 프로그램으로 통합하기 위한 설계안이다. 단순 SFTP 파일 복사가 아니라 파일 유입 검증, 중복 방지, 송·수신 이력 관리, 장애 복구, 대량 파일 처리 성능까지 포함하여 운영 가능한 범용 RINEX 전송 클라이언트를 목표로 한다.

| 핵심 목표  RINEX 파일을 누락·중복 없이 목적지까지 전달하고, 실제 송·수신 완료 여부를 검증 가능한 상태로 기록하면서 FileZilla 수준의 실효 전송 성능을 확보한다. |
| --- |

| 항목 | 설계 방향 |
| --- | --- |
| 운영체제 | Windows 우선 개발 후 Linux 확장 |
| 전송 모드 | PUT / DOWNLOAD / BOTH |
| 데이터 | RINEX2 / RINEX3, Daily / Hourly |
| SFTP 서버 | SFTPGo 설치를 기본 전제로 함 |
| 상태관리 | SQLite 기반 Common / PUT / DOWNLOAD Ledger |
| 성능 기준 | 동일 조건에서 FileZilla 대비 현저한 성능 저하가 없을 것 |
| 배포 환경 | 폐쇄망 포함, 사전 준비된 설치/배포 패키지 반입 |

2. 개발 배경 및 해결 대상

기관마다 Windows/Linux 환경과 RINEX 저장 경로가 달라 기관별 전용 프로그램이 증가할 수 있음.

기존 전송 프로그램은 외부망/대량 파일 환경에서 처리 적체가 발생하여 RINEX2 Daily/Hourly 데이터조차 장시간 내 처리하지 못한 사례가 있음.

단순 sent 목록만으로는 파일이 실제 목적지까지 정상 전달되었는지, 실패·재시도 상태가 무엇인지 추적하기 어려움.

수신기 또는 외부 프로세스가 작성 중인 파일을 너무 일찍 집어 전송하면 불완전 파일이 전달될 위험이 있음.

PUT과 DOWNLOAD를 별도 프로그램으로 계속 관리하기보다 하나의 배포 단위에서 모드로 제어할 필요가 있음.

설정 파일에 접속 비밀번호가 평문으로 저장되어 있어, 파일이 유출되면 서버 접근 권한이 그대로 노출되는 보안 취약점이 있음. 자동 실행 환경이므로 사람이 매번 입력하는 방식으로는 대체할 수 없음.

| 설계 원칙  기관별 차이는 코드가 아니라 Config로 처리하고, 기능을 합치되 각 기능의 결합도는 낮게 유지한다. 성능 최적화는 목적이 아니라 운영 가능한 처리량을 확보하기 위한 수단으로 사용한다. |
| --- |

3. 개발 방식 - MVP 기반 반복 개발

개발은 모든 기능을 한 번에 구현하는 방식이 아니라, 각 단계마다 실제로 실행 가능한 최소 기능 제품(MVP)을 완성하고 검증한 뒤 다음 기능을 추가하는 반복적 개발 방식으로 진행한다.

각 MVP는 이전 단계의 검증된 기능을 유지하면서 기능 범위를 확장한다. 단계 종료 시 기능 테스트, 장애 복구 확인, Ledger/Log 확인 및 실제 SFTPGo 연동 테스트를 수행하고 결과를 기록한다.

| MVP 1<br>DOWNLOAD | MVP 2<br>PUT | MVP 2<br>PUT | MVP 3<br>BOTH | MVP 3<br>BOTH | MVP 4<br>PUT 성능 개선 | MVP 4<br>PUT 성능 개선 | MVP 5<br>Linux 대응 | MVP 5<br>Linux 대응 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 단계 | 단계 | 목표 | 목표 | 핵심 구현 내용 | 핵심 구현 내용 | 완료/검증 기준 | 완료/검증 기준 |  |
| MVP 1<br>DOWNLOAD | MVP 1<br>DOWNLOAD | 원격 SFTP 서버의 RINEX 파일을 로컬 서버로 안정적으로 수신 | 원격 SFTP 서버의 RINEX 파일을 로컬 서버로 안정적으로 수신 | SFTPGo 접속, 원격 경로 Scan, Path Template, DOWNLOAD Queue/Worker 기본 구조, .part 수신, Size 검증, Rename, Common/Download Ledger, Retry, Log | SFTPGo 접속, 원격 경로 Scan, Path Template, DOWNLOAD Queue/Worker 기본 구조, .part 수신, Size 검증, Rename, Common/Download Ledger, Retry, Log | 지정 데이터셋 다운로드 성공 / 중단·재실행 시 실패 파일 재시도 / Remote-Local Size 검증 및 Ledger SUCCESS 확인 | 지정 데이터셋 다운로드 성공 / 중단·재실행 시 실패 파일 재시도 / Remote-Local Size 검증 및 Ledger SUCCESS 확인 |  |
| MVP 2<br>PUT | MVP 2<br>PUT | 수신기 또는 로컬에서 생성된 정상 파일을 Target 서버로 안정적으로 송신 | 수신기 또는 로컬에서 생성된 정상 파일을 Target 서버로 안정적으로 송신 | Ingress Verification, Common Ledger READY 판정, PUT Queue/Worker, .part Upload, Size 검증, Rename, PUT Ledger, 중복 전송 방지, 기관별 경로 설정 | Ingress Verification, Common Ledger READY 판정, PUT Queue/Worker, .part Upload, Size 검증, Rename, PUT Ledger, 중복 전송 방지, 기관별 경로 설정 | 미완성 파일 전송 방지 / 동일 파일 중복 전송 방지 / Target 파일 존재·Size 일치 후 PUT Ledger SUCCESS 확인 | 미완성 파일 전송 방지 / 동일 파일 중복 전송 방지 / Target 파일 존재·Size 일치 후 PUT Ledger SUCCESS 확인 |  |
| MVP 3<br>BOTH | MVP 3<br>BOTH | DOWNLOAD와 PUT을 하나의 실행 프로그램에서 통합 운용 | DOWNLOAD와 PUT을 하나의 실행 프로그램에서 통합 운용 | Dispatcher/Orchestrator, DOWNLOAD 완료 후 PUT 순차 수행, Common·PUT·Download Ledger 상호 검증, 다운로드 파일의 재업로드(ping-pong) 방지, Mode 설정 | Dispatcher/Orchestrator, DOWNLOAD 완료 후 PUT 순차 수행, Common·PUT·Download Ledger 상호 검증, 다운로드 파일의 재업로드(ping-pong) 방지, Mode 설정 | Mode=both에서 DOWNLOAD → 검증 → PUT 정상 수행 / Ledger 기반 재전송·역전송 방지 / 장애 시 단계별 복구 확인 | Mode=both에서 DOWNLOAD → 검증 → PUT 정상 수행 / Ledger 기반 재전송·역전송 방지 / 장애 시 단계별 복구 확인 |  |
| MVP 4<br>PUT 성능 개선 | MVP 4<br>PUT 성능 개선 | 기존 장시간 적체 문제를 해결하고 FileZilla 수준의 실효 처리량 확보 | 기존 장시간 적체 문제를 해결하고 FileZilla 수준의 실효 처리량 확보 | FileZilla 기준 Benchmark, Worker 1/2/4/8 비교, bounded Worker Pool, Global Limiter, SFTP Connection/Session 재사용 방식 검증, Scan/Ledger/Transfer/Verify 구간별 계측 | FileZilla 기준 Benchmark, Worker 1/2/4/8 비교, bounded Worker Pool, Global Limiter, SFTP Connection/Session 재사용 방식 검증, Scan/Ledger/Transfer/Verify 구간별 계측 | 동일 PC·Network·SFTPGo·Dataset 조건에서 FileZilla 대비 운영상 현저한 성능 저하가 없도록 튜닝 / 최적 Worker 및 Limiter 값 확정 | 동일 PC·Network·SFTPGo·Dataset 조건에서 FileZilla 대비 운영상 현저한 성능 저하가 없도록 튜닝 / 최적 Worker 및 Limiter 값 확정 |  |
| MVP 5<br>Linux | MVP 5<br>Linux | Windows에서 검증된 핵심 로직을 Linux 환경으로 확장 | Windows에서 검증된 핵심 로직을 Linux 환경으로 확장 | Linux Build, 경로/권한 차이 처리, Key 권한, scheduler(cron/systemd 등) 적용, SFTPGo 연동, Windows/Linux 공통 Core 유지 | Linux Build, 경로/권한 차이 처리, Key 권한, scheduler(cron/systemd 등) 적용, SFTPGo 연동, Windows/Linux 공통 Core 유지 | Linux 실제 서버에서 Download/PUT/BOTH 및 Ledger/Retry/검증 동작 확인 / OS별 차이를 제외한 핵심 로직 동일성 유지 | Linux 실제 서버에서 Download/PUT/BOTH 및 Ledger/Retry/검증 동작 확인 / OS별 차이를 제외한 핵심 로직 동일성 유지 |  |

각 MVP는 실행 가능한 프로그램 상태로 종료한다. 다음 MVP 때문에 이전 MVP의 검증된 기능을 임시로 깨뜨리지 않는다.

MVP 1~3은 기능 정확성·무결성·복구성을 우선하며, 성능 병목은 MVP 4에서 동일 조건 Benchmark를 통해 집중 개선한다.

MVP 4의 목표는 FileZilla를 이기는 것이 아니라, 검증·Ledger·Retry 기능을 유지하면서 FileZilla 수준의 운영 가능한 처리량을 확보하는 것이다.

MVP 5는 기능 재개발이 아니라 Windows에서 검증된 Core를 유지한 채 OS 종속 부분만 분리·보완하는 단계로 진행한다.

3.1 단계별 검증 방식

각 MVP는 기능이 동작하는 것만으로 종료하지 않는다. 기존 프로그램의 문제가 소량 조건이 아니라 대량 조건에서 드러났으므로, 검증도 대량 유입 상태에서 수행한다.

검증 환경은 이미 구축되어 있는 BNC 서버를 파일 생성원으로 사용한다. RINEX 파일 생성 주기를 15분으로 설정하여 Hourly 기준 대비 4배의 파일을 만들어내고, 인스턴스를 다수 기동하여 실제 운영보다 높은 부하를 재현한다. 이 상태에서 프로그램이 적체 없이 파일을 수용하는지, 누락과 중복이 발생하지 않는지를 확인한다.

| 진행 원칙  MVP 1·2·3은 각 단계의 통과 기준을 충족한 것을 확인한 뒤 다음 단계에 착수한다. 통과하지 못한 상태에서 다음 단계를 시작하지 않는다. 상세 환경과 단계별 통과 기준은 13절에 기술한다. |
| --- |

4. 전체 처리 아키텍처

그림 1. RINEX Client 전체 처리 로직

전체 흐름은 Scanner가 파일을 탐색하고 Ingress/Common Ledger 검사를 거친 뒤 Dispatcher가 현재 Mode와 파일 상태에 따라 PUT 또는 DOWNLOAD 작업을 Queue에 배치하는 방식이다. Worker는 제한된 병렬도로 작업하며, Global Limiter는 전체 SFTP 동시 작업 수가 시스템·네트워크 허용치를 넘지 않도록 제어한다.

전송이 완료되어도 그 시점에는 SUCCESS로 확정하지 않는다. Transfer Verification 단계에서 목적지 파일의 존재와 Size를 대조한 뒤 Ledger 상태를 확정하며, 실패로 판정된 항목은 다음 Scan에서 재시도 대상으로 환원된다. 세부 절차는 8절에 기술한다.

| 구성요소 | 책임 |
| --- | --- |
| Scanner | LocalPath 또는 RemotePath를 탐색하여 후보 파일을 수집한다. |
| Ingress / Ledger 검사 | 작성 중 파일 여부, READY 상태, 중복 전송 여부, 파일 Origin 등을 확인한다. |
| Dispatcher | Mode, Category, Ledger 상태를 기준으로 PUT/DOWNLOAD Queue에 작업을 배치한다. |
| Queue / Workers | 파일 작업을 비동기 Queue에 넣고 MaxWorkers 범위에서 병렬 처리한다. |
| Global Limiter | PUT/DOWNLOAD Worker가 공통으로 사용하는 전체 SFTP 작업 상한을 제공한다. |
| SFTP Core | SSH/SFTP 연결, 업로드·다운로드, 디렉터리 생성, .part/Rename 등의 공통 기능을 제공한다. |
| Transfer Verification | 전송 후 Size와 최종 파일 존재를 확인한 뒤 Ledger를 SUCCESS로 확정한다. |

4.1 BOTH 모드의 기본 실행 정책

초기 운영 정책은 방향 간 자원 경합을 줄이고 다운로드된 파일의 재전송을 방지하기 위해 다음 순서를 기본으로 한다.

| DOWNLOAD 처리 → 검증 및 Ledger 반영 → PUT 대상 재판정 → PUT 처리 |
| --- |

따라서 그림 1의 두 Queue는 공통 아키텍처상 모두 존재하되, BOTH 기본 정책에서는 Dispatcher가 DOWNLOAD Batch를 먼저 처리한 후 PUT Batch를 활성화한다. 향후 동시 송·수신이 필요한 기관이 생기면 Global Limiter를 통해 총 동시 작업 수를 제한하는 방식으로 확장할 수 있다.

4.2 예상 최종 구조

그림 2. 예상 최종 구조

예상 최종 구조는 BOTH 기준을 중심으로 표현하였으며, 실제 운영에서는 Mode에 따라 PUT 또는 DOWNLOAD 단독 실행이 가능하다. 그림 1이 파일 1건이 거치는 처리 순서를 나타낸다면, 그림 2는 완성된 프로그램이 어떤 구성요소로 나뉘어 있는지를 나타낸다. 세 Ledger의 책임 분리와 주요 필드는 9절에서 상세히 기술한다.

5. 설치 위치 및 서버 구성 대응

그림 3. 기관별 설치 위치 예시

프로그램은 특정 기관의 서버 배치 형태에 종속되지 않는다. 수신기에서 바로 파일이 쌓이는 서버에 설치할 수도 있고, 해양측위정보원과 같이 1차 수신 서버 이후 중간 서버가 존재하는 구조에도 설치할 수 있다. 핵심은 RINEX Client가 자신이 설치된 서버의 LocalPath를 기준으로 정상 파일을 식별하고 이후 전송을 책임지는 것이다.

6. 지원 모드

| Mode | 입력 | 출력 | 기본 동작 |
| --- | --- | --- | --- |
| PUT | LocalPath | Remote SFTP | READY 파일을 Queue에 넣어 전송 후 검증 |
| DOWNLOAD | Remote SFTP | LocalPath | 원격 파일을 수신 후 검증 및 Ledger 반영 |
| BOTH | Remote + Local | Local + Remote | DOWNLOAD 완료 후 Ledger 반영, 이후 PUT 수행 |

Mode 변경은 실행 중 Hot Reload 방식으로 처리하지 않는다. 프로그램 종료 → config.ini 수정 → 프로그램 재시작을 운영 원칙으로 한다.

7. 수신기/외부 프로세스에서 들어온 파일 검증

수신기 → 서버 구간을 RINEX Client가 직접 전송하지 않는 경우가 존재한다. 이 경우 프로그램은 수신기 원본과의 완전한 End-to-End 동일성을 직접 증명할 수 없으므로, LocalPath에 도착한 파일이 정상적으로 완성되었는지를 Ingress Verification으로 판정한다.

| 검증 | 내용 | 내용 |
| --- | --- | --- |
| 파일 존재 | 경로에 파일이 실제 존재하는지 확인 | 경로에 파일이 실제 존재하는지 확인 |
| 크기 유효성 | Size가 0이 아닌지 확인 | Size가 0이 아닌지 확인 |
| 작성 중 여부 | 최근 mtime / Grace Time 기준으로 작성 중 파일 제외 | 최근 mtime / Grace Time 기준으로 작성 중 파일 제외 |
| Size 안정 | 필요 시 일정 간격 재확인하여 Size가 변하지 않는지 확인 | 필요 시 일정 간격 재확인하여 Size가 변하지 않는지 확인 |
| 파일명/경로 | RINEX 및 기관별 규칙에 맞는지 확인 | RINEX 및 기관별 규칙에 맞는지 확인 |
| 선택적 형식검사 | 필요 시 RINEX 기본 구조 또는 압축 확장자 검사 | 필요 시 RINEX 기본 구조 또는 압축 확장자 검사 |
| 검증 범위 한계  수신기 측 원본 Checksum을 제공하지 않는 경우, RINEX Client는 “수신기 원본과 서버 파일이 100% 동일하다”까지 증명할 수는 없다. 수신기에서 SHA-256 등의 Checksum을 함께 제공한다면 End-to-End 검증을 확장할 수 있다. | 검증 범위 한계  수신기 측 원본 Checksum을 제공하지 않는 경우, RINEX Client는 “수신기 원본과 서버 파일이 100% 동일하다”까지 증명할 수는 없다. 수신기에서 SHA-256 등의 Checksum을 함께 제공한다면 End-to-End 검증을 확장할 수 있다. |  |

8. 파일 전송 및 결과 검증

그림 4. 전송 결과 검증 흐름

전송 함수가 오류를 반환하지 않았다는 사실만으로 SUCCESS 처리하지 않는다. 실제 목적지 파일을 확인한 뒤 Ledger 상태를 확정한다.

8.1 PUT 검증 절차

Local 원본 Size 확인

Remote에 .part 임시명으로 업로드

Remote .part 존재 및 Size 확인

Local Size == Remote Size 확인

최종 파일명으로 Rename

최종 파일 존재 확인

PUT Ledger = SUCCESS

8.2 DOWNLOAD 검증 절차

Remote 원본 Size 확인

Local에 .part 임시명으로 다운로드

Local .part Size 확인

Remote Size == Local Size 확인

최종 파일명으로 Rename

최종 파일 존재 확인

DOWNLOAD Ledger = SUCCESS

Hash(SHA-256 등) 검증은 더 강한 무결성을 제공하지만 대량 파일에서 추가 I/O가 발생하므로 기본값은 Size + .part + Rename 검증으로 하고, 기관 요구가 있을 경우 선택 옵션으로 확장한다.

9. Ledger 설계

| Ledger 설계 원칙  세 Ledger가 같은 사실을 반복 저장하지 않는다. “하나의 사실에는 하나의 주인만 둔다”는 원칙으로 파일 정체성, PUT 이력, DOWNLOAD 이력을 분리하고 file_id로 상호 검증한다. |
| --- |

| Ledger | 책임 | 주요 필드 예시 |
| --- | --- | --- |
| COMMON_LEDGER | 파일 자체의 정체성 및 입고 상태 | file_id, name, path, size, mtime, origin, state, first_seen, verified_at |
| PUT_LEDGER | 송신 시도·성공·실패·검증 이력 | file_id, status, attempts, local_size, remote_size, sent_at, verified_at, error |
| DOWNLOAD_LEDGER | 수신 시도·성공·실패·검증 이력 | file_id, status, attempts, remote_size, local_size, remote_path, received_at, verified_at, error |

Common Ledger의 Origin 예시는 RECEIVER / LOCAL / DOWNLOAD이며, 다운로드로 생성된 파일은 Origin=DOWNLOAD로 기록하여 BOTH 모드에서 동일 파일이 다시 PUT되는 Ping-Pong 현상을 방지한다.

9.1 파일 식별자(file_id) 정의

file_id는 세 Ledger를 연결하는 유일한 키이므로 경로에 의존하지 않는 논리적 식별자로 정의한다. 경로를 키로 사용하면 DOWNLOAD LocalPath와 PUT LocalPath가 서로 다른 구성에서 동일한 파일이 서로 다른 file_id로 기록되어, BOTH 모드의 재전송 방지 규칙이 동작하지 않는다.

| file_id = SHA-256( Domain \| Category \| NormalizedName ) 의 앞 16바이트 Hex |
| --- |

Domain — 설치 인스턴스 식별자이며 config.ini의 [GENERAL] Domain에서 지정한다. 서로 다른 기관·노드에서 동일한 파일명이 사용될 때의 충돌을 방지한다.

Category — RINEX2_DAILY / RINEX2_HOURLY / RINEX3_DAILY / RINEX3_HOURLY

NormalizedName — 파일명만 사용하고 디렉터리 경로는 제외한다. 대소문자는 소문자로 통일하고 .part 등 임시 접미사는 제거하되, 압축 확장자(.gz, .Z)는 유지한다.

경로는 식별자가 아니라 속성으로 취급하여 COMMON_LEDGER의 local_path 및 remote_path 컬럼에 별도로 기록한다. 동일 파일이 서로 다른 경로에 존재하더라도 file_id는 하나로 유지된다.

파일 갱신(Revision) 처리

이미 등록된 file_id가 서로 다른 Size 또는 mtime으로 재관측되는 경우가 있다. 관측소에서 결측 구간을 채워 파일을 재생성하는 상황이 대표적이다. 이때는 신규 파일이 아니라 갱신으로 판정하여 COMMON_LEDGER의 state를 CHANGED로 전환하고 revision을 1 증가시킨 뒤 재전송 대상에 포함한다. PUT/DOWNLOAD Ledger는 (file_id, revision)을 키로 이력을 누적하므로 과거 전송 기록이 덮어써지지 않는다.

Ping-Pong 방지 규칙

DOWNLOAD로 생성된 파일은 COMMON_LEDGER의 origin을 DOWNLOAD로 기록한다. Dispatcher는 PUT 후보 선정 시 origin이 DOWNLOAD인 file_id를 기본적으로 제외한다. 다만 중계 목적으로 수신한 파일을 다시 송신해야 하는 구성이 존재할 수 있으므로, [GENERAL] RepostDownloaded 값을 두고 기본값을 false로 설정하여 해당 기관에서만 명시적으로 허용한다.

| 설계 판단  file_id를 경로 기반으로 정의하면 구현은 단순해지지만 BOTH 모드의 재전송 방지와 Ledger 상호 검증이 모두 성립하지 않는다. 따라서 경로 비의존 식별자를 MVP 1 착수 전 확정 항목으로 둔다. |
| --- |

9.2 SQLite 저장 방식 및 조회

세 Ledger는 논리적으로 분리하되 하나의 SQLite DB 파일에 별도 테이블로 저장한다. 별도의 PostgreSQL/MySQL DB 서버는 설치하지 않는다.

| rinex_ledger.db<br> ├─ COMMON_LEDGER<br> ├─ PUT_LEDGER<br> └─ DOWNLOAD_LEDGER |
| --- |

운영 장애 분석을 위해 RINEXClient 자체에 조회 명령을 제공하며, MVP 단계별로 필요한 조회 기능을 점진적으로 확장한다. 직접 SQL을 허용하는 경우 운영 중에는 SELECT 전용으로 제한한다.

| RINEXClient.exe db status<br>RINEXClient.exe db failed put<br>RINEXClient.exe db query "SELECT * FROM PUT_LEDGER WHERE STATUS='FAILED';" |
| --- |

9.3 SQLite 동시성 및 드라이버 정책

SQLite는 동시 Writer를 하나만 허용한다. 따라서 MaxWorkers 개의 Worker가 각자 Ledger를 갱신하는 구조에서는 database is locked 오류가 발생한다. 이는 MVP 1 단계에서 즉시 발현되는 문제이므로 다음을 설계 전제로 확정한다.

Writer 단일화 — Worker는 Ledger에 직접 쓰지 않고 처리 결과를 Channel로 전달하며, 단일 Ledger Writer 고루틴이 이를 순차 반영한다. 쓰기 경로가 하나이므로 잠금 경합 자체가 발생하지 않는다.

배치 커밋 — Ledger Writer는 N건 또는 T초 단위로 트랜잭션을 묶어 커밋한다. 파일 단위 커밋 대비 디스크 동기화 횟수가 줄어 대량 처리 시 Ledger가 병목이 되지 않는다.

Journal 모드 — 접속 시 journal_mode를 WAL로, synchronous를 NORMAL로, busy_timeout을 5000ms로 설정하여 조회 명령이 전송 중 쓰기를 차단하지 않도록 한다.

연결 분리 — 쓰기 연결은 최대 커넥션 수를 1로 제한하고, 9.2의 조회 명령은 별도의 읽기 전용 연결을 사용한다.

중단 복구를 위한 상태 전이

Ledger의 전송 상태는 PENDING → IN_PROGRESS → VERIFIED 또는 FAILED로 정의하고, 상태 갱신은 트랜잭션 안에서 수행한다. 프로그램이 비정상 종료되면 IN_PROGRESS 상태로 남은 항목과 정리되지 않은 .part 파일이 함께 존재하게 되므로, 시작 시 해당 항목을 조회하여 잔여 .part 파일을 삭제하고 상태를 PENDING으로 되돌린다. 이 절차가 없으면 .part 파일이 계속 누적되어 저장공간을 잠식한다.

드라이버 선정

SQLite 드라이버는 순수 Go로 구현된 modernc.org/sqlite를 사용한다. 널리 쓰이는 mattn/go-sqlite3는 cgo 기반이므로 Windows 개발 환경에서 Linux 바이너리를 생성하려면 별도의 크로스 컴파일 툴체인이 필요하며, 이는 16절의 동일 Core Source 유지 원칙 및 MVP 5의 전제와 충돌한다. modernc.org/sqlite는 CGO_ENABLED=0 상태에서 GOOS/GOARCH 지정만으로 빌드된다.

| set CGO_ENABLED=0<br>set GOOS=linux<br>set GOARCH=amd64<br>go build -o RINEXClient ./cmd/rinexclient |
| --- |

단일 Writer 구조에서 Ledger 쓰기량은 초당 수백 건 수준이므로 두 드라이버의 성능 차이는 전체 처리시간에 영향을 주지 않는다. 즉 이 선택은 성능이 아니라 배포 구조에 의해 결정된다.

운영체제 호환성

SQLite는 유닉스 계열에서 개발된 파일 기반 데이터베이스이며, Linux는 원래의 개발·검증 환경이다. 대부분의 Linux 배포판에 기본 포함되어 있고, 안드로이드 단말의 기본 데이터베이스로 사용될 만큼 널리 검증된 구현이다. 따라서 Windows에서 Linux로 확장할 때 데이터베이스 계층의 호환성 문제는 고려 대상이 아니다.

또한 SQLite는 별도의 데이터베이스 서버 프로세스를 필요로 하지 않는다. 프로그램이 파일 하나를 직접 읽고 쓰는 구조이므로 현장 서버에 추가로 설치하거나 관리할 구성요소가 없으며, modernc.org/sqlite를 사용하면 데이터베이스 구현이 실행파일에 포함되어 배포된다. 폐쇄망 환경에서도 실행파일 반입만으로 동작한다.

| 보관 위치 제약  Ledger DB 파일은 프로그램이 설치된 서버의 로컬 디스크에 둔다. NFS 또는 SMB로 마운트된 네트워크 저장소에 두면 파일 잠금이 정상 동작하지 않아 데이터가 손상될 수 있다. 전송 대상 RINEX 파일은 네트워크 저장소에 있어도 무방하다. |
| --- |

10. Config 및 기관별 경로 추상화

물리적인 설정 파일은 config.ini 한 개로 운영하되 GENERAL / PUT / DOWNLOAD를 논리적으로 분리한다. 기관별 폴더 구조 차이는 Path Template으로 처리하고 기관명 조건문을 Core 코드에 넣지 않는다.

| [GENERAL]<br>Mode = both<br><br>[PUT]<br>MaxWorkers = 4<br><br>[PUT.RINEX2_HOURLY]<br>LocalPath  = D:\RINEX2\(YYYY)\(DOY)\(HH)\<br>RemotePath = /RINEX2/(YYYY)/(DOY)/(HH)/<br><br>[DOWNLOAD]<br>MaxWorkers = 4<br><br>[DOWNLOAD.RINEX2_HOURLY]<br>RemotePath = /(YYYY)/RINEX2/(DOY)/(HH)/<br>LocalPath  = D:\DOWNLOAD\RINEX2\(YYYY)\(DOY)\(HH)\ |
| --- |

기본 Token 후보: (YYYY), (YY), (DOY), (MM), (DD), (HH), (SITE). 필요한 기관이 생길 때 Token을 확장한다.

10.1 수신 경로와 송신 경로의 분리

하위 폴더 구조는 기관의 공식 규칙을 따르므로 같은 기관 안에서는 수신과 송신이 동일하다. 실제 차이는 최상위 폴더에서 발생하며, 수신용과 송신용 디렉터리를 별도로 운영하는 형태가 일반적이다. 예를 들어 수신은 RNX 아래, 송신은 RNXOutgoing 아래에 위치한다.

PUT과 DOWNLOAD가 각각 독립된 Config 섹션을 가지므로 이 차이는 최상위 경로만 다르게 지정하여 처리한다. 하위 경로 표현은 동일하게 유지되므로 Token을 추가할 필요가 없다.

| [DOWNLOAD.RINEX2_HOURLY]<br>RemotePath = /RNX/(YYYY)/(DOY)/(HH)/<br>LocalPath  = D:\RINEX2\(YYYY)\(DOY)\(HH)\<br><br>[PUT.RINEX2_HOURLY]<br>LocalPath  = D:\RINEX2\(YYYY)\(DOY)\(HH)\<br>RemotePath = /RNXOutgoing/(YYYY)/(DOY)/(HH)/ |
| --- |

DOWNLOAD의 LocalPath와 PUT의 LocalPath를 동일하게 지정하면, 수신한 파일을 그대로 송신 대상으로 사용하는 중계 구성이 된다. 이 경우 프로그램은 별도의 이동이나 복사 없이 같은 디렉터리를 두 방향에서 참조한다.

중계 구성 시 필수 조건

9.1의 Ping-Pong 방지 규칙에 따라 Origin이 DOWNLOAD인 파일은 PUT 대상에서 기본적으로 제외된다. 따라서 중계 구성에서는 해당 옵션을 명시적으로 활성화해야 하며, 그렇지 않으면 수신은 정상이나 송신 대상이 하나도 선정되지 않는다.

| [GENERAL]<br>RepostDownloaded = true |
| --- |

| 경로 분리 조건  RepostDownloaded를 활성화할 경우, 송신 대상 경로가 다시 수신 Scan 범위에 포함되지 않아야 한다. 수신 경로와 송신 경로가 서로 다른 최상위 디렉터리로 분리되어 있어야 하며, 이는 선택 사항이 아니라 무한 재전송을 방지하기 위한 필수 조건이다. |
| --- |

프로그램은 시작 시 Config를 검사하여 RepostDownloaded가 활성화된 상태에서 송신 경로가 수신 Scan 범위에 포함되어 있으면 경고를 기록하고 실행을 중단한다.

11. RINEX Category 및 늦게 도착하는 파일

RINEX2 Daily

RINEX2 Hourly

RINEX3 Daily

RINEX3 Hourly

각 Category는 Enable/Disable 가능하게 한다. 과거 날짜의 누락 파일이 늦게 생성될 수 있으므로 현재 시점만 Scan하지 않고 설정된 기간(예: 30일)을 재탐색하여 Ledger에 없는 신규 파일을 자동 복구 대상으로 포함한다.

11.1 스캔 범위와 주기 분리

설정된 30일 구간 전체를 매 실행마다 탐색하면 관측소 수 × 30일 × 24시간 규모의 디렉터리 조회가 발생하여 스캔 자체가 병목이 된다. 특히 Hourly 데이터에서 부담이 크다. 따라서 스캔을 두 단계로 분리한다.

Hot Scan — 최근 ScanRecentDays(기본 2일) 구간만 탐색하며 기본 실행 주기마다 수행한다. 정상 운영 중 신규 파일은 대부분 이 구간에서 발견된다.

Deep Scan — ScanDays(기본 30일) 전 구간을 탐색하며 1일 1회, 전송량이 적은 시간대에 수행한다. Hot Scan 구간을 벗어나 뒤늦게 생성·복구된 파일을 회수한다.

Hot Scan만으로 충분하지 않은 이유는, mtime 기준 판별이 원본 시각을 보존하는 복사 방식에는 반응하지 못하기 때문이다. 과거 날짜의 파일이 원본 mtime을 유지한 채 뒤늦게 들어오면 최근 구간 탐색에서는 발견되지 않는다. Deep Scan은 이 누락을 정기적으로 회수하는 안전망 역할을 한다.

디렉터리 단위 조기 종료 — DOY 또는 시간 단위 디렉터리의 mtime이 직전 스캔 이후 변하지 않았다면 내부 탐색을 생략한다. 다만 일부 네트워크 스토리지는 디렉터리 mtime 갱신이 정확하지 않으므로 UseDirMtimeSkip 옵션으로 제어하고 기본값은 false로 둔다.

12. 성능 요구사항 및 Benchmark

기존 SFTP 프로그램은 외부망의 대량 RINEX 전송에서 장시간 적체되는 문제가 확인되었다. 따라서 신규 Client의 성능 목표는 “기존보다 빠름”이 아니라 검증된 범용 클라이언트인 FileZilla를 기준선으로 삼아 운영상 현저한 성능 저하가 없도록 하는 것이다. 이 성능 개선 및 최적화 작업은 MVP 4에서 집중 수행한다.

| 성능 합격 기준  동일 PC · 동일 Network · 동일 SFTPGo · 동일 Storage · 동일 파일 Dataset 조건에서 FileZilla와 비교해 실효 처리량이 동급 수준인지 검증한다. 아직 실측 전이므로 목표 Worker 수와 허용 편차는 Benchmark 후 확정한다. |
| --- |

| 테스트 케이스 | 목적 |
| --- | --- |
| FileZilla | 기준선 |
| 기존 SFTP Client | 현행 병목 비교 |
| RINEX Client Worker=1 | 순차 기준 |
| RINEX Client Worker=2 | 병렬성 비교 |
| RINEX Client Worker=4 | 병렬성 비교 |
| RINEX Client Worker=8 | 과도 병렬 여부 확인 |

측정 항목은 전체 처리시간, 파일 수, 총 Bytes, MB/s, 평균/최대 파일 처리시간, 성공/실패/Retry 수이며 필요 시 Scan, Ledger, Queue 대기, Transfer, Verify 시간을 분리 계측한다.

12.1 Worker Pool과 Global Limiter

무제한 goroutine을 사용하지 않고 설정 가능한 bounded Worker Pool을 사용한다. MaxWorkers는 Benchmark 결과로 결정하며, PUT과 DOWNLOAD가 동일한 SFTP 자원을 공유할 경우 Global Limiter가 전체 동시 연결/전송 작업 수를 제한한다.

| PUT Workers + DOWNLOAD Workers  →  Global Limiter  →  SFTP Core |
| --- |

12.2 Benchmark 데이터셋 구분

전송 성능의 지배 요인은 파일 크기 분포에 따라 달라진다. 따라서 Worker 수 축만으로는 병목을 식별할 수 없으며, 12절의 테스트 케이스는 아래 각 데이터셋에 대해 반복 수행한다.

데이터셋 A · 소파일 다수 — RINEX Hourly 기준 수천 개 × 수백 KB. 병목은 대역폭이 아니라 파일당 SFTP 왕복 횟수이며, Worker 수와 연결 재사용 방식이 처리량을 지배한다.

데이터셋 B · 대파일 소수 — RINEX Daily 기준 수십 개 × 수십 MB. 병목은 파일 내부 전송 처리량이며, 파일 단위 동시 요청 수와 패킷 크기가 처리량을 지배한다.

데이터셋 C · 혼합 — A와 B를 동시에 투입하여 대파일 전송이 소파일 처리를 지연시키지 않는지 확인한다.

측정에 앞서 대상 서버까지의 왕복 지연시간(RTT)을 기준값으로 기록한다. 파일 1개의 전송은 open · write · close · stat · rename 등 최소 5회의 왕복을 포함하므로, RTT가 20ms인 환경에서는 연결 1개당 초당 약 10개가 이론적 상한이 된다. 데이터셋 A에서 측정값이 이 상한에 근접했다면 성능 문제는 전송 코드가 아니라 왕복 횟수와 동시성 설계의 문제로 판정한다.

| 측정 기준  Worker 수만 바꿔가며 측정하면 데이터셋 A와 B에서 상반된 결론이 나올 수 있다. 데이터셋 축을 함께 두어야 최적값을 하나로 고정하지 않고 운영 조건별로 설정할 수 있다. |
| --- |

12.3 SFTP 계층 튜닝 파라미터

Worker 수는 파일 간 병렬도만 제어하며, 파일 내부의 전송 속도는 SFTP 클라이언트 옵션이 결정한다. Go 환경에서 표준적으로 사용되는 github.com/pkg/sftp 기준으로 다음 항목을 Config로 노출하여 Benchmark 대상에 포함한다.

MaxConcurrentRequestsPerFile — 파일 하나에 대해 동시에 발행하는 요청 수이며 기본값은 64이다.

MaxPacketUnchecked — 페이로드 최대 크기이며 기본값은 32768바이트이다. 표준 상한을 넘는 값을 허용하며 대파일 업로드에서 처리량 개선 효과가 크다. 다만 다운로드에서 과도하게 키우면 연결이 끊기는 사례가 보고되어 있으므로 업로드와 다운로드에 서로 다른 값을 적용할 수 있게 한다.

UseConcurrentReads / UseConcurrentWrites — 파일 내부 병렬 읽기 및 쓰기 활성화 여부이다.

주의할 점은 동시 쓰기가 전송할 데이터의 전체 크기를 미리 판별할 수 있을 때만 활성화된다는 것이다. 크기를 알 수 없는 Reader를 전달하면 라이브러리가 단일 스레드 전송으로 자동 하향하므로, 업로드 시에는 파일 핸들을 그대로 넘기고 크기 정보를 감추는 래퍼로 감싸지 않는다. 이 조건을 놓치면 옵션을 켜도 성능이 개선되지 않는다.

SSH 연결 정책

하나의 SSH 연결 위에 여러 SFTP 세션을 두는 방식과 Worker마다 별도 SSH 연결을 두는 방식은 특성이 다르다. 전자는 연결 수립 비용이 없으나 단일 연결의 암복호화 처리에 묶이고, 후자는 연산을 여러 코어에 분산할 수 있으나 연결 수립 비용과 서버 측 동시 세션 제한을 받는다. 데이터셋 A처럼 왕복 횟수가 지배적인 조건에서는 후자가 유리한 경우가 많으므로, 두 방식을 모두 측정한 뒤 기본값을 확정한다. 관련 SFTPGo 측 동시 세션 제한값도 함께 확인한다.

13. 테스트 환경 구축 및 단계별 통과 기준

각 MVP는 기능이 동작하는 것만으로 종료하지 않고, 대량 파일이 지속적으로 유입되는 조건에서 적체 없이 수용되는지를 확인한 뒤 다음 단계로 넘어간다. 기존 프로그램의 문제가 소량 조건이 아니라 대량 조건에서 드러났으므로, 검증 환경도 대량 유입을 재현할 수 있어야 한다.

| 테스트 목적  정상 파일 1건을 보낼 수 있는지가 아니라, 생성 속도를 처리 속도가 따라가는지를 확인한다. 즉 적체 발생 여부가 판정 대상이다. |
| --- |

13.1 BNC 기반 파일 생성 환경

이미 구축되어 있는 BNC 서버를 파일 생성원으로 사용한다. NTRIP 스트림을 수신하여 RINEX 파일을 생성하는 구조이므로, 실제 관측소 환경과 동일한 형태의 파일을 실서비스에 영향을 주지 않고 만들어낼 수 있다.

생성 주기 단축 — BNC 설정 파일의 RINEX 파일 생성 주기를 15분으로 지정한다. Hourly 기준 대비 관측소당 파일 수가 4배가 되며, 1개 인스턴스에서 하루 96개가 생성된다.

인스턴스 다중화 — 구축된 BNC 인스턴스를 다수 기동하여 파일 수를 증폭한다. 35개 인스턴스 기준 하루 약 3,360개가 생성되며, 이는 12.2의 데이터셋 A(소파일 다수) 조건에 해당한다.

생성 경로 분리 — 테스트용 인스턴스의 출력 경로와 설정 파일은 운영 중인 인스턴스와 분리한다. 15분 생성은 부하 재현을 위한 테스트 전용 설정이며 운영 인스턴스에는 적용하지 않는다.

| [BNC 테스트 인스턴스]        →  RINEX 생성(15분 주기)  →  LocalPath / RemotePath<br>[SFTPGo 테스트 서버]        →  전송 대상<br>[RINEXClient]               →  검증 대상 |
| --- |

13.2 검증 항목

적체 여부 — 단위시간당 생성 파일 수와 처리 완료 파일 수를 비교한다. Queue 대기 건수가 시간에 따라 증가 추세를 보이면 미통과로 판정한다.

누락 — 생성된 파일 수와 Ledger의 SUCCESS 건수가 일치하는지 확인한다.

중복 — 동일 file_id가 중복 전송된 건수가 0인지 확인한다.

중단 복구 — 전송 중 프로그램을 강제 종료한 뒤 재실행하여, 잔여 .part 파일이 정리되고 미처리분이 자동으로 회수되는지 확인한다.

Ledger 무결성 — 재실행 후 IN_PROGRESS 상태로 남은 항목이 0건인지 확인한다.

13.3 단계별 통과 기준

아래 기준을 통과하지 못한 단계에서는 다음 단계에 착수하지 않는다. 기능을 먼저 쌓고 성능을 나중에 확인하면, 문제가 발생했을 때 어느 단계에서 유입된 것인지 분리할 수 없기 때문이다.

MVP 1 (DOWNLOAD) — 15분 주기로 생성되는 파일을 지속 수신하는 동안 적체가 발생하지 않고, 누락 0건 · 중복 0건 · 중단 후 재시작 시 자동 회수가 확인될 것.

MVP 2 (PUT) — 동일 조건에서 송신 시 미완성 파일 전송 0건 · 중복 전송 0건이며, Target 파일의 존재와 Size가 원본과 일치할 것.

MVP 3 (BOTH) — 수신과 송신을 통합 운용한 상태에서 다운로드한 파일이 다시 송신되는 역전송 0건이며, Common·PUT·Download Ledger 간 상호 정합이 유지될 것.

MVP 4의 Benchmark는 위 세 단계가 모두 통과한 뒤 동일한 BNC 생성 환경 위에서 수행한다. 따라서 13절의 환경은 기능 검증과 성능 측정에 동일하게 사용되며, 별도의 테스트 환경을 다시 구성하지 않는다.

| 운영 서버 보호  테스트는 전용 BNC 인스턴스와 전용 SFTPGo 서버에서만 수행하며, 운영 중인 관측소 데이터 흐름과 실서비스 서버에는 부하를 가하지 않는다. |
| --- |

14. 장애 복구 및 로그 정책

| 항목 | 정책 | 정책 |
| --- | --- | --- |
| Retry | 전송/검증 실패 파일은 FAILED로 기록하고 다음 실행 또는 정책에 따라 재시도 | 전송/검증 실패 파일은 FAILED로 기록하고 다음 실행 또는 정책에 따라 재시도 |
| Lock | 동일 프로그램 중복 실행 방지, 비정상 종료 시 Stale Lock 정책 적용 | 동일 프로그램 중복 실행 방지, 비정상 종료 시 Stale Lock 정책 적용 |
| 부분파일 | 정식 파일명 대신 .part로 처리 후 검증 성공 시 Rename | 정식 파일명 대신 .part로 처리 후 검증 성공 시 Rename |
| 미완성 파일 | Grace Time / Size 안정 기준으로 다음 Scan까지 보류 | Grace Time / Size 안정 기준으로 다음 Scan까지 보류 |
| 로그 | 일반 성공은 집계 중심, 실패·Retry는 상세 원인 기록 | 일반 성공은 집계 중심, 실패·Retry는 상세 원인 기록 |
| 보존 | 로그 기본 30일, Ledger는 원본 보존기간보다 약간 길게 유지하도록 Config화 | 로그 기본 30일, Ledger는 원본 보존기간보다 약간 길게 유지하도록 Config화 |
| [SCAN] RINEX2_H found=300 new=287 skipped=13<br>[PUT] success=284 failed=3 duration=182s<br>[ERROR] file=AAAA.rnx direction=PUT attempt=3 error=timeout | [SCAN] RINEX2_H found=300 new=287 skipped=13<br>[PUT] success=284 failed=3 duration=182s<br>[ERROR] file=AAAA.rnx direction=PUT attempt=3 error=timeout |  |

15. SFTPGo 및 인증

SFTPGo는 서버 측 필수 구성요소로 설치 가능하도록 패키지/설치 절차를 준비한다.

기관별 계정 및 Home Directory/권한을 분리하여 경로 충돌과 오접근을 방지한다.

자동 실행 환경에서는 SSH 공개키 인증을 기본으로 하고, Known Hosts 검증을 사용한다.

PUT/DOWNLOAD/BOTH Mode에 따라 필요한 SFTPGo 권한(upload, list, download, create_dirs, rename 등)을 계정별로 정확히 부여한다.

폐쇄망에서는 개발/검증된 SFTPGo 설치파일, Client 실행파일, Config, Key를 사전 준비하여 반입한다.

15.1 인증 방식 및 자격증명 관리

무인 자동 실행 환경이므로 설정 파일에 비밀번호를 평문으로 저장하지 않는다. 비밀번호를 암호화해 보관하는 방식보다, 애초에 비밀번호를 사용하지 않는 공개키 인증을 기본으로 두는 것이 관리 대상 자체를 줄인다.

공개키 인증 기본 — config.ini에는 개인키 파일 경로와 known_hosts 경로만 기록하고 비밀번호 항목을 두지 않는다.

Known Hosts 검증 — 서버 호스트키를 사전 등록하여 대상 서버의 신원을 확인한다. 등록되지 않은 호스트키에 대해서는 접속을 거부하여 오접속과 중간자 개입을 차단한다.

키 파일 권한 — Linux에서는 소유자 전용 권한(600), Windows에서는 실행 계정 전용 ACL로 제한한다. 프로그램 시작 시 개인키 파일 권한을 검사하여 부적합하면 경고를 기록하고, 정책에 따라 실행을 중단할 수 있다.

Passphrase 정책 — 무인 실행 환경에서 개인키에 Passphrase를 걸면 그 값을 다시 어딘가에 보관해야 하므로 동일한 문제가 반복된다. 따라서 Passphrase 없는 키와 파일 권한 보호를 기본 조합으로 한다.

| [GENERAL]<br>AuthMethod  = publickey<br><br>[SFTP]<br>Host        = 192.168.x.x<br>Port        = 22<br>User        = rinexclient<br>PrivateKey  = keys/id_ed25519<br>KnownHosts  = keys/known_hosts |
| --- |

비밀번호 인증이 불가피한 경우

상대 기관 서버가 공개키 인증을 허용하지 않는 경우에 한하여 비밀번호 인증을 사용한다. 이때에도 평문 저장은 허용하지 않으며, 운영체제의 자격증명 보호 기능을 사용해 암호화한 값을 저장한다. Windows에서는 DPAPI를 사용하며, 해당 PC의 해당 계정에서만 복호화되므로 설정 파일을 복사해 가더라도 다른 환경에서는 사용할 수 없다.

| RINEXClient.exe config set-password<br><br>[SFTP]<br>Password = enc:AQAAANCMnd8BFdERjHoAwE/Cl+sBAAAA... |
| --- |

enc: 접두어로 암호화 여부를 구분하며, 평문 값이 입력되어 있으면 시작 시 경고를 기록한다. Linux는 자격증명 저장 방식이 배포판에 따라 다르므로 적용 방식을 MVP 5에서 확정한다.

키 배포 및 폐쇄망 반입

키 쌍은 클라이언트 측에서 생성하고 공개키만 SFTPGo 계정에 등록한다. 개인키는 생성된 서버 밖으로 반출하지 않는 것을 원칙으로 하되, 폐쇄망 반입이 필요한 경우 반입 대상과 경로를 기록으로 남긴다. 계정별 Home Directory 분리와 최소 권한 부여는 기존 정책을 그대로 적용한다.

16. 배포 구조

Windows 버전을 먼저 완성하고 동일 Core Source에서 Linux 빌드를 추가한다. 현장 PC에서는 Go를 설치하거나 Source를 컴파일하지 않고 빌드된 Binary를 배포한다.

| RINEXClient/<br> ├─ RINEXClient.exe<br> ├─ config.ini<br> ├─ rinex_ledger.db<br> ├─ keys/<br> │   ├─ private_key<br> │   └─ known_hosts<br> ├─ logs/<br> └─ README.txt |
| --- |

17. 기존 Go 프로토타입 재사용 방향

기존 프로토타입을 전면 폐기하지 않고 재사용 가능 부분과 재설계 부분을 분리한다.

| 재사용 검토 | 확장/재설계 |
| --- | --- |
| Config Parser<br>Scanner<br>SFTP Client<br>SSH Key / Known Hosts<br>Lock<br>Logger<br>.part → Rename<br>기존 Retry 기초 | DOWNLOAD Engine<br>BOTH Orchestrator<br>SQLite Ledger<br>Ingress Verification<br>Transfer Verification<br>Dispatcher / Queue / Worker<br>Global Limiter<br>Performance Metrics |

기존 00/01/02 파일 목록 방식은 최초 프로토타입의 신규 파일 판별에는 유용하지만, 송·수신 이력과 상호 검증까지 요구하는 최종 구조에서는 SQLite Ledger로 확장한다.

18. 검증 후 확정할 항목 (TBD)

기본 MaxWorkers 값 및 GlobalLimiter 값

FileZilla 대비 허용 가능한 처리시간/처리량 편차

PUT/DOWNLOAD별 SFTP Connection 재사용 방식

기관별 ScanDays / LedgerRetentionDays 기본값

Hash 검증이 필요한 기관 및 적용 범위

실제 SFTPGo 계정/권한 정책 및 Port

Linux 대상 배포판별 호환성 범위

18.1 본 개정에서 확정한 항목

아래 항목은 MVP 1 착수 이전에 결정되어야 하며, 이후 단계에서 변경할 경우 Ledger 구조 또는 배포 방식 전체에 영향을 주므로 TBD에서 분리하여 확정 항목으로 둔다.

file_id 생성 규칙 — 경로 비의존 식별자로 정의(9.1)

Ledger 쓰기 구조 — 단일 Writer 고루틴 + 배치 커밋 + WAL(9.3)

SQLite 드라이버 — modernc.org/sqlite, CGO 비의존(9.3)

스캔 구조 — Hot Scan / Deep Scan 2단계 분리(11.1)

Benchmark 축 — Worker 수 × 데이터셋 유형(12.2)

인증 방식 — SSH 공개키 기본, 설정 파일 내 평문 비밀번호 금지(15.1)

검증 환경 — BNC 15분 생성 기반 대량 유입 테스트, 단계별 통과 후 진행(13)

경로 정책 — 수신·송신 최상위 경로 분리, 중계 시 RepostDownloaded 활성화(10.1)

19. 최종 정의

| 설계 정의  Go 기반 RINEX 통합 SFTP Client는 기관별 RINEX 저장환경을 Config로 추상화하고 PUT·DOWNLOAD·BOTH를 하나의 배포 단위에서 제공한다. Common/PUT/DOWNLOAD Ledger와 Ingress/Transfer Verification을 통해 수신부터 최종 전송까지 추적 가능한 무결성을 확보하며, 실제 SFTPGo 환경에서 FileZilla 수준의 대량 파일 처리성능을 목표로 한다. |
| --- |

본 문서는 구현 전 설계 기준이며, 실제 개발은 MVP 1~5의 반복적 개발 방식으로 진행한다. Worker 수·성능 허용치·기관별 세부 경로 및 권한은 각 MVP의 실제 SFTPGo 통합 테스트와 현장 조건 확인 결과를 기준으로 확정한다.
