// Package put 은 PUT 방향의 실행 파이프라인을 조립한다.
//
// 확정 흐름 (GUIDELINES 5절. 순서를 바꾸지 않는다):
//
//	List → Batch Lookup → 메모리 대조 → (신규·변경만) Verify → Upsert
//	→ 후보 필터 → 정렬 → MaxFilesPerRun 절단 → PENDING 일괄 등록 → (전송)
//
// 이 패키지는 판정 로직을 새로 만들지 않는다.
// 판정은 verify 가, 기록은 ledger 가, 나열은 scan 이 한다.
// put 은 그 사이의 순서와 흐름만 소유한다.
//
// Unchanged 를 후보 경로에서 떨어뜨리지 않는다 (2026-08-30 확정).
// "장부와 같다" 는 "이미 보냈다" 가 아니다 — 직전 실행이 PENDING 등록
// 직후 죽었다면 파일은 오늘 Unchanged 로 보이지만 아직 전송되지 않았다.
// Upsert 생략(쓰기 절약)과 후보 탈락(전송 포기)은 다른 결정이다.
package put

import (
	"log"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
)

// Candidate 는 이번 실행이 전송해야 한다고 판정한 파일 하나다.
type Candidate struct {
	// Key 는 파일의 category/file_name 과 revision 정보를 담는다.
	//
	// live 에서는 실제 put_ledger 논리 키다.
	//
	// dry-run 신규 파일은 아직 common_ledger 행이 없으므로 Revision=0,
	// 변경 파일은 현재 common_ledger revision 을 담는다.
	// 둘 다 RevisionPending=true 이며 live 전송용 최종 revision 은 아니다.
	Key ledger.PutKey

	// LocalPath 는 스캔이 관측한 그대로의 로컬 경로다
	// (확장 완료 디렉터리 + 원본 이름. 정규화 전).
	LocalPath string

	// Size 는 스캔 시점의 크기다. BeginPut 의 local_size 입력이 된다.
	Size int64

	// When 은 이 파일이 속한 디렉터리의 UTC 관측 시각이다 (Batch.When).
	// 전송 시 RemotePath.Expand(When) 의 입력이 된다.
	When time.Time

	// IsRetry 는 기존 FAILED 행의 재시도임을 뜻한다.
	//
	// PENDING 고아는 IsRetry=false 다. PENDING INSERT 는
	// ON CONFLICT DO NOTHING 이므로 재등록 시에도 기존 행은 보존된다.
	IsRetry bool

	// RevisionPending 은 dry-run 에서 live 전송용 revision 이 아직
	// 생성되지 않았음을 뜻한다.
	//
	//	false:
	//	  Key.Revision 이 실제 사용할 revision 이다.
	//
	//	true:
	//	  dry-run 신규/변경 파일이다.
	//	  신규는 Revision=0,
	//	  변경은 현재 common_ledger revision 을 담는다.
	//	  live 에서는 Upsert 후 ledger 에서 최종 revision 을 다시 읽는다.
	//
	// "Estimated" 가 아니라 "Pending" 인 이유는 put 이 미래 revision 을
	// 추정하지 않기 때문이다 (A안, 2026-08-30 확정). 값을 지어내는 대신
	// 아직 확정되지 않았다는 사실만 표시한다.
	RevisionPending bool

	// SetKey 는 게이트를 통과한 후보가 속한 세트의 키다.
	// MaxFilesPerRun 절단이 세트를 쪼개지 않도록 plan.go 가 사용한다.
	// 빈 문자열은 게이트 OFF 카테고리 또는 세트 소속 유보 파일이며,
	// 두 경우 모두 기존과 같은 파일 단위 절단을 따른다.
	SetKey string

	// RetryCeiling 은 이 후보 하나에만 적용하는 BeginPut maxRetries
	// 대체값이다. 0 이면 Opts.MaxRetries 를 그대로 쓴다 (기존 동작).
	//
	// 수동 재무장(v4 §4.3)이 소진 행에 "현재 attempts + 1" 을 실어
	// BeginPut 의 `attempts < ?` 가드를 이번 실행 한 번만 통과시킨다.
	// attempts 자체는 되돌리지 않는다 — ledger 는 바꾸지 않고 판정
	// 입력만 바꾼다(§10 구현 주의). RetryCeiling > 0 인 후보는 항상
	// IsRetry=true 다 (FAILED 행 후보는 IsRetry 뿐이라는 transfer.go
	// 불변식의 부분집합).
	RetryCeiling int64
}

