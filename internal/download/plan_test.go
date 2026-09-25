package download

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// DOWNLOAD 커밋 4 — 순수 후보·기존 대상 판정 (DOWNLOAD_COMMIT_PLAN v1 §5).
// 네트워크·실제 파일 I/O 없이 판정 규칙만 고정한다.

// 판정 시각. 원격 파일 mtime 은 이보다 1시간 전이 기본이다.
var planNow = time.Date(2026, 9, 25, 15, 0, 0, 0, time.UTC)

func oldEntry(name string, size int64) scan.Entry {
	return scan.Entry{Name: name, Size: size, MTime: planNow.Add(-time.Hour)}
}

// slashJoin 은 테스트용 LocalStore.Join 이다. OS 와 무관하게 결과를 고정한다.
func slashJoin(dir, name string) string {
	return strings.TrimRight(dir, "/") + "/" + name
}

func newTestPlanner(
	t *testing.T,
	cat domain.Category,
	localTpl string,
	sites []string,
) *planner {
	t.Helper()

	p, err := newPlanner(
		cat,
		mustTpl(t, localTpl),
		verify.Verifier{Grace: time.Minute, Now: func() time.Time { return planNow }},
		sites,
		slashJoin,
	)
	if err != nil {
		t.Fatalf("newPlanner: %v", err)
	}

	return p
}

func evalOne(t *testing.T, p *planner, e scan.Entry) (Candidate, Reason) {
	t.Helper()

	c, r, err := p.evaluate("/RNX/2026/268/", e, day268)
	if err != nil {
		t.Fatalf("evaluate(%q): %v", e.Name, err)
	}

	return c, r
}

// ── §5.1 판정 순서 ──────────────────────────────────────────────────

// D18: 이름만 보면 유효한 .part·.filepart 는 NormalizeName 전에 제외된다.
// 목적지에 (SITE)·(HH) 가 있어 SITE·HH 추출이 돌면 후보가 됐을 이름이다.
func TestEvaluate_PartFilesExcludedBeforeNormalize(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/(SITE)/(HH)/", nil)

	for _, name := range []string{
		"soch268a.26o.gz.part",
		"soch268a.26o.gz.filepart",
		"SOCH268A.26O.GZ.PART",
	} {
		_, r := evalOne(t, p, oldEntry(name, 100))
		if r != ReasonPart {
			t.Errorf("%s → %q, want part", name, r)
		}
	}

	// 대조군: 접미사를 뗀 같은 이름은 후보다.
	if _, r := evalOne(t, p, oldEntry("soch268a.26o.gz", 100)); r != "" {
		t.Fatalf("control file → %q, want candidate", r)
	}
}

func TestEvaluate_VerifyReasons(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/", nil)

	cases := []struct {
		name string
		e    scan.Entry
		want Reason
	}{
		{"0바이트", oldEntry("soch268a.26o.gz", 0), ReasonZero},
		{"유예중", scan.Entry{Name: "soch268a.26o.gz", Size: 1, MTime: planNow.Add(-30 * time.Second)}, ReasonGrace},
		{"미래시각", scan.Entry{Name: "soch268a.26o.gz", Size: 1, MTime: planNow.Add(10 * time.Minute)}, ReasonFutureMTime},
		{"종류불일치 (Hourly 자리의 Daily 이름)", oldEntry("soch2680.26o.gz", 1), ReasonMismatch},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, r := evalOne(t, p, tc.e); r != tc.want {
				t.Fatalf("reason = %q, want %q", r, tc.want)
			}
		})
	}
}

