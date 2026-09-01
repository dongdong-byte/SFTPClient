package put

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
)

const (
	// putPartSuffix 는 PUT 전송 중 사용하는 임시 파일 접미어다.
	//
	// 현재 domain.IsPartFile 도 동일한 ".part" 규칙을 판정한다.
	// domain 에 공개된 접미어 상수가 생기면 그 값을 공유하는 편이 좋지만,
	// IsPartFile 내부 구현을 역으로 참조할 수는 없으므로 이 단계에서는
	// 전송 경로 생성 지점을 여기 한 곳으로 제한한다.
	putPartSuffix = ".part"

	// cleanupTimeout 은 실패 정리의 원격 작업(.part 삭제) 상한이다.
	//
	// 정리는 취소된 ctx 에서도 수행해야 하므로 context.WithoutCancel 을
	// 쓰는데, 그것은 취소 신호뿐 아니라 deadline 까지 함께 지운다.
	// 상한을 다시 씌우지 않으면 원격이 응답하지 않을 때 종료되지 않는
	// 프로세스가 된다. Ctrl+C 나 스케줄러 kill 이 먹지 않는 상태다.
	cleanupTimeout = 30 * time.Second

	// ledgerTimeout 은 FailPut / FinishPut 의 Ledger 쓰기 상한이다.
	//
	// cleanupTimeout 과 예산을 공유하지 않는다. 하나의 context 를 쓰면
	// 원격 Remove 가 30초를 다 소모했을 때 FailPut 이 남은 시간 0으로
	// 즉시 DeadlineExceeded 가 되어 IN_PROGRESS 가 남는다.
	// 그것이 바로 이 정리 절차가 막으려던 상태다.
	//
	// FinishPut 도 같은 상한을 쓴다. 원격 검증이 끝난 뒤 취소된 ctx 를
	// 그대로 넘기면 VERIFIED 를 못 남기고 IN_PROGRESS 가 된다.
	ledgerTimeout = 10 * time.Second
)

// errPreflightStale 은 Scan 당시와 로컬 입력이 달라 BeginPut 없이
// 이 revision 의 PENDING 을 FAILED 로 접어야 하는 경우다.
//
// attempts 는 BeginPut 에서만 오르므로 retry budget 은 소모하지 않는다.
var errPreflightStale = errors.New("put: local input no longer matches scan")

// TransferReport 는 전송 단계의 관측이다.
type TransferReport struct {
	// Attempted 는 BeginPut 이 실제로 성공하여 attempts 가 증가한 수다.
	//
	// 단순히 BeginPut 호출 횟수가 아니다.
	// preflight 실패나 ErrNotCandidate 는 포함하지 않는다.
	Attempted int

	// Verified 는 최종 이름의 Size 검증까지 통과하고
	// FinishPut 으로 VERIFIED 상태가 된 수다.
	Verified int

	// Failed 는 BeginPut 성공 이후의 전송 작업이 실패하여
	// FailPut 으로 FAILED 상태가 된 수다.
	Failed int

	// NotCandidate 는 BeginPut 이 ErrNotCandidate 로 거른 수다.
	//
	// 다른 실행/Worker 가 이미 선점했거나 현재 상태가 후보가 아니거나
	// 누적 attempts 상한에 도달한 경우이며 전송 오류로 보지 않는다.
	NotCandidate int

	// SkippedPreflight 는 BeginPut 이전에 로컬 원본 상태가 달라져
	// 이번 실행에서 건너뛴 수다.
	//
	// 전송 실패가 아니므로 attempts 를 소모하지 않는다.
	//
	//	파일 없음 · Stat 오류
	//	  PENDING 을 유지한다. 같은 revision 이 다음 스캔에 다시
	//	  나오면 고아 PENDING 으로 재개한다.
	//
	//	Size 변경 · 비정규 파일 (errPreflightStale)
	//	  이 revision 의 PENDING 을 FailPut 한다.
	//	  다음 회차에 파일이 바뀌어 있으면 Upsert 가 새 revision 을
	//	  만들고, 옛 revision PENDING 을 남겨 두지 않는다.
	//
	// 이 값이 상시 0 이 아니면 Scan 과 전송 사이의 창에서 로컬 파일이
	// 실제로 바뀌고 있다는 뜻이므로 그 자체가 운영 신호다.
	SkippedPreflight int

	Elapsed time.Duration
}