// CategoryJob 은 카테고리 하나의 스캔 입력 조립이다.
//
// config 를 여기서 풀어 scan 이 config 를 모르게 한다 (SCAN DESIGN 9절).
// RemotePath 는 이 단계(후보 선정)에서는 쓰지 않지만, 전송 단계가
// Candidate.When 으로 Expand 할 대상이므로 조립 시점에 함께 받는다.
type CategoryJob struct {
	Category   domain.Category
	LocalPath  *pathpl.Template
	RemotePath *pathpl.Template

	// RequiredKinds 는 Set Completeness Gate 의 필수 종 목록이다
	// (config [SET.RINEXx], 소문자 정규화 완료 값). nil/빈 목록이면
	// 이 카테고리의 게이트는 OFF 이며 기존 동작과 완전히 동일하다.
	RequiredKinds []string

	// ResendMinKinds 는 resend 단계의 세트 게이트 최소 종이다
	// (config [SET.RINEXx] ResendMinKinds — v4 §5, 커밋 2).
	//
	// Opts.Resend 일 때만 쓰인다. nil 이면 resend 게이트 무조건
	// 우회(§5.3). 값 문법·RequiredKinds 부분집합·게이트 OFF 조합
	// 거부는 config 가 이미 검증했다 — put 은 재검증하지 않는다.
	ResendMinKinds []string
}

