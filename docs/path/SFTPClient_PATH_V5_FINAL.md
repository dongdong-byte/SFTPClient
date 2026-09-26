# SFTPClient — 경로 토큰(PATH v4) 최종 정리 — 2026-09-26 상태 갱신판

> `SFTPClient_PATH_V4_FINAL.md`(보존)와 내용이 같고 §8 남은 일의 진행 상태만
> 갱신했다. 파일 이름의 V5 는 문서 판 번호이며 PATH 설계 v5 가 아니다.

> 작성 2026-09-25. 2026-09-23 ~ 09-25 작업의 최종 결과 기록이다.
> 계획 문서: `SFTPClient_PATH_V4_COMMIT_PLAN.md` / DOWNLOAD 구현 기준: `docs/download/SFTPClient_DOWNLOAD_DESIGN_v3.md`

---

## 0. 한 줄 요약

**경로 템플릿의 어느 위치에든 `(SITE)`·`(HH)`를 둘 수 있게 되었다.
원본(PUT `LocalPath`)에서는 그 자리의 폴더를 나열해 찾고, 목적지(DOWNLOAD 저장 위치)에서는 파일명에서 뽑은 값으로 채운다.
운영 중인 PUT의 전송 이력은 경로 모양이 바뀌어도 재전송되지 않는다.**

---

## 1. 무엇을 왜 했나

| 항목 | 내용 |
|---|---|
| 지시 | "site뿐 아니라 다른 인자도 경로 어디에 있든 유연하게 받아들여야 한다. 경로가 어떻게 될지 모르니까." (대표) |
| 전제 | 대상 기관(특히 Linux 기관)의 실제 경로는 모른다. 관측소 폴더가 날짜 위·아래·폴더 이름 중간 어디든 있을 수 있다 |
| 주 목적 | **원본(스캔 경로)** 에서 토큰을 받는 것. 목적지(DOWNLOAD 저장)는 부수 목적 |
| 원칙 | 운영 중인 PUT(3기관 + Linux)을 흔들지 않는다. 동작이 바뀌는 커밋은 하나(커밋 6)로 모은다 |

---

## 2. 핵심 개념

### 2.1 원본은 "찾기", 목적지는 "채우기"

| | 원본 (PUT `LocalPath`) | 목적지 (DOWNLOAD `LocalPath`) |
|---|---|---|
| 토큰 처리 | 그 자리의 **부모 폴더를 나열**해 패턴에 맞는 폴더만 따라 내려간다 | 파일명에서 뽑은 값(`DBON`, `13`)을 **넣는다** |
| 코드 | pathpl `Steps`·`Match` + scan `resolveRoots` | pathpl `ExpandFile` |
| 로컬·원격 | scan은 `DirLister`로 로컬과 SFTP를 같은 코드로 다룬다. DOWNLOAD 원격 원본에도 그대로 적용된다 | — |

원본을 `Template.Expand(when)`로 만들면 `/data/(SITE)/2026/250/` 같은 없는 경로가 되고,
scan은 없는 폴더를 정상적인 빈 칸으로 취급하므로 **오류 없이 파일 0개**로 끝난다. 원본은 반드시 나열 경로를 탄다.

### 2.2 토큰

| 토큰 | 값 | 원본에서의 패턴 | 값을 뽑는 곳 |
|---|---|---|---|
| `(SITE)` | 관측소 코드 4자리 (`DBON`) | 영숫자 4자리, **대소문자 무시**, 값은 대문자로 정규화 | `domain.SiteFromName` |
| `(HH)` | 관측 시작 시각 (`00`~`23`, UTC) | 숫자 2자리 `00`~`23` | `domain.HourFromName` |

날짜 토큰 `(YYYY)` `(YY)` `(DOY)` `(MM)` `(DD)`는 지금처럼 스캔 날짜로 채운다.

### 2.3 경로 역할별 허용