// uploadResult 는 uploadOne 이 어디까지 진행했는지를 돌려준다.
//
// 오류가 있어도 이 값은 유효하다. 실패 정리를 어느 단계 기준으로 할지가
// 여기에 달려 있기 때문이다.
type uploadResult struct {
	// Renamed 는 .part → 최종 이름 전환이 성공했는지다.
	//
	// 실패 정리의 분기 기준이다.
	//
	//	Rename 이전 실패 → partPath 에 불완전 파일이 남아 있으므로 지운다.
	//	Rename 이후 실패 → 최종 이름의 파일은 이미 .part 단계에서 Size
	//	                   검증을 통과한 온전한 데이터다. 지우면 멀쩡한
	//	                   파일을 지우게 되고, 그 사이 수신측이 가져갔다면
	//	                   파일이 사라진 것으로 보인다.
	Renamed bool

	// SentAt 은 Rename 이 성공한 시각이다. Renamed 가 false 면 zero 다.
	//
	// put_ledger 의 sent_at 과 transfer_verified_at 을 나누기 위해 받는다.
	// 두 시각 사이에는 최종 Size 검증을 위한 원격 왕복이 들어 있어
	// 실제로 수십~수백 ms 차이가 난다. 같은 값을 넣으면 "보냈지만 아직
	// 검증되지 않은 구간" 이라는 CONCEPT 4.6 의 구분이 데이터에 남지 않는다.
	SentAt    time.Time
	FinalSize int64 // 원격에서 관측한 최종 크기
}

// directoryBatch 는 워커 하나가 통째로 담당하는 작업 단위다.
//
// 낱개 Candidate 를 워커 채널에 흘리지 않고 디렉터리로 묶는 이유는
// "디렉터리 전담" 전제 때문이다. 같은 원격 디렉터리의 파일이 워커 둘로
// 갈라지면 EnsureDir 캐시가 워커 간 공유되어야 하고(락 필요), 디렉터리
// 내부의 정렬 순서도 무너진다. 한 디렉터리를 한 워커가 통째로 잡으면
// 캐시는 워커 로컬로 충분하고 내부 순서도 보존된다.
type directoryBatch struct {
	Category  domain.Category
	RemoteDir string

	// Candidates 는 이 디렉터리에 속한 후보들이며, 정렬된 kept 의
	// 상대 순서를 그대로 유지한다 (groupByDirectory 가 순서대로 append).
	Candidates []Candidate
}

// dirKey 는 그룹핑 조회표의 키다. 배치 순서를 만드는 데는 쓰지 않는다.
type dirKey struct {
	category domain.Category
	dir      string
}

