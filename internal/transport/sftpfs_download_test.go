package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// DOWNLOAD 커밋 2 — transport 의 원격 나열·다운로드 (DOWNLOAD_COMMIT_PLAN v1 §3).
//
// 실제 SFTP 프로토콜을 테스트 프로세스 안에서 통과시킨다. vendor 에 있는
// pkg/sftp 의 NewServer 와 NewClientPipe 를 io.Pipe 두 개로 잇는다.
// SSH 계층은 없다 — 키 인증·known_hosts·deadline 은 dialSFTP 공통 경로이며
// 실서버 계약 테스트(SFTPTEST_*)와 현장 D14 가 확인한다.
//
// 테스트 전용 공개 생성자는 두지 않는다 (§3.4). 이 파일은 package transport
// 안에서 비공개 필드(client·conn)를 직접 채운다. conn 에는 실제로 pipe 를
// 끊는 closer 를 넣어, Abort 가 블록된 원격 호출을 깨우는 운영 계약을 그대로
// 시험한다.

// ── 파이프 서버 ──────────────────────────────────────────────────────

// pipeLink 는 in-process 클라이언트·서버 사이의 양방향 파이프다.
//
// Close 는 네 끝을 모두 끊는다. 운영의 ssh.Client.Close 와 같은 효과다:
// 클라이언트의 수신 루프가 오류로 끝나며 대기 중인 모든 요청이 깨어난다.
// 멱등이다.
type pipeLink struct {
	c2sR *io.PipeReader // 서버가 읽는다
	c2sW *io.PipeWriter // 클라이언트가 쓴다
	s2cR *io.PipeReader // 클라이언트가 읽는다
	s2cW *io.PipeWriter // 서버가 쓴다

	gate *gate

	once sync.Once
}

func (p *pipeLink) Close() error {
	p.once.Do(func() {
		p.gate.shutdown()
		_ = p.c2sW.Close()
		_ = p.s2cR.Close()
		_ = p.c2sR.Close()
		_ = p.s2cW.Close()
	})

	return nil
}

// gate 는 서버 → 클라이언트 방향의 응답을 멈췄다 풀 수 있는 밸브다.
//
// 서버가 응답을 못 보내면 클라이언트 호출은 응답을 기다리며 블록된다.
// 운영에서 서버·회선이 멎은 상태(stall)를 재현하는 장치다.
//
// pauseAfter 가 양수이면 그만큼의 바이트를 흘려보낸 뒤 스스로 닫히고
// paused 채널로 알린다. 전송 도중의 특정 지점에서 멈추기 위해 쓴다.
type gate struct {
	mu         sync.Mutex
	cond       *sync.Cond
	open       bool
	closed     bool
	passed     int64
	pauseAfter int64
	paused     chan struct{}
}

func newGate() *gate {
	g := &gate{open: true, paused: make(chan struct{}, 1)}
	g.cond = sync.NewCond(&g.mu)

	return g
}

func (g *gate) pause() {
	g.mu.Lock()
	g.open = false
	g.mu.Unlock()
}

