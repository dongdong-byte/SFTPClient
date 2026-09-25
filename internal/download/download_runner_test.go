package download

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
)

// DOWNLOAD 커밋 5 — Runner (DOWNLOAD_COMMIT_PLAN v1 §6).
//
// 원격은 임시 폴더의 실제 파일을 복사하는 fake, 로컬은 운영 구현
// OSLocalStore 그대로다. 로컬 판정·Rename·정리를 실제 파일시스템에서 본다.

// ── fake 원격 ───────────────────────────────────────────────────────

// fileRemote 는 root 아래 파일을 원격처럼 나열·복사한다.
//
// DownloadPart 는 워커가 동시에 부르므로 모든 상태를 mu 로 지킨다.
// 같은 .part 에 대한 동시 호출 수(active)를 세어 목적지 중복 제거를 확인한다.
type fileRemote struct {
	root string

	mu        sync.Mutex
	calls     []string
	active    map[string]int
	maxActive int

	// before 는 복사 전에 불린다. 오류를 돌려주면 복사하지 않고 그 오류로 끝난다.
	before func(ctx context.Context, remote, part string) error
	// after 는 복사 성공 직후 불린다 (Rename 전 경합 재현).
	after func(remote, part string)
	// shrink 가 true 면 마지막 1바이트를 빼고 쓴다 (크기 불일치 재현).
	shrink bool
}

func newFileRemote(t *testing.T) *fileRemote {
	return &fileRemote{root: t.TempDir(), active: map[string]int{}}
}

func (r *fileRemote) localOf(remote string) string {
	return filepath.Join(r.root, filepath.FromSlash(strings.TrimPrefix(remote, "/")))
}

func (r *fileRemote) ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	return (&osRemote{root: r.root}).ReadDir(ctx, dir)
}

func (r *fileRemote) DownloadPart(ctx context.Context, remote, part string) error {
	r.mu.Lock()
	r.calls = append(r.calls, remote)
	r.active[part]++
	if r.active[part] > r.maxActive {
		r.maxActive = r.active[part]
	}
	before, after, shrink := r.before, r.after, r.shrink
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.active[part]--
		r.mu.Unlock()
	}()

	if err := ctx.Err(); err != nil {
		return err
	}

	if before != nil {
		if err := before(ctx, remote, part); err != nil {
			return err
		}
	}

	data, err := os.ReadFile(r.localOf(remote))
	if err != nil {
		return err
	}

	if shrink && len(data) > 0 {
		data = data[:len(data)-1]
	}

	// 처음부터 덮어쓴다 (transport.DownloadPart 의 D9 계약).
	if err := os.WriteFile(part, data, 0o644); err != nil {
		return err
	}

	if after != nil {
		after(remote, part)
	}

	return nil
}

func (r *fileRemote) put(t *testing.T, remote string, data string) {
	t.Helper()

	p := r.localOf(remote)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *fileRemote) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return len(r.calls)
}

// ── 공통 헬퍼 ───────────────────────────────────────────────────────

type runEnv struct {
	remote *fileRemote
	local  string // 로컬 목적지 루트
	logs   *bytes.Buffer
	runner *Runner
}

func newRunEnv(t *testing.T) *runEnv {
	t.Helper()

	logs := &bytes.Buffer{}

	env := &runEnv{
		remote: newFileRemote(t),
		local:  t.TempDir(),
		logs:   logs,
	}

	env.runner = &Runner{
		Remote: env.remote,
		Local:  OSLocalStore{},
		Opts: Options{
			MaxWorkers: 4,
			Logger:     log.New(&syncWriter{w: logs}, "", 0),
		},
	}

	return env
}

// syncWriter 는 여러 goroutine 이 쓰는 로그 버퍼를 지킨다.
type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.w.Write(p)
}

func (e *runEnv) job(t *testing.T, cat domain.Category, remoteTpl, localRel string) Job {
	t.Helper()

	return Job{
		Category:   cat,
		RemotePath: mustTpl(t, remoteTpl),
		LocalPath:  mustTpl(t, filepath.Join(e.local, filepath.FromSlash(localRel))),
	}
}

func (e *runEnv) run(t *testing.T, ctx context.Context, jobs ...Job) (Report, error) {
	t.Helper()

	return e.runner.Run(ctx, jobs, scan.Range{From: day268, To: day268})
}

func (e *runEnv) localFile(rel string) string {
	return filepath.Join(e.local, filepath.FromSlash(rel))
}

