package put

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/scan"
)

// resend 게이트·수동 재시도 (v4 §4.3·§5, 커밋 6) 테스트.
//
// 고정하는 계약:
//   - resend 의 보류 판정은 ResendMinKinds 다. 모든 최소 종이 있으면
//     전송, 하나라도 없으면 계속 보류(AND). nil 이면 무조건 우회(§5.3).
//   - 세트 정체성(SetKey·경계 절단)은 계속 RequiredKinds 기준이다 —
//     우회로 통과한 세트도 절단선에서 쪼개지 않는다(§5.5).
//   - 우회 전송 세트는 [SET][RESEND] 로 세트 키·있는 종·없는 종을 남긴다.
//   - 소진 재무장은 RearmExhausted(수동)에서만 일어나고, attempts 를
//     되돌리지 않으며, RetryCeiling=attempts+1 로 실제 BeginPut 을 딱
//     한 번 통과해 VERIFIED 까지 간다(§10 구현 주의).
//   - RearmExhausted 는 Resend 전제, Resend 는 SeedMode 와 배타다.

const (
	resendSonpMN = "SONP00KOR_R_20260010300_01H_MN.rnx.gz"
)

// resendJobs 는 siteTestFixture 의 jobs 에 resend 게이트 설정을 얹는다.
func resendJobs(
	jobs []CategoryJob,
	required []string,
	minKinds []string,
) []CategoryJob {
	jobs[0].RequiredKinds = required
	jobs[0].ResendMinKinds = minKinds

	return jobs
}

// TestRunner_ResendGate_MinKinds — 최소 종 충족/미충족 (§5.2).
// dbon 세트에 mo 만 있다. 평소 게이트({mo,mn})는 보류하고,
// resend 게이트({mo})는 통과시킨다. mn 만 있으면 resend 도 보류한다.
func TestRunner_ResendGate_MinKinds(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	required := []string{"mo", "mn"}
	minKinds := []string{"mo"}

	run := func(names []string, resend bool) (int, CategoryReport, string) {
		t.Helper()

		var buf bytes.Buffer

		r, jobs, _ := siteTestFixture(
			t, db, when, names, nil,
			func(o *RunOptions) {
				o.DryRun = true
				o.Resend = resend
				o.Logger = log.New(&buf, "", 0)
			},
		)

		jobs = resendJobs(jobs, required, minKinds)

		kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
		if err != nil {
			t.Fatalf("Run(resend=%t): %v", resend, err)
		}

		return len(kept), report.Categories[0], buf.String()
	}

	// 평소 실행 — mo 단독 세트는 보류.
	kept, rep, _ := run([]string{siteDbonMO}, false)
	if kept != 0 || rep.SetHeld != 1 {
		t.Fatalf("평소 kept=%d held=%d, want 0/1", kept, rep.SetHeld)
	}

	// resend — 최소 종(mo) 충족, 통과 + [SET][RESEND] 관측.
	kept, rep, logs := run([]string{siteDbonMO}, true)
	if kept != 1 || rep.SetHeld != 0 {
		t.Fatalf("resend kept=%d held=%d, want 1/0", kept, rep.SetHeld)
	}

	if !strings.Contains(logs, "[SET][RESEND]") ||
		!strings.Contains(logs, "missing=mn") {
		t.Fatalf("[SET][RESEND] 우회 관측 로그 누락:\n%s", logs)
	}

	// resend — 최소 종(mo) 미충족(mn 만), 계속 보류.
	kept, rep, logs = run([]string{siteDbonMN}, true)
	if kept != 0 || rep.SetHeld != 1 {
		t.Fatalf("min 미충족 kept=%d held=%d, want 0/1", kept, rep.SetHeld)
	}

	if strings.Contains(logs, "[SET][RESEND]") {
		t.Fatalf("보류된 세트가 우회 로그에 나왔다:\n%s", logs)
	}
}

