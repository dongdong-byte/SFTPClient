# SFTPClient — Unit 5 결정 기록: `.filepart` 임시 파일 제외

- **작성일:** 2026-09-18
- **선행:** FOLLOWUP_PLAN v2 §6.3·§6.5, 인시던트 §7-5
- **성격:** 소형 유닛. 코드 변경은 `domain.IsPartFile` 확장 하나다. 무게가
  있는 결정은 §2의 "IdentityRule 을 올리지 않는다" 하나뿐이다.

---

## 1. 동작 계약

파일을 만드는 쪽이 "전송 완료"를 표시할 때까지 기다린다. 기다린다는 것은
파일을 붙잡고 대기하는 것이 아니라 **이번 스캔에서 건너뛰고 다음 실행에서
다시 보는 것**이다. (상류가 WinSCP 로 감시 폴더에 넣는 구조를 가정한다)

1. **전송 중** — 목적지에 `data.rnx.gz.filepart` 로 쌓인다. SFTPClient 는
   `.filepart` 라는 이유로 후보에서 제외한다. **크기가 크거나 mtime 이
   오래됐다는 이유로 완성이라 판단하지 않는다** — 끊긴 미완성 파일도 그 조건을
   만족한다.
2. **전송이 끊김** — WinSCP 는 이어 전송을 위해 `.filepart` 를 남긴다.
   SFTPClient 는 삭제·이름 변경을 하지 않는다. 그동안 다른 파일은 정상 처리한다.
3. **전송 완료** — WinSCP 가 `.filepart` → `data.rnx.gz` 로 이름을 바꾼다.
   이 이름 변경이 두 프로그램 사이의 완료 신호다.
4. **다음 스캔** — 임시 접미사가 없으므로 기존 절차(Grace·Zero·Category
   Ingress → 장부 대조 → 해시 판정 → 세트 게이트)를 그대로 거친다. 최종
   이름이 됐다고 무조건 보내는 것이 아니라 기존 검증을 받을 자격이 생긴 것이다.

송신 단계에서 이미 미완성임을 아는 파일은 보내지 않는다. QC 가 나중에
걸러주더라도 불완전 데이터 전송 비용과 QC 적재를 만들지 않기 위해서다.

## 2. 구현 결정

**`IsPartFile` 만 확장한다. `NormalizeName`·`IdentityRule`(`FILENAME_V1`)은
그대로 둔다.**

- `internal/domain/filename.go`: `tempSuffixes = {".part", ".filepart"}`.
  `IsPartFile` 이 목록을 대소문자 무시로 대조한다. `NormalizeName` 은 계속
  `.part` 만 제거한다(`partSuffix`).
- `verify`·`runner` 는 `IsPartFile` 을 호출만 하므로 변경 없음. 제외 집계는
  기존 `SkippedPart` / `rejected: part=` 에 합산된다 — 로그 키 불변.

근거:

1. 임시 파일은 `NormalizeName` 에 도달하기 **전에** Ingress 에서 제외된다
   (`runner.visitBatch` 사전 제외, `verify.Verify` 2차). 정상 흐름에서
   `NormalizeName` 의 `.filepart` 처리는 쓰이지 않으므로 식별자 의미가 바뀌지
   않는다.
2. `IdentityRule` 을 올리면 운영 3개 기관 DB 가 모두
   `ErrIdentityRuleMismatch` 로 시작을 거부한다. identity 마이그레이션을 새로
   만들어야 하는, 프로젝트에서 가장 비싼 변경(CONCEPT 4.1)이다.
3. `NormalizeName` 까지 확장하면 이미 장부에 등록된 `*.filepart` 행이 이름
   가드(`NormalizeName(x) != x`, `LookupCommon`·`LookupPut` 등)에 걸린다.

**기각안:**

- `[기각]` `tempSuffixes` 확장 + `IdentityRule` 상향 (구 filename.go 주석의
  방침) — 위 근거 2·3. 주석을 이번 결정으로 정정했다.