// RunOptions 는 실행 방식이다.
type RunOptions struct {
	// DryRun 이면 common_ledger / put_ledger 의 업무 데이터를 변경하지 않는다.
	// 조회만 수행하여 "실행하면 무엇을 할 것인지"를 관측한다.
	//
	// ledger.Open 자체의 기존 schema/PRAGMA 처리는 ledger 책임이다.
	DryRun bool

	// MaxRetries 는 누적 자동 PUT 착수 상한이다 (config.Put.MaxRetries).
	// 최초 실제 전송 착수도 포함하며, attempts 는 BeginPut 성공 시 증가한다.
	//
	// FAILED && attempts >= MaxRetries 는 동일 revision 의 자동 재시도
	// 후보에서 빠진다.
	MaxRetries int

	// MaxFilesPerRun 은 전 카테고리 후보 합산 후 적용하는 절단 상한이다.
	// 0 이면 절단하지 않는다.
	//
	// 0 을 허용하는 것은 config 의 규약과 같다 (config.example.ini,
	// config.Put.MaxFilesPerRun, validate 의 음수만 거부). 정기 실행에서
	// 쓰라고 둔 값이 아니라, 밀린 물량을 의도적으로 한 번에 밀어넣어야
	// 하는 일회성 복구 상황을 위한 것이다.
	MaxFilesPerRun int

	// MaxWorkers 는 디렉터리 단위 전송의 동시 워커 상한이다 (config.Put.MaxWorkers).
	//
	// 작업 단위는 원격 디렉터리이며, 디렉터리 수가 이 값보다 많아도 워커는
	// 이 수만큼만 뜬다. 적으면(Daily 단독=2) 그만큼만 가동한다. 0 이하면
	// Transfer 가 1 로 보정한다(단일 워커) — validate 가 이미 1 이상을 강제하나
	// DB 를 직접 여는 호출자를 위한 방어다.
	MaxWorkers int

	// SeedMode 는 후보 계산 결과를 seed(설치 초기화)가 소비하는 모드다.
	//
	// finalize 에서 두 가지를 생략한다.
	//
	//	MaxFilesPerRun 절단 — seed 의 목적은 "첫 실행 창 전체의 중복
	//	  방지" 다. 전송 회차 상한을 적용하면 2000건만 seed 되고
	//	  나머지는 첫 live 실행이 재전송한다.
	//	PENDING 등록 — seed 는 전송 목록을 만들지 않는다. 원격 대조로
	//	  VERIFIED 를 직접 기록하고(SeedVerified), 대조 실패분은 첫
	//	  live 실행이 정상 경로로 처리한다.
	//
	// 후보 계산(verify·upsert·revision 확정·DOWNLOAD origin 제외)은
	// live 와 완전히 같다 — seed 전용 파이프라인 복제를 기각한 이유다
	// (2026-09-01: 동일 로직 이중 유지보수).
	SeedMode bool

	// MaxHashBackfillPerRun 은 한 실행에서 자연 백필(비후보 행의
	// 지문 채우기)로 해시할 최대 파일 수다 (config, UNIT2 설계 v3 §3).
	// 0 이면 백필을 끈다. 판정·필수 해시는 이 예산과 무관하다.
	MaxHashBackfillPerRun int

	// RepostDownloaded 가 false 면 Origin=DOWNLOAD 인 파일을 후보에서
	// 제외한다 (Ping-Pong 방지, 설계안 9.1).
	RepostDownloaded bool

	// Sites 는 관측소 선택 조건이다 (SITE v1 §4, resend 커밋 5).
	//
	// domain.ParseSiteList 결과(대문자 4자리, 정규화·중복 제거 완료)를
	// 담는 것이 정석이다. Run 은 이 목록을 domain.NormalizeSiteList 로
	// 다시 통과시킨다 — 규칙의 주인은 domain 하나이고 put 은 자기 문법을
	// 만들지 않는다. 정규화를 거치지 않은 값(빈 항목·9자리·영숫자 외)은
	// Run 오류다. 정확 일치만 하면 그런 값이 오류 없이 "0 건 선택"으로
	// 끝나므로 침묵 실패 대신 오류로 드러낸다. 실행 도중 호출자가 이
	// 목록을 변경하지 않는다.
	//
	// 제로값(nil/빈) = 필터 없음. 자동 PUT 과 자동 resend(커밋 7)는 이
	// 필드를 비운다. 채우는 곳은 수동 resend CLI(커밋 8)뿐이다 — 일반
	// 자동 PUT 의 --site 노출은 SITE v1 §7 미결이며 여기서 열지 않는다.
	//
	// 선택 밖 파일은 Ledger Upsert·해시(백필 포함)·전송 상태 변경·세트
	// 관측을 일절 하지 않는다(등록 전 필터). 생략과 명시적 빈 값의
	// 구분, 파싱 오류 시 요청 전체 거부는 CLI 몫이다 — 오류를 무시하고
	// 빈 목록을 넘기면 잘못된 요청이 전체 선택으로 확대된다.
	Sites []string

	// Resend 는 resend 단계(자동·수동 공통) 실행이다 (resend 설계 v4 §5).
	//
	// 세트 게이트의 보류 판정이 RequiredKinds 대신 CategoryJob 의
	// ResendMinKinds 를 쓴다 — nil 이면 무조건 우회(§5.3). 세트
	// 정체성(SetKey 새김·경계 절단·우회 관측)은 계속 RequiredKinds
	// 기준이다(§5.5 — 우회로 통과한 세트도 절단선에서 쪼개지 않는다).
	//
	// 배선은 커밋 7(자동)·커밋 8(수동)이 한다. SeedMode 와 조합할 수
	// 없다 — seed 는 PENDING 을 등록하지 않으므로 resend 의 목적
	// (재전송 목록 생성)과 모순된다. checkInput 이 거부한다.
	Resend bool

	// RearmExhausted 는 소진 파일 재무장이다 (v4 §4.3 — 수동 resend 전용).
	//
	// FAILED && attempts >= MaxRetries 인 현재 revision 을 이번 실행
	// 한 번만 재시도 후보로 올린다. attempts 는 0 으로 되돌리지 않는다
	// (누적값은 장애 분석 기록) — 후보에 RetryCeiling = attempts+1 을
	// 실어 BeginPut 의 WHERE 가드를 딱 한 번 통과시킨다(§10 구현 주의).
	//
	// 자동 resend 에서 켜면 영구 실패 파일이 Retention 한계까지 매시간
	// 재시도되어 MaxRetries 상한이 사실상 사라진다 — Resend=true 전제를
	// checkInput 이 강제하고, 자동 배선(커밋 7)은 이 필드를 켜지 않는다.
	RearmExhausted bool

	// Logger 는 요약·경고 출력에 쓴다. nil 이면 log.Default().
	Logger *log.Logger

	// Now 는 테스트 주입용 현재 시각이다. nil 이면 time.Now.
	// IngressVerifiedAt 기록과 실행 시간 관측에 쓴다.
	Now func() time.Time
}

