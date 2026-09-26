package download

import (
	"bytes"
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
)

// DOWNLOAD 커밋 6 — 보고 포맷 (DOWNLOAD_COMMIT_PLAN v1 §7, DOWNLOAD v3 §10).

// v3 §10.2 의 예시 줄이 글자 그대로 나온다.
func TestFormatCategory_DesignExample(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Hourly)
	c.Scan.Files = 412
	c.Downloaded = 37
	c.Reasons[ReasonExists] = 357
	c.Reasons[ReasonSiteExcluded] = 12
	c.Reasons[ReasonSiteUnknown] = 2
	c.Reasons[ReasonGrace] = 3
	c.Failed = 1
	c.Duration = 2412 * time.Millisecond

	// 0 인 사유를 명시적으로 넣어도 나오지 않는다.
	c.Reasons[ReasonConflict] = 0
	c.Reasons[ReasonPart] = 0

	want := "[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 원격=412 수신=37 기존=357 " +
		"사이트제외=12 사이트식별불가=2 유예중=3 실패=1 소요=2.4s"

	if got := formatCategory(c, RangeHot); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

// 처리 집계가 모두 0 이면 대상없음 하나. 종류·범위·소요는 항상 나온다.
func TestFormatCategory_NothingToDo(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Hourly)
	c.Duration = 18300 * time.Microsecond

	want := "[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 대상없음 소요=18ms"

	if got := formatCategory(c, RangeHot); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

// D15: 표시에서 빠져도 Report 의 숫자는 그대로다.
func TestFormatCategory_OmissionKeepsNumbers(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX3Daily)
	c.Reasons[ReasonConflict] = 0
	c.Downloaded = 0

	_ = formatCategory(c, RangeDeep)

	if n, ok := c.Reasons[ReasonConflict]; !ok || n != 0 {
		t.Fatal("formatting removed or changed a Report value")
	}
}

// 모든 사유가 표시되는 전체 줄 — 고정 순서를 한 번에 확인한다.
func TestFormatCategory_FullOrder(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX3Hourly)
	c.Scan.Files = 1
	c.Downloaded = 2
	for i, rl := range reasonLabels {
		c.Reasons[rl.reason] = 10 + i
	}
	c.Failed = 3
	c.NotStarted = 4
	c.Duration = time.Second

	want := "[DOWNLOAD] 종류=RINEX3_HOURLY 범위=심층 원격=1 수신=2 기존=10 사이트제외=11 " +
		"사이트식별불가=12 시각식별불가=13 유예중=14 미래시각=15 임시파일=16 0바이트=17 " +
		"종류불일치=18 충돌=19 비정상대상=20 목적지중복=21 실패=3 미착수=4 소요=1s"

	if got := formatCategory(c, RangeDeep); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

// map 순회 순서에 영향을 받지 않는다. 삽입 순서를 바꿔 여러 번 만들어도 같다.
func TestFormatCategory_StableAcrossMapOrder(t *testing.T) {
	t.Parallel()

	build := func(reverse bool) CategoryReport {
		c := newCategoryReport(domain.CategoryRINEX2Daily)

		idx := make([]int, len(reasonLabels))
		for i := range idx {
			idx[i] = i
			if reverse {
				idx[i] = len(reasonLabels) - 1 - i
			}
		}

		for _, i := range idx {
			c.Reasons[reasonLabels[i].reason] = i + 1
		}

		c.Reasons[Reason("zeta")] = 1
		c.Reasons[Reason("alpha")] = 2

		return c
	}

	first := formatCategory(build(false), RangeHot)

	for i := 0; i < 50; i++ {
		if got := formatCategory(build(i%2 == 1), RangeHot); got != first {
			t.Fatalf("output changed between runs:\n%s\n%s", first, got)
		}
	}
}

// 표에 없는 사유는 사라지지 않고 줄 끝에 기타(키)=N 으로, 이름순으로 나온다.
func TestFormatCategory_UnknownReasonIsNotDropped(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Daily)
	c.Reasons[Reason("zeta")] = 1
	c.Reasons[Reason("alpha")] = 2
	c.Failed = 1

	got := formatCategory(c, RangeHot)

	if !strings.Contains(got, "실패=1 기타(alpha)=2 기타(zeta)=1 소요=") {
		t.Fatalf("got %s", got)
	}
}

// 선언된 사유는 전부 표시명을 가진다. 새 사유를 추가하면 이 목록과
// reasonLabels 를 함께 고쳐야 테스트가 통과한다.
func TestReasonLabels_CoverAllReasons(t *testing.T) {
	t.Parallel()

	all := []Reason{
		ReasonPart, ReasonZero, ReasonGrace, ReasonFutureMTime, ReasonMismatch,
		ReasonSiteExcluded, ReasonSiteUnknown, ReasonHourUnknown,
		ReasonExists, ReasonConflict, ReasonBadTarget, ReasonDuplicate,
	}

	labeled := map[Reason]string{}
	labels := map[string]bool{}

	for _, rl := range reasonLabels {
		if _, dup := labeled[rl.reason]; dup {
			t.Errorf("reason %q listed twice", rl.reason)
		}

		if labels[rl.label] {
			t.Errorf("label %q used twice", rl.label)
		}

		labeled[rl.reason] = rl.label
		labels[rl.label] = true
	}

	for _, r := range all {
		if labeled[r] == "" {
			t.Errorf("reason %q has no display label", r)
		}
	}

	if len(reasonLabels) != len(all) {
		t.Errorf("reasonLabels has %d entries, reasons declared %d", len(reasonLabels), len(all))
	}
}

