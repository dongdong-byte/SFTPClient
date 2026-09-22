package scan

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

// fakeLister 는 Scanner 자체만 시험하기 위한 DirLister 이다.
//
// calls 에 실제 나열 요청 순서를 보관한다.
// fn 이 nil 이면 빈 디렉터리로 성공한다.
//
// 주의: 재귀 Scanner 는 IsDir Entry 를 만나면 그 하위 경로로 다시
// List 를 호출한다. fn 이 경로와 무관하게 폴더 Entry 를 반환하면
// 무한 재귀가 되므로, fn 은 반드시 dir 별로 응답을 나눠야 한다.
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

// treeLister 는 경로 → 항목 표로 파일 트리를 흉내 내는 DirLister 이다.
//
// 표에 없는 경로는 fs.ErrNotExist 다 — 실제 파일시스템과 같다.
type treeLister struct {
	calls []string
	tree  map[string][]Entry
	errs  map[string]error
}

func (f *treeLister) List(
	_ context.Context,
	dir string,
) ([]Entry, error) {
	f.calls = append(f.calls, dir)

	if err, ok := f.errs[dir]; ok {
		return nil, err
	}

	entries, ok := f.tree[dir]
	if !ok {
		return nil, fs.ErrNotExist
	}

	return entries, nil
}

func file(name string, size int64) Entry {
	return Entry{
		Name:  name,
		Size:  size,
		MTime: utcDate(2026, time.January, 1),
	}
}

func dir(name string) Entry {
	return Entry{
		Name:  name,
		IsDir: true,
		Type:  fs.ModeDir,
	}
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

// TestScannerScan_DailyTwoDays 는 평면 날짜 디렉터리(지리원형)가
// 기존과 동일하게 날짜당 1회 나열되는지 본다 (v3 §7.1 T2).
func TestScannerScan_DailyTwoDays(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/2026/001": {file("suwn0010.26o.gz", 100)},
			"/data/2026/002": {file("suwn0020.26o.gz", 100)},
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

// TestScannerScan_RecursesBelowDateDirectory 는 서울시형(날짜 아래
// (HH) 폴더)에서 재귀가 전부 수집하는지 본다 (v3 §7.1 T1).
//
// 폴더 Entry 는 Batch 로 넘어오지 않아야 하고(§4), 순회는
// "현재 폴더 파일 먼저 → 하위 폴더 이름 오름차순"(§3.2)이어야 한다.
func TestScannerScan_RecursesBelowDateDirectory(t *testing.T) {
	root := "/data/2026/001"

	lister := &treeLister{
		tree: map[string][]Entry{
			root: {
				// os.ReadDir 는 이름순으로 준다: 폴더 00, 07 이
				// 평면 파일보다 앞에 온다. 그래도 Batch 는
				// 파일 먼저다 (§3.2).
				dir("00"),
				dir("07"),
				file("flat001a.26o.gz", 10),
			},
			root + "/00": {file("aaaa001a.26o.gz", 20)},
			root + "/07": {
				file("bbbb001h.26o.gz", 30),
				file("cccc001h.26o.gz", 40),
			},
		},
	}

	scanner := New(lister)

	var batches []Batch

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(b Batch) error {
			batches = append(batches, b)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	wantCalls := []string{
		root,
		root + "/00",
		root + "/07",
	}

	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf(
			"List calls = %#v, want %#v",
			lister.calls,
			wantCalls,
		)
	}

	if result.Dirs != 3 {
		t.Errorf("Dirs = %d, want 3", result.Dirs)
	}

	// 폴더 2개는 Files 에 들어가지 않는다.
	if result.Files != 4 {
		t.Errorf("Files = %d, want 4", result.Files)
	}

	if len(batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(batches))
	}

	// 파일 먼저: 첫 Batch 는 날짜 폴더의 평면 파일이다.
	if batches[0].Dir != root {
		t.Errorf(
			"batches[0].Dir = %q, want %q (파일 먼저)",
			batches[0].Dir,
			root,
		)
	}

	if len(batches[0].Entries) != 1 ||
		batches[0].Entries[0].Name != "flat001a.26o.gz" {
		t.Errorf(
			"batches[0].Entries = %#v, want [flat001a.26o.gz]",
			batches[0].Entries,
		)
	}

	// 하위 폴더는 이름 오름차순이다.
	if batches[1].Dir != root+"/00" || batches[2].Dir != root+"/07" {
		t.Errorf(
			"subdir batch order = %q, %q; want 00 then 07",
			batches[1].Dir,
			batches[2].Dir,
		)
	}

	// 폴더 Entry 는 어느 Batch 에도 없다 (§4).
	for _, b := range batches {
		for _, e := range b.Entries {
			if e.IsDir {
				t.Errorf(
					"batch %q delivered dir entry %q",
					b.Dir,
					e.Name,
				)
			}
		}

		// 하위 Batch 의 When 도 날짜 자정이다.
		if !b.When.Equal(utcDate(2026, time.January, 1)) {
			t.Errorf(
				"batch %q When = %v, want day 00:00 UTC",
				b.Dir,
				b.When,
			)
		}
	}
}

// TestScannerScan_RecursesArbitraryDepth 는 더 깊은 임의 폴더
// (지질자원연형 SITE/주기 폴더 등)도 전부 수집하는지 본다 (v3 §7.1 T4).
//
// 파일이 없는 중간 폴더는 visit 되지 않지만 Dirs 에는 집계된다.
func TestScannerScan_RecursesArbitraryDepth(t *testing.T) {
	root := "/gnssdata/KIGAM/2026/001"

	lister := &treeLister{
		tree: map[string][]Entry{
			root:           {dir("HDBG")},
			root + "/HDBG": {dir("1s1h"), dir("30s1d")},
			root + "/HDBG/1s1h": {
				file("hdbg001a.26o.gz", 10),
			},
			root + "/HDBG/30s1d": {
				file("hdbg0010.26o.gz", 20),
			},
		},
	}

	scanner := New(lister)

	var batches []Batch

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, "/gnssdata/KIGAM/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(b Batch) error {
			batches = append(batches, b)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	wantCalls := []string{
		root,
		root + "/HDBG",
		root + "/HDBG/1s1h",
		root + "/HDBG/30s1d",
	}

	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf(
			"List calls = %#v, want %#v",
			lister.calls,
			wantCalls,
		)
	}

	if result.Dirs != 4 {
		t.Errorf("Dirs = %d, want 4", result.Dirs)
	}

	if result.Files != 2 {
		t.Errorf("Files = %d, want 2", result.Files)
	}

	// 파일 없는 폴더(날짜 폴더, HDBG)는 visit 되지 않는다.
	if len(batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(batches))
	}

	if batches[0].Dir != root+"/HDBG/1s1h" ||
		batches[1].Dir != root+"/HDBG/30s1d" {
		t.Errorf(
			"batch dirs = %q, %q; want 1s1h then 30s1d",
			batches[0].Dir,
			batches[1].Dir,
		)
	}
}

