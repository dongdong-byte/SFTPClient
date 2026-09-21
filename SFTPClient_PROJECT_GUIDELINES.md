# SFTPClient 프로젝트 최종 개발 지침

> 이 문서는 SFTPClient 개발 시 ChatGPT, Claude Code, Cursor가 공통으로 따라야 하는 최종 개발 기준이다.
> `Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx`의 설계 내용은 기준 문서로 유지한다.
> 단, 현재 개발 순서는 원문과 달리 **PUT(MVP1) → MVP2 보강 → DOWNLOAD(MVP3) → BOTH/성능 개선 재검토**로 진행한다.
> MVP2 보강 범위는 세트 원자성, `resend`, 경로 범용화, Linux 배포·테스트,
> Retention Cleanup 구현·검증이다. DOWNLOAD는 MVP3으로 연기한다.
> Linux 배포에는 Linux 전용 보안 기능 개발을 포함하지 않는다. 공식 보안점검에서
> 구체적인 보안 요구가 나온 경우에만 요구 범위에 맞춰 별도 설계·구현한다.
> 이 개발 순서의 차이 외에 원문 요구사항을 임의로 삭제·축소·대체하지 않는다.

### 개정 이력

| 일자 | 내용 |
|---|---|
| **2026-09-21** | **인시던트 후속 유닛 1~5 최종 결정 통합.** 로그 보존, schema v6·해시 판정, 임시 입력 제외, 30초 SFTP 무진행 감시·취소 종료, 기존 요약의 비율·모집단 표시. 임계 경보는 선택 사항. 아래 최종 결정 절과 종합 결과 문서 참조. |
| 2026-08-25 | 병렬 처리 방침 정정 — Scan은 순차, 전송은 MVP 1부터 `MaxWorkers=4` 병렬. 기존 "worker=1 순차" 표기를 전면 수정 (4·5절) |
| 2026-08-25 | 9.1 대상 확장자 항목 정정. 비압축 유입이 실재하며 Hourly 는 비압축만 있는 경우 확인 (9.7 신설) |
| 2026-08-25 | 9.3 Hourly Scan 근거 정정. 산술 오류(×4 → ×2) 수정 및 근거를 원격 왕복 비용으로 교체 |
| 2026-08-25 | Deep Scan 주기 관리 방식 확정. `scan_state` 테이블 대신 `DeepScanHour` (9.8 신설) |
| 2026-08-25 | 7절 테스트 항목에서 미구현 파서(`rinexname.go`) 요구 제거 |
| **2026-08-28** | **스키마 v5.** `local_path` 삭제, 후보 선정을 DB 주도 → Scan 주도로 변경 (4절 Ledger 스키마 취급) |
| 2026-08-28 | **9.3 전면 정정.** Hourly 는 `(HH)` 를 **계산한다.** 나열 방식은 기각 |
| 2026-08-28 | 9.7 확장자 항목 정정. 실제 유입은 `.gz` / `.Z` / `.zip` 압축본이며 DOY 폴더에 비압축은 오지 않는다 |
| 2026-08-28 | 9.9 신설 — Scan 범위 3갈래(Hot / Deep / Recovery)와 `--dry-run` / `seed` |
| 2026-08-28 | 4절 미결 구조 항목 해소 — `internal/scan` 분리, `config` 완료 |
| 2026-08-28 | 패키지명 표기 `pathtpl` → `pathpl` 통일. 코드가 원본이다 |
| 2026-08-29 | `DirLister.List` 시그니처에 `context.Context` 반영 (구현과 정합) |
| **2026-08-30** | **Ledger 복합키 `(category, file_name)`, `schema_version` 4. lookup 완료** |
| 2026-08-30 | **`internal/lock` 추가** — 프로세스 단일 실행 (디렉터리 + owner token) |
| 2026-08-30 | `.gitattributes` — `*.go`/`*.sql`/`*.md`/`*.ini` 줄끝 LF 고정 |
| 2026-08-30 | **`LockStaleSeconds` config 확정** — `General.LockStale`(Duration). Minutes 기각 |
| **2026-08-30** | **PUT 조립·`put_ledger`·main 배선 완료.** `--dry-run` 관측, live 는 transport 전까지 거부. 아래 「2026-08-30 확정」 |
| 2026-08-30 | **`MaxRetries` 재정의** — 실행 안 재시도 폐기, 동일 revision 누적 시도 상한(기본 5) |
| 2026-08-30 | **IN_PROGRESS 회수 = FAILED** — 설계안 PENDING 되돌리기 폐기. `FailPut` → 재시도는 `BeginPut` |
| 2026-08-30 | **Unchanged ≠ 전송 완료** — 후보 경로에서 떨어뜨리지 않음. PENDING 고아 재개 경로 |
| **2026-08-31** | **transport 완료 — localfs·sftpfs.** put 소비자측 `Uploader` 계약, `.part`→Size→PosixRename→최종 Stat. localfs 관통: 2,153건 VERIFIED, 재실행 candidates=0 |
| **2026-08-31** | **posix-rename 판별 = 광고 → 기능 탐침 2단계.** SFTPGo 2.7.5 실측: 지원하되 광고 안 함(HasExtension 거짓 음성). 기각: 첫 Rename 시점 판정(attempts 는 파일 예산). 아래 「2026-08-31 확정」 |
| 2026-08-31 | SFTP 접속 상한 = `dialTimeout` 상수 10초 (TCP+handshake 단일 예산). config 키 승격은 운영 근거 관측 후 |
| 2026-08-31 | `main` — `signal.NotifyContext` 배선(Ctrl+C→ctx 취소), `--transport sftp` live 배선. "live 거부" 절단면 해제 |
| 2026-08-31 | sftpfs 계약 테스트 — 오프라인 6종 + 실서버 10종(`SFTPTEST_*` 게이트). 로컬 SFTPGo 2.7.5 전부 PASS. ★RenameOverwritesExisting = posix-rename 실증 |
| 2026-08-31 | 실서버 실측 — 관련정보 문서(260527)는 낡음. 지리원·서울시 OS 세대교체 확인, 측위원 SFTPGo 가동. 설치처는 기관 서버가 아닌 중간 전달자 호스트(대표 확인) |
| 2026-09-01 | config 기본 경로 = exe 옆(`DefaultPath`). `remote_size` = 원격 `FinalSize`. 2차 Ctrl+C 재무장 |
| **2026-09-01** | **IN_PROGRESS 회수 조립 (A안, salvage 없음).** live·lock 이후 Recover. Size만으로 rename 완료로 보지 않음 |
| **2026-09-01** | **PUT Worker Pool** — 디렉터리 단위 `(category, RemotePath.Expand(When))`, `MaxWorkers` 기본 4 |
| **2026-09-01** | **real seed** — 원격 Stat+Size 일치만 VERIFIED. List 기반·`--seed-common` 기각. 창은 `ScanDays` |
| 2026-09-01 | `Transport` 를 `[GENERAL]` 필수 키로. `--transport` 는 이번 실행 override. 인자 없는 exe 기동 |
| 2026-09-01 | `HourLayout` dir/flat. Daily `(HH)` 거부. Hourly `(HH)` 검증 |
| 2026-09-04 | Hourly/타깃 경로에 `(HH)` 없는 단일 경로 수용 (서울시) |
| **2026-09-04** | **enc: DPAPI LocalMachine.** `internal/security` · 로드 시 `value()` 복호화 · `secure-set`. SFTP 평문 Host/User/Port 는 WARN. 측위원 평문 저장 금지 대응 |
| **2026-09-09** | **MVP2 세트 완성도 게이트 설계 확정.** 전송 시작 원자성만(롤백 없음), 기본 OFF·Opt-in, `[SET.RINEXx] RequiredKinds`(false/CSV, Enabled 키 없음), kind=데이터 종류만(표현 형식 제외), 게이트 위치 Upsert 뒤·PENDING 앞, 매 스캔 재계산, resend 는 게이트 우회, 활성화 전제조건(현장 접근 토폴로지). 12절 참조 |
| **2026-09-10** | **세트 게이트 구현 완료 (커밋 5개).** §5 3차 확정(버전 출처=닫힌 열거형, ini 키 기각), 의미론 2건(미완성 세트 선택 종 포함 전체 보류 / 파싱 유보 개별 통과+관측), **스키마 v8·`schema_version` '5'** — `common_ledger` 에 `set_key`·`kind`(NOT NULL DEFAULT ''), 최초의 운영 DB 보존 전환을 Open 자동 마이그레이션으로 수행(서브커맨드 기각), 파생의 주인은 `UpsertCommon` 내부 `domain.SetKeyKind`, MaxFilesPerRun 세트 경계 절단. 12절 참조 |
| **2026-09-15** | **MVP2 실행 범위 재확정.** ① 세트 원자성(완료) ② `resend` 명령 신설 ③ 서울시 긴급 대응으로 추가한 `HourLayout` 제거와 범위 제한 재귀 탐색 기반 경로 범용화 ④ Linux 배포·테스트 ⑤ Retention Cleanup 구현·검증. DOWNLOAD는 MVP3으로 연기. 보류 리포트는 보안 취약점 우려로 제외. 게이트 활성화 운영 절차와 RINEX3 운영 프로파일도 이번 범위에서 제외. Linux 전용 추가 보안은 선제 구현하지 않고 공식 보안점검 요구가 있을 때만 별도 진행. |
| **2026-09-16** | **문서 정합.** 9.3을 현재 결론/하지 말 것/과도기 코드/역사 기록으로 분리. 12절 봉인 문구 강화(15·18만 안내 덧붙임, 본문 전체 현행화 아님). 수동 복구 표기를 `resend`로 통일. |
| **2026-09-16** | **Ledger Retention 운영값 현행화.** 서울시·측위원 현장 배포에서 적용한 1개월 보존을 기준으로 `[LEDGER] RetentionDays = 30` 확정. 달력 월이 아니라 30일로 계산하며, `[LOG] RetentionDays = 30`과 값은 같지만 책임은 별개다. |

### MVP2 인시던트 후속 유닛 1~5 — 최종 결정 (2026-09-21)

후속 보강 **1 → 2 → 5 → 3 → 4 구현이 반영됐다.** 이 번호는 아래의 기존
MVP2 다섯 목표와 별개이며, 전체 MVP2·현장 배포 완료를 뜻하지 않는다.
결정 이유·구현 위치·검증 이력·잔여 사항은
[종합 결정 및 결과](docs/SFTPClient_MVP2_UNIT1_5_DECISIONS_AND_RESULTS.md)를 본다.
기존 본문과 후속 계획 v2·v3의 오래된 미착수·초안 설명이 충돌하면 이 절을
우선한다. 관련 없는 기존 MVP2 범위를 확대하거나 축소하지 않는다.

**1. 로그 보존**

- 표준 `log` 유지. main에서 stderr와 일자별 파일 Writer를 배선한다.
  저장 방식의 책임은 `internal/logging`에 두며 put·ledger에 의존을 추가하지 않는다.
- 실제 설정은 `[LOG] Dir`, `RetentionDays`다. 유닛 1 초안의 GENERAL 신규
  키를 구현된 키로 취급하지 않는다. 로컬 날짜 파일명으로 회전·보존 정리한다.
- 로깅 실패는 경고 후 전송을 계속한다. 매 줄 fsync·slog 전환·새 수준 필터는 없다.
  로그 보존과 Ledger 행 보존을 별개로 취급한다.

**2. 해시 판정·schema v6**

- SHA-256은 로컬 전송 파일 전체 바이트에 적용한다. size 동일·mtime 변경에서
  저장 지문과 새 안정 지문이 같으면 revision 유지, mtime 기준선만 갱신한다.
- 지문 부재 또는 판정 해시 읽기 실패는 동일 내용으로 단정하지 않는다.
  보수 신규/변경 처리를 유지하고, 해시 불안정 관측은 파일 단위 보류한다.
- Unchanged·MetadataOnly도 현재 put 상태에 따라 전송 후보일 수 있다.
  내용 지문은 전송 완료 증거가 아니며 VERIFIED와 의미를 섞지 않는다.
- schema v6 및 v5→v6·v4→v5→v6 마이그레이션을 유지한다. DB 재생성 금지.
  Touch/SetContentHash 가드 0행은 WARN·보류, 실제 DB 오류는 반환한다.
- 판정·필수 해시는 예산 제한 없음. 자연 백필만 `MaxHashBackfillPerRun` 적용:
  키 부재 500, 0 끔, 양수 상한, 음수 거부. 실패한 백필 시도도 예산을 쓴다.
- dry-run의 장부 쓰기·자연 백필과 seed의 지문 계산은 하지 않는다.
  전송 실패로 revision을 올리거나 attempts를 초기화하지 않는다.

**3. 미완성 입력**

- 이름 끝 `.part`·`.filepart`는 대소문자 무관하게 정규화·장부 조회 전에 제외한다.
  삭제·rename·내용 수정·신규 장부 등록 없이 기존 제외 집계를 남긴다.
