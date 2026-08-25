# SFTPClient Ledger 개념 · 논리 모델

> 대상 산출물: `internal/ledger/schema.sql` (v4)
> 기준 문서: `Go_RINEX_SFTP_통합_프로그램_설계안_Rev1.6.docx` 9절
> 이 문서는 스키마의 **근거**를 남긴다. 스키마 자체는 SQL 파일이 원본이다.
> 두 파일은 항상 함께 갱신한다. 한쪽만 바뀌면 근거를 잃은 스키마가 된다.

### 개정 이력

| 일자 | 내용 |
|---|---|
| 2026-08-25 | 4.7 에 Hatanaka 압축(`.crx`) 실측 사실 추가. `base_name` 이 `.rnx` 계열과 `.crx` 계열로 갈리는 동작을 명시 |
| 2026-08-25 | 4.8 에 `MaxWorkers` 기본값 4 확정 반영. 단일 Ledger Writer 는 정합성 요건이 아니라 MVP 4 최적화임을 명시 |

스키마 자체는 이번 개정에서 변경되지 않았다. 여전히 v4 이다.

---

## 1. 이 문서를 쓰는 이유

Ledger는 "장부"라고 부르지만 엄연히 데이터베이스다. 개념 → 논리 → 물리 순서로 검토해야 하나,
MVP 일정상 물리 설계(DDL)를 먼저 확정하고 개념·논리를 사후에 정리했다.

따라서 이 문서는 **설계 순서를 되짚어 빠진 검토를 채우고, 결정을 기록으로 남기는 것**이 목적이다.
"왜 이렇게 했는가"에 답하지 못하는 결정은 시간이 지나면 사고가 된다.

---

## 2. 개념 모델

### 2.1 엔티티

| 엔티티 | 무엇에 대한 사실인가 | 테이블 |
|---|---|---|
| 파일 | 이 파일이 무엇이고 지금 어떤 상태인가 | `common_ledger` |
| 송신 이력 | 이 파일을 보냈는가, 결과는 무엇인가 | `put_ledger` |
| 수신 이력 | 이 파일을 받았는가, 결과는 무엇인가 | `download_ledger` (MVP 2) |

설계안 9절의 "하나의 사실에는 하나의 주인만 둔다"를 그대로 따른다.

**장부는 판정하지 않는다.** 판정은 `verify` 패키지의 책임이고, 장부는 그 결과를 기록만 한다.
"common이 준비됐다고 판단한다"는 표현은 프로세스 설명이지 개념 모델이 아니다.
이 구분이 흐려지면 판정 로직이 `ledger` 패키지로 새어 들어간다.

**다만 revision 증가와 state 전환은 예외로 `ledger`가 책임진다.**
경계는 판정에 무엇이 필요한가로 나뉜다.

| 판단 | 무엇을 보는가 | 주인 |
|---|---|---|
| 이 파일이 정상인가 (size>0, Grace Time, Category 대조) | 파일과 config | `verify` |
| 이 관측을 어떻게 기록하는가 (revision, state) | 저장된 이전 행 | `ledger` |

후자를 `verify`가 하려면 조회와 쓰기 사이에 트랜잭션이 끊긴다.
Worker Pool 도입 시 두 고루틴이 같은 파일을 보고 revision을 각각 올릴 수 있다.
UPSERT 한 문장이면 그 창이 없다.

### 2.2 관계와 카디널리티

```
파일 1 ────< 송신 이력 0..N
파일 1 ────< 수신 이력 0..N
```

`common_ledger`는 파일 하나당 **한 행**을 유지하고 재관측 시 UPDATE 한다.
`put_ledger`는 `(file_name, revision)`으로 **행을 누적**한다.
따라서 "현재 상태"는 common이, "지나온 기록"은 put이 소유한다.

### 2.3 시간 순서 — PUT과 DOWNLOAD는 대칭이 아니다

개념 모델에서 가장 중요한 발견이다.

```
PUT       로컬 파일 발견 → common 행 생성(부모) → put 행 생성(자식)
DOWNLOAD  원격 파일 발견 → download 행 생성(자식) → 수신 완료 → common 행 생성(부모)
```

PUT은 파일이 **이미 로컬에 존재하는 상태**에서 시작한다. 부모가 먼저 생기므로 자연스럽다.

DOWNLOAD는 파일이 **아직 로컬에 없는 상태**에서 시작한다.
`common_ledger`는 `local_path NOT NULL`이고 "행이 존재한다 = Ingress 검증 통과"라는 규칙이므로,
수신 전 원격 파일은 이 테이블에 들어갈 수 없다.
그런데 중단 복구를 위한 IN_PROGRESS 기록은 수신 **전에** 남겨야 한다.