// TestScannerScan_IrregularEntriesSkippedAndCounted 는 링크·junction 등
// 비정규 항목이 후보로도, 하강 대상으로도 취급되지 않고 집계만
// 되는지 본다 (v3 §5, §7.1 T7 의 Scanner 몫).
func TestScannerScan_IrregularEntriesSkippedAndCounted(t *testing.T) {
	root := "/data/2026/001"

	lister := &treeLister{
		tree: map[string][]Entry{
			root: {
				file("suwn0010.26o.gz", 10),
				{
					Name: "link-to-archive",
					Type: fs.ModeSymlink,
				},
				{
					Name: "junction",
					Type: fs.ModeIrregular,
				},
			},
		},
	}

	scanner := New(lister)

	var batches []Batch

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Daily,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(b Batch) error {
			batches = append(batches, b)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	// 링크로 하강하지 않는다: List 호출은 날짜 폴더 1회뿐이다.
	if len(lister.calls) != 1 {
		t.Errorf(
			"List calls = %#v, want [%s] only",
			lister.calls,
			root,
		)
	}

	if len(batches) != 1 || len(batches[0].Entries) != 1 {
		t.Fatalf(
			"batches = %#v, want 1 batch with 1 regular file",
			batches,
		)
	}

	if batches[0].Entries[0].Name != "suwn0010.26o.gz" {
		t.Errorf(
			"delivered = %q, want suwn0010.26o.gz",
			batches[0].Entries[0].Name,
		)
	}

	if result.Files != 1 {
		t.Errorf("Files = %d, want 1 (비정규 미포함)", result.Files)
	}

	if result.Irregular != 2 {
		t.Errorf("Irregular = %d, want 2", result.Irregular)
	}

	if len(result.IrregularDetails) != 2 {
		t.Fatalf(
			"IrregularDetails = %#v, want 2",
			result.IrregularDetails,
		)
	}

	wantDetail := IrregularEntry{
		Path: root + "/link-to-archive",
		Type: fs.ModeSymlink,
	}

	if result.IrregularDetails[0] != wantDetail {
		t.Errorf(
			"IrregularDetails[0] = %#v, want %#v",
			result.IrregularDetails[0],
			wantDetail,
		)
	}
}

// TestScannerScan_MissingDirectoriesAreNormal 은 없는 날짜 폴더와,
// 나열과 하강 사이에 사라진 하위 폴더가 모두 Missing 으로 집계되고
// 순회가 계속되는지 본다.
func TestScannerScan_MissingDirectoriesAreNormal(t *testing.T) {
	day2 := "/data/2026/002"

	lister := &treeLister{
		tree: map[string][]Entry{
			// 001 은 표에 없다 → fs.ErrNotExist (날짜 미생성).
			day2: {
				dir("gone"), // 표에 없다 → 하강 시 fs.ErrNotExist
				file("suwn0020.26o.gz", 10),
			},
		},
	}

	scanner := New(lister)

	visited := 0

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Daily,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 2),
		},
		func(b Batch) error {
			visited++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	if result.Missing != 2 {
		t.Errorf("Missing = %d, want 2 (날짜 1 + 하위 1)", result.Missing)
	}

	if result.Dirs != 1 {
		t.Errorf("Dirs = %d, want 1", result.Dirs)
	}

	if result.Files != 1 {
		t.Errorf("Files = %d, want 1", result.Files)
	}

	if visited != 1 {
		t.Errorf("visit count = %d, want 1", visited)
	}
}

