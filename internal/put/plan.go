package put

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"SFTPClient/internal/ledger"
)

// finalize 는 전 카테고리 후보를 합쳐 정렬·절단하고,
// live 면 절단 후 대상만 PENDING 일괄 등록 대상으로 넘긴다.
func (r *Runner) finalize(
	ctx context.Context,
	all []Candidate,
	report *RunReport,
) ([]Candidate, int, error) {
	SortCandidates(all)

	report.TotalCandidates = len(all)

	kept := all

	// SeedMode 는 절단·PENDING 등록을 생략한다 (근거: RunOptions.SeedMode
	// 주석). revision 검증은 live 와 동일하게 통과해야 한다 — seed 도
	// 확정 revision 으로 SeedVerified 를 기록하기 때문이다.
	if r.Opts.SeedMode {
		for _, c := range kept {
			if c.RevisionPending || c.Key.Revision < 1 {
				return nil, 0, fmt.Errorf(
					"put: unresolved revision in seed candidate: "+
						"category=%s file=%q revision=%d",
					c.Key.Category,
					c.Key.FileName,
					c.Key.Revision,
				)
			}
		}

		return kept, 0, nil
	}

	if r.Opts.MaxFilesPerRun > 0 &&
		len(all) > r.Opts.MaxFilesPerRun {
		// full slice expression 으로 cap 을 닫는다.
		// all[:n] 만 쓰면 cap 이 len(all) 로 남아, 이후 누군가
		// append(kept, ...) 하는 순간 잘려 나간 후보의 메모리를
		// 조용히 덮어쓴다. 최종 목록이므로 여기서 닫는다.
		kept = all[:r.Opts.MaxFilesPerRun:r.Opts.MaxFilesPerRun]
		report.Cut = len(all) - len(kept)

		// 잘린 항목에는 put_ledger PENDING 을 등록하지 않는다.
		//
		// live 의 신규·변경 파일은 이 시점 이전에 common_ledger 에
		// 이미 관측값이 기록되어 있을 수 있다. 다음 회차에는
		// Unchanged + PutStatus=="" 경로가 다시 후보로 되살린다.
		//
		// 즉 "아무 장부 기록도 남기지 않는다" 가 아니라
		// "이번 실행 목록을 뜻하는 PENDING 을 만들지 않는다" 가 정확하다.

		// 세트 경계 절단 (게이트 ON) — 절단선에 걸려 쪼개진
		// 세트는 잔여 멤버를 이번 회차에서 통째로 빼 다음 회차로
		// 넘긴다. 게이트를 통과한 세트가 회차 경계에서 부분 도착
		// 창을 만들면 게이트의 목적이 무너지기 때문이다.
		// SetKey 가 빈 파일(게이트 OFF / 소속 유보)은 기존과 동일한
		// 파일 단위 절단 그대로다. 빠진 만큼 잘린 쪽에서 채워 넣지
		// 않는다 — 상한은 "이하" 보장이지 "정확히" 보장이 아니고,
		// 채움은 절단을 비결정적으로 만든다.
		//
		// dropSplitSets 는 kept 의 backing array 를 재사용하므로
		// 위에서 닫은 cap 이 유지된다.
		if kept2, moved := dropSplitSets(kept, all[len(kept):]); moved > 0 {
			kept = kept2
			report.Cut += moved

			r.logf(
				"[SET] MaxFilesPerRun 절단선을 세트 경계로 조정: "+
					"moved=%d (쪼개진 세트의 잔여 멤버를 다음 회차로 이월)",
				moved,
			)
		}
	}

	// Cut WARN 은 Print 가 아니라 실행 자체가 남긴다.
	// Print 는 선택적 표시 계층이라, Run 만 호출하는 경로에서
	// WARN 이 증발하면 안 된다. 태그는 [PUT][WARN] 으로 고정한다 —
	// dry-run 에서 [DRYRUN][WARN] 이 되면 grep '\[PUT\]\[WARN\]'
	// 수집에서 빠진다. (소진 WARN 과 같은 규약)
	if report.Cut > 0 {
		r.logf(
			"[PUT][WARN] MaxFilesPerRun limit reached: cut=%d; "+
				"remaining files will be recalculated next run",
			report.Cut,
		)
	}

	if r.Opts.DryRun {
		return kept, 0, nil
	}

	keys := make([]ledger.PutKey, 0, len(kept))

	for _, c := range kept {
		// RevisionPending 은 dry-run 신규/변경 Candidate 에만 허용된다.
		//
		// main 이 현재 live 모드를 막더라도, transport 도입 후 전송이 열렸을 때
		// 미확정 revision 이 put_ledger 로 흘러가는 것을 여기서 막는다.
		if c.RevisionPending {
			return nil, 0, fmt.Errorf(
				"put: unresolved revision in live candidate: "+
					"category=%s file=%q revision=%d",
				c.Key.Category,
				c.Key.FileName,
				c.Key.Revision,
			)
		}

		// 위 검사와 조건이 겹쳐 보이지만 원인이 다르므로 분리한다.
		//
		//	RevisionPending  dry-run 후보가 live 경로로 새어 들어옴
		//	Revision < 1     revision 확정 자체에 실패함
		//
		// 메시지가 갈려야 어느 쪽인지 진단이 된다.
		if c.Key.Revision < 1 {
			return nil, 0, fmt.Errorf(
				"put: invalid revision in live candidate: "+
					"category=%s file=%q revision=%d",
				c.Key.Category,
				c.Key.FileName,
				c.Key.Revision,
			)
		}

		keys = append(keys, c.Key)
	}

	// 재시도(IsRetry)나 고아 PENDING 도 함께 넘긴다.
	//
	// InsertPendingBatch 는 신규 행만 INSERT 하고 기존
	// (category, file_name, revision)은 ON CONFLICT DO NOTHING 으로
	// 보존한다. 따라서 FAILED/PENDING 을 별도 분기해 제외할 필요가 없다.
	//
	// PENDING 등록 트랜잭션이 실패하면 Worker 를 시작하지 않는다.
	// 후보 목록을 nil 로 반환하여 호출자가 전송할 목록을 얻지 못하게 한다.
	if err := r.DB.InsertPendingBatch(ctx, keys); err != nil {
		return nil, 0, fmt.Errorf(
			"put: register pending: %w",
			err,
		)
	}

	// InsertPendingBatch 는 ON CONFLICT DO NOTHING 이므로
	// len(keys)는 실제 새 INSERT 행 수가 아니라 이번 batch 대상 수다.
	return kept, len(keys), nil
}

