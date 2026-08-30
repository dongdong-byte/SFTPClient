package put

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// exhaustedExampleCap 은 소진 WARN 에 나열하는 파일명 예시 상한이다.
// 전부 나열하면 대량 소진 시 로그 한 줄이 수백 KB 가 된다.
const exhaustedExampleCap = 5

// Runner 는 PUT 파이프라인을 실행한다.
//
// 모든 의존이 필드로 드러나 있어 테스트가 fake DirLister 와
// 임시 DB 만으로 전체 흐름을 검증할 수 있다.
type Runner struct {
	Scanner  *scan.Scanner
	DB       *ledger.DB
	Verifier verify.Verifier
	Opts     RunOptions
}

// failedRef 는 FAILED 로 관측되어 attempts 확인이 필요한 후보 예비다.
//
// LookupCommon 의 PutStatus 는 상태만 알려주고 attempts 를 모른다.
// attempts 는 FAILED 부분집합에만 필요하므로, 배치 처리 중에는 모아만
// 두고 카테고리 끝에서 LookupPut 한 번으로 해소한다.
type failedRef struct {
	nameRev ledger.NameRev
	cand    Candidate
}

// Run 은 활성 카테고리 전체를 순차 실행하고, 정렬·절단·PENDING 등록까지
// 마친 최종 전송 대상과 리포트를 돌려준다.
//
// dry-run 이면 common_ledger / put_ledger 업무 데이터를 변경하지 않고
// 목록과 리포트만 만든다.
// 반환한 []Candidate 는 전송 단계(Worker)가 소비할 목록이다.
func (r *Runner) Run(
	ctx context.Context,
	jobs []CategoryJob,
	rng scan.Range,
) ([]Candidate, RunReport, error) {
	var report RunReport
	report.DryRun = r.Opts.DryRun

	if err := r.checkInput(jobs); err != nil {
		return nil, report, err
	}

	started := r.now()

	var all []Candidate

	for _, job := range jobs {
		cands, catRep, err := r.runCategory(ctx, job, rng)
		if err != nil {
			return nil, report, fmt.Errorf(
				"put: category %s: %w",
				job.Category,
				err,
			)
		}

		report.Categories = append(report.Categories, catRep)
		all = append(all, cands...)
	}

	kept, registered, err := r.finalize(ctx, all, &report)
	if err != nil {
		return nil, report, err
	}

	report.Registered = registered
	report.Elapsed = r.now().Sub(started)

	return kept, report, nil
}

// checkInput 은 실행 불가능한 옵션을 입구에서 막는다.
//
// MaxRetries < 1 은 ledger.BeginPut 도 거부하지만, 여기서 막지 않으면
// 후보 필터의 attempts < MaxRetries 가 전부 거짓이 되어 FAILED 재시도가
// 조용히 0건이 된다. BeginPut 의 가드와 같은 이유, 같은 계열이다.
//
// MaxFilesPerRun 은 0 을 허용한다. config 가 0 을 "제한 없음" 으로
// 문서화하고 Validate 도 음수만 거부한다. 세 곳의 규약을 맞춘다.
func (r *Runner) checkInput(jobs []CategoryJob) error {
	if r.Scanner == nil || r.DB == nil {
		return fmt.Errorf("put: scanner and db are required")
	}

	if len(jobs) == 0 {
		return fmt.Errorf("put: at least one category job is required")
	}

	if r.Opts.MaxRetries < 1 {
		return fmt.Errorf(
			"put: MaxRetries must be at least 1, got %d",
			r.Opts.MaxRetries,
		)
	}

	if r.Opts.MaxFilesPerRun < 0 {
		return fmt.Errorf(
			"put: MaxFilesPerRun must not be negative, got %d",
			r.Opts.MaxFilesPerRun,
		)
	}

	seen := map[domain.Category]struct{}{}

	for _, job := range jobs {
		if job.LocalPath == nil || job.RemotePath == nil {
			return fmt.Errorf(
				"put: category %s has nil path template",
				job.Category,
			)
		}

		// 같은 Category 가 두 번 오면 스캔·후보·집계가 두 배가 되고
		// PENDING 은 두 번째에서 DO NOTHING 이 된다. 조용히 두 배로
		// 도는 것보다 입구에서 거부한다. config 조립이 정상이라면
		// 도달할 수 없는 경로다.
		if _, dup := seen[job.Category]; dup {
			return fmt.Errorf(
				"put: duplicate category job: %s",
				job.Category,
			)
		}

		seen[job.Category] = struct{}{}
	}

	return nil
}

