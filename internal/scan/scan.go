// Package scan 은 경로 템플릿을 날짜 범위로 확장하여 디렉터리를 나열하고,
// 발견한 항목을 있는 그대로 호출자에게 전달한다.
//
// scan 은 판정하지 않는다.
//
//	scan    어떤 항목이 어디에 있고 크기·시각이 얼마인가
//	verify  이 파일이 전송 가능한 상태인가
//	ledger  이미 관측·전송한 파일인가
//
// 0바이트, .part, 확장자, 파일명 규칙, Grace Time 은 여기서 검사하지 않는다.
//
// Hot / Deep / Recovery 도 구분하지 않는다.
// 세 방식의 차이는 Range 뿐이며, 어떤 Range 를 사용할지는 호출자가 결정한다.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

const (
	// Hourly Category 는 날짜마다 00~23 디렉터리를 직접 계산한다.
	hoursPerDay = 24

	// 디렉터리 나열 실패가 대량 발생해도 상세 오류를 무한히 보관하지 않는다.
	maxFailureDetails = 10
)

// ErrInvalidInput 은 Scanner 에 전달된 입력 자체가 유효하지 않을 때 반환된다.
var ErrInvalidInput = errors.New("scan: invalid input")

// Entry 는 디렉터리 나열에서 관측한 항목 하나이다.
//
// scan 은 값을 정규화하거나 필터링하지 않는다.
type Entry struct {
	Name  string
	Size  int64
	MTime time.Time
	IsDir bool
}

// DirLister 는 디렉터리 하나를 나열한다.
//
// scan 이 os.ReadDir 에 직접 의존하지 않게 하여
// 로컬 filesystem 과 향후 SFTP 구현을 같은 Scan 로직에서 사용할 수 있게 한다.
//
// 존재하지 않는 디렉터리는 errors.Is(err, fs.ErrNotExist) 로
// 판별할 수 있는 오류를 반환해야 한다.
type DirLister interface {
	List(ctx context.Context, dir string) ([]Entry, error)
}

// Range 는 스캔할 날짜 범위이다.
//
// From 과 To 날짜를 모두 포함한다.
// 경로 토큰은 UTC 기준이므로 Scan 도 UTC 날짜를 사용한다.
type Range struct {
	From time.Time
	To   time.Time
}

// Days 는 Range 에 포함되는 날짜 수를 돌려준다.
//
// To 가 From 보다 이전이면 0이다.
func (r Range) Days() int {
	from := truncateToUTCDay(r.From)
	to := truncateToUTCDay(r.To)

	if to.Before(from) {
		return 0
	}

	return int(to.Sub(from)/(24*time.Hour)) + 1
}

// truncateToUTCDay 는 t 를 UTC 기준 해당 날짜의 자정으로 내린다.
func truncateToUTCDay(t time.Time) time.Time {
	u := t.UTC()

	return time.Date(
		u.Year(),
		u.Month(),
		u.Day(),
		0,
		0,
		0,
		0,
		time.UTC,
	)
}

// Batch 는 디렉터리 하나의 나열 결과이다.
//
// 전체 Scan 결과를 한 슬라이스에 모으지 않고 디렉터리 단위로 바로 전달한다.
type Batch struct {
	// Category 는 파일명에서 추론한 값이 아니라
	// 이 디렉터리를 Scan 하도록 호출자가 지정한 Category 이다.
	Category domain.Category

	// Dir 은 실제로 나열한 확장 완료 경로이다.
	Dir string

	// When 은 해당 디렉터리에 대응하는 UTC 관측 시각이다.
	//
	// Daily 는 해당 날짜 00:00,
	// Hourly 는 실제 00~23 시각이다.
	//
	// 이후 호출자가 대응되는 다른 Path Template 을 확장할 때 사용할 수 있다.
	When time.Time

	// Entries 는 디렉터리에서 발견한 항목이다.
	//
	// 빈 디렉터리에는 VisitFunc 을 호출하지 않으므로
	// Batch 로 전달될 때는 항상 1건 이상이다.
	Entries []Entry
}

// VisitFunc 은 디렉터리 하나의 나열 결과를 소비한다.
//
// 오류를 반환하면 Scan 은 즉시 중단한다.
type VisitFunc func(Batch) error

// DirError 는 나열에 실패한 디렉터리와 그 원인이다.
type DirError struct {
	Dir string
	Err error
}

func (e DirError) Error() string {
	return fmt.Sprintf("%q: %v", e.Dir, e.Err)
}

func (e DirError) Unwrap() error {
	return e.Err
}

// Result 는 Scan 한 번의 집계이다.
//
// error == nil 은 Scan 루프가 끝까지 완주했다는 의미이다.
// 일부 디렉터리 나열에 실패했는지는 Errs 로 별도 확인한다.
type Result struct {
	// Dirs 는 나열에 성공한 디렉터리 수이다.
	// 빈 디렉터리도 포함한다.
	Dirs int

	// Missing 은 존재하지 않아 정상적으로 건너뛴 디렉터리 수이다.
	Missing int

	// Files 는 성공적으로 나열한 Entry 수이다.
	//
	// scan 은 IsDir=true 항목도 필터링하지 않으므로
	// 엄밀히는 "전송 대상 파일 수"가 아니라 관측한 항목 수이다.
	Files int

	// Errs 는 fs.ErrNotExist 이외의 이유로
	// 나열에 실패한 디렉터리 수이다.
	Errs int

	// Failures 는 나열 실패 상세이다.
	// 최대 maxFailureDetails 건까지만 보관한다.
	Failures []DirError
}

