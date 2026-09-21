package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"SFTPClient/internal/put"
)

// 생산 코드는 put 을 import 하지 않는다.
// 구조적 만족은 테스트에서만 고정한다 (localfs_test.go 와 동일 방식).
var _ put.Uploader = (*SFTPFS)(nil)

// ============================================================
// 오프라인 단위 테스트 — 서버 없이 항상 실행된다
// ============================================================

func TestSFTPFSJoinUsesSlash(t *testing.T) {
	t.Parallel()

	got := (*SFTPFS)(nil).Join("a/b", "c.rnx")
	if got != "a/b/c.rnx" {
		t.Fatalf("Join() = %q, want slash-separated path", got)
	}
}

// Abort 이후 Close 는 nil client/conn 을 건드리지 않는다.
// 스톨 경로에서 sftp.Client.Close 드레인을 타면 종료가 다시 멈춘다.
func TestCloseAfterAbortSkipsSFTPDrain(t *testing.T) {
	t.Parallel()

	s := &SFTPFS{}
	s.aborted.Store(true)

	if err := s.Close(); err != nil {
		t.Fatalf("Close after Abort = %v, want nil", err)
	}
}

func TestIgnoreBenignClose(t *testing.T) {
	t.Parallel()

	realErr := errors.New("broken pipe 같은 실제 오류")

	cases := []struct {
		name string
		in   error
		want error
	}{
		{"nil 은 nil", nil, nil},
		{"EOF 는 정상 종료로 흡수", io.EOF, nil},
		{"wrap 된 EOF 도 흡수", fmt.Errorf("close: %w", io.EOF), nil},
		{"ErrClosed 도 흡수", net.ErrClosed, nil},
		{"실제 오류는 보존", realErr, realErr},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ignoreBenignClose(tc.in); !errors.Is(got, tc.want) {
				t.Fatalf("ignoreBenignClose(%v) = %v, want %v",
					tc.in, got, tc.want)
			}
		})
	}
}

func TestWrapNotExistNormalizes(t *testing.T) {
	t.Parallel()

	// "없음" 계열: fs.ErrNotExist 판별이 성립해야 하고
	// 원문 텍스트도 보존되어야 한다 (Uploader 계약: 원문을 버리지 않는다).
	src := fmt.Errorf("server said: %w", fs.ErrNotExist)

	got := wrapNotExist("stat", src)
	if !errors.Is(got, fs.ErrNotExist) {
		t.Fatalf("errors.Is(got, fs.ErrNotExist) = false, want true: %v", got)
	}

	// "없음" 이 아닌 오류: fs.ErrNotExist 가 생기면 안 된다.
	// 권한 오류를 "전송 안 됨" 으로 오판하는 것이 바로 계약이 막는 사고다.
	other := errors.New("permission denied")

	got = wrapNotExist("stat", other)
	if errors.Is(got, fs.ErrNotExist) {
		t.Fatalf("권한 오류가 fs.ErrNotExist 로 오판된다: %v", got)
	}
}

func TestDialSFTPMissingPrivateKey(t *testing.T) {
	t.Parallel()

	_, err := DialSFTP(SFTPDialOptions{
		Host:           "127.0.0.1",
		Port:           22,
		User:           "u",
		PrivateKeyPath: filepath.Join(t.TempDir(), "no-such-key"),
		KnownHostsPath: "unused",
	})
	if err == nil {
		t.Fatal("존재하지 않는 개인키인데 err = nil")
	}

	// 키를 읽기 전에는 네트워크에 나가지 않는다 —
	// 이 테스트가 통과한다는 것 자체가 그 순서의 증거다.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist 체인", err)
	}
}

func TestDialSFTPMissingKnownHosts(t *testing.T) {
	t.Parallel()

	keyPath := writeTestKey(t)

	_, err := DialSFTP(SFTPDialOptions{
		Host:           "127.0.0.1",
		Port:           22,
		User:           "u",
		PrivateKeyPath: keyPath,
		KnownHostsPath: filepath.Join(t.TempDir(), "no-such-known-hosts"),
	})
	if err == nil {
		t.Fatal("존재하지 않는 known_hosts 인데 err = nil")
	}

	// 첫 배포에서 가장 흔한 실패이므로 조치(ssh-keyscan)가
	// 오류 메시지에 들어 있어야 한다 (DialSFTP 주석의 취지).
	if !containsAll(err.Error(), "known_hosts", "ssh-keyscan") {
		t.Fatalf("오류에 known_hosts 조치 안내가 없다: %v", err)
	}
}

