# SFTPClient MVP2 — `resend` 설계 v4 (설계 마감본)

> 작성 2026-09-21. MVP2 실행 범위 ② 「`resend` 명령 신설」의 구현 전 설계다.
> 상위 기준은 [PROJECT_GUIDELINES](../../SFTPClient_PROJECT_GUIDELINES.md)의
> 「MVP2 현재 실행 범위」·「세트 완성도 게이트 — 현행 결정 통합」·9.9절이며,
> 이 문서는 그중 「MVP2 구현 전 결정 필요」에 남은 resend 항목을 닫는다.
>
> **구현 기준은 이 문서(v4) 하나다.** v1~v3은 삭제하며, 각 판의 변경 내용과
> 폐기 사유는 아래 「개정 이력」에 모두 옮겼다.
>
> 표기: **[확정]** 사용자 확인 완료 / **[미결]** 이번 범위에서 결정하지 않음.
> v4로 설계를 마감하면서 v3까지 [제안]이던 항목은 모두 [확정]으로 전환했다.
>
> 관측소 선택 공통 규칙은 [SITE 선택 설계 v1](../site/SFTPClient_SITE_DESIGN_v1.md)에서
> 관리한다. `--site`의 출처는 대표가 제안한 특정 관측소 다운로드 인자이며,
> 식별 규칙은 PUT `resend`와 향후 DOWNLOAD가 공유한다. 이 문서로 DOWNLOAD를
> 구현하지 않는다.

---

## 개정 이력

### v1 — 최초안

- resend = 운영자가 기간을 지정해 Deep Scan 창 밖 파일을 보내는 **수동 명령**.
- 핵심 불변식 "탐색 범위 ⊂ 장부 기억 범위"와 Retention 한계 규칙(§3) 도입.
- Ledger 선별 방식(평소 판정 재사용), 재시도 소진 파일 재무장, 게이트 조건 선택지
  (a) 무조건 우회 / (b-1) 설정 / (b-2) CLI 제시.
- 대량 처리는 resend가 `MaxFilesPerRun` 단위 회차를 **스스로 반복**하고,
  "진전 없음"이면 종료하는 방식.
- 사용자 수정으로 `--site` 인자 추가.

### v1 → v2

| 항목 | v1 | v2 |
|---|---|---|
| 실행 주체 | 수동 명령만 | **자동 단계(정시 회차) + 수동 명령** |
| 우선순위 | 독립 명령 | **같은 락 안에서 put 1순위 → resend 2순위** |
| 대량 처리 | resend가 회차를 스스로 반복 + 락 대기·양보 | **반복 없음.** 매 정시 회차가 남은 예산만큼 처리 |
| 종료 조건 | "진전 없음" 판정 | **없음.** 누락분도 프로그램에게는 신규 파일이다 |
| 게이트 조건 | (a)/(b-1)/(b-2) 선택지 | **(b-1) `ResendMinKinds`** (RINEX2=`o`, RINEX3=`mo`) |
| 소진 재무장 | resend 전체 | **수동에서만** (자동은 MaxRetries 무력화 방지) |

**v1 회차 반복 폐기 사유:** 정시 회차는 매시 한 번만 뜬다. resend가 반복하는 동안
회차 사이의 짧은 틈에 정시 회차가 들어갈 수 없어, resend가 도는 내내 신규분 전송이
멈춘다.

### v2 → v3

| 항목 | v2 | v3 |
|---|---|---|
| lock 탈취 서술 | "같은 프로세스가 같은 락 안에서 실행하므로 탈취 경로가 없다" | **틀림.** ①과 ②는 서로 다투지 않지만, 회차 전체가 `LockStaleSeconds`를 넘기면 **다음 정시 프로세스**가 owner 나이로 탈취한다(heartbeat 없음) |
| 수동 배치 시간 | "1시간 안에 끝난다" | 보장하지 않음. 회선에 따라 3시간을 넘을 수 있음 |
| 대응 | — | 수동 `resend`만 `Acquire`에 24시간 stale, 정시 ② 시작 전 stale 근접 가드 |
| SITE | v1 resend §6.1 안에 있던 규칙 | SITE를 독립 문서로 분리. CLI는 **4자리** 정석, 9자리 거부 |
| mtime 의미론 | 언급 없음 | resend 전용 의미론 없음. 기존 신규·지문 판정 그대로 |

### v3 → v4 (설계 마감)

| 항목 | v3 | v4 |
|---|---|---|
| 정시 ② stale 근접 가드 | ② 시작 시 경과 ≥ `LockStale − 여유`면 ② 생략 | **삭제.** 회차 시간은 `MaxFilesPerRun` 튜닝으로 통제하는 운영 계약(§6.4) |
| 수동 stale 24시간 | 수동 `Acquire`만 24시간 | **삭제.** 수동도 설정 `LockStaleSeconds` 그대로 |
| heartbeat 기각 근거 | 수동 24h + stale 가드로 다룬다 | 운영 계약 + 유닛 3 결론으로 다룬다 |
| 예산 차감 카운터 | ①의 `Registered` | ①의 **`len(kept)`** (§6.2) |
| [제안] 항목 | Q2·Q4·Q5·Q7~Q9·Q12 | 설계 마감으로 **[확정]** |