// localTree 는 로컬 목적지 아래 모든 파일의 상대 경로다 ('/' 구분, 정렬).
func (e *runEnv) localTree(t *testing.T) []string {
	t.Helper()

	var out []string

	err := filepath.WalkDir(e.local, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if !d.IsDir() {
			rel, _ := filepath.Rel(e.local, p)
			out = append(out, filepath.ToSlash(rel))
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	sort.Strings(out)

	return out
}

func mustRead(t *testing.T, p string) string {
	t.Helper()

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func assertNoPart(t *testing.T, e *runEnv) {
	t.Helper()

	for _, f := range e.localTree(t) {
		if domain.IsPartFile(f) {
			t.Errorf(".part left behind: %s", f)
		}
	}
}

// ── D1 · D2 ────────────────────────────────────────────────────────

// D1: 정상 파일이 (SITE)·(HH) 전개된 목적지에 생긴다.
// D2: 같은 설정으로 재실행하면 수신 0, 기존으로 집계된다.
func TestRun_D1_ReceivesToExpandedDestination_D2_RerunIsExists(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/SOCH/soch268a.26o.gz", "aaaa")
	e.remote.put(t, "/RNX/2026/268/SOCH/soch268n.26o.gz", "nnnnnn")
	e.remote.put(t, "/RNX/2026/268/PALM/palm268x.26d.gz", "x")

	job := e.job(t, domain.CategoryRINEX2Hourly,
		"/RNX/(YYYY)/(DOY)/(SITE)/", "(YYYY)/(DOY)/(SITE)/(HH)/")

	rep, err := e.run(t, context.Background(), job)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := []string{
		"2026/268/PALM/23/palm268x.26d.gz",
		"2026/268/SOCH/00/soch268a.26o.gz",
		"2026/268/SOCH/13/soch268n.26o.gz",
	}

	if got := e.localTree(t); !equalStrings(got, want) {
		t.Fatalf("local tree =\n%v\nwant\n%v", got, want)
	}

	if mustRead(t, e.localFile("2026/268/SOCH/13/soch268n.26o.gz")) != "nnnnnn" {
		t.Error("content mismatch")
	}

	cr := rep.Categories[0]
	if cr.Downloaded != 3 || cr.Failed != 0 || cr.Scan.Files != 3 || rep.Downloaded() != 3 {
		t.Fatalf("first run = %+v", cr)
	}

	// D2
	before := e.remote.callCount()

	rep, err = e.run(t, context.Background(), job)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}

	cr = rep.Categories[0]
	if cr.Downloaded != 0 || cr.Reasons[ReasonExists] != 3 || cr.Failed != 0 {
		t.Fatalf("rerun = %+v", cr)
	}

	if e.remote.callCount() != before {
		t.Error("rerun called DownloadPart for existing files")
	}
}

// ── D4 ─────────────────────────────────────────────────────────────

// .part·0바이트·유예 미달·미래 mtime 은 제외만 되고 다음 회차에서 다시
// 판정된다. 제외는 어떤 로컬 흔적도 남기지 않는다.
func TestRun_D4_ExcludedFilesAreReevaluatedNextRun(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz.part", "partial")
	e.remote.put(t, "/RNX/2026/268/soch268b.26o.gz", "") // 0바이트
	e.remote.put(t, "/RNX/2026/268/soch268c.26o.gz", "ccc")
	e.remote.put(t, "/RNX/2026/268/soch268d.26o.gz", "ddd")

	realNow := time.Now()

	// d 는 원격 시계가 2시간 앞선 것처럼 미래 mtime.
	future := realNow.Add(2 * time.Hour)
	if err := os.Chtimes(e.remote.localOf("/RNX/2026/268/soch268d.26o.gz"), future, future); err != nil {
		t.Fatal(err)
	}

	e.runner.Opts.Grace = time.Minute

	now := realNow
	e.runner.Opts.Now = func() time.Time { return now }

	job := e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/")

	rep, err := e.run(t, context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	wantReasons := map[Reason]int{
		ReasonPart:        1,
		ReasonZero:        0, // 방금 쓰인 0바이트는 유예 쪽이 먼저다 (verify 순서)
		ReasonGrace:       2, // b(0바이트), c
		ReasonFutureMTime: 1, // d
	}

	for r, n := range wantReasons {
		if cr.Reasons[r] != n {
			t.Errorf("run1 %s = %d, want %d (%v)", r, cr.Reasons[r], n, cr.Reasons)
		}
	}

	if cr.Downloaded != 0 || len(e.localTree(t)) != 0 || e.remote.callCount() != 0 {
		t.Fatalf("run1 left traces: downloaded=%d tree=%v calls=%d",
			cr.Downloaded, e.localTree(t), e.remote.callCount())
	}

	// 다음 회차: 3시간 뒤, 0바이트 파일은 그 사이 채워졌다.
	e.remote.put(t, "/RNX/2026/268/soch268b.26o.gz", "bbbb")
	old := realNow.Add(-time.Hour)
	for _, n := range []string{"soch268b.26o.gz", "soch268c.26o.gz"} {
		if err := os.Chtimes(e.remote.localOf("/RNX/2026/268/"+n), old, old); err != nil {
			t.Fatal(err)
		}
	}

	now = realNow.Add(3 * time.Hour)

	rep, err = e.run(t, context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	cr = rep.Categories[0]
	if cr.Downloaded != 3 || cr.Reasons[ReasonPart] != 1 {
		t.Fatalf("run2 = downloaded %d reasons %v", cr.Downloaded, cr.Reasons)
	}

	// 원격 .part 는 끝까지 받지 않는다.
	for _, f := range e.localTree(t) {
		if strings.Contains(f, "soch268a") {
			t.Errorf("remote temp file was received: %s", f)
		}
	}
}

// ── D5 ─────────────────────────────────────────────────────────────

// 전송 중 회차가 취소되면 새 수신을 시작하지 않고, 진행 중이던 파일은
// 실패로 끝나며, 정상 cleanup 환경에서는 최종 파일과 .part 가 모두 없다.
func TestRun_D5_CancelStopsNewDownloadsAndCleansUp(t *testing.T) {
	e := newRunEnv(t)
	e.runner.Opts.MaxWorkers = 1

	for _, s := range []string{"a", "b", "c", "d", "e", "f"} {
		e.remote.put(t, "/RNX/2026/268/soch268"+s+".26o.gz", "data-"+s)
	}

	started := make(chan struct{})
	var once sync.Once

	// 첫 파일: .part 를 반쯤 쓴 뒤 회차 취소까지 블록된다 (stall 재현).
	e.remote.before = func(ctx context.Context, remote, part string) error {
		if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
			return err
		}

		once.Do(func() { close(started) })
		<-ctx.Done()

		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		rep Report
		err error
	}

	done := make(chan result, 1)

	go func() {
		rep, err := e.run(t, ctx, e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
		done <- result{rep, err}
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("download never started")
	}

	cancel()

	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if !errors.Is(res.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (a canceled run must not look successful)", res.err)
	}

	if n := e.remote.callCount(); n != 1 {
		t.Errorf("DownloadPart calls = %d, want 1 (no new downloads after cancel)", n)
	}

	cr := res.rep.Categories[0]
	if cr.Downloaded != 0 || cr.Failed != 1 {
		t.Errorf("report = downloaded %d failed %d notStarted %d", cr.Downloaded, cr.Failed, cr.NotStarted)
	}

	// 취소된 회차에서도 cleanup 은 돈다 (context.WithoutCancel).
	if tree := e.localTree(t); len(tree) != 0 {
		t.Fatalf("local tree after cancel = %v, want empty", tree)
	}
}

// 이미 취소된 ctx 로는 아무것도 나열·수신하지 않는다.
func TestRun_PreCanceledDoesNothing(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "a")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := e.run(t, ctx, e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}

	if e.remote.callCount() != 0 || len(e.localTree(t)) != 0 {
		t.Fatal("work was done on a canceled run")
	}
}

// ── D6 ─────────────────────────────────────────────────────────────

// .part 크기가 원격 Entry 크기와 다르면 Rename 하지 않고 .part 를 지우며
// 실패로 센다.
func TestRun_D6_SizeMismatchIsFailureWithoutRename(t *testing.T) {
	e := newRunEnv(t)
	e.remote.shrink = true
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "abcdef")

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	if cr.Failed != 1 || cr.Downloaded != 0 || rep.Failed() != 1 {
		t.Fatalf("report = %+v", cr)
	}

	if len(cr.Failures) != 1 || !strings.Contains(cr.Failures[0].Err.Error(), "size mismatch: .part=5 remote=6") {
		t.Errorf("failures = %v", cr.Failures)
	}

	if tree := e.localTree(t); len(tree) != 0 {
		t.Fatalf("local tree = %v, want empty (no final, no .part)", tree)
	}

	if !strings.Contains(e.logs.String(), "[DOWNLOAD] 실패 원격=/RNX/2026/268/soch268a.26o.gz") {
		t.Errorf("per-file failure log missing:\n%s", e.logs.String())
	}
}

// ── D9 ─────────────────────────────────────────────────────────────

// 이전 회차가 남긴 .part 가 있어도 처음부터 받아 정상 완료한다.
func TestRun_D9_LeftoverPartIsReplaced(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "fresh")

	part := e.localFile("2026/268/soch268a.26o.gz.part")
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(part, []byte(strings.Repeat("STALE", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	if rep.Categories[0].Downloaded != 1 {
		t.Fatalf("report = %+v", rep.Categories[0])
	}

	if got := mustRead(t, e.localFile("2026/268/soch268a.26o.gz")); got != "fresh" {
		t.Fatalf("final = %q", got)
	}

	assertNoPart(t, e)
}

// ── 목적지 판정 · Rename 직전 재확인 ─────────────────────────────────

// 처음부터 기존·충돌·비정상이면 원격을 건드리지 않는다. 충돌은 덮어쓰지
// 않고 파일별 로그를 남긴다.
func TestRun_InitialTargetStates(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "same")    // 기존
	e.remote.put(t, "/RNX/2026/268/soch268b.26o.gz", "remote!") // 충돌
	e.remote.put(t, "/RNX/2026/268/soch268c.26o.gz", "x")       // 디렉터리 자리

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(os.MkdirAll(e.localFile("2026/268/soch268c.26o.gz"), 0o755))
	must(os.WriteFile(e.localFile("2026/268/soch268a.26o.gz"), []byte("same"), 0o644))
	must(os.WriteFile(e.localFile("2026/268/soch268b.26o.gz"), []byte("local"), 0o644))

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	if cr.Reasons[ReasonExists] != 1 || cr.Reasons[ReasonConflict] != 1 || cr.Reasons[ReasonBadTarget] != 1 {
		t.Fatalf("reasons = %v", cr.Reasons)
	}

	if e.remote.callCount() != 0 {
		t.Error("DownloadPart called for a non-absent destination")
	}

	if mustRead(t, e.localFile("2026/268/soch268b.26o.gz")) != "local" {
		t.Error("conflicting local file was overwritten")
	}

	for _, want := range []string{"[DOWNLOAD] 충돌 ", "[DOWNLOAD] 비정상대상 "} {
		if !strings.Contains(e.logs.String(), want) {
			t.Errorf("log missing %q:\n%s", want, e.logs.String())
		}
	}
}

// 전송 중 외부에서 최종 파일이 생기면 Rename 하지 않는다. 생긴 파일은 그대로
// 두고 .part 는 지운다. 생긴 것의 종류에 따라 기존·충돌·비정상대상이다.
func TestRun_RecheckBeforeRenameNeverOverwrites(t *testing.T) {
	cases := []struct {
		name   string
		create func(final string) error
		want   Reason
	}{
		{"같은 크기 파일 → 기존", func(f string) error { return os.WriteFile(f, []byte("REMOTE"), 0o644) }, ReasonExists},
		{"다른 크기 파일 → 충돌", func(f string) error { return os.WriteFile(f, []byte("other-writer"), 0o644) }, ReasonConflict},
		{"디렉터리 → 비정상대상", func(f string) error { return os.Mkdir(f, 0o755) }, ReasonBadTarget},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newRunEnv(t)
			e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "remote")

			final := e.localFile("2026/268/soch268a.26o.gz")

			var snapshot string

			e.remote.after = func(_, _ string) {
				if err := tc.create(final); err != nil {
					t.Error(err)
				}

				if fi, err := os.Lstat(final); err == nil && fi.Mode().IsRegular() {
					snapshot = mustRead(t, final)
				}
			}

			rep, err := e.run(t, context.Background(),
				e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
			if err != nil {
				t.Fatal(err)
			}

			cr := rep.Categories[0]
			if cr.Reasons[tc.want] != 1 || cr.Downloaded != 0 || cr.Failed != 0 {
				t.Fatalf("report = %+v", cr)
			}

			if snapshot != "" && mustRead(t, final) != snapshot {
				t.Fatal("the externally created final file was overwritten")
			}

			assertNoPart(t, e)
		})
	}
}

// ── 목적지 중복 · 동시성 ─────────────────────────────────────────────

// 같은 목적지로 수렴하는 원격 파일이 여럿이어도 한 worker 만 받는다.
// 같은 .part 에 대한 동시 DownloadPart 가 없다 (go test -race 로도 본다).
func TestRun_NoConcurrentDownloadForSameDestination(t *testing.T) {
	e := newRunEnv(t)
	e.runner.Opts.MaxWorkers = 8

	sites := []string{"AAAA", "BBBB", "CCCC", "DDDD", "EEEE", "FFFF"}
	for _, s := range sites {
		for _, h := range []string{"a", "b", "c"} {
			e.remote.put(t, "/RNX/2026/268/"+s+"/soch268"+h+".26o.gz", "same-"+h)
		}
	}

	// 겹칠 기회를 늘린다.
	e.remote.before = func(context.Context, string, string) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	// 목적지에 (SITE) 가 없어 관측소 6곳이 한 폴더로 수렴한다.
	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/(SITE)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	if cr.Downloaded != 3 || cr.Reasons[ReasonDuplicate] != 3*(len(sites)-1) {
		t.Fatalf("downloaded %d duplicates %d", cr.Downloaded, cr.Reasons[ReasonDuplicate])
	}

	e.remote.mu.Lock()
	maxActive := e.remote.maxActive
	e.remote.mu.Unlock()

	if maxActive > 1 {
		t.Fatalf("same .part downloaded concurrently (%d)", maxActive)
	}

	// 먼저 발견한 AAAA 쪽이 이긴다 — 로그에 승자가 남는다.
	if !strings.Contains(e.logs.String(), "먼저배정=/RNX/2026/268/AAAA/") {
		t.Errorf("duplicate log does not name the winner:\n%s", e.logs.String())
	}
}

// 두 Category 의 목적지가 같아도 destSet 이 회차 전체에 하나라서 한 번만 받는다.
func TestRun_DuplicateAcrossCategories(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/A/2026/268/soch268a.26o.gz", "x")
	e.remote.put(t, "/B/2026/268/soch268a.26o.gz", "x")

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/A/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"),
		e.job(t, domain.CategoryRINEX2Hourly, "/B/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if rep.Categories[0].Downloaded != 1 || rep.Categories[1].Reasons[ReasonDuplicate] != 1 {
		t.Fatalf("report = %+v", rep.Categories)
	}
}

// ── 실패와 정리 ────────────────────────────────────────────────────

// 수신 오류 한 건은 그 파일만 실패시키고 나머지는 계속한다. 같은 회차에
// 재시도하지 않는다.
func TestRun_OneFailureDoesNotStopOthers(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "a")
	e.remote.put(t, "/RNX/2026/268/soch268b.26o.gz", "b")

	boom := errors.New("connection reset")
	e.remote.before = func(_ context.Context, remote, part string) error {
		if strings.HasSuffix(remote, "soch268a.26o.gz") {
			_ = os.WriteFile(part, []byte("partial"), 0o644)
			return boom
		}
		return nil
	}

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	if cr.Failed != 1 || cr.Downloaded != 1 || !errors.Is(cr.Failures[0].Err, boom) {
		t.Fatalf("report = %+v", cr)
	}

	if e.remote.callCount() != 2 {
		t.Errorf("calls = %d, want 2 (no same-run retry)", e.remote.callCount())
	}

	assertNoPart(t, e)
}