- `NormalizeName`은 `.part`만 제거하고 `FILENAME_V1`은 유지한다.
  `.filepart`는 장부 이름 가드가 차단하지 않으므로 스캔 우회 경로도
  입력 제외 계약을 지켜야 한다. 자체 업로드 `.part` 프로토콜은 유지한다.
- 최종 이름은 기존 검증·변경 판정·세트 조건을 거친다. rename 추적이나
  옆 임시 파일 소멸 확인은 없다. 기존 장부·원격 청소는 추가하지 않되
  Recover의 IN_PROGRESS 정리·FAILED 전이는 유지한다.

**4. SFTP 무진행·취소 종료**

- 공유 연결 유지. 진행 중 작업이 있고 진전이 멈췄을 때 감지한다.
  `[PUT.SFTP] StallTimeoutSeconds` 기본 30초, 허용 5~600. 파일 총시간이나
  연결 idle 제한이 아니다. 30초를 현장 검증된 최적값으로 표현하지 않는다.
- TCP·SSH handshake·SFTP 초기화·rename 탐침은 단일 10초 접속 예산이다.
  deadline은 탐침 완료 후 해제하고 이후 Recover·seed·전송을 감시한다.
- 계측은 mutex와 monotonic 시계, 유휴 0→1 재설정·완료·청크 진전으로 한다.
- 스톨은 회차 취소 → SSH Abort → 기존 실패 기록 → 정상 함수 반환으로 끝낸다.
  일반 취소에서도 SSH를 닫고 감시 종료를 기다리는 배선을 제거하지 않는다.
  Abort 뒤 Close는 SFTP 종료 대기를 다시 하지 않는다.
- VERIFIED는 유지하며 재시도는 같은 revision의 남은 예산·스캔 범위 안에서만 한다.
  회차 내 재연결·무제한 재시도·keepalive·워커별 연결·이어받기는 추가하지 않는다.
- 로컬/NAS 대기, 전체 회차 강제 종료, lock 구조 변경은 별도 범위다.
  lock은 나이 기반이므로 조기 종료가 stale 이중 실행을 완전히 막는다고 보장하지 않는다.
  실제 DROP/RST 종료·재실행 검증은 구현 완료와 구분해 남긴다.

**5. 관측 요약·경보**

- 기존 요약에 `evaluated = Unchanged + New + Changed + MetadataOnly`,
  `suppressed_ratio`, `mode=live/dryrun/seed`, `range=hot/deep`을 표시한다.
  분모 0은 `n/a`, 범위 미설정은 `unset`이다. 전송 후보 수를 분모로 쓰지 않는다.
- 카운터는 분류 시점의 관측이다. 후속 해시·가드 보류나 전송 성공 여부와
  혼동하지 않는다. `changed`에 보수 판정이 포함됨을 유지한다.
- suppressed·changed 임계 경보는 필수가 아니다. 400 등 임의 문턱 금지.
  실측 후에도 조치 가치가 없으면 도입하지 않는다. INFO 집계는 유지한다.
- exhausted>0의 기존 WARN·최대 5개 예시를 재사용한다. 새 중복 경고·
  외부 알림·회차 간 상태·연속 STALL/lock/임시 파일 장기 경보는 추가하지 않는다.
- `.part` 잔류나 QC 장애를 SFTP 스톨·중복전송의 확정 증거로 취급하지 않는다.
  코드 사실, 사용자 전달, 과거 가설, 현장 검증 결과를 구분해 기록한다.

### MVP2 현재 실행 범위 (2026-09-15 확정)

| # | 목표 | 상태 / 범위 |
|---|---|---|
| 1 | 세트 원자성 | **완료.** 12절의 Set Completeness Gate 구현을 유지한다. |
| 2 | `resend` 명령 신설 | **미구현.** 기간·대상 지정 재전송이며 세트 게이트를 우회한다. |
| 3 | 경로 범용화 | **설계 예정.** 서울시 평면 Hourly 경로를 급히 지원하려고 추가한 `HourLayout`을 제거하고, 설정 루트 아래의 범위 제한 재귀 탐색으로 전환한다. 로컬 하위 구조는 원격에 복제하지 않고 RINEX 버전별 고정 평면 `RemotePath`로 전송한다. 상세 탐색 경계는 Linux OS·실경로 확인 후 확정한다. |
| 4 | Linux 배포·테스트 | **미구현.** Linux 빌드, 실행환경·경로·스케줄러·SFTPGo 연동과 통합 테스트를 포함한다. Linux 전용 추가 보안은 포함하지 않으며 공식 보안점검에서 요구가 나온 경우에만 별도 진행한다. |
| 5 | Retention Cleanup | **미구현.** Ledger 30일 보존 설정이 실제 DB 행 삭제로 이어지도록 구현하고 경계·FK cascade·재전송 방지 테스트를 수행한다. |

이번 MVP2에서 명시적으로 제외하는 항목:

- DOWNLOAD 및 `download_ledger`: MVP3으로 연기한다. BOTH의 후속 MVP 번호는 아직 정하지 않는다.
- 미완성 세트 보류 리포트: 대표 확인 결과 보안 취약점 우려가 있어 구현하지 않는다.
- 게이트 활성화 운영 절차: 이번 MVP2의 개발 산출물에서 제외한다. 기존 안전 전제 기록은 역사적 설계 근거로만 보존한다.
- RINEX3 운영 프로파일: 작성·현장 적용하지 않는다. 이미 구현된 RINEX3 파싱·세트 게이트 지원을 제거한다는 뜻은 아니다.

위 범위가 기존 문서의 MVP 순서, 잔여 항목 또는 우선순위와 충돌하면 이 절을 우선한다.

### MVP2 구현 전 결정 필요 (2026-09-15)

아래 항목은 아직 확정하지 않았다. 구현 시 임의로 가정하지 말고 현장 정보와
교차검증을 거쳐 결정한 뒤 이 문서에 반영한다.

- **`resend`의 Ledger 의미론:** 지정 범위 전체를 강제로 다시 보낼지, Ledger로
  미전송·변경분을 거르고 별도 강제 옵션을 둘지 결정이 필요하다. 명령 인자와
  보존기간 밖 요청의 처리도 이 결정에 맞춰 확정한다.
- **제한 재귀 탐색의 경계:** 시작 루트, 최대 깊이, 날짜 범위 적용 방식,
  권한 오류 처리, symbolic link/junction 추적 여부는 다음 Linux 설치처의
  OS 버전과 실제 경로를 받은 뒤 결정한다.
- **Retention과 재탐색/`resend`의 결합 규칙:** 30일이 지나 삭제된 Ledger 행의
  로컬 파일이 자동 탐색에서 신규로 재등록·재전송되지 않도록 경계를 확정하고,
  보존기간 밖 `resend`의 경고·거부·강제 실행 정책을 결정한다.

이미 확정된 사항은 Ledger 보존기간 30일, 성공한 Deep Scan 뒤 Cleanup 수행,
`common_ledger` 삭제 시 `put_ledger` FK cascade, 원격의 RINEX 버전별 고정 평면
경로다. 위 미결 항목을 정할 때 이 결정을 임의로 변경하지 않는다.

### 관련 문서

| 문서 | 역할 |
|---|---|
| `Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx` | 설계 기준 문서 |
| 본 문서 | 개발 지침 |
| `internal/ledger/schema.sql` | Ledger 물리 스키마 **원본** |
| `docs/SFTPClient_LEDGER_CONCEPT.md` | Ledger 개념·논리 모델과 결정 근거 |
| `docs/SFTPClient_SCAN_DESIGN_DECISIONS.md` | Scan 범위·복구 전략의 결정 경위와 **기각 목록** |
| `config.example.ini` | 설정 템플릿. 커밋 대상. `config.ini` 는 `.gitignore` |

