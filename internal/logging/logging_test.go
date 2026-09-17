package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// day 는 테스트용 로컬 정오 시각을 만든다.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local)
}

// fixedNow 는 포인터가 가리키는 시각을 돌려주는 nowFn 을 만든다.
func fixedNow(t *time.Time) func() time.Time {
	return func() time.Time { return *t }
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func mustWrite(t *testing.T, w *RotatingWriter, s string) {
	t.Helper()
	if _, err := w.Write([]byte(s)); err != nil {
		t.Fatalf("Write %q: %v", s, err)
	}
}

// T1 — 생성자가 디렉터리·오늘 파일을 즉시 생성하고, Write 는 append 다.
func TestNew_CreatesDirAndTodayFileImmediately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "logs") // 존재하지 않는 하위 디렉터리
	now := day(2026, 9, 17)

	w, err := newRotatingWriter(dir, 30, &hooks{nowFn: fixedNow(&now), warnOut: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	path := filepath.Join(dir, "rinexclient_20260917.log")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("생성자 직후 오늘 파일이 없다 (즉시 열기 계약 위반): %v", err)
	}

	mustWrite(t, w, "hello\n")
	if got := readFile(t, path); got != "hello\n" {
		t.Fatalf("내용 불일치: %q", got)
	}
}

// T2 — 날짜 전환. 회전 열기 실패 시 기존 파일에 계속 기록하고 열린 날짜
// 상태를 갱신하지 않으며, 다음 Write 가 자동 재시도한다.
func TestRotate_SwitchesAtDateChange_AndRetriesAfterOpenFailure(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)

	failOpen := false
	h := &hooks{
		nowFn:   fixedNow(&now),
		warnOut: io.Discard,
		openFn: func(name string) (*os.File, error) {
			if failOpen {
				return nil, errors.New("injected open failure")
			}
			return defaultOpen(name)
		},
	}
	w, err := newRotatingWriter(dir, 30, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	mustWrite(t, w, "day1\n")

	// 자정 경과 + 새 파일 열기 실패 → 기존 파일에 강등 기록.
	now = day(2026, 9, 18)
	failOpen = true
	mustWrite(t, w, "day2-degraded\n")

	oldPath := filepath.Join(dir, "rinexclient_20260917.log")
	newPath := filepath.Join(dir, "rinexclient_20260918.log")
	if got := readFile(t, oldPath); got != "day1\nday2-degraded\n" {
		t.Fatalf("강등 기록이 기존 파일에 없다: %q", got)
	}
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("실패했는데 새 날짜 파일이 존재: %v", err)
	}
	if w.openedDate != "20260917" {
		t.Fatalf("실패 시 openedDate 가 갱신됨 (재시도 경로 사망): %s", w.openedDate)
	}

	// 원인 해소 → 다음 Write 가 자동으로 새 파일로 전환.
	failOpen = false
	mustWrite(t, w, "day2-ok\n")
	if got := readFile(t, newPath); got != "day2-ok\n" {
		t.Fatalf("복구 후 새 파일 내용 불일치: %q", got)
	}
	if w.openedDate != "20260918" {
		t.Fatalf("성공 후 openedDate 미갱신: %s", w.openedDate)
	}
}

