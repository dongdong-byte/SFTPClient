# SFTPClient resend 커밋 5 — 설계 쟁점 2건 검토 v1

> 작성 2026-09-21. resend 구현 계획([v4 §10](SFTPClient_RESEND_DESIGN_v4.md#10-구현-계획-커밋-단위))의
> 커밋 5 「put: resend 대상 범위·site 필터」 착수 전 검토다.
>
> 이 문서는 **검토 의견**이며 구현 완료 기록이 아니다. 교차검증 결과를 합쳐
> 채택안이 정해지면, 기각 사유와 함께 v4 본문(§3.3·§9)과 SITE v1(§4·§6)에
> 반영하고 이 문서는 그 경위 기록으로 남긴다.
>
> 표기: **[제안]** 이 문서의 결론 / **[확정]** 상위 문서에서 이미 닫힌 항목.
> 교차검증 보완: 2026-09-21 — 빈 창 경계 정정, 입력 오류와 전체 선택의 구분,
> 필터 위치·검증 범위 보강. 자료구조 선택은 운영 결함과 구분한 제안이다.
> 교차검증 보완(2): 2026-09-21 — scan 패키지 부착 기각, cmd의 import 불가,
> `time.Now`/로그 금지, SeedMode 선례, 자동 resend는 Sites를 비운다.

---

## 0. 배경

현재 자동 PUT은 `main`이 스캔 범위(`scan.Range`)를 계산해
`put.Runner.Run(ctx, jobs, rng)`에 넘기고, put은 **받은 범위를 스캔**해
Ledger 대조 → 후보 선정·PENDING 등록까지 수행한다. 실제 전송은 후속 단계가 한다.

커밋 5는 **부품만 추가하고 아무도 호출하지 않는다.** 배선은 커밋 7(정시 회차
뒤 자동 resend)과 커밋 8(`resend` 서브커맨드)이 한다. 따라서 두 쟁점 모두
"커밋 5는 부품만, 호출 없음, **일반 실행 동작 불변**" 범위 안에서 판단한다.

상위 규칙은 이미 확정이다 **[확정]** (v4 §3.3·§3.5).

```text
today  = 실행 시각의 UTC 날짜
limit  = today − (RetentionDays − 2)      ; Retention 한계
origin = 장부 운영 시작일 (커밋 4의 OperationOrigin으로 조회)

자동 창:  From = max(limit, origin),  To = today − ScanDays
수동:     From >= limit, To <= today, From <= To  (위반 시 거부, origin 미적용)
```

v4 §3.3은 "한계 계산은 **하나의 함수**로 두고 자동·수동·경로 범용화가 같이
쓴다"고 못박았다.

프로젝트 원칙: 수정 최소, 하나의 사실에 하나의 주인, SOLID, 선제 구현 금지.

---

## 1. 쟁점 1 — 창 계산 함수를 어디에 두는가

### 1.1 선택지

| 안 | 위치 | 요지 |
|---|---|---|
| A | `internal/<새 패키지>` | 순수 함수 3개. `RetentionLimit` / `Auto` / `ValidateManual`. origin은 호출자가 `ledger.OperationOrigin`으로 읽어 값만 넘긴다 |
| B | `internal/put` | 범위를 소비하는 곳에 계산도 둔다 |
| C | `cmd/rinexclient/window.go` | 패키지를 만들지 않는다 |
| — | `internal/scan`에 부착 | 선택지에 없었으나, Range 옆에 두면 응집되어 보인다는 반론이 있어 함께 기각한다 |

### 1.2 결론 — A안 **[제안]**

**"함수 3개짜리 마이크로 패키지는 과분리"라는 비판에는 동의하지 않는다.**
패키지 경계는 줄 수보다 **책임과 의존 방향**으로 판단한다. 날짜 정책을 순수
함수로 모으고 ledger·config를 직접 읽지 않는다. `scan.Range`를 반환하면 scan
패키지에는 의존하므로, 의존이 없는 잎사귀라고 표현하지는 않는다.

**C안 기각 사유**는 파일 충돌 위험이 아니다.

1. 프로젝트 원칙이 **`main.go`에 비즈니스 로직을 두지 않는다**고 못 박았다.
   `window.go`로 파일을 나눠도 패키지는 `main`이다. 다른 `internal` 패키지가
   import할 수 없으므로, v4 §9의 경로 범용화가 같은 `limit`을 쓰려면
   처음부터 `internal`이어야 한다.
2. v4 §9가 **경로 범용화에서 같은 한계 함수를 재사용한다**고 명시했다.
   미래 호출 계층은 아직 단정할 수 없지만, CLI 밖에서도 사용할 정책을
   cmd에 묶지 않는 쪽이 해당 재사용 의도를 잘 드러낸다.
3. 지금 `main.go`는 두 곳(`:428` seed, `:519` 정시)에서 `scan.Range`를
   **인라인으로 계산**한다. 여기에 창 규칙 세 개를 더 얹으면 시간 정책이
   main 에 흩어지는 현재 상태를 굳힌다.

**B안 기각 사유**는 원 제기 그대로 타당하다. put이 시간 정책(Retention 여유일,
origin 하한)을 소유하게 되고, put이 쓰지도 않는 `ValidateManual`(CLI 전용)까지
품는다. 기존 책임 배치는 "창 이름은 main이 알고, put은 받은 `scan.Range`만
스캔한다"이다. Hot/Deep도 그렇게 되어 있다.

**`internal/scan` 부착 기각.** `scan`은 이미 "Hot / Deep / resend 도 구분하지
않는다. 차이는 Range 뿐"이라고 패키지 주석에 적혀 있다. `Auto`·`ValidateManual`을
붙이면 나열 패키지가 resend 시간 정책을 알게 되어 그 계약을 깬다.
`scan.Range`를 **반환값으로 쓰는 것**과 정책 함수를 scan에 **두는 것**은 다르다.

**교차검증 의견:** C안도 가능한 차선책이다. 호출자가 둘이라는 사실만으로
별도 패키지가 필요하지는 않으며, main 패키지에서 테스트하는 것도 결함이 아니다.
A안 채택 근거는 확정된 공통 날짜 정책의 재사용이며, 기존 Hot/Deep 계산까지
이번 커밋에서 이동하거나 미래용 인터페이스를 미리 만들지는 않는다.

### 1.3 A안에서 바꿀 두 가지 **[제안]**

**(1) `Auto`의 반환을 `(scan.Range, ok bool)`로 두지 않는다.**

호출자가 로그 수준을 판단하려면 "왜 비었는지"를 알아야 한다. bool 로는 알 수
없어 호출자가 `origin`과 `limit`을 **다시 비교**하게 되고, 같은 계산이 두 곳에
생긴다. 이유를 값으로 돌려준다.

```go
type AutoResult struct {
    Range scan.Range
    Empty bool
    Why   EmptyReason // EmptyNone / EmptyBeforeOrigin / EmptyRetentionTooShort
}
```

이 형태가 **v4 §3.3(WARN)과 §3.5(INFO)의 로그 수준 충돌**도 함께 닫는다.

| 사유 | 뜻 | 로그 |
|---|---|---|
| `EmptyBeforeOrigin` | `limit <= To`이고 `origin > To` | INFO (운영 초기의 정상 상태) |
| `EmptyRetentionTooShort` | `limit > To`, 즉 `RetentionDays < ScanDays + 2` | WARN (자동 창을 확보하지 못한 설정) |

판단 근거가 데이터에 실리므로 호출자가 재계산하지 않는다.

**경계 정정:** 양끝 포함이므로 `RetentionDays = ScanDays + 2`이면
`limit = To`다. origin이 이를 넘지 않으면 **1일짜리 유효 창**이다.
v4 §3.3의 "ScanDays + 2 이하이면 빈 창" 설명은 공식과 모순되므로,
채택안 반영 시 "미만"으로 함께 정정해야 한다. 두 원인이 겹치면 먼저
`limit > To`를 확인하여 설정 원인이 INFO로 가려지지 않도록 한다.

`Empty`와 `Why`를 함께 반환한다면 `Empty == (Why != EmptyNone)`을 보장한다.
둘을 반드시 둘 필요는 없으며 사유 하나로 빈 여부를 표현하는 것도 가능하다.
구체적인 반환 구조는 설계 선택이다.

**(2) `ValidateManual`은 센티널 오류를 내보낸다.**

`ErrBeforeRetentionLimit`, `ErrToInFuture`, `ErrFromAfterTo` 정도면 된다.
커밋 8의 CLI가 메시지·종료 코드를 정할 때 문자열 매칭을 하지 않아도 된다.

**입력 책임:** 날짜 문자열(`YYYY-MM-DD`/연-DOY) 해석은 CLI가 맡는다.
창 함수는 모든 날짜를 같은 UTC 날짜 기준으로 다루고 자동·수동 모두
`RetentionLimit`을 재사용한다. `Auto`와 `ValidateManual`은 각자 식을
다시 쓰지 않고 이 함수를 호출한다 — §3.3의 "하나의 함수"가 코드로
보이는 지점이다. 잘못된 설정값·유효하지 않은 origin을 정상적인 빈 창으로
숨기지 않는다. 검증된 입력만 받는 전제를 명시하거나 오류를 별도로 반환한다.
호출자는 `OperationOrigin` 오류를 전파하며, 실패 시 origin을 zero 값이나
Retention 한계로 대체하지 않는다.

**커밋 5 계약 (호출 없음).** 패키지는 `time.Now()`를 부르지 않는다. `today`·
`origin`은 호출자(테스트)가 자른 UTC 날짜로 넘긴다. slog/WARN/INFO도 넣지
않는다. 로그 수준은 `AutoResult.Why`를 읽는 커밋 7·8의 몫이다.
ledger·config를 import하지 않는다. 커밋 4의 `OperationOrigin`은 값이 된
뒤에야 이 함수에 들어온다.

**이름:** `window.Auto`는 호출부에서 무슨 창인지 드러나지 않는다.
`internal/scanwindow`를 권한다.

---

## 2. 쟁점 2 — site 필터를 runner에 어떻게 주입하는가

### 2.1 확정된 계약 **[확정]** (SITE v1 §4)

- 선택 밖 파일에는 Ledger Upsert·해시 백필·전송 상태 변경을 **일절 하지 않는다**
  (등록 전 필터).
- site 지정 시 관측소를 식별할 수 없는 파일은 제외하고 집계한다.
- site 생략 시에는 **site 검사 자체를 하지 않는다** — 식별 불가를 새 제외 사유로
  만들지 않는다.
- 추출 함수 `domain.SiteFromName`은 커밋 3으로 구현 완료.

### 2.2 선택지

| 안 | 방식 | 요지 |
|---|---|---|
| A | `RunOptions`에 필드 추가 | `visitBatch`에서 임시파일 제외 직후, Upsert 이전에 거른다 |
| B | resend 전용 runner·데코레이터 | 판정·게이트·백필 로직을 복제하거나 가로채는 계층을 둔다 |
| C | `Run` 시그니처에 인자 추가 | `Run(ctx, jobs, rng, sites)` |

### 2.3 결론 — A안 **[제안]**

**결정적 이유는 구조다.** 필터가 걸리는 자리는 `visitBatch`(`runner.go:411`)인데,
C안은 `Run` → `runCategory` → `visitBatch` **세 시그니처를 모두** 고쳐야 한다.
`visitBatch`는 이미 인자가 10개다. 여기에 하나를 더 붙이는 것은 "수정 최소"와
반대 방향이다. `Opts`는 `r.Opts`로 어디서든 닿는다.

C안의 명분("실행마다 달라지는 값은 Opts 가 아니라 인자")은 원칙으로는 옳다.
다만 이 저장소에서 그 비용이 세 단계 시그니처 변경이고, 얻는 것이 개념적
정합뿐이므로 채택하지 않는다.

**B안 기각 사유**는 원 제기 그대로다. 판정 3분화·세트 게이트·해시 백필이
runner 에 있어 사실상 복제이며, 이후 수정마다 두 곳을 고쳐야 한다.
seed가 이미 같은 결정을 했다. 전용 파이프라인 복제를 기각하고 `SeedMode`로
같은 `Run`을 탄다(`RunOptions` 주석, 2026-09-01). site도 **같은 파이프라인에서
Upsert 전에만 거르는 옵션**이다. 전용 runner는 seed에서 기각한 이유를
반복한다.

### 2.4 "Opts 오염" 비판의 평가 **[제안]**

site는 처리 대상 선택 조건이다. 이 값에 게이트 우회나 재시도 재무장 의미를
결합하지 않으면, 옵션 하나의 추가가 runner 분리의 근거는 되지 않는다.
일반 PUT에 `--site`를 노출할지는 cmd의 책임이다. SITE v1은 일반 자동 PUT의
`--site`를 **[미결]**로 남겼다. 커밋 5·7에서 그 미결을 열지 않는다.

자료구조를 감추더라도 잘못 배선한 선택 조건이 일반 실행에 적용되는 것을
자동으로 막지는 못한다. 사고 지점은 커밋 5가 아니라 **커밋 7**이다.
`RunOptions` 주석에 다음을 적는다. **제로값 = 필터 없음. 자동 PUT과 자동
resend(커밋 7)는 이 필드를 비운다. 채우는 곳은 수동 CLI(커밋 8)뿐이다.**
필터 키는 `domain.SiteFromName`과 같은 **대문자 4자리**다. 필터 쪽에서
대소문자를 다시 접지 않는다. 정규화·중복 제거·문법 거부는 `ParseSiteList`
(커밋 3)와 CLI(커밋 8)의 몫이다.

다음은 캡슐화 대안이며 필수 확정 사항은 아니다.

```go
// domain 패키지 — ParseSiteList·SiteFromName 과 같은 곳
type SiteSet struct{ m map[string]struct{} }

func NewSiteSet(codes []string) SiteSet  // ParseSiteList 결과를 받는다
func (s SiteSet) Empty() bool            // 제로값 = 전체 선택
func (s SiteSet) Has(site string) bool
```

`Opts.Sites SiteSet`을 선택할 이유는 다음과 같다.

1. **제로값이 곧 "전체 통과"** 이므로 일반 실행은 구조적으로 영향받지 않는다.
   현재 모든 호출부가 이 필드를 쓰지 않으므로 자동으로 안전하다.
2. 대소문자·중복 정규화가 `ParseSiteList`(커밋 3) 한 곳에 남는다. map 을
   노출하는 범위를 줄일 수 있다. 다만 위 `NewSiteSet([]string)` 시그니처만으로는
   소문자·잘못된 코드를 막지 못한다. 정규화 완료 입력이라는 전제를 명시하거나
   기존 domain 검증을 재사용해 오류로 거부해야 하며, 별도 정규화 규칙을 만들지 않는다.
3. map/slice 논쟁이 사라진다. N이 작아 성능 차이는 없고 내부 구현은 감춰진다.

**빈 값 = 전체 통과**는 타당하다. SITE §4의 "site 생략 시 site 검사 자체를 하지
않는다"와 같은 뜻이다. `Empty()` 일 때는 `SiteFromName` 을 **호출하지 않는다** —
이 자리에서 "식별 불가를 새 제외 사유로 만들지 않는다"가 지켜진다.

**교차검증 권고:** 수정 최소 관점에서는 `RunOptions.Sites []string`으로
`ParseSiteList` 결과를 받고, 필요하면 Run 시작 시 내부 집합을 구성하는 것으로
충분하다. 입력 순서는 로그에도 활용할 수 있다. `SiteSet`은 필요가 확인될 때
선택할 대안이며, slice 사용 자체를 결함으로 취급하지 않는다.
직접 집합을 노출한다면 `map[string]bool`의 `{"DBON": false}`처럼
존재와 선택 여부가 갈리는 상태를 피하도록 `map[string]struct{}`를 권한다.
어느 방식을 택해도 실행 도중 호출자가 목록·집합을 변경하지 않는 계약을 둔다.

**운영상 필수 구분:** `--site` 생략만 전체 선택이다. 명시적 빈 값이나
복수 항목 중 하나의 오류는 전체 요청을 거부한다. 파싱 오류를 무시하고
nil/빈 목록을 넘기면 **잘못된 요청이 전체 선택으로 확대**된다.
유효한 site를 지정했지만 일치 파일이 0개여도 전체 선택으로 되돌리지 않는다.
생략과 명시적 빈 인자 구별의 실제 CLI 배선은 커밋 8에서 검증한다.

---

## 3. 커밋 5에서 함께 챙길 것 **[제안]**

쟁점과 별개로 놓치기 쉬운 항목이다.

1. **`SiteFromName`의 오류는 전파한다.** 라우팅이 누락된 카테고리는
   `err != nil` 이다. 이를 "식별 불가"로 접으면 새 카테고리 전체가 조용히
   제외된다(커밋 3의 err/유보 이원 계약).
2. **제외한 파일은 `seen`·`stations`·`observed` 에도 넣지 않는다.** 특히
   `observed` 는 세트 게이트의 관측 목록이다. 세트 키가 관측소를 포함하므로
   다른 site 를 빼도 선택된 site 의 판정은 바뀌지 않는다 — **이것을 테스트로
   고정한다**("site 필터가 선택된 관측소의 세트 완성도 판정을 바꾸지 않는다").
3. **리포트 카운터 두 개**(site 불일치 수, site 식별 불가 수)를
   `CategoryReport` 에 추가한다(SITE §5). 커밋 8의 로그가 이 값을 읽는다.
4. **"선택 밖 파일은 장부 불변"을 실제 DB 로 검증한다.** 신규 파일의
   `common_ledger` 행이 생기지 않는지만 보면 부족하다. 기존 VERIFIED·FAILED·
   PENDING 행의 revision·mtime·content_hash·상태·attempts도 유지되는지 확인한다.
   특히 지문 없는 VERIFIED 행의 자연 백필과 선택 밖 신규/변경 파일의 해시 호출이
   발생하지 않는지 확인한다. 시작 Recover는 별도 책임이며 이 테스트의 필터 범위와
   혼동하지 않는다.
5. **창 계산 테스트에 첫 설치 시나리오를 넣는다.** origin 이 오늘일 때
   D+0 부터 D+ScanDays−1 까지 매일 `Auto` 가 `EmptyBeforeOrigin` 인지 확인한다.
   origin은 최초 날짜 D로 고정하고 today만 진행시킨다. Retention 창이 충분하면
   D+ScanDays에는 origin 당일 1일짜리 창이 열려야 한다. 빈 DB에서 몇 주 뒤
   처음 운영하는 경우도 커밋 4의 조회 결과와 연결해 확인한다.
6. **필터 위치를 고정한다.** 디렉터리·임시파일 제외 → 정규화 → site 필터 →
   `LookupCommon` 배치 구성 → 기존 판정 순이다. 단순히 Upsert 직전에만 넣으면
   그 전에 실행되는 해시나 판정 관측을 막지 못한다.
7. **날짜 경계를 검증한다.** `RetentionDays = ScanDays + 1/+2/+3`,
   origin과 limit의 선후 관계, UTC/KST 날짜 경계, 월·연도 전환을 포함한다.
   수동은 `From=limit`, `To=today`, 하루 범위를 허용하고 각각의 범위 위반을
   거부한다. origin 이전이지만 Retention 안인 수동 요청은 허용한다.
8. **생략·선택 0건·live/dry-run을 비교한다.** site 생략 시 기존 식별 불가
   파일 처리도 유지하고, 선택이 있으면 식별 불가와 불일치를 구분한다.
   동일 입력 상태에서 live/dry-run의 site 선택 결과가 같고 dry-run은 장부를
   바꾸지 않아야 한다. 같은 DB에서 live 후 dry-run을 실행해 후보 상태가 달라지는
   현상과 site 선택의 차이를 혼동하지 않는다.

커밋 5의 빈 창 반환 테스트가 실제 실행 생략을 보증하는 것은 아니다.
**빈 창이면 Runner를 호출하지 않는 배선**, 정상 PUT 이후의 예산 처리와
오류·취소 시 생략은 커밋 7에서 별도로 확인한다.

---

## 4. 결론 요약

| 쟁점 | 결론 | 핵심 조건 |
|---|---|---|
| 1. 창 계산 위치 | **A안 — 독립 패키지** (`internal/scanwindow`) | `Auto` 는 빈 창 사유를 값으로 반환, `ValidateManual` 은 센티널 오류 |
| 2. site 주입 | **A안 — `RunOptions` 필드** | `[]string` 우선 권고, `domain.SiteSet`은 대안. 제로값 = 전체 선택, 명시적 입력 오류는 거부 |

두 결론 모두 "커밋 5는 부품만, 호출 없음, 일반 실행 불변" 범위 안이다.
**창 함수는 put 밖, site 필터만 put 안**이다. 둘 다 put에 넣으면 시간 정책까지
put이 가져가고, 둘 다 cmd에 넣으면 테스트·재사용이 막힌다.

빈 창 부등호·오류 시 전체 선택 확대·선택 밖 장부 변경은 실패 사례로 검증할
항목이다. 패키지 이름·slice/집합·결과 구조 선택은 그와 구분해 한 번 결정한 뒤
새 실패 근거가 없으면 재논의하지 않는다.