// CurrentPutObservation 은 dry-run 변경 파일이 현재 revision 에서
// 어떤 PUT 상태였는지를 집계한 관측값이다.
//
// 이 값은 후보 제외 판정이 아니다.
// 변경 파일은 live 에서 새 revision 을 얻으므로 현재 revision 이
// VERIFIED 또는 exhausted 여도 새 revision 의 전송 후보가 된다.
//
// 여섯 필드는 LookupPut 판정표(put.go)와 1:1 로 대응한다.
// 판정 규칙이 바뀌면 이 타입도 함께 걸리도록 의도한 구성이다.
type CurrentPutObservation struct {
	// NoRow 는 현재 revision 의 전송 이력이 아직 없는 수다.
	// 오류가 아니라 정상 관측값이다.
	NoRow      int
	Pending    int
	InProgress int
	Verified   int

	// FailedRetryable 은 attempts < MaxRetries 인 FAILED 수다.
	//
	// Exhausted 와 이름을 대칭으로 둔 이유는 출력 때문이다.
	// 단순히 Failed 로 두면 로그의 failed=2 exhausted=3 을 보고
	// "FAILED 가 총 2건인가 5건인가" 가 갈린다. 필드명과 출력 키를
	// 같게 유지하여 그 모호함을 없앤다.
	FailedRetryable int

	// Exhausted 는 attempts >= MaxRetries 인 FAILED 수다.
	Exhausted int
}

// CategoryReport 는 카테고리 하나의 실행 관측이다.
//
// 필드 대부분이 dry-run 체크포인트(GUIDELINES 1차 목표: 관측소 수,
// 확장자 분포, 경로 정합, 소요시간)의 답이 되도록 구성했다.
// HashStats 는 해시 계측 묶음이다 (건수·바이트·소요 ms).
type HashStats struct {
	Files  int64
	Bytes  int64
	Millis int64
}