---

## 3. 결정: FK는 PUT에만 건다

### 3.1 결정 내용

| | `common_ledger` 참조 | FK |
|---|---|---|
| `put_ledger` | 필요 | **건다** (`ON DELETE CASCADE`) |
| `download_ledger` | 불필요 | **걸지 않는다** |

### 3.2 근거 — 후보 선정 방식이 다르다

```
PUT       common_ledger 를 조회해서 "무엇을 보낼지" 고른다   → 남의 장부에 의존
DOWNLOAD  원격 목록을 긁고 자기 장부로 "이미 받았나"만 거른다 → 자기 장부로 완결
```

`put_ledger`의 행은 common에 Ingress를 통과한 사실이 있어야만 의미가 성립한다.
부모 없는 송신 이력은 있을 수 없는 상태이므로 FK가 실제로 오류를 막는다.

`download_ledger`의 행은 common 없이도 완결된 사실이다.
"원격의 이 파일을 이때 받았다"는 그 자체로 참이다.
FK는 "이 행은 저 행 없이는 의미가 없다"는 선언인데, 여기서는 그 선언이 거짓이다.

### 3.3 file_name 은 DOWNLOAD 에서도 식별자로 필요하다

FK를 걸지 않는 것과 식별자가 불필요한 것은 다르다. 혼동하지 않는다.

`download_ledger`에 file_name이 없으면 다음이 전부 불가능하다.

- 재스캔 시 이미 받은 파일을 거르지 못해 **매번 전량 재수신**한다 (설계안 13.2 중복 0건 위반)
- FAILED 항목의 재시도 대상을 특정하지 못한다
- 중단된 IN_PROGRESS 항목의 잔여 `.part`를 찾지 못한다

즉 **식별에는 필요하고, 참조 무결성 강제에는 불필요하다.**

### 3.4 DOWNLOAD 도 common 에 쓰기는 한다

FK가 없다고 해서 두 테이블이 무관한 것은 아니다.

| | common 을 **읽는다** | common 에 **쓴다** |
|---|---|---|
| PUT | 예 (후보 선정) | 예 (state, revision 갱신) |
| DOWNLOAD | 아니오 | 예 (수신 후 `origin='DOWNLOAD'` 등록) |

수신 완료 후 common에 `origin='DOWNLOAD'`로 등록해야 Ping-Pong 방지가 성립한다 (설계안 9.1).
DOWNLOAD는 common을 **읽지 않고 쓰기만 한다.** 이 단방향성이 FK를 걸지 않는 결정과 앞뒤가 맞는다.
쓰는 쪽이 순서를 스스로 통제하므로 참조 무결성을 DB에 위임할 필요가 없다.

---

## 4. 논리 모델 결정사항

### 4.1 식별자 — 파일명 (설계안 9.1 에서 개정)

원안은 `SHA-256( Domain │ Category │ NormalizedName )`의 앞 16바이트였다.
아래 근거로 정규화한 파일명 자체를 키로 사용하도록 변경했다.

**RINEX3 긴 파일명이 이미 유일하다.**

```
SONP 00 KOR _R_ 20260010200 _01H _01S _MS .rnx.gz
  │   │  │   │       │        │    │    └ 데이터 타입 (MO/MN/MS)
  │   │  │   │       │        │    └ 샘플링 간격
  │   │  │   │       │        └ 파일 주기
  │   │  │   │       └ 시작시각 YYYY DDD HHMM
  │   │  │   └ 데이터 소스 (BNC 는 R 대신 S)
  │   │  └ 국가
  │   └ 마커·수신기 번호
  └ 관측소 ID
```

관측소·시각·주기·샘플링·타입 조합이 같으면 실제로 같은 파일이다.
해시는 유일한 입력을 유일한 출력으로 바꿀 뿐이므로 유일성에 기여하지 않고, 정보만 잃는다.

**Domain 을 키에서 제외한 이유** — Ledger DB는 설치 서버 로컬 디스크에 둔다(설계안 9.3).
한 DB 파일에는 항상 Domain 값 하나만 존재하므로 키에 넣어도 구분되는 것이 없다.
반면 운영자가 config의 Domain을 변경하면 전체 식별자가 바뀌어 Ledger가 통째로 고아가 된다.