// SortCandidates 는 MaxFilesPerRun 절단 전에 적용하는 결정적 정렬이다.
//
// 밀린 데이터를 오래된 것부터 배수하며, 동일 시각의 결과도
// 실행마다 바뀌지 않도록 보조 키를 둔다.
//
//	When asc → file_name asc → category asc
//
// category 를 세 번째 키로 두는 이유는 RINEX3 과 RINEX4 가 같은
// long filename 을 가질 수 있기 때문이다. 앞 두 키만으로는 그 둘의
// 순서가 정해지지 않는다.
func SortCandidates(cands []Candidate) {
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]

		if !a.When.Equal(b.When) {
			return a.When.Before(b.When)
		}

		if a.Key.FileName != b.Key.FileName {
			return a.Key.FileName < b.Key.FileName
		}

		return a.Key.Category < b.Key.Category
	})
}

// evaluated 는 이 카테고리에서 변경 판정에 도달한 파일-회차 수다
// (UNIT4 §3 — suppressed_ratio 의 분모).
//
// 새 증가 카운터를 두지 않고 판정의 네 출구 합으로 계산한다. 판정
// 루프에 다섯 번째 계수를 깔면 다음 분기 추가에서 분모만 조용히
// 어긋나기 때문이다 (§3). Ingress 거부·중복·임시 파일·hash unstable
// 등 판정에 도달하지 못한 입력은 분모 밖이다 — 분모에서 뺐다고 그
// 파일이 사라지는 것은 아니며, 각각 ingress_reject / skipped /
// hash unstable= 줄에서 본다.
func (c CategoryReport) evaluated() int {
	return c.Unchanged + c.New + c.Changed + c.MetadataOnly
}