// TestScannerScan_SubdirListFailureIsCollectedAndScanContinues 는
// 하위 폴더 나열 실패가 형제 폴더와 나머지 순회를 막지 않는지 본다
// (v3 §1-9: 하위 폴더도 DirError 계속).
func TestScannerScan_SubdirListFailureIsCollectedAndScanContinues(t *testing.T) {
	permissionErr := errors.New("permission denied")

	root := "/data/2026/001"

	lister := &treeLister{
		tree: map[string][]Entry{
			root: {
				dir("aa"),
				dir("bb"),
				file("flat0010.26o.gz", 10),
			},
			root + "/bb": {file("suwn0010.26o.gz", 20)},
		},
		errs: map[string]error{
			root + "/aa": permissionErr,
		},
	}

	scanner := New(lister)

	var batches []Batch

	result, err := scanner.Scan(
		context.Background(),
		domain.CategoryRINEX2Daily,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(b Batch) error {
			batches = append(batches, b)
			return nil
		},
	)
	if err != nil {
		t.Fatalf(
			"directory failures must not stop Scan: %v",
			err,
		)
	}

	if result.Errs != 1 {
		t.Errorf("Errs = %d, want 1", result.Errs)
	}

	if len(result.Failures) != 1 ||
		!errors.Is(result.Failures[0], permissionErr) {
		t.Fatalf(
			"Failures = %#v, want [permission denied at aa]",
			result.Failures,
		)
	}

	if result.Failures[0].Dir != root+"/aa" {
		t.Errorf(
			"Failures[0].Dir = %q, want %q",
			result.Failures[0].Dir,
			root+"/aa",
		)
	}

	// 실패한 aa 뒤의 bb 는 정상 순회된다.
	if len(batches) != 2 ||
		batches[0].Dir != root ||
		batches[1].Dir != root+"/bb" {
		t.Errorf(
			"batches = %#v, want [root flat, bb]",
			batches,
		)
	}

	if result.Dirs != 2 {
		t.Errorf("Dirs = %d, want 2 (root + bb)", result.Dirs)
	}
}