// Scanner 는 날짜 범위를 디렉터리 경로로 펼쳐 순차적으로 나열한다.
//
// Scan 은 병렬화하지 않는다.
// 전송 Worker Pool 과 Scan 순차성은 서로 별개의 문제이다.
type Scanner struct {
	lister DirLister
}

// New 는 Scanner 를 만든다.
func New(lister DirLister) *Scanner {
	return &Scanner{
		lister: lister,
	}
}

// Scan 은 날짜 범위에 해당하는 디렉터리를 순서대로 나열한다.
//
// Daily:
//
//	날짜당 1개 디렉터리
//
// Hourly:
//
//	날짜당 00~23, 총 24개 디렉터리
//
// 디렉터리 나열 오류 정책:
//
//	fs.ErrNotExist → Missing 증가 후 계속
//	그 외 오류      → Errs/Failures 기록 후 계속
//	context 취소    → 즉시 중단
//	visit 오류      → 즉시 중단
func (s *Scanner) Scan(
	ctx context.Context,
	category domain.Category,
	tpl *pathpl.Template,
	r Range,
	visit VisitFunc,
) (Result, error) {
	var result Result

	if ctx == nil {
		return result, fmt.Errorf(
			"%w: context is nil",
			ErrInvalidInput,
		)
	}

	if s == nil || s.lister == nil {
		return result, fmt.Errorf(
			"%w: lister is nil",
			ErrInvalidInput,
		)
	}

	if visit == nil {
		return result, fmt.Errorf(
			"%w: visit func is nil",
			ErrInvalidInput,
		)
	}

	if err := checkInput(category, tpl, r); err != nil {
		return result, err
	}

	from := truncateToUTCDay(r.From)
	days := r.Days()

	perDay := 1
	if category.IsHourly() {
		perDay = hoursPerDay
	}

	for dayOffset := 0; dayOffset < days; dayOffset++ {
		day := from.AddDate(0, 0, dayOffset)

		for hour := 0; hour < perDay; hour++ {
			if err := ctx.Err(); err != nil {
				return result, fmt.Errorf(
					"scan: canceled: %w",
					err,
				)
			}

			when := day.Add(time.Duration(hour) * time.Hour)
			dir := tpl.Expand(when)

			entries, err := s.lister.List(ctx, dir)

			// List 구현이 context 취소를 다른 형태의 오류로 감싸더라도
			// 취소는 일반적인 디렉터리 실패로 집계하지 않는다.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, fmt.Errorf(
					"scan: canceled at %q: %w",
					dir,
					ctxErr,
				)
			}

			switch {
			case errors.Is(err, fs.ErrNotExist):
				result.Missing++
				continue

			case err != nil:
				result.Errs++

				if len(result.Failures) < maxFailureDetails {
					result.Failures = append(
						result.Failures,
						DirError{
							Dir: dir,
							Err: err,
						},
					)
				}

				continue
			}

			result.Dirs++
			result.Files += len(entries)

			if len(entries) == 0 {
				continue
			}

			batch := Batch{
				Category: category,
				Dir:      dir,
				When:     when,
				Entries:  entries,
			}

			if err := visit(batch); err != nil {
				return result, fmt.Errorf(
					"scan: visit %q: %w",
					dir,
					err,
				)
			}
		}
	}

	return result, nil
}

// checkInput 은 Scan 시작 전에 호출 인자를 검증한다.
//
// config.Validate 를 통과한 값이 일반적인 진입 경로지만,
// Scanner 자체도 잘못된 입력으로 조용히 실행되지 않도록 방어한다.
func checkInput(
	category domain.Category,
	tpl *pathpl.Template,
	r Range,
) error {
	// 알 수 없는 Category 를 Daily 로 오인하는 것을 막는다.
	//
	// 정의된 Category 는 반드시 Daily 또는 Hourly 중 하나이다.
	if !category.IsDaily() && !category.IsHourly() {
		return fmt.Errorf(
			"%w: unknown category %q",
			ErrInvalidInput,
			category,
		)
	}

	if tpl == nil {
		return fmt.Errorf(
			"%w: template is nil",
			ErrInvalidInput,
		)
	}

	if r.From.IsZero() || r.To.IsZero() {
		return fmt.Errorf(
			"%w: range is not initialized",
			ErrInvalidInput,
		)
	}

	if r.Days() < 1 {
		return fmt.Errorf(
			"%w: to %s is before from %s",
			ErrInvalidInput,
			truncateToUTCDay(r.To).Format(time.DateOnly),
			truncateToUTCDay(r.From).Format(time.DateOnly),
		)
	}

	// config.Validate 에서도 같은 불변식을 확인하지만,
	// Scanner 를 직접 사용할 때의 조용한 오동작도 막는다.
	switch {
	case category.IsHourly() && !tpl.HasToken(pathpl.TokenHH):
		return fmt.Errorf(
			"%w: hourly category %s has no (%s) in %q",
			ErrInvalidInput,
			category,
			pathpl.TokenHH,
			tpl.String(),
		)

	case category.IsDaily() && tpl.HasToken(pathpl.TokenHH):
		return fmt.Errorf(
			"%w: daily category %s must not have (%s) in %q",
			ErrInvalidInput,
			category,
			pathpl.TokenHH,
			tpl.String(),
		)
	}

	return nil
}