// Transfer 는 후보를 원격 디렉터리 단위로 나누어 병렬 전송한다.
//
// 작업 단위와 동시 상한은 다른 것이다.
//
//	작업 단위(분배) = 원격 디렉터리. 한 디렉터리를 한 워커가 전담한다.
//	동시 상한       = MaxWorkers. 디렉터리가 그보다 많아도 워커는 이
//	                  수만큼 뜨고, 큐에서 하나씩 꺼내 처리한다.
//	                  디렉터리가 더 적으면(Daily 단독=2) 그만큼만 뜬다.
//
// 디렉터리당 워커를 무제한 생성하지 않는다. Deep Hourly 는 7×24=168
// 디렉터리라 그대로 두면 168 고루틴이 단일 SFTP 세션을 동시에 두드려
// 수신측 동시 연결 제한을 넘기고, 처리량은 회선에서 병목이라 이득도
// 없이 메모리만 압박한다 (2026-09-01 확정).
//
// 파일 하나의 확정 순서(preflight → BeginPut → 전송 → 검증 → FinishPut)
// 와 상태 전이 규칙은 단일 전송 시절과 동일하다. 병렬화는 그 단위를
// 디렉터리별로 나눠 돌릴 뿐, 파일 단위 로직을 바꾸지 않는다.
//
// 오류는 두 층위다 (단일 전송 철학의 병렬화).
//
//	파일 전송 실패 → FailPut 으로 FAILED 기록 → 해당 워커는 다음 파일 계속.
//	fatal(Ledger 오류·ctx 취소·조립 오류) → 첫 오류가 pool 전체를 취소하고
//	  return err. 나머지 워커는 진행 중 파일을 접은 뒤 종료한다.
//
// errgroup 을 쓰지 않는다. vendor 에 golang.org/x/sync 가 없고(실측),
// 계약은 "첫 fatal 이 전체를 취소한다" 이지 특정 라이브러리가 아니다.
// sync.WaitGroup + context.WithCancel + mutex 로 직접 조립한다.
//
// Report 는 워커별 로컬 TransferReport 에 쌓고 전 워커 종료 후 합산한다.
// 공유 report + mutex 를 두지 않아 race 여지를 원천 제거한다.
//
// 반환 error 는 파일 한 건의 실패가 아니라 실행 자체를 중단해야 하는
// 오류다. Elapsed 는 어느 경로로 빠져나가든 defer 가 채운다.
func (r *Runner) Transfer(
	ctx context.Context,
	up Uploader,
	jobs []CategoryJob,
	cands []Candidate,
) (rep TransferReport, err error) {
	started := r.now()

	defer func() {
		rep.Elapsed = r.now().Sub(started)
	}()

	if up == nil {
		return rep, fmt.Errorf("put: uploader is required")
	}

	// 후보를 디렉터리 단위로 묶는다. jobs 검증(중복 category·nil
	// RemotePath·후보 category 누락)도 여기서 함께 한다 — 전송이 실제로
	// 쓰는 입력이기 때문이다.
	batches, err := r.groupByDirectory(jobs, cands)
	if err != nil {
		return rep, err
	}

	if len(batches) == 0 {
		// 후보 0건도 요약 한 줄은 남긴다. "재실행 시 전송 0건" 은
		// 중복 방지의 증거이며(시연 ④), 라인이 아예 없으면 "전송
		// 단계가 돌지 않았다" 와 구분되지 않는다.
		r.logf(
			"[XFER] attempted=0 verified=0 failed=0 "+
				"not_candidate=0 skipped_preflight=0 workers=0 dirs=0 elapsed=%s",
			r.now().Sub(started).Round(time.Millisecond),
		)

		return rep, nil
	}

	// 워커 수는 MaxWorkers 로 상한하되, 디렉터리보다 많이 띄우지 않는다.
	// MaxWorkers <= 0 은 validate 가 이미 막지만, DB 를 직접 여는
	// 호출자를 위해 1 로 보정한다 (BeginPut 의 maxRetries 가드와 같은 이유).
	workers := r.Opts.MaxWorkers
	if workers < 1 {
		workers = 1
	}
	if workers > len(batches) {
		workers = len(batches)
	}

	// 첫 fatal 이 나면 이 ctx 를 취소해 나머지 워커·피더를 접는다.
	poolCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		errMu    sync.Mutex
		firstErr error
	)

	// setErr 는 첫 fatal 만 붙잡고 pool 을 취소한다. 이후 오류는 버린다 —
	// 원인은 첫 번째이고, 뒤따르는 것들은 대개 취소의 파생이다.
	setErr := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
			cancel()
		}
		errMu.Unlock()
	}

	// 워커별 로컬 리포트. 인덱스로 분리하여 워커는 자기 것만 쓴다.
	// 합산은 wg.Wait 이후이므로 happens-before 가 성립해 race 가 없다.
	reports := make([]TransferReport, workers)

	batchCh := make(chan directoryBatch)

	for w := 0; w < workers; w++ {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()

			local := &reports[idx]

			// EnsureDir 캐시는 워커 로컬이다. 디렉터리 전담 구조라
			// 워커 간 캐시 키가 겹치지 않으므로 공유 map + mutex 가
			// 필요 없다. 서로 다른 워커가 공통 부모 디렉터리를 동시에
			// mkdir 하는 경우는 캐시 문제가 아니라 원격 mkdir 멱등성
			// 문제이며, EnsureDir 계약("이미 있음 = 성공")이 흡수한다.
			ensured := make(map[string]struct{})

			for {
				select {
				case <-poolCtx.Done():
					return

				case b, ok := <-batchCh:
					if !ok {
						return
					}

					if bErr := r.transferBatch(
						poolCtx, up, b, local, ensured,
					); bErr != nil {
						setErr(bErr)
						return
					}
				}
			}
		}(w)
	}

	// 피더는 별도 고루틴이다. 취소되면 남은 배치를 흘리지 않고 채널을
	// 닫아, 워커들이 채널 닫힘 또는 Done 으로 빠져나가게 한다.
	go func() {
		defer close(batchCh)

		for _, b := range batches {
			select {
			case <-poolCtx.Done():
				return
			case batchCh <- b:
			}
		}
	}()

	wg.Wait()

	for i := range reports {
		rep.Attempted += reports[i].Attempted
		rep.Verified += reports[i].Verified
		rep.Failed += reports[i].Failed
		rep.NotCandidate += reports[i].NotCandidate
		rep.SkippedPreflight += reports[i].SkippedPreflight
	}

	// fatal 이 있으면 그것을 우선 올린다. 없더라도 부모 ctx 가 취소된
	// 경우(피더/유휴 중 취소 등 워커가 setErr 를 못 남긴 경로)를 위해
	// ctx.Err 을 backstop 으로 확인한다 — 단일 전송의 취소 반환과 동일.
	if firstErr != nil {
		return rep, firstErr
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return rep, ctxErr
	}

	r.logf(
		"[XFER] attempted=%d verified=%d failed=%d "+
			"not_candidate=%d skipped_preflight=%d workers=%d dirs=%d elapsed=%s",
		rep.Attempted,
		rep.Verified,
		rep.Failed,
		rep.NotCandidate,
		rep.SkippedPreflight,
		workers,
		len(batches),
		r.now().Sub(started).Round(time.Millisecond),
	)

	return rep, nil
}