**이후 config 에서도 제거했다.** 식별자에서 빠진 뒤 남은 용도는 로그 태그뿐이었으나,
인스턴스마다 로그 파일이 이미 분리되어 있어 로그 본문에 기관명을 다시 적을 이유가 없다.
아무 동작도 하지 않는 설정값은 운영자가 바꿔놓고 무언가 달라지기를 기대하게 만든다.
여러 인스턴스의 로그를 한곳에 모아 보는 요구가 생기면 그때 추가한다.
`file_id` 에 들어가지 않으므로 나중에 추가해도 누적 Ledger에 영향이 없다.

**Category 를 키에서 제외한 이유** — 파일명의 `_01D_` / `_01H_` 필드에 이미 인코딩되어 있다.
키에 넣으면 중복 인코딩이며, 설정 실수로 같은 디렉터리가 두 Category에 걸릴 경우
물리적으로 동일한 파일이 서로 다른 식별자 두 개로 등록되어 중복 방지 규칙이 깨진다.

**운영 조회 가독성** — `db failed put` 출력이 `a3f29c1b74e0d582` 대신
`sonp00kor_r_20260010200_01h_01s_ms.rnx.gz`로 나온다. 장애 대응 시 조인 없이 바로 읽힌다.

**`schema_version`과 파일 개정 이력은 다른 것을 센다.**
`schema_v4.sql`의 v1~v4는 스키마 *설계* 개정 이력이고 사람이 읽는 문서다.
`schema_meta.schema_version`은 *배포된 DB*의 마이그레이션 세대이며 프로그램이 판정한다.
v1~v4는 전부 운영 배포 전이라 마이그레이션 대상이 없으므로 세대는 여전히 `1`이다.
운영에 나간 뒤 컬럼을 추가하면 그때 `2`가 된다.

**되돌리는 비용이 가장 큰 항목이다.** 식별자 규칙이 바뀌면 누적 Ledger 전체가 무효가 된다.
`schema_meta.identity_rule = 'FILENAME_V1'`로 기록하여 실행파일이 시작 시 감지하도록 한다.

### 4.2 정규화 규칙

- 디렉터리 경로 제외, 파일명만 사용
- 대소문자는 소문자로 통일
- `.part` 등 임시 접미사 제거
- 압축 확장자(`.gz`, `.Z`)는 **유지**

**소문자 통일** — RINEX3 긴 파일명은 규격상 전부 대문자라 폴딩 충돌은 발생하지 않는다.
Linux 확장(MVP 5) 시 대소문자 차이로 같은 파일이 두 번 등록되는 것을 막는다.
`CHECK (file_name = lower(file_name))`으로 DB가 강제하되,
변환 자체는 `domain` 패키지 함수 하나에서만 수행한다. CHECK는 최후 방어선이지 입구가 아니다.

**압축 확장자 유지** — 서울시·해양측위정보원 양쪽에서 `.rnx`와 `.rnx.gz`가 모두 유입된다.

| | 확장자 유지 | 확장자 제거 |
|---|---|---|
| 같은 데이터가 두 형태로 오면 | 두 번 전송 (중복) | 한 번 전송 |
| 다른 데이터가 우연히 겹치면 | 정상 | 하나가 **영구 누락** |

중복은 로그에 남고 사후 정리가 가능하지만 누락은 아무도 모르게 사라진다.
누락 위험을 피하는 쪽을 택했다. 설계안 9.1의 판단과 동일하다.

### 4.3 값 도메인 — CHECK 제약을 반드시 둔다

`category` `origin` `state` `status` `size` `revision` `attempts` 전부에 CHECK를 건다.

근거: 제약이 없으면 오타 한 글자(`'VERIFED'`)가 들어간 행이
성공 집계에도 실패 재시도 대상에도 잡히지 않아 **파일이 조용히 누락된다.**
설계안 13.2의 "누락 0건" 기준을 깨는 가장 흔한 경로다.

**`status` 표기 통일** — 설계안 8.1·13.2는 `SUCCESS`, 9.3은 `VERIFIED`로 엇갈린다.
상태 전이를 명시적으로 정의한 9.3을 따라 `VERIFIED`로 통일한다.
두 이름이 코드에 섞이지 않도록 `domain` 패키지 상수로만 참조한다.

**`origin` 축소** — 설계안 9.1은 `RECEIVER / LOCAL / DOWNLOAD` 세 값을 예시하나,
`LOCAL / DOWNLOAD` 두 값으로 통합했다.
Scanner는 디렉터리에 놓인 파일만 볼 뿐 누가 썼는지 판별할 수 없어
RECEIVER와 LOCAL을 구분할 근거가 없고, 동작 차이도 없다(둘 다 PUT 대상).
검증 불가능한 값은 config에 적힌 대로 받아 적게 되고, 틀려도 아무도 모른다.
되돌릴 경우 CHECK에 `'RECEIVER'`를 추가하면 되며,
그때 그 값은 config 선언에 의존하는 미검증 메타데이터임을 전제해야 한다.

