// Package scan 은 경로 템플릿을 날짜 범위로 확장하여 날짜 디렉터리를
// 나열하고, 그 아래를 재귀로 나열하여, 발견한 일반 파일을 호출자에게
// 전달한다.
//
// scan 은 파일의 의미를 판정하지 않는다.
//
//	scan    어떤 일반 파일이 어디에 있고 크기·시각이 얼마인가
//	verify  이 파일이 전송 가능한 상태인가
//	ledger  이미 관측·전송한 파일인가
//
// 0바이트, .part, 확장자, 파일명 규칙, Grace Time 은 여기서 검사하지 않는다.
// scan 이 하는 유일한 선별은 항목 종류다 (PATH_DESIGN v3 §4·§5):
// 진짜 폴더만 내려가고, 일반 파일만 Batch 로 넘기며, 그 외(링크·junction
// 등)는 건너뛰고 집계한다.
//
// Hot / Deep / resend 도 구분하지 않는다.
// 차이는 Range 뿐이며, 어떤 Range 를 사용할지는 호출자가 결정한다.
// 운영자 재전송 명령 이름은 resend 다. Recover(시작 시 IN_PROGRESS 회수)와
// 혼동하지 않는다.
package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

// maxFailureDetails — 디렉터리 나열 실패·비정규 항목이 대량 발생해도
// 상세를 무한히 보관하지 않는다. Failures 와 IrregularDetails 가
// 같은 상한을 쓴다 (PATH_DESIGN v3 §5.4).
const maxFailureDetails = 10

// ErrInvalidInput 은 Scanner 에 전달된 입력 자체가 유효하지 않을 때 반환된다.
var ErrInvalidInput = errors.New("scan: invalid input")

// Entry 는 디렉터리 나열에서 관측한 항목 하나이다.
//
// scan 은 값을 정규화하지 않는다.
type Entry struct {
	Name  string
	Size  int64
	MTime time.Time
	IsDir bool

	// Type 은 항목의 종류 비트이다 (fs.FileMode 의 type bits,
	// 즉 mode & fs.ModeType). 일반 파일이면 0 이다.
	//
	// LocalLister 가 lstat 의미(DirEntry.Info)로 채우므로, 링크는
	// 대상이 아니라 링크 자신의 종류(fs.ModeSymlink)로 보고된다.
	// Windows junction/mount point 는 Go 1.23 이후 fs.ModeIrregular 로
	// 보고된다 (PATH_DESIGN v3 §5).
	//
	// 이 필드는 관측 사실이다. "일반 파일만 수집, 진짜 폴더만 하강,
	// 그 외는 건너뛰고 집계"라는 판정(PATH_DESIGN v3 §5.1)은 재귀를
	// 수행하는 Scanner 의 책임이다.
	Type fs.FileMode
}