// suppressedRatio 는 MetadataOnly / evaluated 의 표시 문자열이다.
//
// 분모 0 은 "정상 0%" 가 아니라 판정이 아무것도 보지 못한 회차라는
// 신호이므로 n/a 로 표시한다 (UNIT4 §3 — 같은 줄의 dirs/missing/
// listed/errs 와 함께 읽는다).
func (c CategoryReport) suppressedRatio() string {
	ev := c.evaluated()
	if ev == 0 {
		return "n/a"
	}

	return fmt.Sprintf(
		"%.1f%%",
		100*float64(c.MetadataOnly)/float64(ev),
	)
}

// mode 는 요약 줄의 모집단 식별자다 (UNIT4 §5 — live/dryrun/seed).
//
// seed 가 dryrun 보다 우선한다. seed 는 지문 판정 경로가 달라 어느
// 분포와도 섞이면 안 되는 모집단이기 때문이다 (§4). 기존 [DRYRUN]
// 태그는 유지하되, 태그만으로는 seed 가 live 와 구별되지 않으므로
// mode= 필드가 그 구멍을 막는다 (§0.1-2).
func (rr RunReport) mode() string {
	switch {
	case rr.SeedMode:
		return "seed"
	case rr.DryRun:
		return "dryrun"
	default:
		return "live"
	}
}

// rangeLabel 은 스캔 창 이름이다. 미설정을 조용한 기본값으로 가리지
// 않는다 — 모집단 불명은 불명으로 찍혀야 분포 분석에서 걸러진다.
func (rr RunReport) rangeLabel() string {
	if rr.Range == "" {
		return "unset"
	}

	return rr.Range
}

