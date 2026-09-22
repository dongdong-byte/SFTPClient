package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

// fakeLister 는 Scanner 자체만 시험하기 위한 DirLister 이다.
//
// calls 에 실제 나열 요청 순서를 보관한다.
// fn 이 nil 이면 빈 디렉터리로 성공한다.
type fakeLister struct {
	calls []string
	fn    func(ctx context.Context, dir string) ([]Entry, error)
}

func (f *fakeLister) List(
	ctx context.Context,
	dir string,
) ([]Entry, error) {
	f.calls = append(f.calls, dir)

	if f.fn == nil {
		return nil, nil
	}

	return f.fn(ctx, dir)
}

func mustScanTemplate(t *testing.T, value string) *pathpl.Template {
	t.Helper()

	tpl, err := pathpl.Parse(value)
	if err != nil {
		t.Fatalf("pathpl.Parse(%q): %v", value, err)
	}

	return tpl
}

func utcDate(year int, month time.Month, day int) time.Time {
	return time.Date(
		year,
		month,
		day,
		0,
		0,
		0,
		0,
		time.UTC,
	)
}

func TestRangeDays(t *testing.T) {
	tests := []struct {
		name string
		r    Range
		want int
	}{
		{
			name: "하루",
			r: Range{
				From: utcDate(2026, time.January, 1),
				To:   utcDate(2026, time.January, 1),
			},
			want: 1,
		},
		{
			name: "양끝 포함 2일",
			r: Range{
				From: utcDate(2026, time.January, 1),
				To:   utcDate(2026, time.January, 2),
			},
			want: 2,
		},
		{
			name: "시각 성분 제거",
			r: Range{
				From: time.Date(
					2026, time.January, 1,
					14, 30, 0, 0,
					time.UTC,
				),
				To: time.Date(
					2026, time.January, 2,
					1, 10, 0, 0,
					time.UTC,
				),
			},
			want: 2,
		},
		{
			name: "역순",
			r: Range{
				From: utcDate(2026, time.January, 2),
				To:   utcDate(2026, time.January, 1),
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.Days(); got != tt.want {
				t.Errorf(
					"Range.Days() = %d, want %d",
					got,
					tt.want,
				)
			}
		})
	}
}

func TestScannerScan_DailyTwoDays(t *testing.T) {
	lister := &fakeLister{
		fn: func(
			ctx context.Context,
			dir string,
		) ([]Entry, error) {
			return []Entry{
				{
					Name:  "sample.rnx.gz",
					Size:  100,
					MTime: utcDate(2026, time.January, 1),
				},
			}, nil
		},
	}

	scanner := New(lister)

	tpl := mustScanTemplate(
		t,
		"/data/(YYYY)/(DOY)",
	)

	r := Range{
		From: utcDate(2026, time.January, 1),
		To:   utcDate(2026, time.January, 2),
	}

	var batches []Batch

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX3Daily,
		tpl,
		r,
		func(batch Batch) error {
			batches = append(batches, batch)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	wantCalls := []string{
		"/data/2026/001",
		"/data/2026/002",
	}

	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf(
			"List calls = %#v, want %#v",
			lister.calls,
			wantCalls,
		)
	}

	if result.Dirs != 2 {
		t.Errorf("Dirs = %d, want 2", result.Dirs)
	}

	if result.Files != 2 {
		t.Errorf("Files = %d, want 2", result.Files)
	}

	if result.Missing != 0 {
		t.Errorf("Missing = %d, want 0", result.Missing)
	}

	if result.Errs != 0 {
		t.Errorf("Errs = %d, want 0", result.Errs)
	}

	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}

	if batches[0].Category != domain.CategoryRINEX3Daily {
		t.Errorf(
			"batch category = %q, want %q",
			batches[0].Category,
			domain.CategoryRINEX3Daily,
		)
	}

	if !batches[0].When.Equal(
		utcDate(2026, time.January, 1),
	) {
		t.Errorf(
			"first When = %v, want 2026-01-01 UTC",
			batches[0].When,
		)
	}

	if !batches[1].When.Equal(
		utcDate(2026, time.January, 2),
	) {
		t.Errorf(
			"second When = %v, want 2026-01-02 UTC",
			batches[1].When,
		)
	}
}