// IsRegular 는 이 항목이 일반 파일인지 보고한다.
//
// fs.FileMode.IsRegular 와 같은 규약이다: 종류 비트가 없는 항목
// (Type == 0)이 일반 파일이다. 종류를 지정하지 않고 만든 테스트
// fake 의 Entry 도 자연히 일반 파일이 된다.
//
// 디렉터리·링크·junction 은 LocalLister 가 해당 종류 비트를 채우므로
// false 가 된다.
//
// 두 가지를 더 지킨다.
//   - 종류 비트만 본다(Type & fs.ModeType). 권한 비트가 섞인 값
//     (예: 0o644)을 넣은 lister 에서 일반 파일이 조용히 비정규로
//     빠지는 누락을 막는다.
//   - IsDir 도 본다. Type 을 채우지 않고 IsDir 만 세운 Entry(테스트
//     fake, 향후 다른 DirLister)가 "폴더이면서 일반 파일"이 되면
//     폴더가 전송 후보로 새어 나간다.
func (e Entry) IsRegular() bool {
	return !e.IsDir && e.Type&fs.ModeType == 0
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

// Batch 는 디렉터리 하나에서 발견한 일반 파일들이다.
//
// 전체 Scan 결과를 한 슬라이스에 모으지 않고 디렉터리 단위로 바로 전달한다.
type Batch struct {
	// Category 는 파일명에서 추론한 값이 아니라
	// 이 디렉터리를 Scan 하도록 호출자가 지정한 Category 이다.
	Category domain.Category

	// Dir 은 실제로 나열한 경로이다.
	//
	// 템플릿이 확장된 날짜 디렉터리이거나, 그 아래의 하위 디렉터리이다.
	Dir string

	// When 은 이 날짜 슬롯의 00:00 UTC 이다.
	//
	// 날짜 디렉터리 아래의 하위 디렉터리 Batch 도 같은 When 을 갖는다.
	// 파일 자체의 실제 관측 시각을 의미하지 않는다 — 실제 시각은
	// 파일명에 인코딩되어 있으며 scan 은 해석하지 않는다.
	//
	// 소비처는 둘이다 (PATH_DESIGN v3 §1.2): RemotePath.Expand(When)
	// (날짜 토큰만 소비)과 SortCandidates 의 1차 정렬 키. When 이 항상
	// 자정이므로 하루 안 정렬은 이름순이 된다.
	When time.Time

	// Entries 는 이 디렉터리에서 발견한 일반 파일이다.
	//
	// 재귀로 소비한 폴더와 링크·junction 등 비정규 항목은 넘기지
	// 않는다 (PATH_DESIGN v3 §4·§5). put 의 IsDir 방어 분기는 이 계약의
	// 위반 신호 감지용으로 남는다.
	//
	// 일반 파일이 없는 디렉터리에는 VisitFunc 을 호출하지 않으므로
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

// IrregularEntry 는 일반 파일도 진짜 폴더도 아니어서 건너뛴 항목이다.
type IrregularEntry struct {
	// Path 는 부모 디렉터리와 항목 이름을 이은 경로다.
	Path string

	// Type 은 관측된 종류 비트다 (예: fs.ModeSymlink, fs.ModeIrregular).
	Type fs.FileMode
}

// Result 는 Scan 한 번의 집계이다.
//
// error == nil 은 Scan 루프가 끝까지 완주했다는 의미이다.
// 일부 디렉터리 나열에 실패했는지는 Errs 로 별도 확인한다.
type Result struct {
	// Dirs 는 나열에 성공한 디렉터리 수이다.
	// 날짜 디렉터리와 그 아래 하위 디렉터리를 모두 포함하며,
	// 빈 디렉터리도 포함한다.
	Dirs int

	// Missing 은 존재하지 않아 정상적으로 건너뛴 디렉터리 수이다.
	//
	// 날짜 디렉터리가 없는 경우(해당 날짜 데이터 미생성)와,
	// 나열과 하강 사이에 하위 디렉터리가 사라진 경우를 모두 포함한다.
	Missing int

	// Files 는 재귀 탐색 중 발견하여 Batch 로 전달한 일반 파일 수이다
	// (PATH_DESIGN v3 §5.4). 재귀로 소비한 디렉터리와 링크·junction 등
	// 비정규 항목은 포함하지 않는다.
	//
	// .part·.filepart 는 일반 파일이므로 포함하며, 이후 Verifier 가
	// SkippedPart 로 제외한다. scan 요약의 listed= 가 이 값이다.
	Files int

	// Errs 는 fs.ErrNotExist 이외의 이유로
	// 나열에 실패한 디렉터리 수이다. 모든 깊이에서 집계한다.
	Errs int

	// Failures 는 나열 실패 상세이다.
	// 최대 maxFailureDetails 건까지만 보관한다.
	Failures []DirError

	// Irregular 은 일반 파일도 진짜 폴더도 아니어서 건너뛴 항목 수이다
	// (링크·junction 등, PATH_DESIGN v3 §5). 0 이면 호출자는 아무것도
	// 출력하지 않는다 — 정상 실행의 출력을 늘리지 않는다 (§5.4).
	Irregular int

	// IrregularDetails 는 건너뛴 비정규 항목의 상세이다.
	// Failures 와 같은 상한(maxFailureDetails)을 적용한다.
	IrregularDetails []IrregularEntry
}

// Scanner 는 날짜 범위를 날짜 디렉터리로 펼치고 그 아래를 재귀로
// 순차 나열한다.
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

// Scan 은 날짜 범위에 해당하는 날짜 디렉터리를 오래된 날짜부터 나열하고,
// 각 날짜 디렉터리 아래를 재귀로 나열한다.
//
// 재귀 정책 (PATH_DESIGN v3 §1·§3·§5):
//
//   - 깊이 제한은 없다. 날짜 디렉터리가 이미 탐색 범위를 제한하며,
//     진짜 폴더만 내려가므로("IsDir 이면 하강" — 링크는 lstat 의미로
//     IsDir=false) 재귀가 내려가는 모든 폴더는 물리적으로 날짜
//     디렉터리의 자식이다. 순환이 없으므로 재귀는 유한하다.
//   - 하위 디렉터리 구조((HH) 폴더, 관측소 폴더 등)는 해석하지 않고
//     순회만 한다. 주기의 출처는 config 섹션, 시각의 출처는 파일명이며
//     scan 은 어느 쪽도 소비하지 않는다.
//   - 순회 순서 규약 (§3.2): 현재 디렉터리의 일반 파일을 먼저 하나의
//     Batch 로 넘기고, 그다음 하위 폴더를 이름 오름차순으로 내려간다.
//     put 의 duplicate 가드는 먼저 나온 파일이 이기므로, 날짜 폴더
//     바로 아래의 평면 파일이 하위 폴더 안 사본보다 이긴다. 이 순서가
//     실행마다 같아야 승자가 결정적이다. 하위 폴더 순서는 부모 나열의
//     Entry 순서를 따르며, os.ReadDir 는 이름순 정렬을 보장한다.
//   - 일반 파일도 진짜 폴더도 아닌 항목(링크·junction 등)은 따라가지도
//     않고 파일로도 취급하지 않는다. 건너뛰고 Irregular 로 집계한다 (§5).
//
// 디렉터리 나열 오류 정책 (모든 깊이에서 동일):
//
//	fs.ErrNotExist → Missing 증가 후 계속
//	그 외 오류      → Errs/Failures 기록 후 계속 (형제·다음 날짜 진행)
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

	for dayOffset := 0; dayOffset < days; dayOffset++ {
		day := from.AddDate(0, 0, dayOffset)
		dir := tpl.Expand(day)

		if err := s.scanDir(
			ctx,
			category,
			dir,
			day,
			visit,
			&result,
		); err != nil {
			return result, err
		}
	}

	return result, nil
}

// scanDir 은 디렉터리 하나를 나열하여 일반 파일을 visit 에 전달하고,
// 발견한 진짜 폴더로 재귀한다.
//
// 반환 오류는 "전체 Scan 을 중단해야 하는 오류"(context 취소, visit 오류)
// 뿐이다. 나열 실패는 result 에 집계하고 nil 을 반환하여 형제 디렉터리와
// 다음 날짜의 순회를 계속한다.
func (s *Scanner) scanDir(
	ctx context.Context,
	category domain.Category,
	dir string,
	when time.Time,
	visit VisitFunc,
	result *Result,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf(
			"scan: canceled: %w",
			err,
		)
	}

	entries, err := s.lister.List(ctx, dir)

	// List 구현이 context 취소를 다른 형태의 오류로 감싸더라도
	// 취소는 일반적인 디렉터리 실패로 집계하지 않는다.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf(
			"scan: canceled at %q: %w",
			dir,
			ctxErr,
		)
	}

	switch {
	case errors.Is(err, fs.ErrNotExist):
		// 날짜 디렉터리가 없는 것은 해당 날짜 데이터 미생성으로
		// 정상이다. 하위 디렉터리가 나열과 하강 사이에 사라진 경우도
		// 같은 정책이다.
		result.Missing++
		return nil

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

		return nil
	}

	result.Dirs++

	// 종류별 선별 (PATH_DESIGN v3 §4·§5):
	//
	//	진짜 폴더    → 하강 대상 (Batch 로 넘기지 않는다)
	//	일반 파일    → Batch 로 전달
	//	그 외        → 건너뛰고 집계 (따라가지도, 파일로 취급하지도 않는다)
	//
	// 진짜 폴더는 IsDir 와 Type 이 함께 일치해야 한다. 단,
	// Type 을 채우지 않고 IsDir 만 세운 기존 테스트 fake 는
	// 호환을 위해 폴더로 본다. IsDir=true여도 Type이 링크면
	// 충돌하는 관측이므로 하강하지 않고 비정규로 집계한다.
	var (
		files   []Entry
		subdirs []Entry
	)

	for _, e := range entries {
		typeBits := e.Type & fs.ModeType
		isRealDir := e.IsDir && (typeBits == 0 || typeBits == fs.ModeDir)

		switch {
		case isRealDir:
			subdirs = append(subdirs, e)

		case e.IsRegular():
			files = append(files, e)

		default:
			result.Irregular++

			if len(result.IrregularDetails) < maxFailureDetails {
				result.IrregularDetails = append(
					result.IrregularDetails,
					IrregularEntry{
						Path: joinChild(dir, e.Name),
						Type: e.Type,
					},
				)
			}
		}
	}

	result.Files += len(files)

	// 현재 디렉터리의 파일을 먼저 넘긴 뒤 하위로 내려간다 (§3.2).
	if len(files) > 0 {
		batch := Batch{
			Category: category,
			Dir:      dir,
			When:     when,
			Entries:  files,
		}

		if err := visit(batch); err != nil {
			return fmt.Errorf(
				"scan: visit %q: %w",
				dir,
				err,
			)
		}
	}

	// DirLister 구현의 반환 순서에 기대지 않고 Scanner 가 직접
	// v3 §3.2의 하위 폴더 이름 오름차순 계약을 보장한다.
	// LocalLister(os.ReadDir)는 이미 이름순이지만, 향후 다른
	// DirLister가 같은 규칙을 우연히 지켜야 하게 두지 않는다.
	sort.Slice(subdirs, func(i, j int) bool {
		return subdirs[i].Name < subdirs[j].Name
	})

	for _, sub := range subdirs {
		child := joinChild(dir, sub.Name)

		if err := s.scanDir(
			ctx,
			category,
			child,
			when,
			visit,
			result,
		); err != nil {
			return err
		}
	}

	return nil
}