// runCategory 는 카테고리 하나의 스캔부터 후보 필터까지다.
func (r *Runner) runCategory(
	ctx context.Context,
	job CategoryJob,
	rng scan.Range,
) ([]Candidate, CategoryReport, error) {
	started := r.now()

	rep := CategoryReport{
		Category: job.Category,
		Rejected: map[string]int{},
		ExtCount: map[string]int{},
	}

	var (
		cands []Candidate
		// failed 는 Unchanged 로 관측된 FAILED 의 attempts 해소 대기열이다.
		failed []failedRef
		// changedObs 는 dry-run 변경 파일의 현재 revision 관측 대기열이다.
		// 후보 판정과 무관하며 리포트에만 쓴다.
		changedObs []ledger.NameRev
		seen       = map[string]struct{}{}
		stations   = map[string]struct{}{}
	)

	visit := func(b scan.Batch) error {
		return r.visitBatch(
			ctx,
			job,
			b,
			&rep,
			seen,
			stations,
			&cands,
			&failed,
			&changedObs,
		)
	}

	res, err := r.Scanner.Scan(
		ctx,
		job.Category,
		job.LocalPath,
		rng,
		visit,
	)
	rep.Scan = res

	// 개별 디렉터리 나열 실패는 Scan 이 이미 Errs/Failures 로 접었다.
	// 여기 err 는 중단 사유(입력 오류·ctx 취소·visit 오류)뿐이다.
	if err != nil {
		return nil, rep, err
	}

	for _, f := range res.Failures {
		r.logf("[SCAN][WARN] category=%s %v", job.Category, f)
	}

	// FAILED 부분집합의 attempts 해소.
	//
	// 여기 들어오는 FAILED 는 Unchanged 파일의 현재 revision 이다.
	// 변경 파일은 live 에서 새 revision 이 생겨 예산이 리셋되므로
	// 이 필터에 넣지 않는다.
	if err := r.resolveFailed(
		ctx,
		job.Category,
		failed,
		&rep,
		&cands,
	); err != nil {
		return nil, rep, err
	}

	// dry-run 변경 파일의 현재 revision 관측.
	//
	// 위 resolveFailed 와 달리 후보를 만들거나 제외하지 않는다.
	// 순수하게 "지금 장부에 무엇이 있는가" 를 리포트에 남긴다.
	if err := r.observeChangedCurrentPut(
		ctx,
		job.Category,
		changedObs,
		&rep,
	); err != nil {
		return nil, rep, err
	}

	rep.StationCount = len(stations)
	rep.Candidates = len(cands)
	rep.Elapsed = r.now().Sub(started)

	return cands, rep, nil
}

