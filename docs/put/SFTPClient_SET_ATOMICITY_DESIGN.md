# SFTPClient 세트 원자성 (Set Completeness Gate) — 단일 주제 설계서

> 상태: **확정·구현 완료** (2026-09-10, 커밋 5개)
> 운영 범위 갱신: **2026-09-15** — 게이트 활성화 운영 절차와 미완성 세트 보류 리포트는 현재 개발 산출물에서 제외
> 기준 문서: `SFTPClient_PROJECT_GUIDELINES.md` 12절 (통합본 — 상충 시 그쪽이 우선)
> 본 문서는 세트 원자성 주제만 발췌·요약한 단일 주제 문서다.
> 12절을 개정하면 본 문서도 함께 갱신한다.
>
> (2026-09-09 자 동명 초안은 폐기 — kind 표기 `mo.crx` 등이 확정과 상충)

## 1. 무엇인가

**전송 시작 원자성**: 필수 파일 종류(kind)가 모두 존재하는 세트만
전송 후보로 올린다. 도봉 DOY 250(O·S만 도착, G·L·N 영구 누락 →
목적지 QC 결손)이 동기다.

하지 않는 것: 원격 롤백·일괄 삭제·HELD 상태. 전송 시작 후 부분
실패는 기존 FAILED→Retry 경로가 수렴시킨다.

## 2. 확정 의미론 (2026-09-10)

1. **미완성 세트는 선택 종 포함 전체 보류.** S만 새면 목적지에
   부분 세트가 생긴다 — 게이트가 막으려는 바로 그 상태.
   (§7 "S 개별 전송"은 완성 세트에서의 동작·판정 불참의 의미)
2. **세트 소속 유보(파싱 불가)는 개별 통과 + 관측**
   (`SetUnparsed`, `[SET][WARN]`). YONS060.20M 같은 표준 밖
   실데이터가 실재 — 보류안은 게이트 ON 시 영구 미전송이라 기각.
3. **완성도의 우주 = 관측(observed), 후보 아님.** Ingress 통과
   실재 파일 전체(VERIFIED 제외분, DOWNLOAD origin — 변경 시
   재검증 통과분만). 거부 파일(0바이트·Grace 미경과)은 불포함.
4. 완성/미완성은 영구 상태가 아니라 **매 스캔 재계산**.
5. FAILED 재시도 대기열에도 동일 필터.
6. `resend`(운영자 명시 지시)는 게이트를 우회한다.

## 3. 파일명 파생 (domain.SetKeyKind — 유일한 주인)

```
RINEX2   DBON2500.26G.gz → set_key=dbon2500.26 (세션 문자 포함), kind=g
RINEX3/4 DBON00KOR_R_20262500000_01D[_30S]_MO.crx.gz
         → set_key=앞 4필드 재조립, kind=mo   (MN은 레이트 필드 생략)
```

- 판정은 필드 "수"가 아니라 **형태**(관측소 9자·소스 1자·타임스탬프
  11자리·주기 숫자2+영문1). 수만 보면 임의 이름이 유령 세트를 만든다.
- kind 는 데이터 종류만 — 표현 형식(.rnx/.crx)·압축(.gz) 제외.
- 반환 계약 3갈래: err(라우팅 공백 — 반드시 잡음) /
  ok=false(유보 — 정상 범위) / ok=true. 둘을 합치지 않는다.
- 버전의 출처는 닫힌 열거형 `Category.RinexVersion()` 하나
  (§5 3차 확정 — ini 명시 키·이름 문자열 파생 모두 기각.
  전수 테스트 + default err 가 대조 검증을 대체).

## 4. config — [SET.RINEXx] RequiredKinds (버전 단위, 기본 OFF)

```ini
[SET.RINEX2]
RequiredKinds = false        ; OFF (기본) | G,L,N,O (ON)
```

- false=OFF / CSV=ON / 빈 값·true·빈 항목·중복·영문 외 문자 = 시작 오류
  (영문 제한: 파서가 영문만 산출 → 그 밖 kind는 영원히 미완성 게이트)
- Enabled 키 없음(모순 설정을 문법에서 차단), OptionalKinds 없음
- 섹션 부재 = OFF(실행파일 교체만으로 배포).
  **섹션이 있으면 키는 필수**(절반짜리 설정의 조용한 OFF 방지)