// joinChild 는 부모 디렉터리 경로에 하위 이름을 잇는다.
//
// filepath.Join 을 쓰지 않는 이유: filepath.Join 은 실행 OS 의 구분자로
// 경로 전체를 다시 쓰므로, config 템플릿이 사용한 표기(로그·테스트의
// 기대 문자열 포함)가 OS 에 따라 달라진다. 대신 부모 경로가 이미 사용한
// 구분자를 따른다. Windows 파일 API 는 '/' 도 허용하므로 혼합 표기
// 경로에서 '/' 로 이어도 동작에는 문제가 없다.
func joinChild(dir, name string) string {
	trimmed := strings.TrimRight(dir, `/\`)

	sep := "/"
	if strings.ContainsRune(trimmed, '\\') &&
		!strings.ContainsRune(trimmed, '/') {
		sep = `\`
	}

	return trimmed + sep + name
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

	// (HH) 는 제거된 토큰이다 (PATH_DESIGN v3 §1).
	//
	// 시각별 하위 디렉터리는 재귀가 흡수하므로 템플릿에 적을 이유가
	// 없고, 남아 있으면 자정(00)으로만 확장되어 조용히 일부만 스캔하게
	// 된다. 조용한 오동작 대신 여기서 시끄럽게 거부한다.
	//
	// 과도기 검사다: 다음 커밋에서 pathpl 의 TokenHH 지원 자체를
	// 삭제하면 (HH) 템플릿은 config 로드 단계에서 "알 수 없는 토큰"으로
	// 거부되어 이 지점에 도달할 수 없으므로, 이 검사도 그 커밋에서
	// 함께 삭제한다.
	if tpl.HasToken(pathpl.TokenHH) {
		return fmt.Errorf(
			"%w: (%s) 는 제거된 토큰입니다. 경로를 (DOY) 까지로 줄이면 그 아래는 자동으로 재귀 탐색합니다: %q",
			ErrInvalidInput,
			pathpl.TokenHH,
			tpl.String(),
		)
	}

	return nil
}