func TestRange_Label(t *testing.T) {
	t.Parallel()

	cases := map[Range]string{
		RangeHot:  "최근",
		RangeDeep: "심층",
		"":        "미지정",
		"manual":  "manual",
	}

	for r, want := range cases {
		if got := r.label(); got != want {
			t.Errorf("Range(%q).label() = %q, want %q", r, got, want)
		}
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	cases := map[time.Duration]string{
		0:                        "0s",
		-time.Second:             "0s",
		750 * time.Microsecond:   "750µs",
		18300 * time.Microsecond: "18ms",
		999 * time.Millisecond:   "999ms",
		2412 * time.Millisecond:  "2.4s",
		75 * time.Second:         "1m15s",
	}

	for d, want := range cases {
		if got := formatDuration(d); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

// ── 나열 경고 ───────────────────────────────────────────────────────

func TestFormatScanWarnings_CleanScanHasNone(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Hourly)
	c.Scan = scan.Result{Dirs: 30, Files: 400, Missing: 1} // 오늘 폴더 아직 없음 — 정상

	if got := formatScanWarnings(c); got != nil {
		t.Fatalf("warnings = %v, want none", got)
	}
}

// 목록을 읽지 못한 폴더는 요약 줄에서 원격 0 건·대상없음으로 보인다.
// 경고 줄이 따로 나와야 장애가 가려지지 않는다.
// 기관 구조에서 나온 관찰(비정규·폴더불일치·폴더밖파일)은 경고에 섞이지 않고
// 참고 줄로 따로 간다.
func TestFormatScanLines_WarningsAndNotesAreSeparated(t *testing.T) {
	t.Parallel()

	denied := errors.New("permission denied")

	c := newCategoryReport(domain.CategoryRINEX2Hourly)
	c.Scan = scan.Result{
		Dirs:      1,
		Errs:      1,
		Failures:  []scan.DirError{{Dir: "/RNX/2026/268/13", Err: denied}},
		Irregular: 1,
		IrregularDetails: []scan.IrregularEntry{
			{Path: "/RNX/2026/268/link13", Type: fs.ModeSymlink},
		},
		Unmatched:          2,
		PatternParentFiles: 3,
		PatternRoots:       1, // 맞는 폴더도 있었다 — 불일치는 기관 구조의 잡음
	}

	if s := formatCategory(c, RangeHot); !strings.Contains(s, "대상없음") {
		t.Fatalf("precondition: summary = %s", s)
	}

	wantWarn := []string{
		"[DOWNLOAD] 나열경고 종류=RINEX2_HOURLY 나열실패=1",
		"[DOWNLOAD] 나열실패 종류=RINEX2_HOURLY 폴더=/RNX/2026/268/13 원인=permission denied",
	}

	if got := formatScanWarnings(c); strings.Join(got, "\n") != strings.Join(wantWarn, "\n") {
		t.Fatalf("warnings\n got %q\nwant %q", got, wantWarn)
	}

	wantNote := []string{
		"[DOWNLOAD] 나열참고 종류=RINEX2_HOURLY 비정규항목=1 폴더불일치=2 폴더밖파일=3",
		"[DOWNLOAD] 비정규항목 종류=RINEX2_HOURLY 경로=/RNX/2026/268/link13 형태=L---------",
	}

	if got := formatScanNotes(c); strings.Join(got, "\n") != strings.Join(wantNote, "\n") {
		t.Fatalf("notes\n got %q\nwant %q", got, wantNote)
	}
}

// 기관 구조의 관찰만 있으면 경고 줄은 없다 — 화면에는 아무것도 뜨지 않는다.
func TestFormatScanLines_NotesOnlyProduceNoWarning(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Hourly)
	c.Scan = scan.Result{Dirs: 5, Files: 10, Unmatched: 1, Irregular: 1, PatternRoots: 2}

	if got := formatScanWarnings(c); got != nil {
		t.Fatalf("warnings = %q, want none", got)
	}

	if got := formatScanNotes(c); len(got) == 0 {
		t.Fatal("notes missing")
	}
}

// 범위 전체에서 폴더가 하나도 없으면(RemotePath 오기 가능성) 경고한다.
// 일부 날짜만 없는 것은 경고하지 않는다.
func TestFormatScanWarnings_AllFoldersMissing(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Daily)
	c.Scan = scan.Result{Dirs: 0, Missing: 2}

	got := formatScanWarnings(c)
	if len(got) != 1 || got[0] != "[DOWNLOAD] 나열경고 종류=RINEX2_DAILY 원격폴더없음=2" {
		t.Fatalf("warnings = %q", got)
	}

	c.Scan = scan.Result{Dirs: 1, Missing: 1}
	if got := formatScanWarnings(c); got != nil {
		t.Fatalf("partial missing warned: %q", got)
	}
}