// visitBatch 는 디렉터리 하나(Batch)를 처리한다.
//
// 흐름:
// 구조상 Ledger 조회 전에 제외해야 하는 항목을 verify 로 판정
// → Batch Lookup
// → 메모리 대조
// → 신규·변경만 Verify
// → (live) Upsert
// → 후보/예비 분류.
//
// Unchanged 는 Upsert 만 생략한다.
// "장부와 같다" 는 "이미 보냈다" 는 뜻이 아니므로 put 상태 판정은 계속한다.
func (r *Runner) visitBatch(
	ctx context.Context,
	job CategoryJob,
	b scan.Batch,
	rep *CategoryReport,
	seen map[string]struct{},
	stations map[string]struct{},
	cands *[]Candidate,
	failed *[]failedRef,
	changedObs *[]ledger.NameRev,
) error {
	names := make([]string, 0, len(b.Entries))
	byName := make(map[string]scan.Entry, len(b.Entries))

	for _, e := range b.Entries {
		// NormalizeName 은 .part 를 제거하므로 .part 를 그대로 Lookup 하면
		// 최종 파일과 같은 식별자로 합쳐질 수 있다.
		//
		// 실제 제외 판정의 주인은 verify 이므로 Verifier 의 결과를 기록한다.
		if e.IsDir || domain.IsPartFile(e.Name) {
			reason := r.Verifier.Verify(verify.Input{
				Name:     e.Name,
				Size:     e.Size,
				MTime:    e.MTime,
				IsDir:    e.IsDir,
				Category: job.Category,
			})

			if reason.OK() {
				return fmt.Errorf(
					"verifier accepted non-ledger entry %q",
					e.Name,
				)
			}

			rep.Rejected[reason.String()]++

			if e.IsDir {
				rep.SkippedDirs++
			} else {
				rep.SkippedPart++
			}

			continue
		}

		n := domain.NormalizeName(e.Name)

		if _, dup := seen[n]; dup {
			rep.SkippedDuplicate++

			r.logf(
				"[PUT][WARN] category=%s duplicate normalized name=%q source=%q",
				job.Category,
				n,
				e.Name,
			)

			continue
		}

		seen[n] = struct{}{}
		names = append(names, n)
		byName[n] = e

		// 확장자 관측값은 실제 파일명의 표기를 유지한다.
		// NormalizeName 후 집계하면 .Z 가 .z 로 바뀐다.
		rep.ExtCount[filepath.Ext(e.Name)]++

		if station := stationOf(n); station != "" {
			stations[station] = struct{}{}
		}
	}

	if len(names) == 0 {
		return nil
	}

	known, err := r.DB.LookupCommon(ctx, job.Category, names)
	if err != nil {
		return fmt.Errorf("lookup common (dir %s): %w", b.Dir, err)
	}

	// live 에서 Upsert 한 이름.
	// 배치 끝에 재조회하여 실제 revision 을 확정한다.
	var upserted []string
	protos := map[string]Candidate{}

	for _, n := range names {
		e := byName[n]
		mtime := e.MTime.UTC().Unix()
		k, exists := known[n]

		proto := Candidate{
			Key: ledger.PutKey{
				Category: job.Category,
				FileName: n,
			},
			LocalPath: filepath.Join(b.Dir, e.Name),
			Size:      e.Size,
			When:      b.When,
		}

		// origin 은 최초 유입 사실이며 ledger 가 재관측 시 갱신하지 않는다.
		//
		// 아래 live Upsert 에 OriginLocal 을 넘기더라도 기존 DOWNLOAD 행의
		// origin 이 LOCAL 로 바뀌지 않는다는 ledger 불변식을 전제로 한다.
		//
		// DOWNLOAD 파일이 변경되었더라도 RepostDownloaded=false 이면
		// PUT 대상으로 새어 나가면 안 되므로 Unchanged 분기보다 먼저 검사한다.
		if exists &&
			k.Origin == domain.OriginDownload &&
			!r.Opts.RepostDownloaded {
			rep.ExcludedDownloadOrigin++
			continue
		}

		// ── Unchanged ───────────────────────────────────────────────
		if exists && k.Size == e.Size && k.MTime == mtime {
			rep.Unchanged++

			proto.Key.Revision = k.Revision
			// 현재 Ledger 의 실제 revision 이므로 RevisionPending=false.

			switch k.PutStatus {
			case "":
				// Common 에는 있으나 현재 revision 전송 이력이 없다.
				// MaxFilesPerRun 에 잘렸던 항목도 이 경로로 다시 후보가 된다.
				*cands = append(*cands, proto)

			case domain.StatusPending:
				// 직전 실행이 PENDING 등록 후 죽은 고아.
				*cands = append(*cands, proto)

			case domain.StatusInProgress:
				// 시작 시 회수 절차 몫이다 (3탄).
				rep.ExcludedInProgress++

			case domain.StatusVerified:
				rep.ExcludedVerified++

			case domain.StatusFailed:
				*failed = append(*failed, failedRef{
					nameRev: ledger.NameRev{
						FileName: n,
						Revision: k.Revision,
					},
					cand: proto,
				})

			default:
				return fmt.Errorf(
					"unknown put status %q for %q rev=%d",
					k.PutStatus,
					n,
					k.Revision,
				)
			}

			continue
		}

		// ── 신규 또는 변경: Ingress 판정 ──────────────────────────
		reason := r.Verifier.Verify(verify.Input{
			Name:     e.Name,
			Size:     e.Size,
			MTime:    e.MTime,
			IsDir:    e.IsDir,
			Category: job.Category,
		})

		if !reason.OK() {
			rep.Rejected[reason.String()]++
			continue
		}

		if exists {
			rep.Changed++
		} else {
			rep.New++
		}

		if r.Opts.DryRun {
			// dry-run 은 미래 revision 을 산술로 만들어내지 않는다.
			//
			// 신규:
			//   아직 common_ledger 행이 없으므로 Revision=0.
			//   0은 실제 Ledger revision 으로 유효하지 않아
			//   실수로 LookupPut 에 넣어도 즉시 거부된다.
			//
			// 변경:
			//   현재 common_ledger revision 을 그대로 표시한다.
			//   live 에서는 Upsert 후 새 revision 이 생성되지만,
			//   그 값은 ledger 가 결정할 사실이므로 put 이 추정하지 않는다.
			//
			// 두 경우 모두 RevisionPending=true 이다.
			proto.RevisionPending = true

			if exists {
				proto.Key.Revision = k.Revision

				// 현재 revision 의 put 상태를 관측 대기열에 넣는다.
				//
				// 후보 여부는 이미 결정되었다(아래에서 무조건 추가한다).
				// live 에서는 새 revision 이 생겨 예산이 리셋되므로
				// 현재 revision 이 VERIFIED 든 소진 FAILED 든 제외 사유가
				// 아니다. 다만 같은 파일이 반복 실패해 온 사실은
				// 운영자가 보아야 하므로 리포트에는 남긴다.
				*changedObs = append(*changedObs, ledger.NameRev{
					FileName: n,
					Revision: k.Revision,
				})
			} else {
				proto.Key.Revision = 0
			}

			*cands = append(*cands, proto)
			continue
		}

		res, err := r.DB.UpsertCommon(ctx, ledger.CommonInput{
			FileName:          n,
			BaseName:          domain.BaseName(n),
			Category:          job.Category,
			Size:              e.Size,
			MTime:             mtime,
			Origin:            domain.OriginLocal,
			IngressVerifiedAt: r.now().UTC().Unix(),
		})
		if err != nil {
			return fmt.Errorf("upsert %q: %w", n, err)
		}

		// 메모리 대조가 "신규·변경" 이라 했는데 DB 가 Unchanged 라
		// 답하면 두 관측이 어긋난 것이다. lock 아래 단일 실행에서는
		// 일어날 수 없어야 한다.
		//
		// 실행을 중단하지는 않는다 — 하류가 자기방어를 한다.
		// 재조회 revision 의 put 행이 VERIFIED 여도 BeginPut 의
		// WHERE 가드가 거부하여 건너뛰기가 되므로 이중 전송으로
		// 이어지지 않는다. 크게 알리되 회차를 죽이지 않는다.
		if res == ledger.ResultUnchanged {
			r.logf(
				"[PUT][WARN] upsert observed Unchanged for "+
					"new/changed file %q (category=%s) — "+
					"memory diff and ledger disagree",
				n, job.Category,
			)
		}

		upserted = append(upserted, n)
		protos[n] = proto
	}

	// revision 의 실제 값은 Common Ledger 에서 다시 읽어 확정한다.
	//
	// 산술 +1 로 live 값을 구성하지 않는다.
	// 정상 운영에서 신규·변경은 소수이므로 배치당 재조회 한 번이면 충분하다.
	if len(upserted) > 0 {
		fresh, err := r.DB.LookupCommon(ctx, job.Category, upserted)
		if err != nil {
			return fmt.Errorf(
				"re-lookup after upsert (dir %s): %w",
				b.Dir,
				err,
			)
		}

		for _, n := range upserted {
			k, ok := fresh[n]
			if !ok {
				return fmt.Errorf(
					"upserted %q missing from re-lookup",
					n,
				)
			}

			proto := protos[n]
			proto.Key.Revision = k.Revision
			*cands = append(*cands, proto)
		}
	}

	return nil
}