// groupByDirectory 는 정렬된 후보를 원격 디렉터리 단위로 묶는다.
//
// ★ 배치 순서는 정렬된 cands 를 순서대로 순회하며 first-seen 으로
// 만든다. dirKey→인덱스 맵은 "이 디렉터리가 이미 어느 배치에 있나" 를
// O(1) 로 찾는 조회용일 뿐, 순회하지 않는다. map 순회로 배치 순서를
// 만들면 Go map 순회의 무작위성 때문에 When→file_name 정렬이 조용히
// 깨진다 (가끔만 실패하는 최악 부류). 같은 디렉터리 파일이 정렬상
// 연속이 아니어도(정렬 키가 file_name 우선이라 흩어질 수 있다) 이
// 방식은 각 배치 내부 순서를 정렬 순서 그대로 보존한다.
//
// jobs 검증도 여기서 한다. Transfer 는 Run 과 별개 진입점이라
// checkInput 을 재사용할 수 없고(Scanner 를 요구), 전송이 실제로 쓰는
// 입력만 검사한다.
func (r *Runner) groupByDirectory(
	jobs []CategoryJob,
	cands []Candidate,
) ([]directoryBatch, error) {
	remoteJobs := make(map[domain.Category]*CategoryJob, len(jobs))

	for i := range jobs {
		if _, dup := remoteJobs[jobs[i].Category]; dup {
			return nil, fmt.Errorf(
				"put: duplicate category job %s",
				jobs[i].Category,
			)
		}

		if jobs[i].RemotePath == nil {
			return nil, fmt.Errorf(
				"put: category %s has nil RemotePath template",
				jobs[i].Category,
			)
		}

		remoteJobs[jobs[i].Category] = &jobs[i]
	}

	var batches []directoryBatch
	index := make(map[dirKey]int, len(cands))

	for _, c := range cands {
		job, ok := remoteJobs[c.Key.Category]
		if !ok {
			return nil, fmt.Errorf(
				"put: no job for candidate category %s",
				c.Key.Category,
			)
		}

		remoteDir := job.RemotePath.Expand(c.When)
		k := dirKey{category: c.Key.Category, dir: remoteDir}

		i, seen := index[k]
		if !seen {
			batches = append(batches, directoryBatch{
				Category:  c.Key.Category,
				RemoteDir: remoteDir,
			})
			i = len(batches) - 1
			index[k] = i
		}

		batches[i].Candidates = append(batches[i].Candidates, c)
	}

	return batches, nil
}