// TestRunner_ResendGate_NilMinBypassesAll — 키 없음(nil)은 무조건
// 우회다 (§5.3 "게이트 ON, 키 없음"). 보류 게이트는 꺼지지만 우회
// 관측은 남는다.
func TestRunner_ResendGate_NilMinBypassesAll(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	var buf bytes.Buffer

	r, jobs, _ := siteTestFixture(
		t, db, when, []string{siteDbonMO}, nil,
		func(o *RunOptions) {
			o.DryRun = true
			o.Resend = true
			o.Logger = log.New(&buf, "", 0)
		},
	)

	jobs = resendJobs(jobs, []string{"mo", "mn"}, nil)

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 1 || rep.SetHeld != 0 {
		t.Fatalf("kept=%d held=%d, want 1/0", len(kept), rep.SetHeld)
	}

	if rep.SetGate {
		t.Fatal("보류 게이트가 켜진 것으로 보고되었다 (nil = 무조건 우회)")
	}

	if !strings.Contains(buf.String(), "[SET][RESEND]") {
		t.Fatalf("전면 우회에서도 우회 관측은 남아야 한다:\n%s", buf.String())
	}
}

// TestRunner_ResendCutDoesNotSplitBypassedSet — MaxFilesPerRun 세트
// 경계 절단은 resend 에도 적용되고, 우회로 통과한 세트도 쪼개지 않는다
// (§5.5). 보류 게이트가 전면 우회(nil)여도 SetKey 는 RequiredKinds
// 기준으로 새겨져야 성립한다 — keyGate 분리의 회귀 테스트다.
func TestRunner_ResendCutDoesNotSplitBypassedSet(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	names := []string{siteDbonMO, siteDbonMN, siteSonpMO, resendSonpMN}

	r, jobs, _ := siteTestFixture(
		t, db, when, names, nil,
		func(o *RunOptions) {
			o.DryRun = true
			o.Resend = true
			o.MaxFilesPerRun = 3
		},
	)

	jobs = resendJobs(jobs, []string{"mo", "mn"}, nil)

	kept, _, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// 정렬(이름순) 후 절단 3 은 sonp 세트를 가르므로, kept 쪽 sonp
	// 멤버가 제거되어 dbon 세트 2건만 남아야 한다.
	if len(kept) != 2 {
		t.Fatalf("kept=%d, want 2 (세트가 쪼개졌다)", len(kept))
	}

	for _, c := range kept {
		if c.SetKey == "" {
			t.Fatalf("우회 통과 후보의 SetKey 가 비었다: %q", c.Key.FileName)
		}

		if !strings.HasPrefix(c.Key.FileName, "dbon") {
			t.Fatalf("절단 결과에 sonp 멤버가 남았다: %q", c.Key.FileName)
		}
	}
}

// exhaustPut 은 hash 지문이 있는 common 행을 만들고 BeginPut/FailPut 을
// times 회 반복해 FAILED && attempts=times 인 소진 행을 만든다.
func exhaustPut(
	t *testing.T,
	db *ledger.DB,
	name string,
	size int64,
	mtime int64,
	times int,
) ledger.PutKey {
	t.Helper()

	ctx := context.Background()

	key := insertKnownForHashTest(t, db, name, size, mtime, siteTestHash, false)

	if err := db.InsertPendingBatch(ctx, []ledger.PutKey{key}); err != nil {
		t.Fatalf("InsertPendingBatch: %v", err)
	}

	for i := 0; i < times; i++ {
		if err := db.BeginPut(
			ctx, key, "/out/x", "/out/x.part", size, times,
		); err != nil {
			t.Fatalf("BeginPut #%d: %v", i+1, err)
		}

		if err := db.FailPut(ctx, key, "test: injected failure"); err != nil {
			t.Fatalf("FailPut #%d: %v", i+1, err)
		}
	}

	return key
}