func (g *gate) resume() {
	g.mu.Lock()
	g.open = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

// pauseAt 는 지금부터 n 바이트가 더 흐른 뒤 닫히게 한다.
func (g *gate) pauseAt(n int64) {
	g.mu.Lock()
	g.pauseAfter = g.passed + n
	g.mu.Unlock()
}

func (g *gate) shutdown() {
	g.mu.Lock()
	g.closed = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

// wait 는 게이트가 열릴 때까지 기다린다. 링크가 끊기면 false.
func (g *gate) wait() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	for !g.open && !g.closed {
		g.cond.Wait()
	}

	return !g.closed
}

func (g *gate) account(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.passed += int64(n)

	if g.pauseAfter > 0 && g.passed >= g.pauseAfter {
		g.pauseAfter = 0
		g.open = false

		select {
		case g.paused <- struct{}{}:
		default:
		}
	}
}

// gatedWriter 는 서버의 응답 쓰기를 gate 로 통제한다.
type gatedWriter struct {
	w io.WriteCloser
	g *gate
}

func (gw gatedWriter) Write(p []byte) (int, error) {
	if !gw.g.wait() {
		return 0, io.ErrClosedPipe
	}

	n, err := gw.w.Write(p)
	gw.g.account(n)

	return n, err
}

func (gw gatedWriter) Close() error { return gw.w.Close() }

// serverRWC 는 sftp.NewServer 가 요구하는 ReadWriteCloser 다.
type serverRWC struct {
	io.Reader
	io.WriteCloser
}

// newPipeSFTPFS 는 in-process SFTP 서버와 연결된 SFTPFS 를 만든다.
// 서버는 테스트 프로세스의 실제 파일시스템을 서비스한다.
func newPipeSFTPFS(t *testing.T) (*SFTPFS, *pipeLink) {
	t.Helper()

	link := &pipeLink{gate: newGate()}
	link.c2sR, link.c2sW = io.Pipe()
	link.s2cR, link.s2cW = io.Pipe()

	srv, err := sftp.NewServer(serverRWC{
		Reader:      link.c2sR,
		WriteCloser: gatedWriter{w: link.s2cW, g: link.gate},
	})
	if err != nil {
		t.Fatalf("sftp.NewServer: %v", err)
	}

	served := make(chan struct{})

	go func() {
		defer close(served)
		_ = srv.Serve()

		// Serve 는 클라이언트 쪽 EOF 를 받으면 끝나지만 자기 쓰기 쪽을
		// 닫지 않는다. 운영에서는 SSH 채널 종료가 그 역할을 하므로,
		// 여기서 서버→클라이언트 파이프를 닫아 같은 효과를 낸다. 이것이
		// 없으면 sftp.Client.Close 가 수신 루프 종료를 영원히 기다린다.
		_ = link.s2cW.Close()
	}()

	client, err := sftp.NewClientPipe(link.s2cR, link.c2sW)
	if err != nil {
		_ = link.Close()
		t.Fatalf("sftp.NewClientPipe: %v", err)
	}

	s := &SFTPFS{client: client, conn: link}

	t.Cleanup(func() {
		// 정상 종료 경로(Close)가 테스트마다 실제로 끝나는지도 본다.
		// Abort 된 경우 Close 는 드레인을 건너뛴다.
		done := make(chan struct{})

		go func() {
			_ = s.Close()
			_ = link.Close()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("SFTPFS.Close did not return within 5s")
			_ = link.Close()
		}

		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("in-process sftp server did not stop within 5s")
		}
	})

	return s, link
}

// remotePathOf 는 로컬 절대경로를 in-process 서버가 받는 SFTP 경로로 바꾼다.
//
//	Linux    /tmp/x/a.gz          → /tmp/x/a.gz
//	Windows  C:\Users\...\a.gz    → /C:/Users/.../a.gz
//
// pkg/sftp 서버는 Windows 에서 "/C:/..." 를 "C:\..." 로 되돌린다
// (server_windows.go toLocalPath). 이 매핑이 성립하지 않으면 아래 모든
// 테스트가 엉뚱한 경로를 보므로 TestPipeSFTP_PathMapping 이 먼저 고정한다.
func remotePathOf(t *testing.T, local string) string {
	t.Helper()

	abs, err := filepath.Abs(local)
	if err != nil {
		t.Fatal(err)
	}

	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	return p
}

func randomBytes(t *testing.T, n int) []byte {
	t.Helper()

	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}

	return b
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitInFlight 는 원격 호출이 실제로 진행 중(응답 대기)이 될 때까지 기다린다.
func waitInFlight(t *testing.T, s *SFTPFS) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)

	for time.Now().Before(deadline) {
		if n, _ := s.Progress(); n > 0 {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("remote call never became in-flight")
}

// ── §3.4 경로 매핑 (커밋 초반에 먼저 확인) ────────────────────────────

func TestPipeSFTP_PathMapping(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	dir := t.TempDir()
	local := filepath.Join(dir, "mapping.txt")
	writeFile(t, local, []byte("hello"))

	fi, err := s.client.Stat(remotePathOf(t, local))
	if err != nil {
		t.Fatalf("server cannot see %q as %q: %v", local, remotePathOf(t, local), err)
	}

	if fi.Size() != 5 {
		t.Fatalf("size = %d, want 5", fi.Size())
	}
}

// ── §3.2 ReadDir ────────────────────────────────────────────────────

func TestReadDir_KindsArePreserved(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dbon0010.26o.gz"), []byte("abc"))

	if err := os.Mkdir(filepath.Join(dir, "13"), 0o755); err != nil {
		t.Fatal(err)
	}

	// 디렉터리를 가리키는 링크. 따라가서 디렉터리로 보면 안 된다.
	haveLink := true
	if err := os.Symlink(filepath.Join(dir, "13"), filepath.Join(dir, "link13")); err != nil {
		// Windows 에서 개발자 모드·관리자 권한이 없으면 링크를 만들 수 없다.
		t.Logf("symlink unavailable, link case skipped: %v", err)
		haveLink = false
	}

	entries, err := s.ReadDir(context.Background(), remotePathOf(t, dir))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}

	got := make(map[string]fs.FileInfo, len(entries))
	for _, e := range entries {
		got[e.Name()] = e
	}

	file, ok := got["dbon0010.26o.gz"]
	if !ok || !file.Mode().IsRegular() || file.Size() != 3 {
		t.Errorf("regular file entry = %+v", file)
	}

	sub, ok := got["13"]
	if !ok || !sub.IsDir() {
		t.Errorf("directory entry = %+v", sub)
	}

	if haveLink {
		link, ok := got["link13"]
		if !ok {
			t.Fatal("link entry missing")
		}

		if link.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("link mode = %v, want ModeSymlink bit", link.Mode())
		}

		if link.IsDir() {
			t.Error("link to a directory must not be reported as a directory")
		}
	}

	for _, dot := range []string{".", ".."} {
		if _, ok := got[dot]; ok {
			t.Errorf("entry %q must not be returned", dot)
		}
	}

	if n, _ := s.Progress(); n != 0 {
		t.Errorf("inFlight after ReadDir = %d, want 0", n)
	}
}

