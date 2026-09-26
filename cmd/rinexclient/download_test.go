package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/lock"
	"SFTPClient/internal/security"
	"SFTPClient/internal/transport"
)

// DOWNLOAD 커밋 7 — main 배선 (DOWNLOAD_COMMIT_PLAN v1 §8.3).
//
// run() 은 한 테스트 프로세스에서 한 번만 부를 수 있어(flag.CommandLine,
// autoresend_wiring_test.go 가 이미 쓴다) runDownload 를 직접 부른다.
// 설정은 실제 config.Load 로 읽는다. 원격은 임시 폴더를 쓰는 대역이다.
// 실제 SFTPGo 확인(D14)은 사무실에서 한다.

// 판정 시각. DeepScanHour(4)와 겹치지 않는 10시다.
var dlNow = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC) // DOY 268

// ── 원격 대역 ───────────────────────────────────────────────────────

type fakeConn struct {
	root string

	mu      sync.Mutex
	calls   int
	lastCtx context.Context
	aborted bool

	// canceledBeforeAbort 는 Abort 가 불렸을 때 수신 ctx 가 이미 취소돼
	// 있었는지다 (D10 — cancelRun → Abort 순서).
	canceledBeforeAbort bool

	abortCh chan struct{}
	once    sync.Once

	// block 이면 DownloadPart 가 Abort 또는 취소까지 멈추고, Progress 는
	// "진행 중 1개, 오래 조용함"을 보고한다 (stall 재현).
	block bool

	// fail 이면 DownloadPart 가 이 오류로 끝난다.
	fail error

	// readDirFail이면 원격 목록 호출이 이 오류로 끝난다.
	readDirFail error

	closed atomic.Bool
}

func newFakeConn(root string) *fakeConn {
	return &fakeConn{root: root, abortCh: make(chan struct{})}
}

func (f *fakeConn) local(remote string) string {
	return filepath.Join(f.root, filepath.FromSlash(strings.TrimPrefix(remote, "/")))
}

func (f *fakeConn) ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.readDirFail != nil {
		return nil, f.readDirFail
	}

	des, err := os.ReadDir(f.local(dir))
	if err != nil {
		return nil, err
	}

	out := make([]fs.FileInfo, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, fi)
	}

	return out, nil
}

func (f *fakeConn) DownloadPart(ctx context.Context, remote, part string) error {
	f.mu.Lock()
	f.calls++
	f.lastCtx = ctx
	block, fail := f.block, f.fail
	f.mu.Unlock()

	// 실패·중단 전에 .part 를 남긴다. 실제 DownloadPart 도 복사 중에
	// 파일을 만들어 둔다. 배선이 정리하지 않으면 다음 회차까지 남는다.
	if fail != nil || block {
		if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
			return err
		}
	}

	if fail != nil {
		return fail
	}

	if block {
		select {
		case <-f.abortCh:
			return errors.New("connection closed")
		case <-ctx.Done():
			<-f.abortCh // 실제 SFTP 도 Abort 전에는 깨지 않는다
			return ctx.Err()
		}
	}

	data, err := os.ReadFile(f.local(remote))
	if err != nil {
		return err
	}

	return os.WriteFile(part, data, 0o644)
}

func (f *fakeConn) Progress() (int64, time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.block && f.calls > 0 && !f.aborted {
		return 1, time.Hour
	}

	return 0, 0
}

func (f *fakeConn) Abort() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !f.aborted && f.lastCtx != nil && f.lastCtx.Err() != nil {
		f.canceledBeforeAbort = true
	}

	f.aborted = true
	f.once.Do(func() { close(f.abortCh) })
}

func (f *fakeConn) Close() error {
	f.closed.Store(true)
	return nil
}

var _ downloadConn = (*fakeConn)(nil)
var _ downloadConn = (*transport.SFTPFS)(nil)

// ── 설정 ────────────────────────────────────────────────────────────

type dlEnv struct {
	root   string // 설치 폴더 (config.ini 위치)
	remote string // 원격 대역의 루트
	local  string // 로컬 목적지 루트
	cfg    *config.Config
	logs   *bytes.Buffer
	detail *bytes.Buffer
}