### 4.4 NULL 정책

`common_ledger`에는 Ingress 검증을 통과한 파일만 행이 생긴다.
미통과 파일(size=0, Grace Time 미충족)은 행을 만들지 않고 다음 Scan에서 재판정한다.

**0바이트 파일은 현장에서 흔하게 관측된다.** 수집 프로그램이 파일을 먼저 만들고
내용을 이어 쓰므로 `0 byte → 수 KB → 수 MB`로 증가한다. 따라서 Scanner는 이런 파일도
발견해야 하고 verify는 다음 Scan에서 계속 재판정해야 한다.

다만 `size > 0`만으로는 부족하다. 0바이트를 지나 일부만 쓰인 시점에 Scan하면
통과해 버린다. Grace Time이 그런 파일의 상당수를 걸러낸다.

**그러나 Grace Time은 정확성 장치가 아니라 헛전송을 줄이는 장치이다.**
현장의 파일 생성 방식이 append 형이라는 사실이 확인되었으므로(BNC, 0바이트 선생성 후 증가),
쓰기 사이의 공백이 Grace Time보다 길면 미완성 파일이 그대로 통과한다.
그 공백의 최대치는 알 수 없고, 알 수 있다 해도 그만큼 값을 키우면 정상 파일까지 지연된다.

**정확성의 주인은 `revision`이다.** 미완성 파일이 통과하더라도 완성 시점에 size가 달라져
`revision`이 올라가고 재전송된다. Grace Time을 0으로 두어도 최종 데이터는 정확하며
전송 횟수만 늘어난다. 따라서 이 값은 "완벽히 걸러내는 값"이 아니라
"헛전송과 지연의 균형점"으로 정한다.

`state`는 `READY` / `CHANGED` 두 값만 가지며 `ingress_verified_at`은 NOT NULL이다.

**테이블에 존재한다는 것 자체가 검증 통과를 뜻한다.** 이 불변식을 깨지 않는다.

`put_ledger`의 측정값(`local_size`, `remote_size`)은 NULL을 허용한다.
"아직 측정하지 않음"과 "0바이트"를 구분해야 하기 때문이다.

### 4.5 상태 컬럼과 후보 선정 — `state`는 필터가 아니다

`state`는 `READY` / `CHANGED` 두 값을 갖지만 **둘 다 전송 대상**이다.
따라서 조회 조건에 넣어도 거르는 것이 없다. 후보 판정의 주체는 `state`가 아니라
`put_ledger`와의 **revision 매칭**이다.

```sql
SELECT c.file_name, c.revision, c.local_path, c.size
  FROM common_ledger c
  LEFT JOIN put_ledger p
    ON  p.file_name = c.file_name
    AND p.revision  = c.revision
 WHERE c.category = ?
   AND c.origin   = 'LOCAL'
   AND (p.status IS NULL OR p.status = 'FAILED');
```

| 상황 | put 행 | 판정 |
|---|---|---|
| 한 번도 보낸 적 없음 | 없음 | 후보 |
| 보냈고 검증 완료 | `VERIFIED` | 제외 |
| 파일이 갱신되어 revision 상승 | 해당 revision 행 없음 | 후보 |
| 전송 실패 | `FAILED` | 재시도 후보 |

이 구조가 "하나의 사실에 하나의 주인"에 부합한다.
"보냈는가"는 `put_ledger`의 사실이므로 common이 그것을 중복 보유하지 않는다.
`state`를 후보 조건에 넣으면 같은 사실이 두 곳에 생기고, 어긋나는 순간이 곧 버그다.

**그럼에도 `state`를 두는 이유** — 운영 가시성이다.
결측 보정이나 0바이트 선생성 후 채워지는 현상이 현장에서 얼마나 발생하는지는
아직 관측된 바 없고, 이 컬럼이 그것을 답한다. `base_name`과 같은 성격의 관측 수단이다.

```sql
SELECT COUNT(*) FROM common_ledger WHERE state='CHANGED';
```

**CHANGED 대기를 채택하지 않은 근거** — 두 스캔의 size·mtime을 비교해 안정 여부를
판정하는 방안(Stability Check)을 검토했으나 기각했다.