// transferBatch 는 디렉터리 하나의 후보를 정렬 순서대로 순차 전송한다.
//
// 워커 하나가 이 함수를 호출하며, ensured 와 rep 은 그 워커의 로컬
// 소유물이다. 반환 error 는 fatal(실행 중단)뿐이다 — 파일 한 건의
// 전송 실패는 transferOne 안에서 FAILED 로 접고 nil 을 돌려준다.
func (r *Runner) transferBatch(
	ctx context.Context,
	up Uploader,
	b directoryBatch,
	rep *TransferReport,
	ensured map[string]struct{},
) error {
	for _, c := range b.Candidates {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		if err := r.transferOne(ctx, up, b.RemoteDir, c, rep, ensured); err != nil {
			return err
		}
	}

	return nil
}

// transferOne 은 파일 하나의 확정 순서를 실행한다.
//
//	preflight → BeginPut → EnsureDir → uploadOne → FinishPut
//
// 반환 error 는 fatal(실행 중단)뿐이다. preflight skip, ErrNotCandidate,
// 전송 실패(FAILED 로 접음)는 rep 에 세고 nil 을 돌려준다. 파일 단위
// 로직은 단일 전송 시절과 동일하며, 호출자(워커)가 remoteDir 을 배치에서
// 넘겨주는 점만 다르다.
func (r *Runner) transferOne(
	ctx context.Context,
	up Uploader,
	remoteDir string,
	c Candidate,
	rep *TransferReport,
	ensured map[string]struct{},
) error {
	// ------------------------------------------------------------
	// PRE-FLIGHT
	// ------------------------------------------------------------
	//
	// Scan 과 전송 사이에 로컬 원본이 사라지거나 크기가 변할 수 있다.
	// BeginPut 이후에 발견하면 attempts 가 올라 "실제 PUT 착수 횟수"
	// 의미가 깨지므로 BeginPut 전에 다시 확인한다. 실패해도 실행을
	// 중단하지 않고 이 파일만 건너뛴다.
	if pfErr := preflightLocal(c); pfErr != nil {
		rep.SkippedPreflight++

		// IsRetry(기존 FAILED 행)는 stale 이어도 장부 정리가 필요 없다.
		// 행이 이미 FAILED 고 preflight 는 attempts 를 소모하지 않는다.
		// FailPut 을 부르면 WHERE 가드에 걸려 전이 위반 오류가 되고,
		// 파일 하나의 변화가 회차를 중단시킨다 (2026-08-31 교차 리뷰).
		//
		// 불변식: 이 시점에 put_ledger 행이 FAILED 인 후보는 IsRetry
		// 뿐이다 (runner.go 후보 필터). 필터에 경로를 추가할 때 이
		// 문장을 먼저 읽어라.
		if errors.Is(pfErr, errPreflightStale) && !c.IsRetry {
			if failErr := r.failPending(ctx, c, pfErr); failErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return errors.Join(ctxErr, failErr)
				}

				return failErr
			}
		}

		r.logf(
			"[XFER][SKIP] %s rev=%d: %v",
			c.Key.FileName,
			c.Key.Revision,
			pfErr,
		)

		return nil
	}

	// 원격에는 스캔이 관측한 원본 파일명의 대소문자를 보존한다.
	// Key.FileName 은 NormalizeName 된 값이라 실제 이름 출력에 쓰지 않는다.
	// filepath.Base 는 로컬 경로 조작이므로 로컬 OS 규칙이 맞고,
	// 원격 경로 결합에만 up.Join 을 쓴다.
	base := filepath.Base(c.LocalPath)
	finalPath := up.Join(remoteDir, base)
	partPath := finalPath + putPartSuffix

	// ------------------------------------------------------------
	// 실제 PUT 착수 — 여기서부터 attempts 를 소모한다.
	// ------------------------------------------------------------
	if beginErr := r.DB.BeginPut(
		ctx,
		c.Key,
		finalPath,
		partPath,
		c.Size,
		r.Opts.MaxRetries,
	); beginErr != nil {
		if errors.Is(beginErr, ledger.ErrNotCandidate) {
			// 후보 재확인 시점과 BeginPut 사이에 상태가 바뀌거나
			// attempts 상한에 도달했다면 skip 이 정상 동작이다.
			rep.NotCandidate++
			return nil
		}

		// Ledger 가 상태 전이를 못 했다면 이후 원격 I/O 를 할 근거가
		// 없으므로 실행을 중단한다.
		return fmt.Errorf("begin put %q: %w", c.Key.FileName, beginErr)
	}

	rep.Attempted++

	// EnsureDir 실패도 전송 실패로 취급한다. BeginPut 이 이미 성공해
	// attempts 를 소모했고, 권한·경로 문제라면 재시도해도 같은 결과라
	// MaxRetries 소진이 옳은 귀결이다.
	var res uploadResult

	transferErr := r.ensureRemoteDir(ctx, up, remoteDir, ensured)
	if transferErr == nil {
		res, transferErr = r.uploadOne(ctx, up, c, partPath, finalPath)
	}

	if transferErr != nil {
		if failErr := r.failOne(ctx, up, c, partPath, res, transferErr); failErr != nil {
			// FAILED 확정 실패면 IN_PROGRESS 가 남는다. 취소 때문이면
			// 그 사실도 함께 올린다.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Join(ctxErr, failErr)
			}

			return failErr
		}

		rep.Failed++

		// 취소는 개별 파일을 FAILED 로 접은 뒤 실행 전체를 중단한다.
		// (워커가 이 오류를 setErr 로 올려 pool 을 취소한다)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return nil
	}

	// uploadOne 성공 = .part 업로드 → .part Size 일치 → Rename →
	// final Size 일치. 이 시점에만 VERIFIED 로 확정한다.
	//
	// 원격은 이미 맞다. 취소된 ctx 로 FinishPut 하면 IN_PROGRESS 가
	// 남아 recovery 가 정상 파일을 실패로 오인하므로 WithoutCancel 로
	// 격리하고 timeout 만 다시 씌운다.
	finishCtx, finishCancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		ledgerTimeout,
	)
	finishErr := r.DB.FinishPut(
		finishCtx,
		c.Key,
		res.FinalSize,
		res.SentAt,
		r.now().UTC(),
	)
	finishCancel()

	if finishErr != nil {
		// 최종 파일은 이미 올바르게 존재한다. FinishPut 실패를 FailPut
		// 으로 덮지 않는다. Ledger 기록 실패는 실행 중단 사유이며,
		// 이 경우 남은 IN_PROGRESS 는 다음 startup recovery 가 다룬다.
		return fmt.Errorf("finish put %q: %w", c.Key.FileName, finishErr)
	}

	rep.Verified++

	return nil
}
func (r *Runner) ensureRemoteDir(
	ctx context.Context,
	up Uploader,
	dir string,
	ensured map[string]struct{},
) error {
	if _, ok := ensured[dir]; ok {
		return nil
	}

	if err := up.EnsureDir(ctx, dir); err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}

	ensured[dir] = struct{}{}

	return nil
}