func newDLEnv(t *testing.T) *dlEnv {
	t.Helper()

	root := t.TempDir()
	e := &dlEnv{
		root:   root,
		remote: filepath.Join(root, "remote"),
		local:  filepath.Join(root, "down"),
		logs:   &bytes.Buffer{},
		detail: &bytes.Buffer{},
	}

	for _, d := range []string{"data", "keys", "logs", e.remote} {
		p := d
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, d)
		}
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, f := range []string{"keys/id_ed25519", "keys/known_hosts"} {
		if err := os.WriteFile(filepath.Join(root, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	localTpl := filepath.Join(e.local, "(YYYY)", "(DOY)") + string(filepath.Separator)

	ini := `[GENERAL]
Mode = download
Transport = sftp
RepostDownloaded = false
LockStaleSeconds = 10800
LockPath = data/rinexclient.lock

[SCAN]
ScanRecentDays = 1
ScanDays = 7
DeepScanHour = 4
UseDirMtimeSkip = false

[DOWNLOAD]
Sites =
MaxWorkers = 2
GraceSeconds = 0

[DOWNLOAD.SFTP]
AuthMethod = publickey
Host = 10.0.0.9
Port = 22
User = getter
PrivateKey = keys/id_ed25519
KnownHosts = keys/known_hosts

[DOWNLOAD.RINEX2_DAILY]
Enabled = true
RemotePath = /RNX/(YYYY)/(DOY)/
LocalPath = ` + localTpl + `

[DOWNLOAD.RINEX2_HOURLY]
Enabled = false
RemotePath = /RNXH/(YYYY)/(DOY)/
LocalPath = ` + localTpl + `h

[DOWNLOAD.RINEX3_DAILY]
Enabled = false
RemotePath = /R3D/(YYYY)/(DOY)/
LocalPath = ` + localTpl + `r3d

[DOWNLOAD.RINEX3_HOURLY]
Enabled = false
RemotePath = /R3H/(YYYY)/(DOY)/
LocalPath = ` + localTpl + `r3h

[DOWNLOAD.RINEX4_DAILY]
Enabled = false
RemotePath = /R4D/(YYYY)/(DOY)/
LocalPath = ` + localTpl + `r4d

[DOWNLOAD.RINEX4_HOURLY]
Enabled = false
RemotePath = /R4H/(YYYY)/(DOY)/
LocalPath = ` + localTpl + `r4h

[LOG]
Level = info
Dir = logs
RetentionDays = 30
`

	path := filepath.Join(root, "config.ini")
	if err := os.WriteFile(path, []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(path, security.New())
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	e.cfg = cfg

	return e
}

func (e *dlEnv) putRemote(t *testing.T, rel, data string) {
	t.Helper()

	p := filepath.Join(e.remote, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *dlEnv) run(t *testing.T, conn *fakeConn) (error, int) {
	t.Helper()

	dials := 0
	dial := func(transport.SFTPDialOptions) (downloadConn, error) {
		dials++
		return conn, nil
	}

	err := runDownload(
		context.Background(),
		e.cfg,
		false,
		dlNow,
		dial,
		log.New(e.logs, "", 0),
		log.New(e.detail, "", 0),
	)

	return err, dials
}

// ── 정상 회차 · D12 역방향 ─────────────────────────────────────────

// 정상 회차: 받고, 보고하고, 접속을 닫고, lock 을 푼다. PUT 장부 DB 는
// 어디에도 생기지 않는다 (D12 역방향).
func TestRunDownload_ReceivesReportsAndOpensNoLedger(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")

	conn := newFakeConn(e.remote)

	err, dials := e.run(t, conn)
	if err != nil {
		t.Fatalf("runDownload: %v\n%s", err, e.logs.String())
	}

	if dials != 1 || !conn.closed.Load() {
		t.Errorf("dials = %d closed = %v", dials, conn.closed.Load())
	}

	got, err := os.ReadFile(filepath.Join(e.local, "2026", "268", "soch2680.26o.gz"))
	if err != nil || string(got) != "daily" {
		t.Fatalf("received file = %q, %v", got, err)
	}

	if !strings.Contains(e.logs.String(), "[DOWNLOAD] 종류=RINEX2_DAILY 범위=최근 원격=1 수신=1 소요=") {
		t.Errorf("summary line missing:\n%s", e.logs.String())
	}

	if strings.Contains(e.logs.String(), "RINEX2_HOURLY") {
		t.Errorf("disabled category was reported:\n%s", e.logs.String())
	}

	// lock 이 풀렸다 — 다음 회차가 잡을 수 있다.
	l, err := lock.Acquire(e.cfg.LockFile(), time.Hour)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	_ = l.Release()

	// D12 역방향: 어떤 .db 도 생기지 않았다.
	_ = filepath.WalkDir(e.root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".db") {
			t.Errorf("ledger-like file created: %s", p)
		}
		return nil
	})

	if e.cfg.General.LedgerPath != "" {
		t.Errorf("LedgerPath = %q, want empty for a download-only config", e.cfg.General.LedgerPath)
	}
}

// v3 §10.3: 파일 실패는 보고를 남긴 뒤 부분 실패 종료가 된다.
func TestRunDownload_FileFailureReturnsPartialFailure(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")

	conn := newFakeConn(e.remote)
	conn.fail = errors.New("connection reset")

	err, _ := e.run(t, conn)
	if !errors.Is(err, errDownloadPartial) {
		t.Fatalf("runDownload = %v, want partial failure", err)
	}

	if !strings.Contains(e.logs.String(), " 실패=1 ") {
		t.Errorf("failure not reported:\n%s", e.logs.String())
	}

	if _, err := os.Stat(filepath.Join(e.local, "2026", "268", "soch2680.26o.gz")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed download left a final file: %v", err)
	}

	_ = filepath.WalkDir(e.local, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".part") {
			t.Errorf(".part left after a file failure: %s", p)
		}

		return nil
	})
}

