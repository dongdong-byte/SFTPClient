package transport

import (
	"context"
	"sync"
	"time"
)

// progress 는 원격 SFTP 작업의 "진전"을 연결 단위로 계측한다.
// (UNIT3 v3 §3.3 — 연결 하나, 진행 시각 하나)
//
// 판정에 쓰는 사실은 두 개다:
//
//   - inFlight: 지금 원격 응답을 기다리는 작업 수
//   - lastBeat: 마지막으로 진전이 관측된 시각
//
// 작업별·파일별 타이머를 두지 않는다 (v3 §3.3 기각 — 블록을 푸는
// 지렛대가 연결 close 하나뿐이므로 호출마다 감시 기계를 깔 이유가 없다).
//
// 유휴 0→1과 완료에서 기준 시각을 갱신한다. 카운터와 시각은 같은
// mutex 아래에서 변경·조회한다. 진행 중 새 착수는 제한을 연장하지 않는다.
// time.Time의 monotonic clock을 보존해 시스템 시각 보정 영향을 피한다.
//
// SSH keepalive 응답으로는 갱신하지 않는다 (v3 §3.3 규칙 3 —
// 연결 생존 ≠ 작업 진전). 현재 keepalive 자체가 없으므로 자동 충족.
type progress struct {
	mu       sync.Mutex
	inFlight int64
	lastBeat time.Time
}

// enter 는 원격 작업의 시작을 기록한다. exit 와 짝이다.
func (p *progress) enter() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight == 0 {
		p.lastBeat = time.Now()
	}
	p.inFlight++
}

// exit 는 원격 작업의 종료(성공·실패 무관)를 기록한다.
// 실패 반환도 "원격이 응답했다"는 진전이다.
func (p *progress) exit() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastBeat = time.Now()
	p.inFlight--
}

// beat 는 진전 시각을 지금으로 갱신한다. UploadPart 가 청크 쓰기
// 성공마다 호출한다 — 오래 걸리는 큰 파일도 청크가 계속 쓰이는 한
// 스톨이 아니다 (파일당 총시간 제한 기각의 구현체).
func (p *progress) beat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastBeat = time.Now()
}

// snapshot 은 (진행 중 작업 수, 마지막 진전 이후 경과)를 돌려준다.
// 아직 아무 원격 작업도 없었으면 경과는 0이다 (감시자가 유휴를
// 스톨로 읽지 않도록).
func (p *progress) snapshot() (inFlight int64, idle time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastBeat.IsZero() {
		return p.inFlight, 0
	}

	return p.inFlight, time.Since(p.lastBeat)
}

// ProgressSource 는 무진행 감시가 읽는 계측이다.
// *SFTPFS 가 만족한다. 인터페이스로 두는 이유는 WatchStall 을
// 실제 SSH 연결 없이 테스트하기 위해서다.
type ProgressSource interface {
	Progress() (inFlight int64, idle time.Duration)
}

// watchPoll 은 무진행 감시의 표본 주기다.
//
// config 로 두지 않는다 — 발화 정밀도(±1초)는 StallTimeout(5초 이상)
// 대비 충분하고, 운영에서 조정할 근거가 없다.
const watchPoll = time.Second

// WatchStall 은 src 의 무진행을 감시한다.
// (UNIT3 v3 §3.1~§3.3 — 인지 계층. 조치는 onStall 이 한다)
//
// 발화 조건: 진행 중인 원격 작업이 존재하는데(inFlight > 0)
// 마지막 진전 이후 timeout 이 지났다. 유휴(inFlight == 0)는 스톨이
// 아니다 — 스캔·해시 등 로컬 단계가 아무리 길어도 발화하지 않는다
// (로컬 블록은 이 유닛의 범위 밖, v3 §3.4 분리).
//
// 발화 시 onStall 을 정확히 한 번 부르고 반환한다. ctx 취소 시
// 조용히 반환한다. main 이 goroutine 으로 띄우고, onStall 안에서
// 회차 취소 → Abort 순서를 배선한다 (순서 근거: v3 §3.2 — 취소가
// 먼저여야 깨어난 워커가 죽은 세션에 새 착수를 이어가며 attempts 를
// 소모하는 경로가 닫힌다).
func WatchStall(
	ctx context.Context,
	src ProgressSource,
	timeout time.Duration,
	onStall func(inFlight int64, idle time.Duration),
) {
	t := time.NewTicker(watchPoll)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-t.C:
			n, idle := src.Progress()
			if n > 0 && idle > timeout {
				onStall(n, idle)
				return
			}
		}
	}
}