기각 사유는 **효과가 Grace Time과 동일하기 때문**이다.
실행 주기가 1시간이므로 "두 스캔 연속 동일"은 "1시간 동안 쓰기가 없었다"와 같은 뜻이고,
이는 `GraceTime = 1h`가 판정하는 것과 다르지 않다.
실패 방식까지 같아서, 관측이 90분 끊긴 구간에 스캔이 걸리면 두 방식 모두
미완성 파일을 통과시킨다. 상태값과 판정 단계를 늘려서 얻는 것이 없다.

애초에 미완성 파일이 한 번 전송되는 것은 정확성 문제가 아니라 대역폭 비용이다.
완성되면 size가 달라져 `revision`이 올라가고 재전송되므로 누락은 발생하지 않는다(4.4).

**다만 초안의 전제 하나는 사실이 아니었음을 기록한다.**
초안은 "파일이 1시간 넘게 쓰이는 일은 RINEX Hourly에서 발생하지 않는다"를 근거로 삼았다.
현장의 생성 방식이 append 형이고 0바이트에서 채워지는 사례가 실제로 확인되었으므로
이 전제는 성립하지 않는다. Daily 파일은 하루 종일 쓰인다.
결론은 위 사유로 유지되지만 근거는 다르다.

헛전송이 실제로 얼마나 발생하는지는 `state='CHANGED'` 누적량으로 관측한다.

**인덱스 컬럼 순서** — `(category, origin, state)`.
등가 비교인 `category`·`origin`을 앞에 두고 선택도가 낮은 `state`를 뒤에 둔다.
`state`를 중간에 두면 그 뒤 컬럼이 인덱스로 좁혀지지 않는다.

### 4.6 검증 시각은 두 종류다

| 컬럼 | 무엇을 검증했는가 | 설계안 |
|---|---|---|
| `common_ledger.ingress_verified_at` | 파일이 정상적으로 완성되었는가 | 7 |
| `put_ledger.transfer_verified_at` | 목적지에 제대로 도착했는가 | 8.1 |

초안에서는 양쪽 모두 `verified_at`이었다. 같은 이름이지만 다른 개념이므로
조인 조회에서 어느 쪽 시각인지 판별할 수 없어 접두어로 분리했다.

`put_ledger`는 `sent_at`과 `transfer_verified_at`을 별도로 갖는다.
"보냈지만 아직 검증되지 않은" 구간을 표현하기 위함이며,
전송 함수가 오류 없이 끝난 것과 목적지 파일이 확인된 것은 다른 사실이다(설계안 8.1).

### 4.7 의도적 비정규화 — 두 곳

엄밀히는 정규화 위반이다. 알고 한 것임을 기록한다.

```
file_name → base_name    (압축 확장자만 제거하면 유도 가능)
file_name → category     (RINEX2/3 은 파일명 형식, D/H 는 01D/01H 필드)
```

**`base_name`** — 같은 관측 데이터가 `.rnx`와 `.rnx.gz` 두 형태로 유입되는지
운영 중에 확인하기 위한 관측 수단이다. 식별자가 아니므로 UNIQUE를 걸지 않는다.

```sql
SELECT base_name, COUNT(*) FROM common_ledger
 GROUP BY base_name HAVING COUNT(*) > 1;
```

**Hatanaka 압축(`.crx`)** — 설계안에 언급이 없으나 실제 디렉터리에 존재한다.
한 관측 데이터가 네 형태로 함께 놓여 있는 것을 확인했다.

```
GAGE00KOR_R_20261820000_01D_30S_MO.rnx      16,514KB
GAGE00KOR_R_20261820000_01D_30S_MO.rnx.gz
GAGE00KOR_R_20261820000_01D_30S_MO.crx       5,211KB
GAGE00KOR_R_20261820000_01D_30S_MO.crx.gz    1,868KB
```

`BaseName`은 `.gz` / `.Z` 만 제거하므로 두 그룹으로 갈린다.

```
...mo.rnx  ← .rnx, .rnx.gz
...mo.crx  ← .crx, .crx.gz
```

`.crx`는 단순 압축이 아니라 관측 데이터의 포맷 변환이므로 현재는 이 동작을 유지한다.
넷을 한 그룹으로 묶고 싶어지면 그때 규칙을 넓힌다.
`base_name`은 식별자가 아니라 관측 수단이므로 되돌리는 비용이 없다. (7절 D등급)

**네 형태는 `file_name`이 서로 다르므로 각각 별개의 행이 되고 각각 전송된다.**
확장자 필터를 두지 않기로 했기 때문이다. 전송량 영향은 지침 9.7에 기록한다.

