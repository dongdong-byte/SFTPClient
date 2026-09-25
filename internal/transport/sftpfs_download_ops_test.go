package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

type modeInfo struct{ mode fs.FileMode }

func (m modeInfo) Name() string       { return "name" }
func (m modeInfo) Size() int64        { return 0 }
func (m modeInfo) Mode() fs.FileMode  { return m.mode }
func (m modeInfo) ModTime() time.Time { return time.Time{} }
func (m modeInfo) IsDir() bool        { return m.mode.IsDir() }
func (m modeInfo) Sys() any           { return nil }

func TestClassifyReadDirFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "a.gz")
	writeFile(t, file, []byte("x"))

	fileInfo, err := os.Lstat(file)
	if err != nil {
		t.Fatal(err)
	}

	dirInfo, err := os.Lstat(dir)
	if err != nil {
		t.Fatal(err)
	}

	missing := fs.ErrNotExist

	t.Run("파일은 Missing 이 아니다", func(t *testing.T) {
		err := classifyReadDirFailure(file, missing, fileInfo, nil)
		if errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("있는 디렉터리의 목록 실패는 Missing 이 아니다", func(t *testing.T) {
		err := classifyReadDirFailure(dir, missing, dirInfo, nil)
		if errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "exists but listing failed") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("디렉터리를 가리키는 링크도 따라가지 않는다", func(t *testing.T) {
		link := modeInfo{mode: fs.ModeDir | fs.ModeSymlink}
		err := classifyReadDirFailure(dir, missing, link, nil)
		if errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("상태 코드 NO_SUCH_FILE 도 있는 디렉터리면 Missing 이 아니다", func(t *testing.T) {
		listErr := &sftp.StatusError{Code: uint32(sftp.ErrSSHFxNoSuchFile)}
		err := classifyReadDirFailure(dir, listErr, dirInfo, nil)
		if errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "exists but listing failed") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("둘 다 없으면 Missing", func(t *testing.T) {
		err := classifyReadDirFailure(dir, missing, nil, missing)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("err = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("권한 오류는 Missing 이 아니다", func(t *testing.T) {
		denied := errors.New("permission denied")
		err := classifyReadDirFailure(dir, denied, dirInfo, nil)
		if errors.Is(err, fs.ErrNotExist) || !errors.Is(err, denied) {
			t.Fatalf("err = %v", err)
		}
	})
}

// Windows 는 읽기 전용 .part 를 Remove 하지 못한다. 속성을 풀지 않으면
// 그 파일은 다음 회차에도 같은 오류로 남는다.
func TestDownloadPart_ReplacesReadOnlyPart(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	data := []byte("fresh")
	src := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, src, data)

	part := filepath.Join(t.TempDir(), "a.gz.part")
	writeFile(t, part, []byte("stale-readonly"))
	if err := os.Chmod(part, 0o444); err != nil {
		t.Fatal(err)
	}

	if err := s.DownloadPart(context.Background(), remotePathOf(t, src), part); err != nil {
		t.Fatalf("DownloadPart: %v", err)
	}

	got, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("part = %q, want %q", got, data)
	}
}

func TestReadDir_CancelWhileWaitingForResponse(t *testing.T) {
	dir := t.TempDir()
	s, link := newPipeSFTPFS(t)
	link.gate.pause()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := s.ReadDir(ctx, remotePathOf(t, dir))
		done <- err
	}()

	waitInFlight(t, s)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadDir = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ReadDir did not honor context cancellation")
	}
}

type cancelingEOFReader struct {
	cancel func()
	done   bool
}

func (r *cancelingEOFReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	n := copy(p, "last short chunk")
	r.cancel()
	return n, io.EOF
}

func TestCopyDownload_CancelOnFinalShortReadIsNotSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var dst bytes.Buffer
	beats := 0

	err := copyDownload(ctx, &dst, &cancelingEOFReader{cancel: cancel}, func() {
		beats++
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copyDownload = %v, want context.Canceled", err)
	}
	if dst.String() != "last short chunk" || beats != 1 {
		t.Fatalf("dst = %q, beats = %d", dst.String(), beats)
	}
}

// 예측 가능한 .part 이름이 다른 파일의 hard link 여도 그 원본을 자르면
// 안 된다. 기존 O_TRUNC 구현에서 실제 데이터 손실이 나는 회귀 테스트다.
// hard link 는 Windows 에서도 관리자 권한 없이 만들 수 있어 항상 검증된다.
func TestDownloadPart_ReplacesHardLinkedPartWithoutTruncatingTarget(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	remoteData := []byte("downloaded")
	src := filepath.Join(t.TempDir(), "remote.gz")
	writeFile(t, src, remoteData)

	localDir := t.TempDir()
	target := filepath.Join(localDir, "must-survive.txt")
	original := []byte("important local data")
	writeFile(t, target, original)

	part := filepath.Join(localDir, "remote.gz.part")
	if err := os.Link(target, part); err != nil {
		t.Fatalf("create hard link: %v", err)
	}

	if err := s.DownloadPart(context.Background(), remotePathOf(t, src), part); err != nil {
		t.Fatalf("DownloadPart: %v", err)
	}

	gotTarget, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotTarget, original) {
		t.Fatalf("hard-link target was modified: %q, want %q", gotTarget, original)
	}

	gotPart, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPart, remoteData) {
		t.Fatalf("part = %q, want %q", gotPart, remoteData)
	}
}