// resolveFailed 는 FAILED 예비들의 attempts 를 LookupPut 으로 읽어
// 재시도 가능 여부를 판정한다.
//
// 이 함수는 Unchanged 파일의 현재 revision 에만 적용한다.
// 변경 파일은 live 에서 새 revision 이 생기므로 현재 revision 의
// retry 소진 여부를 새 revision 의 후보 제외 사유로 사용하지 않는다.
func (r *Runner) resolveFailed(
	ctx context.Context,
	cat domain.Category,
	failed []failedRef,
	rep *CategoryReport,
	cands *[]Candidate,
) error {
	if len(failed) == 0 {
		return nil
	}

	keys := make([]ledger.NameRev, 0, len(failed))
	for _, f := range failed {
		keys = append(keys, f.nameRev)
	}

	states, err := r.DB.LookupPut(ctx, cat, keys)
	if err != nil {
		return fmt.Errorf("lookup put (failed subset): %w", err)
	}

	for _, f := range failed {
		st, ok := states[f.nameRev]
		if !ok {
			// LookupCommon 조인이 FAILED 를 봤는데 행이 사라졌다.
			// 단일 실행 lock 아래에서는 일어날 수 없으므로 중단한다.
			return fmt.Errorf(
				"put row vanished for %q rev=%d",
				f.nameRev.FileName,
				f.nameRev.Revision,
			)
		}

		if st.Status != domain.StatusFailed {
			return fmt.Errorf(
				"put row changed unexpectedly for %q rev=%d: status=%s",
				f.nameRev.FileName,
				f.nameRev.Revision,
				st.Status,
			)
		}

		if st.Attempts < int64(r.Opts.MaxRetries) {
			c := f.cand
			c.IsRetry = true
			rep.Retries++
			*cands = append(*cands, c)
			continue
		}

		rep.ExcludedExhausted++

		if len(rep.ExhaustedExamples) < exhaustedExampleCap {
			rep.ExhaustedExamples = append(
				rep.ExhaustedExamples,
				f.nameRev.FileName,
			)
		}
	}

	if rep.ExcludedExhausted > 0 {
		extra := ""
		if rep.ExcludedExhausted > len(rep.ExhaustedExamples) {
			extra = fmt.Sprintf(
				" 외 %d건",
				rep.ExcludedExhausted-len(rep.ExhaustedExamples),
			)
		}

		r.logf(
			"[PUT][WARN] category=%s exhausted=%d (MaxRetries=%d): %s%s",
			cat,
			rep.ExcludedExhausted,
			r.Opts.MaxRetries,
			strings.Join(rep.ExhaustedExamples, ", "),
			extra,
		)
	}

	return nil
}