// Grace=0 은 유예·미래시각 검사를 끈다 ([DOWNLOAD] GraceSeconds = 0).
func TestEvaluate_GraceZeroDisablesAgeChecks(t *testing.T) {
	p, err := newPlanner(
		domain.CategoryRINEX2Hourly,
		mustTpl(t, "/down/(YYYY)/(DOY)/"),
		verify.Verifier{Grace: 0, Now: func() time.Time { return planNow }},
		nil,
		slashJoin,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, mt := range []time.Time{planNow, planNow.Add(time.Hour)} {
		e := scan.Entry{Name: "soch268a.26o.gz", Size: 1, MTime: mt}
		if _, r := evalOne(t, p, e); r != "" {
			t.Errorf("mtime %v → %q, want candidate", mt, r)
		}
	}
}

// D3: Sites=SOCH 이면 SOCH 만 후보, 나머지는 사이트제외.
func TestEvaluate_SitesFilter(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/", []string{"soch"})

	cases := []struct {
		name string
		want Reason
	}{
		{"soch268a.26o.gz", ""},
		{"SOCH268N.26O.gz", ""}, // 대소문자 무시
		{"palm268a.26o.gz", ReasonSiteExcluded},
		{"db-n268a.26o.gz", ReasonSiteUnknown}, // 종류는 OK, 관측소 식별 불가
		{"readme.txt", ReasonSiteUnknown},      // 종류 Unknown 은 verify 통과
	}

	for _, tc := range cases {
		if _, r := evalOne(t, p, oldEntry(tc.name, 10)); r != tc.want {
			t.Errorf("%s → %q, want %q", tc.name, r, tc.want)
		}
	}
}

// Sites 도 (SITE) 도 없으면 관측소를 식별하지 않는다 — 식별 불가가 새 제외
// 사유가 되지 않는다 (SITE v1 §4, PUT 과 같은 규칙).
func TestEvaluate_NoSiteNeededNoSiteUnknown(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/", nil)

	for _, name := range []string{"db-n268a.26o.gz", "readme.txt"} {
		if _, r := evalOne(t, p, oldEntry(name, 10)); r != "" {
			t.Errorf("%s → %q, want candidate (site not needed)", name, r)
		}
	}
}

// Sites 가 없어도 목적지에 (SITE) 가 있으면 식별이 필요하다.
func TestEvaluate_SiteTokenRequiresSite(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/(SITE)/", nil)

	if _, r := evalOne(t, p, oldEntry("db-n268a.26o.gz", 10)); r != ReasonSiteUnknown {
		t.Fatalf("reason = %q, want site_unknown", r)
	}
}

// HourFromName 은 LocalPath 에 (HH) 가 있을 때만 부른다.
func TestEvaluate_HourOnlyWhenNeeded(t *testing.T) {
	// 시각 필드 99 — 종류·관측소는 OK, 시각만 식별 불가.
	const name = "SOCH00KOR_R_20262689900_01H_30S_MO.rnx.gz"

	withHH := newTestPlanner(t, domain.CategoryRINEX3Hourly, "/down/(YYYY)/(DOY)/(HH)/", nil)
	if _, r := evalOne(t, withHH, oldEntry(name, 10)); r != ReasonHourUnknown {
		t.Fatalf("with (HH) → %q, want hour_unknown", r)
	}

	noHH := newTestPlanner(t, domain.CategoryRINEX3Hourly, "/down/(YYYY)/(DOY)/", nil)
	if _, r := evalOne(t, noHH, oldEntry(name, 10)); r != "" {
		t.Fatalf("without (HH) → %q, want candidate", r)
	}
}

// D1 의 판정 몫: (SITE)·(HH) 가 전개된 목적지, 원본 대소문자 보존.
func TestEvaluate_CandidateDestination(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX3Hourly, "/down/(YYYY)/(DOY)/(SITE)/(HH)/", nil)

	const name = "SOCH00KOR_R_20262681300_01H_30S_MO.rnx.gz"

	e := oldEntry(name, 4321)

	for _, dir := range []string{"/RNX/2026/268/", "/RNX/2026/268"} {
		c, r, err := p.evaluate(dir, e, day268)
		if err != nil || r != "" {
			t.Fatalf("evaluate: reason=%q err=%v", r, err)
		}

		want := Candidate{
			Category:   domain.CategoryRINEX3Hourly,
			RemotePath: "/RNX/2026/268/" + name,
			Name:       name,
			Size:       4321,
			MTime:      e.MTime,
			LocalDir:   "/down/2026/268/SOCH/13/",
			LocalPath:  "/down/2026/268/SOCH/13/" + name,
		}

		if !reflect.DeepEqual(c, want) {
			t.Fatalf("dir %q:\n got %+v\nwant %+v", dir, c, want)
		}

		if c.PartPath() != want.LocalPath+".part" {
			t.Errorf("PartPath = %q", c.PartPath())
		}

		// 로컬 .part 이름도 임시 파일로 인식된다 — 이 폴더를 PUT 원본으로
		// 스캔해도 제외된다.
		if !domain.IsPartFile(c.PartPath()) {
			t.Errorf("PartPath %q is not recognized as a temp file", c.PartPath())
		}
	}
}

// 계약 위반은 사유로 접지 않고 오류로 올린다.
func TestEvaluate_ContractViolationsAreErrors(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/", nil)

	// Scanner 는 디렉터리를 Batch 로 넘기지 않는다.
	dir := scan.Entry{Name: "13", IsDir: true, Type: fs.ModeDir}

	_, r, err := p.evaluate("/RNX/2026/268/", dir, day268)
	if !errors.Is(err, ErrUnmappedVerifyReason) || r != "" {
		t.Fatalf("reason = %q err = %v, want ErrUnmappedVerifyReason", r, err)
	}

	// verify 에 새 사유가 생겼는데 fromVerify 를 빠뜨린 경우.
	if _, err := fromVerify(verify.Reason(99)); !errors.Is(err, ErrUnmappedVerifyReason) {
		t.Fatalf("fromVerify(99) err = %v", err)
	}
}