**`category`** — Scanner가 파싱하지 않고 config의 `[PUT.<CATEGORY>]` 섹션에서 전달받는다.
`verify` 단계에서 config가 지정한 값과 파일명이 함의하는 주기를 대조하여
불일치 시 Ingress를 거부한다. **Config 오기입 탐지 장치**이므로 중복 저장의 값이 있다.

### 4.8 접속 시 필수 PRAGMA

```sql
PRAGMA foreign_keys = ON;      -- 기본값 OFF. 켜지 않으면 FK 선언이 장식으로 남는다
PRAGMA journal_mode = WAL;     -- 설계안 9.3
PRAGMA synchronous  = NORMAL;  -- 설계안 9.3
PRAGMA busy_timeout = 5000;    -- 설계안 9.3
```

**`Exec`로 한 번 실행하지 않고 DSN 파라미터로 지정한다.**
PRAGMA 중 상당수는 connection 단위 설정이다. `database/sql`은 커넥션 풀을 쓰므로
한 번 실행하고 끝내면 풀이 새 connection을 만들 때 누락된다.

```
file:///C:/.../rinex_ledger.db?_foreign_keys=1&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000
```

**지정한 뒤 되읽어 확인한다.** DSN 파라미터의 오타는 조용히 무시되므로,
확인하지 않으면 `foreign_keys`가 꺼진 채로 운영에 들어가 고아 행이 쌓인다.

PRAGMA의 반환 타입은 항목마다 다르다. `journal_mode`만 TEXT이고 나머지는 INTEGER이므로
전부 문자열로 Scan하면 드라이버가 돌려주는 `driver.Value` 타입에 의존하게 된다.
`any`로 받아 정규화한 뒤 비교한다.

| PRAGMA | 반환 타입 | 값 |
|---|---|---|
| `foreign_keys` | INTEGER | `1` |
| `journal_mode` | TEXT | `"wal"` |
| `synchronous` | INTEGER | `1` (NORMAL) |
| `busy_timeout` | INTEGER | `5000` |

---

### 4.9 UPSERT 동작 — 세 갈래로 갈린다

`common_ledger` 한 행의 등록·갱신은 `INSERT ... ON CONFLICT DO UPDATE ... WHERE` +
`RETURNING revision` 한 문장으로 처리한다.

| 상황 | `ON CONFLICT` WHERE | RETURNING | 결과 |
|---|---|---|---|
| 행 없음 | — (INSERT) | `1` | Inserted |
| size·mtime 동일 | false | **없음** | Unchanged |
| size 또는 mtime 다름 | true | `n+1` | Updated |

**WHERE가 거짓이면 아무 행도 변경되지 않아 `RETURNING`이 결과를 내지 않고,
Go에서는 `sql.ErrNoRows`가 된다.** Unchanged 판정 전체가 이 동작에 걸려 있으므로
실제 SQLite로 검증했으며, `common_test.go`가 회귀를 막는다.

`RETURNING`은 SQLite 3.35 이상이 필요하다. 사용 중인 엔진은 시스템에 설치된
`sqlite3` 실행파일이 아니라 `modernc.org/sqlite`에 내장된 것이므로,
드라이버 버전이 곧 SQLite 버전을 결정한다.

정상 운영에서 압도적으로 흔한 결과는 **Unchanged**다.
Scan은 같은 파일을 매 주기 다시 보지만 대부분 아무것도 바뀌지 않는다.

---

## 5. 스키마가 강제하지 못하는 규약

DDL로 표현할 수 없어 코드 규율에 의존하는 항목이다. 코드 리뷰에서 확인한다.

**① `remote_path` / `part_path` 는 IN_PROGRESS 전환 트랜잭션 안에서 기록한다.**
전송 완료 후에 기록하면 중단된 항목이 NULL로 남아
재시작 시 잔여 `.part` 삭제 대상을 찾지 못한다 (설계안 9.3, 13.2).

**② 파일명 정규화는 `domain` 패키지 함수 하나에서만 수행한다.**
CHECK가 위반을 잡아주지만 잡히는 시점이 INSERT라 이미 스캔이 끝난 뒤다.

**③ 상태 문자열은 `domain` 패키지 상수로만 참조한다.**
`SUCCESS` / `VERIFIED` 혼용을 방지한다.

**④ `origin`과 `first_seen`은 UPDATE 대상에서 제외한다.**
둘 다 최초 관측 시점의 사실이며 재관측으로 달라지지 않는다.
특히 `origin`을 갱신하면 DOWNLOAD로 받은 파일을 Scanner가 나중에 발견해
`LOCAL`로 덮어쓰고, PUT 후보 제외가 풀려 Ping-Pong이 발생한다.

