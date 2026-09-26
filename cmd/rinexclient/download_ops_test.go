package main

import (
	"context"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/lock"
	"SFTPClient/internal/transport"
)

// DOWNLOAD 커밋 7 리뷰에서 찾은 운영 치명 지점을 고정한다.
//
//	stall 뒤 lock 미해제  → 다음 정시부터 LockStaleSeconds(3시간) 동안 매 회차
//	                        ErrHeld 로 조용히 exit 0 — 수신이 멈춘 줄 모른다
//	Ctrl+C 중단           → 같은 이유로 lock·접속·.part 가 정리되어야 한다
//	파일 전용 줄 누수     → 기관 구조 원인(종류미상 경로 등)이 고객 화면에 뜬다

// assertLockFree 는 다음 회차가 lock 을 잡을 수 있는지 본다.
func assertLockFree(t *testing.T, e *dlEnv) {
	t.Helper()

	l, err := lock.Acquire(e.cfg.LockFile(), time.Hour)
	if err != nil {
		t.Fatalf("lock not released — the next scheduled run would silently skip: %v", err)
	}

	_ = l.Release()
}

func assertNoPartLeft(t *testing.T, e *dlEnv) {
	t.Helper()

	_ = filepath.WalkDir(e.local, func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".part") {
			t.Errorf(".part left: %s", p)
		}
		return nil
	})
}

// 접속 옵션은 [DOWNLOAD.SFTP] 다. PUT 쪽 호스트로 이어지면 다른 서버를 읽는다.
func TestRunDownload_DialsDownloadEndpoint(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")

	var got transport.SFTPDialOptions

	err := runDownload(
		context.Background(), e.cfg, false, dlNow,
		func(o transport.SFTPDialOptions) (downloadConn, error) {
			got = o
			return newFakeConn(e.remote), nil
		},
		log.New(e.logs, "", 0), log.New(e.detail, "", 0),
	)
	if err != nil {
		t.Fatal(err)
	}

	if got.Host != "10.0.0.9" || got.Port != 22 || got.User != "getter" {
		t.Fatalf("dial = %+v, want the DOWNLOAD endpoint", got)
	}

	if !strings.HasSuffix(filepath.ToSlash(got.PrivateKeyPath), "keys/id_ed25519") ||
		!strings.HasSuffix(filepath.ToSlash(got.KnownHostsPath), "keys/known_hosts") {
		t.Fatalf("dial paths = key %q hosts %q", got.PrivateKeyPath, got.KnownHostsPath)
	}
}

// stall 로 끝난 회차도 lock 을 풀고 접속을 닫는다.
func TestRunDownload_StallReleasesLockAndClosesConn(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")
	e.cfg.Download.SFTP.StallTimeout = 50 * time.Millisecond

	conn := newFakeConn(e.remote)
	conn.block = true

	done := make(chan error, 1)
	go func() {
		err, _ := e.run(t, conn)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled run returned nil")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("stalled run did not end")
	}

	if !conn.closed.Load() {
		t.Error("connection not closed after stall")
	}

	assertLockFree(t, e)
	assertNoPartLeft(t, e)
}

// 운영자가 Ctrl+C 로 끊으면 회차는 오류로 끝나고(성공 위장 없음) lock·접속·
// .part 가 정리된다. 다음 정시가 정상으로 이어받는다.
func TestRunDownload_InterruptCleansUp(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")

	conn := newFakeConn(e.remote)
	conn.block = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runDownload(ctx, e.cfg, false, dlNow,
			func(transport.SFTPDialOptions) (downloadConn, error) { return conn, nil },
			log.New(e.logs, "", 0), log.New(e.detail, "", 0))
	}()

	// 수신이 실제로 시작된 뒤 끊는다.
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn.mu.Lock()
		started := conn.calls > 0
		conn.mu.Unlock()

		if started {
			break
		}

		if time.Now().After(deadline) {
			t.Fatal("download never started")
		}

		time.Sleep(5 * time.Millisecond)
	}

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("interrupted run returned nil (must not look successful)")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("interrupted run did not end")
	}

	if !conn.closed.Load() {
		t.Error("connection not closed after interrupt")
	}

	assertLockFree(t, e)
	assertNoPartLeft(t, e)

	// 다음 회차는 정상으로 받는다.
	e.logs.Reset()
	if err, _ := e.run(t, newFakeConn(e.remote)); err != nil {
		t.Fatalf("next run: %v", err)
	}

	if !strings.Contains(e.logs.String(), "수신=1") {
		t.Errorf("next run did not receive:\n%s", e.logs.String())
	}
}

// main 배선이 DetailLogger 를 러너와 Print 양쪽에 넘긴다. 파일 전용 줄(종류미상
// 경로)은 화면 로거에 나오지 않고, 요약의 건수만 화면에 나온다.
func TestRunDownload_FileOnlyLinesStayOffScreen(t *testing.T) {
	e := newDLEnv(t)
	e.putRemote(t, "RNX/2026/268/soch2680.26o.gz", "daily")
	e.putRemote(t, "RNX/2026/268/readme.txt", "junk")

	if err, _ := e.run(t, newFakeConn(e.remote)); err != nil {
		t.Fatal(err)
	}

	console, detail := e.logs.String(), e.detail.String()

	if !strings.Contains(console, "종류미상=1") {
		t.Errorf("summary count missing on screen:\n%s", console)
	}

	if strings.Contains(console, "readme.txt") {
		t.Errorf("file-only line reached the screen:\n%s", console)
	}

	if !strings.Contains(detail, "[DOWNLOAD] 종류미상 원격=/RNX/2026/268/readme.txt") {
		t.Errorf("file-only line missing from the detail log:\n%s", detail)
	}
}