func TestReadDir_MissingIsErrNotExist(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	missing := filepath.Join(t.TempDir(), "no", "such", "doy")

	_, err := s.ReadDir(context.Background(), remotePathOf(t, missing))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

// 파일을 디렉터리처럼 나열하면 서버 응답 코드와 무관하게 명시적 오류다.
// Windows in-process 서버처럼 NO_SUCH_FILE 을 주는 구현도 Lstat 재확인으로
// 비디렉터리임을 밝혀 Scanner 의 Missing 으로 조용히 넘어가지 않게 한다.
func TestReadDir_FileAsDirectoryFails(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	file := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, file, []byte("x"))

	_, err := s.ReadDir(context.Background(), remotePathOf(t, file))
	if err == nil {
		t.Fatal("ReadDir on a file returned nil error")
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadDir on a file = %v, must not be classified as Missing", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("ReadDir on a file = %v, want explicit kind error", err)
	}
}

// TestWrapNotExist_StatusCodes 는 서버 상태 코드 → fs.ErrNotExist 분류를
// OS·서버와 무관하게 고정한다. transport 의 계약은 "NO_SUCH_FILE 만 없음,
// 권한·일반 실패는 없음이 아니다"이다.
func TestWrapNotExist_StatusCodes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		code uint32
		want bool
	}{
		{"NO_SUCH_FILE → 없음", uint32(sftp.ErrSSHFxNoSuchFile), true},
		{"PERMISSION_DENIED → 없음 아님", uint32(sftp.ErrSSHFxPermissionDenied), false},
		{"FAILURE → 없음 아님", uint32(sftp.ErrSSHFxFailure), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := wrapNotExist("read dir", &sftp.StatusError{Code: tc.code})

			if got := errors.Is(err, fs.ErrNotExist); got != tc.want {
				t.Fatalf("errors.Is(%v, fs.ErrNotExist) = %v, want %v", err, got, tc.want)
			}

			// 원문(서버 상태)은 체인에 남는다.
			var se *sftp.StatusError
			if !errors.As(err, &se) || se.Code != tc.code {
				t.Errorf("status error lost from chain: %v", err)
			}
		})
	}
}

func TestReadDir_CanceledBeforeCall(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.ReadDir(ctx, remotePathOf(t, t.TempDir()))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	// 원격 호출이 시작되지 않았다.
	if n, idle := s.Progress(); n != 0 || idle != 0 {
		t.Errorf("Progress = (%d, %v), want untouched (0, 0)", n, idle)
	}
}

// ── §3.3 DownloadPart ───────────────────────────────────────────────