// failPending 은 BeginPut 전에 이 revision 의 PENDING 을 FAILED 로 접는다.
//
// attempts 는 증가하지 않는다. Size 가 바뀌어 다음 Upsert 가 새
// revision 을 만들 때 옛 PENDING 이 남아 운영 조회를 속이지 않게 한다.
func (r *Runner) failPending(
	ctx context.Context,
	c Candidate,
	cause error,
) error {
	failCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		ledgerTimeout,
	)
	defer cancel()

	if err := r.DB.FailPut(failCtx, c.Key, flattenErr(cause)); err != nil {
		return fmt.Errorf("fail put %q: %w", c.Key.FileName, err)
	}

	return nil
}

// failOne 은 전송 실패 한 건의 잔여물을 정리하고 FAILED 로 확정한다.
//
// 반환 error 는 FailPut 자체가 실패한 경우뿐이며, 그것은 실행 중단
// 사유다. .part 정리 실패는 여기서 오류로 올리지 않고 사유 문자열에
// 합쳐 기록한다 — cleanup 실패 때문에 FAILED 확정을 생략하면
// IN_PROGRESS 가 남아 상태가 더 나빠진다.
func (r *Runner) failOne(
	ctx context.Context,
	up Uploader,
	c Candidate,
	partPath string,
	res uploadResult,
	transferErr error,
) error {
	cause := transferErr

	// Rename 이전에 실패한 경우에만 .part 를 정리한다.
	//
	// 남기면 안 되는 이유:
	// FAILED 파일이 이후 Scan 범위 밖으로 밀려나면 startup IN_PROGRESS
	// recovery 대상도 아니고 재시도도 되지 않는다. 그러면 .part 가
	// 원격 저장소에 영구 누적된다. (설계안 9.3)
	//
	// Rename 이후라면 partPath 는 이미 없고 최종 이름의 파일이 온전하다.
	// 계약상 Remove 는 없는 대상에도 성공하므로 호출해도 무해하지만,
	// 원격 왕복을 낭비하고 무엇보다 이 분기 결정이 코드에서 사라진다.
	if !res.Renamed {
		// 취소 신호는 제거하되 무한 대기는 막는다.
		// WithoutCancel 은 deadline 도 함께 지우므로 다시 씌운다.
		removeCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			cleanupTimeout,
		)

		removeErr := up.Remove(removeCtx, partPath)
		cancel()

		if removeErr != nil {
			// 문자열로 이어붙이지 않고 error 로 결합한다.
			// 나중에 특정 원인을 errors.Is 로 판별할 여지를 남긴다.
			cause = errors.Join(
				cause,
				fmt.Errorf("cleanup .part: %w", removeErr),
			)
		}
	}

	r.logf(
		"[XFER][FAIL] %s rev=%d: %s",
		c.Key.FileName,
		c.Key.Revision,
		flattenErr(cause),
	)

	if err := r.failPending(ctx, c, cause); err != nil {
		return err
	}

	return nil
}