// 정리 실패는 원래 원인 뒤에 덧붙는다. 주 원인이 먼저다.
func TestRun_CleanupFailureIsJoined(t *testing.T) {
	e := newRunEnv(t)
	e.remote.shrink = true
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "abc")

	denied := errors.New("remove denied")
	e.runner.Local = failingRemove{OSLocalStore: OSLocalStore{}, err: denied}

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	f := rep.Categories[0].Failures[0].Err
	if !errors.Is(f, denied) || !strings.HasPrefix(f.Error(), "size mismatch") {
		t.Fatalf("failure = %v, want the size mismatch first and the cleanup error joined", f)
	}
}

// 전송 중 최종 파일이 생긴 뒤 .part 정리까지 실패하면, 최종 파일은
// 보존하되 회차를 성공으로 위장하지 않는다.
func TestRun_RecheckCleanupFailureIsCountedAsFailure(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "remote")

	final := e.localFile("2026/268/soch268a.26o.gz")
	e.remote.after = func(_, _ string) {
		if err := os.WriteFile(final, []byte("external"), 0o644); err != nil {
			t.Error(err)
		}
	}

	denied := errors.New("remove denied")
	e.runner.Local = failingRemove{OSLocalStore: OSLocalStore{}, err: denied}

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	if cr.Failed != 1 || cr.Downloaded != 0 || cr.Reasons[ReasonConflict] != 0 {
		t.Fatalf("report = %+v, want cleanup failure", cr)
	}
	if len(cr.Failures) != 1 || !errors.Is(cr.Failures[0].Err, denied) ||
		!strings.Contains(cr.Failures[0].Err.Error(), string(ReasonConflict)) {
		t.Fatalf("failure = %v, want conflict context and cleanup cause", cr.Failures)
	}
	if got := mustRead(t, final); got != "external" {
		t.Fatalf("external final changed: %q", got)
	}
	if !strings.Contains(e.logs.String(), "[DOWNLOAD] 임시파일정리실패") {
		t.Fatalf("cleanup failure log missing:\n%s", e.logs.String())
	}
}