**v3 가드 삭제 사유**

1. **실제로 막지 못했다.** 시작 가드는 ②의 시작 시점만 보고 소요 시간은 보지 않는다.
   경과 2시간 40분에 통과한 ②가 35분을 쓰면 회차는 3시간을 넘는다.
2. **수동 24시간은 수동 회차를 보호하지 않는다.** stale 문턱은 락을 가진 쪽이 아니라
   **빼앗으려는 쪽**의 인자로 판정한다(`internal/lock/lock.go` `Acquire` —
   `prev.staleThreshold(staleAfter)`). 수동이 24시간으로 잡아도 정시 회차는 자기 3시간으로
   그 락의 나이를 잰다. 24시간 설정이 실제로 한 일은 "수동이 남의 락을 24시간 동안
   빼앗지 않는다"뿐이었다.
3. **이미 결론 낸 문제를 ②에서만 다시 열었다.** 유닛 3(UNIT3 STALL v3 §2.1, §9·§9.1)은
   멈춘 회선은 30초 무진행 감시로 조기 종료시키고, **느리게라도 진행 중인 회선**은
   "stale 탈취 이전 종료를 보장하지 않는다", "lock 구조 변경은 이번 범위 밖"으로
   명시적으로 남겼다. 같은 위험을 가진 ①은 오늘까지 가드 없이 운영되고 있다.
   ②에만 가드를 두는 것은 결정된 정책을 한쪽에서만 뒤집는 반쪽짜리 예외다.
4. **resend는 회차 시간 상한을 늘리지 않는다.** ①+②의 합이 `MaxFilesPerRun`을 넘지
   않으므로 상한은 resend 이전과 같은 "MaxFilesPerRun × 파일당 시간"이다.
   장애 후 적체가 있으면 resend 없이도 ①이 매 회차 max를 채운다.

---

## 0. 한 줄 요약

`resend`는 **Deep Scan 창(ScanDays) 밖으로 밀려난 파일을 보내는 2순위 단계**다.
평소에는 정시 회차가 put을 끝낸 뒤 남은 예산으로 **자동** 실행하고, 운영자가
명령을 입력하면 지정한 기간·관측소·카테고리로 **수동** 실행한다. 판정은 평소
Ledger 판정을 재사용하고, 탐색 기간은 Ledger 기억(RetentionDays) 안으로 제한한다.

원격→로컬 수신이 아니다. 지리원 사례에서 수신 측이 3일·약 5만 개를 하루 종일
내려받았던 구멍을, **송신 측이 날짜 창을 지정해 먼저 PUT** 하는 것으로 막는다.

---

## 1. 배경 — 왜 필요한가

### 1.1 계기 (지리원 사례)

지리원에서 3일치 누락이 발생했고, 그 3일치가 약 5만 개였다. 수신 측이 이를
하루 종일 내려받아야 했다. 송신 측이 이 물량을 먼저 보낼 수 있었다면 수신 측이
직접 내려받지 않아도 됐다.

운영 전제: 한 회차 상한은 **`MaxFilesPerRun = 4000`** 을 계획한다. 5만 건은
한 방이 아니라 여러 정시 회차(및 필요 시 수동 배치)에 나뉜다. 캠페인 전체를
한 lock으로 붙잡지 않는다.

### 1.2 자동 경로가 구조적으로 놓치는 것

v5부터 후보 선정은 Scan 주도다(schema.sql [v5 개정 주석]). Scan이 보지 않는
날짜의 파일은 장부에 무엇이 적혀 있든 후보가 되지 않는다.

| # | 상황 | 장부 상태 | 기존 자동 경로 | resend |
|---|---|---|---|---|
| A | **늦은 유입** — 관측일이 ScanDays보다 과거인 파일이 뒤늦게 도착 | 행 없음 | 해당 날짜를 보지 않음 | **자동** |
| B | **장기 장애 후 만료** — 실패한 채 ScanDays가 지남 | FAILED / PENDING, attempts < MaxRetries | 범위 밖이라 재시도 없음 | **자동** |
| C | **장애 중 재시도 소진** — 매시간 재시도하다 상한 도달 | FAILED, attempts ≥ MaxRetries | 범위 안이어도 제외 | **수동** (§4.3) |

7~31일 사이에 늦게 들어오는 자료는 실제로 있을 수 있다 **[확정]**. 31일을 넘는
경우는 없다고 가정한다 **[확정]**. 거절 경계의 숫자는 가정이 아니라
`RetentionDays`(현재 예제 30, 운영 권장 35 — §3.4)다.

누락분은 "다시 보내는 파일"처럼 들리지만 A·B는 **프로그램 입장에서 아직 한 번도
VERIFIED가 되지 않은 파일**이다. 평소 신규 파일과 같은 판정·같은 경로로 처리되므로
별도의 요청 상태나 종료 판정이 필요 없다 **[확정]**.

mtime 인시던트(유닛 2)용 별도 의미론은 두지 않는다 **[확정]**. 처음 장부에
오르면 기존 신규 경로(첫 `revision`은 1)를 탄다. 이미 관측된 파일은 기존
size/mtime/지문 판정을 그대로 쓴다. `resend`가 revision을 올리거나 지문을
무시하지 않는다.

