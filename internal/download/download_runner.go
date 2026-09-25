package download

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// DOWNLOAD 커밋 5 — Runner (DOWNLOAD_COMMIT_PLAN v1 §6).

// DefaultCleanupTimeout 은 실패 후 .part 정리의 기본 상한이다 (§6.3).
// 취소된 회차에서도 정리는 시도하되 무기한 기다리지 않는다.
const DefaultCleanupTimeout = 10 * time.Second

// Job 은 Category 하나의 실행 단위다. main 이 config 의 활성
// [DOWNLOAD.<CATEGORY>] 로 만든다.
type Job struct {
	Category   domain.Category
	RemotePath *pathpl.Template // 원격 원본 (스캔 경로)
	LocalPath  *pathpl.Template // 로컬 목적지
}

// Options 는 실행 방식이다.
type Options struct {
	// MaxWorkers 는 동시 수신 상한이다 ([DOWNLOAD] MaxWorkers). 0 이하면 1.
	MaxWorkers int

	// Grace 는 원격 mtime 나이 문턱이다 ([DOWNLOAD] GraceSeconds). 0 이면
	// 유예·미래시각 검사를 끈다.
	Grace time.Duration

	// Sites 는 [DOWNLOAD] Sites 다. 비면 전체 관측소.
	Sites []string

	// CleanupTimeout 은 실패 후 .part 정리의 상한이다. 0 이하면
	// DefaultCleanupTimeout.
	CleanupTimeout time.Duration

	// Logger 는 파일별 실패·충돌 로그에 쓴다. nil 이면 log.Default().
	// 회차 요약 줄은 여기서 쓰지 않는다 (커밋 6).
	Logger *log.Logger

	// Now 는 Grace 판정의 현재 시각이다. nil 이면 time.Now.
	Now func() time.Time
}

// Runner 는 DOWNLOAD 한 회차를 실행한다.
//
// 모든 의존이 필드로 드러나 있다 (put.Runner 와 같은 방식). Remote 는
// main 이 DialSFTPReadOnly 로 만든 *transport.SFTPFS, Local 은 OSLocalStore.
// 접속 수명(Close·Abort)과 stall 감시는 main 의 몫이다 — 러너는 받은 ctx 가
// 취소되면 새 수신을 시작하지 않고 돌아올 뿐이다.
type Runner struct {
	Remote RemoteFS
	Local  LocalStore
	Opts   Options
}

// outcome 은 파일 하나의 결과다. worker·producer 가 만들어 집계 goroutine 에
// 보낸다. 다섯 결과 중 정확히 하나다: downloaded / notStarted / err / reason.
type outcome struct {
	remote string
	local  string

	downloaded bool
	notStarted bool
	reason     Reason
	err        error
}

// Run 은 jobs 를 순서대로 실행하고 집계를 돌려준다.
//
// Category 는 하나씩 끝까지 처리한다 (나열·판정 → 수신 → 전부 끝남 → 다음
// Category). Category 별 소요가 의미를 갖고, 목적지 중복 제거(destSet)는
// 회차 전체에 하나라서 Category 사이의 수렴도 잡는다.
//
// 한 Category 안의 구조 (§6.1):
//
//	producer(이 goroutine)  Scanner → planner → destSet → jobs 채널
//	worker × MaxWorkers     jobs → fetch → results 채널
//	aggregator(goroutine 1) results → CategoryReport   ← 유일한 쓰기 주체
//
// producer 의 제외 사유도 results 로 보낸다. Report 에 쓰는 goroutine 이
// 하나뿐이라 잠금이 없다.
//
// 반환 오류: ctx 취소(회차 중단) 또는 입력 결함. 취소되면 그때까지의 집계와
// 함께 ctx 오류를 감싸 돌려준다 — 부분 회차를 성공으로 위장하지 않는다.
// 파일 하나의 실패는 오류가 아니라 Report.Failed 다.
func (r *Runner) Run(ctx context.Context, jobs []Job, rng scan.Range) (rep Report, retErr error) {
	if err := r.check(ctx, jobs); err != nil {
		return rep, err
	}

	start := time.Now()
	defer func() { rep.Duration = time.Since(start) }()

	dests := newDestSet()
	sc := scan.New(newSFTPLister(r.Remote))

	for _, job := range jobs {
		cr, err := r.runCategory(ctx, sc, job, rng, dests)
		rep.Categories = append(rep.Categories, cr)

		if err != nil {
			return rep, err
		}
	}

	return rep, nil
}

func (r *Runner) check(ctx context.Context, jobs []Job) error {
	switch {
	case ctx == nil:
		return errors.New("download: run: context is nil")
	case r.Remote == nil:
		return errNilRemote
	case r.Local == nil:
		return errors.New("download: run: local store is nil")
	}

	for _, j := range jobs {
		if j.RemotePath == nil || j.LocalPath == nil {
			return fmt.Errorf("download: run: [%s] path template is nil", j.Category)
		}
	}

	return nil
}