type CategoryReport struct {
	Category domain.Category

	// Scan 은 나열 통계다 (Dirs/Missing/Files/Errs/Failures).
	//
	// Failures 를 리포트에 함께 남기는 이유는 candidates=0 의 뜻이
	// 두 가지이기 때문이다 — "보낼 것이 없다" 와 "아무것도 못 봤다".
	// 나열 실패 건수가 함께 보여야 그 둘이 구분된다.
	Scan scan.Result

	// SkippedDirs 는 Entries 중 IsDir 라 제외한 수다.
	SkippedDirs int

	// SkippedPart 는 임시 접미사(.part / .filepart)라 Ledger Lookup 전에
	// 제외한 수다. 매 회차 0 이 아니면 상류 전송이 진행 중이거나 끊겨
	// 남은 임시 파일이 있다는 뜻이다. 실제 reject reason 의 판정은
	// verify 가 담당한다.
	//
	// .part 는 NormalizeName 이 접미사를 떼므로 Lookup 전에 빼지 않으면
	// 최종 파일과 같은 식별자로 합쳐진다. .filepart 는 정규화 불변이라
	// 키가 합쳐지지 않는다. 그쪽 방어는 IsPartFile 게이트가 전부다.
	SkippedPart int

	// SkippedDuplicate 는 카테고리 안에서 정규화 이름이 중복 관측되어
	// 두 번째 이후를 버린 수다.
	SkippedDuplicate int

	// SiteMismatch / SiteUnknown 은 Opts.Sites 지정 시의 제외 집계다
	// (SITE v1 §5, resend 커밋 5).
	//
	//	Mismatch — 관측소를 추출했으나 선택 밖.
	//	Unknown  — 이름에서 관측소를 확정할 수 없어(유보) 제외.
	//
	// Sites 가 비면 둘 다 항상 0 이다 — site 검사 자체를 하지 않으므로
	// 식별 불가가 새 제외 사유가 되지 않는다 (SITE §4). 출력 배선은
	// 커밋 8 의 resend 로그가 한다.
	SiteMismatch int
	SiteUnknown  int

	// SiteMatched 는 요청 site별 일치 관측 수다
	// (메시지 문서 MSG-SITE-01 선행 계약, SITE v1 §5).
	//
	// "발견"의 판정 근거는 kept 가 아니라 이 값이다 — 전부 VERIFIED
	// 여도, 안정성·세트 검사로 보류되어도 발견은 된 것이다. 따라서
	// 관측 시점은 site 필터 일치 직후, 중복 검사·검증·게이트·후보
	// 제외 전부의 앞이다.
	//
	// 키는 domain.NormalizeSiteList 가 정규화한 관측소 코드(대문자)다 —
	// Opts.Sites 의 표기와 무관하게 Run 입구에서 통일되며, Sites 지정 시
	// 요청한 모든 site 가 0 으로 초기화된다. 합계만으로는 DBON,DBOM 중
	// DBON 만 발견된 상황을 구분할 수 없다(문서 §5). Sites 가 비면
	// nil 이다. 같은 정규화 이름이 중복 관측되면 중복도 센다 —
	// 발견 여부 판정에는 영향이 없다.
	SiteMatched map[string]int

	Unchanged int
	New       int
	Changed   int

	// MetadataOnly 는 size 같음 · mtime 다름 · 지문 일치로 판정되어
	// revision 을 올리지 않고 기준선(mtime)만 옮긴 수다.
	// (UNIT2 설계 v3 §1 — 억제된 mtime 드리프트. 순서 4 경보의 토대)
	MetadataOnly int

	// Hash* 는 해시 원인 4분류별 계측이다 (설계 v3 §6).
	HashNew      HashStats // 신규 최초 지문
	HashChanged  HashStats // 변경(size≠ 또는 지문 상이)의 새 지문
	HashDrift    HashStats // size=·mtime≠ 판정 해시
	HashBackfill HashStats // 자연 백필

	// HashFailed 는 읽기 실패(보수 진행), HashUnstable 은 계산
	// 전후·스캔 대비 불안정 관측(회차 보류) 수다 (설계 v3 §1.4).
	HashFailed   int
	HashUnstable int

	// BackfillDeferred 는 예산 소진으로 다음 회차로 미룬 백필 수다.
	BackfillDeferred int

	// Rejected 는 Ingress 거부의 사유별 건수다 (verify.Reason.String 키).
	Rejected map[string]int

	// RejectedExamples 는 거부된 파일명 예시다 (사유별 최대 5개).
	//
	// 개수만으로는 정상 파일이 규칙 오류로 걸린 것인지, 혼입 파일이
	// 옳게 걸린 것인지 구분할 수 없다. 거부된 파일은 어느 Category 도
	// 보내지 않으므로 그 사실이 사람에게 도달해야 한다.
	// ExhaustedExamples 와 같은 성격의 관측 수단이다.
	//
	// 원본 파일명을 담는다. NormalizeName 을 거치면 대소문자가 바뀌어
	// 실제 디스크에서 찾을 때 어긋난다.
	RejectedExamples map[string][]string

	// Unchanged/current revision 후보의 실제 제외 사유별 건수다.
	ExcludedVerified       int
	ExcludedInProgress     int
	ExcludedExhausted      int
	ExcludedDownloadOrigin int

	// ExhaustedExamples 는 동일 revision 의 자동 재시도 소진으로
	// 실제 후보에서 제외된 파일명 예시다 (최대 5개).
	ExhaustedExamples []string

	// DryRunChangedCurrentPut 은 dry-run 변경 파일의 현재 revision
	// put_ledger 상태를 보여주는 관측값이다.
	//
	// 현재 revision 이 exhausted 여도 변경 파일 자체는 제외하지 않는다.
	// live 에서는 Upsert 로 새 revision 이 생겨 attempts 예산이 리셋된다.
	//
	// live 실행에서는 항상 제로값이다. 이 관측은 dry-run 전용이다.
	DryRunChangedCurrentPut CurrentPutObservation

	// SetGate 는 이 카테고리에 세트 게이트가 켜져 있었는지다.
	// 아래 카운터 0 이 "게이트 없음" 인지 "있었으나 전부 완성" 인지
	// 로그에서 구분하기 위한 값이다.
	SetGate bool

	// SetHeld 는 필수 종 미완성으로 이번 회차 후보에서 보류된
	// 파일 수다 (재시도 대기열 포함). put_ledger 에는 아무것도
	// 기록되지 않으며 다음 스캔이 재계산한다.
	SetHeld int

	// SetUnparsed 는 게이트가 켜진 카테고리에서 세트 소속을 확정할
	// 수 없어 개별 파일로 통과한 관측 파일 수다. (2026-09-10 확정 ①)
	SetUnparsed int

	// Candidates 는 필터 통과 후보 수다 (전 카테고리 절단 이전).
	Candidates int

	// Retries 는 그중 기존 FAILED revision 을 재시도하는 후보 수다.
	Retries int

	// Rearmed 는 Retries 중 소진(attempts >= MaxRetries) 상태에서
	// 수동 재무장(v4 §4.3)으로 올라온 후보 수다 (Rearmed ⊆ Retries).
	// RearmExhausted 가 꺼져 있으면 항상 0 이다.
	Rearmed int

	// ExtCount 는 스캔에서 관측한 원본 파일명의 마지막 확장자 분포다.
	// 대소문자를 그대로 보존하므로 .Z 와 .z 는 별도로 집계될 수 있다.
	ExtCount map[string]int

	// StationCount 는 dry-run 관측용 관측소 수다.
	// RINEX2 short / RINEX3·4 long filename 모두 앞 4자를 station ID 로
	// 사용한다. 정밀 RINEX 파서가 아니라 현장 분포 확인용 집계다.
	StationCount int

	Elapsed time.Duration
}

