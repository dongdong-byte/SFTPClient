package download

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/transport"
)

// DOWNLOAD 커밋 3 — RemoteFS 와 SFTP 나열 어댑터 (DOWNLOAD_COMMIT_PLAN v1 §4).
//
// 어댑터를 실제 scan.Scanner 에 꽂아 §4.3 완료 조건을 확인한다. scan 코드는
// 이 커밋에서 바꾸지 않는다 — 여기 테스트가 통과한다는 것이 "PUT 의 Scanner
// 를 수정 없이 원격에 쓴다"의 증거다.

// 구조적 만족은 테스트에서만 고정한다 (생산 코드는 transport 를 import
// 하지 않는다 — put.Uploader 와 같은 방식).
var (
	_ RemoteFS       = (*transport.SFTPFS)(nil)
	_ scan.DirLister = sftpLister{}
	_ RemoteFS       = (*osRemote)(nil)
	_ RemoteFS       = (*mapRemote)(nil)
)

// ── fake: 로컬 폴더를 원격처럼 ──────────────────────────────────────

// osRemote 는 로컬 임시 폴더를 SFTP 원격처럼 보여 준다.
//
// 원격 경로 "/RINEX2/2026/268/" 는 root 아래 같은 상대 경로로 대응한다.
// ReadDir 은 transport.SFTPFS 의 계약(lstat 의미, 없음은 fs.ErrNotExist)을
// 실제 파일시스템으로 재현한다 — 링크·없는 폴더가 진짜로 그렇게 보인다.
type osRemote struct {
	root  string
	calls []string
}

func (r *osRemote) local(remote string) string {
	return filepath.Join(r.root, filepath.FromSlash(strings.TrimPrefix(remote, "/")))
}

func (r *osRemote) ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.calls = append(r.calls, dir)

	des, err := os.ReadDir(r.local(dir))
	if err != nil {
		return nil, err
	}

	out := make([]fs.FileInfo, 0, len(des))
	for _, de := range des {
		fi, err := de.Info() // lstat 의미
		if err != nil {
			return nil, err
		}

		out = append(out, fi)
	}

	return out, nil
}

func (r *osRemote) DownloadPart(context.Context, string, string) error {
	return errors.New("not used by the lister")
}

func (r *osRemote) mkdir(t *testing.T, remote string) {
	t.Helper()

	if err := os.MkdirAll(r.local(remote), 0o755); err != nil {
		t.Fatal(err)
	}
}

func (r *osRemote) file(t *testing.T, remote string) {
	t.Helper()

	r.mkdir(t, path.Dir(remote))

	if err := os.WriteFile(r.local(remote), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// symlink 은 링크를 만든다. 권한이 없으면(Windows 비관리자) false.
func (r *osRemote) symlink(t *testing.T, target, link string) bool {
	t.Helper()

	if err := os.Symlink(r.local(target), r.local(link)); err != nil {
		t.Logf("symlink unavailable: %v", err)
		return false
	}

	return true
}

// ── fake: 서버 응답을 그대로 ────────────────────────────────────────

type fakeInfo struct {
	name string
	size int64
	mode fs.FileMode
	mt   time.Time
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return f.mt }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// mapRemote 는 디렉터리별 응답을 그대로 돌려준다.
type mapRemote struct {
	dirs   map[string][]fs.FileInfo
	errs   map[string]error
	gotCtx context.Context
}

func (m *mapRemote) ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	m.gotCtx = ctx

	if err, ok := m.errs[dir]; ok {
		return nil, err
	}

	infos, ok := m.dirs[dir]
	if !ok {
		return nil, fs.ErrNotExist
	}

	return infos, nil
}

func (m *mapRemote) DownloadPart(context.Context, string, string) error {
	return errors.New("not used by the lister")
}

// ── Scan 헬퍼 ───────────────────────────────────────────────────────

// day268 은 2026-09-25 UTC(DOY 268) 이다.
var day268 = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)

func mustTpl(t *testing.T, s string) *pathpl.Template {
	t.Helper()

	tpl, err := pathpl.Parse(s)
	if err != nil {
		t.Fatalf("pathpl.Parse(%q): %v", s, err)
	}

	return tpl
}