| 설정 | 역할 | 파일 토큰 |
|---|---|---|
| `[PUT.*] LocalPath` | 원본 | **허용** (커밋 6) |
| `[PUT.*] RemotePath` | 보내는 목적지 | **금지.** put 전송이 배치당 경로 하나(`RemotePath.Expand(When)`)를 만들므로 파일별 값이 들어갈 자리가 없다 |
| `[DOWNLOAD.*] RemotePath` | 원본 | **허용 (구현 완료).** 원격 폴더를 패턴 나열한다 |
| `[DOWNLOAD.*] LocalPath` | 받는 목적지 | **허용 (구현 완료).** `ExpandFile`로 파일별 값을 채운다 |
| Daily 카테고리 `LocalPath`의 `(HH)` | — | **거부** (Daily에는 시각 폴더가 없다) |

---

## 3. 확정한 결정

| # | 결정 | 이유 |
|---|---|---|
| D1 | **서울시 옛 `(HH)` 설정을 다시 받는다.** PATH v3의 "옛 `(HH)` 시작 거부"를 번복 | 옛 `(HH)`는 스캔 날짜로 시각을 **계산**해 자정 폴더만 봤다. 새 `(HH)`는 00~23 폴더를 **나열**한다. DOY 아래가 00~23 폴더뿐이면 재귀 수집과 같은 파일을 모은다 |
| D2 | **폴더의 관측소와 파일명의 관측소를 대조하지 않는다** | 둘이 다르면 수신기 설정 오류다. 폴더는 길 찾기용이고 파일의 정체는 파일명이 정한다 |
| D3 | **패턴에 맞지 않는 폴더(`backup` 등)는 건너뛰고 출력하지 않는다. 단 맞는 폴더가 하나도 없으면 `[SCAN][WARN]`** | 일부만 맞으면 정상 운영의 잡음이다. 전부 안 맞으면 설정 오류이며 조용히 파일 0개로 끝나는 것을 막아야 한다 |

### 3.1 지켜진 불변식

| # | 불변식 | 증명 |
|---|---|---|
| I1 | PUT 장부는 경로가 아니라 `(category, 정규화 파일명)`으로 식별한다. **경로 모양이 바뀌어도 이미 보낸 파일은 재전송되지 않는다** | `TestRunner_SwitchingHourTokenDoesNotResend`, `TestRunner_SwitchingToSiteTokenDoesNotResend` |
| I2 | 토큰이 없는 기존 설정은 동작·나열 횟수·로그가 바뀌지 않는다 | `TestStepsWithoutFileTokensEqualsExpand`, `TestScannerScan_TokenFreeTemplateUnchanged`, `TestRunner_TokenFreeConfigHasNoPatternLog`, 기존 scan 테스트 무수정 통과 |
| I3 | 모든 커밋에서 `go test ./...` 전체 통과 (13개 패키지) | 커밋마다 확인 |
| I4 | 파일 토큰 값은 domain만 파일명에서 뽑는다. pathpl은 파일명을 읽지 않는다 | 패키지 구조 |
| I5 | 경로 역할별 허용은 config 정책이 정한다 | `putRemoteForbiddenTokens`, Validate |

---

## 4. 커밋 기록

| 커밋 | 해시 | 내용 | 운영 영향 |
|---|---|---|---|
| 1 | `a35d068` | **domain `HourFromName`**: 파일명에서 관측 시작 시각 추출. RINEX2 세션 `a`~`x` → `00`~`23`, RINEX3/4 `YYYYDDDHHMM`의 `HH`. 관측소 코드·DOY·시각 범위·윤년 검사. 측위원 실제 파일 6개로 확인 | 없음 |
| 2 | `0e09d68` | **config 보호막**: PUT 경로의 `(SITE)`·`(HH)`를 시작 단계에서 명시 거부. pathpl이 토큰을 알게 되기 **전에** 거부를 먼저 깔아, 어느 커밋에서도 옛 `(HH)`가 뚫리는 순간이 없게 했다. 오류 문구는 PUT 현장 운영자용 조치 안내 | 없음 (거부 문구만 변경) |
| 3 | `90325f1` | **pathpl 파일 토큰 + `ExpandFile`**: `(SITE)`·`(HH)` 인식, 목적지 채우기. 값 누락·형식 위반(`.`·`..`·구분자 포함) 시 오류, 부분 경로 반환 안 함. `FileTokens()` | 없음 |
| 4 | `7794dde` | **pathpl `Steps`·`Match`·`Merge`**: 템플릿을 폴더 단위 단계(고정 단/패턴 단)로 분해. 구분자·드라이브·UNC를 원문 보존. 고정 폭 대조라 `(SITE)(HH)` 붙여 쓰기도 모호하지 않음 | 없음 |
| 5 | `0184c81` | **scan 패턴 단 나열**: 부모를 나열해 맞는 진짜 폴더만 따라 내려가 루트 여러 개 생성. 회차 안 부모 목록 캐시, `Unmatched`·`PatternRoots` 집계. PUT 러너의 "맞는 폴더 0개" 경고 | 없음 (커밋 2가 PUT 원본 토큰을 아직 막음) |
| 6 | `3b9ed01` | **config: PUT `LocalPath` 토큰 허용**. `RemotePath`는 계속 금지. Daily의 `(HH)` 거부. `config.example.ini` 안내 | **있음 — 처음이자 유일한 동작 변화.** 서울시 옛 `(HH)` 설정이 거부 대신 로드된다 |