// observeChangedCurrentPut 은 dry-run 변경 파일의 현재 revision 이
// put_ledger 에서 어떤 상태였는지를 집계한다.
//
// 후보를 만들지도 제외하지도 않는다. 호출 시점에 해당 파일들은 이미
// 후보에 들어가 있다. A안(2026-08-30 확정)의 취지가 여기에 있다 —
// 미래 revision 을 지어내지 않으면서, 현재 장부에 실제로 무엇이 있는지는
// 그대로 보여준다.
//
// 이 관측이 없으면 dry-run 화면에서 "이 파일은 이전 revision 에서
// 5번 실패했다" 는 사실이 사라진다. 같은 파일이 반복 실패해 온 패턴을
// 운영자가 볼 수 없게 된다.
//
// live 에서는 호출되지 않는다. changedObs 가 dry-run 분기에서만
// 채워지기 때문이다. live 는 어차피 새 revision 을 만들고, 이 조회는
// 순수 관측용이라 쓰기 경로에 조회를 더할 이유가 없다.
//
// resolveFailed 와 달리 행이 없는 것(NoRow)은 오류가 아니라 정상이다.
// 해당 revision 의 전송 이력이 아직 없다는 사실 자체가 관측값이다.
func (r *Runner) observeChangedCurrentPut(
	ctx context.Context,
	cat domain.Category,
	keys []ledger.NameRev,
	rep *CategoryReport,
) error {
	if len(keys) == 0 {
		return nil
	}

	states, err := r.DB.LookupPut(ctx, cat, keys)
	if err != nil {
		return fmt.Errorf("lookup put (changed observation): %w", err)
	}

	obs := &rep.DryRunChangedCurrentPut

	for _, key := range keys {
		st, ok := states[key]
		if !ok {
			obs.NoRow++
			continue
		}

		switch st.Status {
		case domain.StatusPending:
			obs.Pending++

		case domain.StatusInProgress:
			obs.InProgress++

		case domain.StatusVerified:
			obs.Verified++

		case domain.StatusFailed:
			if st.Attempts < int64(r.Opts.MaxRetries) {
				obs.FailedRetryable++
			} else {
				obs.Exhausted++
			}

		default:
			return fmt.Errorf(
				"unknown put status %q for %q rev=%d",
				st.Status,
				key.FileName,
				key.Revision,
			)
		}
	}

	return nil
}

// stationOf 는 dry-run 관측용 관측소 코드다. 정밀 파서가 아니다.
//
// 파일명 규칙의 주인은 domain 이다 (MatchesName). 이 함수는 현장 분포를
// 눈으로 확인하기 위한 집계일 뿐이므로, 여기에 형식 판정을 늘리지 않는다.
//
// RINEX2 short filename 과 RINEX3/4 long filename 모두
// 파일명 앞 4자가 station ID 이므로 동일 규칙으로 집계한다.
func stationOf(normName string) string {
	if len(normName) < 4 {
		return ""
	}

	return normName[:4]
}

func (r *Runner) now() time.Time {
	if r.Opts.Now != nil {
		return r.Opts.Now()
	}

	return time.Now()
}

func (r *Runner) logf(format string, args ...any) {
	l := r.Opts.Logger
	if l == nil {
		l = log.Default()
	}

	l.Printf(format, args...)
}