type failingRemove struct {
	OSLocalStore
	err error
}

func (f failingRemove) Remove(context.Context, string) error { return f.err }

func TestRun_InputChecks(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	if _, err := (&Runner{Local: OSLocalStore{}}).Run(ctx, nil, scan.Range{}); !errors.Is(err, errNilRemote) {
		t.Errorf("nil remote: %v", err)
	}

	if _, err := (&Runner{Remote: &mapRemote{}}).Run(ctx, nil, scan.Range{}); err == nil {
		t.Error("nil local accepted")
	}

	r := &Runner{Remote: &mapRemote{}, Local: OSLocalStore{}}
	if _, err := r.Run(ctx, []Job{{Category: domain.CategoryRINEX2Daily}}, scan.Range{}); err == nil {
		t.Error("nil templates accepted")
	}
}

func TestRun_ReportDurationIsReturned(t *testing.T) {
	e := newRunEnv(t)
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "x")
	e.remote.before = func(context.Context, string, string) error {
		time.Sleep(5 * time.Millisecond)
		return nil
	}

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Duration <= 0 {
		t.Fatalf("Duration = %v, want elapsed time", rep.Duration)
	}
}

// ── OSLocalStore ────────────────────────────────────────────────────

func TestOSLocalStore(t *testing.T) {
	ctx := context.Background()
	s := OSLocalStore{}
	dir := t.TempDir()

	t.Run("Remove: 없으면 성공", func(t *testing.T) {
		if err := s.Remove(ctx, filepath.Join(dir, "none.part")); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Remove: 빈 디렉터리도 지우지 않는다", func(t *testing.T) {
		d := filepath.Join(dir, "x.gz.part")
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}

		if err := s.Remove(ctx, d); err == nil {
			t.Fatal("directory removed")
		}

		if _, err := os.Stat(d); err != nil {
			t.Fatal("directory is gone")
		}
	})

	t.Run("Remove: 읽기 전용 파일도 지운다", func(t *testing.T) {
		f := filepath.Join(dir, "ro.part")
		if err := os.WriteFile(f, []byte("x"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(f, 0o444); err != nil {
			t.Fatal(err)
		}

		if err := s.Remove(ctx, f); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Lstat(f); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("still there: %v", err)
		}
	})

	t.Run("Size: 디렉터리는 오류", func(t *testing.T) {
		if _, err := s.Size(ctx, dir); err == nil {
			t.Fatal("directory size accepted")
		}
	})

	t.Run("Stat: 링크를 따라가지 않는다", func(t *testing.T) {
		target := filepath.Join(dir, "target")
		if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}

		link := filepath.Join(dir, "link")
		if err := os.Symlink(target, link); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}

		fi, err := s.Stat(ctx, link)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			t.Fatalf("Stat(link) = %v %v, want the link itself", fi, err)
		}
	})

	t.Run("취소된 ctx 는 시작 전에 거부", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		cancel()

		if err := s.EnsureDir(c, filepath.Join(dir, "never")); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}

		if _, err := os.Stat(filepath.Join(dir, "never")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatal("dir created on a canceled ctx")
		}
	})
}

// LocalStore 계약을 운영 구현이 만족한다.
var _ LocalStore = OSLocalStore{}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