func TestDownloadPart_RejectsSymlinkPartWithoutTouchingTarget(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	src := filepath.Join(t.TempDir(), "remote.gz")
	writeFile(t, src, []byte("downloaded"))

	localDir := t.TempDir()
	target := filepath.Join(localDir, "must-survive.txt")
	original := []byte("important local data")
	writeFile(t, target, original)

	part := filepath.Join(localDir, "remote.gz.part")
	if err := os.Symlink(target, part); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}

	err := s.DownloadPart(context.Background(), remotePathOf(t, src), part)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("DownloadPart = %v, want non-regular .part rejection", err)
	}

	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("symlink target was modified: %q, want %q", got, original)
	}
}

// DOWNLOAD 커밋 2 리뷰에서 찾은 운영 치명 지점을 고정한다.
//
//	전송 도중 서버·회선 정지 → WatchStall 이 발화해 Abort 로 풀려야 한다.
//	                          못 풀면 lock 을 쥔 채 프로세스가 멈춘다.
//	워커 병렬 수신          → 한 SFTP 세션 위의 동시 DownloadPart 가 서로의
//	                          내용을 섞으면 크기만 맞는 손상 파일이 최종화된다.
//	로컬 목적지 폴더 부재    → 원격 부재와 같은 ErrNotExist 가 나온다. 러너가
//	                          EnsureDir 을 빠뜨려도 원인이 문구로 드러나야 한다.

// D10 의 transport 몫. 두 청크쯤 받은 뒤 서버 응답이 멎는다. 실제 WatchStall
// 을 붙여 발화 → Abort 로 DownloadPart 가 오류로 돌아오는지, 남은 .part 가
// 원격보다 작아 러너의 크기 검증에서 반드시 걸리는지 확인한다.
func TestDownloadPart_StallMidTransferIsAbortedByWatchStall(t *testing.T) {
	s, link := newPipeSFTPFS(t)

	total := 6 * uploadChunk
	src := filepath.Join(t.TempDir(), "big.gz")
	writeFile(t, src, randomBytes(t, total))
	part := filepath.Join(t.TempDir(), "big.gz.part")

	// 전송 도중에 멈춘다 (Open 이 아니라 Read 단계의 정지).
	link.gate.pauseAt(int64(2*uploadChunk) + 1024)

	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()

	fired := make(chan struct{})

	go WatchStall(watchCtx, s, 1500*time.Millisecond, func(int64, time.Duration) {
		close(fired)
		s.Abort()
	})

	done := make(chan error, 1)

	go func() {
		done <- s.DownloadPart(context.Background(), remotePathOf(t, src), part)
	}()

	select {
	case <-link.gate.paused:
	case err := <-done:
		t.Fatalf("DownloadPart returned before the stall: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("gate never paused")
	}

	select {
	case <-fired:
	case <-time.After(10 * time.Second):
		t.Fatal("WatchStall did not fire on a mid-transfer stall")
	}

	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Abort did not wake DownloadPart (process would hang holding the lock)")
	}

	if err == nil {
		t.Fatal("stalled DownloadPart returned nil")
	}

	if n, _ := s.Progress(); n != 0 {
		t.Errorf("inFlight after abort = %d, want 0", n)
	}

	fi, statErr := os.Stat(part)
	if statErr != nil {
		t.Fatalf("partial .part should remain for the runner to clean up: %v", statErr)
	}

	if fi.Size() >= int64(total) {
		t.Fatalf(".part size = %d, must be below remote %d so the size check rejects it",
			fi.Size(), total)
	}
}