// Print 는 사람이 읽는 실행 요약이다.
// logging 패키지는 MVP 이후이므로 표준 log 로 충분하다.
func (rr RunReport) Print(l *log.Logger) {
	if l == nil {
		l = log.Default()
	}

	tag := "[PUT]"
	if rr.DryRun {
		tag = "[DRYRUN]"
	}

	for _, c := range rr.Categories {
		// mode= / range= / evaluated= / suppressed_ratio= 는 UNIT4 의
		// 확장이다. 새 [DRIFT] 줄을 만들지 않고 기존 요약 줄을 넓힌다 —
		// 같은 사실의 출처가 둘이 되면 둘이 어긋나는 날이 온다 (§5).
		l.Printf(
			"%s mode=%s range=%s category=%s dirs=%d missing=%d listed=%d errs=%d "+
				"unchanged=%d new=%d changed=%d metadata_only=%d "+
				"evaluated=%d suppressed_ratio=%s",
			tag,
			rr.mode(),
			rr.rangeLabel(),
			c.Category,
			c.Scan.Dirs,
			c.Scan.Missing,
			c.Scan.Files,
			c.Scan.Errs,
			c.Unchanged,
			c.New,
			c.Changed,
			c.MetadataOnly,
			c.evaluated(),
			c.suppressedRatio(),
		)

		// 해시 계측은 활동이 있었을 때만 한 줄로 남긴다 (설계 v3 §6).
		// 성공 로그는 집계 중심이라는 로그 정책(설계안 14)을 따른다.
		if hashed := c.HashNew.Files + c.HashChanged.Files +
			c.HashDrift.Files + c.HashBackfill.Files; hashed > 0 ||
			c.HashFailed > 0 || c.HashUnstable > 0 ||
			c.BackfillDeferred > 0 {
			l.Printf(
				"%s   hash new=%d/%dB changed=%d/%dB drift=%d/%dB "+
					"backfill=%d/%dB ms=%d failed=%d unstable=%d deferred=%d",
				tag,
				c.HashNew.Files, c.HashNew.Bytes,
				c.HashChanged.Files, c.HashChanged.Bytes,
				c.HashDrift.Files, c.HashDrift.Bytes,
				c.HashBackfill.Files, c.HashBackfill.Bytes,
				c.HashNew.Millis+c.HashChanged.Millis+
					c.HashDrift.Millis+c.HashBackfill.Millis,
				c.HashFailed,
				c.HashUnstable,
				c.BackfillDeferred,
			)
		}

		if len(c.Rejected) > 0 {
			l.Printf(
				"%s   ingress_reject=%s",
				tag,
				formatSortedCounts(c.Rejected),
			)

			// 사유별 파일명 예시.
			// 개수만 보면 사람이 디스크를 직접 뒤져야 한다.
			for _, reason := range sortedKeys(c.RejectedExamples) {
				names := c.RejectedExamples[reason]

				extra := ""
				if c.Rejected[reason] > len(names) {
					extra = fmt.Sprintf(
						" 외 %d건",
						c.Rejected[reason]-len(names),
					)
				}

				l.Printf(
					"%s     %s: %s%s",
					tag,
					reason,
					strings.Join(names, ", "),
					extra,
				)
			}
		}

		l.Printf(
			"%s   candidates=%d (retry=%d) excluded={verified:%d "+
				"in_progress:%d exhausted:%d download_origin:%d} "+
				"skipped={dir:%d part:%d dup:%d}",
			tag,
			c.Candidates,
			c.Retries,
			c.ExcludedVerified,
			c.ExcludedInProgress,
			c.ExcludedExhausted,
			c.ExcludedDownloadOrigin,
			c.SkippedDirs,
			c.SkippedPart,
			c.SkippedDuplicate,
		)

		if c.SetGate {
			l.Printf(
				"%s   set_gate: held=%d unparsed=%d",
				tag,
				c.SetHeld,
				c.SetUnparsed,
			)
		}

		// A안:
		// 변경 파일의 후보 여부를 결정하는 값이 아니라,
		// 현재 revision 에 실제로 존재하는 put 상태의 관측값이다.
		if rr.DryRun {
			obs := c.DryRunChangedCurrentPut
			observed :=
				obs.NoRow +
					obs.Pending +
					obs.InProgress +
					obs.Verified +
					obs.FailedRetryable +
					obs.Exhausted

			if observed > 0 {
				l.Printf(
					"%s   changed_current_put={"+
						"no_row:%d pending:%d in_progress:%d "+
						"verified:%d failed_retryable:%d exhausted:%d}",
					tag,
					obs.NoRow,
					obs.Pending,
					obs.InProgress,
					obs.Verified,
					obs.FailedRetryable,
					obs.Exhausted,
				)
			}
		}

		l.Printf(
			"%s   ext=%s stations=%d elapsed=%s",
			tag,
			formatSortedCounts(c.ExtCount),
			c.StationCount,
			c.Elapsed.Round(time.Millisecond),
		)
	}

	l.Printf(
		"%s total: candidates=%d cut=%d pending_batch=%d elapsed=%s",
		tag,
		rr.TotalCandidates,
		rr.Cut,
		rr.Registered,
		rr.Elapsed.Round(time.Millisecond),
	)

	if rr.DryRun {
		l.Printf(
			"%s revision 은 preview 값이다. live 에서는 Upsert 결과의 확정 revision 을 사용한다.",
			tag,
		)
	}
}

// formatSortedCounts 는 map 을 키 정렬 순서로 찍는다.
//
// %v 는 실행마다 키 순서가 바뀌어 현장 로그 diff 를 지저분하게 만든다.
// 리포트는 사람이 비교하는 산출물이므로 순서를 고정한다.
func formatSortedCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')

	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}

		fmt.Fprintf(&b, "%s:%d", k, m[k])
	}

	b.WriteByte('}')

	return b.String()
}

// sortedKeys 는 map 순회 순서를 고정한다.
//
// formatSortedCounts 와 같은 이유다. Go 의 map 순회는 실행마다 순서가
// 바뀌므로, 사람이 회차 간 비교하는 리포트에서는 정렬해야 한다.
func sortedKeys(m map[string][]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