- `[기각]` 확장자 화이트리스트 — 모르는 임시 접미사를 일괄 차단하지만 RINEX2
  확장자가 다양해(`.26o`, `.26d.Z` 등) 정상 파일을 조용히 누락시킬 위험이
  있다. 누락 > 헛전송 원칙에 반한다.

## 3. 범위 밖·한계·미결

- **기존 장부의 `*.filepart` 행** — 배포 후 스캔에서 빠져 다시 후보가 되지
  않는다. PENDING/FAILED 는 고아로 남고, IN_PROGRESS 는 `Recover` 가 처리한다
  (`FailPut` 은 이름을 검증하지 않음). 행·원격 파일 정리는 현장 확인 후 별도
  판단 — FOLLOWUP_PLAN §3.1 체크리스트 2번.
- **WinSCP 임시 이름 전송의 적용 기준** `[미확인]` — WinSCP 는 기본 설정에서
  일정 크기(100KB로 알려짐) 이상 파일에만 `.filepart` 를 쓰는 것으로 알려져
  있다. 그보다 작은 파일은 처음부터 최종 이름으로 쓰일 수 있고, 이 경우 유닛 5는
  막지 못한다. 기존 Grace(쓰는 중)와 revision(나중에 완성되어 size 변경)이
  담당한다. 현장 WinSCP 버전·설정으로 확인한다.
- **리눅스 기관의 상류 도구** `[미확인]` — 신규 기관이 어떤 도구로 감시 폴더에
  파일을 넣는지 모른다. 예: rsync 는 `.원래이름.XXXXXX` 숨김 임시 파일을 써서
  접미사 규칙으로 잡히지 않는다. "점으로 시작하는 파일 제외" 규칙은 근거 없이
  넣지 않고, 설치 전 확인 항목으로 둔다.
- **대소문자 충돌 (리눅스)** — `A.rnx.gz` 와 `a.rnx.gz` 가 공존하면 정규화
  충돌이 난다. 기존 `SkippedDuplicate` + WARN 이 처리하므로 추가 작업 없음.
- **버려진 `.filepart`** — SFTPClient 가 지우지 않으므로 영구 잔류한다.
  `SkippedPart` 가 매 회차 로그에 찍혀 신호는 있다. 장기 잔류 경보는 유닛 4
  검토 후보(결정 아님).
- 파일 내용의 정상 여부는 보장하지 않는다. 미완성 파일을 최종 이름으로 저장하는
  도구에는 별도 검증이 필요하다.

## 4. 검증

- `domain.TestIsPartFile` — `.filepart` 소·대문자, Windows·POSIX 경로, 점 없는
  `...filepart`·중간 위치는 false.
- `domain.TestNormalizeNameKeepsFilepart` — `NormalizeName` 이 `.filepart` 를
  제거하지 않고 `IdentityRule == FILENAME_V1` 임을 고정.
- `put.TestRunner_FilepartIsExcludedUntilRenamed` — 오래되고 크기 있는
  `.filepart` 2건(대문자 포함)이 `SkippedPart=2`·`rejected part=2` 로 빠지고
  장부에 오르지 않으며, 같은 디렉터리의 완성 파일만 후보가 된다.
- `put.TestRunner_FilepartBecomesCandidateAfterRename` — 1회차 `.filepart`
  만 있으면 후보·장부 없음. 2회차 최종 이름은 기존 절차로 후보가 되고
  장부에 오른다. 임시 키와 완성 키는 합쳐지지 않는다.
- `put.TestRunner_ExistingFilepartLedgerRowIsNotReselected` — 기존
  `*.filepart` common/PENDING 행은 디스크에 그대로 있어도 재선정되지 않고
  행은 유지된다.
- `ledger.TestLookupCommonAcceptsNormalizedFilepart` — 장부 이름 가드가
  `.filepart` 를 거부하지 않음을 고정(`.part` 거부와 비대칭).
- 기존 `putPartSuffix` ↔ `IsPartFile` drift 테스트 유지.
- `go test ./... -count=1` 전체 통과 (2026-09-18).
