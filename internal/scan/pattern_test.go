package scan

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
)

// 경로 토큰 커밋 5 — 패턴 단에서 부모를 나열해 루트를 여러 개 만든다.
//
// 파일 토큰이 없는 템플릿의 동작이 바뀌지 않았다는 것(I2)은 scan_test.go
// 의 기존 테스트 전부가 수정 없이 통과하는 것으로 증명한다. 여기서는
// 파일 토큰이 있는 경우만 본다.

func link(name string) Entry {
	return Entry{
		Name: name,
		Type: fs.ModeSymlink,
	}
}

func scanCollect(
	t *testing.T,
	lister DirLister,
	tmpl string,
	from, to time.Time,
) (Result, []Batch) {
	t.Helper()

	var batches []Batch

	result, err := New(lister).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, tmpl),
		Range{From: from, To: to},
		func(b Batch) error {
			batches = append(batches, b)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	return result, batches
}

func fileNames(batches []Batch) []string {
	var names []string
	for _, b := range batches {
		for _, e := range b.Entries {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	return names
}

// 관측소가 날짜 위: 부모 "/data/" 를 한 번 나열해 관측소 폴더마다
// 루트를 갈라 낸다. 패턴 밖 폴더(D3)·링크(R4)·부모에 놓인 파일은 각각
// 건너뛴다. 이틀 스캔에서 부모는 한 번만 나열한다 (R3 캐시).
func TestScannerScan_SiteAboveDate(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {
				dir("backup"),
				dir("DBON"),
				link("LINK"),
				file("readme.txt", 1),
				dir("suw1"),
			},
			"/data/DBON/2026/001/": {file("dbon001a.26o.gz", 10)},
			"/data/DBON/2026/002/": {file("dbon002a.26o.gz", 11)},
			"/data/suw1/2026/001/": {file("suw1001a.26o.gz", 20)},
			// suw1 의 002 는 없다 → Missing
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 2),
	)

	wantCalls := []string{
		"/data/",
		"/data/DBON/2026/001/",
		"/data/suw1/2026/001/",
		"/data/DBON/2026/002/",
		"/data/suw1/2026/002/",
	}
	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf("List calls = %#v, want %#v", lister.calls, wantCalls)
	}

	wantFiles := []string{"dbon001a.26o.gz", "dbon002a.26o.gz", "suw1001a.26o.gz"}
	if got := fileNames(batches); !reflect.DeepEqual(got, wantFiles) {
		t.Errorf("files = %v, want %v", got, wantFiles)
	}

	if result.Files != 3 {
		t.Errorf("Files = %d, want 3", result.Files)
	}
	if result.Dirs != 4 { // /data/ + 성공한 루트 3
		t.Errorf("Dirs = %d, want 4", result.Dirs)
	}
	if result.Missing != 1 {
		t.Errorf("Missing = %d, want 1", result.Missing)
	}
	if result.Unmatched != 1 || !reflect.DeepEqual(result.UnmatchedDetails, []string{"/data/backup"}) {
		t.Errorf("Unmatched = %d %v, want 1 [/data/backup]", result.Unmatched, result.UnmatchedDetails)
	}
	if result.Irregular != 1 || result.IrregularDetails[0].Path != "/data/LINK" {
		t.Errorf("Irregular = %d %v, want 1 [/data/LINK]", result.Irregular, result.IrregularDetails)
	}

	// 관측소 폴더는 소문자여도 따라간다. 경로는 디스크의 표기 그대로다.
	for _, b := range batches {
		if b.Dir == "/data/SUW1/2026/001/" {
			t.Error("경로는 디스크 표기(suw1)를 유지해야 한다")
		}
	}
}