func (r *Runner) workers() int {
	if r.Opts.MaxWorkers < 1 {
		return 1
	}

	return r.Opts.MaxWorkers
}

func (r *Runner) logger() *log.Logger {
	if r.Opts.Logger == nil {
		return log.Default()
	}

	return r.Opts.Logger
}

func (r *Runner) cleanupTimeout() time.Duration {
	if r.Opts.CleanupTimeout <= 0 {
		return DefaultCleanupTimeout
	}

	return r.Opts.CleanupTimeout
}

// runCategory 는 Category 하나를 끝까지 처리한다.
func (r *Runner) runCategory(
	ctx context.Context,
	sc *scan.Scanner,
	job Job,
	rng scan.Range,
	dests *destSet,
) (CategoryReport, error) {
	start := time.Now()
	cr := newCategoryReport(job.Category)

	p, err := newPlanner(
		job.Category,
		job.LocalPath,
		verify.Verifier{Grace: r.Opts.Grace, Now: r.Opts.Now},
		r.Opts.Sites,
		r.Local.Join,
	)
	if err != nil {
		return cr, err
	}

	cands := make(chan Candidate)
	results := make(chan outcome)

	// aggregator — CategoryReport 의 유일한 쓰기 주체.
	aggDone := make(chan struct{})

	go func() {
		defer close(aggDone)

		for o := range results {
			cr.add(o)
		}
	}()

	// workers
	var wg sync.WaitGroup

	for i := 0; i < r.workers(); i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for c := range cands {
				results <- r.fetch(ctx, c)
			}
		}()
	}

	// producer — Scanner 와 판정은 순차다 (§6.1).
	scanRes, scanErr := sc.Scan(ctx, job.Category, job.RemotePath, rng, func(b scan.Batch) error {
		for _, e := range b.Entries {
			c, reason, err := p.evaluate(b.Dir, e, b.When)
			remote := remoteJoin(b.Dir, e.Name)

			switch {
			case err != nil:
				// 구현·계약 결함. 이 파일만 실패로 세고 계속한다.
				r.logf("실패 원격=%s 원인=%v", remote, err)
				results <- outcome{remote: remote, err: err}

				continue

			case reason != "":
				results <- outcome{remote: remote, reason: reason}

				continue
			}

			if ok, winner := dests.claim(c); !ok {
				r.logf("목적지중복 원격=%s 로컬=%s 먼저배정=%s", c.RemotePath, c.LocalPath, winner)
				results <- outcome{remote: c.RemotePath, local: c.LocalPath, reason: ReasonDuplicate}

				continue
			}

			// 취소되면 새 수신을 넘기지 않는다. 이미 넘긴 것은 worker 가
			// 착수 전에 한 번 더 확인한다.
			select {
			case cands <- c:
			case <-ctx.Done():
				results <- outcome{remote: c.RemotePath, local: c.LocalPath, notStarted: true}

				return ctx.Err()
			}
		}

		return nil
	})

	close(cands)
	wg.Wait()
	close(results)
	<-aggDone

	cr.Scan = scanRes
	cr.Duration = time.Since(start)

	if scanErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return cr, fmt.Errorf("download: [%s] canceled: %w", job.Category, ctxErr)
		}

		return cr, fmt.Errorf("download: [%s] scan: %w", job.Category, scanErr)
	}

	// Scan 이 끝난 뒤 취소되면 worker 가 남은 후보를 착수하지 않았을 수
	// 있다 (NotStarted). 그 회차도 취소로 돌려준다.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return cr, fmt.Errorf("download: [%s] canceled: %w", job.Category, ctxErr)
	}

	return cr, nil
}

func (r *Runner) logf(format string, args ...any) {
	r.logger().Printf("[DOWNLOAD] "+format, args...)
}