// hourSubdirs 는 날짜 디렉터리 아래 00~23 시각 폴더 항목을 만든다
// (서울시형 원본 배치).
func hourSubdirs() []Entry {
	subdirs := make([]Entry, 0, 24)

	for h := 0; h < 24; h++ {
		subdirs = append(subdirs, Entry{
			Name:  fmt.Sprintf("%02d", h),
			IsDir: true,
			Type:  fs.ModeDir,
		})
	}

	return subdirs
}

// isDateDir 는 fake 경로가 날짜 디렉터리(/data/YYYY/DOY)인지 본다.
func isDateDir(dir string) bool {
	return strings.Count(dir, "/") == 3
}

// TestScannerScan_HourlySubdirsAreRecursed 는 (HH) 없이 (DOY) 까지 적은
// 템플릿에서 시각 하위 폴더가 재귀로 수집되는지 본다 (PATH_DESIGN v3 §1).
// 날짜당 나열 = 날짜 폴더 1 + 시각 폴더 24.
func TestScannerScan_HourlySubdirsAreRecursed(t *testing.T) {
	lister := &fakeLister{
		fn: func(
			ctx context.Context,
			dir string,
		) ([]Entry, error) {
			if isDateDir(dir) {
				return hourSubdirs(), nil
			}

			return []Entry{
				{
					Name:  "hourly.rnx.gz",
					Size:  10,
					MTime: time.Now(),
				},
			}, nil
		},
	}

	scanner := New(lister)

	var batches []Batch

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 2),
		},
		func(batch Batch) error {
			batches = append(batches, batch)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	if len(lister.calls) != 50 {
		t.Fatalf("List calls = %d, want 50 (2일 × (1 + 24))", len(lister.calls))
	}

	tests := []struct {
		index int
		want  string
	}{
		{0, "/data/2026/001"},
		{1, "/data/2026/001/00"},
		{24, "/data/2026/001/23"},
		{25, "/data/2026/002"},
		{49, "/data/2026/002/23"},
	}

	for _, tt := range tests {
		if got := lister.calls[tt.index]; got != tt.want {
			t.Errorf("calls[%d] = %q, want %q", tt.index, got, tt.want)
		}
	}

	if result.Dirs != 50 || result.Files != 48 {
		t.Errorf(
			"result = {Dirs:%d Files:%d}, want {Dirs:50 Files:48}",
			result.Dirs,
			result.Files,
		)
	}

	if len(batches) != 48 {
		t.Fatalf("batches = %d, want 48", len(batches))
	}

	// 하위 폴더 Batch 도 날짜 00:00 UTC 를 갖는다 (PATH_DESIGN v3 §1.2).
	if !batches[47].When.Equal(utcDate(2026, time.January, 2)) {
		t.Errorf("last When = %v, want 2026-01-02 00:00 UTC", batches[47].When)
	}
}

func TestScannerScan_MissingDirectoryIsNormal(t *testing.T) {
	lister := &fakeLister{
		fn: func(
			ctx context.Context,
			dir string,
		) ([]Entry, error) {
			if dir == "/data/2026/001" {
				return nil, fs.ErrNotExist
			}

			return nil, nil
		},
	}

	scanner := New(lister)

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 2),
		},
		func(batch Batch) error {
			t.Fatal("empty directories must not call visit")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	if len(lister.calls) != 2 {
		t.Errorf("List calls = %d, want 2", len(lister.calls))
	}

	if result.Dirs != 1 {
		t.Errorf("Dirs = %d, want 1", result.Dirs)
	}

	if result.Missing != 1 {
		t.Errorf("Missing = %d, want 1", result.Missing)
	}

	if result.Errs != 0 {
		t.Errorf("Errs = %d, want 0", result.Errs)
	}
}