**용어 대응 — 설계안의 `file_id` ↔ 구현의 `(category, file_name)`.**
설계안 9.1은 식별자를 `SHA-256( Domain │ Category │ NormalizedName )` 해시로 정의하고 `file_id`라 명명했으나,
구현에서는 정규화한 파일명 컬럼을 `file_name`으로 두고 **Category 와 함께 복합키**로 쓴다.
`file_name` 단독이 식별자가 아니다. RINEX3/RINEX4 가 같은 파일명을 가질 수 있기 때문이다
(`schema_version` 4 / CONCEPT 4.1).
설계안 원문을 참조할 때는 `file_id` ≈ `(category, file_name)` 으로 읽는다.

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
│   ├─ config/         [완료]   config.ini 파싱 및 시작 시 유효성 검사.
│   │                           ini.go      — INI 문법 전담. domain·pathpl 을 import 하지 않는다
│   │                           config.go   — 타입 정의. 경로는 *pathpl.Template 으로 보유
│   │                           load.go     — 키 매핑. "이 값을 읽을 수 있는가"
│   │                           validate.go — 값 검증. "이 조합으로 돌려도 되는가"
│   │                           Validate 는 파일시스템을 건드리지 않고, 파일 실재는
│   │                           CheckEnvironment 가 본다. 고치는 방법이 다르기 때문이다.
│   │                           전자는 config.ini 를, 후자는 서버 상태를 고쳐야 한다.
│   │                           경로는 문자열이 아니라 파싱된 템플릿으로 들고 있는다.
│   │                           그래야 문법 오류가 스캔 도중이 아니라 시작 시점에 드러난다.
│   │                           SFTP 접속 정보는 [PUT.SFTP] 처럼 방향 아래에 둔다.
│   │                           BOTH 에서 수신 서버와 송신 서버가 다르기 때문이다.
│   │                           현재는 HourLayout(dir/flat)과 LocalPath의 (HH)를 대조한다.
│   │                           이는 서울시 긴급 대응이며 MVP2 경로 범용화에서 제거할 예정이다.
│   │                           알 수 없는 섹션·키도 거부한다. 오타가 무시되면
│   │                           그 설정이 기본값으로 도는 것과 구분되지 않는다.
│   │                           오류는 첫 건에서 멈추지 않고 줄 번호와 함께 모아서 낸다.
│   │                           관측소 필터는 두지 않는다. 전체 관측소가 대상이다.
│   │                           `LockStaleSeconds`(초) → `General.LockStale`(Duration).
│   │                           600~86400 범위. Minutes 키 없음 (단위 규약=초).
│   │
│   ├─ domain/         [완료]   프로젝트의 핵심 개념 정의. 내부 패키지를 하나도 import하지 않는다.
│   │                           category.go — Category 6값, IsHourly/IsDaily, MatchesName(3-값 대조)
│   │                           status.go   — PUT status 4값, CanTransitionTo, IsTerminal
│   │                           state.go    — common state 2값 (READY/CHANGED)
│   │                           origin.go   — LOCAL/DOWNLOAD
│   │                           filename.go — NormalizeName, BaseName, IsPartFile, IdentityRule
│   │                           mode.go     — PUT/DOWNLOAD/BOTH, DoesPut, DoesDownload
│   │                           파일명 정규화는 이 패키지의 함수 하나에서만 수행한다. (CONCEPT 5②)
│   │                           상태 문자열은 전부 여기 상수로 선언하고 리터럴을 직접 쓰지 않는다.
│   │                           File 구조체는 두지 않는다. ledger.CommonInput과 역할이 겹친다.
│   │
│   ├─ pathpl/        [완료]   (YYYY)(YY)(DOY)(MM)(DD)(HH) 토큰을 실제 경로로 확장. (설계안 10)
│   │                           입출력만 있는 순수 함수. PUT과 DOWNLOAD가 공유한다.
│   │                           (SITE)는 두지 않는다. 실제 기관 경로가 관측소별 디렉터리를
│   │                           두지 않으며, 관측소 조회는 file_name 접두어로 SQL에서 한다.
│   │                           시각은 Expand 내부에서 UTC로 강제한다.
│   │                           알 수 없는 토큰과 짝 없는 괄호는 Parse 단계에서 거부한다.
│   │
│   ├─ scan/  [현행 완료/개편 예정] 디렉터리를 나열하여 어떤 파일이 어디에 있는지 사실만 수집.
│   │                           판정하지 않는다. 0바이트도 .part 도 거르지 않고 그대로 올린다.
│   │                           DirLister 인터페이스를 consumer-side 로 선언하여
│   │                           로컬과 원격 SFTP 를 같은 로직으로 다룬다.
│   │                           주된 근거는 테스트다. fake 하나로 스캔 전체를 검증한다.
│   │                           Hot / Deep / Recovery 는 날짜 범위만 다른 같은 코드다.
│   │                           디렉터리 단위로 콜백에 넘긴다. 수만 건을 슬라이스에 담지 않는다.
│   │                           순차 실행한다. 병렬화 대상이 아니다.
│   │                           fs.ErrNotExist 는 오류가 아니라 건너뛰기다.
│   │                           현행은 템플릿의 정확한 디렉터리를 나열한다. MVP2에서
│   │                           설정 루트 아래 범위 제한 재귀 탐색으로 개편한다.
│   │
│   ├─ verify/         [완료]   판정 로직 전담. 전송도 기록도 하지 않고 "정상인가"만 답한다.
│   │                           Ingress  — size>0, IsPartFile(.part / .filepart 제외), mtime grace (설계안 7)
│   │                           Transfer — 원본/목적지 Size 대조, 최종 파일 존재 확인 (설계안 8)
│   │                           Ingress와 Transfer 모두 구현·테스트 완료.
│   │
│   ├─ lock/           [완료]   프로세스 단위 중복 실행 방지. (설계안 14)
│   │                           lock 디렉터리 + owner-<token>. RemoveAll 금지.
│   │                           빈 경로 거부(ErrInvalidPath). stale 은 나이 문턱만(MVP1).
│   │                           비어 있지 않은 lock dir 은 수동 확인 hard error.
│   │                           config/ledger 를 import 하지 않는다. 경로·staleAfter 는 인자.
│   │                           TookOver / ErrHeld / ErrLost 로 운영 신호를 구분한다.
│   │                           config 는 `LockStaleSeconds` → `General.LockStale`(Duration).
│   │                           Minutes 키는 기각(경과시간 단위 규약=초).
│   │                           main 은 Acquire/Release 배선 완료. ErrHeld → exit 0.
│   │
│   ├─ transport/      [완료]   파일을 실제로 옮기는 계층. 설계안 4절의 SFTP Core.
│   │                           localfs.go — 기준 구현. SFTP 없이 상태 머신 전체를 검증. [완료]
│   │                           sftpfs.go  — SSH 공개키 접속(known_hosts 강제), .part 업로드,
│   │                                        PosixRename, dialTimeout 10s(TCP+handshake 단일 예산),
│   │                                        posix-rename 판별 = 광고→탐침 2단계.        [완료]
│   │                           put.Uploader 를 구조적으로 만족(단언은 *_test.go).
│   │                           put/download를 모른다. 방향을 알지 못하고 파일만 옮긴다.
│   │                           계약 테스트: localfs 10종 + sftpfs 오프라인 6종·실서버 10종.
│   │
│   ├─ logging/       [미구현]  log/slog 설정. 출력 대상, 레벨, 보존 정책. (설계안 14)
│   │                           성공은 집계, 실패·재시도는 상세 원인 기록.
│   │
│   ├─ put/              [완료] 송신 흐름 조립 (GUIDELINES 5절).
│   │                           Scan → Lookup → 대조 → (신규·변경) Verify → Upsert
│   │                           → 후보 필터 → 정렬·절단 → PENDING 일괄 등록 → (--dry-run 리포트).
│   │                           전송 Worker Pool(MaxWorkers 기본 4)과 Transfer 검증 완료.
│   │                           Unchanged 도 후보 경로에 남긴다. "장부와 같다" ≠ "이미 보냈다".
│   │                           transport를 import하지 않으므로 fake 주입으로 조립 테스트 가능.
│   │                           별도 internal/pipeline 패키지 계획은 폐기하고 PUT Worker Pool을
│   │                           internal/put/transfer.go에 통합했다. Global Limiter는 BOTH 착수 시 재검토한다.
│   │
│   ├─ ledger/                  common/put/download Ledger. (설계안 9)
│   │                           schema.sql — 물리 스키마 원본. go:embed로 실행파일에 포함하고
│   │                                        시작 시 실행한다. 스키마는 이 파일이 유일한 원본이며
│   │                                        Go 코드에 CREATE TABLE 문자열을 중복해 두지 않는다.
│   │                                        PK = (category, file_name). schema_version = 5.
│   │                           db.go      — Open/PRAGMA/schema 적용                      [완료]
│   │                                        identity_rule + schema_version 대조
│   │                           common.go  — CommonInput, UpsertCommon (3분기 UPSERT)     [완료]
│   │                                        v5 에서 local_path 제거. 경로를 보유하지 않는다
│   │                                        ON CONFLICT (category, file_name)
│   │                           lookup.go  — category 범위 file_name IN (...) 일괄 조회  [완료]
│   │                                        후보를 만들지 않고 현재 상태만 돌려준다
│   │                           put.go     — put_ledger 상태 머신                         [완료]
│   │                                        BeginPut / FinishPut / FailPut / RegisterPending
│   │                                        LookupPut / ListInProgress. category 필수.
│   │                                        MaxRetries = 동일 revision 누적 시도 상한.
│   │                                        IN_PROGRESS 회수 재료는 ListInProgress;
│   │                                        원격 .part 삭제+FailPut 조립 완료.
│   │                           판정은 하지 않되 revision·state 갱신은 여기서 한다. (CONCEPT 2.1)
│   │                           SetMaxOpenConns(1)이 쓰기를 직렬화하므로 MaxWorkers>1 에서도
│   │                           정합성은 안전하다. 단일 Writer 고루틴 + 배치 커밋은
│   │                           처리량 최적화이며 MVP 4 범위이다. (CONCEPT 4.8)
│   │                           [MVP 3] download Ledger까지 확장.
│   │
│   ├─ download/       [MVP 3] 수신 흐름 조립. put과 대칭 구조.
│   │                           Origin=DOWNLOAD 기록으로 Ping-Pong 방지에 관여. (설계안 9.1)
│   │
│   └─ security/ [Windows 완료] 자격증명 및 설정값 보호. (설계안 15.1)
│                               security.go         — 인터페이스 및 enc: 접두어 처리
│                               security_windows.go — Windows DPAPI LocalMachine 구현
│                               security_other.go   — 비Windows에서 enc: 명시적 거부
│                               Linux 전용 추가 보안은 공식 보안점검 요구 시에만 별도 설계
│
├─ docs/
│   ├─ SFTPClient_LEDGER_CONCEPT.md   (schema.sql과 짝. 항상 함께 갱신)
│   │                           Ledger 개념·논리 모델. 엔티티 정의, 관계, 식별자 근거,
│   │                           의도적 비정규화 사유, 미결 항목, 설계안 대비 변경 요약.
│   │                           원본은 여기만. internal/ledger/ 아래 사본을 두지 않는다.
│   └─ SFTPClient_SCAN_DESIGN_DECISIONS.md
│                               Scan 범위·복구·LockStale 등 결정 경위와 기각 목록.
│
├─ config.example.ini           설정 템플릿. 실제 config.ini는 커밋하지 않는다.
├─ .gitattributes               *.go / *.sql / *.md / *.ini 줄끝 LF 고정
├─ .gitignore                   config.ini, keys/, logs/, data/, *.db, *.db-wal,
│                               *.db-shm, *.exe 등을 제외한다.
└─ go.mod                       모듈 경로(SFTPClient), Go 버전 및 의존성 목록.
                                modernc.org/sqlite  — CGO 불필요. 순수 Go 포팅이라
                                                      CGO_ENABLED=0으로 Linux 크로스 빌드 가능
                                github.com/pkg/sftp — SFTP transport 구현 완료
                                폐쇄망 반입용 vendor/ 확보 완료.
```

### 패키지 책임 및 의존 방향

- `domain`은 어떤 `internal` 패키지도 import하지 않는다.
- `transport`는 `put`과 `download`를 알지 못한다.
- `put`과 `download`는 필요한 인터페이스를 consumer-side에서 선언한다.
- PUT에만 필요한 로직은 `put`에 둔다.
- DOWNLOAD에만 필요한 로직은 `download`에 둔다.
- 양쪽에서 사용하는 기능은 `domain`, `pathpl`, `scan`, `verify`, `transport`, `ledger`, `lock`, `config`, `logging` 등 공통 패키지로 분리한다.
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

- **스키마 세대(`schema_meta.schema_version`)를 시작 시 검증한다.**
  다르면 방향과 무관하게 즉시 중단한다.
  DB 가 낮으면 마이그레이션 미수행이고, 높으면 구버전 실행파일이 배포된 것이다.
  구버전 실행파일이 신버전 DB 에 쓰면 컬럼 불일치로 조용히 잘못된 행이 생긴다.
  `schema.sql` 의 `INSERT OR IGNORE` 는 기존 값을 덮지 않으므로 이 검사가 유일한 방어선이다.
- **경로는 Ledger 에 저장하지 않는다 (v5).**
  `common_ledger.local_path` 를 삭제했다. 장부의 목적은 "잘 보내졌는가" 이고
  "어디에 있었는가" 는 그 목적에 기여하지 않는다. 경로는 자주 바뀌며,
  DB 에 적어두면 코드가 그 값에 의존하게 되어 경로 변경이 중복 전송으로 이어질 여지가 생긴다.
  전송에 필요한 경로는 Scan 이 나열 시점부터 메모리로 들고 다닌다.
- **후보 선정은 Scan 주도이다 (v5).**
  DB 가 "무엇을 어디서 보낼지" 를 지시하지 않는다.
  Scan 이 디렉터리를 나열한 뒤 그 Category + 파일명으로 장부에 묻는다.

  ```sql
  SELECT c.category, c.file_name, c.revision, c.size, c.mtime, p.status
    FROM common_ledger c
    LEFT JOIN put_ledger p
      ON  p.category = c.category
      AND p.file_name = c.file_name
      AND p.revision = c.revision
   WHERE c.category = ?
     AND c.file_name IN (?, ?, ?, ...);
  ```

  복합 PK `(category, file_name)` 인덱스를 그대로 타므로 `file_name` 단독 인덱스는 두지 않는다.
  부수 효과로 `FAILED` 행이 Scan 범위 밖으로 밀려나면 자연히 만료되며,
  자동 재시도와 운영자 `resend`의 경계가 명확해진다.
- **식별자는 `(category, file_name)` 복합키이다 (`schema_version` 4).**
  RINEX3/RINEX4 가 같은 long filename 을 가질 수 있어 `file_name` 단독 PK 는
  category 덮어쓰기 + revision 루프를 만든다. `identity_rule` 은 정규화만 담당하고
  키 구조 변경은 `schema_version` 이 담당한다. (CONCEPT 4.1)
- 다음 두 항목은 스키마가 강제하지 못하므로 코드 리뷰에서 확인한다.
  - `remote_path` / `part_path`는 전송 완료 후가 아니라 `IN_PROGRESS` 전환 트랜잭션 안에서 기록한다.
    완료 후에 기록하면 중단된 항목이 NULL로 남아 잔여 `.part` 정리 대상을 찾지 못한다. (설계안 9.3, 13.2)
  - `put_ledger`의 FK는 `(category, file_name)`을 참조하므로 `revision` 정합성은 강제되지 않는다.
    `revision` 값은 항상 `common_ledger`에서 읽은 값을 그대로 사용하고 직접 구성하지 않는다.
    put_ledger 쓰기는 반드시 `category` 를 함께 넘긴다.

### 아직 결정되지 않은 구조 항목

설계안에 요구사항이 있으나 위 구조에 자리가 정해지지 않은 항목이다. 착수 전에 확정한다.

| 항목 | 설계안 | 현황 |
|---|---|---|
| ~~Hot Scan / Deep Scan 분리~~ | 11.1 | **확정 — `internal/scan` 으로 분리.** PUT 과 DOWNLOAD 가 모두 사용한다 |
| ~~중복 실행 방지 + Stale Lock~~ | 14 | **완료 — `internal/lock` + config `LockStaleSeconds` + main Acquire/Release.** ErrHeld=exit 0 |
| `db status` / `db failed put` / `db query` 조회 명령 | 9.2 | 담당 패키지 미정 |
| 대량 유입 검증 기준 | 13 (18.1 확정 항목) | 7절이 단위 테스트만 다루고 있어 보완 필요 |

`scan` 은 로컬 디렉터리와 원격 SFTP 디렉터리 양쪽에서 같은 로직으로 돌아야 하므로
`os.ReadDir` 에 직접 붙이지 않고 최소 인터페이스를 consumer-side 로 선언한다.

```go
type DirLister interface {
    List(ctx context.Context, dir string) ([]Entry, error)
}
```

**주된 근거는 테스트다.** fake 하나로 스캔 전체를 검증할 수 있고
실제 디렉터리도 SFTP 서버도 필요 없다. DOWNLOAD 재사용은 부수 효과다.

`scan` 은 판정하지 않는다. `ReadDir` 결과를 그대로 올리며
0바이트도 `.part` 도 거르지 않는다. 거르는 것은 `verify` 의 책임이다.

```
scan    어떤 파일이 어디에, 크기·시각이 얼마인가   (사실)
verify  이 파일이 전송 가능한 상태인가             (판정)
ledger  이미 보냈는가                              (기록)
```

## 5. 실제 개발 순서

원문 설계안의 개발 순서는 DOWNLOAD → PUT이지만, 실제 개발은 아래 순서로 진행한다.
MVP2의 범위는 2026-09-15 운영 우선순위에 따라 재정의했다.
DOWNLOAD는 급하지 않으므로 MVP3으로 연기한다.

```text
MVP 1: PUT                                             ✅ 완료 (3개 기관 배포)
MVP 2: · 세트 원자성(Set Completeness Gate)            ✅ 완료 (2026-09-10, 12절)
       · resend — 기간·대상 지정 재전송                 미구현
       · 경로 범용화 — HourLayout 제거 + 제한 재귀 탐색 설계 예정
       · Linux 배포 + 테스트                            미구현 (전용 추가 보안 제외)
       · Retention Cleanup + 경계 테스트                미구현