func TestDownloadPart_ReceivesWholeFile(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	// 청크(1 MiB) 경계에 맞지 않는 크기로 여러 청크를 지나게 한다.
	data := randomBytes(t, 2*uploadChunk+12345)

	src := filepath.Join(t.TempDir(), "SOCH00KOR_R_20262680000_01H_30S_MO.rnx.gz")
	writeFile(t, src, data)

	part := filepath.Join(t.TempDir(), "SOCH.rnx.gz.part")

	if err := s.DownloadPart(context.Background(), remotePathOf(t, src), part); err != nil {
		t.Fatalf("DownloadPart: %v", err)
	}

	got, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, data) {
		t.Fatalf("downloaded %d bytes, content mismatch (want %d)", len(got), len(data))
	}

	n, idle := s.Progress()
	if n != 0 {
		t.Errorf("inFlight after DownloadPart = %d, want 0", n)
	}

	if idle <= 0 || idle > 5*time.Second {
		t.Errorf("idle = %v, want recent progress", idle)
	}
}

func TestDownloadPart_EmptyRemoteFile(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	src := filepath.Join(t.TempDir(), "empty.gz")
	writeFile(t, src, nil)

	part := filepath.Join(t.TempDir(), "empty.gz.part")

	// 0바이트 판정은 download 러너(verify) 몫이다. transport 는 받기만 한다.
	if err := s.DownloadPart(context.Background(), remotePathOf(t, src), part); err != nil {
		t.Fatalf("DownloadPart: %v", err)
	}

	fi, err := os.Stat(part)
	if err != nil || fi.Size() != 0 {
		t.Fatalf("part = %+v, err = %v, want empty file", fi, err)
	}
}

// D9 의 transport 몫: 이전 회차가 남긴 더 긴 .part 는 처음부터 덮어쓴다.
func TestDownloadPart_ReplacesLeftoverPart(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	data := []byte("new content")
	src := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, src, data)

	part := filepath.Join(t.TempDir(), "a.gz.part")
	writeFile(t, part, bytes.Repeat([]byte("OLD-LEFTOVER-"), 1000))

	if err := s.DownloadPart(context.Background(), remotePathOf(t, src), part); err != nil {
		t.Fatalf("DownloadPart: %v", err)
	}

	got, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, data) {
		t.Fatalf("part = %q, want %q (leftover not truncated)", got, data)
	}
}

// 원격 부재는 fs.ErrNotExist 이고, 로컬에 빈 .part 를 남기지 않는다.
func TestDownloadPart_MissingRemote(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	missing := filepath.Join(t.TempDir(), "gone.gz")
	part := filepath.Join(t.TempDir(), "gone.gz.part")

	err := s.DownloadPart(context.Background(), remotePathOf(t, missing), part)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}

	if !strings.Contains(err.Error(), "open remote") {
		t.Errorf("err = %v, want \"open remote\" prefix", err)
	}

	if _, statErr := os.Stat(part); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("local .part must not be created when the remote is missing: %v", statErr)
	}
}

// 로컬 .part 를 만들 수 없으면 오류이며, 원격 쪽 원인과 문구로 구분된다.
func TestDownloadPart_LocalCreateFails(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	src := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, src, []byte("x"))

	// .part 자리에 디렉터리가 있다.
	partDir := filepath.Join(t.TempDir(), "a.gz.part")
	if err := os.Mkdir(partDir, 0o755); err != nil {
		t.Fatal(err)
	}

	err := s.DownloadPart(context.Background(), remotePathOf(t, src), partDir)
	if err == nil {
		t.Fatal("DownloadPart into a directory returned nil")
	}

	if !strings.Contains(err.Error(), "create local .part") {
		t.Errorf("err = %v, want \"create local .part\" prefix", err)
	}

	if n, _ := s.Progress(); n != 0 {
		t.Errorf("inFlight = %d after failure, want 0 (remote handle closed)", n)
	}
}

func TestDownloadPart_CanceledBeforeCall(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	src := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, src, []byte("x"))

	part := filepath.Join(t.TempDir(), "a.gz.part")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.DownloadPart(ctx, remotePathOf(t, src), part)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if _, statErr := os.Stat(part); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("no .part should be created for a pre-canceled call: %v", statErr)
	}
}

