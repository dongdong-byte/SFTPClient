package lock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAcquireAndRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	l, err := Acquire(path, time.Hour)
	if err != nil {
		t.Fatalf("Acquire() 실패: %v", err)
	}

	if l.TookOver {
		t.Error("신규 획득인데 TookOver=true")
	}

	if l.Path() == "" {
		t.Error("Path() 가 비었다")
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release() 실패: %v", err)
	}

	// 해제 후 다시 획득할 수 있어야 한다.
	l2, err := Acquire(path, time.Hour)
	if err != nil {
		t.Fatalf("재 Acquire() 실패: %v", err)
	}

	if err := l2.Release(); err != nil {
		t.Fatalf("재 Release() 실패: %v", err)
	}
}

func TestAcquireRejectsEmptyPath(t *testing.T) {
	for _, path := range []string{"", "   ", "\t"} {
		_, err := Acquire(path, time.Hour)
		if !errors.Is(err, ErrInvalidPath) {
			t.Errorf(
				"Acquire(%q) = %v, want ErrInvalidPath",
				path,
				err,
			)
		}
	}
}

func TestAcquireRejectsNonPositiveStaleAfter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	for _, stale := range []time.Duration{0, -time.Second} {
		_, err := Acquire(path, stale)
		if err == nil {
			t.Errorf("staleAfter=%v: expected error", stale)
		}
	}
}

func TestAcquireHeldByLiveLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	l1, err := Acquire(path, time.Hour)
	if err != nil {
		t.Fatalf("첫 Acquire() 실패: %v", err)
	}
	defer func() { _ = l1.Release() }()

	_, err = Acquire(path, time.Hour)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("두 번째 Acquire() = %v, want ErrHeld", err)
	}
}

func TestAcquireStaleTakeover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	// staleAfter 보다 오래된 owner 를 직접 심는다.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	owner := filepath.Join(path, ownerPrefix+"deadbeefdeadbeefdeadbeefdeadbeef")
	oldStarted := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	content := "pid=1\nstarted=" + oldStarted + "\ntoken=deadbeefdeadbeefdeadbeefdeadbeef\n"

	if err := os.WriteFile(owner, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	l, err := Acquire(path, time.Hour)
	if err != nil {
		t.Fatalf("stale Acquire() 실패: %v", err)
	}

	if !l.TookOver {
		t.Error("TookOver = false, want true")
	}

	if l.Prev.PID != 1 {
		t.Errorf("Prev.PID = %d, want 1", l.Prev.PID)
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release() 실패: %v", err)
	}
}

func TestReleaseErrLostAfterOwnerRemoved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	l, err := Acquire(path, time.Hour)
	if err != nil {
		t.Fatalf("Acquire() 실패: %v", err)
	}

	// 다른 인스턴스가 stale 로 오판하여 owner 만 지운 상황.
	if err := os.Remove(l.ownerPath); err != nil {
		t.Fatalf("Remove owner: %v", err)
	}

	err = l.Release()
	if !errors.Is(err, ErrLost) {
		t.Fatalf("Release() = %v, want ErrLost", err)
	}
}

func TestNoOwnerGraceHoldsBriefly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	// owner 없이 방금 만든 디렉터리 — noOwnerGrace 안이면 ErrHeld.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	_, err := Acquire(path, time.Hour)
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("Acquire() = %v, want ErrHeld (noOwnerGrace)", err)
	}
}

func TestNoOwnerStaleAfterGrace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// 디렉터리 mtime 을 grace 밖으로 민다.
	old := time.Now().Add(-2 * noOwnerGrace)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	l, err := Acquire(path, time.Hour)
	if err != nil {
		t.Fatalf("Acquire() 실패: %v", err)
	}

	if !l.TookOver {
		t.Error("owner 없는 stale 탈취인데 TookOver=false")
	}

	if err := l.Release(); err != nil {
		t.Fatalf("Release() 실패: %v", err)
	}
}

func TestMultipleOwnerMarkersIsHardError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	for _, name := range []string{
		ownerPrefix + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ownerPrefix + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		p := filepath.Join(path, name)
		if err := os.WriteFile(p, []byte("pid=1\n"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	_, err := Acquire(path, time.Hour)
	if err == nil {
		t.Fatal("multiple owners: expected error")
	}

	if errors.Is(err, ErrHeld) {
		t.Error("multiple owners 를 ErrHeld 로 취급하면 안 된다")
	}

	if !contains(err.Error(), "inspect") {
		t.Errorf("수동 확인 안내가 없다: %v", err)
	}
}

func TestClearStaleNonEmptyDirectoryMessage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	// stale owner + 예상 밖 파일.
	owner := filepath.Join(path, ownerPrefix+"cccccccccccccccccccccccccccccccc")
	oldStarted := time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339Nano)
	content := "pid=9\nstarted=" + oldStarted + "\ntoken=cccccccccccccccccccccccccccccccc\n"

	if err := os.WriteFile(owner, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile owner: %v", err)
	}

	junk := filepath.Join(path, "unexpected.txt")
	if err := os.WriteFile(junk, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile junk: %v", err)
	}

	_, err := Acquire(path, time.Hour)
	if err == nil {
		t.Fatal("비어 있지 않은 stale 디렉터리: expected error")
	}

	if !contains(err.Error(), "inspect the lock directory manually") {
		t.Errorf("수동 확인 안내가 없다: %v", err)
	}
}

func TestConcurrentAcquireOnlyOneWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.lock")

	const n = 16

	var (
		start sync.WaitGroup
		done  sync.WaitGroup
		mu    sync.Mutex
		wins  []*Lock
		held  int
		other int
	)

	start.Add(n)
	done.Add(n)
	gate := make(chan struct{})

	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()

			start.Done()
			<-gate

			l, err := Acquire(path, time.Hour)
			if err != nil {
				mu.Lock()
				if errors.Is(err, ErrHeld) {
					held++
				} else {
					other++
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			wins = append(wins, l)
			mu.Unlock()
		}()
	}

	start.Wait()
	close(gate)
	done.Wait()

	if len(wins) != 1 {
		t.Errorf(
			"wins = %d, want 1 (held=%d other=%d)",
			len(wins),
			held,
			other,
		)
	}

	if other != 0 {
		t.Errorf("unexpected errors: %d", other)
	}

	for _, l := range wins {
		if err := l.Release(); err != nil {
			t.Errorf("Release() 실패: %v", err)
		}
	}
}

func TestRelativePathNormalizesToAbsolute(t *testing.T) {
	dir := t.TempDir()

	// TempDir 로 cwd 를 옮기지 않고, dir 아래 상대 형태를 Abs 로 맞춘다.
	rel := filepath.Join(dir, "rel.lock")

	l, err := Acquire(rel, time.Hour)
	if err != nil {
		t.Fatalf("Acquire() 실패: %v", err)
	}
	defer func() { _ = l.Release() }()

	if !filepath.IsAbs(l.Path()) {
		t.Errorf("Path() = %q, want absolute", l.Path())
	}
}

func TestParseInfo(t *testing.T) {
	info := parseInfo("pid=42\nstarted=2026-08-30T00:00:00.000000000Z\ntoken=abc\njunk\n")

	if info.PID != 42 {
		t.Errorf("PID = %d, want 42", info.PID)
	}

	if info.Token != "abc" {
		t.Errorf("Token = %q, want abc", info.Token)
	}

	if info.Started.IsZero() {
		t.Error("Started is zero")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