MVP 3: DOWNLOAD + download_ledger
후속:  BOTH 및 PUT 성능 개선 — MVP 번호·일정 미정
```

MVP2는 현재 운영에서 확인된 문제(세트 결손, 수동 재전송, 기관별 경로 차이,
Linux 배포, 실제 Retention 미작동)를 우선 해결한다. 미완성 세트 보류 리포트는
보안 취약점 우려로 구현하지 않는다.

### 현재 MVP 1 PUT 구현 순서

```text
domain                             ✅ 완료
    ↓
pathpl                             ✅ 완료
    ↓
ledger  db/common/lookup/put, schema_version 5 ✅ 완료
    ↓
config  ini / config / load / validate  ✅ 완료
         (+ LockStaleSeconds, MaxRetries 누적 상한)
    ↓
scan        internal/scan                ✅ 완료
    ↓
verify      Ingress 판정                 ✅ 완료
    ↓
lock        패키지 + main Acquire        ✅ 완료
    ↓
PUT 조립    Scan→Lookup→Verify→Upsert→후보→절단→PENDING
            + --dry-run 관측             ✅ 완료 (2026-08-30)
    ↓
transport   .part → size → rename → 최종 확인   ✅ 완료 (2026-08-31)
            + Transfer Verification (localfs·sftpfs 관통)
    ↓
Recover     시작 시 IN_PROGRESS 회수 (→ FAILED, salvage 없음) ✅ 완료
    ↓
Worker Pool (MaxWorkers = 4) + go test -race              ✅ 완료
    ↓
seed                                                        ✅ 완료
    ↓
Retention Cleanup                                           ← MVP2
```

**현재 절단면은 live 전송(localfs·sftpfs), Transfer Verification,
IN_PROGRESS 회수, Worker Pool, seed까지다.**
live 는 `--transport` 실행 override 또는 `[GENERAL] Transport` 설정을 사용한다.

**`--dry-run` 은 ledger 업무 데이터를 쓰지 않는 관측 수단이다.**
현장 접근이 어려운 상태에서 관측소 개수·확장자 분포·경로 정합성·스캔 소요시간을
한 번에 확정한다. live 와 dry-run 이 같은 Runner 를 탄다.

**흐름의 순서를 바꾸지 않는다.**

```
List → Batch Lookup → 메모리 대조 → (신규·변경만) Verify → Upsert → 후보
     → 정렬(When, file_name, category) → MaxFilesPerRun 절단 → PENDING 일괄 등록