// TestScannerScan_EntriesAreDeliveredUnmodified 는 Batch 로 전달되는
// 일반 파일 Entry 가 값 그대로인지 본다. 0바이트·.part 도 scan 은
// 거르지 않는다 — 그 판정은 verify 의 것이다.
func TestScannerScan_EntriesAreDeliveredUnmodified(t *testing.T) {
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
		},
		{
			Name:  "zero.rnx.gz",
			Size:  0,
			MTime: mtime,
		},
		{
			Name:  "upload.part",
			Size:  55,
			MTime: mtime,
		},
	}

	root := "/data/2026/001"

	entries := append([]Entry{}, wantEntries...)
	entries = append(entries, dir("subdir"))

	lister := &treeLister{
		tree: map[string][]Entry{
			root:             entries,
			root + "/subdir": {},
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

	// subdir 는 비어 있으므로 visit 는 날짜 폴더 1회뿐이다.
	if visited != 1 {
		t.Fatalf("visit count = %d, want 1", visited)
	}

	if !reflect.DeepEqual(got.Entries, wantEntries) {
		t.Errorf(
			"Entries changed:\ngot  %#v\nwant %#v",
			got.Entries,
			wantEntries,
		)
	}

	// Files 는 Batch 로 전달한 일반 파일 수다. 폴더는 세지 않는다.
	if result.Files != 3 {
		t.Errorf(
			"Files = %d, want 3",
			result.Files,
		)
	}

	if result.Dirs != 2 {
		t.Errorf("Dirs = %d, want 2 (빈 subdir 포함)", result.Dirs)
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

// TestScannerScan_VisitErrorStopsImmediately 는 visit 오류가
// 재귀 도중에도 즉시 전체 Scan 을 중단하는지 본다.
func TestScannerScan_VisitErrorStopsImmediately(t *testing.T) {
	visitErr := errors.New("ledger unavailable")

	root := "/data/2026/001"

	lister := &treeLister{
		tree: map[string][]Entry{
			root: {
				dir("aa"),
				dir("bb"),
				file("flat0010.26o.gz", 1),
			},
			root + "/aa": {file("aaaa0010.26o.gz", 1)},
			root + "/bb": {file("bbbb0010.26o.gz", 1)},
		},
	}

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

	// 두 번째 visit(aa) 오류에서 즉시 중단해야 한다. bb 는 나열되지 않는다.
	wantCalls := []string{root, root + "/aa"}
	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf(
			"List calls = %#v, want %#v",
			lister.calls,
			wantCalls,
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

// TestScannerScan_HourlyIsOneListPerDay 는 Hourly 도 날짜당 1회만
// 나열하는지 본다. (HH) 계산이 사라졌으므로 이것이 유일한 Hourly
// 순회 방식이다. 시각별 하위 폴더가 실제로 있으면 재귀가 줍는다.
//
// When 을 단언하는 이유: 전송 단계가 RemotePath.Expand(When) 에 이 값을
// 쓴다. 표준 원격은 (HH) 가 없어 시각이 소비되지 않지만, 규약이
// 문서로만 존재하면 조용히 바뀔 수 있으므로 테스트로 고정한다.
func TestScannerScan_HourlyIsOneListPerDay(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/2026/001": {file("ansg001a.26o.zip", 1024)},
			"/data/2026/002": {file("ansg002a.26o.zip", 1024)},
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

	// 이틀 × 하루 1회 = 2회. 24배가 되지 않는다.
	if len(lister.calls) != 2 {
		t.Errorf(
			"List calls = %d, want 2 (hourly = 1 list/day)",
			len(lister.calls),
		)
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

	// When 은 해당 날짜 00:00 UTC 다 (Batch.When 규약).
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

// TestJoinChild 는 부모 경로의 구분자 표기를 따르는 경로 결합을 본다.
func TestJoinChild(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		sub  string
		want string
	}{
		{
			name: "posix",
			dir:  "/data/2026/001",
			sub:  "00",
			want: "/data/2026/001/00",
		},
		{
			name: "posix 뒤 구분자",
			dir:  "/data/2026/001/",
			sub:  "00",
			want: "/data/2026/001/00",
		},
		{
			name: "windows",
			dir:  `C:\RINEX-V2-H\2026\001`,
			sub:  "00",
			want: `C:\RINEX-V2-H\2026\001\00`,
		},
		{
			name: "windows 뒤 구분자",
			dir:  `C:\RINEX-V2-H\2026\001\`,
			sub:  "00",
			want: `C:\RINEX-V2-H\2026\001\00`,
		},
		{
			name: "UNC",
			dir:  `\\192.168.10.5\Data\2026\001`,
			sub:  "SUWN",
			want: `\\192.168.10.5\Data\2026\001\SUWN`,
		},
		{
			name: "혼합 표기는 / 로",
			dir:  `D:\stage/2026/001`,
			sub:  "00",
			want: `D:\stage/2026/001/00`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := joinChild(tt.dir, tt.sub); got != tt.want {
				t.Errorf(
					"joinChild(%q, %q) = %q, want %q",
					tt.dir,
					tt.sub,
					got,
					tt.want,
				)
			}
		})
	}
}

func safeAt(s []string, i int) string {
	if i < 0 || i >= len(s) {
		return "<none>"
	}
	return s[i]
}