---

## 5. 운영에서 달라지는 것

### 5.1 설정 예시

```ini
[PUT.RINEX3_HOURLY]
LocalPath = D:\RNX\(SITE)\(YYYY)\(DOY)\          ; 관측소가 날짜 위
LocalPath = D:\RNX\(YYYY)\(DOY)\(SITE)\          ; 관측소가 날짜 아래
LocalPath = D:\rnx_(SITE)\(YYYY)\(MM)(DD)\       ; 폴더 이름 중간
LocalPath = C:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\    ; 서울시형 시각 폴더
RemotePath = /RNX3/(YYYY)/(DOY)/                 ; 파일 토큰 금지
```

### 5.2 현장별 영향

| 현장 | 영향 |
|---|---|
| 토큰 없는 기존 설정 (대부분) | **없음.** 나열·로그·전송 동일 |
| 서울시 (옛 `(HH)` 설정 잔존) | 전에는 시작 거부 → 이제 로드되어 00~23 폴더를 수집. 재배포 절차의 "`(HH)\` 제거"가 **필수에서 선택**으로 바뀜. 설정을 바꿔도 재전송 없음 |
| 관측소 폴더가 있는 새 기관 | `LocalPath`에 `(SITE)`를 원하는 위치에 적으면 된다 |

### 5.3 새 로그

| 로그 | 뜻 | 조치 |
|---|---|---|
| `[SCAN][WARN] category=... no folder matched the LocalPath token pattern; unmatched=N ...` + 경로 목록 | 부모 폴더는 있는데 패턴에 맞는 폴더가 하나도 없다 | 토큰 위치, 관측소 폴더 이름(4자리인지), 글자 부분의 대소문자 확인 |
| 시작 거부 `token (SITE) is not supported in PUT RemotePath: ...` | 보내는 목적지에 파일 토큰 | RemotePath에서 토큰 제거 |
| 시작 거부 `LocalPath uses (HH) but the category is daily` | Daily 섹션에 시각 토큰 | 섹션 선택 확인 |

### 5.4 알아 둘 동작

- `(SITE)` 자리는 대소문자를 가리지 않지만(`dbon`=`DBON`), **패턴 단의 글자 부분**(`rnx_` 등)은 구분한다. Linux에서 `rnx_`·`RNX_`가 공존할 때 이중 스캔을 막기 위해서다. 설정에는 실제 폴더 표기 그대로 적는다.
- `(HH)` 패턴은 DOY 바로 아래의 평면 파일과 00~23 이외 폴더 안의 파일은 모으지 않는다. 재귀 수집과 다른 유일한 지점이다.
- 링크·junction으로 된 관측소 폴더는 따라가지 않고 `irregular`로 집계한다.
- 같은 이름 파일이 두 관측소 폴더에 있으면(수신기 설정 오류) 먼저 본 하나만 후보가 된다. revision 루프는 생기지 않는다.
- 부모 목록 캐시는 한 번의 스캔 안에서만 산다. 새 관측소 폴더는 다음 회차에 바로 보인다.

---

## 6. 운영 치명 케이스 테스트