### 1.3 이 문서가 다루지 않는 것

| # | 상황 | 처리 |
|---|---|---|
| D | 우리 장부는 VERIFIED인데 수신 측이 파일을 잃음 | **[미결]** `--force`. §8.1 |
| E | 크래시로 남은 IN_PROGRESS | 기존 `Recover` 책임 |
| F | 원격→로컬 수신 | `download` (MVP3). SITE 규칙은 공유, 명령은 이 문서 밖 |
| G | `HourLayout` 제거·범위 제한 재귀 | 경로 범용화. resend **다음** 작업 |

---

## 2. 명령 책임 경계

- `Recover` = IN_PROGRESS 크래시 회수, `resend` = ScanDays 밖(수동은 지정 기간)
  로컬→원격 전송, `download` = 원격→로컬 수신. 기간 재전송에 recovery라는
  이름을 쓰지 않는다.
- `--deep`은 자동 스캔 창을 ScanDays로 넓히는 기능이며 resend를 대신하지 않는다.
- resend는 세트 게이트를 `ResendMinKinds` 조건으로 우회한다(§5). Ingress,
  `.filepart` 제외, 해시 불안정 보류는 우회하지 않는다.
- **9.9절 변경:** 기존 9.9절의 "Deep Scan 범위 밖의 자료는 자동 후보가 아니다"를
  자동 resend 단계가 대체한다. GUIDELINES 반영은 구현 완료 후 문서 커밋에서 한다.

---

## 3. 핵심 불변식 — 탐색 범위 ⊂ 장부 기억 범위

### 3.1 문제

Ledger는 "이미 보냈다"는 기억이다. Retention Cleanup은 그 기억을 지운다.
로컬 파일은 10년 보존된다. 따라서 **파일은 보이는데 장부 행이 지워졌다면,
프로그램은 그 파일을 처음 보는 신규로 판정해 다시 보낸다.**

자동 resend가 **매시간** 오래된 날짜를 훑으므로 이 불변식은 수동 전용 설계보다
중요하다. 한 번 어긋나면 운영자가 모르는 사이 매시간 재전송이 일어난다.

| 경로 | 탐색 범위를 정하는 것 | 방어 |
|---|---|---|
| 자동 Scan (Hot/Deep) | ScanDays | `validate.go` — `RetentionDays > ScanDays` 시작 시 강제 |
| **자동 resend** | ScanDays ~ Retention 한계 | **§3.3 — 창 계산이 한계를 넘지 않음** |
| **수동 resend** | 운영자 인자 | **§3.3 — 한계 밖 요청 거부** |
| 경로 범용화(재귀 탐색) | 탐색 날짜 경계 | 경로 범용화 설계에서 같은 함수 재사용 (§9) |

### 3.2 왜 "관측일"로 제한하면 안전한가

- Retention Cleanup의 삭제 기준은 `common_ledger.ingress_verified_at`이다
  (schema.sql 「Ledger Retention Cleanup 정책」). 행이 지워졌다는 것은
  `ingress_verified_at < now − RetentionDays`라는 뜻이다.
- resend 범위는 **관측일**(경로 토큰·파일명의 UTC 날짜 `D`)로 정한다.
- 파일은 자기 관측일이 시작되기 전에 입고될 수 없다. 즉 `D(자정) ≤ ingress_verified_at`.
- 따라서 행이 지워진 파일은 반드시 `D < now − RetentionDays`를 만족한다.
  **From을 `now − RetentionDays`보다 뒤로 두면 행이 지워진 파일은 범위에 들어올 수 없다.**

늦은 유입(§1.2 A)은 오히려 유리한 쪽이다. 관측일은 오래됐지만
`ingress_verified_at`이 최근이라 행이 더 오래 남는다.

### 3.3 규칙 **[확정]**

```text
today  = 실행 시각의 UTC 날짜
limit  = today − (RetentionDays − 2)        ; Retention 한계, 여유 1일 포함

자동 resend 창:  From = limit
                 To   = today − ScanDays    ; Deep 창 바로 바깥 날부터
수동 resend:     From >= limit, To <= today, From <= To  (위반 시 거부)
```

- 한계 계산은 **하나의 함수**로 두고 자동·수동·경로 범용화가 같이 쓴다.
- 수동 요청이 한계를 넘으면 **거부하고 종료**한다. Retention 밖을 강제로 여는
  옵션은 두지 않는다.
- 여유 1일은 §3.2 가정(관측일 ≤ 입고 시각)이 날짜 경계·시계 오차·파일명 오기로
  조금 어긋나는 경우를 흡수한다. 수학적으로는 `RetentionDays − 1`까지 안전하다.
- 자동 창이 비는 경우(`From > To`, 즉 RetentionDays가 ScanDays + 2 이하)는 자동 단계를
  건너뛰고 시작 시 WARN을 남긴다. 현재 검증은 `RetentionDays > ScanDays`만 강제하므로
  이 경우가 설정상 가능하다.
