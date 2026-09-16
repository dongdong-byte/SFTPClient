package put

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// recoverCause 는 회수로 FAILED 가 된 행의 error 컬럼에 남기는 사유다.
//
// 전송 자체의 실패(네트워크·크기 불일치)와 구분되도록 고정 문구를 쓴다.
// 운영자가 put_ledger.error 로 "이 FAILED 는 크래시 회수분" 을 구분할 수 있다.
const recoverCause = "recovered from IN_PROGRESS on startup"

// RecoverReport 는 시작 시 IN_PROGRESS 회수 단계의 관측이다.
type RecoverReport struct {
	// Found 는 IN_PROGRESS 로 남아 있던 행 수다.
	// 0 이면 직전 실행이 깨끗하게 끝났다는 뜻이다.
	Found int

	// PartRemoved 는 part_path 가 있어 원격 .part 삭제를 수행한 수다.
	// Remove 는 멱등이므로 대상이 이미 없던 경우도 성공으로 세어 포함한다.
	PartRemoved int

	// Recovered 는 FAILED 로 되돌린 수다.
	//
	// 정상 종료 시 Found 와 같다. 원격/Ledger 오류나 취소로 중간에
	// 중단되면 그 지점까지의 수이며, 남은 항목은 다음 시작이 회수한다.
	Recovered int

	Elapsed time.Duration
}

// Recover 는 시작 시 IN_PROGRESS 로 남은 잔여 전송을 회수한다.
//
// 확정 흐름(Lock → Recover → Scan → 후보 → Transfer)의 첫 단계다.
//
// 회수한 행은 FAILED 로 돌아간다.
// attempts < MaxRetries 인 항목은 같은 실행의 후보 판정에서 IsRetry 로
// 다시 잡혀 곧바로 재전송될 수 있다.
// 이미 누적 시도 상한에 도달한 항목은 exhausted 로 제외된다.
//
// salvage 는 하지 않는다 (A안, 2026-09-01 확정).
//
// "원격 final 존재 + Size 일치" 만으로는 이 revision 의 Rename 이
// 끝났음을 증명할 수 없다. 시작 시점의 IN_PROGRESS 행은 rename 성공
// 여부를 기록하지 않으므로, 다음 두 경우가 구분되지 않는다.
//
//	rename 직후 사망 → 원격 final = 이 revision 의 내용 (salvage 옳음)
//	업로드 전 사망   → 원격 final = 이전 revision 의 내용 (salvage 틀림)
//
// revision 이 size OR mtime 로 오르는 정책상, 크기가 같고 mtime 만
// 다른 보정본이 이전 revision 위에서 죽으면 Size 일치가 성립하여,
// 전송되지 않은 파일을 VERIFIED 로 만들 수 있다.
//
// 이는 조용한 누락이므로 항상 FAILED 로 되돌려 재전송·재검증한다.
// 비용은 post-rename 크래시 때 파일 하나를 다시 보내는 것뿐이며,
// 재전송은 PosixRename 덮어쓰기로 안전하다.
//
// rename 완료를 durable 하게 표시하는 C안은 상태/컬럼 재설계가
// 필요하므로 현재 범위 밖이다.
//
// attempts 는 건드리지 않는다. 크래시 시점의 BeginPut 이 이미 한 번
// 계상했고, 재시도는 BeginPut 이 다시 올린다. MaxRetries 의미가 유지된다.
//
// 회수는 멱등하다. 어느 지점에서 중단되어도 원격 오류·취소 등으로
// 남은 IN_PROGRESS 는 다음 시작에서 다시 회수할 수 있다.
//
// 예상 밖 Remove 오류는 해당 항목만 조용히 건너뛰지 않고 실행을
// 중단한다. 권한·통신·원격 상태 이상일 가능성이 있으며, 회수 자체가
// 멱등하므로 다음 시작에서 안전하게 다시 시도할 수 있다.
//
// lock 아래 단일 실행을 전제한다(main 이 lock 획득 후 호출).
// 따라서 ListInProgress 와 각 항목의 상태 전이 사이에 동시 회수 경합은 없다.
func (r *Runner) Recover(
	ctx context.Context,
	up Uploader,
) (rep RecoverReport, err error) {
	started := r.now()

	// Elapsed 는 어느 경로로 빠져나가든 채워져야 한다.
	// Transfer 와 동일한 관측 원칙이다.
	defer func() {
		rep.Elapsed = r.now().Sub(started)
	}()

	if up == nil {
		return rep, fmt.Errorf("put: uploader is required")
	}

	items, err := r.DB.ListInProgress(ctx)
	if err != nil {
		return rep, fmt.Errorf("put: recover list in_progress: %w", err)
	}

	rep.Found = len(items)

	// 깨끗하게 끝난 정상 실행이 대부분이다.
	// 대상이 없으면 별도 로그를 남기지 않는다.
	if len(items) == 0 {
		return rep, nil
	}

	r.logf("[RECOVER] IN_PROGRESS %d건 회수 시작", len(items))

	for _, it := range items {
		// 취소는 항목 사이에서 확인한다.
		// 여기서 멈춰도 남은 항목은 다음 시작에서 다시 회수된다.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return rep, ctxErr
		}

		// ── 1) 원격 .part 정리 ─────────────────────────────────
		//
		// part_path 는 BeginPut 이 IN_PROGRESS 전환과 같은 UPDATE 에서
		// 기록하므로 정상 경로에서는 항상 채워져 있다.
		//
		// 스키마상 nullable 이므로 nil 은 방어한다.
		//
		// Remove 는 "대상 없음" 을 nil 로 정규화한다(Uploader 계약).
		// 따라서 여기서 반환되는 error 는 권한·통신 등 실제 오류다.
		if it.PartPath != nil {
			removeCtx, cancel := context.WithTimeout(ctx, cleanupTimeout)
			removeErr := up.Remove(removeCtx, *it.PartPath)
			cancel()

			if removeErr != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return rep, errors.Join(ctxErr, removeErr)
				}

				return rep, fmt.Errorf(
					"put: recover remove .part %q: %w",
					*it.PartPath,
					removeErr,
				)
			}

			rep.PartRemoved++
		}

		// ── 2) IN_PROGRESS → FAILED ─────────────────────────────
		//
		// 반드시 .part 정리 뒤에 되돌린다.
		//
		// FAILED 로 먼저 만든 뒤 Remove 가 실패하면 원격 .part 는
		// 남아 있는데 Ledger 에서는 이미 회수가 끝난 것처럼 보일 수 있다.
		//
		// ctx 를 그대로 사용한다(WithoutCancel 아님).
		// 회수 중 취소되어 FailPut 이 수행되지 않아도 행은 IN_PROGRESS 로
		// 남을 뿐이며 다음 시작이 다시 회수할 수 있다.
		failCtx, cancel := context.WithTimeout(ctx, ledgerTimeout)
		failErr := r.DB.FailPut(failCtx, it.PutKey, recoverCause)
		cancel()

		if failErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return rep, errors.Join(ctxErr, failErr)
			}

			return rep, fmt.Errorf(
				"put: recover fail put %q rev=%d: %w",
				it.FileName,
				it.Revision,
				failErr,
			)
		}

		rep.Recovered++

		r.logf(
			"[RECOVER] %s rev=%d → FAILED (재전송 후보)",
			it.FileName,
			it.Revision,
		)
	}

	r.logf(
		"[RECOVER] 완료 found=%d part_removed=%d recovered=%d elapsed=%s",
		rep.Found,
		rep.PartRemoved,
		rep.Recovered,
		r.now().Sub(started).Round(time.Millisecond),
	)

	return rep, nil
}