| 영역 | 테스트 | 막는 사고 |
|---|---|---|
| put | `SwitchingHourTokenDoesNotResend` | 서울시가 설정을 바꿔 재배포할 때 **전량 재전송** |
| put | `SwitchingToSiteTokenDoesNotResend` | 관측소 폴더로 재배치할 때 전량 재전송 |
| put | `SameNameAcrossSiteFoldersDoesNotLoopRevision` | 두 관측소 폴더의 같은 이름 파일이 번갈아 장부를 갱신하는 **무한 재전송** |
| put | `WarnsWhenNoFolderMatchesPattern` / `NoWarnWhenSomeFolderMatches` | 설정 오류의 침묵 / 거짓 경고 |
| put | `TokenFreeConfigHasNoPatternLog` | 기존 현장 로그 변화 |
| scan | `TokenFreeTemplateUnchanged` | 기존 설정에 새 코드가 끼어듦 |
| scan | `CachedParentMatchesPerDay` | 캐시 재사용으로 둘째 날 파일이 **통째로 사라짐** |
| scan | `RegularFileNamedLikeSiteIsNotRoot` | 일반 파일로 내려가려다 매 회차 오류 누적 |
| scan | `NoFolderMatchedIsDetectable` / `MatchedButEmptyIsNotWarning` | 경고 조건의 정확성 |
| scan | `CacheDoesNotOutliveScan` | 새 관측소가 프로세스 재시작 전까지 전송 안 됨 |
| scan | `WindowsSiteAboveDate` | Windows 구분자 표기 변화 |
| scan | `HourPatternEqualsRecursion` | D1의 근거와 유일한 차이(00~23 이외 폴더) 고정 |
| config | `SeoulHourTokenIsAcceptedInLocalPath`, `PutRemotePathFileTokensRejected`, `DailyLocalPathWithHourToken` | 역할별 허용·거부 정책 |
| domain | `HourFromName_*` (세션 24개 전수, 윤년, 범위 밖 값) | 잘못된 시각 폴더 생성 |

---

## 7. 과정에서 바로잡은 것

| 시점 | 내용 |
|---|---|
| DOWNLOAD 설계 v1 → v3 | common을 판단자로 둔 설계에서, download·put이 서로 모르고 coordinator는 순서만 잇는 단순한 구조로 정리. PUT DB는 건드리지 않기로 확정 |
| 커밋 3 검토 | 경로 범용화의 주 목적이 목적지가 아니라 **원본**이라는 점이 드러나 커밋 4~6(원본 나열)을 계획에 추가 |
| 커밋 4 검토 | D1의 "재귀와 같다"에 조건(DOY 아래가 00~23 폴더뿐)이 있음을 확인하고 문서·테스트에 명시 |
| 커밋 5 검토 | "맞는 폴더 0개면 경고"가 집계만 되고 출력되지 않던 것을 `PatternRoots` + 러너 경고로 보완 |
| 커밋 6 검토 | 재전송 테스트가 처음 실패 → 원인은 테스트 준비(`SeedVerified`가 기존 PENDING 행을 건드리지 않음). 실제 전이(`BeginPut`→`FinishPut`)로 고쳐 통과 |

---

## 8. 남은 일

| 순서 | 작업 |
|---|---|
| 1 | **문서 커밋**: PATH v3 문서에 D1(옛 `(HH)` 거부 번복) 기록, 서울시 재배포 절차 갱신, DOWNLOAD v3의 "원본 RemotePath는 날짜 토큰만"·"PATH v4 후속" 표기 정정, 서울시 DOY 폴더 구조 확인 — **〔2026-09-26 DOWNLOAD 커밋 8 에서 문서 정정 완료. 서울시 DOY 폴더 구조 확인만 미완(현장 확인 필요)〕** |
| 2 | **DOWNLOAD 본체** (DOWNLOAD v3 §13): config `[DOWNLOAD]` → transport·scan (SFTP 나열·`DownloadPart`) → download 러너 → main 배선 → DOWNLOAD 로그. `[DOWNLOAD.*] RemotePath`를 읽을 때 원본 토큰이 scan 나열 경로를 타는지 테스트 — **〔DOWNLOAD 커밋 1~7 로 구현 완료. 실서버 검증(D14)은 미완〕** |
| 3 | 후속: PUT `RemotePath`의 파일 토큰 (put 전송이 파일별 원격 경로를 계산하게 바꾼 뒤) |
| 4 | 측위원 배포 설정은 처음부터 `RINEX4_*`로 적는다 (RINEX3/4 파일명이 같아, 나중에 섹션 이름을 바꾸면 카테고리 키가 달라져 전량 재전송) |