func TestScannerScan_ListFailureIsCollectedAndScanContinues(t *testing.T) {
	permissionErr := errors.New("permission denied")
	ioErr := errors.New("network read error")

	lister := &fakeLister{
		fn: func(
			ctx context.Context,
			dir string,
		) ([]Entry, error) {
			switch {
			case isDateDir(dir):
				return hourSubdirs(), nil

			case dir == "/data/2026/001/03":
				return nil, permissionErr

			case dir == "/data/2026/001/17":
				return nil, ioErr

			default:
				return nil, nil
			}
		},
	}

	scanner := New(lister)

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(batch Batch) error {
			return nil
		},
	)
	if err != nil {
		t.Fatalf(
			"directory failures must not stop Scan: %v",
			err,
		)
	}

	// 하위 폴더 두 곳이 실패해도 형제 폴더를 끝까지 간다.
	if len(lister.calls) != 25 {
		t.Errorf(
			"List calls = %d, want 25",
			len(lister.calls),
		)
	}

	// 날짜 폴더 1 + 성공한 시각 폴더 22.
	if result.Dirs != 23 {
		t.Errorf("Dirs = %d, want 23", result.Dirs)
	}

	if result.Errs != 2 {
		t.Errorf("Errs = %d, want 2", result.Errs)
	}

	if len(result.Failures) != 2 {
		t.Fatalf(
			"Failures = %d, want 2",
			len(result.Failures),
		)
	}

	if !errors.Is(result.Failures[0], permissionErr) {
		t.Errorf(
			"Failures[0] = %v, want permissionErr",
			result.Failures[0],
		)
	}

	if !errors.Is(result.Failures[1], ioErr) {
		t.Errorf(
			"Failures[1] = %v, want ioErr",
			result.Failures[1],
		)
	}
}

func TestScannerScan_DoesNotFilterEntries(t *testing.T) {
	mtime := time.Date(
		2026,
		time.January,
		1,
		1,
		2,
		3,
		0,
		time.UTC,
	)

	wantEntries := []Entry{
		{
			Name:  "normal.rnx.gz",
			Size:  100,
			MTime: mtime,
			IsDir: false,
		},
		{
			Name:  "zero.rnx.gz",
			Size:  0,
			MTime: mtime,
			IsDir: false,
		},
		{
			Name:  "upload.part",
			Size:  55,
			MTime: mtime,
			IsDir: false,
		},
		{
			Name:  "subdir",
			Size:  0,
			MTime: mtime,
			IsDir: true,
		},
	}

	// 하위 폴더는 비어 있다. 모든 경로에 같은 항목을 돌려주면 "subdir"
	// 아래에 다시 "subdir" 가 있는 무한 트리가 되어 재귀가 끝나지 않는다.
	lister := &fakeLister{
		fn: func(
			ctx context.Context,
			dir string,
		) ([]Entry, error) {
			if dir == "/data/2026/001" {
				return wantEntries, nil
			}

			return nil, nil
		},
	}

	scanner := New(lister)

	var got Batch
	visited := 0

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX3Daily,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(batch Batch) error {
			visited++
			got = batch
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	if visited != 1 {
		t.Fatalf("visit count = %d, want 1", visited)
	}

	// 0바이트·.part 는 거르지 않는다(verify 의 책임). 재귀로 소비한
	// 폴더만 Batch 에서 빠진다 (PATH_DESIGN v3 §4).
	wantFiles := wantEntries[:3]

	if !reflect.DeepEqual(got.Entries, wantFiles) {
		t.Errorf(
			"Entries changed:\ngot  %#v\nwant %#v",
			got.Entries,
			wantFiles,
		)
	}

	if result.Files != 3 {
		t.Errorf(
			"Files = %d, want 3",
			result.Files,
		)
	}

	if len(lister.calls) != 2 {
		t.Errorf("List calls = %d, want 2 (날짜 폴더 + subdir)", len(lister.calls))
	}
}

func TestScannerScan_EmptyDirectoryDoesNotVisit(t *testing.T) {
	lister := &fakeLister{}

	scanner := New(lister)

	visited := 0

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Daily,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(batch Batch) error {
			visited++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	if visited != 0 {
		t.Errorf(
			"visit count = %d, want 0",
			visited,
		)
	}

	if result.Dirs != 1 {
		t.Errorf("Dirs = %d, want 1", result.Dirs)
	}

	if result.Files != 0 {
		t.Errorf("Files = %d, want 0", result.Files)
	}
}

func TestScannerScan_VisitErrorStopsImmediately(t *testing.T) {
	visitErr := errors.New("ledger unavailable")

	// 날짜 폴더와 시각 폴더 하나에 각각 파일이 있다. 두 번째 Batch
	// (하위 폴더)에서 visit 오류가 나면 재귀 도중에도 즉시 멈춰야 한다.
	lister := &fakeLister{
		fn: func(
			ctx context.Context,
			dir string,
		) ([]Entry, error) {
			entries := []Entry{
				{
					Name: "sample.rnx.gz",
					Size: 1,
				},
			}

			if isDateDir(dir) {
				entries = append(entries, hourSubdirs()...)
			}

			return entries, nil
		},
	}

	scanner := New(lister)

	visited := 0

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(batch Batch) error {
			visited++

			if visited == 2 {
				return visitErr
			}

			return nil
		},
	)

	if !errors.Is(err, visitErr) {
		t.Fatalf(
			"Scan() error = %v, want visitErr",
			err,
		)
	}

	if visited != 2 {
		t.Errorf(
			"visit count = %d, want 2",
			visited,
		)
	}

	// 두 번째 callback 오류에서 즉시 중단해야 한다.
	if len(lister.calls) != 2 {
		t.Errorf(
			"List calls = %d, want 2",
			len(lister.calls),
		)
	}

	// 두 디렉터리는 실제 나열에 성공했으므로 부분 집계에는 포함된다.
	if result.Dirs != 2 {
		t.Errorf(
			"Dirs = %d, want 2",
			result.Dirs,
		)
	}

	if result.Files != 2 {
		t.Errorf(
			"Files = %d, want 2",
			result.Files,
		)
	}
}

func TestScannerScan_ContextCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lister := &fakeLister{}
	scanner := New(lister)

	_, err := scanner.Scan(
		ctx,
		domain.CategoryRINEX3Daily,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(batch Batch) error {
			return nil
		},
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"Scan() error = %v, want context.Canceled",
			err,
		)
	}

	if len(lister.calls) != 0 {
		t.Errorf(
			"List called %d times, want 0",
			len(lister.calls),
		)
	}
}