// TestRunner_RearmExhausted_ManualOnly — 소진 행은 평소·자동 resend
// 에서는 계속 제외되고, RearmExhausted(수동)에서만 RetryCeiling=
// attempts+1 을 실은 재시도 후보가 된다 (§4.3).
func TestRunner_RearmExhausted_ManualOnly(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()
	mtime := when.Add(-time.Hour).Unix()

	const maxRetries = 2

	norm := domain.NormalizeName(siteDbonMO)
	exhaustPut(t, db, norm, siteTestSize, mtime, maxRetries)

	run := func(rearm bool) ([]Candidate, CategoryReport) {
		t.Helper()

		r, jobs, _ := siteTestFixture(
			t, db, when, []string{siteDbonMO}, nil,
			func(o *RunOptions) {
				o.MaxRetries = maxRetries
				o.Resend = true
				o.RearmExhausted = rearm
			},
		)

		kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
		if err != nil {
			t.Fatalf("Run(rearm=%t): %v", rearm, err)
		}

		return kept, report.Categories[0]
	}

	// 재무장 없는 resend — 소진 제외 유지 (자동 resend 와 같은 조건).
	kept, rep := run(false)
	if len(kept) != 0 || rep.ExcludedExhausted != 1 || rep.Rearmed != 0 {
		t.Fatalf(
			"kept=%d exhausted=%d rearmed=%d, want 0/1/0",
			len(kept), rep.ExcludedExhausted, rep.Rearmed,
		)
	}

	// 수동 재무장 — 후보로 올라오고 상한은 attempts+1 이다.
	kept, rep = run(true)
	if len(kept) != 1 || rep.Rearmed != 1 || rep.Retries != 1 ||
		rep.ExcludedExhausted != 0 {
		t.Fatalf(
			"kept=%d rearmed=%d retries=%d exhausted=%d, want 1/1/1/0",
			len(kept), rep.Rearmed, rep.Retries, rep.ExcludedExhausted,
		)
	}

	c := kept[0]
	if !c.IsRetry || c.RetryCeiling != maxRetries+1 {
		t.Fatalf(
			"IsRetry=%t ceiling=%d, want true/%d",
			c.IsRetry, c.RetryCeiling, maxRetries+1,
		)
	}

	// Run 은 전송하지 않는다 — attempts 는 그대로다 (0 리셋 금지).
	st, err := db.LookupPut(ctx, xferTestCat, []ledger.NameRev{
		{FileName: norm, Revision: c.Key.Revision},
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range st {
		if s.Attempts != maxRetries || s.Status != domain.StatusFailed {
			t.Fatalf(
				"attempts=%d status=%s, want %d/FAILED (Run 이 장부를 바꿨다)",
				s.Attempts, s.Status, maxRetries,
			)
		}
	}
}

// TestTransfer_RearmedExhaustedRunsToVerified — §10 구현 주의의 본검증.
// 필터 통과가 아니라 실제 BeginPut 가드를 지나 VERIFIED 가 되는지,
// attempts 가 보존값+1 인지 확인한다. 상한 없는 소진 후보는 여전히
// ErrNotCandidate 로 걸러진다 (재무장이 상한을 실어야만 통과).
func TestTransfer_RearmedExhaustedRunsToVerified(t *testing.T) {
	db, dbPath := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db) // MaxRetries=5

	const exhausted = 5

	content := strings.Repeat("x", int(siteTestSize))
	mtime := int64(1_700_000_100)

	build := func(name string, ceiling int64) Candidate {
		t.Helper()

		norm := domain.NormalizeName(name)
		key := exhaustPut(t, db, norm, siteTestSize, mtime, exhausted)

		return Candidate{
			Key:          key,
			LocalPath:    seedLocal(t, name, content),
			Size:         siteTestSize,
			When:         time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),
			IsRetry:      true,
			RetryCeiling: ceiling,
		}
	}

	rearmed := build(siteDbonMO, exhausted+1)
	stillExhausted := build(siteSonpMO, 0) // 재무장 없음 — 대조군

	rep, err := r.Transfer(
		context.Background(), up, xferJobs(t),
		[]Candidate{rearmed, stillExhausted},
	)
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}

	if rep.Attempted != 1 || rep.Verified != 1 || rep.NotCandidate != 1 {
		t.Fatalf(
			"rep=%+v, want Attempted=1 Verified=1 NotCandidate=1",
			rep,
		)
	}

	// 재무장 후보 — BeginPut 가드 통과, VERIFIED, attempts=보존+1.
	row := readPutRaw(t, dbPath, rearmed.Key)
	if row.status != string(domain.StatusVerified) || row.attempts != exhausted+1 {
		t.Fatalf(
			"rearmed status=%s attempts=%d, want VERIFIED/%d",
			row.status, row.attempts, exhausted+1,
		)
	}

	// 대조군 — 상한 없이는 소진 행이 그대로다.
	row = readPutRaw(t, dbPath, stillExhausted.Key)
	if row.status != string(domain.StatusFailed) || row.attempts != exhausted {
		t.Fatalf(
			"control status=%s attempts=%d, want FAILED/%d",
			row.status, row.attempts, exhausted,
		)
	}
}