// 러너는 MaxWorkers 개 워커로 한 SFTPFS 를 공유한다. 파일마다 크기가 다르고
// 청크 경계도 다르게 잡아, 응답이 섞이면 내용 비교에서 드러나게 한다.
// go test -race 에서 progress·클라이언트 공유의 경쟁도 함께 본다.
func TestDownloadPart_ConcurrentWorkersDoNotMixContent(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	const workers = 8

	srcDir := t.TempDir()
	dstDir := t.TempDir()

	want := make([][]byte, workers)
	for i := range want {
		want[i] = randomBytes(t, uploadChunk/2+i*uploadChunk/3+i*7919)
		writeFile(t, filepath.Join(srcDir, fmt.Sprintf("f%d.gz", i)), want[i])
	}

	var wg sync.WaitGroup
	errs := make([]error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go func(i int) {
			defer wg.Done()

			errs[i] = s.DownloadPart(
				context.Background(),
				remotePathOf(t, filepath.Join(srcDir, fmt.Sprintf("f%d.gz", i))),
				filepath.Join(dstDir, fmt.Sprintf("f%d.gz.part", i)),
			)
		}(i)
	}

	wg.Wait()

	for i := 0; i < workers; i++ {
		if errs[i] != nil {
			t.Errorf("worker %d: %v", i, errs[i])
			continue
		}

		got, err := os.ReadFile(filepath.Join(dstDir, fmt.Sprintf("f%d.gz.part", i)))
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(got, want[i]) {
			t.Errorf("worker %d: content mismatch (%d bytes, want %d)", i, len(got), len(want[i]))
		}
	}

	if n, _ := s.Progress(); n != 0 {
		t.Errorf("inFlight after all workers = %d, want 0", n)
	}
}

// 로컬 목적지 폴더가 없으면 오류는 fs.ErrNotExist 이지만 원격 부재가 아니다.
// 러너가 이 오류로 "원격에서 사라짐"을 판정하면 안 된다는 것을 문구로 고정한다.
func TestDownloadPart_LocalDirMissingIsNotRemoteMissing(t *testing.T) {
	s, _ := newPipeSFTPFS(t)

	src := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, src, []byte("x"))

	part := filepath.Join(t.TempDir(), "2026", "268", "SOCH", "a.gz.part")

	err := s.DownloadPart(context.Background(), remotePathOf(t, src), part)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist from the local side", err)
	}

	if !strings.HasPrefix(err.Error(), "create local .part") {
		t.Errorf("err = %v, want the local cause to be named first", err)
	}

	if strings.Contains(err.Error(), "open remote") {
		t.Errorf("err = %v, must not look like a remote-missing error", err)
	}

	if n, _ := s.Progress(); n != 0 {
		t.Errorf("inFlight = %d, want 0 (remote handle closed)", n)
	}
}

// Abort 뒤 main 의 defer Close 가 드레인을 기다리며 멈추면 lock 을 쥔 채
// 프로세스가 끝나지 않는다. 전송 도중 Abort 한 세션의 Close 는 즉시 끝나야 한다.
func TestClose_AfterAbortDuringDownloadReturnsPromptly(t *testing.T) {
	s, link := newPipeSFTPFS(t)

	src := filepath.Join(t.TempDir(), "a.gz")
	writeFile(t, src, randomBytes(t, 3*uploadChunk))
	part := filepath.Join(t.TempDir(), "a.gz.part")

	link.gate.pauseAt(int64(uploadChunk))

	done := make(chan error, 1)

	go func() {
		done <- s.DownloadPart(context.Background(), remotePathOf(t, src), part)
	}()

	<-link.gate.paused
	s.Abort()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DownloadPart not woken by Abort")
	}

	closed := make(chan error, 1)

	go func() { closed <- s.Close() }()

	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close after Abort = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close after Abort blocked (lock would be held)")
	}
}
