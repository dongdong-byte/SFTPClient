# SFTPClient — 경로 토큰(PATH v4) 커밋 계획

> 작성 2026-09-25. 이 대화에 참여하지 않은 사람·AI가 읽고 이어서 작업할 수 있도록 쓴 문서다.
> 관련 문서: `docs/download/SFTPClient_DOWNLOAD_PATH_DECISIONS_2026-09-23.md`(결정 문서),
> `docs/download/SFTPClient_DOWNLOAD_DESIGN_v3.md`(DOWNLOAD 구현 기준), `docs/path/SFTPClient_PATH_DESIGN_v3.md`(재귀 스캔).

---

## 0. 한 줄 요약

**경로 템플릿의 어느 위치에든 `(SITE)`·`(HH)` 토큰을 둘 수 있게 한다.
원본(스캔 경로)에서는 그 위치의 폴더를 나열해 찾고, 목적지에서는 파일명에서 뽑은 값으로 채운다.**

---

## 1. 배경

- 대표 지시: "site뿐 아니라 다른 인자도 경로 어디에 있든 유연하게 받아들여야 한다. 경로가 어떻게 될지 모르니까."
- **대상 기관(특히 Linux 기관)의 실제 경로는 모른다는 전제**로 설계한다. 관측소 폴더가 날짜 위에 있을 수도, 아래에 있을 수도, 폴더 이름 중간에 있을 수도 있다.
- 주 목적은 **원본(스캔 경로)** 에서 토큰을 받는 것이다. 목적지(DOWNLOAD 저장 위치)는 부수 목적이다.

```
LocalPath = /data/(SITE)/(YYYY)/(DOY)/            관측소가 날짜 위
LocalPath = /data/(YYYY)/(DOY)/(SITE)/            관측소가 날짜 아래
LocalPath = /data/rnx_(SITE)/(YYYY)/(MM)(DD)/     폴더 이름 중간
LocalPath = C:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\      서울시 옛 설정
```

---

## 2. 원본과 목적지의 차이

| | 원본 (스캔 경로) | 목적지 (저장 경로) |
|---|---|---|
| 해당 설정 | PUT `LocalPath`, DOWNLOAD `RemotePath` | DOWNLOAD `LocalPath` (PUT `RemotePath`는 후속) |
| 토큰 처리 | **찾기**: 그 위치의 부모 폴더를 나열해서 패턴에 맞는 폴더만 따라 내려간다 | **채우기**: 파일명에서 뽑은 값(`DBON`, `13`)을 넣는다 |
| 담당 | pathpl(패턴 제공) + scan(나열) | pathpl `ExpandFile` |
| 로컬·원격 | scan은 `DirLister`로 로컬과 SFTP를 같은 코드로 다룬다. 한 번 만들면 PUT·DOWNLOAD 원본에 모두 적용된다 | — |

**주의:** 원본을 `Template.Expand(when)`로 만들면 안 된다. `Expand`는 파일 토큰을 채울 수 없어서
`/data/(SITE)/2026/250/` 같은 존재하지 않는 경로가 되고, scan은 없는 폴더를 정상적인 빈 폴더로 취급하므로
**오류 없이 파일 0개로 끝난다.**

---

## 3. 확정한 결정 (2026-09-25)

| # | 결정 | 이유 |
|---|---|---|
| D1 | **서울시의 옛 `(HH)` 설정을 다시 받는다.** PATH v3의 "옛 (HH) 설정 시작 거부" 결정을 번복한다 | 옛 `(HH)`는 스캔 날짜로 시각을 **계산**해 자정 폴더만 봤다(그래서 막았다). 새 `(HH)`는 00~23 폴더를 **나열**하므로 **DOY 폴더 아래가 00~23 폴더뿐이면** 결과가 재귀 수집과 같다. DOY 바로 아래 평면 파일과 00~23 이외 폴더(`backup` 등) 안의 파일은 모으지 않는다. 서울시 재배포 때 설정을 바꾸지 않아도 된다 (서울시 DOY 폴더 구조 확인 권장) |
| D2 | **폴더의 관측소와 파일명의 관측소를 대조하지 않는다.** 폴더는 길 찾기용이고, 파일의 정체는 파일명이 정한다 | 폴더 `DBON` 안에 `SUW1` 파일이 있는 것은 수신기 설정 오류이며 이 프로그램이 상정하는 경우가 아니다. PUT 장부도 파일명 기준이라 처리에 문제가 없다 |
| D3 | **패턴에 맞지 않는 폴더(`backup` 등)는 건너뛰고 출력하지 않는다.** 오류로 보지 않는다. 단 **맞는 폴더가 하나도 없으면** (`Result.Unmatched > 0 && PatternRoots == 0`) PUT 러너가 `[SCAN][WARN]`으로 올린다 | 일부만 맞으면 정상 운영의 잡음이다. 전부 안 맞으면 설정 오류라 파일 0개로 조용히 끝나는 것을 막아야 한다 |

### 3.1 바뀌지 않는 불변식