func TestScannerScan_InvalidInput(t *testing.T) {
	validRange := Range{
		From: utcDate(2026, time.January, 1),
		To:   utcDate(2026, time.January, 1),
	}

	dailyTpl := mustScanTemplate(
		t,
		"/data/(YYYY)/(DOY)",
	)

	hourlyTpl := mustScanTemplate(
		t,
		"/data/(YYYY)/(DOY)/(HH)",
	)

	visit := func(Batch) error {
		return nil
	}

	tests := []struct {
		name     string
		ctx      context.Context
		scanner  *Scanner
		category domain.Category
		tpl      *pathpl.Template
		r        Range
		visit    VisitFunc
	}{
		{
			name:     "nil context",
			ctx:      nil,
			scanner:  New(&fakeLister{}),
			category: domain.CategoryRINEX3Daily,
			tpl:      dailyTpl,
			r:        validRange,
			visit:    visit,
		},
		{
			name:     "nil Scanner",
			ctx:      context.Background(),
			scanner:  nil,
			category: domain.CategoryRINEX3Daily,
			tpl:      dailyTpl,
			r:        validRange,
			visit:    visit,
		},
		{
			name: "nil lister",
			ctx:  context.Background(),
			scanner: &Scanner{
				lister: nil,
			},
			category: domain.CategoryRINEX3Daily,
			tpl:      dailyTpl,
			r:        validRange,
			visit:    visit,
		},
		{
			name:     "nil visit",
			ctx:      context.Background(),
			scanner:  New(&fakeLister{}),
			category: domain.CategoryRINEX3Daily,
			tpl:      dailyTpl,
			r:        validRange,
			visit:    nil,
		},
		{
			name:     "알 수 없는 Category",
			ctx:      context.Background(),
			scanner:  New(&fakeLister{}),
			category: domain.Category("RINEX5_DAILY"),
			tpl:      dailyTpl,
			r:        validRange,
			visit:    visit,
		},
		{
			name:     "nil template",
			ctx:      context.Background(),
			scanner:  New(&fakeLister{}),
			category: domain.CategoryRINEX3Daily,
			tpl:      nil,
			r:        validRange,
			visit:    visit,
		},
		{
			name:     "초기화되지 않은 Range",
			ctx:      context.Background(),
			scanner:  New(&fakeLister{}),
			category: domain.CategoryRINEX3Daily,
			tpl:      dailyTpl,
			r:        Range{},
			visit:    visit,
		},
		{
			name:     "역순 Range",
			ctx:      context.Background(),
			scanner:  New(&fakeLister{}),
			category: domain.CategoryRINEX3Daily,
			tpl:      dailyTpl,
			r: Range{
				From: utcDate(2026, time.January, 2),
				To:   utcDate(2026, time.January, 1),
			},
			visit: visit,
		},
		{
			name:     "Daily인데 HH 있음",
			ctx:      context.Background(),
			scanner:  New(&fakeLister{}),
			category: domain.CategoryRINEX3Daily,
			tpl:      hourlyTpl,
			r:        validRange,
			visit:    visit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.scanner.Scan(
				tt.ctx,
				tt.category,
				tt.tpl,
				tt.r,
				tt.visit,
			)

			if err == nil {
				t.Fatal("expected error")
			}

			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf(
					"error = %v, want ErrInvalidInput",
					err,
				)
			}
		})
	}
}

