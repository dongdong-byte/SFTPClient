// seed.go — 설치 초기화: 원격 대조 기반 VERIFIED 선반영.
//
// 배경: 설치 시점의 ledger 는 비어 있고, 기관 원격에는 기존 시스템이
// 이미 보낸 최근 7일치 파일이 있다. 그대로 첫 실행을 돌리면 창 안의
// 파일 전부가 "신규" 로 보여 이미 가 있는 파일을 재전송한다.
// seed 는 "원격에 실제로 있고 크기가 일치하는" 파일만 VERIFIED 로
// 선반영하여 이 중복 전송을 막는다.
//
// 판정 (2026-09-01 확정):
//
//	예상 finalPath 에 파일이 존재 AND 원격 Size == 로컬 Size → seed
//	원격 없음 / Size 불일치 → seed 하지 않음. 첫 live 실행이 정상
//	  경로(재전송 + PosixRename 덮어쓰기)로 처리한다.
//
// 원격 디렉터리 List 기반 안은 기각했다 — seed 의 목적이 "로컬에
// 존재하는 파일 중 원격에도 있는 것 반영" 이므로 로컬에 없는 원격
// 파일을 발견할 이유가 없고, 예상 finalPath 를 직접 Stat 하면
// Uploader 인터페이스 확장(List·RemoteEntry) 없이 기존 Size 계약으로
// 끝난다.
//
// 잔여 위험 (수용, 문서화): 로컬에서 내용이 바뀌었는데 크기가 우연히
// 같은 파일은 원격의 옛 내용을 VERIFIED 로 신뢰한다. recovery 의
// size-only salvage 를 기각시킨 그 시나리오와 같은 부류지만, 성격이
// 다르다 —
//
//	recovery: 프로그램이 매 시작 자동으로 내리는 판정. 잘못되면
//	  조용한 누락이 상시 경로에 생긴다.
//	seed: 운영자가 "기존 전송분을 초기 신뢰 기준으로 받아들인다" 고
//
// Size 일치만으로 내용 동일성을 증명할 수는 없으며,
// 동일 크기의 다른 내용일 가능성은 원칙적으로 남는다.
// 다만 seed 는 운영자가 기존 원격 전송분을 초기 신뢰 기준으로
// 받아들인다는 전제 아래 명시적으로 1회 실행하는 설치 절차다.
// MVP1에서는 checksum 수집 비용을 추가하지 않고 이 잔여 위험을 수용한다.
//
// 비용: 판정이 파일당 원격 Stat 1회다 (O(디렉터리)가 아니라 O(파일)).
// 7일 창 실측 113,572 파일 × 왕복 수 ms = 수 분. 1회성 설치 작업이라
// 수용한다. 느리면 Worker Pool 패턴 재사용이 가능하나 구현은 유보한다.
// 수 분 무음이면 죽은 것으로 보이므로 진행 로그를 주기적으로 남긴다.
package put

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"time"
)

// seedProgressEvery 는 진행 로그 주기(검사 파일 수)다.
const seedProgressEvery = 1000

// SeedReport 는 seed 실행의 관측이다.
type SeedReport struct {
	// Candidates 는 Run(SeedMode)이 넘긴 대조 대상 수다.
	Candidates int

	// Seeded 는 원격 대조를 통과해 VERIFIED 로 등록한 수다.
	Seeded int

	// RemoteMissing 은 예상 finalPath 에 원격 파일이 없던 수다.
	// 기존 시스템이 보낸 적 없는 파일 — 첫 live 실행이 전송한다.
	RemoteMissing int

	// SizeMismatch 는 원격 파일은 있으나 Size 가 로컬과 다른 수다.
	// 불완전 전송 잔재 또는 옛 내용 — 첫 live 실행이 덮어쓴다.
	SizeMismatch int

	// AlreadyLedger 는 같은 (category, file_name, revision) 행이 이미
	// put_ledger 에 있어 건드리지 않은 수다 (부분 운영 후 재실행 seed).
	// 기존 행은 live 경로 소유다.
	AlreadyLedger int

	Elapsed time.Duration
}