// 관측소가 날짜 아래: 부모가 날짜 폴더라 날마다 다르다. 캐시가 맞지 않을
// 뿐 결과는 같다.
func TestScannerScan_SiteBelowDate(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/2026/001/": {
				dir("DBON"),
				dir("SUW1"),
				file("flat001a.26o.gz", 5), // 날짜 폴더 바로 아래 파일은 수집하지 않는다
			},
			"/data/2026/001/DBON/": {file("dbon001a.26o.gz", 10)},
			"/data/2026/001/SUW1/": {file("suw1001a.26o.gz", 20)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(YYYY)/(DOY)/(SITE)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	wantCalls := []string{
		"/data/2026/001/",
		"/data/2026/001/DBON/",
		"/data/2026/001/SUW1/",
	}
	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf("List calls = %#v, want %#v", lister.calls, wantCalls)
	}

	wantFiles := []string{"dbon001a.26o.gz", "suw1001a.26o.gz"}
	if got := fileNames(batches); !reflect.DeepEqual(got, wantFiles) {
		t.Errorf("files = %v, want %v", got, wantFiles)
	}
	if result.Files != 2 {
		t.Errorf("Files = %d, want 2", result.Files)
	}
}

// D1 의 근거: 서울시형 "(DOY)\(HH)\" 는 "(DOY)\" + 재귀 수집과 같은 파일
// 집합을 모은다. 옛 (HH) 설정을 다시 받아도 결과가 같다.
//
// 단 조건이 있다 — DOY 폴더 아래가 00~23 폴더뿐일 때만 같다. 재귀는 모으고
// (HH) 패턴은 모으지 않는 것이 두 가지다:
//
//	날짜 폴더 바로 아래의 평면 파일   (패턴 단의 부모 파일은 날짜 트리 밖으로 본다)
//	00~23 이 아닌 폴더 안의 파일      (backup 등, D3 로 건너뜀)
//
// 이 테스트는 두 번째 차이를 backup 폴더로 명시한다. 첫 번째는
// TestScannerScan_SiteBelowDate 의 flat 파일이 보인다.
func TestScannerScan_HourPatternEqualsRecursion(t *testing.T) {
	tree := map[string][]Entry{
		`C:\RINEX-V2-H\2026\001\`: {
			dir("00"),
			dir("13"),
			dir("23"),
			dir("backup"),
		},
		// 패턴 경로 (루트에 끝 구분자 보존)
		`C:\RINEX-V2-H\2026\001\00\`: {file("aaaa001a.26o.gz", 1)},
		`C:\RINEX-V2-H\2026\001\13\`: {file("aaaa001n.26o.gz", 2), file("bbbb001n.26o.gz", 3)},
		`C:\RINEX-V2-H\2026\001\23\`: {file("aaaa001x.26o.gz", 4)},
		// 재귀 경로 (joinChild 는 끝 구분자를 떼고 잇는다)
		`C:\RINEX-V2-H\2026\001\00`:     {file("aaaa001a.26o.gz", 1)},
		`C:\RINEX-V2-H\2026\001\13`:     {file("aaaa001n.26o.gz", 2), file("bbbb001n.26o.gz", 3)},
		`C:\RINEX-V2-H\2026\001\23`:     {file("aaaa001x.26o.gz", 4)},
		`C:\RINEX-V2-H\2026\001\backup`: {file("old.26o.gz", 9)},
	}

	day := utcDate(2026, time.January, 1)

	pat, patBatches := scanCollect(
		t, &treeLister{tree: tree}, `C:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\`, day, day,
	)
	rec, recBatches := scanCollect(
		t, &treeLister{tree: tree}, `C:\RINEX-V2-H\(YYYY)\(DOY)\`, day, day,
	)

	// backup 폴더는 (HH) 가 아니므로 패턴은 건너뛰고(D3), 재귀는 내려간다.
	// 그 안의 파일 하나가 두 결과의 유일한 차이여야 한다.
	wantPat := []string{"aaaa001a.26o.gz", "aaaa001n.26o.gz", "aaaa001x.26o.gz", "bbbb001n.26o.gz"}
	wantRec := append(append([]string{}, wantPat...), "old.26o.gz")
	sort.Strings(wantRec)

	if got := fileNames(patBatches); !reflect.DeepEqual(got, wantPat) {
		t.Errorf("(HH) 패턴 files = %v, want %v", got, wantPat)
	}
	if got := fileNames(recBatches); !reflect.DeepEqual(got, wantRec) {
		t.Errorf("재귀 files = %v, want %v", got, wantRec)
	}
	if pat.Unmatched != 1 {
		t.Errorf("패턴 Unmatched = %d, want 1 (backup)", pat.Unmatched)
	}
	if rec.Unmatched != 0 {
		t.Errorf("재귀 Unmatched = %d, want 0", rec.Unmatched)
	}
}

// 단 사이의 값 일관성: 위에서 DBON 으로 들어왔으면 아래의 (SITE) 도 DBON
// 이어야 한다. SUW1 폴더로는 내려가지 않는다.
func TestScannerScan_PatternFieldsMustAgreeAcrossSteps(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/":                {dir("DBON")},
			"/data/DBON/2026/":      {dir("DBON"), dir("SUW1")},
			"/data/DBON/2026/DBON/": {file("dbon001a.26o.gz", 1)},
			"/data/DBON/2026/SUW1/": {file("suw1001a.26o.gz", 2)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(SITE)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if got := fileNames(batches); !reflect.DeepEqual(got, []string{"dbon001a.26o.gz"}) {
		t.Errorf("files = %v, want [dbon001a.26o.gz]", got)
	}
	if result.Unmatched != 1 || result.UnmatchedDetails[0] != "/data/DBON/2026/SUW1" {
		t.Errorf("Unmatched = %d %v", result.Unmatched, result.UnmatchedDetails)
	}
}

// 부모가 없으면 지금처럼 정상적인 빈 칸이다. 오류가 아니고 루트도 없다.
func TestScannerScan_PatternParentMissing(t *testing.T) {
	lister := &treeLister{tree: map[string][]Entry{}}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 2),
	)

	if len(batches) != 0 {
		t.Errorf("batches = %d, want 0", len(batches))
	}
	if result.Missing != 2 || result.Errs != 0 || result.Dirs != 0 {
		t.Errorf("Missing=%d Errs=%d Dirs=%d, want 2 0 0", result.Missing, result.Errs, result.Dirs)
	}
	// 없는 부모는 캐시하지 않으므로 날마다 다시 본다. 다음 회차가 아니라
	// 이번 회차 안에서 생긴 폴더도 놓치지 않기 위해서다.
	if !reflect.DeepEqual(lister.calls, []string{"/data/", "/data/"}) {
		t.Errorf("List calls = %#v", lister.calls)
	}
}

// 부모 나열 실패는 scanDir 과 같은 정책이다: 집계하고 그 가지만 비운 채
// 계속한다.
func TestScannerScan_PatternParentListFailureIsCollected(t *testing.T) {
	boom := errors.New("permission denied")
	lister := &treeLister{
		tree: map[string][]Entry{},
		errs: map[string]error{"/data/": boom},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if len(batches) != 0 {
		t.Errorf("batches = %d, want 0", len(batches))
	}
	if result.Errs != 1 || len(result.Failures) != 1 || !errors.Is(result.Failures[0].Err, boom) {
		t.Errorf("Errs=%d Failures=%v", result.Errs, result.Failures)
	}
	if result.Failures[0].Dir != "/data/" {
		t.Errorf("Failures[0].Dir = %q, want /data/", result.Failures[0].Dir)
	}
}

// context 취소는 부모 나열에서도 즉시 중단이다.
func TestScannerScan_PatternParentCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	lister := &fakeLister{
		fn: func(_ context.Context, _ string) ([]Entry, error) {
			cancel()
			return []Entry{dir("DBON")}, nil
		},
	}

	_, err := New(lister).Scan(
		ctx,
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, "/data/(SITE)/(YYYY)/(DOY)/"),
		Range{From: utcDate(2026, time.January, 1), To: utcDate(2026, time.January, 1)},
		func(Batch) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan() error = %v, want context.Canceled", err)
	}
	if len(lister.calls) != 1 {
		t.Errorf("List calls = %d, want 1 (부모 나열 뒤 중단)", len(lister.calls))
	}
}

// 첫 폴더가 패턴인 상대 경로는 현재 디렉터리를 나열한다.
func TestScannerScan_PatternAsFirstFolderListsCurrentDir(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			".":              {dir("DBON")},
			"DBON/2026/001/": {file("dbon001a.26o.gz", 1)},
		},
	}

	_, batches := scanCollect(
		t,
		lister,
		"(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if !reflect.DeepEqual(lister.calls, []string{".", "DBON/2026/001/"}) {
		t.Errorf("List calls = %#v", lister.calls)
	}
	if got := fileNames(batches); !reflect.DeepEqual(got, []string{"dbon001a.26o.gz"}) {
		t.Errorf("files = %v", got)
	}
}

// ---- 운영상 치명적인 경우 (경로 토큰 커밋 5 검토에서 추가) ----

// 치명 1 (I2): 파일 토큰이 없는 기존 설정은 나열이 한 번도 늘지 않아야 한다.
// 3기관 운영 설정이 전부 이 경우다. 부모를 추가로 나열하거나 PatternRoots
// 를 세면, 새 코드가 기존 경로에 끼어들었다는 뜻이다.
func TestScannerScan_TokenFreeTemplateUnchanged(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			`D:\RINEX-V3-H\2026\001\`: {file("a.rnx.gz", 1)},
			// 002 는 없다 → Missing
			`D:\RINEX-V3-H\2026\003\`: {file("b.rnx.gz", 2)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		`D:\RINEX-V3-H\(YYYY)\(DOY)\`,
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 3),
	)

	wantCalls := []string{
		`D:\RINEX-V3-H\2026\001\`,
		`D:\RINEX-V3-H\2026\002\`,
		`D:\RINEX-V3-H\2026\003\`,
	}
	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf("List calls = %#v, want %#v", lister.calls, wantCalls)
	}
	if got := fileNames(batches); !reflect.DeepEqual(got, []string{"a.rnx.gz", "b.rnx.gz"}) {
		t.Errorf("files = %v", got)
	}
	if result.PatternRoots != 0 || result.Unmatched != 0 {
		t.Errorf("PatternRoots=%d Unmatched=%d, want 0 0", result.PatternRoots, result.Unmatched)
	}
	if result.Missing != 1 || result.Dirs != 2 {
		t.Errorf("Missing=%d Dirs=%d, want 1 2", result.Missing, result.Dirs)
	}
}

// 치명 2: 캐시된 부모 목록에 날짜별 대조 결과가 섞이면 안 된다.
// "(DOY)_(SITE)" 처럼 날짜와 관측소가 한 폴더에 있으면 부모 "/data/" 는
// 날마다 같아 캐시가 맞지만, 맞는 폴더는 날마다 다르다. 첫날의 대조
// 결과를 재사용하면 둘째 날 파일이 통째로 사라진다.
func TestScannerScan_CachedParentMatchesPerDay(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {
				dir("001_DBON"),
				dir("002_DBON"),
				dir("002_SUW1"),
			},
			"/data/001_DBON/": {file("dbon001a.26o.gz", 1)},
			"/data/002_DBON/": {file("dbon002a.26o.gz", 2)},
			"/data/002_SUW1/": {file("suw1002a.26o.gz", 3)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(DOY)_(SITE)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 2),
	)

	want := []string{"dbon001a.26o.gz", "dbon002a.26o.gz", "suw1002a.26o.gz"}
	if got := fileNames(batches); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}

	// 부모는 한 번만 나열한다 (캐시).
	parentCalls := 0
	for _, c := range lister.calls {
		if c == "/data/" {
			parentCalls++
		}
	}
	if parentCalls != 1 {
		t.Errorf("부모 나열 = %d회, want 1", parentCalls)
	}

	// 첫날 002_* 두 개가 불일치로 한 번만 세진다 (둘째 날은 캐시라 세지 않음).
	if result.Unmatched != 2 {
		t.Errorf("Unmatched = %d, want 2", result.Unmatched)
	}
	if result.PatternRoots != 3 {
		t.Errorf("PatternRoots = %d, want 3 (1일 1개 + 2일 2개)", result.PatternRoots)
	}
}

// 치명 3: 관측소 이름과 같은 "파일"은 루트가 아니다. DBON 이라는 일반
// 파일로 내려가려 하면 나열 실패가 매 회차 Errs 로 쌓인다.
func TestScannerScan_RegularFileNamedLikeSiteIsNotRoot(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {
				file("DBON", 10),
				dir("SUW1"),
			},
			"/data/SUW1/2026/001/": {file("suw1001a.26o.gz", 1)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if got := fileNames(batches); !reflect.DeepEqual(got, []string{"suw1001a.26o.gz"}) {
		t.Errorf("files = %v", got)
	}
	if result.Errs != 0 || result.PatternRoots != 1 {
		t.Errorf("Errs=%d PatternRoots=%d, want 0 1", result.Errs, result.PatternRoots)
	}
	if result.PatternParentFiles != 1 ||
		!reflect.DeepEqual(result.PatternParentFileDetails, []string{"/data/DBON"}) {
		t.Errorf("PatternParentFiles=%d details=%v, want 1 [/data/DBON]",
			result.PatternParentFiles, result.PatternParentFileDetails)
	}
	for _, c := range lister.calls {
		if c == "/data/DBON/2026/001/" {
			t.Error("일반 파일 DBON 아래로 내려가면 안 된다")
		}
	}
}

// 치명: (SITE)를 실제 파일이 있는 층에 잘못 적으면 패턴 부모에는 일반
// 파일만 보인다. 이 파일들을 아무 기록 없이 버리면 Files=0, Unmatched=0,
// PatternRoots=0으로 전송이 조용히 멈춘다. 러너가 경고할 수 있는 신호와
// 상세 경로를 반드시 남긴다.
func TestScannerScan_PatternParentFilesOnlyIsDetectable(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {file("dbon001a.26o.gz", 10)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if len(batches) != 0 || result.Files != 0 || result.PatternRoots != 0 || result.Unmatched != 0 {
		t.Fatalf("batches=%d Files=%d PatternRoots=%d Unmatched=%d, want all 0",
			len(batches), result.Files, result.PatternRoots, result.Unmatched)
	}
	if result.PatternParentFiles != 1 {
		t.Fatalf("PatternParentFiles=%d, want 1", result.PatternParentFiles)
	}
	if want := []string{"/data/dbon001a.26o.gz"}; !reflect.DeepEqual(result.PatternParentFileDetails, want) {
		t.Errorf("PatternParentFileDetails=%v, want %v", result.PatternParentFileDetails, want)
	}
}

// 정상 패턴 루트와 평면 파일이 함께 있으면 파일은 관측하되 패턴 탐색은
// 계속 성공해야 한다. 러너는 PatternRoots>0을 보고 거짓 경고를 내지 않는다.
func TestScannerScan_PatternRootWithParentFileStillScans(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {
				file("README.txt", 1),
				dir("DBON"),
			},
			"/data/DBON/2026/001/": {file("dbon001a.26o.gz", 10)},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if got := fileNames(batches); !reflect.DeepEqual(got, []string{"dbon001a.26o.gz"}) {
		t.Errorf("files=%v, want [dbon001a.26o.gz]", got)
	}
	if result.PatternRoots != 1 || result.PatternParentFiles != 1 {
		t.Errorf("PatternRoots=%d PatternParentFiles=%d, want 1 1",
			result.PatternRoots, result.PatternParentFiles)
	}
}

// 치명 4: 관측소 폴더 이름 규칙이 설정과 다르면(예: 5자리) 오류 없이
// 파일 0개로 끝난다. 호출자가 경고를 올릴 수 있도록 Unmatched>0 이면서
// PatternRoots==0 이 되어야 한다.
func TestScannerScan_NoFolderMatchedIsDetectable(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {dir("DBON1"), dir("SUW10")},
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if len(batches) != 0 {
		t.Errorf("batches = %d, want 0", len(batches))
	}
	if result.Unmatched != 2 || result.PatternRoots != 0 {
		t.Errorf("Unmatched=%d PatternRoots=%d, want 2 0 (경고 조건)", result.Unmatched, result.PatternRoots)
	}
	if result.Errs != 0 {
		t.Errorf("Errs = %d, want 0 (오류가 아니라 설정 불일치)", result.Errs)
	}
}

// 치명 4 의 반대: 관측소 폴더는 맞았는데 오늘 데이터가 아직 없을 뿐이면
// 경고 조건이 아니다. 새벽마다 거짓 경고가 뜨면 운영자가 경고를 무시하게 된다.
func TestScannerScan_MatchedButEmptyIsNotWarning(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/": {dir("DBON"), dir("backup")},
			// /data/DBON/2026/001/ 은 아직 없다
		},
	}

	result, batches := scanCollect(
		t,
		lister,
		"/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	if len(batches) != 0 {
		t.Errorf("batches = %d, want 0", len(batches))
	}
	if result.PatternRoots != 1 || result.Unmatched != 1 {
		t.Errorf("PatternRoots=%d Unmatched=%d, want 1 1 (경고 아님)", result.PatternRoots, result.Unmatched)
	}
}

// 치명 5: 캐시는 한 번의 Scan 안에서만 산다. 회차 사이에 새 관측소
// 폴더가 생기면 다음 회차에 바로 보여야 한다. 캐시가 Scanner 에 붙어
// 회차를 넘기면 새 관측소가 프로세스 재시작 전까지 영영 전송되지 않는다.
func TestScannerScan_CacheDoesNotOutliveScan(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/":               {dir("DBON")},
			"/data/DBON/2026/001/": {file("dbon001a.26o.gz", 1)},
		},
	}
	sc := New(lister)
	tpl := mustScanTemplate(t, "/data/(SITE)/(YYYY)/(DOY)/")
	rng := Range{From: utcDate(2026, time.January, 1), To: utcDate(2026, time.January, 1)}

	collect := func() []string {
		var batches []Batch
		if _, err := sc.Scan(context.Background(), domain.CategoryRINEX2Hourly, tpl, rng,
			func(b Batch) error { batches = append(batches, b); return nil }); err != nil {
			t.Fatalf("Scan() error: %v", err)
		}
		return fileNames(batches)
	}

	if got := collect(); !reflect.DeepEqual(got, []string{"dbon001a.26o.gz"}) {
		t.Fatalf("1회차 files = %v", got)
	}

	// 회차 사이에 새 관측소 폴더가 생겼다.
	lister.tree["/data/"] = []Entry{dir("DBON"), dir("SUW1")}
	lister.tree["/data/SUW1/2026/001/"] = []Entry{file("suw1001a.26o.gz", 2)}

	want := []string{"dbon001a.26o.gz", "suw1001a.26o.gz"}
	if got := collect(); !reflect.DeepEqual(got, want) {
		t.Errorf("2회차 files = %v, want %v (새 관측소가 보여야 한다)", got, want)
	}
}

// 치명 6: Windows 경로에서 관측소가 날짜 위에 있을 때 부모·루트 표기가
// 원문 구분자를 유지해야 한다. "/" 를 섞으면 LocalLister 는 찾더라도
// Batch.Dir 로그와 LocalPath 가 기존 표기와 달라진다.
func TestScannerScan_WindowsSiteAboveDate(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			`D:\data\`:               {dir("DBON")},
			`D:\data\DBON\2026\001\`: {file("dbon001a.26o.gz", 1)},
		},
	}

	_, batches := scanCollect(
		t,
		lister,
		`D:\data\(SITE)\(YYYY)\(DOY)\`,
		utcDate(2026, time.January, 1),
		utcDate(2026, time.January, 1),
	)

	wantCalls := []string{`D:\data\`, `D:\data\DBON\2026\001\`}
	if !reflect.DeepEqual(lister.calls, wantCalls) {
		t.Errorf("List calls = %#v, want %#v", lister.calls, wantCalls)
	}
	if len(batches) != 1 || batches[0].Dir != `D:\data\DBON\2026\001\` {
		t.Errorf("batches = %+v", batches)
	}
}

// 치명 (R1): 원본을 Expand 하면 List 인자가 ".../(SITE)/2026/001/" 가 되어
// 없는 폴더를 정상 Missing 으로 세고 파일 0개로 끝난다. 나열 경로에
// 토큰 원문이 남아 있으면 그 버그다.
func TestScannerScan_ListPathsNeverContainFileTokenLiterals(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			"/data/":               {dir("DBON")},
			"/data/DBON/2026/001/": {file("dbon001a.26o.gz", 1)},
			`C:\H\2026\001\`:       {dir("00"), dir("13")},
			`C:\H\2026\001\00\`:    {file("aaaa001a.26o.gz", 1)},
			`C:\H\2026\001\13\`:    {file("aaaa001n.26o.gz", 1)},
		},
	}

	scanCollect(t, lister, "/data/(SITE)/(YYYY)/(DOY)/",
		utcDate(2026, time.January, 1), utcDate(2026, time.January, 1))
	scanCollect(t, lister, `C:\H\(YYYY)\(DOY)\(HH)\`,
		utcDate(2026, time.January, 1), utcDate(2026, time.January, 1))

	for _, c := range lister.calls {
		if strings.Contains(c, "(SITE)") || strings.Contains(c, "(HH)") {
			t.Errorf("List(%q): 원본을 Expand 한 토큰 원문이 남았다", c)
		}
	}
}

// 치명: 지질자원연형 rnx_(SITE) + (MM)(DD). 글자 접두 rnx_ 는 대소문자를
// 가린다. 디스크가 RNX_ 인데 설정이 rnx_ 이면 파일 0개다. 경고 조건
// (Unmatched>0, PatternRoots==0)이 서야 한다.
func TestScannerScan_FolderPrefixAndMonthDay(t *testing.T) {
	day := utcDate(2026, time.January, 5) // MM/DD = 0105

	t.Run("맞는 접두로 수집", func(t *testing.T) {
		lister := &treeLister{
			tree: map[string][]Entry{
				"/occ/data/": {
					dir("rnx_DBON"),
					dir("RNX_SUW1"),
					dir("backup"),
				},
				"/occ/data/rnx_DBON/2026/0105/": {file("dbon005a.26o.gz", 1)},
				"/occ/data/RNX_SUW1/2026/0105/": {file("suw1005a.26o.gz", 2)},
			},
		}

		result, batches := scanCollect(
			t, lister, "/occ/data/rnx_(SITE)/(YYYY)/(MM)(DD)/", day, day,
		)

		if got := fileNames(batches); !reflect.DeepEqual(got, []string{"dbon005a.26o.gz"}) {
			t.Errorf("files = %v, want [dbon005a.26o.gz] (RNX_ 는 접두 불일치)", got)
		}
		if result.PatternRoots != 1 {
			t.Errorf("PatternRoots = %d, want 1", result.PatternRoots)
		}
		if result.Unmatched != 2 {
			t.Errorf("Unmatched = %d, want 2 (RNX_SUW1, backup)", result.Unmatched)
		}
	})

	t.Run("접두 대소문자 전부 불일치 → 경고 조건", func(t *testing.T) {
		lister := &treeLister{
			tree: map[string][]Entry{
				"/occ/data/": {dir("RNX_DBON")},
			},
		}

		result, batches := scanCollect(
			t, lister, "/occ/data/rnx_(SITE)/(YYYY)/(MM)(DD)/", day, day,
		)

		if len(batches) != 0 {
			t.Errorf("batches = %d, want 0", len(batches))
		}
		if result.Unmatched != 1 || result.PatternRoots != 0 {
			t.Errorf("Unmatched=%d PatternRoots=%d, want 1 0", result.Unmatched, result.PatternRoots)
		}
	})
}

// 치명: 이름만 시각 폴더처럼 보이는 24·99 는 (HH) 가 아니다. 내려가면
// 잘못된 루트가 생기고, 건너뛰지 않으면 D1(재귀와 같은 집합)이 깨진다.
func TestScannerScan_HourFolderOutside00To23IsUnmatched(t *testing.T) {
	lister := &treeLister{
		tree: map[string][]Entry{
			`C:\H\2026\001\`: {
				dir("00"),
				dir("24"),
				dir("99"),
			},
			`C:\H\2026\001\00\`: {file("aaaa001a.26o.gz", 1)},
			`C:\H\2026\001\24\`: {file("bad24.26o.gz", 2)},
			`C:\H\2026\001\99\`: {file("bad99.26o.gz", 3)},
		},
	}

	result, batches := scanCollect(
		t, lister, `C:\H\(YYYY)\(DOY)\(HH)\`,
		utcDate(2026, time.January, 1), utcDate(2026, time.January, 1),
	)

	if got := fileNames(batches); !reflect.DeepEqual(got, []string{"aaaa001a.26o.gz"}) {
		t.Errorf("files = %v, want [aaaa001a.26o.gz]", got)
	}
	if result.Unmatched != 2 || result.PatternRoots != 1 {
		t.Errorf("Unmatched=%d PatternRoots=%d, want 2 1", result.Unmatched, result.PatternRoots)
	}
}