- 전역 하드코딩 금지 — 기관마다 제공 종이 다르다
  (서울시 G·L·N·O·S / 지리원 z / 위성센터 O 단독)

## 5. 파이프라인 위치와 경계 절단

```
Scan → Lookup → Verify → Upsert(set_key/kind 파생 기록)
     → [Set Gate] → 정렬 → MaxFilesPerRun(세트 경계 절단) → PENDING → PUT
```

- 보류는 put_ledger에 아무 기록도 만들지 않는다(PENDING 미생성).
- 절단선 양쪽에 걸친 세트는 잔여 멤버를 통째로 다음 회차 이월
  (집합 방식 — 정렬 후 비인접 대응, setID=category+set_key,
  RINEX3/4 동일 파일명 대응). 빠진 만큼 채워 넣지 않는다.

## 6. Ledger — 사실만 기록

- `common_ledger.set_key/kind` — **NOT NULL DEFAULT '' + lower CHECK**
  (nullable 2회 기각). '' = 유보. 판정 결과(COMPLETE/HELD)는 비저장.
- 파생은 `UpsertCommon` **내부**(호출자가 잊을 수 없는 구조) —
  게이트 ON/OFF 무관하게 기록. "''=유보" 불변식 = 백필 + Upsert 파생.
- v4→v5는 **Open 자동 단일 스텝**(단일 트랜잭션: ALTER+백필+버전,
  tx 전용·커서 닫고 UPDATE). 서브커맨드 기각 — 1인 현장 이식
  배포에서 수동 단계는 "빠뜨리면 조용히 멎는 단계".
  완료 로그: `[LEDGER] schema v4 -> v5 migrated: rows/backfilled/unresolved`

## 7. 관측 로그

```
[SET] category=RINEX2_DAILY held set=dbon2500.26 have=o,s missing=g,l,n
[SET][WARN] category=... unparsed=N (세트 소속 유보 — 개별 통과) examples=...
[SET] MaxFilesPerRun 절단선을 세트 경계로 조정: moved=N
[PUT]   set_gate: held=N unparsed=N        (SetGate ON 카테고리만)
```

## 8. 운영 적용 범위 (2026-09-15 갱신)

세트 게이트의 기술 구현과 `RequiredKinds` 설정 형식은 유지한다. 다만 다음 항목은
현재 개발 산출물에서 제외한다.

- 게이트 활성화 운영 절차 작성·적용
- 미완성 세트 보류 리포트 구현 — 대표 확인 결과 보안 취약점 우려가 있음
- RINEX3 첫 운영 프로파일 작성·현장 적용

따라서 이 문서는 게이트 활성화를 지시하거나 일일 리포트를 활성화 전제조건으로
요구하지 않는다. 실제 기관에서 게이트를 켤지는 별도의 운영 결정이며, 구체적인
운영 요구가 확정되기 전에는 절차나 리포트를 선제 설계·구현하지 않는다.

`resend`가 세트 게이트를 우회한다는 §2의 의미론은 유지한다. 다만 `resend` 명령
자체는 MVP2 남은 구현 항목이다.

## 9. 기각 이력 (재론 방지)

| 기각안 | 사유 |
|---|---|
| kind에 표현 형식 포함(mo.crx) | §11 — rnx/crx는 형식, 종이 아님 |
| Enabled 키 / OptionalKinds | 모순 설정 가능 / 게이트는 필수 종만 알면 됨 |
| RinexVersion ini 키·이름 파생 | 닫힌 열거형과 중복·순환 (§5 3차) |
| 미완성 세트에서 선택 종 개별 통과 | 목적지 부분 세트 = 막으려는 상태 |
| 유보 파일 보류 | 표준 밖 실데이터 영구 미전송 |
| 필드 수만 검사하는 긴 이름 파서 | 유령 세트 |
| nullable set_key/kind | 전 컬럼 NOT NULL 규약·파서 반환값 동형 |
| migrate 서브커맨드 / DB 재생성 | 수동 단계 리스크 / 이력 소실→전량 재전송 |
| 절단 후 빈자리 채움 | 상한은 "이하" 보장, 비결정성 |
| HELD 상태·판정 결과 저장 | Ledger는 사실만, 완성도는 재계산 |