// Seed 는 Run(SeedMode) 이 계산한 후보를 원격과 대조하여 일치분만
// VERIFIED 로 등록한다. 전송하지 않고, 어떤 원격 파일도 변경하지 않는다
// (Stat 만 한다).
//
// 순차 실행이다. 진행은 seedProgressEvery 건마다 로그로 남긴다.
//
// 오류 방침: 원격의 예상 밖 오류(권한·통신)는 실행을 중단한다.
// 원격 상태를 확인할 수 없는데 장부를 쓰는 것은 추측이다. seed 는
// 멱등하므로(이미 등록된 행은 Run 이 후보에서 제외하고, SeedVerified
// 는 DO NOTHING) 중단 후 재실행에 비용이 없다 — recovery 와 같은 방침.
func (r *Runner) Seed(
	ctx context.Context,
	up Uploader,
	jobs []CategoryJob,
	cands []Candidate,
) (rep SeedReport, err error) {
	started := r.now()

	defer func() {
		rep.Elapsed = r.now().Sub(started)
	}()

	if up == nil {
		return rep, fmt.Errorf("put: uploader is required")
	}

	rep.Candidates = len(cands)

	// groupByDirectory 를 재사용한다 — jobs 검증(중복·nil 템플릿·후보
	// category 누락)과 finalPath 의 디렉터리 계산이 전송과 같은 코드를
	// 타야 seed 가 대조하는 경로와 live 가 실제 쓰는 경로가 어긋날 수
	// 없다.
	batches, err := r.groupByDirectory(jobs, cands)
	if err != nil {
		return rep, err
	}

	checked := 0

	for _, b := range batches {
		for _, c := range b.Candidates {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return rep, ctxErr
			}

			finalPath := up.Join(b.RemoteDir, filepath.Base(c.LocalPath))

			gotSize, sizeErr := up.Size(ctx, finalPath)

			switch {
			case errors.Is(sizeErr, fs.ErrNotExist):
				rep.RemoteMissing++

			case sizeErr != nil:
				if ctxErr := ctx.Err(); ctxErr != nil {
					return rep, errors.Join(ctxErr, sizeErr)
				}

				return rep, fmt.Errorf(
					"put: seed stat %q (%s %q rev=%d): %w",
					finalPath,
					c.Key.Category,
					c.Key.FileName,
					c.Key.Revision,
					sizeErr,
				)

			case gotSize != c.Size:
				rep.SizeMismatch++

			default:
				seedCtx, cancel := context.WithTimeout(
					context.WithoutCancel(ctx),
					ledgerTimeout,
				)
				seeded, seedErr := r.DB.SeedVerified(
					seedCtx,
					c.Key,
					finalPath,
					gotSize,
					r.now().UTC(),
				)
				cancel()

				if seedErr != nil {
					return rep, seedErr
				}

				if seeded {
					rep.Seeded++
				} else {
					rep.AlreadyLedger++
				}
			}

			checked++

			if checked%seedProgressEvery == 0 {
				r.logf(
					"[SEED] progress checked=%d/%d seeded=%d "+
						"missing=%d mismatch=%d",
					checked,
					rep.Candidates,
					rep.Seeded,
					rep.RemoteMissing,
					rep.SizeMismatch,
				)
			}
		}
	}

	r.logf(
		"[SEED] 완료 candidates=%d seeded=%d remote_missing=%d "+
			"size_mismatch=%d already_ledger=%d elapsed=%s",
		rep.Candidates,
		rep.Seeded,
		rep.RemoteMissing,
		rep.SizeMismatch,
		rep.AlreadyLedger,
		r.now().Sub(started).Round(time.Millisecond),
	)

	return rep, nil
}
