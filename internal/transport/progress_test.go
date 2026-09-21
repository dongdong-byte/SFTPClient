package transport

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProgress 는 WatchStall 테스트용 ProgressSource 다.
// 실제 SSH 연결 없이 계측값을 임의로 설정한다.
type fakeProgress struct {
	inFlight atomic.Int64
	idle     atomic.Int64 // time.Duration
}

func (f *fakeProgress) Progress() (int64, time.Duration) {
	return f.inFlight.Load(), time.Duration(f.idle.Load())
}

// TestProgressEnterResetsIdle 은 유휴 후 첫 작업이 옛 시각을 물려받아
// 즉시 만료하지 않음을 고정한다 (UNIT3 v3 §3.3 의 0→1 규칙).
func TestProgressEnterResetsIdle(t *testing.T) {
	var p progress

	// 과거의 진전 하나를 심는다 (오래전에 끝난 이전 작업).
	p.lastBeat = time.Now().Add(-time.Hour)

	// 유휴 상태에서는 경과가 커도 inFlight == 0 이므로 발화 대상이 아니다.
	if n, _ := p.snapshot(); n != 0 {
		t.Fatalf("inFlight = %d, want 0", n)
	}

	// 새 작업 시작이 기준 시각을 재설정해야 한다.
	p.enter()
	defer p.exit()

	n, idle := p.snapshot()
	if n != 1 {
		t.Fatalf("inFlight = %d, want 1", n)
	}
	if idle > time.Minute {
		t.Fatalf("enter 가 기준 시각을 재설정하지 않았다: idle = %v", idle)
	}
}

// TestProgressExitDecrements 는 enter/exit 짝이 계수를 복원함을 고정한다.
func TestProgressExitDecrements(t *testing.T) {
	var p progress

	p.enter()
	p.enter()
	p.exit()

	if n, _ := p.snapshot(); n != 1 {
		t.Fatalf("inFlight = %d, want 1", n)
	}

	p.exit()

	if n, _ := p.snapshot(); n != 0 {
		t.Fatalf("inFlight = %d, want 0", n)
	}
}

// 새 착수만으로 이미 진행 중인 작업의 무진행 시간을 초기화하지 않는다.
func TestProgressNewWorkDoesNotMaskStall(t *testing.T) {
	var p progress
	p.enter()
	p.lastBeat = time.Now().Add(-time.Hour)
	p.enter()
	n, idle := p.snapshot()
	if n != 2 || idle < time.Minute {
		t.Fatalf("new work masked stall: (%d, %v)", n, idle)
	}
	p.exit()
	p.exit()
}

// 아무 작업도 없던 연결에서는 경과가 0이다.
func TestProgressSnapshotBeforeAnyWork(t *testing.T) {
	var p progress

	n, idle := p.snapshot()
	if n != 0 || idle != 0 {
		t.Fatalf("snapshot = (%d, %v), want (0, 0)", n, idle)
	}
}

// TestWatchStallFires 는 발화 조건(진행 중 + 무진행 초과)에서
// onStall 이 정확히 한 번 불리고 감시가 종료됨을 고정한다.
func TestWatchStallFires(t *testing.T) {
	src := &fakeProgress{}
	src.inFlight.Store(3)
	src.idle.Store(int64(time.Hour))

	var calls atomic.Int64
	done := make(chan struct{})

	go func() {
		WatchStall(
			context.Background(),
			src,
			50*time.Millisecond,
			func(n int64, idle time.Duration) {
				if n != 3 {
					t.Errorf("onStall inFlight = %d, want 3", n)
				}
				calls.Add(1)
			},
		)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WatchStall 이 발화 후 반환하지 않았다")
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("onStall 호출 %d회, want 1", got)
	}
}

// TestWatchStallIgnoresIdleConnection 은 유휴(inFlight == 0)에서는
// 경과가 아무리 커도 발화하지 않음을 고정한다 — 스캔·해시 같은
// 로컬 단계가 길어도 회차를 끊지 않는다 (v3 §3.4 분리의 경계).
func TestWatchStallIgnoresIdleConnection(t *testing.T) {
	src := &fakeProgress{}
	src.inFlight.Store(0)
	src.idle.Store(int64(time.Hour))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fired := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		WatchStall(ctx, src, 50*time.Millisecond,
			func(int64, time.Duration) { fired <- struct{}{} })
		close(done)
	}()

	// 표본 주기(1초)를 넉넉히 넘겨 관찰한다.
	select {
	case <-fired:
		t.Fatal("유휴 연결에서 발화했다")
	case <-time.After(2500 * time.Millisecond):
	}

	cancel()
	<-done
}

// TestWatchStallStopsOnCancel 은 ctx 취소로 감시가 발화 없이
// 종료됨을 고정한다 (정상 회차의 뒷정리 경로).
func TestWatchStallStopsOnCancel(t *testing.T) {
	src := &fakeProgress{}
	src.inFlight.Store(1)
	src.idle.Store(0) // 진전이 계속 관측되는 정상 전송

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		WatchStall(ctx, src, time.Hour,
			func(int64, time.Duration) { t.Error("정상 전송에서 발화했다") })
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("취소 후에도 WatchStall 이 반환하지 않았다")
	}
}