func TestNewPlanner(t *testing.T) {
	t.Parallel()

	v := verify.Verifier{}

	if _, err := newPlanner(domain.CategoryRINEX2Daily, nil, v, nil, slashJoin); err == nil {
		t.Error("nil template accepted")
	}

	if _, err := newPlanner(domain.CategoryRINEX2Daily, mustTpl(t, "/d/"), v, nil, nil); err == nil {
		t.Error("nil join accepted")
	}

	if _, err := newPlanner(domain.CategoryRINEX2Daily, mustTpl(t, "/d/"), v, []string{"DB*N"}, slashJoin); err == nil {
		t.Error("invalid site accepted")
	}

	// 소문자 목록도 정규화되어 정확 일치한다.
	p, err := newPlanner(domain.CategoryRINEX2Daily, mustTpl(t, "/d/"), v, []string{" soch "}, slashJoin)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := p.sites["SOCH"]; !ok || !p.needSite || p.needHour {
		t.Errorf("planner = %+v", p)
	}
}

// ── §5.2 로컬 대상 판정 ─────────────────────────────────────────────

func TestDecideTarget(t *testing.T) {
	t.Parallel()

	denied := errors.New("access is denied")

	cases := []struct {
		name    string
		fi      fs.FileInfo
		statErr error
		want    targetState
		wantErr error
	}{
		{"없음 → 수신", nil, fs.ErrNotExist, targetAbsent, nil},
		{"감싼 없음 → 수신", nil, &fs.PathError{Op: "lstat", Path: "x", Err: fs.ErrNotExist}, targetAbsent, nil},
		{"D7 같은 크기 일반 파일 → 기존", fakeInfo{name: "a", size: 100, mode: 0o644}, nil, targetExists, nil},
		{"D8 다른 크기 일반 파일 → 충돌", fakeInfo{name: "a", size: 99, mode: 0o644}, nil, targetConflict, nil},
		{"같은 이름 디렉터리 → 비정상", fakeInfo{name: "a", size: 100, mode: fs.ModeDir | 0o755}, nil, targetIrregular, nil},
		{"같은 크기로 보이는 링크 → 비정상 (기존 오인 없음)", fakeInfo{name: "a", size: 100, mode: fs.ModeSymlink | 0o777}, nil, targetIrregular, nil},
		{"장치·비정규 → 비정상", fakeInfo{name: "a", size: 100, mode: fs.ModeIrregular}, nil, targetIrregular, nil},
		{"권한 오류 → 판정값으로 숨기지 않음", nil, denied, 0, denied},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decideTarget(100, tc.fi, tc.statErr)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}

			if err != nil || got != tc.want {
				t.Fatalf("state = %v err = %v, want %v", got, err, tc.want)
			}
		})
	}

	if _, err := decideTarget(1, nil, nil); err == nil {
		t.Error("nil info with nil error accepted")
	}
}

func TestTargetState_Reason(t *testing.T) {
	t.Parallel()

	want := map[targetState]Reason{
		targetAbsent:    "",
		targetExists:    ReasonExists,
		targetConflict:  ReasonConflict,
		targetIrregular: ReasonBadTarget,
	}

	for s, r := range want {
		if got := s.reason(); got != r {
			t.Errorf("%v.reason() = %q, want %q", s, got, r)
		}
	}
}

// ── §5.3 회차 내 목적지 중복 ────────────────────────────────────────

func TestDestSet(t *testing.T) {
	t.Parallel()

	d := newDestSet()

	first := Candidate{RemotePath: "/RNX/SOCH/a.gz", LocalPath: `D:\down\268\a.gz`}

	if ok, _ := d.claim(first); !ok {
		t.Fatal("first claim rejected")
	}

	for _, dup := range []Candidate{
		{RemotePath: "/RNX/PALM/a.gz", LocalPath: `D:\down\268\a.gz`},    // 같은 경로
		{RemotePath: "/RNX/X/A.GZ", LocalPath: `D:\DOWN\268\A.GZ`},       // 대소문자만 다름
		{RemotePath: "/RNX/Y/a.gz", LocalPath: `D:/down/268/a.gz`},       // 구분자만 다름
		{RemotePath: "/RNX/Y/a2.gz", LocalPath: `D:\down\268.\a.gz`},     // 후행 점 별칭
		{RemotePath: "/RNX/Y/a3.gz", LocalPath: `D:\down\268 \a.gz`},     // 후행 공백 별칭
		{RemotePath: "/RNX/Y/a4.gz", LocalPath: `D:\down\x\..\268\a.gz`}, // 정리 별칭
	} {
		ok, winner := d.claim(dup)
		if ok {
			t.Errorf("duplicate %q accepted", dup.LocalPath)
		}

		if winner != first.RemotePath {
			t.Errorf("winner = %q, want the first remote path", winner)
		}
	}

	if ok, _ := d.claim(Candidate{RemotePath: "/RNX/b.gz", LocalPath: `D:\down\268\b.gz`}); !ok {
		t.Error("different destination rejected")
	}
}