// fetch 는 후보 하나를 받는다 (§6.2).
//
//	착수 전 ctx 확인                    → 취소면 NotStarted
//	최종 경로 Stat → decideTarget       → 기존·충돌·비정상이면 끝
//	EnsureDir(목적지 폴더)
//	DownloadPart(원격, 최종+".part")
//	.part 크기 == 원격 Entry 크기       → 다르면 실패 (D6)
//	Rename 직전 최종 경로 재확인         → 생겼으면 .part 삭제 후 그 사유
//	Rename(.part, 최종)
//
// .part 를 만든 뒤의 모든 실패는 .part 삭제를 시도한다 (§6.3). 삭제 실패는
// 원래 실패 원인에 덧붙인다.
func (r *Runner) fetch(ctx context.Context, c Candidate) outcome {
	base := outcome{remote: c.RemotePath, local: c.LocalPath}

	if ctx.Err() != nil {
		base.notStarted = true
		return base
	}

	fail := func(err error) outcome {
		base.err = err
		r.logf("실패 원격=%s 로컬=%s 원인=%v", c.RemotePath, c.LocalPath, err)

		return base
	}

	// 1. 최종 경로 관측. .part 를 만들기 전이므로 정리할 것이 없다.
	if o, done := r.judgeTarget(ctx, c, base, "stat"); done {
		if o.err != nil {
			return fail(o.err)
		}

		return o
	}

	if err := r.Local.EnsureDir(ctx, c.LocalDir); err != nil {
		return fail(fmt.Errorf("ensure dir: %w", err))
	}

	part := c.PartPath()

	// 2. 이후 실패는 .part 를 지운다.
	failClean := func(err error) outcome {
		return fail(r.joinCleanup(ctx, part, err))
	}

	if err := r.Remote.DownloadPart(ctx, c.RemotePath, part); err != nil {
		return failClean(fmt.Errorf("download: %w", err))
	}

	size, err := r.Local.Size(ctx, part)
	if err != nil {
		return failClean(fmt.Errorf("size of .part: %w", err))
	}

	if size != c.Size {
		return failClean(fmt.Errorf(
			"size mismatch: .part=%d remote=%d", size, c.Size,
		))
	}

	// 3. Rename 직전 재확인. 전송 중 외부에서 최종 파일이 생겼으면 덮어쓰지
	//    않는다. .part 는 지운다 — 남겨 두면 다음 회차에 기존 판정만 되고
	//    영원히 쓰이지 않는 쓰레기가 된다.
	if o, done := r.judgeTarget(ctx, c, base, "recheck"); done {
		if o.err != nil {
			return failClean(o.err)
		}

		if cerr := r.cleanup(ctx, part); cerr != nil {
			r.logf("임시파일정리실패 로컬=%s 원인=%v", part, cerr)

			// 최종 파일은 외부에서 생긴 그대로 보존하지만, .part 가 남은
			// 회차를 성공으로 보고하면 종료 코드와 실패 상세에서 장애가
			// 사라진다. 원래 목적지 판정 사유도 오류에 남긴다.
			return fail(fmt.Errorf(
				"cleanup .part after destination became %s: %w",
				o.reason,
				cerr,
			))
		}

		return o
	}

	if err := r.Local.Rename(ctx, part, c.LocalPath); err != nil {
		return failClean(fmt.Errorf("rename: %w", err))
	}

	base.downloaded = true

	return base
}

// judgeTarget 은 최종 경로를 관측해 받을지 판정한다. done=true 면 받지 않는다
// (기존·충돌·비정상·Stat 오류). 충돌·비정상은 운영자가 봐야 하므로 파일별로
// 로그를 남긴다. Stat 오류는 로그 없이 o.err 로 돌려준다 — 실패 로그는
// 호출자의 fail 한 곳에서 남긴다(정리 오류까지 붙은 최종 원인으로).
func (r *Runner) judgeTarget(
	ctx context.Context,
	c Candidate,
	base outcome,
	phase string,
) (outcome, bool) {
	fi, statErr := r.Local.Stat(ctx, c.LocalPath)

	st, err := decideTarget(c.Size, fi, statErr)
	if err != nil {
		base.err = fmt.Errorf("%s destination: %w", phase, err)

		return base, true
	}

	if st == targetAbsent {
		return base, false
	}

	base.reason = st.reason()

	switch st {
	case targetConflict:
		r.logf("충돌 원격=%s 로컬=%s 원격크기=%d 로컬크기=%d (덮어쓰지 않음)",
			c.RemotePath, c.LocalPath, c.Size, fi.Size())

	case targetIrregular:
		r.logf("비정상대상 원격=%s 로컬=%s 종류=%v", c.RemotePath, c.LocalPath, fi.Mode().Type())
	}

	return base, true
}

// cleanup 은 .part 를 지운다. 회차가 취소됐어도 시도하되 상한을 둔다 (§6.3).
func (r *Runner) cleanup(ctx context.Context, part string) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), r.cleanupTimeout())
	defer cancel()

	return r.Local.Remove(cctx, part)
}

// joinCleanup 은 .part 를 지우고, 실패하면 그 원인을 cause 뒤에 덧붙인다.
// 주 원인이 앞이다 — 운영자가 먼저 봐야 하는 것은 수신이 왜 실패했는가다.
func (r *Runner) joinCleanup(ctx context.Context, part string, cause error) error {
	if cerr := r.cleanup(ctx, part); cerr != nil {
		return errors.Join(cause, fmt.Errorf("cleanup %s: %w", part, cerr))
	}

	return cause
}