- 현재 Retention Cleanup은 미구현이라 실제 삭제는 없다. 그래도 resend가 먼저
  배포되므로 규칙은 지금 코드로 강제한다.

### 3.4 설정값 — `RetentionDays` 운영 권장 35 **[확정]**

현재 `config.example.ini`의 `[LEDGER] RetentionDays = 30`이면 resend가 닿는 가장
오래된 날은 **29일 전**이다. "31일까지 늦게 올 수 있다"는 가정을 덮으려면
`RetentionDays ≥ 33`이 필요하다. 운영 권장값은 **35**다. 코드 변경이 아니라
설정값이며, 배포 시 `config.example.ini` 주석과 현장 `config.ini`에 반영한다.

---

## 4. Ledger 의미론 — 선별 방식 **[확정]**

### 4.1 결정

resend는 **평소 회차의 후보 판정을 그대로 재사용**하고, 스캔 창만 §3.3의 창으로
바꾼다. 평소와 달라지는 것은 다음뿐이다.

| | 자동 resend | 수동 resend |
|---|---|---|
| 세트 게이트 | `ResendMinKinds` (§5) | `ResendMinKinds` (§5) |
| 재시도 소진 파일 | 평소처럼 제외 | **재무장** (§4.3) |
| site 선택 | 없음 (전 관측소) | `--site` (SITE 문서) |

```text
장부에 없음                         → 신규. Ingress 후 전송        (A)
있고 size/mtime 동일, VERIFIED      → 제외                        (이미 보냄)
있고 동일, NULL / PENDING / FAILED  → 전송 또는 재시도            (B)
있고 동일, FAILED exhausted         → 자동: 제외 / 수동: 재시도   (C)
있고 상이                           → 기존 판정 (지문 대조 → revision +1)
```

PENDING/IN_PROGRESS에 새 상태를 만들지 않는다. 시작 시 `Recover`는 기존과 같다.
site 필터는 Recover 범위를 줄이지 않는다(SITE §4).

### 4.2 이유

- 지리원 사례의 목적은 누락분이다. 이미 VERIFIED인 파일을 다시 보내면 수신 측
  부담을 줄이려던 기능이 오히려 부담을 늘린다.
