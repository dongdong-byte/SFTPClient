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

	// RepostDownloaded 가 false 면 Origin=DOWNLOAD 인 파일을 후보에서
	// 제외한다 (Ping-Pong 방지, 설계안 9.1).
	RepostDownloaded bool

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

	// SkippedPart 는 .part 라 Ledger Lookup 전에 제외한 수다.
	//
	// NormalizeName 전에 제거하지 않으면 최종 파일과 같은 식별자로
	// 합쳐질 수 있다. 실제 reject reason 의 판정은 verify 가 담당한다.
	SkippedPart int

	// SkippedDuplicate 는 카테고리 안에서 정규화 이름이 중복 관측되어
	// 두 번째 이후를 버린 수다.
	SkippedDuplicate int

	Unchanged int
	New       int
	Changed   int

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

	// Candidates 는 필터 통과 후보 수다 (전 카테고리 절단 이전).
	Candidates int

	// Retries 는 그중 기존 FAILED revision 을 재시도하는 후보 수다.
	Retries int

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

	DryRun  bool
	Elapsed time.Duration
}