// TestRunner_CheckInput_ResendCombos — 잘못된 조합은 입구에서 거부한다.
func TestRunner_CheckInput_ResendCombos(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	tests := []struct {
		name string
		tune func(*RunOptions)
	}{
		{
			"RearmExhausted 는 Resend 전제",
			func(o *RunOptions) { o.RearmExhausted = true },
		},
		{
			"Resend 와 SeedMode 는 배타",
			func(o *RunOptions) {
				o.Resend = true
				o.SeedMode = true
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, jobs, _ := siteTestFixture(
				t, db, when, []string{siteDbonMO}, nil, tt.tune,
			)

			_, _, err := r.Run(
				context.Background(), jobs,
				scan.Range{From: when, To: when},
			)
			if err == nil {
				t.Fatal("잘못된 조합이 통과했다")
			}
		})
	}
}

// ── 게이트 우회 경로 탐색 (커밋 6 리뷰) ──────────────────────────
//
// "resend 로 평소 게이트를 우회할 수 있는가"를 네 방향에서 친다.
// 각 테스트는 우회가 일어나면 실패한다.

// TestRunner_ResendMinKinds_IgnoredWhenNotResend — Resend=false 에서는
// ResendMinKinds 가 배선되어 있어도 평소 게이트(RequiredKinds)가
// 그대로다. 이 계약이 깨지면 정시 PUT 이 최소 종만으로 미완성 세트를
// 내보낸다 — 커밋 7 이 ① 단계에 Resend 를 잘못 켜면 나는 사고다.
func TestRunner_ResendMinKinds_IgnoredWhenNotResend(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	r, jobs, _ := siteTestFixture(
		t, db, when, []string{siteDbonMO}, nil,
		func(o *RunOptions) { o.DryRun = true },
	)

	jobs = resendJobs(jobs, []string{"mo", "mn"}, []string{"mo"})

	kept, report, err := r.Run(
		context.Background(), jobs,
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 0 || rep.SetHeld != 1 {
		t.Fatalf(
			"kept=%d held=%d, want 0/1 — Resend=false 인데 최소 종으로 통과했다",
			len(kept), rep.SetHeld,
		)
	}
}

// TestRunner_ResendMinKinds_GateOffStaysOff — 게이트 OFF 기관
// (RequiredKinds 없음)에서는 ResendMinKinds 가 남아 있어도 보류가
// 생기지 않는다 (§5.4 — 게이트를 켠 기관에서만 의미가 있다).
//
// 우회의 반대 방향 사고다: resend 가 없던 게이트를 만들어 내면,
// 최소 종이 없는 파일이 오류 한 번 없이 영원히 전송되지 않는다.
// config 는 이 조합을 거부하지만(커밋 2), put 은 job 조립을 믿고
// 재검증하지 않으므로 여기서 계약을 고정한다.
func TestRunner_ResendMinKinds_GateOffStaysOff(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	r, jobs, _ := siteTestFixture(
		t, db, when, []string{siteDbonMN}, nil,
		func(o *RunOptions) {
			o.DryRun = true
			o.Resend = true
		},
	)

	// RequiredKinds 없음(게이트 OFF) + ResendMinKinds 잔류.
	jobs = resendJobs(jobs, nil, []string{"mo"})

	kept, report, err := r.Run(
		context.Background(), jobs,
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 1 || rep.SetHeld != 0 {
		t.Fatalf(
			"kept=%d held=%d, want 1/0 — 게이트 OFF 인데 resend 가 보류를 만들었다",
			len(kept), rep.SetHeld,
		)
	}

	if rep.SetGate {
		t.Fatal("게이트 OFF 인데 SetGate=true 로 보고되었다")
	}
}

// TestRunner_RearmDoesNotBypassHold — 재무장(v4 §4.3)이 게이트를
// 뚫지 못한다. 미완성 세트의 소진된 FAILED 멤버는 수동 resend 에서도
// 보류된다 — 재시도 대기열이 게이트 필터보다 뒤에 있으면 "미완성
// 세트의 FAILED 멤버만 새어 나가는" 구멍이 된다(runner.go 게이트 주석).
func TestRunner_RearmDoesNotBypassHold(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()
	mtime := when.Add(-time.Hour).Unix()

	const maxRetries = 2

	// mn 만 있는 세트 — resend 최소 종이 mo 이므로 보류 대상이다.
	// 그 유일한 멤버를 소진 상태로 만든다.
	exhaustPut(
		t, db,
		domain.NormalizeName(resendSonpMN),
		siteTestSize, mtime, maxRetries,
	)

	r, jobs, _ := siteTestFixture(
		t, db, when, []string{resendSonpMN}, nil,
		func(o *RunOptions) {
			o.MaxRetries = maxRetries
			o.Resend = true
			o.RearmExhausted = true
		},
	)

	jobs = resendJobs(jobs, []string{"mo", "mn"}, []string{"mo"})

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("2 회차 Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 0 || rep.Rearmed != 0 {
		t.Fatalf(
			"kept=%d rearmed=%d, want 0/0 — 보류 세트의 소진 행이 재무장으로 새어 나갔다",
			len(kept), rep.Rearmed,
		)
	}

	if rep.SetHeld != 1 {
		t.Fatalf("held=%d, want 1", rep.SetHeld)
	}
}

// TestRunner_ResendDoesNotRetransmitVerified — 이미 VERIFIED 인 파일은
// resend 게이트 우회와 무관하게 제외한다 (v4 §4.1). 이게 깨지면 정시
// 자동 resend 가 Retention 창의 완료 파일을 매시간 다시 보낸다.
func TestRunner_ResendDoesNotRetransmitVerified(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()
	mtime := when.Add(-time.Hour).Unix()
	norm := domain.NormalizeName(siteDbonMO)

	insertKnownForHashTest(t, db, norm, siteTestSize, mtime, siteTestHash, true)

	r, jobs, _ := siteTestFixture(
		t, db, when, []string{siteDbonMO}, nil,
		func(o *RunOptions) { o.Resend = true },
	)
	jobs = resendJobs(jobs, []string{"mo", "mn"}, nil)

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 0 || rep.ExcludedVerified != 1 {
		t.Fatalf(
			"kept=%d verified_excl=%d, want 0/1 — 완료 파일을 resend 가 다시 골랐다",
			len(kept), rep.ExcludedVerified,
		)
	}
}

// TestRunner_ResendMinKinds_ANDNotOR — 최소 종은 RequiredKinds 와 같은
// AND 다 (§5.2). mo·mn 둘 다 최소인데 mo 만 있으면 보류여야 한다.
// OR 로 구현되면 관측 파일만으로 미완성 세트가 목적지에 쌓인다.
func TestRunner_ResendMinKinds_ANDNotOR(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	r, jobs, _ := siteTestFixture(
		t, db, when, []string{siteDbonMO}, nil,
		func(o *RunOptions) {
			o.DryRun = true
			o.Resend = true
		},
	)
	jobs = resendJobs(jobs, []string{"mo", "mn"}, []string{"mo", "mn"})

	kept, report, err := r.Run(
		context.Background(), jobs, scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 0 || rep.SetHeld != 1 {
		t.Fatalf(
			"kept=%d held=%d, want 0/1 — 최소 종이 OR 로 통과했다",
			len(kept), rep.SetHeld,
		)
	}
}

// TestRunner_PermanentRemoteFailureNotRetriedEveryHour — v4 §4.3 사고.
// 원격 권한 오류로 MaxRetries 까지 소진된 뒤, 자동 resend(재무장 없음)가
// 하루(24회) 돌아도 attempts 가 늘지 않고 후보에도 안 오른다.
// RearmExhausted 가 자동에 켜지면 매시간 BeginPut 이 성공해 상한이 사라진다.
func TestRunner_PermanentRemoteFailureNotRetriedEveryHour(t *testing.T) {
	db, dbPath := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()
	mtime := when.Add(-time.Hour).Unix()
	const maxRetries = 2

	norm := domain.NormalizeName(siteDbonMO)
	key := exhaustPut(t, db, norm, siteTestSize, mtime, maxRetries)

	runAuto := func() []Candidate {
		t.Helper()

		r, jobs, _ := siteTestFixture(
			t, db, when, []string{siteDbonMO}, nil,
			func(o *RunOptions) {
				o.MaxRetries = maxRetries
				o.Resend = true
			},
		)

		kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
		if err != nil {
			t.Fatalf("auto Run: %v", err)
		}

		if report.Categories[0].Rearmed != 0 || report.Categories[0].ExcludedExhausted != 1 {
			t.Fatalf(
				"auto rearmed=%d exhausted=%d, want 0/1",
				report.Categories[0].Rearmed,
				report.Categories[0].ExcludedExhausted,
			)
		}

		return kept
	}

	if kept := runAuto(); len(kept) != 0 {
		t.Fatalf("소진 직후 자동 resend kept=%d", len(kept))
	}

	content := strings.Repeat("x", int(siteTestSize))
	r := xferRunner(db)
	r.Opts.MaxRetries = maxRetries
	up := newFakeUploader()
	up.failOn("UploadPart", 1, errors.New("remote permission denied"))

	_, err := r.Transfer(ctx, up, xferJobs(t), []Candidate{{
		Key:          key,
		LocalPath:    seedLocal(t, siteDbonMO, content),
		Size:         siteTestSize,
		When:         when,
		IsRetry:      true,
		RetryCeiling: int64(maxRetries) + 1,
	}})
	if err != nil {
		t.Fatalf("수동 재무장 Transfer: %v", err)
	}

	row := readPutRaw(t, dbPath, key)
	if row.status != string(domain.StatusFailed) || row.attempts != int64(maxRetries)+1 {
		t.Fatalf(
			"재무장 실패 후 status=%s attempts=%d, want FAILED/%d",
			row.status, row.attempts, maxRetries+1,
		)
	}

	for hour := 0; hour < 24; hour++ {
		kept := runAuto()
		if len(kept) != 0 {
			t.Fatalf("D+0h+%d 자동 resend 가 소진 파일을 다시 골랐다 kept=%d", hour, len(kept))
		}

		row = readPutRaw(t, dbPath, key)
		if row.attempts != int64(maxRetries)+1 || row.status != string(domain.StatusFailed) {
			t.Fatalf(
				"h=%d attempts=%d status=%s — 매시간 재시도가 상한을 지웠다",
				hour, row.attempts, row.status,
			)
		}
	}
}