```

정상 운영에서 압도적으로 흔한 결과는 Unchanged 이다(CONCEPT 4.9).
먼저 전부 Upsert 하면 매 실행마다 수만 건의 쓰기를 시도하게 되고
`SetMaxOpenConns(1)` 때문에 전부 직렬화된다.
size·mtime 이 같으면 Upsert 를 호출하지 않는다.
**그러나 Unchanged 를 후보에서 제외하지 않는다** — 아래 「2026-08-30 확정」 참조.

변경이 감지된 파일은 Upsert 로 revision 이 오른 뒤에 후보가 된다.
`put_ledger` 의 PK 가 `(category, file_name, revision)` 이므로
새 revision 의 행이 존재할 수 없음이 보장된다.
live 는 Upsert 후 revision 을 재조회하고, dry-run 신규/변경은 `RevisionPending` 이다.

**`internal/lock` 은 패키지·config·main 배선까지 완료**되었다.
스케줄러 중첩 실행으로 같은 후보가 이중 전송되는 것을 막는다.
`[GENERAL] LockStaleSeconds` 는 로드 시 `General.LockStale`(time.Duration)으로 보관한다
(GraceSeconds 와 같은 방침). 단위는 **초(Seconds)** 로 확정했고 Minutes 키는 기각했다
— 경과시간 키의 단위 규약을 GraceSeconds 와 초 하나로 유지하기 위함이며,
Minutes 안의 이득(가독성·오입력 방향)은 example.ini 주석과 validate 하한 600초로 대체한다.
기본값 10800(3시간)은 크롤링 데이터가 최대 3시간 지연 도착하는 실측에 맞춘다
(SCAN DESIGN 10.9). validate 범위는 600초~86400초.
`main` 은 `lock.Acquire(LedgerPath+".lock", LockStale)` 후 `Release` 한다.
`ErrHeld` 는 exit 0 (회차 겹침은 장애가 아니다).
staleAfter 가 실제 최장 실행보다 짧으면 살아 있는 실행을 탈취해 이중 전송이 되므로
넉넉히 잡는다.

### 2026-08-30 확정 — PUT 조립·Retry·회수·Unchanged

코드(`internal/put`, `internal/ledger/put.go`, `cmd/rinexclient`)와 맞춘 결정이다.
설계안 원문과 다르면 **이쪽이 우선**이다.

#### ① `MaxRetries` — 동일 revision 누적 시도 상한

| | 폐기(구) | 확정(현) |
|---|---|---|
| 의미 | 한 프로세스 실행 안의 재시도 횟수 | **첫 시도를 포함한** 누적 자동 시도 상한 |
| 기본값 | (낮음) | **5** |
| 회차 내 재시도 | — | **없음.** 매시 스케줄러가 곧 retry 간격 |
| 상한 도달 | — | `FAILED` + `attempts >= MaxRetries` 유지. `EXHAUSTED` 상태 신설 없음 |
| 소진 알림 | — | 후보에서 제외하고 `[PUT][WARN]` 으로 알림 |

`MaxRetries < 1` 은 config Validate 와 put Runner 입구에서 거부한다.
시도 0회는 허용하지 않는다.

#### ② IN_PROGRESS 회수 → **FAILED** (PENDING 아님)

설계안 9.3의 "PENDING으로 되돌린다"는 **폐기**한다.

```
시작 시 ListInProgress → 원격 .part 삭제 → FailPut (IN_PROGRESS → FAILED)
이후 후보 선정 → BeginPut (FAILED → IN_PROGRESS) 로 재시도
```

이유: 회수를 PENDING 으로 하면 `attempts` 가 실패로 집계되지 않아
상한·장애 분석이 어긋난다. 이미 `FailPut` / `BeginPut` 경로가 있으므로
복구 전용 PENDING 되돌리기를 두지 않는다.
`domain.Status.CanTransitionTo` 도 `IN_PROGRESS → PENDING` 을 허용하지 않는다.

(조립은 transport 도입 시. 지금은 `ListInProgress` / `FailPut` API 만 준비.)

#### ③ Unchanged ≠ 이미 보냈다

메모리 대조가 Unchanged 여도 **후보 경로에서 떨어뜨리지 않는다.**

- "장부와 size·mtime 이 같다" 는 "이미 보냈다" 가 아니다.
- 직전 실행이 PENDING 등록 직후 죽었다면, 다음 회차에서 파일은 Unchanged 로
  보이지만 put 행은 아직 미전송(또는 PENDING 고아)이다.
- LookupCommon 의 `PutStatus` 조인으로 후보를 판정한다.
  `attempts` 가 필요한 FAILED 부분집합만 LookupPut 을 추가 호출한다.

#### ④ PENDING 일괄 등록 · dry-run · live 전송

- MaxFilesPerRun 절단 **후** 대상만 한 트랜잭션으로 PENDING INSERT
  (`ON CONFLICT DO NOTHING`). 커밋 전에 Worker 를 시작하지 않는다.
- PENDING 등록 실패 시 후보 목록을 반환하지 않는다 (Worker 출발 금지).
- dry-run 은 common/put 업무 데이터를 쓰지 않는다. revision 표시는
  LookupCommon 의 현재 값 또는 `RevisionPending`.
- transport 구현 전에는 live를 거부했으나, 2026-08-31 localfs·sftpfs 구현 후 해제했다.

#### ⑤ 절단 정렬

`When` → `file_name` → `category` (오래된 것부터 배수).

- MVP 1 에서는 **정확성, 무결성, 중복 방지, 실패 추적**을 우선한다.
- **Scan은 순차, 전송은 병렬이다.** 두 가지를 혼동하지 않는다.
  Scan은 디렉터리를 하나씩 훑으며 병렬화 대상이 아니다.
  전송은 MVP 1부터 Worker Pool로 병렬 처리한다. 기본 `MaxWorkers = 4`.
  Go를 선택한 이유가 이 병렬 전송이며, 적정값은 MVP 4 Benchmark로 확정한다. (설계안 12.2)
  **다만 지금 절단면에서는 Worker 를 아직 붙이지 않았다.** transport 와 함께 도입한다.
- `SetMaxOpenConns(1)`이 Ledger 쓰기를 직렬화하므로 `MaxWorkers > 1`에서도
  Ledger 정합성은 안전하다. 단일 Ledger Writer 고루틴 + 배치 커밋은
  정합성 요건이 아니라 **처리량 최적화**이며 MVP 4 범위이다.
- 병렬 전송에서 유일한 실질 위험은 **트랜잭션 안의 중첩 쿼리**이다.
  `BeginTx`가 단 하나의 커넥션을 점유한 상태에서 다른 쿼리를 호출하면
  커넥션 풀 레벨에서 영구 데드락이 된다. `busy_timeout`은 여기서 걸리지 않는다.
  트랜잭션 안에서는 반드시 `*sql.Tx` 핸들만 사용한다. (CONCEPT 4.8)
- 동시성 코드를 추가한 시점부터 `go test -race ./...`를 기본 절차에 포함한다.
- 성능 튜닝은 MVP 4 이전에 과도하게 진행하지 않는다.

### 2026-08-31 확정 — transport(sftpfs) · posix-rename 탐침 · 접속 상한

#### ① posix-rename 판별 — 광고 → 기능 탐침 2단계

Uploader.Rename 계약("대상이 존재하면 덮어쓴다")을 만족하는 SFTP 명령은
`posix-rename@openssh.com` 확장뿐이다(표준 SSH_FXP_RENAME 은 대상 존재 시
동작이 서버 재량). 지원 판별은 접속(Dial) 시점에 2단계로 한다.

1. **광고**: 서버가 SSH_FXP_VERSION 에서 확장을 광고하면 신뢰한다.
2. **탐침**: 광고가 없으면 실존할 수 없는 경로(나노초 타임스탬프)로
   PosixRename 을 1회 보내 서버의 **행동**으로 판별한다.
   "no such file" 계열 → 지원(대상이 없었을 뿐). 그 외 → 미지원으로
   서버 응답을 담아 시끄럽게 실패.

근거: 확장 광고는 명세상 **선택 사항**이다. SFTPGo 2.7.5 가 posix-rename 을
처리하면서도 광고하지 않음이 로컬 실측(2026-08-31)에서 확인됐다. 광고 단독
판정(HasExtension)은 거짓 음성 — 측위원처럼 SFTPGo 를 운영하는 기관으로의
전송을 통째로 오탐 차단한다. 광고는 서버의 말이고 탐침은 행동이며,
계약이 요구하는 것은 행동이다. 최종 심판은 계약 테스트
`RenameOverwritesExisting`(실파일 덮어쓰기 실증)이다.

**기각 목록**
- **첫 Rename 시점 판정** — 진짜 미지원 서버에서 매시 MaxFilesPerRun 개
  파일이 업로드까지 마친 뒤 실패하며 attempts 를 소모한다. attempts 는
  파일의 문제에 쓰는 예산이지 환경의 문제에 쓰는 예산이 아니다
  (preflight attempts 결정과 동일 원칙).
- **표준 Rename 강등**(미지원 시 표준 Rename 으로 계속) — 기각에 가까운
  보류. revision 재전송(어제 실측: 하루 절단 회수 153건)이 이 도메인의
  일상 경로라, 덮어쓰기 불가 서버에서 강등은 "일상 경로의 상시 고장"이
  된다. 실측상 대부분 기관이 SFTPGo 가동 중이라 밟을 확률도 낮다.
  posix-rename 미지원 서버가 실재로 확인되는 날 재론한다.
- **Remove → Rename fallback** — 재론 금지 유지. Remove 성공 후 Rename
  실패 시 원격의 멀쩡한 옛 파일까지 잃는다.

#### ② SFTP 접속 상한 — `dialTimeout` 상수 10초

TCP 연결과 SSH handshake 가 **하나의 10초 예산을 공유**한다(절대 deadline
선계산). `ssh.ClientConfig.Timeout` 만으로는 handshake 매달림을 못 막고,
매달린 프로세스는 lock 을 쥔 채 살아 LockStale takeover → 이중 실행의
전제 붕괴로 이어진다. handshake 후 **deadline 해제는 필수**다(남기면 접속
10초 뒤부터 모든 I/O 가 timeout — 소량 테스트에선 안 드러나고 다건에서
터지는 시한폭탄). config 키로 두지 않는다 — 운영 중 조정할 근거가 관측되면
그때 [PUT.SFTP] 로 승격한다(승격 시 구조체·knownKeys·validate·example·문서
다섯 곳 동반).

#### ③ 실서버 환경 실측 (관련정보 문서 260527 대비 갱신)

- 지리원·서울시: OS 세대교체 확인(직접 타건). 문서의 Server 2003 기록은 낡음.
- 측위원: **SFTPGo 가동 중** — 대표가 명세에 SFTPGo 를 지정한 근거.
- 설치처: 기관 서버가 아니라 **중간 전달자 호스트**(대표 확인). Go 1.27
  산출물 제약(Win10/Server2016+)은 전달자 호스트 설치 시 체크 항목.
- 확인 잔여: 전달자 호스트 OS 확정, 기관별 SFTPGo 버전 목록화(제안:
  신규 구축분은 SFTPGo 최신 안정판 표준화), mismatch 혼입 파일 정책.
- ※ 실측 "10/12 버전" 표기가 정확히 무엇의 버전인지 명기 필요.

#### ④ Close 오류 취급 (Uploader 양쪽 공통 원칙)

전송 계층의 Close 오류는 버리지 않는다. 단 io.EOF·net.ErrClosed 는 정상
종료의 다른 모습이므로 **구성원 단위로** 흡수한다(errors.Join 결과에
일괄 필터를 걸면 한쪽의 EOF 가 다른 쪽의 실오류를 삼킨다 — sftpfs.Close).

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
  - `Category.MatchesName` — RINEX2 short filename과 RINEX3/RINEX4 long filename의
    Daily/Hourly 주기를 대조한다.
    RINEX3/RINEX4는 `_` 분리 주기 필드(`01D` / `01H`)를 사용하며,
    RINEX2는 `SSSSDDDh.YYt` 형태의 `h` 값(`0`=Daily, `a`~`x`=Hourly)을 대조한다.
    명확히 판정할 수 없는 파일명은 `CategoryMatchUnknown`으로 유보한다.
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
- Linux 전용 자격증명 암호화·개인키 권한 강제 등 추가 보안 기능은 선제 구현하지 않는다.
  공식 보안점검에서 구체적인 요구가 나온 경우에만 요구 범위에 맞춰 별도 설계·구현한다.
  기존 SSH 공개키 인증과 `known_hosts` 검증은 SFTP 연결의 공통 기본 동작으로 유지한다.
- **`KnownHosts` 가 비어 있으면 시작을 거부한다.** 검증을 생략하고
  "일단 붙여보는" 경로를 만들지 않는다.
- **`AuthMethod` 는 `publickey` 만 지원한다.** 설정 파일에 평문 비밀번호를 두지 않으므로
  다른 값을 받아둘 이유가 없다.

**설정 파일의 위치**

- 기본은 **실행파일과 같은 디렉터리의 `config.ini`** 이다.
  현재 작업 디렉터리를 기준으로 하지 않는다.
  `schtasks` 가 작업 디렉터리를 실행파일 위치와 다르게 잡을 수 있어
  "설정 파일이 없다" 로 끝나는 사고가 흔하다.
- `--config` 플래그로 다른 경로를 지정할 수 있게 둔다.

**`config.example.ini` 와 코드의 정합성을 테스트로 고정한다.**
어긋나면 두 가지 사고가 난다. 코드에만 키를 추가하면 새 서버 설치 때
그 키가 누락되고, 템플릿에만 두면 실행이 unknown key 로 거부된다.
사람의 기억에 맡기지 않는다.

## 9. 기존 운영 스크립트에서 확인한 사실

측위원에서 실제로 운용 중인 WinSCP 기반 스크립트(다운로드용 PowerShell,
전송용 배치, 원격 정리용 PowerShell)를 분석하여 확인한 항목이다.
설계안에 없거나 설계안과 다른 것만 적는다.

### 9.1 경로와 값

| 항목 | 확인된 사실 |
|---|---|
| DOY 표기 | **3자리 0채움**. 정리 스크립트가 `'^\d{3}$'` 로 폴더를 매칭한다 |
| 시각 기준 | **UTC**. 스케줄은 서버 로컬시간이나 대상 날짜 계산은 UTC이다 |
| 실행 주기 | 매시각 :10, `schtasks /SC HOURLY /MO 1` |
| 전송·수신 범위 | 오늘 포함 **7일**. `ScanDays`(Deep Scan)를 7로 정한 근거이다 |
| 원격 보관 | 30일 |
| 대상 확장자 | 스크립트는 `*.gz` 만 전송·수신한다. 실제 유입은 `.gz` / `.Z` / `.zip` 이 관측소마다 혼재한다. 아래 9.7 참조 |
| 파일명 대소문자 | RINEX2 소문자 / RINEX3 대문자 혼재. `NormalizeName` 의 소문자 통일 근거 |
| Category 구분 | RNX2_D / RNX2_H / RNX3_D / RNX3_H 4종. `domain.Category` 와 1:1 |
| 파일 생성 방식 | 0바이트 파일이 관측된다. 원인은 선행 프로그램의 **중복 복사**이며 1시간 이내에 채워진다. 이후 추가되지 않는다 (2026-08-28 정정) |
| 원본 스토리지 | **SMB(NAS)**. `X: → \\192.168.103.100\gnss-prod-nas\data` |
| 로컬 보존 | **10년**. `2016`~`2026` 폴더 확인. `LedgerRetentionDays > ScanDays` 가 절대 조건인 이유 |
| 관측소 수 | 국토지리정보원 109개. 시간 슬롯의 약 22% 는 비어 있는 것이 정상 |

**`ScanRecentDays` 는 7이 아니라 2이다.** 위 7일은 Hot Scan 이 아니라
Deep Scan 의 근거이다. 두 값의 역할이 다르므로 혼동하지 않는다.

| | 값 | 주기 | 역할 |
|---|---|---|---|
| `ScanRecentDays` | 2 | 매시간 | 정상 유입 |
| `ScanDays` | 7 | 하루 1회 | 며칠 늦게 유입된 자료 회수 |

`ScanRecentDays` 를 1로 두지 않는 이유는 경로 토큰이 UTC 인데
실제 도착이 3~4시간 이상 지연되기 때문이다.
1일이면 UTC 하루의 마지막 몇 시간 분량이 매일 Hot Scan 을 빠져나간다. (9.9)

경로 문자열 자체는 세 스크립트가 서로 다르다(하이픈/언더스코어, 드라이브, 기관 접미사).
전부 Path Template 의 리터럴 구간이므로 `config.ini` 값만 바꾸면 되며,
이것이 설계안 10절이 의도한 바이다. 실서버 반입 전에 실제 경로를 확정한다.

### 9.2 설계안에 없는 필수 절차 — 원격 디렉터리 사전 생성

전송 스크립트는 `put` 전에 목적지 경로를 세그먼트 단위로 재귀 생성한다.
설계안 8절에 이 절차가 없다. **없으면 첫 전송이 전부 실패한다.**

구현 시 반영할 세부는 세 가지다.

- 경로를 `/` 로 쪼개 상위부터 순차 `mkdir` 한다
- 드라이브 세그먼트(`E:`)는 건너뛴다
- 이미 만든 경로는 집합으로 기억하여 중복 호출하지 않는다

### 9.3 Hourly Scan 의 `(HH)` 계산 — 현행 과도기 구현, MVP2에서 교체 (2026-09-15 재정리)

**현재 결론 (구현 지시):** `HourLayout`은 장기 설계가 아니라 서울시의 `(HH)` 없는
평면 Hourly 경로를 급히 지원하기 위해 2026-09-04 추가한 과도기 설정이다.
지리원에도 `(HH)` 폴더가 없고 다음 Linux 설치처의 경로도 다를 가능성이 있어,
MVP2에서 `HourLayout`과 `(HH)` 24회 계산 의존을 제거하고 설정 루트 아래의
**범위 제한 재귀 탐색**으로 대체한다.

원격은 로컬 하위 구조를 복제하지 않는다. 발견한 파일은 RINEX 버전별 고정
평면 경로(운영 예: `D:\incoming\RNX2`, `D:\incoming\RNX3`)로 전송하므로
파일의 날짜·시각으로 원격 경로를 계산할 필요가 없다.

재귀 시작점·최대 깊이·날짜 범위 적용 방식·권한 오류·symbolic link/junction
정책은 대표가 Linux OS 버전과 실제 경로를 확인한 뒤 확정한다. 이 문서에서
먼저 추측하여 고정하지 않는다.

**하지 말 것.** Hourly `(HH)` 필수 검증을 더 엄격히 만들거나, 날짜당 00~23
계산을 장기 설계로 되돌리거나, 기관이 늘 때마다 `HourLayout` 배치 값을
추가하지 않는다. 목표는 과도기 우회를 제거하는 것이다.

**현재 코드는 과도기다.** `HourLayout=dir/flat`, Hourly `(HH)` 유무 검증,
시각 0~23 전개는 서울시 긴급 대응용으로 **아직 남아 있는 구현**이다.
유지하다가 MVP2에서 제거하는 대상이지, 재강화하거나 새 기관용으로 확장하는
대상이 아니다.

아래 「역사 기록」은 2026-08-28~09-04 당시 결정이다. 현재형으로 적혀 있어도
현행 구현 지시가 아니다. 위 현재 결론이 우선한다.

#### 역사 기록 — 정확한 템플릿 전개 채택 (2026-09-15 폐기)

당시 이전 판은 "계산하지 않고 하위 디렉터리를 나열한다"였다. 그때는 기각했다.

기각 근거는 당시 현장 확인이었다.

> 당시에는 모든 관측소가 시각까지 디렉터리를 둔다고 판단했다.
> 2026-09-15 재확인 결과 지리원에는 `(HH)` 폴더가 없으므로 이 전제는 폐기한다.

**당시 결정:** config의 Hourly 경로는 `LocalPath`와 `RemotePath` 양쪽 모두
시각까지 적고, Scanner는 날짜당 0~23을 만들어 각각 `ReadDir`한다.

```
Hourly   E:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\
Daily    E:\RINEX-V3-D\(YYYY)\(DOY)\
```

**당시 나열 방식을 기각한 이유 세 가지**

- **당시에는 `RemotePath`에도 `(HH)`가 필요하다고 가정했다.** 03시 파일을
  원격 03 디렉터리에 놓아야 한다고 봤기 때문이다. 현재 운영 목적지는
  RINEX 버전별 고정 평면 경로이므로 이 근거는 폐기한다.
- **나열 방식은 필터 규칙이 하나 더 필요하다.** `^\d{2}$` 및 00~23 검증을
  적용해야 하고, 현장 화면에 `00_backup` 같은 항목이 섞여 있어 예외 관리가 늘어난다.
- **`pathpl.HasToken`은 이미 구현·테스트가 끝난 검증 장치였다.** 나열 방식이면
  쓸 곳이 없다고 봤다.

**당시 원격 왕복 절감 비교** — 이득이 작다고 판단했다.

| 방식 | Deep Scan 7일 · Hourly 2종 | RTT 30ms |
|---|---|---|
| **(HH) 계산 (당시 채택)** | **336회** | **약 10초** |
| 하위 디렉터리 나열 | 14회 | 약 0.4초 |

1시간 주기 실행에서 10초는 감당된다고 봤다.
설계안 11.1이 지적한 "스캔 자체가 병목"은 상위 폴더를 재귀 탐색할 때의 이야기이며,
당시에는 `pathpl`로 말단 디렉터리를 직접 계산하는 방식에 해당하지 않는다고 적었다.
2026-09-15 이후의 목표는 범위 제한 재귀 탐색이며, 무제한 전체 디스크 walk가 아니다.

**당시 config 검증 (현재 코드에 남은 과도기 동작).** 아래 표를 근거로 Hourly
`(HH)` 거부를 **더 강하게 만들지 않는다.** MVP2에서 검증 자체를 재귀 탐색
정책에 맞게 교체한다.

| 조건 | 당시 거부 이유 |
|---|---|
| Hourly 인데 `(HH)` 없음 | 0~23시가 모두 같은 디렉터리로 확장된다. 경로가 실제로 존재해 오류도 나지 않고 파일도 일부 발견되어 아무도 알아채지 못한다 |
| Daily 인데 `(HH)` 있음 | 존재하지 않는 경로가 24개 생기고 전부 `ENOENT` 가 된다. Scan 이 이를 정상으로 취급하므로 조용히 아무 일도 하지 않는다 |

**과도기 Scanner 주의 — 없는 디렉터리는 오류가 아니다.** 시각 전개가 남아
있는 동안에는 다음을 유지한다.

```go
if errors.Is(err, fs.ErrNotExist) { continue }
```

크롤링 매트릭스 기준 시간 슬롯의 약 22%가 비어 있다.
이를 오류로 취급하면 정상 운영에서 매시간 수백 건의 오류 로그가 쌓여
진짜 오류가 묻힌다.

### 9.4 Transfer Verification 이 필요한 실증 근거

전송 스크립트는 `option batch continue` 와 `option failonnomatch off` 를 사용하며,
스크립트 자체에 다음 주석이 있다.

```
[NOTE] WinSCP returns 0 even if files were skipped (option batch continue).
[NOTE] If you saw "No such file (code 2)", NOTHING was uploaded
```

**한 건도 올라가지 않아도 종료코드가 0 이다.**
"전송 함수가 오류 없이 끝났다는 사실만으로 VERIFIED 로 두지 않는다"(설계안 8.1)는
원칙이 추상적 방어가 아니라 현장에서 실제로 발생한 문제임을 보여준다.

### 9.5 RemotePath 형태 주의

SFTP 서버가 파일시스템을 어떻게 노출하느냐에 따라 경로 형태가 달라진다.

```
전체 파일시스템 노출 → /E:/RINEX2Outgoing/...
E:\ 로 chroot        → /RINEX2Outgoing/...      ( /E: 를 뺀다 )
```

형태가 틀리면 모든 `mkdir` / `put` 이 `No such file (code 2)` 로 실패한다.
`config.ini` 의 `RemotePath` 값 하나로 흡수되므로 코드에 분기를 두지 않는다.

### 9.6 범위에 넣지 않은 것

| 항목 | 판단 |
|---|---|
| 관측소 필터 (`STATIONS`) | 신규 프로그램은 전체 관측소 대상이므로 두지 않는다 |
| 원격 보관기간 정리 | **미결정.** 원격 삭제는 오작동 시 복구가 불가능하므로 MVP 1 에서는 제외하는 쪽을 검토 중이다. 대표 요구인 "단일 통합 프로그램"과 상충할 수 있어 확정 필요 |
| 결측 리포트 (관측소가 파일을 아예 만들지 않은 경우) | **MVP 2 이후.** 장부는 "본 것" 의 기록이지 "있어야 할 것" 의 기록이 아니므로 단독으로는 답할 수 없다. 별도 크롤링 매트릭스와 대조하는 조회 명령으로 분리한다 |
| DOWNLOAD | **현재 알려진 모든 기관에 불필요.** 원본 서버와 중간 서버 양쪽에 설치할 수 있으면 다단 구성도 PUT 두 번으로 성립한다. "타 기관 소유라 설치가 불가한 서버" 가 나타날 때를 위한 빈 칸이다 |

### 9.7 확장자 필터를 두지 않는다

기존 스크립트는 송·수신 모두 `*.gz` 만 처리한다.
`option batch continue` + `failonnomatch off` 조합 때문에
필터에 걸려 한 건도 보내지 않아도 종료코드는 0 으로 끝난다.

신규 프로그램은 **확장자 필터 개념 자체를 두지 않는다.**
기관의 파일 규칙은 시간에 따라 바뀌므로 거르지 않는 편이 누락 위험이 낮다.

**실제 유입 형태 (2026-08-28 현장 확인으로 정정)**

이전 판은 스크린샷을 근거로 "비압축 `.rnx` 가 함께 유입되며 Hourly 는 비압축만 있다" 고
적었다. **그 화면은 Incoming(입고 지점)이었다.**
우리가 스캔하는 DOY 디렉터리에는 QC 를 마친 **압축 완제품만** 저장된다.

| | 실제 |
|---|---|
| 압축 형식 | `.gz` / `.Z` / `.zip` 세 가지. **관측소마다 다르며 한 디렉터리에 섞여 있다** |
| 비압축 유입 | DOY 디렉터리에는 오지 않는다 |
| 취급 | 전부 전송한다. 필터를 두지 않는다 |

한때 "수신 측이 압축본만 요구하므로 비압축을 배제하자" 는 안(`ExcludeUncompressed`)을
검토했으나, 위 사실이 확인되면서 **철회했다.** 거를 대상이 없다.

**압축 확장자 목록을 관리하지 않는다.**
정규화 규칙이 "인식" 이 아니라 "보존" 이므로 `.part` 만 제거하고 나머지는
문자열 그대로 둔다. 새로운 압축 형식이 등장해도 코드를 수정하지 않는다.
목록이 필요한 곳은 `BaseName` 산출 한 곳뿐이며 그 값은 식별자가 아니다. (CONCEPT 4.2)

**Hatanaka 압축(`.crx`)** — 설계안과 CONCEPT 에 언급이 없다.
`BaseName` 은 압축 확장자만 제거하므로 `.rnx` 계열과 `.crx` 계열은
서로 다른 `base_name` 그룹이 된다. `.crx` 는 단순 압축이 아니라 포맷 변환이므로
현재는 이 동작을 유지한다. `base_name` 은 관측 수단이므로
나중에 규칙을 넓혀도 되돌리는 비용이 없다. (CONCEPT 4.7, 7)

**운영상 대가** — 같은 관측 데이터가 여러 압축 형태로 존재하면 전송량이 늘어난다.
실제로 그런 상황이 발생하는지는 `base_name` 중복 조회로 관측한다.

```sql
SELECT base_name, COUNT(*) FROM common_ledger
 GROUP BY base_name HAVING COUNT(*) > 1;