// flattenErr 는 errors.Join 이 개행으로 잇는 메시지를 한 줄로 바꾼다.
//
// put_ledger.error 컬럼과 로그 한 줄에 넣기 위한 표현 변환일 뿐이다.
// 원인 판별은 error 값으로 하고 문자열화는 저장 직전에만 수행한다.
func flattenErr(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", "; ")
}

// preflightLocal 은 Scan 당시 Candidate 와 실제 전송 직전의 로컬 파일이
// 여전히 같은 입력인지 확인한다.
//
// 이 검사는 BeginPut 보다 반드시 먼저 수행한다.
//
// 이유:
//
//	attempts = 실제 PUT 전송 착수 횟수
//
// 라는 Ledger 의미를 보존해야 하기 때문이다.
//
//	없음 / Stat 오류
//	  일반 error. PENDING 유지. 같은 revision 재스캔 시 재개.
//
//	Size 변경 / 비정규 파일
//	  errPreflightStale. PENDING 을 FailPut. budget 미소모.
//
// 현재 검증은 Size 기반이다.
// mtime 까지 다시 검증할지는 별도 정책이며 이번 단일 PUT 경로에서는
// Scan 이 확정한 Size 를 실제 전송 바이트 수의 기준으로 사용한다.
func preflightLocal(c Candidate) error {
	info, err := os.Stat(c.LocalPath)
	if err != nil {
		return err
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: not a regular file", errPreflightStale)
	}

	if info.Size() != c.Size {
		return fmt.Errorf(
			"%w: size changed since scan: scanned=%d current=%d",
			errPreflightStale,
			c.Size,
			info.Size(),
		)
	}

	return nil
}