| # | 불변식 |
|---|---|
| I1 | **PUT 장부는 경로가 아니라 `(category, 정규화 파일명)`으로 파일을 식별한다** (schema v5). 원본 경로 모양이 바뀌어도 이미 보낸 파일은 재전송되지 않는다 |
| I2 | 토큰이 없는 기존 설정은 동작이 전혀 바뀌지 않는다 |
| I3 | 모든 커밋에서 `go test ./...` 전체 통과 |
| I4 | 파일 토큰의 값은 domain(`SiteFromName`, `HourFromName`)만 파일명에서 뽑는다. pathpl은 파일명을 읽지 않는다 |
| I5 | 어느 경로 역할에 어느 토큰을 허용할지는 config 정책이 정한다. pathpl은 문법과 치환만 안다 |

---

## 4. 커밋 현황과 계획

### 4.1 완료

| 커밋 | 해시 | 내용 |
|---|---|---|
| 1 | `a35d068` | **domain `HourFromName`**: 파일명에서 관측 시작 시각(UTC, `00`~`23`) 추출. RINEX2 세션 `a`~`x`, RINEX3/4 `YYYYDDDHHMM`의 `HH`. DOY·시각 범위와 윤년 검사. 측위원 실제 파일 6개로 확인 |
| 2 | `0e09d68` | **config 보호막**: PUT `LocalPath`·`RemotePath`의 `(SITE)`·`(HH)`를 시작 단계에서 명시적으로 거부 (`putPathForbiddenTokens`). pathpl이 토큰을 알게 되기 **전에** 거부 정책을 먼저 깔아, 어느 커밋에서도 옛 `(HH)`가 뚫리는 순간이 없게 했다. 오류 문구는 PUT 현장 운영자용 조치 안내 |

### 4.2 커밋 3 — pathpl 파일 토큰 + 목적지 채우기 (작성 완료, 커밋 전)

| 항목 | 내용 |
|---|---|
| 파일 | `internal/pathpl/pathpl.go`, `pathpl_test.go` |
| 내용 | `TokenSITE`·`TokenHH` 추가, `FileFields`, `ExpandFile(when, fields)` (값 누락·구분자 포함 시 오류, 부분 경로 반환 안 함), `FileTokens()` |
| `Expand(when)` | 파일 토큰을 원문 `(SITE)` 그대로 남긴다 |
| **커밋 전 수정** | `Expand` 주석의 "원문을 남기면 보이는 실패가 된다"는 **목적지에서만** 맞다. "원본에 쓰면 빈 폴더로 조용히 끝나므로 원본에는 쓰면 안 된다(§2 주의)"로 고친다 |
| 선택 | `ExpandFile`에서 값이 `.`·`..`이면 `ErrBadFileField` (domain 값에서는 나올 수 없는 계약 위반 방어) |
| 운영 영향 | 없음. 커밋 2가 PUT 경로의 토큰을 계속 막는다 |

### 4.3 커밋 4 — pathpl: 템플릿을 폴더 단위 "단계"로 분해