```

### 9.8 Deep Scan 주기는 상태를 저장하지 않는다

주기 실행(매시 :10) 구조에서는 매 실행이 새 프로세스이므로
"오늘 Deep Scan 을 했는가"를 메모리에 둘 수 없다.

`scan_state` 테이블을 두는 안을 검토했으나 **채택하지 않았다.**
`config.ini` 의 `DeepScanHour` 와 실행 시각의 시(hour)를 비교하면
상태 저장 없이 하루 한 번이 보장된다.

| | 해당 시각 실행이 걸러졌을 때 |
|---|---|
| `DeepScanHour` 비교 | 그날 Deep Scan 없음. 다음날 같은 시각에 수행 |
| `scan_state` 테이블 | 다음 실행에 "24시간 경과"로 판정하여 수행 |

차이는 최대 하루 지연뿐이다.
Deep Scan 은 안전망이지 주 경로가 아니며, 30일 구간 안에서 매일 회수 기회가 있다.
반면 테이블을 두면 DDL · 읽기/쓰기 코드 · 스키마 개정 · CONCEPT 갱신이 따라온다.
운영 중 Deep Scan 누락이 잦은 것이 확인되면 그때 도입한다.

**`DeepScanHour` 의 시간대에 주의한다.**
`pathpl` 의 토큰 확장은 UTC 로 강제하지만, `DeepScanHour` 는 스케줄러와 기준을 맞춘다.
`schtasks` 는 서버 로컬시간으로 동작하며 "전송량이 적은 시간대"도 로컬 개념이다.
UTC 로 둘 경우 `4` 는 KST 13시(한낮)가 되므로 값과 주석이 어긋나기 쉽다.
`config.ini` 주석에 어느 기준인지 반드시 명시한다.
`internal/config` 의 `ScanConfig.DeepScanHour` 주석에도 같은 내용을 둔다.

### 9.9 자동 Scan과 운영자 재전송을 분리한다 (2026-09-15 갱신)

자동으로 도는 두 갈래와 사람이 부르는 한 갈래를 나눈다.

```text
[자동 · 정상운영]
  Hot Scan       최근 2일   매시간    정상 유입
  Deep Scan      최근 7일   하루 1회  며칠 늦게 유입된 자료 회수

[수동 · 운영자 재전송]
  resend         운영자가 기간·대상을 지정
                 세트 게이트는 우회
                 전체 강제 재전송인지 Ledger 선별 방식인지는 구현 전 결정 필요
```

**`ScanRecentDays = 2` 인 이유** — 경로 토큰은 UTC 인데 실제 도착이 3~4시간
이상 지연된다. 1일로 두면 UTC 하루의 마지막 몇 시간 분량이 매일 Hot Scan 을
빠져나가고 Deep Scan 이 하루 늦게 회수한다.

Deep Scan 범위 밖의 자료는 자동 후보가 아니다. 과거 장애 기간은 운영자가
`resend`에 명시한다. 다만 허용 기간과 보존기간 밖 요청의 경고·거부·강제 실행
정책은 「MVP2 구현 전 결정 필요」에 따라 구현 전에 확정한다.

한때 상한을 없애고 디렉터리 mtime 으로 거르는 안을 검토했으나 **기각했다.**
디렉터리 mtime 은 파일 생성뿐 아니라 **삭제 시에도 갱신되며**,
로컬 보존이 10년이므로 정리 작업이 돌면 오래된 디렉터리가 통째로
스캔 대상이 되어 장부에 없는 파일이 전량 재전송된다.
`UseDirMtimeSkip = false` 를 유지하는 실질 근거가 이것이다.

`resend`가 Ledger 판정을 재사용하는 방식이라면 `LedgerRetentionDays`가 안전
범위의 실질 상한이 된다. 전체 강제 재전송 방식이라면 같은 의미의 상한이 아니다.
현재 확정된 것은 `config`가 `LedgerRetentionDays > ScanDays`를 시작 시 강제한다는
점이며, `resend`와의 결합 규칙은 구현 전 결정한다.

**2026-09-16 구현 상태:** `[LEDGER] RetentionDays = 30`과 위 불변식 검증은
구현되어 있지만 실제 DB 행을 지우는 Retention Cleanup은 아직 구현되지 않았다.
따라서 현재 실행파일은 30일이 지나도 `common_ledger`와 연결된 `put_ledger`
행을 자동 삭제하지 않는다. `cmd/rinexclient/main.go`에도 Deep Scan 뒤 Cleanup이
TODO로 남아 있다.

MVP2에서 다음을 구현·검증한다.

- 성공한 Deep Scan 뒤 `ingress_verified_at < now - RetentionDays`인 common 행 삭제
- FK `ON DELETE CASCADE`에 의한 해당 put 행 삭제
- 경계 시각의 포함/제외, 최근 행 보존, Cleanup 실패 처리
- SQLite 파일 크기는 즉시 축소하지 않고 free page를 재사용하며 정기 VACUUM은 하지 않음
- 재귀 Scan이 Retention 범위 밖의 오래된 파일을 다시 신규로 올려 재전송하지 않도록
  Scan 날짜 범위와 Cleanup 정책을 함께 검증

**운영 로그에 `oldest_new` 를 남긴다.**

```text
[DEEPSCAN] dirs=336 files=42000 new=340 oldest_new=2026-08-23 (5일 전)
```

`ScanDays = 7` 은 잠정값이다. 한 달 운영하면 이 값의 분포가 나오고,
7일에 붙어 잘리는 것이 보이면 늘려야 한다는 증거가 된다.
지금 앉아서 정할 수 없는 값이므로 **나중에 답을 가져올 장치를 대신 넣는다.**

### 9.10 첫 붙임은 "추가 투입" 이 아니라 "기존 송신자 대체" 이다

빈 장부로 붙이면 Deep Scan 범위 전체가 신규로 판정되어 한 번에 큐에 들어간다.
기존 송신자가 이미 보낸 것을 통째로 재전송하는 셈이다.

**`seed` 는 원격 나열을 근거로 한다. 로컬 존재를 근거로 삼지 않는다.**

로컬에 있다는 것은 보내졌다는 뜻이 아니다.
기존 송신자가 실패한 파일까지 `VERIFIED` 로 굳으면 revision 도 오르지 않고
후보 조회에서도 빠지며 `resend`로도 부를 수 없어 **영구 미전송**이 된다.

```text
seed --days N
  1) 로컬 Scan → common_ledger 등록
  2) 원격 목적지 디렉터리 나열
  3) 원격에 있고 size 일치 → put_ledger VERIFIED
     그 외                 → 기록하지 않음 (다음 정기 실행이 전송)
```

"원격 존재 + size 대조" 는 설계안 8.1 의 Transfer Verification 과 동일한 기준이다.
전송만 생략했을 뿐 검증은 실제로 수행한다.
RemotePath 가 어긋나면 아무것도 찾지 못해 전부 전송 대상이 된다.
**헛전송이지 누락이 아니다.**

**전환 순서**

```text
--dry-run 으로 규모 확인
  → 기존 송신자 종료를 실측 확인 (통보만 믿지 않는다. 어긋난 전례가 있다)
  → seed
  → 정상 가동