// RunReport 는 전체 실행의 관측이다.
type RunReport struct {
	Categories []CategoryReport

	// TotalCandidates 는 절단 전 전체 후보 수다.
	TotalCandidates int

	// Cut 은 MaxFilesPerRun 으로 이번 실행 대상에서 잘린 수다.
	//
	// 잘린 파일에는 put_ledger PENDING 을 등록하지 않는다.
	// live 모드에서는 신규·변경 파일의 common_ledger 관측값이 이미
	// 기록되어 있을 수 있으며, 다음 실행에서 Unchanged 로 보이더라도
	// 현재 revision 의 put 상태를 다시 확인하여 후보가 된다.
	//
	// 즉 잘린 파일이 다음 회차에 되살아나는 경로는 "신규" 가 아니라
	// runner 의 Unchanged + PutStatus=="" 분기다. 두 경로를 혼동하면
	// 절단 후 재처리가 안 되는 것처럼 잘못 읽힌다.
	//
	// Cut > 0 이면 리포트 숫자로만 남기지 않고 WARN 을 남긴다
	// (config.example.ini 의 "제한에 걸렸다면 WARN 로그를 남긴다").
	Cut int

	// Registered 는 이번 실행 대상 수, 즉 InsertPendingBatch 에 넘긴
	// 건수다 (Print 의 pending_batch=). dry-run 은 항상 0이다.
	//
	// 실제 신규 INSERT 행 수와 다를 수 있다 — 재시도(FAILED)와 고아
	// PENDING 은 ON CONFLICT DO NOTHING 으로 no-op 이 되기 때문이다.
	// 신규만 정확히 세려면 ledger API 가 RowsAffected 합산을 반환해야
	// 하는데, 숫자 하나의 해석을 위해 API 를 바꾸지 않는다 (2026-08-30).
	Registered int

	DryRun bool

	// SeedMode 는 이 리포트가 seed 회차의 것임을 나타낸다 (UNIT4 §0.1-2).
	// Run 이 Opts.SeedMode 를 복사한다. Print 의 mode= 식별에만 쓰이며
	// 판정·장부 쓰기에는 영향이 없다 — seed 는 지문 판정 경로가 달라
	// 요약이 live 분포와 한 통에 섞이면 배포 후 분포 분석 자체가
	// 무효가 되기 때문에 존재한다 (§4).
	SeedMode bool

	// Range 는 이 회차 스캔 창의 이름이다 ("hot" / "deep").
	// Run 은 scan.Range 좌표만 알고 창의 이름은 모르므로, 이름을 아는
	// main 이 Print 전에 채운다 (UNIT4 §0.1-3). 채우지 않으면 Print 가
	// range=unset 으로 시끄럽게 찍는다 — 조용한 기본값으로 모집단이
	// 오염되는 것보다 낫다.
	Range string

	Elapsed time.Duration
}