// Print 는 조치가 필요한 줄만 console 로, 기관 구조의 관찰은 detail 로 보낸다.
func TestReport_PrintSplitsConsoleAndDetail(t *testing.T) {
	t.Parallel()

	a := newCategoryReport(domain.CategoryRINEX2Daily)
	a.Scan = scan.Result{Dirs: 0, Missing: 2}

	b := newCategoryReport(domain.CategoryRINEX2Hourly)
	b.Scan = scan.Result{Dirs: 3, Files: 5, Unmatched: 1, PatternRoots: 1}
	b.Downloaded = 5

	var console, detail bytes.Buffer

	Report{Categories: []CategoryReport{a, b}, Range: RangeDeep}.Print(
		log.New(&console, "", 0),
		log.New(&detail, "", 0),
	)

	wantConsole := "[DOWNLOAD] 종류=RINEX2_DAILY 범위=심층 대상없음 소요=0s\n" +
		"[DOWNLOAD] 나열경고 종류=RINEX2_DAILY 원격폴더없음=2\n" +
		"[DOWNLOAD] 종류=RINEX2_HOURLY 범위=심층 원격=5 수신=5 소요=0s\n"

	if console.String() != wantConsole {
		t.Fatalf("console\n got %q\nwant %q", console.String(), wantConsole)
	}

	if detail.String() != "[DOWNLOAD] 나열참고 종류=RINEX2_HOURLY 폴더불일치=1\n" {
		t.Fatalf("detail = %q", detail.String())
	}
}

// detail 이 nil 이면 줄을 버리지 않고 console 로 보낸다.
func TestReport_PrintNilDetailKeepsEverything(t *testing.T) {
	t.Parallel()

	c := newCategoryReport(domain.CategoryRINEX2Hourly)
	c.Scan = scan.Result{Dirs: 1, Unmatched: 1, PatternRoots: 1}

	var buf bytes.Buffer

	Report{Categories: []CategoryReport{c}}.Print(log.New(&buf, "", 0), nil)

	if !strings.Contains(buf.String(), "[DOWNLOAD] 나열참고 ") {
		t.Fatalf("note dropped: %q", buf.String())
	}
}

// 러너가 만든 실제 Report 가 Print 를 거쳐 기대한 줄이 된다 (커밋 5 → 6 연결).
func TestReport_PrintFromRealRun(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "a")
	e.remote.put(t, "/RNX/2026/268/soch268b.26o.gz.part", "p")

	job := e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/")

	rep, err := e.run(t, t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}

	rep.Range = RangeHot

	var buf bytes.Buffer
	rep.Print(log.New(&buf, "", 0), nil)

	line := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(line, "[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 원격=2 수신=1 임시파일=1 소요=") {
		t.Fatalf("line = %s", line)
	}
}

// 러너의 파일별 로그도 같은 기준으로 나뉜다. 목적지중복(원격 구조)은 파일
// 전용, 충돌(로컬 문제)은 화면+파일이다.
func TestRun_FileLogsFollowConsoleDetailSplit(t *testing.T) {
	e := newRunEnv(t)

	var detail bytes.Buffer
	e.runner.Opts.DetailLogger = log.New(&syncWriter{w: &detail}, "", 0)

	// 두 관측소 폴더의 같은 이름이 (SITE) 없는 목적지로 수렴 → 목적지중복.
	e.remote.put(t, "/RNX/2026/268/PALM/soch268a.26o.gz", "x")
	e.remote.put(t, "/RNX/2026/268/SOCH/soch268a.26o.gz", "x")

	// 로컬에 크기가 다른 파일 → 충돌.
	e.remote.put(t, "/RNX/2026/268/PALM/soch268b.26o.gz", "remote")
	if err := os.MkdirAll(e.localFile("2026/268"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.localFile("2026/268/soch268b.26o.gz"), []byte("local-longer"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := e.run(t, t.Context(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/(SITE)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	console := e.logs.String()

	if strings.Contains(console, "목적지중복") {
		t.Errorf("duplicate log reached the console:\n%s", console)
	}

	if !strings.Contains(detail.String(), "[DOWNLOAD] 목적지중복 ") {
		t.Errorf("duplicate log missing from detail:\n%s", detail.String())
	}

	if !strings.Contains(console, "[DOWNLOAD] 충돌 ") || strings.Contains(detail.String(), "충돌") {
		t.Errorf("conflict must go to the console logger only\nconsole:\n%s\ndetail:\n%s", console, detail.String())
	}
}