```

`MaxFilesPerRun` 은 어느 쪽이든 최후 방어선으로 둔다.
`seed` 를 빠뜨렸을 때뿐 아니라, 대량 복사로 mtime 이 일괄 갱신되어
Scan 범위 전체가 재전송 대상이 될 때도 회선을 보호한다.

## 10. Git 제외 권장 항목

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

## 11. 프로젝트 별도 지침 원문

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
> 따라서 아래 원문의 과거 MVP 번호·기본값·구현 예정 표기는 현재 상태가 아니다.
> 현재 기준은 본 문서 상단의 「MVP2 현재 실행 범위」와 5절을 우선한다.

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

Category — RINEX2_DAILY / RINEX2_HOURLY / RINEX3_DAILY / RINEX3_HOURLY / RINEX4_DAILY / RINEX4_HOURLY

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

Ledger의 전송 상태는 PENDING → IN_PROGRESS → VERIFIED 또는 FAILED로 정의하고, 상태 갱신은 트랜잭션 안에서 수행한다. 프로그램이 비정상 종료되면 IN_PROGRESS 상태로 남은 항목과 정리되지 않은 .part 파일이 함께 존재하게 되므로, 시작 시 해당 항목을 조회하여 잔여 .part 파일을 삭제하고 상태를 **FAILED**로 되돌린다(설계안 원문의 PENDING 되돌리기는 폐기 — 5절 「2026-08-30 확정」 ②). 이후 동일 revision 재시도는 `BeginPut`(FAILED → IN_PROGRESS)이 담당한다. 이 절차가 없으면 .part 파일이 계속 누적되어 저장공간을 잠식한다.

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

---

## 12. MVP2 세트 완성도 게이트 — 확정 설계·구현 기록 (2026-09-09 ~ 09-10)

> [!CAUTION]
> **역사 기록 봉인 — 이 절 본문을 현행 개발·운영 지시로 읽지 않는다.**
>
> 이 절은 2026-09-10 당시 세트 원자성 결정과 구현 과정을 보존한 스냅샷이다.
> 현재 MVP2 범위·제외 항목·미결 사항의 **유일한 현재 기준**은 문서 상단
> 「MVP2 현재 실행 범위」와 「MVP2 구현 전 결정 필요」다. 이 절과 상충하면
> 문서 상단이 이긴다.
>
> 15절과 18절에만 2026-09-15 안내를 덧붙였다. 이는 봉인 본문 전체를
> 현행화했다는 뜻이 아니다. 1.3절 게이트 활성화 전제, 16절 config 주석의
> 활성화 전제, 17절 원칙 3·11은 당시 문면 그대로 남아 있으며, 현재형으로
> 적혀 있어도 지금 구현하거나 운영 산출물로 삼지 않는다.
>
> 이 절에서 현재도 유효한 것은 세트 게이트의 **이미 구현된 의미론·스키마
> 근거**(기본 OFF, kind 파생, 미완성 세트 전체 보류, 유보 개별 통과,
> `resend` 우회 등)다. 게이트 활성화 운영 절차, 미완성 세트 보류 리포트,
> RINEX3 운영 프로파일은 현재 개발 산출물에서 제외한다.

> 본 절은 `SFTPClient_MVP2_CONFIRMED_DECISIONS_v2.md` 를 본 문서로 통합한 것이다.
> 별도 파일과 이 절은 모두 통합 시점의 역사 기록이며, 이후 개정은 문서 상단의
> 현행 범위와 각 단일 주제 문서를 기준으로 한다.
> 본 절 내부의 "§n" 표기는 아래 통합 원문의 절 번호를 가리킨다 (본 문서의
> 1~11절과 무관). 코드 주석의 "확정 §n" / "v2 §n" 참조도 아래를 가리킨다.

> 작성일: 2026-09-09 (v2) / 최종 갱신: 2026-09-10 (구현 완료 반영)
> 범위: `recovery` 구조, 세트 완성도 게이트(Set Completeness Gate)
> 상태: **확정 + 구현 완료** — 2026-09-10, 5개 커밋으로 소스 반영·전체 테스트 통과.
> §5 는 3차 확정으로 교체되었고, §19 가 구현 중 확정된 의미론·구현 사항을 기록한다.
>
> 본 문서는 다음 두 문서를 **대체**한다. 두 문서는 폐기한다.
> - `SFTPClient_SET_ATOMICITY_DESIGN.md` (Claude 초안 — kind 표기 `mo.crx` 등 본 문서와 상충)
> - `SFTPClient_MVP2_CONFIRMED_DECISIONS_2026-09-09.md` (v1 — §5 카테고리→버전 매핑이 v2에서 번복됨)

---

### 1. recovery 구조

#### 1.1 A안 확정

재전송과 수신은 서로 다른 방향의 작업이므로 명령을 분리한다.

- `resend` — 로컬 → 원격. 특정 기관/관측소/기간의 파일을 다시 전송한다.
- `download` — 원격 → 로컬. 특정 기관/관측소/기간의 파일을 수신한다.
- 기존 `Recover` — 비정상 종료로 `IN_PROGRESS`에 남은 항목을 회수하는 크래시 복구. 역할이 다르므로 그대로 구분한다.

신규 기간 지정 재전송 기능에 `recovery`라는 이름은 사용하지 않는다.

#### 1.2 resend는 세트 게이트를 우회한다 (v2 확정)

세트 게이트는 **자동 파이프라인의 판단**이고, `resend`는 **운영자의 명시적 판단**이다.
운영자가 기간과 대상을 지정해 재전송을 지시했다면 프로그램이 세트 완성도를 이유로
거부하지 않는다. 미완성 세트를 의도적으로 내보내는 유일한 정규 경로가 `resend`다.

#### 1.3 게이트 활성화의 전제조건 (v2 확정)

배포 토폴로지: 프로그램은 각 기관의 **송신 서버**에 설치되어 지리원 측 SFTPGo로
전송하며, 송신 서버는 사무실에서 원격 접근이 불가능하다. 따라서 `resend` 실행에는
현장 접근(방문 또는 현장 담당자 경유)이 필요하다.

이 조건에서 게이트 활성화는 기능 배포가 아니라 **운영 결정**이며, 켜기 전에
"이 서버에서 영구 미완성 세트가 발생하면 어떻게 해소하는가"에 답이 있어야 한다.

1. `resend`가 구현·배포되기 전에는 어떤 기관에도 게이트를 켜지 않는다.
2. 현장 접근이 어려운 송신 서버에 게이트를 켜려면, 그 시점에
   자동 방출 장치(예: MaxHoldDays — N일 경과 시 경고와 함께 보유분 부분 전송)를
   구현하여 함께 배포한다. **MaxHoldDays는 현재 미구현이며, 이 조건이 성립하기
   전까지 구현하지 않는다.** (요건 없는 선제 구현 금지)

> 참고 — 지리원 서버에서 타 기관 송신 서버가 보이는지는 미확인이며,
> 설계는 이에 의존하지 않는다. `resend`는 송신 서버에서 실행되는 명령이다.

---

### 2. 세트 원자성의 의미

MVP2에서 구현하는 원자성은 **전송 시작 원자성**이다.

> 필수 파일 종류가 모두 존재할 때만 해당 세트의 파일을 전송 후보로 올린다.

("해당 세트의 파일" = 선택 종 포함 전체. §19-1 의 전체 보류 확정 참조.)

전송 시작 후 개별 파일이 실패해도 이미 전송된 원격 파일을 삭제하거나 롤백하지 않는다.
부분 실패는 기존 Ledger의 `FAILED → Retry` 경로로 다음 회차에 수렴시킨다.

MVP2에서 하지 않는 것:

- 원격 파일 롤백 / 일괄 삭제 / 트랜잭션성 원격 커밋
- `put_ledger`에 `HELD` 상태 추가

---

### 3. 세트 게이트는 기본 OFF

도봉 사례(DOY 250, G·L·N 영구 미도착)는 실제 장애이지만 현재까지 희귀 사례다.
같은 날 다른 서울시 관측소는 전체 파일이 정상 생성되었고, 기관마다 제공하는
파일 종류도 다르다 (예: 위성센터는 O만 제공).

따라서 세트 완성도 검사는 **전역 강제 규칙이 아니라 Opt-in 기능**이다.

기본값:

```ini
[SET.RINEX2]
RequiredKinds = false

[SET.RINEX3]
RequiredKinds = false

[SET.RINEX4]
RequiredKinds = false
```

`RequiredKinds = false` → 세트 검사 OFF → MVP1과 동일하게 파일 단위 전송.
필요한 경우에만 `false`를 지우고 실제 필수 종류를 적는다.

---

### 4. `RequiredKinds` 설정 규칙

Boolean과 목록을 한 키에서 처리한다.

| 값 | 의미 |
|---|---|
| `false` | 게이트 OFF |
| CSV 목록 (예: `G,L,N,O`) | 게이트 ON. 목록의 모든 kind가 존재해야 세트 완성 |
| 빈 값 | 설정 오류 |
| `true` | 필수 kind가 없으므로 설정 오류 |

별도의 `Enabled=true/false` 키는 두지 않는다.
`Enabled=false` + `RequiredKinds=MO,MN` 같은 모순 설정을 만들지 않기 위해서다.

섹션/키 부재 시의 동작은 별도 규칙으로 규정하지 않는다. 기본 배포 config에
세 섹션이 포함되며, 운영자가 이를 삭제하는 시나리오는 방어 대상으로 보지 않는다.
(구현상 부재는 OFF와 동일하게 동작한다.)

---

### 5. 카테고리 → 버전 매핑: 닫힌 열거형 메서드 (3차 확정 — v1·v2 §5 를 모두 대체)

#### 확정

RINEX 버전의 출처는 **하나**다: `domain.Category` (닫힌 열거형) 의
`RinexVersion()` 메서드.

```go
func (c Category) RinexVersion() (int, error)   // 미지원 값은 err
```

- config 에 `RinexVersion` 키를 두지 않는다. `[PUT.<CATEGORY>]` 섹션
  이름이 열거형으로 변환되는 순간 버전이 결정된다.
- 세트 정책은 종전대로 버전 단위 `[SET.RINEXx]` 섹션의 `RequiredKinds`
  가 지정한다. 이 결정은 세트 지정 방식에 아무 영향이 없다.
- 흐름: 섹션 이름 → ParseCategory(열거형 강제) → RinexVersion() →
  해당 `[SET.RINEXx]` 정책 적용.

#### 안전 장치 (이 결정의 성립 조건 — 삭제 금지)

열거형 방식의 안전성은 다음 두 장치가 담보한다. §5 의 "명시 키 + 대조
검증"을 이 둘이 **대체**하므로, 중복으로 보여도 제거하면 안 된다.

1. `setkey_test.go` 의 `TestRinexVersion_TotalOverCategories` —
   `Categories()` 전수가 버전으로 매핑됨을 고정한다. 새 Category 상수를
   추가하고 switch 갈래를 빠뜨리면 컴파일 직후 이 테스트가 깨진다.
2. `SetKeyKind` / `RinexVersion()` 의 `default` → **err** (유보 아님) —
   테스트를 뚫고 런타임에 도달해도 조용히 우회되지 않고 시끄럽게
   실패한다. err 와 유보(ok=false)를 합치지 않는 반환 계약이 전제다.

#### 번복 이력 (재론 방지용 — 이 쟁점은 세 번 결정되었다)

| 차수 | 결정 | 기각 사유 |
|---|---|---|
| 1차 (v1 §5) | 카테고리 **이름 문자열**에서 버전 파생 | 이름 파싱은 규약 밖 이름(`RINEX2X_DAILY` 류)에서 조용히 어긋날 수 있다는 우려. 단, 이 우려는 카테고리가 자유 문자열일 때의 것이었다. |
| 2차 (v2 §5) | config 에 `RinexVersion` **명시 키** + 이름 접두어 대조, 불일치 시 시작 중단 | 코드 실물 확인 결과 Category 는 ParseCategory 가 강제하는 **닫힌 열거형**이라, 1차가 걱정한 "규약 밖 이름" 경로가 구조적으로 부재. 버전 출처가 열거형 하나뿐이므로 명시 키는 **없던 두 번째 출처를 만들어 자기 자신과 대조**하는 순환이며, 대조가 잡는 오류는 키 도입 때문에 생기는 오류뿐이다. 운영자 기입 부담만 남는다. |
| 3차 (최종) | 닫힌 열거형 메서드 + 전수 테스트 + default err | — (현행) |

1차와 3차의 차이에 주의: 3차는 이름 **문자열을 파싱**하지 않는다.
열거형 값 → 버전을 switch 로 대응시키며, 판단 근거가 6개 상수로 닫혀
있다. "이름에서 파생"이 아니라 "열거형에서 대응"이다.

새 버전(예: RINEX5) 추가 절차: domain 열거형 상수 + RinexVersion()
switch 갈래 + SetKeyKind 라우팅 + schema.sql CHECK + config knownKeys.
전수 테스트가 누락 지점을 가리킨다.

---

---

### 6. RINEX 버전 규격과 기관 전송 정책의 분리

| 책임 | 소유 |
|---|---|
| 파일명 해석, `set_key`/`kind` 추출 | Category.RinexVersion() → 파서 |
| 이 배포 환경에서 어떤 kind가 모두 있어야 전송하는가 | `RequiredKinds` → 운영 정책 |

`RINEX2 = 항상 G,L,N,O 필수`처럼 코드에 전역 하드코딩하지 않는다.
기관마다 실제 제공 정책이 다르다 (서울시 G·L·N·O vs 위성센터 O 단독).

---

### 7. OptionalKinds는 만들지 않는다

게이트가 알아야 하는 것은 **반드시 있어야 하는 종류**뿐이다.
`RequiredKinds = G,L,N,O`일 때 S의 존재 여부는 완성 판정에 영향을 주지 않으며,
**완성 세트에서** S는 세트와 함께 정상 전송된다. 미완성 세트에서는 S 도
함께 보류된다 — §7 은 "판정 불참"이지 "미완성 세트에서의 개별 누출 허용"이
아니다 (§19-1, 2026-09-10 확정). `OptionalKinds = S` 같은 설정은 두지 않는다.

---

### 8. RINEX2 세트 파싱

```text
DBON2500.26G.gz → set_key = dbon2500.26, kind = g
```

- 종별 문자 앞까지를 `set_key`로 사용한다.
- 8번째 문자는 세션 코드(0 = 일별, a~x = 시간별)이며 **set_key에 포함**된다.
  포함하지 않으면 시간별 세트가 서로 섞인다.

---

### 9. RINEX3 지원

MVP2 범위에 포함한다. 현재 운영 파일이 충분하지 않아도, 타 기관의 RINEX3 서비스
개시가 약속되어 있으므로 구현에서 제외하지 않는다. 기본은 `RequiredKinds = false`이며,
향후 기관 정책 확정 시 config 수정만으로 대응한다. 코드 수정은 필요하지 않아야 한다.

---

### 10. RINEX3/4 장기 파일명 파싱

```text
DBON00KOR_R_20262500000_01D_30S_MO.crx.gz   ← 레이트(30S) 있음
DBON00KOR_R_20262500000_01D_MN.rnx.gz       ← 없음 (항법 파일은 레이트 필드 생략)
```

- "뒤에서 N개 세그먼트 절단" 방식은 사용하지 않는다. MN에서 깨진다.
- `_` 필드 분해 후 재조립: `set_key = 관측소ID + _R_ + 타임스탬프 + _ + 기간`
  (예: `dbon00kor_r_20262500000_01d`)

---

### 11. `kind`에는 `.rnx` / `.crx` / `.gz`를 넣지 않는다

`kind`는 데이터 종류만 사용한다: `MO`, `MN`, `MS`.
`rnx`/`crx`는 표현 형식이고 `gz`는 외부 압축이다. 파일 형식 검증이 필요해지면
세트 완성도와 별개의 검증 책임으로 다룬다.

```text
..._MO.crx.gz → kind = mo
..._MO.rnx.gz → kind = mo
```

---

### 12. 현재 확인된 RINEX4 운영 사례

서울시 DBON·해양측위정보원 계열 실파일에서 MO.crx.gz / MN.rnx.gz / MS.rnx.gz 확인.
확인된 MS는 MO/MN을 입력으로 생성된 QC Summary 성격의 산출물이므로
필수로 강제하지 않는다. 게이트가 필요하다고 판단될 때의 후보 설정:

```ini
[SET.RINEX4]
RequiredKinds = MO,MN
```

기본값은 여전히 `false`다.

---

### 13. 세트 게이트 위치

```text
Scan → Lookup/대조 → Verify → Upsert(common_ledger)
     → [Set Completeness Gate] → 후보 필터 → 정렬 / MaxFilesPerRun(세트 경계 절단)
     → PENDING 등록 → PUT