func TestDestKey_PreservesUNCDistinction(t *testing.T) {
	t.Parallel()

	unc := destKey(`\\server\share\a.gz`)
	rooted := destKey(`\server\share\a.gz`)
	if unc == rooted {
		t.Fatalf("UNC key %q collided with rooted key %q", unc, rooted)
	}
}

// Scanner → planner → destSet 을 이어, 원본 (SITE) 폴더 두 곳의 같은 이름이
// (SITE) 없는 목적지로 수렴하면 Scanner 가 먼저 준 것 하나만 남는다.
// 같은 .part 를 두 워커가 동시에 쓰는 상태가 만들어지지 않는다.
func TestPlan_ConvergingSourcesKeepFirstOnly(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RNX/2026/268/PALM/soch268a.26o.gz")
	r.file(t, "/RNX/2026/268/SOCH/soch268a.26o.gz")
	r.file(t, "/RNX/2026/268/SOCH/soch268b.26o.gz")

	p, err := newPlanner(
		domain.CategoryRINEX2Hourly,
		mustTpl(t, "/down/(YYYY)/(DOY)/"),
		verify.Verifier{}, // Grace 0 — osRemote 의 mtime 은 방금이다
		nil,
		slashJoin,
	)
	if err != nil {
		t.Fatal(err)
	}

	dests := newDestSet()

	var (
		kept   []string
		dups   []string
		winFor = map[string]string{}
	)

	_, err = scan.New(newSFTPLister(r)).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustTpl(t, "/RNX/(YYYY)/(DOY)/(SITE)/"),
		scan.Range{From: day268, To: day268},
		func(b scan.Batch) error {
			for _, e := range b.Entries {
				c, reason, err := p.evaluate(b.Dir, e, b.When)
				if err != nil {
					return err
				}

				if reason != "" {
					t.Fatalf("%s → %q", e.Name, reason)
				}

				ok, winner := dests.claim(c)
				if ok {
					kept = append(kept, c.RemotePath)
				} else {
					dups = append(dups, c.RemotePath)
					winFor[c.RemotePath] = winner
				}
			}

			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// 루트는 이름순(PALM → SOCH)이므로 PALM 쪽이 먼저이고 이긴다.
	wantKept := []string{
		"/RNX/2026/268/PALM/soch268a.26o.gz",
		"/RNX/2026/268/SOCH/soch268b.26o.gz",
	}

	if !reflect.DeepEqual(kept, wantKept) {
		t.Fatalf("kept = %v, want %v", kept, wantKept)
	}

	if !reflect.DeepEqual(dups, []string{"/RNX/2026/268/SOCH/soch268a.26o.gz"}) {
		t.Fatalf("dups = %v", dups)
	}

	if winFor[dups[0]] != wantKept[0] {
		t.Errorf("winner = %q, want %q", winFor[dups[0]], wantKept[0])
	}
}

// 로컬 Join 이 OS 규칙을 따르는지만 가볍게 본다. planner 는 join 을 주입받아
// 경로 규칙을 스스로 가정하지 않는다.
func TestEvaluate_UsesInjectedJoin(t *testing.T) {
	p, err := newPlanner(
		domain.CategoryRINEX2Daily,
		mustTpl(t, filepath.Join(t.TempDir(), "(YYYY)", "(DOY)")),
		verify.Verifier{},
		nil,
		func(dir, name string) string { return filepath.Join(dir, name) },
	)
	if err != nil {
		t.Fatal(err)
	}

	c, _, err := p.evaluate("/R/", oldEntry("soch2680.26o.gz", 1), day268)
	if err != nil {
		t.Fatal(err)
	}

	if c.LocalPath != filepath.Join(c.LocalDir, "soch2680.26o.gz") {
		t.Errorf("LocalPath = %q", c.LocalPath)
	}
}