// uploadOne 은 파일 하나의 실제 원격 작업과 Size 기반 Transfer
// Verification 을 수행한다.
//
// Ledger 상태는 전혀 만지지 않는다.
// BeginPut / FinishPut / FailPut 은 Transfer 의 책임이다.
// 잔여 .part 정리도 하지 않는다. 실패 정리는 failOne 한 곳에서만 한다.
//
// 원격 디렉터리 준비는 호출자가 이미 마쳤다고 전제한다.
// 같은 디렉터리에 수백 개 파일이 들어오므로 캐시가 필요한데,
// 그 캐시는 실행 단위(Transfer)에 있어야 하기 때문이다.
//
// 반환하는 uploadResult 는 오류가 있어도 유효하다.
// Renamed 가 실패 정리의 분기 기준이 된다.
func (r *Runner) uploadOne(
	ctx context.Context,
	up Uploader,
	c Candidate,
	partPath string,
	finalPath string,
) (uploadResult, error) {
	var res uploadResult

	if err := up.UploadPart(ctx, c.LocalPath, partPath); err != nil {
		return res, fmt.Errorf("upload .part: %w", err)
	}

	// ------------------------------------------------------------
	// 1차 Transfer Verification
	// ------------------------------------------------------------
	//
	// UploadPart 가 성공을 반환했다는 사실만 믿지 않는다.
	// 기존 WinSCP 스크립트가 한 건도 올리지 못하고도 종료코드 0 을
	// 돌려주던 것이 이 검증을 두는 실증 근거다. (GUIDELINES 9.4)
	partSize, err := up.Size(ctx, partPath)
	if err != nil {
		return res, fmt.Errorf("size .part: %w", err)
	}

	if partSize != c.Size {
		return res, fmt.Errorf(
			".part size mismatch: local=%d remote=%d",
			c.Size,
			partSize,
		)
	}

	// .part 검증이 끝난 뒤에만 최종 이름으로 전환한다.
	//
	// finalPath 에 이전 revision 의 파일이 이미 존재할 수 있다.
	// 이것은 예외가 아니라 정상 경로이며, 덮어쓰기는 Uploader.Rename 이
	// 계약으로 보장한다. 원자적 교체까지는 계약하지 않는다.
	if err := up.Rename(ctx, partPath, finalPath); err != nil {
		return res, fmt.Errorf("rename: %w", err)
	}

	// 이 지점 이후의 실패는 .part 를 지우지 않는다.
	// 최종 이름의 파일은 이미 Size 검증을 통과한 온전한 데이터다.
	res.Renamed = true
	res.SentAt = r.now().UTC()

	// ------------------------------------------------------------
	// 2차 / 최종 Transfer Verification
	// ------------------------------------------------------------
	//
	// VERIFIED 의 근거는 최종 이름의 파일이 실제로 존재하고
	// 그 Size 가 원본과 일치한다는 사실이다. (설계안 8.1)
	finalSize, err := up.Size(ctx, finalPath)
	if err != nil {
		return res, fmt.Errorf("size final: %w", err)
	}

	if finalSize != c.Size {
		return res, fmt.Errorf(
			"final size mismatch: local=%d remote=%d",
			c.Size,
			finalSize,
		)
	}

	res.FinalSize = finalSize
	return res, nil
}