// 원격 나열 실패는 후보가 0이어도 정상적인 대상 없음이 아니다. 스케줄러가
// 미수신 장애를 성공으로 오인하지 않도록 부분 실패로 끝낸다.
func TestRunDownload_ScanFailureReturnsPartialFailure(t *testing.T) {
	e := newDLEnv(t)
	conn := newFakeConn(e.remote)
	conn.readDirFail = errors.New("permission denied")

	err, _ := e.run(t, conn)
	if !errors.Is(err, errDownloadPartial) {
		t.Fatalf("runDownload = %v, want partial failure", err)
	}
	if !strings.Contains(e.logs.String(), "나열실패=1") ||
		!strings.Contains(e.logs.String(), "permission denied") {
		t.Errorf("scan failure report missing:\n%s", e.logs.String())
	}
}

// ── D10 stall ──────────────────────────────────────────────────────

// 수신 도중 원격이 멎으면 watchdog 이 발화해 cancelRun → Abort 순서로
// 회차를 끝내고, 회차는 성공으로 위장하지 않는다.
func TestRunDownload_D10_StallCancelsThenAborts(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")

	// 설정 하한(5초)을 테스트에서만 줄인다. 감시 주기는 1초다.
	e.cfg.Download.SFTP.StallTimeout = 50 * time.Millisecond

	conn := newFakeConn(e.remote)
	conn.block = true

	done := make(chan error, 1)
	go func() {
		err, _ := e.run(t, conn)
		done <- err
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("stalled run did not end (process would hang holding the lock)")
	}

	if err == nil || !strings.Contains(err.Error(), "stall") {
		t.Fatalf("err = %v, want a stall error (never report a stalled run as success)", err)
	}

	conn.mu.Lock()
	ordered := conn.canceledBeforeAbort
	conn.mu.Unlock()

	if !ordered {
		t.Error("Abort ran before cancelRun (new work could start on a dead session)")
	}

	if !strings.Contains(e.logs.String(), "[STALL] 원격 무진행") {
		t.Errorf("stall log missing:\n%s", e.logs.String())
	}

	// 취소된 회차도 집계는 남긴다.
	if !strings.Contains(e.logs.String(), "[DOWNLOAD] 종류=RINEX2_DAILY") {
		t.Errorf("partial report missing:\n%s", e.logs.String())
	}

	// .part 가 남지 않았다 (러너의 취소 후 정리).
	_ = filepath.WalkDir(e.local, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".part") {
			t.Errorf(".part left: %s", p)
		}
		return nil
	})
}