// §3.5 "큰 파일 전송 중 ctx 취소가 청크 경계에서 반영된다".
//
// 서버 응답을 첫 청크 반쯤 지난 지점에서 멈춰, 두 번째 청크를 읽는 도중에
// ctx 를 취소한다. 진행 중이던 청크는 끝까지 기록되고, 다음 청크 경계에서
// 취소가 반영되어 나머지를 받지 않는다.
func TestDownloadPart_CancelAtChunkBoundary(t *testing.T) {
	s, link := newPipeSFTPFS(t)

	total := 6 * uploadChunk
	data := randomBytes(t, total)

	src := filepath.Join(t.TempDir(), "big.gz")
	writeFile(t, src, data)

	part := filepath.Join(t.TempDir(), "big.gz.part")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	link.gate.pauseAt(int64(uploadChunk) + uploadChunk/2)

	done := make(chan error, 1)

	go func() {
		done <- s.DownloadPart(ctx, remotePathOf(t, src), part)
	}()

	select {
	case <-link.gate.paused:
	case err := <-done:
		t.Fatalf("DownloadPart returned before the gate paused: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("gate never paused")
	}

	cancel()
	link.gate.resume()

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("DownloadPart did not return after cancel")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	fi, statErr := os.Stat(part)
	if statErr != nil {
		t.Fatal(statErr)
	}

	// 경계에서 멈췄다: 청크 단위로 기록됐고 전체보다 적다.
	if fi.Size() == 0 || fi.Size() >= int64(total) {
		t.Fatalf(".part size = %d, want a partial size below %d", fi.Size(), total)
	}

	if fi.Size()%int64(uploadChunk) != 0 {
		t.Errorf(".part size = %d, want a whole number of chunks", fi.Size())
	}

	got, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, data[:len(got)]) {
		t.Error("partial content does not match the remote prefix")
	}
}

// §3.5 "테스트 pipe 종료가 블록된 원격 호출을 실제로 해제한다".
//
// 서버 응답을 멈춘 상태에서 호출하면 호출은 응답을 기다리며 블록된다.
// Abort 가 연결을 끊으면 호출이 오류로 돌아와야 한다. 이 경로가 운영의
// WatchStall → cancelRun → Abort 계약의 transport 몫이다.
func TestAbort_WakesBlockedRemoteCalls(t *testing.T) {
	cases := []struct {
		name string
		call func(s *SFTPFS, remote, part string) error
	}{
		{
			name: "ReadDir",
			call: func(s *SFTPFS, remote, _ string) error {
				_, err := s.ReadDir(context.Background(), filepath.ToSlash(filepath.Dir(remote)))
				return err
			},
		},
		{
			name: "DownloadPart",
			call: func(s *SFTPFS, remote, part string) error {
				return s.DownloadPart(context.Background(), remote, part)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, link := newPipeSFTPFS(t)

			src := filepath.Join(t.TempDir(), "a.gz")
			writeFile(t, src, randomBytes(t, 4096))
			part := filepath.Join(t.TempDir(), "a.gz.part")

			link.gate.pause()

			done := make(chan error, 1)

			go func() {
				done <- tc.call(s, remotePathOf(t, src), part)
			}()

			waitInFlight(t, s)

			select {
			case err := <-done:
				t.Fatalf("call returned while the server was stalled: %v", err)
			case <-time.After(100 * time.Millisecond):
			}

			s.Abort()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("aborted call returned nil error")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Abort did not wake the blocked remote call")
			}

			if n, _ := s.Progress(); n != 0 {
				t.Errorf("inFlight after abort = %d, want 0", n)
			}
		})
	}
}

// Abort 이후의 새 호출은 즉시 실패한다 (죽은 세션에 새 착수가 매달리지 않는다).
func TestAbort_LaterCallsFailFast(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	s.Abort()
	s.Abort() // 멱등

	done := make(chan error, 1)

	go func() {
		_, err := s.ReadDir(context.Background(), remotePathOf(t, t.TempDir()))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("ReadDir after Abort returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadDir after Abort blocked")
	}
}

// ── 순수 helper ─────────────────────────────────────────────────────

type fakeWriter struct {
	max   int // 한 번에 받는 최대 바이트, 0 이면 (0, nil) 을 돌려준다
	err   error
	calls int
	buf   bytes.Buffer
}

func (w *fakeWriter) Write(p []byte) (int, error) {
	w.calls++

	if w.err != nil {
		return 0, w.err
	}

	if w.max == 0 {
		return 0, nil
	}

	n := min(len(p), w.max)
	w.buf.Write(p[:n])

	return n, nil
}