반대로 `local_path`와 `ingress_verified_at`은 **갱신한다.** 최신 관측값이 유용하다.
`category`도 갱신 대상이지만, config 오기입 시 조용히 덮어쓰는 경로가 열려 있다(6절 미결 5).

**⑤ `put_ledger.revision`은 `common_ledger`에서 읽은 값을 그대로 쓴다.**
FK는 `file_name`만 참조하므로 revision 정합성은 DB가 강제하지 못한다.
부모가 `revision=1`인데 자식에 `revision=99`를 넣어도 삽입된다(검증 완료).
4.5의 후보 선정 쿼리가 common에서 읽은 값을 전달하는 한 발생하지 않으나,
revision 값을 직접 구성하거나 증가시키는 코드 경로를 만들지 않는다.

**⑥ 트랜잭션 안에서는 `tx`만 쓰고 `db.conn`을 건드리지 않는다.**
`ledger.Open`은 `SetMaxOpenConns(1)`로 커넥션을 하나로 제한한다.
`BeginTx`가 그 하나를 점유한 상태에서 `db.conn`으로 다른 쿼리를 하면
새 커넥션을 기다리는데 그 커넥션은 트랜잭션이 끝나야 반납되므로 **영구 데드락**이다.

`busy_timeout=5000`은 여기서 걸리지 않는다. 그것은 SQLite 엔진의 락 대기이고
이 정지는 Go 커넥션 풀 레벨이라 그 앞단에서 막힌다.
`ctx`에 timeout이 없으면 프로세스가 그대로 멈춘다.

MVP 1의 `MaxWorkers` 기본값은 **4**이며 전송은 처음부터 병렬로 수행한다.
`SetMaxOpenConns(1)` 때문에 Ledger 쓰기는 자동으로 직렬화되며 이는 정상 동작이다.
정합성 관점에서 병렬도 자체는 안전하고, **위험은 트랜잭션 안의 중첩 쿼리 하나뿐이다.**

단일 Ledger Writer 고루틴 + 배치 커밋(설계안 9.3)은 이 직렬화 구간이
실제로 병목이 되는지 측정한 뒤 도입한다. 정합성 요건이 아니라 처리량 최적화이다.
MVP 4 Benchmark 범위이다.

Scan은 병렬화 대상이 아니다. 디렉터리를 순차로 훑으며,
따라서 `UpsertCommon` 호출도 Scan 단계에서는 순차이다.

---

## 6. 미결 항목

| # | 항목 | 결정 시점 |
|---|---|---|
| 1 | 시도별 상세 이력을 Ledger가 아닌 Log에 맡기는 것이 맞는가 | MVP 1 종료 |
| 2 | 과거 revision 의 size/mtime 소실을 허용하는가 | MVP 1 종료 |
| 3 | `download_ledger` 의 최종 구조 및 `common_ledger` 등록 시점 | MVP 2 착수 전 |
| 4 | `origin` 에 `RECEIVER` 재도입 여부 | 현장 요구 발생 시 |
| 5 | config 오기입으로 `category` 가 덮어써지는 경로를 막을 것인가 | MVP 1 종료 |
| 6 | RINEX2 파일명의 주기 판정 규칙 | 실제 유입 파일 확인 후 |
| 7 | `.part` 외 임시 접미사(`.tmp` 등)가 유입되는가 | MVP 1 운영 로그 확인 후 |
| 8 | Grace Time 실측값 | `state='CHANGED'` 누적량 관측 후 |
| 9 | 원격 디렉터리 사전 생성 실패 시의 상태 처리 | MVP 1 구현 중 |
| 10 | Hourly Scan 이 현재 UTC 시각 디렉터리를 제외할 것인가 | MVP 1 운영 로그 확인 후 |

### 6.1 항목 1 상세

현재 `put_ledger` PK는 `(file_name, revision)`이므로 **revision당 한 행**이다.
3회 실패하면 `attempts=3`, `error='timeout'` 한 줄로 뭉개진다.
1차 실패가 언제 무슨 이유였는지는 답할 수 없다.

설계안 9절은 PUT_LEDGER를 "송신 시도·성공·실패·검증 **이력**"이라 했으나
현재 구조는 이력이 아니라 **집계**다.

다만 설계안 14절이 "실패·Retry는 상세 원인 기록"을 **로그**의 책임으로 두고 있으므로,
시도별 상세는 로그가 주인이고 Ledger는 집계만 갖는 것이 "하나의 사실에 하나의 주인"에 부합한다.
**틀린 구조는 아니나 결정된 적이 없다.** MVP 1 운영 로그를 보고 확정한다.