// ── lock ───────────────────────────────────────────────────────────

// 앞 회차가 lock 을 쥐고 있으면 접속하지 않고 정상 종료한다.
func TestRunDownload_LockHeldSkipsWithoutConnecting(t *testing.T) {
	e := newDLEnv(t)

	held, err := lock.Acquire(e.cfg.LockFile(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Release() }()

	err, dials := e.run(t, newFakeConn(e.remote))
	if err != nil {
		t.Fatalf("runDownload = %v, want nil (overlap is not a failure)", err)
	}

	if dials != 0 {
		t.Errorf("dials = %d, want 0 (lock is taken before connecting)", dials)
	}
}

// 접속 실패는 회차 실패(1)이고 lock 은 풀린다.
func TestRunDownload_DialFailureReleasesLock(t *testing.T) {
	e := newDLEnv(t)

	boom := errors.New("ssh: handshake failed")

	err := runDownload(
		context.Background(), e.cfg, false, dlNow,
		func(transport.SFTPDialOptions) (downloadConn, error) { return nil, boom },
		log.New(e.logs, "", 0), nil,
	)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}

	l, err := lock.Acquire(e.cfg.LockFile(), time.Hour)
	if err != nil {
		t.Fatalf("lock not released after dial failure: %v", err)
	}
	_ = l.Release()
}

// ── 금지 조합 · 범위 · resend ──────────────────────────────────────

func TestCheckDownloadFlags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		dry, seed bool
		transport string
		wantErr   string
	}{
		{"기본", false, false, "", ""},
		{"--transport=sftp", false, false, " SFTP ", ""},
		{"--seed 거부", false, true, "", "--seed"},
		{"--dry-run 거부", true, false, "", "--dry-run"},
		{"--transport=localfs 거부", false, false, "localfs", "--transport=localfs"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkDownloadFlags(tc.dry, tc.seed, tc.transport)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// Hot/Deep 판정과 로그 문구는 PUT 과 같다.
func TestDownloadRange(t *testing.T) {
	e := newDLEnv(t)

	var buf bytes.Buffer
	l := log.New(&buf, "", 0)

	rng, name := downloadRange(e.cfg, false, dlNow, l)
	if name != "hot" || !rng.From.Equal(dlNow) || buf.Len() != 0 {
		t.Errorf("hot: %v %v log=%q", rng, name, buf.String())
	}

	rng, name = downloadRange(e.cfg, true, dlNow, l)
	if name != "deep" || !rng.From.Equal(dlNow.AddDate(0, 0, -6)) ||
		!strings.Contains(buf.String(), "[SCAN] deep run (--deep 수동 강제)") {
		t.Errorf("deep: %v %v log=%q", rng, name, buf.String())
	}

	buf.Reset()
	at4 := time.Date(2026, 9, 25, 4, 0, 0, 0, time.UTC)
	if _, name = downloadRange(e.cfg, false, at4, l); name != "deep" ||
		!strings.Contains(buf.String(), "[SCAN] deep run (DeepScanHour=4 matched)") {
		t.Errorf("auto deep: %v log=%q", name, buf.String())
	}
}

// resend 는 PUT 장부 기능이다. Mode=download 에서는 거부한다.
func TestRequirePutMode(t *testing.T) {
	e := newDLEnv(t)

	if err := requirePutMode(e.cfg); err == nil || !strings.Contains(err.Error(), "resend") {
		t.Fatalf("err = %v", err)
	}
}