| 항목 | 내용 |
|---|---|
| 파일 | `internal/pathpl/` |
| 내용 | 템플릿을 경로 구분자(`/`, `\`) 기준 폴더 단위로 나눈다. 각 단은 둘 중 하나다 |
| | **고정 단**: 글자와 날짜 토큰만 있다. 스캔 날짜로 채워 문자열 하나가 된다 |
| | **패턴 단**: 파일 토큰이 하나 이상 있다. 폴더 이름 하나를 받아 맞는지와 뽑힌 값(`FileFields`)을 돌려주는 matcher가 된다 |
| 패턴 규칙 | `(SITE)` = 영숫자 4자리, **대소문자 무시**, 뽑힌 값은 대문자로 정규화 / `(HH)` = 숫자 2자리 `00`~`23` / 같은 단의 글자·날짜 토큰은 정확히 일치 (예: `rnx_(SITE)` → `rnx_` + 4자리) |
| 경계 | 구분자와 드라이브(`C:\`)·루트(`/`)는 원문 그대로 보존. 연속 토큰(`(SITE)(HH)`)은 고정 폭이라 모호하지 않다 |
| 테스트 | §1의 네 가지 모양, 대소문자, 윤곽 불일치(`backup`), 날짜 토큰과 섞인 패턴 단 |
| 운영 영향 | 없음 (순수 함수, 아직 부르는 곳 없음) |

API 이름·모양은 구현자가 정한다. 예: `(t *Template) Steps(when time.Time) []Step`, `Step.Fixed string`, `Step.Match(name) (FileFields, bool)`.

### 4.4 커밋 5 — scan: 패턴 단에서 폴더 나열

| 항목 | 내용 |
|---|---|
| 파일 | `internal/scan/` |
| 내용 | 날짜마다 `tpl.Expand(day)`로 루트 하나를 만들던 곳을, 커밋 4의 단계를 따라 **루트 여러 개**를 만드는 방식으로 바꾼다 |
| | 고정 단: 경로에 붙인다 |
| | 패턴 단: 부모 폴더를 `DirLister.List`로 나열하고, **진짜 폴더**(PATH v3 §5.1 규칙 — 링크·junction 제외)이면서 matcher에 맞는 것만 따라 내려간다 |
| | 마지막 단까지 온 각 루트 아래는 **지금의 재귀 수집 그대로** |
| 없는 부모 폴더 | 지금처럼 정상적인 빈 칸 (`fs.ErrNotExist`) |
| 패턴 불일치 폴더 | 건너뛰고 debug 로그 (D3) |
| 파일 토큰 없는 템플릿 | 단계가 전부 고정 단이므로 루트 하나 = 지금과 **완전히 같은 경로** (I2). 기존 scan 테스트가 그대로 통과해야 한다 |
| 공통 | `DirLister`만 쓰므로 로컬(PUT)과 SFTP(DOWNLOAD)에 모두 적용된다 |
| 테스트 | fake DirLister로 §1의 네 모양. 서울시형 `(DOY)\(HH)\`가 `(DOY)\`+재귀와 **같은 파일 집합**을 모으는지 (D1 근거) |
| 운영 영향 | 없음. 커밋 2가 PUT 원본의 토큰을 아직 막고 있으므로 새 갈래에 도달하는 설정이 없다 |

### 4.5 커밋 6 — config: PUT 원본의 파일 토큰 허용

| 항목 | 내용 |
|---|---|
| 파일 | `internal/config/load.go`, `load_test.go`, `config.example.ini` |
| 허용 | PUT `LocalPath`의 `(SITE)`·`(HH)` |
| 계속 금지 | PUT `RemotePath`(보내는 목적지). put 전송이 파일별 원격 경로를 계산하게 될 때까지 (`put/transfer.go`는 지금 `RemotePath.Expand(When)`로 배치당 한 경로만 만든다) |
| 정책 정리 | `putPathForbiddenTokens`를 역할별로 나눈다 (PUT 원본 허용 / PUT 목적지 금지). `downloadSourceForbiddenTokens`는 원본 허용으로 바뀌므로 정리한다 |
| D1 반영 | `TestMapConfig_RemovedHourTokenIsRejected`를 "**서울시형 옛 설정이 로드되고 모든 시각 폴더를 수집한다**"로 바꾼다. `forbiddenTokenHints`의 `HH` 문구는 PUT 목적지용으로 바꾼다 |
| 운영 영향 | **여기서 처음 동작이 바뀐다.** 서울시 옛 설정이 거부 대신 로드된다 (결과는 재귀와 같음). 토큰 없는 설정은 영향 없음 |

### 4.6 문서 커밋

- `SFTPClient_PATH_DESIGN_v3.md`: D1(옛 `(HH)` 거부 번복) 기록
- `SFTPClient_DOWNLOAD_DESIGN_v3.md`: "원본 RemotePath는 날짜 토큰만"(§5.2-2, §7.1-2)과 "PATH v4 후속" 표기를 이 문서 기준으로 정정
- 서울시 재배포 절차: "`(HH)\` 제거" 단계가 선택으로 바뀜

### 4.7 그다음: DOWNLOAD 본체 (DOWNLOAD 설계 v3 §13)

config `[DOWNLOAD]` → transport·scan (SFTP 나열·`DownloadPart`) → download 러너 → main 배선 → DOWNLOAD 로그 → 문서.
원본 토큰 지원이 끝나 있으므로 DOWNLOAD `RemotePath`에도 토큰을 쓸 수 있다.

---

## 5. 운영 위험과 대응

| # | 위험 | 대응 |
|---|---|---|
| R1 | 원본에 `Expand`를 쓰면 파일 0개로 조용히 끝남 | 원본은 반드시 커밋 5의 나열 경로를 탄다. 커밋 3 주석에 명시 |
| R2 | 관측소 폴더가 소문자(`dbon`)인 현장 | `(SITE)` 자리는 대소문자 무시, 값은 대문자로 정규화 (커밋 4). 단 **패턴 단의 글자 부분(`rnx_` 등)은 대소문자를 구분한다** — Linux 에서 `rnx_`·`RNX_`가 공존할 때 이중 스캔을 막기 위해서다. 설정에는 실제 폴더 표기 그대로 적는다 |
| R3 | 관측소 폴더가 수백 개인 원격 서버에서 매 날짜 나열 | 패턴 단 나열은 날짜마다 부모 한 번. 필요하면 회차 안에서 캐시 (측정 후 판단) |
| R4 | 링크·junction으로 된 관측소 폴더 | PATH v3 §5.1 규칙대로 진짜 폴더만 하강. 링크는 건너뛰고 집계 |
| R6 | 같은 이름 파일이 두 관측소 폴더에 있음 (수신기 설정 오류) | 기존 put 중복 가드가 먼저 본 하나만 후보로 둔다. revision 루프 없음 (`TestRunner_SameNameAcrossSiteFoldersDoesNotLoopRevision`) |
| R5 | 서울시 재배포 후 옛 `(HH)` 설정 | 커밋 6 이후 로드되고 전 시각 폴더를 수집 (D1). 커밋 6 이전 버전이면 기존처럼 거부 |