// scanRemote 는 어댑터를 실제 Scanner 에 꽂아 돌리고, 수집한 파일의
// "디렉터리/이름" 목록(정렬)과 Result 를 돌려준다.
func scanRemote(
	t *testing.T,
	remote RemoteFS,
	tplRaw string,
	from, to time.Time,
) ([]string, scan.Result) {
	t.Helper()

	var got []string

	res, err := scan.New(newSFTPLister(remote)).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustTpl(t, tplRaw),
		scan.Range{From: from, To: to},
		func(b scan.Batch) error {
			for _, e := range b.Entries {
				got = append(got, strings.TrimRight(b.Dir, "/")+"/"+e.Name)
			}
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	sort.Strings(got)

	return got, res
}

// ── §4.3 완료 조건 ──────────────────────────────────────────────────

// "/RINEX2/(YYYY)/(DOY)/ 아래 시간 폴더의 파일을 재귀 수집한다."
// 폴더 이름을 00~23 으로 해석하거나 제한하지 않는다 (§4.2).
func TestSFTPLister_RecursesUnderDateFolder(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RINEX2/2026/268/soch2680.26o.gz")
	r.file(t, "/RINEX2/2026/268/00/soch268a.26o.gz")
	r.file(t, "/RINEX2/2026/268/13/soch268n.26o.gz")
	r.file(t, "/RINEX2/2026/268/backup/deep/soch268x.26o.gz")
	r.mkdir(t, "/RINEX2/2026/268/empty")

	got, res := scanRemote(t, r, "/RINEX2/(YYYY)/(DOY)/", day268, day268)

	want := []string{
		"/RINEX2/2026/268/00/soch268a.26o.gz",
		"/RINEX2/2026/268/13/soch268n.26o.gz",
		"/RINEX2/2026/268/backup/deep/soch268x.26o.gz",
		"/RINEX2/2026/268/soch2680.26o.gz",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files =\n%v\nwant\n%v", got, want)
	}

	if res.Files != 4 || res.Missing != 0 || res.Errs != 0 || res.Irregular != 0 {
		t.Errorf("result = %+v", res)
	}

	// 날짜 폴더, 00, 13, backup, backup/deep, empty.
	if res.Dirs != 6 {
		t.Errorf("Dirs = %d, want 6", res.Dirs)
	}

	// 원격에 넘어간 경로는 템플릿 표기('/') 그대로다.
	for _, c := range r.calls {
		if strings.Contains(c, `\`) {
			t.Errorf("remote path uses a backslash: %q", c)
		}
	}
}

// "없는 날짜 폴더는 오류가 아니라 Missing 으로 집계된다."
func TestSFTPLister_MissingDateIsMissing(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RINEX2/2026/268/soch2680.26o.gz")

	// 267·268·269 중 268 만 있다.
	got, res := scanRemote(
		t, r, "/RINEX2/(YYYY)/(DOY)/",
		day268.AddDate(0, 0, -1), day268.AddDate(0, 0, 1),
	)

	if len(got) != 1 {
		t.Fatalf("files = %v, want one", got)
	}

	if res.Missing != 2 || res.Errs != 0 {
		t.Errorf("Missing = %d Errs = %d, want 2 and 0", res.Missing, res.Errs)
	}
}

// "(SITE) 원본 토큰이 패턴 나열로 수집된다."
// "패턴에 맞지 않는 폴더는 Unmatched 로 집계된다 (PATH v4 §8-2)."
func TestSFTPLister_SitePatternListsParent(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RNX/2026/268/SOCH/soch2680.26o.gz")
	r.file(t, "/RNX/2026/268/palm/palm2680.26o.gz") // 대소문자 무시
	r.file(t, "/RNX/2026/268/backup/old.26o.gz")    // 4자리 아님 → Unmatched
	r.file(t, "/RNX/2026/268/stray.26o.gz")         // 패턴 부모의 파일

	got, res := scanRemote(t, r, "/RNX/(YYYY)/(DOY)/(SITE)/", day268, day268)

	want := []string{
		"/RNX/2026/268/SOCH/soch2680.26o.gz",
		"/RNX/2026/268/palm/palm2680.26o.gz",
	}
	sort.Strings(want)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files =\n%v\nwant\n%v", got, want)
	}

	if res.PatternRoots != 2 {
		t.Errorf("PatternRoots = %d, want 2", res.PatternRoots)
	}

	if res.Unmatched != 1 || len(res.UnmatchedDetails) != 1 ||
		!strings.HasSuffix(res.UnmatchedDetails[0], "/backup") {
		t.Errorf("Unmatched = %d %v, want 1 [.../backup]", res.Unmatched, res.UnmatchedDetails)
	}

	if res.PatternParentFiles != 1 {
		t.Errorf("PatternParentFiles = %d, want 1", res.PatternParentFiles)
	}
}

// 관측소가 날짜 위에 있는 형태도 같은 경로로 수집된다.
func TestSFTPLister_SiteAboveDate(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/data/SOCH/2026/268/soch2680.26o.gz")
	r.file(t, "/data/SUW1/2026/268/suw12680.26o.gz")
	r.mkdir(t, "/data/SUW1/2026/267") // 다른 날짜 — 이번 범위 밖

	got, res := scanRemote(t, r, "/data/(SITE)/(YYYY)/(DOY)/", day268, day268)

	if len(got) != 2 || res.PatternRoots != 2 || res.Errs != 0 {
		t.Fatalf("files = %v result = %+v", got, res)
	}
}

// (HH) 원본 토큰도 같은 경로다. 00~23 판정은 Scanner 의 Match 몫이다.
func TestSFTPLister_HourPattern(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RNX/2026/268/00/soch268a.26o.gz")
	r.file(t, "/RNX/2026/268/23/soch268x.26o.gz")
	r.file(t, "/RNX/2026/268/24/bogus.26o.gz")  // 범위 밖
	r.file(t, "/RNX/2026/268/tmp/bogus.26o.gz") // 숫자 아님

	got, res := scanRemote(t, r, "/RNX/(YYYY)/(DOY)/(HH)/", day268, day268)

	if len(got) != 2 {
		t.Fatalf("files = %v, want 00 and 23 only", got)
	}

	if res.Unmatched != 2 {
		t.Errorf("Unmatched = %d, want 2 (24, tmp)", res.Unmatched)
	}
}

// "링크·비정규 대상은 Scanner 의 Irregular 정책을 그대로 따른다."
func TestSFTPLister_LinksAreIrregular(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RINEX2/2026/268/soch2680.26o.gz")
	r.file(t, "/elsewhere/13/secret.26o.gz")

	if !r.symlink(t, "/elsewhere/13", "/RINEX2/2026/268/13") {
		t.Skip("symlink unavailable on this host")
	}

	if !r.symlink(t, "/elsewhere/13/secret.26o.gz", "/RINEX2/2026/268/linked.26o.gz") {
		t.Skip("symlink unavailable on this host")
	}

	got, res := scanRemote(t, r, "/RINEX2/(YYYY)/(DOY)/", day268, day268)

	if !reflect.DeepEqual(got, []string{"/RINEX2/2026/268/soch2680.26o.gz"}) {
		t.Fatalf("files = %v, links must be neither followed nor collected", got)
	}

	if res.Irregular != 2 {
		t.Errorf("Irregular = %d, want 2", res.Irregular)
	}

	for _, c := range r.calls {
		if strings.Contains(c, "/13") {
			t.Errorf("descended into a linked directory: %q", c)
		}
	}
}

// (SITE) 자리의 링크 폴더도 따라가지 않는다 (PATH v4 R4).
func TestSFTPLister_LinkedSiteFolderNotFollowed(t *testing.T) {
	r := &osRemote{root: t.TempDir()}

	r.file(t, "/RNX/2026/268/SOCH/soch2680.26o.gz")
	r.file(t, "/other/PALM/palm2680.26o.gz")

	if !r.symlink(t, "/other/PALM", "/RNX/2026/268/PALM") {
		t.Skip("symlink unavailable on this host")
	}

	got, res := scanRemote(t, r, "/RNX/(YYYY)/(DOY)/(SITE)/", day268, day268)

	if len(got) != 1 || res.PatternRoots != 1 || res.Irregular != 1 {
		t.Fatalf("files = %v result = %+v", got, res)
	}
}

// 오류는 감싸지 않고 올린다: 없음은 Missing, 그 외는 Errs/Failures.
func TestSFTPLister_ErrorsPassThrough(t *testing.T) {
	denied := errors.New("sftp: permission denied")

	m := &mapRemote{
		dirs: map[string][]fs.FileInfo{
			"/R/2026/268/": {
				fakeInfo{name: "13", mode: fs.ModeDir | 0o755},
				fakeInfo{name: "14", mode: fs.ModeDir | 0o755},
			},
			"/R/2026/268/14": {
				fakeInfo{name: "soch268o.26o.gz", size: 10, mode: 0o644},
			},
		},
		errs: map[string]error{
			"/R/2026/268/13": denied,
		},
	}

	got, res := scanRemote(t, m, "/R/(YYYY)/(DOY)/", day268, day268.AddDate(0, 0, 1))

	if !reflect.DeepEqual(got, []string{"/R/2026/268/14/soch268o.26o.gz"}) {
		t.Fatalf("files = %v", got)
	}

	if res.Errs != 1 || len(res.Failures) != 1 || !errors.Is(res.Failures[0], denied) {
		t.Errorf("Errs = %d Failures = %v, want the permission error kept", res.Errs, res.Failures)
	}

	// 269 는 mapRemote 에 없어 fs.ErrNotExist → Missing.
	if res.Missing != 1 {
		t.Errorf("Missing = %d, want 1", res.Missing)
	}
}

// ── 어댑터 단위 ─────────────────────────────────────────────────────

func TestEntryOf(t *testing.T) {
	t.Parallel()

	mt := time.Date(2026, 9, 25, 13, 5, 0, 0, time.UTC)

	cases := []struct {
		name  string
		in    fakeInfo
		isDir bool
		typ   fs.FileMode
	}{
		{
			name:  "일반 파일 — 권한 비트는 버린다",
			in:    fakeInfo{name: "a.gz", size: 42, mode: 0o644, mt: mt},
			isDir: false,
			typ:   0,
		},
		{
			name:  "디렉터리",
			in:    fakeInfo{name: "13", mode: fs.ModeDir | 0o755},
			isDir: true,
			typ:   fs.ModeDir,
		},
		{
			name:  "링크 — 디렉터리로 바꾸지 않는다",
			in:    fakeInfo{name: "13", mode: fs.ModeSymlink | 0o777},
			isDir: false,
			typ:   fs.ModeSymlink,
		},
		{
			name:  "권한 필드가 없는 응답(Mode 0) — 일반 파일 관측값을 보존",
			in:    fakeInfo{name: "a.gz", size: 7, mode: 0},
			isDir: false,
			typ:   0,
		},
		{
			name:  "권한 000 인 파일도 일반 파일",
			in:    fakeInfo{name: "a.gz", mode: 0},
			isDir: false,
			typ:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := entryOf(tc.in)

			if e.Name != tc.in.name || e.Size != tc.in.size || !e.MTime.Equal(tc.in.mt) {
				t.Errorf("fields not preserved: %+v", e)
			}

			if e.IsDir != tc.isDir || e.Type != tc.typ {
				t.Errorf("IsDir = %v Type = %v, want %v %v", e.IsDir, e.Type, tc.isDir, tc.typ)
			}
		})
	}
}

// 경로 한 단이 아닌 이름은 버리지 않고 Irregular 로 드러낸다.
func TestEntryOf_UnsafeNamesAreIrregular(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"", ".", "..", "a/b", `..\..\x`, "a\x00b",
		"soch2680.26o.gz.", "soch2680.26o.gz ",
		"NUL", "nul.gz", "CON.txt", "com1.26o", "LPT9",
	} {
		for _, mode := range []fs.FileMode{0o644, fs.ModeDir | 0o755} {
			e := entryOf(fakeInfo{name: name, mode: mode})

			if e.Type != fs.ModeIrregular || e.IsDir || e.IsRegular() {
				t.Errorf("name %q mode %v → %+v, want irregular", name, mode, e)
			}
		}
	}
}

// 안전하지 않은 이름과 관측된 비정규 항목은 Scanner 에서 하강·수집 없이
// Irregular 로 집계된다. Mode 0 일반 파일은 정상 수집한다.
func TestSFTPLister_UnsafeEntriesAreIrregularAndModeZeroFileIsCollected(t *testing.T) {
	m := &mapRemote{
		dirs: map[string][]fs.FileInfo{
			"/R/2026/268/": {
				fakeInfo{name: "soch2680.26o.gz", mode: 0o644},
				fakeInfo{name: "palm2680.26o.gz", size: 9, mode: 0},
				fakeInfo{name: "../escape", mode: fs.ModeDir | 0o755},
				fakeInfo{name: "odd", mode: fs.ModeDir | fs.ModeSymlink},
				fakeInfo{name: "NUL.gz", size: 4, mode: 0o644},
				fakeInfo{name: "soch2680.26o.gz.", size: 4, mode: 0o644},
			},
		},
	}

	got, res := scanRemote(t, m, "/R/(YYYY)/(DOY)/", day268, day268)

	want := []string{
		"/R/2026/268/palm2680.26o.gz",
		"/R/2026/268/soch2680.26o.gz",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v", got)
	}

	if res.Files != 2 || res.Irregular != 4 || res.Dirs != 1 {
		t.Errorf("Files = %d Irregular = %d Dirs = %d, want 2, 4 and 1",
			res.Files, res.Irregular, res.Dirs)
	}
}

func TestSFTPLister_ContextPassedThrough(t *testing.T) {
	t.Parallel()

	type key struct{}

	m := &mapRemote{dirs: map[string][]fs.FileInfo{"/x": nil}}
	ctx := context.WithValue(context.Background(), key{}, "run-1")

	if _, err := newSFTPLister(m).List(ctx, "/x"); err != nil {
		t.Fatal(err)
	}

	if m.gotCtx == nil || m.gotCtx.Value(key{}) != "run-1" {
		t.Error("the caller's context was not passed to RemoteFS")
	}
}

func TestSFTPLister_ListOrderIsDeterministic(t *testing.T) {
	t.Parallel()

	m := &mapRemote{dirs: map[string][]fs.FileInfo{
		"/x": {
			fakeInfo{name: "soch268a.26o.gz", mode: 0o644},
			fakeInfo{name: "SOCH268A.26O.GZ", mode: 0o644},
			fakeInfo{name: "palm268a.26o.gz", mode: 0o644},
		},
	}}

	entries, err := newSFTPLister(m).List(context.Background(), "/x")
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, e := range entries {
		got = append(got, e.Name)
	}
	want := []string{"SOCH268A.26O.GZ", "palm268a.26o.gz", "soch268a.26o.gz"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want stable %v", got, want)
	}
}

func TestSFTPLister_ContractViolations(t *testing.T) {
	t.Parallel()

	t.Run("RemoteFS nil 은 빈 목록이 아니라 오류", func(t *testing.T) {
		_, err := newSFTPLister(nil).List(context.Background(), "/x")
		if !errors.Is(err, errNilRemote) {
			t.Fatalf("err = %v, want errNilRemote", err)
		}
	})

	t.Run("nil ctx", func(t *testing.T) {
		//nolint:staticcheck // 계약 위반 입력을 일부러 넣는다.
		_, err := newSFTPLister(&mapRemote{}).List(nil, "/x")
		if err == nil {
			t.Fatal("nil ctx accepted")
		}
	})

	t.Run("nil 항목은 추측하지 않고 목록 전체를 실패", func(t *testing.T) {
		m := &mapRemote{dirs: map[string][]fs.FileInfo{
			"/x": {fakeInfo{name: "a.gz", mode: 0o644}, nil},
		}}

		entries, err := newSFTPLister(m).List(context.Background(), "/x")
		if err == nil || entries != nil {
			t.Fatalf("entries = %v err = %v, want no partial list", entries, err)
		}
	})

	t.Run("있는 경로의 목록 실패는 Missing 으로 올라가지 않는다", func(t *testing.T) {
		// 디렉터리는 있는데 목록만 실패한 오류. ErrNotExist 로 올리면
		// Scanner 가 그 날짜를 빈 슬롯으로 넘겨 파일을 받지 않는다.
		listFailed := errors.New("read dir: exists but listing failed")
		m := &mapRemote{errs: map[string]error{"/R/2026/268/": listFailed}}

		_, res := scanRemote(t, m, "/R/(YYYY)/(DOY)/", day268, day268)
		if res.Missing != 0 || res.Errs != 1 || !errors.Is(res.Failures[0], listFailed) {
			t.Fatalf("Missing = %d Errs = %d Failures = %v", res.Missing, res.Errs, res.Failures)
		}
	})

	t.Run("fs.ErrNotExist 는 판별 가능한 채로 올라간다", func(t *testing.T) {
		_, err := newSFTPLister(&mapRemote{}).List(context.Background(), "/missing")
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want fs.ErrNotExist", err)
		}
	})
}