// TestScannerScan_HourlyFlatMakesOneDirectoryPerDay 는 하위 폴더가 없는
// Hourly 경로(지리원형 평면)에서 Scanner 가 날짜당 1회만 나열하고,
// Batch.When 이 해당 날짜 00:00 UTC 인지 본다.
//
// When 을 단언하는 이유: 전송 단계가 RemotePath.Expand(When) 에 이 값을
// 쓴다. flat 원격에는 (HH) 가 없어 시각이 소비되지 않지만, 규약이
// 문서로만 존재하면 조용히 바뀔 수 있으므로 테스트로 고정한다.
func TestScannerScan_HourlyFlatMakesOneDirectoryPerDay(t *testing.T) {
	lister := &fakeLister{
		fn: func(_ context.Context, _ string) ([]Entry, error) {
			return []Entry{
				{
					Name:  "ansg001a.26z.zip",
					Size:  1024,
					MTime: utcDate(2026, time.January, 1),
				},
			}, nil
		},
	}

	scanner := New(lister)

	var whens []time.Time

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 2),
		},
		func(b Batch) error {
			whens = append(whens, b.When)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	// 이틀 × 하루 1회 = 2회. (HH) 가 없으므로 24배가 되지 않는다.
	if len(lister.calls) != 2 {
		t.Errorf("List calls = %d, want 2 (flat hourly = 1 dir/day)", len(lister.calls))
	}

	wantDirs := []string{"/data/2026/001", "/data/2026/002"}
	for i, want := range wantDirs {
		if i >= len(lister.calls) || lister.calls[i] != want {
			t.Errorf("call[%d] = %q, want %q", i, safeAt(lister.calls, i), want)
		}
	}

	if result.Dirs != 2 || result.Files != 2 {
		t.Errorf(
			"result = {Dirs:%d Files:%d}, want {Dirs:2 Files:2}",
			result.Dirs,
			result.Files,
		)
	}

	// flat 의 When 은 해당 날짜 00:00 UTC 다 (Batch.When 규약).
	wantWhens := []time.Time{
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 2),
	}

	if len(whens) != len(wantWhens) {
		t.Fatalf("visited batches = %d, want %d", len(whens), len(wantWhens))
	}

	for i, want := range wantWhens {
		if !whens[i].Equal(want) {
			t.Errorf(
				"When[%d] = %v, want %v (day 00:00 UTC)",
				i,
				whens[i],
				want,
			)
		}
	}
}

func safeAt(s []string, i int) string {
	if i < 0 || i >= len(s) {
		return "<none>"
	}
	return s[i]
}