func TestWriteFull(t *testing.T) {
	t.Parallel()

	t.Run("short write 를 이어 쓴다", func(t *testing.T) {
		w := &fakeWriter{max: 3}
		data := []byte("0123456789")

		if err := writeFull(w, data); err != nil {
			t.Fatal(err)
		}

		if w.buf.String() != string(data) || w.calls != 4 {
			t.Fatalf("buf = %q calls = %d", w.buf.String(), w.calls)
		}
	})

	t.Run("(0, nil) 은 무한 루프 대신 ErrShortWrite", func(t *testing.T) {
		if err := writeFull(&fakeWriter{max: 0}, []byte("x")); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("err = %v, want io.ErrShortWrite", err)
		}
	})

	t.Run("Write 오류는 그대로", func(t *testing.T) {
		boom := errors.New("disk full")

		if err := writeFull(&fakeWriter{err: boom}, []byte("x")); !errors.Is(err, boom) {
			t.Fatalf("err = %v, want disk full", err)
		}
	})
}

type fakeCloser struct{ err error }

func (c fakeCloser) Close() error { return c.err }

func TestCloseInto(t *testing.T) {
	t.Parallel()

	primary := errors.New("read remote: connection lost")
	closeErr := errors.New("flush failed")

	t.Run("주 오류 없음 + Close 오류 → Close 오류가 결과", func(t *testing.T) {
		var err error
		closeInto(&err, fakeCloser{closeErr}, "close local .part")

		if !errors.Is(err, closeErr) || !strings.HasPrefix(err.Error(), "close local .part:") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("주 오류 + Close 오류 → 둘 다 보존, 주 오류가 앞", func(t *testing.T) {
		err := primary
		closeInto(&err, fakeCloser{closeErr}, "close local .part")

		if !errors.Is(err, primary) || !errors.Is(err, closeErr) {
			t.Fatalf("err = %v, want both", err)
		}

		if !strings.HasPrefix(err.Error(), primary.Error()) {
			t.Errorf("primary cause must come first: %v", err)
		}
	})

	t.Run("Close 성공 → 불변", func(t *testing.T) {
		err := primary
		closeInto(&err, fakeCloser{}, "close")

		if err != primary {
			t.Fatalf("err = %v, want primary unchanged", err)
		}

		var none error
		closeInto(&none, fakeCloser{}, "close")

		if none != nil {
			t.Fatalf("err = %v, want nil", none)
		}
	})
}

// ── §3.1 읽기 전용 접속 ─────────────────────────────────────────────

// DialSFTPReadOnly 는 DialSFTP 와 같은 접속 경로(dialSFTP)를 거친다.
// 키·known_hosts·TCP 단계의 실패가 같은 방식으로 드러나는지 고정한다.
//
// posix-rename 검사를 "건너뛴다"는 사실 자체는 in-process 로 시험하지 못한다.
// vendor 의 pkg/sftp 서버는 이 확장을 항상 광고하므로(sftp.go
// supportedSFTPExtensions) 검사가 있어도 통과한다. 실서버에서의 확인은
// 커밋 7·8 의 D14 로 남긴다.
func TestDialSFTPReadOnly_SharesDialPipeline(t *testing.T) {
	t.Parallel()

	t.Run("개인키 없음", func(t *testing.T) {
		_, err := DialSFTPReadOnly(SFTPDialOptions{
			Host:           "127.0.0.1",
			Port:           22,
			User:           "u",
			PrivateKeyPath: filepath.Join(t.TempDir(), "no-such-key"),
			KnownHostsPath: "unused",
		})
		if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "read private key") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("known_hosts 없음", func(t *testing.T) {
		_, err := DialSFTPReadOnly(SFTPDialOptions{
			Host:           "127.0.0.1",
			Port:           22,
			User:           "u",
			PrivateKeyPath: writeTestKey(t),
			KnownHostsPath: filepath.Join(t.TempDir(), "no-such-known-hosts"),
		})
		if err == nil || !strings.Contains(err.Error(), "known_hosts") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("접속 거부", func(t *testing.T) {
		kh := filepath.Join(t.TempDir(), "known_hosts")
		writeFile(t, kh, nil)

		port := closedLocalPort(t)

		_, err := DialSFTPReadOnly(SFTPDialOptions{
			Host:           "127.0.0.1",
			Port:           port,
			User:           "u",
			PrivateKeyPath: writeTestKey(t),
			KnownHostsPath: kh,
		})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("dial 127.0.0.1:%d", port)) {
			t.Fatalf("err = %v", err)
		}
	})
}

// closedLocalPort 는 방금 닫은 루프백 포트를 돌려준다. 즉시 RST 가 오므로
// dialTimeout 을 기다리지 않는다 (TestDialSFTPConnectionRefused 와 같은 방식).
func closedLocalPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	return port
}