func TestDialSFTPConnectionRefused(t *testing.T) {
	t.Parallel()

	keyPath := writeTestKey(t)

	// 빈 known_hosts 는 유효한 파일이다 — knownhosts 파싱은 통과하고
	// 접속 단계까지 가야 한다.
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(khPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	// 닫힌 포트를 하나 확보한다: listener 를 열었다 닫으면
	// 그 포트는 (짧은 시간 동안) 아무도 안 듣는 포트다.
	// 즉시 RST 가 오므로 dialTimeout(10s)을 기다리지 않는다.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	_, err = DialSFTP(SFTPDialOptions{
		Host:           "127.0.0.1",
		Port:           port,
		User:           "u",
		PrivateKeyPath: keyPath,
		KnownHostsPath: khPath,
	})
	if err == nil {
		t.Fatal("닫힌 포트인데 err = nil")
	}
}

// ============================================================
// 실서버 계약 테스트 — localfs_test.go 10종의 거울
//
// SFTPGo 를 상대로 Uploader 계약이 LocalFS 와 같은 결과를 내는지
// 검증한다. 접속 정보가 환경변수에 없으면 skip 된다.
//
// PowerShell 예:
//   $env:SFTPTEST_HOST = "localhost"
//   $env:SFTPTEST_PORT = "2022"
//   $env:SFTPTEST_USER = "rinex"
//   $env:SFTPTEST_KEY  = "C:\SFTPClient\keys\id_ed25519"
//   $env:SFTPTEST_KNOWN_HOSTS = "C:\SFTPClient\keys\known_hosts"
//   $env:SFTPTEST_DIR  = "/uploadertest"     # 쓰기 가능한 원격 기준 경로
//   go test ./internal/transport -run TestSFTPFSContract -v
//
// 이 테스트의 통과는 곧 "권한 4종 + PosixRename" 확인의 자동화다:
// EnsureDir=mkdir, UploadPart=업로드, RenameOverwrites=posix-rename,
// Remove=삭제가 전부 실서버에서 실행된다.
// ============================================================