// T3 — 보존 경계 3점: 오늘−29 생존 / 오늘−30 삭제 / 패턴 불일치 생존.
// 셋 다 mtime 은 "오늘"(방금 생성)이다 — 파일명 날짜 기준임을 함께 증명.
func TestCleanup_RetentionBoundaryByFilenameDate(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)

	keep := filepath.Join(dir, "rinexclient_20260819.log") // 오늘−29
	drop := filepath.Join(dir, "rinexclient_20260818.log") // 오늘−30
	other := filepath.Join(dir, "someone_elses_file.log")  // 패턴 불일치
	for _, p := range []string{keep, drop, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	w, err := newRotatingWriter(dir, 30, &hooks{nowFn: fixedNow(&now), warnOut: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("오늘−29 가 삭제됨 (경계 오차): %v", err)
	}
	if _, err := os.Stat(drop); !os.IsNotExist(err) {
		t.Fatalf("오늘−30 이 생존 (경계 오차): %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("패턴 불일치 파일이 삭제됨 (자기 산출물 원칙 위반): %v", err)
	}
}

// T4 — 병렬 Write 직렬화. -race 로 실행되며 줄 수·무결성을 검증한다.
func TestWrite_ConcurrentLinesIntact(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)
	w, err := newRotatingWriter(dir, 30, &hooks{nowFn: fixedNow(&now), warnOut: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	const goroutines, lines = 8, 200
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < lines; i++ {
				if _, err := fmt.Fprintf(w, "g%02d-l%03d\n", g, i); err != nil {
					errCh <- err
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent Write: %v", err)
	}

	content := readFile(t, filepath.Join(dir, "rinexclient_20260917.log"))
	got := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if len(got) != goroutines*lines {
		t.Fatalf("줄 수 불일치: got=%d want=%d", len(got), goroutines*lines)
	}
	seen := make(map[string]bool, len(got))
	for _, line := range got {
		if len(line) != len("g00-l000") {
			t.Fatalf("줄이 섞임: %q", line)
		}
		seen[line] = true
	}
	if len(seen) != goroutines*lines {
		t.Fatalf("중복/유실: unique=%d want=%d", len(seen), goroutines*lines)
	}
}

// T5 — 최초 열기 실패: 생성자가 error 를 반환한다 (강등 판단은 main 몫).
// 부모가 디렉터리가 아니라 파일이면 MkdirAll 이 실패한다 — root 로
// 실행되는 환경에서도 재현되는 실패 유도다.
func TestNew_ReturnsErrorWhenDirUnavailable(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not_a_dir")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := newRotatingWriter(filepath.Join(parent, "logs"), 30, &hooks{warnOut: io.Discard})
	if err == nil {
		t.Fatal("디렉터리 생성 불가인데 error 가 아니다")
	}
}

// T6 — 배선 도달: 표준 log 경로(MultiWriter)로 들어온 출력이 파일에 남는다.
func TestWiring_StdLogThroughMultiWriterReachesFile(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)
	w, err := newRotatingWriter(dir, 30, &hooks{nowFn: fixedNow(&now), warnOut: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var stderrStub bytes.Buffer
	l := log.New(io.MultiWriter(&stderrStub, w), "", log.LstdFlags)
	l.Printf("[SCAN] wired=%d", 1)

	content := readFile(t, filepath.Join(dir, "rinexclient_20260917.log"))
	if !strings.Contains(content, "[SCAN] wired=1") {
		t.Fatalf("표준 log 출력이 파일에 없다: %q", content)
	}
	if !strings.Contains(stderrStub.String(), "[SCAN] wired=1") {
		t.Fatal("stderr 병행 출력이 없다")
	}
}

// T7 — 패턴과 일치하는 비정규 항목(비어 있지 않은 디렉터리)은 삭제하지
// 않는다. 생성·기록은 정상이고 남의 inode 를 건드리지 않는다.
// logging.go 는 IsRegular 가 아닌 항목을 os.Remove 대상에서 제외한다.
func TestCleanup_SkipsNonRegularMatchingName(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)

	undeletable := filepath.Join(dir, "rinexclient_20200101.log")
	if err := os.MkdirAll(filepath.Join(undeletable, "child"), 0o755); err != nil {
		t.Fatal(err)
	}

	var warns bytes.Buffer
	w, err := newRotatingWriter(dir, 30, &hooks{nowFn: fixedNow(&now), warnOut: &warns})
	if err != nil {
		t.Fatalf("비정규 항목이 생성자를 실패시킴: %v", err)
	}
	defer w.Close()

	mustWrite(t, w, "still working\n")
	if _, err := os.Stat(undeletable); err != nil {
		t.Fatalf("비정규 항목이 삭제됨: %v", err)
	}
	if strings.Contains(warns.String(), "retention delete") {
		t.Fatalf("비정규 항목에 삭제 시도가 있다: %q", warns.String())
	}
}

// T8 — append 계약: 동일 날짜 파일이 이미 존재할 때 새 Writer 가 기존
// 내용을 유지하고 뒤에 추가한다 (매시 재기동 = 같은 파일 24회 재열기).
func TestOpen_AppendsToExistingSameDateFile(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)
	path := filepath.Join(dir, "rinexclient_20260917.log")
	if err := os.WriteFile(path, []byte("previous run\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w, err := newRotatingWriter(dir, 30, &hooks{nowFn: fixedNow(&now), warnOut: io.Discard})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()
	mustWrite(t, w, "this run\n")

	if got := readFile(t, path); got != "previous run\nthis run\n" {
		t.Fatalf("truncate 발생 또는 append 실패: %q", got)
	}
}

// T9 — Write 실패 강등: (len, nil) 반환, 경고는 상태 진입 시 1회,
// 복구 시 resumed 통지와 함께 기록이 재개된다.
func TestWrite_FailureDegradesOnceAndResumes(t *testing.T) {
	dir := t.TempDir()
	now := day(2026, 9, 17)

	failWrite := false
	var warns bytes.Buffer
	h := &hooks{
		nowFn:   fixedNow(&now),
		warnOut: &warns,
		writeFn: func(f *os.File, p []byte) (int, error) {
			if failWrite {
				return 0, errors.New("injected write failure")
			}
			return f.Write(p)
		},
	}
	w, err := newRotatingWriter(dir, 30, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	failWrite = true
	for i := 0; i < 3; i++ {
		n, err := w.Write([]byte("lost\n"))
		if err != nil || n != len("lost\n") {
			t.Fatalf("강등 계약 위반: n=%d err=%v", n, err)
		}
	}
	if c := strings.Count(warns.String(), "[LOG][WARN] file logging failed"); c != 1 {
		t.Fatalf("실패 경고가 1회가 아니다: %d회\n%s", c, warns.String())
	}

	failWrite = false
	mustWrite(t, w, "back\n")
	if !strings.Contains(warns.String(), "[LOG][INFO] file logging resumed") {
		t.Fatalf("복구 통지가 없다: %q", warns.String())
	}
	content := readFile(t, filepath.Join(dir, "rinexclient_20260917.log"))
	if !strings.Contains(content, "back\n") {
		t.Fatalf("복구 후 기록이 없다: %q", content)
	}

	// 재실패 → 새 상태 진입이므로 경고가 한 번 더 나와야 한다.
	failWrite = true
	if _, err := w.Write([]byte("lost again\n")); err != nil {
		t.Fatalf("재실패 Write: %v", err)
	}
	if c := strings.Count(warns.String(), "[LOG][WARN] file logging failed"); c != 2 {
		t.Fatalf("재진입 경고 누락: %d회", c)
	}
}

// 보조 — retentionDays 계약: 1 미만은 생성자 거부.
func TestNew_RejectsNonPositiveRetention(t *testing.T) {
	if _, err := newRotatingWriter(t.TempDir(), 0, &hooks{warnOut: io.Discard}); err == nil {
		t.Fatal("retentionDays=0 이 통과됨")
	}
}
