package download

import (
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
)

// 상세 보관 상한. 대량 발생해도 Report 가 무한히 커지지 않는다.
const (
	exampleCap = 5  // 사유별 원격 경로 예시
	failureCap = 10 // 실패 상세
)

// Report 는 DOWNLOAD 회차 한 번의 집계다.
//
// 숫자와 상세만 담는다. 사람이 읽는 형식(한국어 필드명, 0 생략, 대상없음,
// 고정 순서)은 report_print.go 의 Print 가 정한다.
type Report struct {
	Categories []CategoryReport
	Duration   time.Duration

	// Range 는 이번 회차의 범위 종류다 (RangeHot·RangeDeep). 러너는 받은
	// scan.Range 만 알고 그 이름은 모르므로 main 이 채운다 — PUT 의
	// RunReport.Range 와 같은 방식. Print 가 "범위=" 로 쓴다.
	Range Range
}

// Failed 는 종료 판단에 쓰는 회차 전체의 운영 실패 수다. 파일 수신 실패와
// 원격 나열 실패를 함께 센다. 나열 실패는 일부 경로를 읽지 못해 요청 범위를
// 완전히 확인하지 못한 상태이므로, 후보가 0이어도 성공 종료로 위장하면 안 된다.
//
// Category 요약의 "실패="는 c.Failed(파일 실패)만 표시하고, 나열 실패는
// 별도의 "나열경고 … 나열실패=" 줄로 표시한다. 두 숫자를 합쳐 출력하지 않아
// 현장에서 실패 종류를 구분할 수 있다.
func (r Report) Failed() int {
	n := 0
	for _, c := range r.Categories {
		n += c.Failed + c.Scan.Errs
	}

	return n
}

// Downloaded 는 회차 전체의 수신 수다.
func (r Report) Downloaded() int {
	n := 0
	for _, c := range r.Categories {
		n += c.Downloaded
	}

	return n
}

// CategoryReport 는 Category 하나의 집계다.
type CategoryReport struct {
	Category domain.Category

	// Scan 은 원격 나열 집계다. Scan.Files 가 보고의 "원격" 이다.
	// Missing·Errs·Irregular·Unmatched 는 커밋 6 이 경고 줄로 쓴다.
	Scan scan.Result

	// Downloaded 는 최종 Rename 까지 성공한 수다 (수신).
	Downloaded int

	// UnknownKind 는 Downloaded 가운데 파일명으로 Category 를 알 수 없었던
	// 수다 (종류미상). Downloaded 의 부분집합이며 별도 결과가 아니다.
	// 이번 회차에 새로 받은 것만 센다 — 이미 받아 둔 파일(기존)까지 매시간
	// 세면 기관 쪽 잡파일 하나가 고객 화면에 영구히 뜬다.
	UnknownKind int

	// Reasons 는 받지 않은 사유별 건수다. 제외(임시파일·유예중 등)와
	// 목적지 판정(기존·충돌·비정상대상·목적지중복)이 함께 들어간다.
	Reasons map[Reason]int

	// Examples 는 사유별 원격 경로 예시다 (exampleCap 건까지).
	Examples map[Reason][]string

	// Failed 는 실패 수다. 이번 회차에 재시도하지 않으며, 최종 파일이 없으므로
	// 다음 회차가 다시 시도한다 (커밋 계획 §1.1-6).
	Failed int

	// Failures 는 실패 상세다 (failureCap 건까지). 파일별 로그는 발생 즉시
	// 따로 남긴다 — 이것은 요약용 사본이다.
	Failures []FileFailure

	// NotStarted 는 후보로 판정됐지만 회차 취소로 수신을 시작하지 않은 수다.
	// 실패가 아니다(착수하지 않았다). 최종 파일이 없으므로 다음 회차가
	// 다시 판정한다. 0 이 아니면 회차 자체가 취소 오류로 끝난다.
	NotStarted int

	// Duration 은 이 Category 의 나열·판정·수신 전체 소요다.
	Duration time.Duration
}

// FileFailure 는 실패한 파일 하나의 원인이다.
type FileFailure struct {
	RemotePath string
	LocalPath  string
	Err        error
}

func newCategoryReport(cat domain.Category) CategoryReport {
	return CategoryReport{
		Category: cat,
		Reasons:  map[Reason]int{},
		Examples: map[Reason][]string{},
	}
}

// add 는 결과 하나를 반영한다. 러너의 집계 goroutine 만 부른다.
func (c *CategoryReport) add(o outcome) {
	switch {
	case o.downloaded:
		c.Downloaded++

		if o.unknownKind {
			c.UnknownKind++
		}

	case o.notStarted:
		c.NotStarted++

	case o.err != nil:
		c.Failed++

		if len(c.Failures) < failureCap {
			c.Failures = append(c.Failures, FileFailure{
				RemotePath: o.remote,
				LocalPath:  o.local,
				Err:        o.err,
			})
		}

	case o.reason != "":
		c.Reasons[o.reason]++

		if len(c.Examples[o.reason]) < exampleCap {
			c.Examples[o.reason] = append(c.Examples[o.reason], o.remote)
		}
	}
}
