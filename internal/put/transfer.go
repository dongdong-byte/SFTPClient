package put

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	SentAt time.Time
}

// Transfer 는 후보를 순차로 전송한다.
//
// Worker Pool 이전의 단일 전송 경로이며, 이후 이 함수의 파일별 실행
// 단위를 Worker 가 소비하더라도 상태 전이 순서는 유지해야 한다.
//
// 파일 하나의 확정 순서:
//
//	[preflight]
//	  로컬 파일 존재 / regular / Size 불변 확인
//
//	[실제 PUT 착수]
//	  BeginPut
//	    → IN_PROGRESS
//	    → attempts++
//	    → remote_path / part_path / local_size 기록
//
//	[전송]
//	  EnsureDir (디렉터리 단위. 같은 dir 재호출은 캐시가 걷어낸다)
//	  → UploadPart
//	  → .part Size == local_size
//	  → Rename(.part → final)
//	  → final Size == local_size
//
//	[확정]
//	  FinishPut
//	  → VERIFIED
//
// preflight 를 BeginPut 보다 먼저 두는 이유:
//
// attempts 의 확정 의미는 "실제 PUT 전송 착수 횟수" 다.
// Scan 이후 로컬 파일이 사라졌거나 크기가 변한 것은 SFTP 전송 실패가
// 아니라 로컬 입력 문제이므로 retry budget 을 소모하면 안 된다.
//
// 다만 그것이 "실행을 중단한다" 는 뜻은 아니다. 해당 파일만 건너뛴다.
// 중단시키면 후보 정렬이 매 실행 동일하므로 같은 파일이 같은 위치에서
// 계속 막고, attempts 가 오르지 않아 MaxRetries 소진으로 빠지지도 않아
// 그 뒤 후보 전체가 영구히 전송되지 않는다.
//
// BeginPut 을 실제 원격 작업보다 먼저 두는 이유:
//
// BeginPut 이후 프로세스가 죽으면 Ledger 에 IN_PROGRESS + part_path 가
// 남는다. 다음 시작의 recovery 가 잔여 .part 를 찾아 정리할 수 있다.
//
// UploadPart 가 오류 없이 반환했다는 사실만으로 VERIFIED 하지 않는다.
// .part Size 검증과 Rename 후 최종 Size 검증까지 성공해야 FinishPut 한다.
//
// 개별 전송 실패는 .part cleanup 을 best-effort 로 수행한 뒤 FailPut 으로
// FAILED 에 접고 다음 후보로 진행한다.
//
// 반환 error 는 파일 한 건의 전송 실패가 아니라, 실행 자체를 중단해야
// 하는 오류(ctx 취소, Ledger 오류, 조립 오류)다.
func (r *Runner) Transfer(
	ctx context.Context,
	up Uploader,
	jobs []CategoryJob,
	cands []Candidate,
) (rep TransferReport, err error) {
	started := r.now()

	// Elapsed 는 어느 경로로 빠져나가든 채워져야 한다.
	// 반환 지점마다 수동으로 대입하면 언젠가 한 곳을 빠뜨린다.
	defer func() {
		rep.Elapsed = r.now().Sub(started)
	}()

	if up == nil {
		return rep, fmt.Errorf("put: uploader is required")
	}

	// Category → Job 조회표.
	//
	// Candidate 는 Runner.Run 이 만든 목록이므로 정상적인 조립에서는
	// 반드시 같은 category 의 CategoryJob 이 존재해야 한다.
	//
	// 키를 string 으로 변환하지 않는다. domain.Category 가 이미 비교
	// 가능하고, checkInput 도 map[domain.Category]struct{} 를 쓴다.
	// 같은 패키지 안에서 두 표현이 섞이면 조회 실패가 조용히 난다.
	remoteJobs := make(map[domain.Category]*CategoryJob, len(jobs))

	for i := range jobs {
		if _, dup := remoteJobs[jobs[i].Category]; dup {
			return rep, fmt.Errorf(
				"put: duplicate category job %s",
				jobs[i].Category,
			)
		}

		// Transfer 는 Run 과 별개 진입점이므로 checkInput 을 재사용할
		// 수 없다 (checkInput 은 Scanner 를 요구하는데 Transfer 는
		// 스캔하지 않는다). 전송이 실제로 쓰는 입력만 여기서 검사한다.
		if jobs[i].RemotePath == nil {
			return rep, fmt.Errorf(
				"put: category %s has nil RemotePath template",
				jobs[i].Category,
			)
		}

		remoteJobs[jobs[i].Category] = &jobs[i]
	}

	// EnsureDir 중복 호출 제거용 캐시.
	//
	// 캐시를 구현체가 아니라 여기에 두는 것은 Uploader 계약이 정한
	// 책임 배치다. 구현체를 무상태로 두어야 LocalFS 와 SFTPFS 가 같은
	// 최적화를 각자 구현하지 않는다.
	//
	// 한 시각 디렉터리에 관측소 100여 개 파일이 함께 들어 있으므로,
	// 캐시가 없으면 같은 경로로 100번 넘게 호출된다. SFTP 에서는
	// 경로 세그먼트 수만큼 왕복이 곱해진다. (GUIDELINES 9.2)
	//
	// 실패한 디렉터리는 캐시에 넣지 않는다. 일시적 실패였다면
	// 다음 파일에서 다시 시도할 수 있어야 한다.
	//
	// 순차 Transfer 에서는 동기화가 필요 없다. Worker 도입 때
	// 이 맵의 동시 접근을 호출자가 막아야 한다.
	ensured := make(map[string]struct{})

	for _, c := range cands {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return rep, ctxErr
		}

		job, ok := remoteJobs[c.Key.Category]
		if !ok {
			return rep, fmt.Errorf(
				"put: no job for candidate category %s",
				c.Key.Category,
			)
		}

		// ------------------------------------------------------------
		// PRE-FLIGHT
		// ------------------------------------------------------------
		//
		// Scan 과 실제 전송 사이에는 시간이 존재하므로 로컬 원본이
		// 사라지거나 교체/증가할 수 있다. MaxFilesPerRun 이 크고 회선이
		// 느리면 마지막 후보는 스캔된 지 수십 분 뒤에 전송된다.
		//
		// 이것을 BeginPut 이후에 발견하면 attempts 가 증가하여
		// "실제 PUT 전송 착수 횟수" 라는 의미가 깨진다.
		// 따라서 로컬 입력 상태는 반드시 BeginPut 전에 다시 확인한다.
		//
		// 실패해도 실행을 중단하지 않고 이 파일만 건너뛴다.
		if pfErr := preflightLocal(c); pfErr != nil {
			rep.SkippedPreflight++

			// IsRetry(기존 FAILED 행)는 stale 이어도 장부 정리가 필요
			// 없다. 행이 이미 FAILED 고 preflight 는 attempts 를
			// 소모하지 않으므로 할 일이 없다. FailPut 을 부르면 WHERE
			// 가드(PENDING, IN_PROGRESS)에 걸리지 않아 전이 위반
			// 오류가 되고, 파일 하나의 크기 변화가 회차 전체를
			// 중단시킨다 (2026-08-31 교차 리뷰에서 발견).
			//
			// 불변식: 이 시점에 put_ledger 행이 FAILED 인 후보는
			// IsRetry 뿐이다. (runner.go 후보 필터 — 행없음/PENDING
			// 고아는 PENDING, VERIFIED·IN_PROGRESS·소진은 후보가
			// 아니다. InsertPendingBatch 는 기존 행을 건드리지 않는다.)
			// 이 불변식이 깨지면 FailPut 의 WHERE 가드에 걸려 회차가
			// 중단된다. 후보 필터에 경로를 추가할 때 이 문장을 먼저
			// 읽어라. 필터가 복잡해져 IsRetry 로 상태를 추론하기
			// 어려워지면, 그때는 ledger 에 ErrNotPending sentinel 을
			// 두고 failPending 이 errors.Is 로 no-op 하는 판으로
			// 갈아탄다 (보류 중인 대안 ③).
			if errors.Is(pfErr, errPreflightStale) && !c.IsRetry {
				if failErr := r.failPending(ctx, c, pfErr); failErr != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return rep, errors.Join(ctxErr, failErr)
					}

					return rep, failErr
				}
			}

			r.logf(
				"[XFER][SKIP] %s rev=%d: %v",
				c.Key.FileName,
				c.Key.Revision,
				pfErr,
			)

			continue
		}

		// 원격에는 스캔에서 관측한 원본 파일명의 대소문자를 보존한다.
		//
		// Key.FileName 은 Ledger 식별을 위해 NormalizeName 된 값이므로
		// RINEX 파일의 실제 이름을 그대로 출력하는 용도로 쓰면 안 된다.
		//
		// filepath.Base 는 로컬 경로 조작이므로 로컬 OS 규칙이 맞다.
		// 원격 경로 결합에만 up.Join 을 쓴다.
		base := filepath.Base(c.LocalPath)
		remoteDir := job.RemotePath.Expand(c.When)
		finalPath := up.Join(remoteDir, base)
		partPath := finalPath + putPartSuffix

		// ------------------------------------------------------------
		// 실제 PUT 착수
		// ------------------------------------------------------------
		//
		// 여기서부터 attempts 를 소모한다.
		//
		// BeginPut 은 status 변경 + attempts++ + 경로/크기 기록을
		// 하나의 UPDATE 로 수행한다.
		if beginErr := r.DB.BeginPut(
			ctx,
			c.Key,
			finalPath,
			partPath,
			c.Size,
			r.Opts.MaxRetries,
		); beginErr != nil {
			if errors.Is(beginErr, ledger.ErrNotCandidate) {
				// 후보 재확인 시점과 실제 BeginPut 사이에 상태가 바뀌거나
				// attempts 상한에 도달했다면 skip 이 정상 동작이다.
				rep.NotCandidate++
				continue
			}

			// Ledger 자체가 상태 전이를 수행하지 못했다면 이후 원격 I/O 를
			// 수행할 근거가 없으므로 실행을 중단한다.
			return rep, fmt.Errorf(
				"begin put %q: %w",
				c.Key.FileName,
				beginErr,
			)
		}

		rep.Attempted++

		// ------------------------------------------------------------
		// 실제 파일 전송 + Transfer Verification
		// ------------------------------------------------------------
		//
		// EnsureDir 실패도 전송 실패로 취급한다. BeginPut 이 이미
		// 성공했으므로 attempts 를 소모하며, 권한이나 RemotePath 설정
		// 문제라면 재시도해도 같은 결과라 MaxRetries 소진이 옳은 귀결이다.
		var res uploadResult

		transferErr := r.ensureRemoteDir(ctx, up, remoteDir, ensured)
		if transferErr == nil {
			res, transferErr = r.uploadOne(ctx, up, c, partPath, finalPath)
		}

		if transferErr != nil {
			if failErr := r.failOne(
				ctx,
				up,
				c,
				partPath,
				res,
				transferErr,
			); failErr != nil {
				// FAILED 확정에 실패하면 IN_PROGRESS 가 남는다.
				// 취소 때문이라면 그 사실도 함께 올린다.
				if ctxErr := ctx.Err(); ctxErr != nil {
					return rep, errors.Join(ctxErr, failErr)
				}

				return rep, failErr
			}

			rep.Failed++

			// 취소는 개별 파일 실패로 Ledger 에 접은 뒤
			// 다음 파일로 진행하지 않고 실행 전체를 중단한다.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return rep, ctxErr
			}

			continue
		}

		// uploadOne 이 성공했다는 것은
		//
		//	.part upload 성공
		//	.part Size 일치
		//	Rename 성공
		//	final Size 일치
		//
		// 까지 끝났다는 뜻이다. 이 시점에만 VERIFIED 로 확정할 수 있다.
		//
		// 원격은 이미 맞다. 취소된 ctx 로 FinishPut 하면 IN_PROGRESS 가
		// 남아 recovery 가 정상 파일을 실패로 오인한다.
		finishCtx, finishCancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			ledgerTimeout,
		)
		finishErr := r.DB.FinishPut(
			finishCtx,
			c.Key,
			c.Size,
			res.SentAt,
			r.now().UTC(),
		)
		finishCancel()

		if finishErr != nil {
			// 주의:
			// 최종 파일 자체는 이미 올바르게 존재한다.
			// FinishPut 실패를 파일 전송 실패처럼 FailPut 으로 덮지 않는다.
			//
			// Ledger 기록 실패는 실행 중단 사유다.
			// 이 경우 IN_PROGRESS 가 남으며 다음 startup recovery 가 다룬다.
			//
			// recovery 설계 시 메모 — 이 경로로 남은 IN_PROGRESS 는
			// .part 가 이미 rename 되어 없고 최종 파일은 정상이다.
			// 무조건 FAILED 로 되돌리면 멀쩡한 파일을 재전송하게 되므로,
			// remote_path 의 Size 를 먼저 확인하는 분기를 검토한다
			// (seed 의 판정 로직과 같은 기준. CONCEPT 4.11).
			return rep, fmt.Errorf(
				"finish put %q: %w",
				c.Key.FileName,
				finishErr,
			)
		}

		rep.Verified++
	}

	r.logf(
		"[XFER] attempted=%d verified=%d failed=%d "+
			"not_candidate=%d skipped_preflight=%d elapsed=%s",
		rep.Attempted,
		rep.Verified,
		rep.Failed,
		rep.NotCandidate,
		rep.SkippedPreflight,
		r.now().Sub(started).Round(time.Millisecond),
	)

	return rep, nil
}

// ensureRemoteDir 은 원격 목적지 디렉터리를 준비하되 같은 경로에 대한
// 중복 호출을 걷어낸다.
//
// 기존 운영 스크립트도 put 전에 목적지 경로를 세그먼트 단위로 재귀
// 생성하고 있었다. 이 절차가 없으면 첫 전송이 전부 "No such file
// (code 2)" 로 실패한다. (GUIDELINES 9.2)
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

	return res, nil
}