### 6.2 항목 2 상세

`common_ledger`는 UPDATE로 갱신되므로 revision 3이 되면 revision 1의 size·mtime은 사라진다.
전송 시도가 있었던 revision은 `put_ledger.local_size`에 흔적이 남지만,
시도가 없었던 revision은 완전히 소실된다.
현재로서는 운영상 문제 시나리오가 확인되지 않아 허용하되, 결정으로 기록한다.

---

### 6.3 항목 6·7 상세 — 관측으로 답한다

둘 다 지금 추측해서 규칙을 채우면 검증되지 않은 코드가 남는다. 운영 로그로 답한다.

**RINEX2 주기 판정** — 짧은 파일명의 8번째 세션 코드로 구분한다고 알려져 있으나
(`0`=Daily, `a`~`x`=Hourly) 실제 유입 파일로 확인한 바가 없다.
확인 전까지 `Category.MatchesName`은 `CategoryMatchUnknown`을 돌려주고 통과시킨다.
틀린 규칙으로 정상 파일을 거부하면 누락되고, 누락은 아무도 모르게 사라진다.

**임시 접미사** — 현재 `.part`만 `IsPartFile`이 거른다. 다른 접미사가 쓰이더라도
mtime이 최근이므로 Grace Time에 걸린다. `IsPartFile`은 불필요한 후보를 미리
쳐내는 최적화지 유일한 방어선이 아니다.
Scanner에 예상 밖 확장자를 기록하는 로그를 두어 하루치 운영으로 확인한다.

---

## 7. 변경 비용 등급

압축된 설계의 위험을 항목별로 분리해 둔다.

| 등급 | 항목 | 비용 |
|---|---|---|
| 낮음 | 컬럼 추가 | `ALTER TABLE` 한 줄 |
| 낮음 | 테이블 추가 (`download_ledger`) | `CREATE TABLE` 한 번 |
| 중간 | CHECK 값 추가/변경 | 테이블 재작성, 데이터는 유지 |
| **높음** | **식별자 규칙 변경** | **누적 Ledger 전체 무효, 전량 재전송** |

되돌릴 수 없는 것은 마지막 하나뿐이며,
이 항목에 검토 시간의 대부분을 사용했고 실제 운영 파일명으로 검증했다.

---

## 8. 설계안 Rev1.6 대비 변경 요약

| 항목 | 설계안 | 본 스키마 | 절 |
|---|---|---|---|
| 식별자 | SHA-256 해시 앞 16바이트 | 정규화 파일명 | 4.1 |
| 컬럼명 | `file_id` | `file_name` | 4.1 |
| Domain | 식별자 구성요소 | 사용하지 않음. config 에서도 제거 | 4.1 |
| Category | 식별자 구성요소 | 일반 컬럼 + 오기입 대조 | 4.1, 4.7 |
| `origin` | RECEIVER / LOCAL / DOWNLOAD | LOCAL / DOWNLOAD | 4.3 |
| 전송 상태 | SUCCESS 와 VERIFIED 혼용 | VERIFIED 로 통일 | 4.3 |
| DOWNLOAD FK | 명시 없음 | 걸지 않음 | 3 |
| `part_path` | 없음 | 추가 | 5-① |
| `base_name` | 없음 | 추가 | 4.7 |
| `schema_meta` | 없음 | 추가 | 4.1 |
| Category 대조 | 없음 | 3-값(OK/Mismatch/Unknown) | 4.7, 6.3 |
| PRAGMA 적용 | 명시 없음 | DSN 파라미터 + 되읽기 확인 | 4.8 |
| `verified_at` | 양쪽 동명 | `ingress_` / `transfer_` 로 분리 | 4.6 |
| `state` | 전송 후보 필터 | 운영 관측용. 후보는 revision 매칭 | 4.5 |
| Grace Time | 작성 중 파일 배제 장치 | 헛전송 감소 장치. 정확성은 revision | 4.4 |
| `ScanRecentDays` | 기본 2일 | **7일**. 기존 운영 스크립트 실측 | — |
| SFTP 접속 정보 | `[SFTP]` 최상위 | `[PUT.SFTP]`. 방향별로 서버가 다름 | — |
| 원격 디렉터리 생성 | 명시 없음 | PUT 전 재귀 `mkdir` 필수 | — |

용어 대응: 설계안 9절의 `file_id` ↔ 본 스키마의 `file_name`.
설계 문서를 참조할 때는 두 이름을 같은 것으로 읽는다.