- 판정 로직을 새로 만들지 않는다. `put` 패키지 원칙("판정은 verify, 기록은 ledger,
  나열은 scan") 그대로이며, 유닛 1~5의 해시 판정·`.filepart` 제외·Ingress 규칙이
  resend에도 자동으로 적용된다.
- `scan.Range`가 이미 임의의 From/To를 받는다(`internal/scan/scan.go`). Scan 쪽은
  수정할 것이 없다. Hot/Deep/resend 차이는 Range와 호출 옵션뿐이다.
- 한 회차에 다 못 보낸 파일은 다음 정시 회차의 자동 resend가 다시 발견한다.
  **매 회차가 곧 수렴 수단**이다. 요청 상태·종료 판정을 저장하지 않는다.

### 4.3 재시도 소진 파일 — 수동에서만 재무장 **[확정]**

§1.2 C는 누락의 흔한 원인일 수 있다. 3일 장애라면 매시간 회차가 같은 파일을
MaxRetries(5)번 시도하고 소진시킨다.

- **수동 resend**에서는 `FAILED && attempts ≥ MaxRetries`도 재시도 후보로 올린다.
  운영자의 명시적 지시가 자동 재시도 예산보다 우선한다. 게이트 우회와 같은 원칙이다.
- **자동 resend**에서는 재무장하지 않는다. 자동에서 재무장하면 영구 실패 파일
  (예: 원격 권한 오류)이 ScanDays를 넘긴 뒤 Retention 한계까지 약 24일 동안
  **매시간** 다시 시도된다. MaxRetries 상한이 사실상 사라진다.
- `attempts`는 0으로 되돌리지 않는다. 누적값은 장애 분석 기록이므로 보존한다.
  BeginPut은 평소처럼 +1 한다.
- 수동 1회 실행에서 예산 밖으로 잘린 소진 파일은 다음 수동 실행에서 다시 재무장된다.
  자동 단계는 이 파일들을 계속 제외한다.

---

## 5. 세트 게이트 조건 — `ResendMinKinds` **[확정]**

### 5.1 요구

"일부 파일이 끝까지 오지 않아 세트가 미완성으로 보류된 경우, O 파일 같은 필수
파일이 있으면 강제로 전송한다." 게이트 우회는 resend에 겸용한다.

### 5.2 설정

```ini
[SET.RINEX2]
RequiredKinds  = o,n,...      ; 평소 게이트 (예시)
ResendMinKinds = o            ; RINEX2 는 o 가 필수

[SET.RINEX3]
RequiredKinds  = mo,mn,...
ResendMinKinds = mo           ; RINEX3 는 mo 가 필수
```

- 정책은 기존처럼 `[SET.RINEXx]` 한 곳에 둔다. 코드에 `o`·`mo`를 상수로 넣지 않는다
  (세트 결정: "필수 종류를 버전별 상수로 강제하지 않는다").
- resend 단계(자동·수동)의 미완성 세트는 `ResendMinKinds`의 **모든** 종이 있으면
  전송하고, 하나라도 없으면 계속 보류한다. RequiredKinds와 같은 AND 의미다.

### 5.3 검증 규칙

| 경우 | 동작 |
|---|---|
| 게이트 ON, 키 있음 | 값 문법은 RequiredKinds와 동일(빈 항목·중복·영문 외 문자 오류). **RequiredKinds의 부분집합**이어야 한다 |
| 게이트 ON, 키 없음 | resend 단계에서 게이트를 **무조건 우회**한다(기존 GUIDELINES 12절 결정과 동일) |
| 게이트 OFF(`RequiredKinds = false`), 키 있음 | **시작 오류.** 우회할 게이트가 없는 설정은 운영자 착오다 |
| 게이트 OFF, 키 없음 | 현재 배포 상태. 변화 없음 |

- RINEX4 섹션도 같은 규칙이다. 운영값(`mo` 예상)은 RINEX4 운영 시 확정한다.
- 주의: RINEX2 Hatanaka 관측 파일(`.yyd`)은 kind가 `d`다. 어떤 관측소가 `o` 대신 `d`만
  내보낸다면 `ResendMinKinds = o`를 영원히 충족하지 못한다. RequiredKinds에도 똑같이
  있는 문제이며, 게이트를 켜는 기관의 실제 파일 분포로 확인한다.

### 5.4 현재 효과 범위

현재 모든 기관이 `RequiredKinds = false`(게이트 OFF)다. 게이트가 꺼져 있으면
resend에 우회할 대상이 없으므로 이 설정은 **게이트를 켠 기관에서만** 의미가 있다.

### 5.5 공통 규칙

- 파싱 불가 파일은 평소처럼 개별 통과(`SetUnparsed`)한다.
- `MaxFilesPerRun` 세트 경계 절단은 resend에도 적용한다. 우회로 통과한 세트도
  절단선에서 쪼개지 않는다.
- 우회로 전송한 미완성 세트는 `[SET][RESEND]` 로그로 세트 키·있는 종·없는 종을 남긴다.
  보고 리포트 기능이 아니라 기존 `heldSets` 요약을 재사용한 로그 한 줄이다.

---

## 6. 실행 형태

### 6.1 자동 단계 — 정시 회차의 2순위 **[확정]**

```text
정시 회차 (락 하나, stale = 설정 LockStaleSeconds)
  Lock → Dial(+stall watchdog) → Recover
  → ① put   : Hot/Deep, 기존 그대로                       (1순위)
  → ② resend: 자동 창(§3.3), 남은 예산만큼                (2순위)
  → Release
```

- put과 resend는 **같은 락**을 쓴다. 같은 프로세스 안에서 차례로 실행하므로
  ①과 ②가 서로 락을 다투지 않는다.
- 다음 정시 프로세스의 stale 탈취는 같은 프로세스라는 사실로 막히지 않는다.
  이는 §6.4의 운영 계약으로 다룬다.
- ②는 ①의 Transfer가 **끝난 뒤** 시작한다. put 코드와 정렬 규칙은 바꾸지 않는다.
- **stall·오류 처리:** 유닛 3의 30초 무진행 감시가 ①에서 발화하면 회차 ctx가
  취소되고 SSH가 닫힌다. 이 상태로 ②를 시작하면 죽은 연결로 착수만 시도하게 되므로,
  ②는 **stall 표식을 확인하고 건너뛴다.** 새 기능이 아니라 기존 stall 종료 경로를
  ② 앞에서 한 번 더 지키는 조건문이다. ①이 stall 이외의 오류로 끝나도 ②는 실행하지 않는다.
- `report.Range = "resend"`로 요약을 한 줄 더 찍어 모집단을 hot/deep과 분리한다(UNIT4 §5).

### 6.2 예산 **[확정]**

```text
resend 예산 = MaxFilesPerRun − len(①의 kept)
예산 ≤ 0    → ② 건너뜀, [RESEND] skipped (budget=0) 로그
```

- 두 단계가 `MaxFilesPerRun` 하나를 나눠 쓴다. 계획 운영값은 **4000**이다.
- **차감 기준은 `len(kept)`다.** live에서는 `Registered`가 `len(kept)`와 같지만
  (`plan.go` finalize — keys를 kept로만 만든다), dry-run에서는 `Registered = 0`이라
  ② 예산이 전체 `MaxFilesPerRun`으로 계산되어 미리보기가 실제보다 커진다.
  live와 dry-run이 같은 숫자를 내도록 kept로 통일한다.
- 실측: 5000개 약 45분(분당 약 110개). 지리원 평시 유입은 시간당 약 220개
  (Hourly 110 + Daily 110)다. `MaxFilesPerRun = 4000`이면 평시 ① 이후 resend 몫이
  약 3800개다. 5만 개 누락은 예산이 매시간 거의 남을 때 약 13회차다.
- ①이 예산을 다 쓰면 ②는 쉰다. 신규분 우선이며, 이때 누락 해소는 **며칠**이 걸릴
  수 있다. 한 lock을 며칠 붙잡아 정시를 멈추는 것보다 낫다.
- 매시간 1만 개 이상 유입은 ①이 예산을 채운다. 이런 유입은 사람이 FileZilla 등으로
  수동 복구할 때 말고는 거의 없다.
- **`MaxFilesPerRun = 0`:** 0은 "절단 없음"이다. ②에 넘기기 전에 0을 명시적으로
  처리한다(`MaxFilesPerRun = 0`이면 ②도 무제한, 남은 예산 0이면 건너뜀). 테스트로
  고정한다. 0은 회차 시간 상한이 없으므로 §6.4 운영 계약 밖의 값이며 운영에
  쓰지 않는다. config에서 0을 거부할지는 **[미결]**(§8.5).

### 6.3 수동 명령과 `--site` **[확정]**

`--site`를 추가하고 `--category`와 함께 사용할 수 있게 한다.
출처는 대표의 관측소 지정 다운로드 요구이며, 공통 식별은
[SITE v1](../site/SFTPClient_SITE_DESIGN_v1.md)이 소유한다. 이번 구현은
**송신 `resend` CLI**에만 `--site`를 붙인다. 일반 정시 PUT에 `--site`를 노출하지
않고, DOWNLOAD CLI 전체 형태는 **[미결]**이다.

문서의 인자 표기 순서는 `site → category → from → to`다. 파서에서 옵션 순서를
강제하지 않는다.

```text
rinexclient resend [--site <SITE>[,<SITE>...]] [--category <C>[,<C>...]]
                   --from <날짜> --to <날짜>
                   [--dry-run] [--config <path>] [--transport <t>]

rinexclient resend --site DBON --category RINEX2_DAILY --from 2026-09-01 --to 2026-09-03
```

| 인자 | 필수 | 의미 |
|---|---|---|
| `--site` | 아니오 | 지정한 관측소만 선택. 생략 시 모든 관측소. **4자리 영숫자** (SITE 문서) |
| `--category` | 아니오 | 생략하면 활성 PUT Category 전부. 지정 시 활성 Category만 허용 |
| `--from`, `--to` | 예 | **UTC 관측일**, 양끝 포함. `YYYY-MM-DD` 또는 `YYYY-DDD`(연-DOY) |
| `--dry-run` | 아니오 | 장부를 쓰지 않고 후보 수·분포만 관측 |
| `--config`, `--transport` | 아니오 | 기존 의미 그대로 |

- 서브커맨드 분기는 `secure-set`과 같은 방식(`os.Args[1]`)이다.
- 날짜는 UTC로 해석한다. 로그에는 두 표기를 함께 남긴다:
  `from=2026-09-01(244) to=2026-09-03(246)`.
- `--seed`, `--deep`은 resend와 함께 쓸 수 없다.
- 인자 이름은 `--site`이며 `--station` 별칭은 추가하지 않는다.
- site·category·기간을 함께 지정하면 세 조건의 교집합이다.
  site 필터는 Ledger 등록·후보 선정 전에 적용하고, dry-run에도 같다.
  site는 게이트 우회·VERIFIED 제외·재시도 정책을 바꾸지 않는다.
- 수동 기간은 ScanDays 안쪽 날짜를 포함해도 된다. 한계는 §3.3의 Retention 쪽만 둔다.

SITE 매칭:

- CLI 정석은 **4자리**다. `DBON`, 네 번째가 숫자인 `SUW1` 모두 허용.
  9자리(`DBON00KOR`)는 `--site` 값으로 받지 않는다.
- 파일에서 뽑은 코드(RINEX2 4자리, RINEX3·4 긴 이름의 앞 4자리)와 정확 일치한다.
- 쉼표 복수·대소문자 무시·와일드카드 없음. 식별 불가 파일은 SITE 문서대로 제외·집계.

**수동 실행 흐름**

```text
Lock(같은 경로, stale = 설정 LockStaleSeconds)
→ Dial(+stall watchdog) → Recover
→ resend 1회 (지정 조건, 예산 = MaxFilesPerRun, 소진 재무장)
→ Release
```

- **한 번 실행 = 한 배치**다. 스스로 반복하지 않는다. 잘린 파일은 자동 창 안이면
  다음 정시가 이어 보내고, 운영자는 필요하면 다시 실행한다.
- 수동 실행에는 put 단계(①)가 없다. put은 정시 회차의 책임이다.
- 수동도 예산이 `MaxFilesPerRun`이므로 정시 회차와 **같은 운영 계약(§6.4)** 으로
  시간이 통제된다. 수동 전용 stale 값을 두지 않는다.
- **락이 잡혀 있을 때:** 스케줄러는 조용히 종료(0)한다. 수동은 사람이 기다리므로
  `[RESEND] lock held — waiting` 후 재시도한다. 대기 상한은 설정 `LockStaleSeconds`를
  넘기지 않는다. 상한을 넘기면 오류로 끝나고 운영자가 다시 실행한다. 재시도 간격은
  구현 시 정한다.
- 수동이 정시 시각과 겹치면 그 정시는 기존처럼 `ErrHeld`로 한 번 양보한다.

### 6.4 회차 시간 — `MaxFilesPerRun` 운영 계약 **[확정]**

회차 시간은 코드 가드가 아니라 **`MaxFilesPerRun` 튜닝으로 통제한다.**

> `MaxFilesPerRun`은 평상시 회차(①+②, 또는 수동 1배치)가 약 1시간 안에 끝나도록
> 정한다. `LockStaleSeconds`(기본 3시간)는 회선 저하에 대비한 약 3배 여유다.
> 회선이 느려져 여유가 부족하면 **상한(LockStale)을 늘리지 말고 절단 크기
> (`MaxFilesPerRun`)를 줄인다.**

- 이 계약은 resend 이전부터 ①이 따르던 것과 같다. resend는 ①+②의 합이
  `MaxFilesPerRun`을 넘지 않으므로 회차 시간 상한을 늘리지 않는다.
- lock 구조는 바꾸지 않는다. heartbeat를 도입하지 않고, owner 나이만으로 stale을
  판정하는 현재 lock을 유지한다. 정시·수동·seed 모두 `Acquire`에 설정
  `LockStaleSeconds`를 쓴다.
- 멈춘 회선은 유닛 3의 30초 무진행 감시가 조기 종료시킨다. 느리게라도 진행 중인
  회선에서 stale 탈취 이전 종료를 보장하지 않는 것은 유닛 3의 결론과 같으며,
  이 계약이 그 위험을 운영값으로 다룬다.
- `MaxFilesPerRun`이 회차 시간 제어의 **유일한 장치**다. 이 값을 크게 올릴 때는
  lock 탈취 여유를 함께 확인한다. `config.example.ini` 주석에 이 계약을 적는다.

**계약으로 통제되지 않는 부분 (운영 관측 대상)**

- **② Run 단계 시간:** 신규 파일의 해시(`runner.go`)는 `MaxFilesPerRun` 절단
  (`plan.go` finalize)보다 **먼저** 일어난다. 늦은 유입 5만 건이 처음 보이는 ②에서는
  5만 건을 전부 Verify·해시·Upsert한 뒤 자른다. 1회성 비용이며(다음 회차부터 Unchanged),
  UNIT4 해시 계측(`HashNew` ms)으로 실측한다.
- **3배를 넘는 회선 저하:** resend 이전에도 같은 위험이다. 배포 후 회차 소요시간으로
  관측하고, 필요하면 위 처방(절단 크기 축소)을 적용한다.

---

## 7. 관측 (로그·리포트)

- 자동: ② 시작 시 `[RESEND] auto from=… to=… budget=…`, 건너뛸 때 사유
  (`budget=0` / `stalled` / `put error` / `empty window`)
- 수동: `[RESEND] manual sites=… categories=… from=… to=… days=… retention_limit=…`
- site 생략은 `sites=all`로 표시하고, 지정 시 정규화한 선택값을 남긴다.
  site 불일치·식별 불가 제외 집계는 SITE 문서의 관측 규칙을 따른다.
- 요약 한 줄(`range=resend`)은 자동·수동 공통이다. 자동/수동 구분은 `[RESEND]` 줄의
  `auto`/`manual`로 한다.
- 우회 전송한 미완성 세트: `[SET][RESEND]` (§5.5)
- 수동 재무장한 소진 파일 수: `rearmed_exhausted=` 카운터

---

## 8. 미결·비범위

### 8.1 `--force` (VERIFIED 재전송) **[미결]**

§1.3 D. 수신 측이 받은 파일을 잃은 경우다. VERIFIED 행을 다시 보내려면
put_ledger 의미론을 바꿔야 한다(같은 revision을 VERIFIED → 재시도로 되돌릴지, 이력을
어떻게 남길지). 지리원 사례는 선별 방식으로 충분하므로 이번 범위에서 제외한다.

### 8.2 Retention 밖 요청

거부로 확정한다(§3.3). 31일 초과는 없다는 가정이 깨지면 재검토한다.

### 8.3 자동 resend의 스캔 비용

자동 resend는 매시간 약 `RetentionDays − ScanDays`일치(35/7 기준 약 26일) 디렉터리를
나열하고 장부를 조회한다. Deep이 하루 한 번 7일을 보던 것보다 나열량이 크다.

- 전송이 아니라 나열·조회 비용이며, 대부분 Unchanged + VERIFIED로 빠진다.
  해시는 size/mtime이 바뀐 경우와 백필 예산 안에서만 돈다.
- 배포 후 `range=resend` 요약의 소요시간으로 실측한다 **[확정]**. 부담이 크면 자동
  resend를 Deep처럼 하루 N회로 줄이는 방안을 그때 검토한다. 선제 구현하지 않는다.

### 8.4 lock heartbeat·stale 가드

기각한다. §6.4 운영 계약과 유닛 3의 무진행 감시로 다룬다. 기각 경위는
「개정 이력 v3 → v4」의 가드 삭제 사유를 따른다.

### 8.5 `MaxFilesPerRun = 0` config 거부 **[미결]**

0은 §6.4 운영 계약 밖의 값이다. 교차검증에서 config 단계 거부가 제안되었다.
참고로 키 **부재**는 이미 시작 오류다(`load.go` `intVal` → `value` → `absent`
→ `ErrMissingKey`). 따라서 부재 시 기본값+WARN으로 바꾸는 안은 현행보다 느슨해지므로
채택하지 않는다. 0 거부를 도입할 경우 validate 메시지 `(use 0 for unlimited)`,
`config.example.ini` 주석, 현장 config의 0 사용 여부 확인이 함께 필요하다.
resend 구현과 독립적인 항목이므로 이번 범위에서 결정하지 않는다.

---

## 9. 경로 범용화와의 관계

- resend **다음에** 진행한다. 이 문서에서 `HourLayout`을 제거하지 않는다.
- resend는 경로를 직접 계산하지 않는다. `CategoryJob.LocalPath`와 Scan이 나열한
  결과를 그대로 쓰므로, 경로 범용화로 Scan이 재귀 탐색으로 바뀌어도 resend 코드는
  그대로 따라간다.
- 템플릿이 가리키지 않는 위치의 파일은 지금 Deep도 수동 resend도 못 찾는다.
  그 구멍은 경로 범용화 몫이다.
- §3의 불변식은 재귀 탐색의 날짜 경계에도 똑같이 적용한다. §3.3의 한계 함수를
  `config`가 아닌 독립 함수로 둔다(구현 시 위치 확정).

---

## 10. 구현 계획 (커밋 단위)

| # | 커밋 | 내용 | 테스트 |
|---|---|---|---|
| 1 | docs | 이 문서(v4)와 SITE 공통 설계 | — |
| 2 | config: `ResendMinKinds` | 키 파싱·§5.3 검증 | 부분집합, 게이트 OFF+키 오류, 키 없음 |
| 3 | domain: site 선택 | SITE 확정 규칙에 따른 추출·매칭. DOWNLOAD에서도 재사용할 공통 부분 | 짧은/긴 이름, 대소문자, 복수, 불일치·식별 불가, 4자리만 허용 |
| 4 | put: resend 옵션 + 필터 | 게이트 조건(§5)·소진 재무장(§4.3)·Retention 한계(§3.3)·site 필터(등록 전) | 최소 종, 재무장 수동만, attempts 보존, 세트 절단, 한계 경계, 선택 밖 장부 불변 |
| 5 | cmd: 자동 resend 단계 | ① 뒤에 ②, 예산(§6.2, `len(kept)` 차감), stall·오류 시 건너뜀 | 예산 0/음수/`MaxFilesPerRun=0`, dry-run 예산 일치, stall, put 오류, 빈 창 |
| 6 | cmd: `resend` 서브커맨드 | site·category·날짜 인자, 한계 검증, 락 대기(상한 = LockStale), 종료 코드 | 교집합, dry-run, 범위 거부, 형식 오류, ErrHeld 대기 상한 |
| 7 | docs | GUIDELINES(9.9절 대체 포함)·결과 문서, SITE 결정 반영, `config.example.ini` 주석(RetentionDays 35, ResendMinKinds, §6.4 운영 계약) | — |

기본 예상은 총 7개다. 5번에서 `run()` 정리가 커지면 "동작 변경 없는 회차 함수
추출"을 앞에 두어 8개가 될 수 있다. 고정 개수는 아니다.

---

## 11. 결정 목록 (설계 마감)

| # | 항목 | 결정 |
|---|---|---|
| Q1 | 게이트 조건 | **확정** — (b-1) `ResendMinKinds`, RINEX2=`o`, RINEX3=`mo` |
| Q2 | 소진 파일 재무장 | **확정** — 수동에서만 (§4.3) |
| Q3 | 대량 처리 | **확정** — 같은 락, put 1순위·resend 2순위, 반복·종료 판정 없음, 캠페인 lock 없음 |
| Q4 | `RetentionDays` 운영값 30 → 35 (§3.4) | **확정** — 운영 권장값, 배포 시 반영 |
| Q5 | 날짜 인자 두 형식 허용 (§6.3) | **확정** |
| Q6 | site: CLI는 4자리 영숫자 정석, 9자리 입력 거부, 네 번째 숫자 허용 | **확정** (SITE §3). 복수·생략·식별 불가·0건 종료 코드는 SITE 문서. DOWNLOAD CLI는 미결 |
| Q7 | `ResendMinKinds` 검증 — 게이트 OFF에서 키가 있으면 시작 오류 (§5.3) | **확정** |
| Q8 | 자동 resend 창 시작점 = Deep 창 바로 바깥 (§3.3) | **확정** |
| Q9 | 자동 resend 스캔 비용은 배포 후 실측 (§8.3) | **확정** |
| Q10 | lock stale — 정시·수동 모두 설정 `LockStaleSeconds`, heartbeat 없음 (§6.4) | **확정** (v3의 수동 24시간 폐기) |
| Q11 | 회차 시간 통제 — `MaxFilesPerRun` 운영 계약, stale 근접 가드 없음 (§6.4) | **확정** (v3의 stale 가드 폐기) |
| Q12 | 수동 lock 대기 상한 = 설정 `LockStaleSeconds` (§6.3) | **확정** |
| Q13 | 예산 차감 = `len(①의 kept)` (§6.2) | **확정** |
| Q14 | `MaxFilesPerRun = 0` config 거부 (§8.5) | **미결** — resend와 독립 |