func TestSFTPFSContract(t *testing.T) {
	host := os.Getenv("SFTPTEST_HOST")
	if host == "" {
		t.Skip("SFTPTEST_HOST 미설정 — 실서버 계약 테스트 skip " +
			"(설정 방법은 이 테스트 상단 주석)")
	}

	port, err := strconv.Atoi(os.Getenv("SFTPTEST_PORT"))
	if err != nil {
		t.Fatalf("SFTPTEST_PORT: %v", err)
	}

	baseDir := os.Getenv("SFTPTEST_DIR")
	if baseDir == "" {
		t.Fatal("SFTPTEST_DIR 미설정 (쓰기 가능한 원격 기준 경로)")
	}

	s, err := DialSFTP(SFTPDialOptions{
		Host:           host,
		Port:           port,
		User:           os.Getenv("SFTPTEST_USER"),
		PrivateKeyPath: os.Getenv("SFTPTEST_KEY"),
		KnownHostsPath: os.Getenv("SFTPTEST_KNOWN_HOSTS"),
	})
	if err != nil {
		t.Fatalf("DialSFTP: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	ctx := context.Background()

	// 실행마다 고유한 작업 디렉터리를 만들어 이전 실행의 잔여물과
	// 격리한다. 정리는 best-effort 다 — 실패해도 다음 실행은
	// 새 디렉터리를 쓰므로 오염되지 않는다.
	work := path.Join(baseDir,
		"uploadertest-"+strconv.FormatInt(time.Now().UnixNano(), 10))

	if err := s.EnsureDir(ctx, work); err != nil {
		t.Fatalf("작업 디렉터리 생성: %v", err)
	}
	t.Cleanup(func() { cleanupRemoteDir(t, s, work) })

	// --- localfs 계약 10종의 거울 ---

	t.Run("EnsureDirIdempotent", func(t *testing.T) {
		dir := path.Join(work, "a/b/c")
		if err := s.EnsureDir(ctx, dir); err != nil {
			t.Fatalf("1차: %v", err)
		}
		if err := s.EnsureDir(ctx, dir); err != nil {
			t.Fatalf("2차 (이미 존재): %v", err)
		}
	})

	t.Run("EnsureDirRejectsFile", func(t *testing.T) {
		p := uploadString(t, s, work, "blocker", "x")
		if err := s.EnsureDir(ctx, p); err == nil {
			t.Fatal("같은 이름의 파일이 있는데 EnsureDir 이 성공했다")
		}
	})

	t.Run("UploadPartOverwrites", func(t *testing.T) {
		p := uploadString(t, s, work, "ow.part", "first-longer-content")
		local := writeLocalFile(t, "second")
		if err := s.UploadPart(ctx, local, p); err != nil {
			t.Fatalf("덮어쓰기 업로드: %v", err)
		}

		size, err := s.Size(ctx, p)
		if err != nil {
			t.Fatalf("Size: %v", err)
		}
		if size != int64(len("second")) {
			t.Fatalf("size = %d, want %d (truncate 후 덮어쓰기)",
				size, len("second"))
		}
	})

	t.Run("UploadPartRequiresParent", func(t *testing.T) {
		local := writeLocalFile(t, "x")
		err := s.UploadPart(ctx, local,
			path.Join(work, "no-such-parent/f.part"))
		if err == nil {
			t.Fatal("부모 디렉터리가 없는데 UploadPart 가 성공했다 " +
				"(디렉터리 생성은 EnsureDir 의 책임)")
		}
	})

	t.Run("SizeNotExist", func(t *testing.T) {
		_, err := s.Size(ctx, path.Join(work, "no-such-file"))
		if !errors.Is(err, fs.ErrNotExist) {
			// 이 단언이 실패하면 wrapNotExist 의 StatusError 판별이
			// 실서버의 실제 오류 형태와 안 맞는 것이다 — 정규화 계약의
			// 실서버 증명이 이 한 줄이다.
			t.Fatalf("err = %v, want fs.ErrNotExist 체인", err)
		}
	})

	t.Run("SizeRejectsDir", func(t *testing.T) {
		if _, err := s.Size(ctx, work); err == nil {
			t.Fatal("디렉터리인데 Size 가 성공했다")
		}
	})

	t.Run("RenameOverwritesExisting", func(t *testing.T) {
		// ★ PosixRename 의 실서버 증명.
		// revision 재전송 경로의 정상 동작이 여기 달려 있다.
		final := path.Join(work, "final.rnx")
		uploadStringTo(t, s, final, "old-version-longer")

		part := uploadString(t, s, work, "new.part", "new")
		if err := s.Rename(ctx, part, final); err != nil {
			t.Fatalf("대상 존재 상태의 Rename: %v", err)
		}

		size, err := s.Size(ctx, final)
		if err != nil {
			t.Fatalf("Size(final): %v", err)
		}
		if size != int64(len("new")) {
			t.Fatalf("size = %d, want %d (덮어쓰기가 안 됐다)",
				size, len("new"))
		}

		if _, err := s.Size(ctx, part); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("rename 후 oldPath 가 남아 있다: %v", err)
		}
	})

	t.Run("RemoveMissingIsNil", func(t *testing.T) {
		if err := s.Remove(ctx, path.Join(work, "never-existed")); err != nil {
			t.Fatalf("없는 파일 Remove 는 멱등이어야 한다: %v", err)
		}
	})

	t.Run("RemoveRejectsDir", func(t *testing.T) {
		if err := s.Remove(ctx, work); err == nil {
			t.Fatal("디렉터리인데 Remove 가 성공했다")
		}
	})

	t.Run("UploadPartCanceledContext", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()

		local := writeLocalFile(t, "x")
		err := s.UploadPart(canceled, local, path.Join(work, "c.part"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

// ============================================================
// 테스트 헬퍼
// ============================================================

// writeTestKey 는 passphrase 없는 ed25519 개인키 파일을 만든다.
// Dial 의 키 파싱 단계를 통과시키기 위한 용도다.
func writeTestKey(t *testing.T) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	block, err := ssh.MarshalPrivateKey(priv, "uploadertest")
	if err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(p, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}

	return p
}

// writeLocalFile 은 로컬 임시 파일을 만들고 경로를 반환한다
// (UploadPart 의 localPath 용).
func writeLocalFile(t *testing.T, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "src")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return p
}

// uploadString 은 content 를 dir/name 으로 업로드하고 원격 경로를 반환한다.
func uploadString(
	t *testing.T,
	s *SFTPFS,
	dir string,
	name string,
	content string,
) string {
	t.Helper()

	p := path.Join(dir, name)
	uploadStringTo(t, s, p, content)

	return p
}

func uploadStringTo(t *testing.T, s *SFTPFS, remote, content string) {
	t.Helper()

	if err := s.UploadPart(
		context.Background(), writeLocalFile(t, content), remote,
	); err != nil {
		t.Fatalf("upload %s: %v", remote, err)
	}
}

// cleanupRemoteDir 은 작업 디렉터리를 best-effort 로 정리한다.
// 파일 → 하위 디렉터리 → 자신 순서다. 실패는 로그만 남긴다 —
// 다음 실행은 새 디렉터리를 쓰므로 잔여물이 테스트를 오염시키지 않는다.
func cleanupRemoteDir(t *testing.T, s *SFTPFS, dir string) {
	t.Helper()

	entries, err := s.client.ReadDir(dir)
	if err != nil {
		t.Logf("cleanup: readdir %s: %v", dir, err)

		return
	}

	for _, e := range entries {
		p := path.Join(dir, e.Name())
		if e.IsDir() {
			cleanupRemoteDir(t, s, p)

			continue
		}

		if err := s.client.Remove(p); err != nil {
			t.Logf("cleanup: remove %s: %v", p, err)
		}
	}

	if err := s.client.RemoveDirectory(dir); err != nil {
		t.Logf("cleanup: rmdir %s: %v", dir, err)
	}
}

// containsAll 은 s 가 모든 subs 를 포함하는지 본다.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}

	return true
}