```

- Verify를 통과한 파일은 `common_ledger`에 기록한다. 미완성 세트라고 해서
  파일 도착 사실을 Ledger에서 숨기지 않는다.
- 게이트를 통과하지 못한 파일은 PUT 후보가 되지 않는다. `put_ledger` 행도 생기지 않는다.
- 세트 완성/미완성은 영구 상태가 아니라 매 스캔 재계산한다.
- 보류 발생 시 계측 로그를 남긴다:
  `[SET] cat=RINEX2_DAILY held_sets=1 dbon2500.26 missing=g,l,n`

#### 13.1 MaxFilesPerRun은 세트 경계에서 절단한다 (v2 확정)

정렬 후 상한 절단 시 세트를 중간에서 자르지 않는다. 세트가 통째로 들어가지 않으면
그 세트 전체를 다음 회차로 넘긴다. 게이트를 통과한 세트가 회차 경계에서 쪼개지면
부분 실패와 동일한 부분 도착 창이 인위적으로 생기기 때문이다.
게이트 OFF인 카테고리는 기존과 동일하게 파일 단위로 절단한다.

---

### 14. Ledger에 저장할 값

`common_ledger`에 파일명 파생 사실 두 가지를 저장한다.

```text
set_key  (예: dbon2500.26 / dbon00kor_r_20262500000_01d)
kind     (예: o / mo)
```

`COMPLETE` / `INCOMPLETE` / `HELD` 같은 판정 결과는 영구 저장하지 않는다.

> Ledger는 사실을 기록하고, 세트 완성 여부는 현재 정책과 현재 파일 상태로 계산한다.

---

### 15. 일일 리포트 — 구현 제외 (2026-09-15)

아래 내용은 2026-09-10 당시의 설계안이다. 대표 확인 결과 보안 취약점 우려가 있어
미완성 세트 보류 리포트는 MVP2에서 구현하지 않으며, 현재 후속 구현 대상으로도 두지 않는다.

당시 구상은 `common_ledger`의 `set_key`, `kind`, `first_seen`으로 통지를 산출하는 것이었다.

```text
DBON2500.26
보유: O,S / 필수: G,L,N,O / 누락: G,L,N / 최초 도착: 2026-09-08 (2일째)
```

게이트가 활성화된 버전만 대상으로 한다. `RequiredKinds = false`인 버전은 제외한다.

---

### 16. config 예시

> **역사 스냅샷.** 아래 config 주석의 「활성화 전제」는 2026-09-10 기록이다.
> 게이트 활성화 운영 절차는 현재 개발 산출물이 아니다. 이 주석을 근거로
> 운영 절차나 일일 리포트를 구현하지 않는다.

```ini
; =============================================================================
; Set Completeness Gate
;   false    : 세트 검사 OFF. 기존 파일 단위 전송.
;   CSV 목록 : 지정한 모든 kind가 존재할 때만 세트 전송 시작.
;   빈 값과 true는 허용하지 않는다.
;   활성화 전제: resend 배포 완료 + 해당 서버의 미완성 세트 해소 경로 확보 (1.3절)
; =============================================================================

[SET.RINEX2]
RequiredKinds = false

[SET.RINEX3]
RequiredKinds = false

[SET.RINEX4]
RequiredKinds = false
```

---

### 17. 최종 설계 원칙

> **역사 스냅샷.** 아래 원칙 중 3(1.3절 전제)과 11(RINEX3 지원은 MVP2에서 구현)은
> 2026-09-10 문면이다. 현행은 문서 상단 범위가 우선한다. 이미 구현된 RINEX3
> 파싱·세트 게이트 지원 코드는 유지하되, RINEX3 운영 프로파일은 작성하지 않는다.

1. 세트 게이트는 **기본 OFF**다.
2. 희귀 장애 하나 때문에 모든 기관에 원자성을 강제하지 않는다.
3. 세트 정책은 **Opt-in**이며, 활성화는 1.3절의 전제조건을 갖춘 운영 결정이다.
4. `RequiredKinds = false`이면 MVP1 동작을 그대로 유지한다.
5. CSV 목록이면 해당 종류가 모두 존재할 때만 전송을 시작한다.
6. `OptionalKinds`는 만들지 않는다.
7. 세트 정책은 `[SET.RINEXx]`에 버전별 한 번만. Daily/Hourly 중복 설정 없음.
8. 버전의 출처는 닫힌 열거형 `Category.RinexVersion()` 하나다. config 에 버전 키를 두지 않으며, 전수 테스트와 default err 가 대조 검증을 대체한다. (§5 3차 확정 — 2차의 "명시 키 + 대조" 를 재번복)
9. RINEX 버전은 파싱 규칙을, `RequiredKinds`는 운영 정책을 결정한다.
10. `kind`는 데이터 종류만(`mo`, `mn`), 표현 형식(`.rnx`/`.crx`)과 압축(`.gz`)은 제외.
11. RINEX3 지원은 MVP2에서 구현한다.
12. 전송 완료 원자성 / 원격 롤백은 구현하지 않는다.
13. 세트 완성 여부는 영구 저장하지 않고 매 스캔 재계산한다.
14. `Recover`(크래시 회수) / `resend`(기간 재전송) / `download`(수신)의 책임을 분리한다.
15. `resend`는 세트 게이트를 우회한다. 운영자의 명시적 지시가 게이트에 우선한다.
16. `MaxFilesPerRun` 절단은 세트 경계에서 수행한다.

---

### 18. 잔여 항목 (2026-09-15 갱신)

- ~~v5 Ledger 마이그레이션 방법~~ → **해결**: Open 시 자동 단일 스텝
  (v4→v5, 트랜잭션 ALTER+백필+버전 갱신). §19-3.
- **MVP2 남은 목표 1:** `resend` 인자 설계·구현 및 세트 게이트 우회.
- **MVP2 남은 목표 2:** 서울시 긴급 대응으로 추가한 `HourLayout` 제거와
  설정 루트 아래 범위 제한 재귀 탐색 기반 경로 범용화. 세부 정책은 Linux
  OS·실경로 확인 후 확정하며, 원격은 RINEX 버전별 고정 평면 경로를 유지한다.
- **MVP2 남은 목표 3:** Linux 배포 준비와 테스트. Linux 전용 추가 보안은 범위에서 제외하며,
  공식 보안점검에서 구체적인 요구가 나온 경우에만 별도 설계·구현한다.
- **MVP2 남은 목표 4:** Ledger Retention Cleanup 구현 및 30일 경계·FK cascade·
  오래된 파일 재전송 방지 테스트. 현재는 설정과 불변식 검증만 있고 실제 삭제는 없다.
- **MVP3으로 연기:** DOWNLOAD 및 `download_ledger`. BOTH의 MVP 번호는 미정이다.
- **구현 제외:** 미완성 세트 보류 리포트(보안 취약점 우려), 게이트 활성화 운영 절차,
  RINEX3 첫 운영 프로파일.
- `MaxHoldDays`는 기존 조건부 미구현 결정을 유지하며 MVP2 범위에 넣지 않는다.

---

### 19. 구현 확정 사항 (2026-09-10 — 구현 중 결정, 소스 반영 완료)

#### 19-1. 게이트 의미론 2건

1. **미완성 세트는 선택 종 포함 전체 보류.** §2 가 §7 문면에 우선한다.
   미완성 세트에서 S 만 새면 목적지에 부분 세트가 생긴다 — 게이트가
   막으려는 바로 그 상태다. (기각: 선택 종 개별 통과안)
2. **세트 소속 유보(파싱 불가)는 개별 통과 + 관측**(`SetUnparsed`
   카운터, `[SET][WARN]` 예시 로그). YONS060.20M 같은 표준 밖
   실데이터가 실재하므로 보류안(게이트 ON 시 영구 미전송)을 기각.
   MatchesName 의 유보 철학과 동일 계열. 누출 방지는 domain 파서의
   필드 형태 검사(관측소 9자·소스 1자·타임스탬프 11자리·주기 형태)가
   담당 — 필드 "수"만 검사하는 안은 유령 세트를 만들어 기각.

#### 19-2. 완성도의 우주 = 관측(observed), 후보 아님

Ingress 를 통과한 실재 파일 전체가 판정 재료다. VERIFIED 로 후보에서
빠진 멤버, DOWNLOAD origin 제외분(변경 시 재검증 통과분만)도 존재로
센다. 거부 파일(0바이트·Grace 미경과)은 세지 않는다. FAILED 재시도
대기열에도 동일 필터를 적용한다. 절단은 집합 방식(setID =
category+SetKey — RINEX3/4 동일 파일명 대응)이며, 빠진 만큼 잘린
쪽에서 채워 넣지 않는다.

#### 19-3. Ledger 구현 확정

- `set_key`/`kind` 는 **NOT NULL DEFAULT ''** + lower CHECK.
  nullable(NULL=유보) 안은 2회 기각 — 전 컬럼 NOT NULL 규약,
  '' 는 파서의 유보 반환값과 동일 표기, CREATE/ALTER 동형 유지.
  '' 의 의미("세트 소속 미확정")는 "전 행이 파서를 통과한다"는
  불변식(마이그레이션 백필 + Upsert 파생)이 유일하게 고정한다.
- 파생의 주인: `UpsertCommon` **내부**에서 `domain.SetKeyKind` 호출
  (CommonInput 필드 안 기각 — 호출자가 잊을 수 없는 구조).
- 마이그레이션: Open 자동, v4→v5 단일 스텝, 단일 트랜잭션(tx 전용,
  커서 닫은 뒤 UPDATE), 완료 로그 `[LEDGER] ... rows/backfilled/
  unresolved`. migrate 서브커맨드 안 기각 — 1인 현장 이식 배포에서
  수동 단계는 "빠뜨리면 조용히 멎는 단계"다. identity_rule 검사가
  마이그레이션에 선행한다.
- 게이트 켜기 전 kind 실측: 백필된 원장의 GROUP BY 조회로 해당
  서버의 실제 종 분포를 확인하고 그대로 RequiredKinds 를 적는다.

#### 19-4. config 구현 확정

- RequiredKinds 검증: 빈 값·true·빈 항목·**중복**·**영문 외 문자**
  전부 시작 오류. 영문 제한 근거 — kind 산출처는 domain 파서 둘뿐이고
  둘 다 영문만 내놓으므로, 그 밖의 kind 는 영원히 완성되지 않는
  게이트가 된다(침묵 대신 시작 오류).
- 섹션 부재 = OFF(실행파일 교체만으로 배포). **섹션이 존재하면
  RequiredKinds 키는 필수** — 절반짜리 설정의 조용한 OFF 방지.
- SET 섹션 목록·knownKeys 는 `domain.Categories()` 에서 파생
  (RINEX5 확장 지점이 domain 한 곳).
