package put

import (
	"context"
	"io"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// site 필터 (SITE v1 §4·§6, resend 커밋 5) 테스트.
//
// 고정하는 계약:
//   - Sites 비움 = 필터 없음. site 검사 자체를 하지 않아 카운터가 0 이고
//     기존 동작이 불변이다.
//   - 선택 밖 파일은 등록 전 필터로 빠진다 — common_ledger 행이 생기지
//     않고, 기존 행(revision·mtime·지문·put attempts)이 불변이며,
//     해시(자연 백필 포함)가 호출되지 않는다.
//   - site 지정 시 식별 불가(유보) 파일은 제외하고 SiteUnknown 으로
//     센다. fallback 은 없다.
//   - 세트 키가 관측소를 포함하므로, 다른 site 의 제외가 선택된 site 의
//     세트 완성도 판정을 바꾸지 않는다.
//   - live 와 dry-run 은 같은 site 선택을 하고, dry-run 은 장부를 바꾸지
//     않는다.

const (
	siteDbonMO = "DBON00KOR_R_20260010300_01H_01S_MO.crx.gz"
	siteDbonMN = "DBON00KOR_R_20260010300_01H_MN.rnx.gz"
	siteSonpMO = "SONP00KOR_R_20260010300_01H_01S_MO.crx.gz"

	// longSetKeyKind 가 유보하는 이름 — site 도 유보된다 (커밋 3).
	siteUnknownName = "backup_old_2026_temp.rnx.gz"

	siteTestSize = int64(100)
	siteTestHash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

// siteTestFixture 는 다중 파일 runner 를 만든다. hashTestRunner 는 파일
// 하나 전제라 재사용하지 않는다. 모든 파일이 같은 size·mtime 을 가져
// stubHasher 하나로 안정 판정을 통과한다.
func siteTestFixture(
	t *testing.T,
	db *ledger.DB,
	when time.Time,
	names []string,
	requiredKinds []string,
	tune func(*RunOptions),
) (*Runner, []CategoryJob, *stubHasher) {
	t.Helper()

	jobs := xferJobs(t)
	jobs[0].RequiredKinds = requiredKinds

	mtime := when.Add(-time.Hour).Unix()
	dir := jobs[0].LocalPath.Expand(when)

	entries := make([]scan.Entry, 0, len(names))
	for _, name := range names {
		entries = append(entries, scan.Entry{
			Name:  name,
			Size:  siteTestSize,
			MTime: time.Unix(mtime, 0).UTC(),
		})
	}

	h := &stubHasher{result: HashResult{
		Hash:    siteTestHash,
		PreSize: siteTestSize, PostSize: siteTestSize,
		PreMTime: mtime, PostMTime: mtime,
		Bytes: siteTestSize,
	}}

	opts := RunOptions{
		MaxRetries: 5,
		Logger:     log.New(io.Discard, "", 0),
		Now:        func() time.Time { return when },
	}
	if tune != nil {
		tune(&opts)
	}

	return &Runner{
		Scanner:  scan.New(fakeLister{dirs: map[string][]scan.Entry{dir: entries}}),
		DB:       db,
		Verifier: verify.Verifier{},
		Hasher:   h,
		Opts:     opts,
	}, jobs, h
}

func siteTestWhen() time.Time {
	return time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
}

// TestRunner_SiteFilter_EmptySitesUnchanged — Sites 비움 = 기존 동작.
// 두 관측소 모두 후보가 되고 site 카운터는 0 이다.
func TestRunner_SiteFilter_EmptySitesUnchanged(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	r, jobs, h := siteTestFixture(
		t, db, when,
		[]string{siteDbonMO, siteSonpMO},
		nil,
		nil,
	)

	kept, report, err := r.Run(
		context.Background(), jobs,
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 2 || rep.SiteMismatch != 0 || rep.SiteUnknown != 0 {
		t.Fatalf(
			"kept=%d mismatch=%d unknown=%d, want 2/0/0",
			len(kept), rep.SiteMismatch, rep.SiteUnknown,
		)
	}

	if h.calls != 2 {
		t.Fatalf("hash calls = %d, want 2 (신규 최초 지문 x2)", h.calls)
	}
}

// TestRunner_SiteFilter_ExcludesOutsideSelection — 선택 밖 신규 파일은
// 후보·장부·해시 어디에도 나타나지 않고 SiteMismatch 로만 남는다.
func TestRunner_SiteFilter_ExcludesOutsideSelection(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	r, jobs, h := siteTestFixture(
		t, db, when,
		[]string{siteDbonMO, siteSonpMO},
		nil,
		func(o *RunOptions) { o.Sites = []string{"dbon"} },
	)

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 1 || rep.SiteMismatch != 1 || rep.SiteUnknown != 0 {
		t.Fatalf(
			"kept=%d mismatch=%d unknown=%d, want 1/1/0",
			len(kept), rep.SiteMismatch, rep.SiteUnknown,
		)
	}

	if kept[0].Key.FileName != domain.NormalizeName(siteDbonMO) {
		t.Fatalf("후보 = %q, want dbon 파일", kept[0].Key.FileName)
	}

	if h.calls != 1 {
		t.Fatalf("hash calls = %d, want 1 — 선택 밖 파일이 해시되었다", h.calls)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{
		domain.NormalizeName(siteDbonMO),
		domain.NormalizeName(siteSonpMO),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, exists := got[domain.NormalizeName(siteSonpMO)]; exists {
		t.Fatal("선택 밖 신규 파일의 common_ledger 행이 생겼다")
	}

	if _, exists := got[domain.NormalizeName(siteDbonMO)]; !exists {
		t.Fatal("선택된 파일의 common_ledger 행이 없다")
	}
}

// TestRunner_SiteFilter_CaseInsensitiveSelection — 운영자는 --site dbon
// 처럼 소문자로 입력한다. ParseSiteList 가 대문자로 정규화하지만, 그
// 경로를 거치지 않은 목록이 들어와도 결과가 같아야 한다.
//
// 정확 일치만 하면 대소문자가 어긋난 순간 모든 파일이 SiteMismatch 로
// 빠져 오류 없이 0 건이 된다. 로그만 보면 "그 기간에 파일이 없다"와
// 구분되지 않는 침묵 실패다.
func TestRunner_SiteFilter_CaseInsensitiveSelection(t *testing.T) {
	for _, sites := range [][]string{
		{"DBON"},
		{"dbon"},
		{"DbOn"},
		{" dbon "},
	} {
		t.Run(strings.Join(sites, ","), func(t *testing.T) {
			db, _ := xferTestDB(t)
			when := siteTestWhen()

			r, jobs, _ := siteTestFixture(
				t, db, when,
				[]string{siteDbonMO, siteSonpMO},
				nil,
				func(o *RunOptions) { o.Sites = sites },
			)

			kept, report, err := r.Run(
				context.Background(), jobs,
				scan.Range{From: when, To: when},
			)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			rep := report.Categories[0]
			if len(kept) != 1 || rep.SiteMismatch != 1 {
				t.Fatalf(
					"sites=%v: kept=%d mismatch=%d, want 1/1",
					sites, len(kept), rep.SiteMismatch,
				)
			}

			if kept[0].Key.FileName != domain.NormalizeName(siteDbonMO) {
				t.Fatalf("sites=%v: 후보 = %q, want dbon 파일",
					sites, kept[0].Key.FileName)
			}
		})
	}
}

// 내부 목록의 대소문자·숫자 코드와 같은 Runner의 선택 변경/해제를 함께 검증한다.
// dry-run으로 같은 입력 상태를 유지해 장부 변화가 비교에 섞이지 않게 한다.
func TestRunner_SiteFilter_SelectionChangesAcrossRuns(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()
	suwn := strings.Replace(siteDbonMO, "DBON", "SUWN", 1)
	numeric := strings.Replace(siteDbonMO, "DBON", "SUW1", 1)
	r, jobs, _ := siteTestFixture(t, db, when, []string{suwn, numeric}, nil,
		func(o *RunOptions) { o.DryRun = true })
	for _, tc := range []struct {
		name  string
		sites []string
		want  []string
	}{
		{"uppercase", []string{"SUWN"}, []string{domain.NormalizeName(suwn)}},
		{"lowercase", []string{"suwn"}, []string{domain.NormalizeName(suwn)}},
		{"mixed_case", []string{" SuWn "}, []string{domain.NormalizeName(suwn)}},
		{"numeric_suffix", []string{"sUw1"}, []string{domain.NormalizeName(numeric)}},
		{"multiple_and_duplicates", []string{"suwn", "SUWN", "sUw1"}, []string{domain.NormalizeName(numeric), domain.NormalizeName(suwn)}},
		{"no_match", []string{"DBON"}, nil},
		{"clear_to_nil", nil, []string{domain.NormalizeName(numeric), domain.NormalizeName(suwn)}},
		{"select_again", []string{"suwn"}, []string{domain.NormalizeName(suwn)}},
		{"clear_to_empty", []string{}, []string{domain.NormalizeName(numeric), domain.NormalizeName(suwn)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r.Opts.Sites = tc.sites
			kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, c := range kept {
				got = append(got, c.Key.FileName)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("candidates = %v, want %v", got, tc.want)
			}
			rep := report.Categories[0]
			if rep.SiteMismatch != 2-len(tc.want) || rep.SiteUnknown != 0 {
				t.Fatalf("mismatch=%d unknown=%d", rep.SiteMismatch, rep.SiteUnknown)
			}
		})
	}
	rows, err := db.LookupCommon(ctx, xferTestCat, []string{domain.NormalizeName(suwn), domain.NormalizeName(numeric)})
	if err != nil || len(rows) != 0 {
		t.Fatalf("dry-run changed ledger: rows=%v err=%v", rows, err)
	}
}

// TestRunner_SiteFilter_LedgerImmutableForExcluded — 지문 없는 VERIFIED
// 행은 자연 백필 대상이지만, 선택 밖이면 백필도 하지 않고 행이 그대로
// 남는다. 대조군(필터 없음)에서 같은 행이 실제로 백필되는 것을 먼저
// 확인해 "백필이 원래 일어났을 상황"임을 증명한다.
func TestRunner_SiteFilter_LedgerImmutableForExcluded(t *testing.T) {
	ctx := context.Background()
	when := siteTestWhen()
	mtime := when.Add(-time.Hour).Unix()

	sonpNorm := domain.NormalizeName(siteSonpMO)

	// 대조군 — 필터 없음: 지문 없는 VERIFIED 행이 백필된다.
	{
		db, _ := xferTestDB(t)
		insertKnownForHashTest(t, db, sonpNorm, siteTestSize, mtime, "", true)

		r, jobs, h := siteTestFixture(
			t, db, when,
			[]string{siteSonpMO},
			nil,
			func(o *RunOptions) { o.MaxHashBackfillPerRun = 10 },
		)

		_, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
		if err != nil {
			t.Fatalf("대조군 Run: %v", err)
		}

		if report.Categories[0].HashBackfill.Files != 1 || h.calls != 1 {
			t.Fatalf(
				"대조군 백필 불발: backfill=%d calls=%d, want 1/1",
				report.Categories[0].HashBackfill.Files, h.calls,
			)
		}

		got, err := db.LookupCommon(ctx, xferTestCat, []string{sonpNorm})
		if err != nil {
			t.Fatal(err)
		}

		if got[sonpNorm].ContentHash != siteTestHash {
			t.Fatalf("대조군 지문 미기록: %q", got[sonpNorm].ContentHash)
		}
	}

	// 본검증 — sonp 를 선택 밖에 두면 백필·해시·행 변경이 전부 없다.
	db, dbPath := xferTestDB(t)
	key := insertKnownForHashTest(t, db, sonpNorm, siteTestSize, mtime, "", true)

	r, jobs, h := siteTestFixture(
		t, db, when,
		[]string{siteSonpMO, siteDbonMO},
		nil,
		func(o *RunOptions) {
			o.MaxHashBackfillPerRun = 10
			o.Sites = []string{"dbon"}
		},
	)

	_, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if rep.HashBackfill.Files != 0 || h.calls != 1 {
		t.Fatalf(
			"선택 밖 백필 발생: backfill=%d calls=%d, want 0/1(dbon 신규만)",
			rep.HashBackfill.Files, h.calls,
		)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{sonpNorm})
	if err != nil {
		t.Fatal(err)
	}

	row := got[sonpNorm]
	if row.Revision != key.Revision || row.MTime != mtime || row.ContentHash != "" {
		t.Fatalf(
			"선택 밖 행이 변했다: rev=%d mtime=%d hash=%q (want rev=%d mtime=%d hash=\"\")",
			row.Revision, row.MTime, row.ContentHash, key.Revision, mtime,
		)
	}

	pr := readPutRaw(t, dbPath, key)
	if pr.status != "VERIFIED" || pr.attempts != 1 {
		t.Fatalf(
			"선택 밖 put 이력이 변했다: status=%s attempts=%d, want VERIFIED/1",
			pr.status, pr.attempts,
		)
	}
}

// TestRunner_SiteFilter_UnknownNameCounted — site 지정 시 식별 불가
// 파일은 SiteUnknown 으로 제외되고 장부에 닿지 않는다. 전체 대상으로
// 되돌리는 fallback 은 없다 (SITE §4).
func TestRunner_SiteFilter_UnknownNameCounted(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	r, jobs, _ := siteTestFixture(
		t, db, when,
		[]string{siteUnknownName, siteDbonMO},
		nil,
		func(o *RunOptions) { o.Sites = []string{"dbon"} },
	)

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if len(kept) != 1 || rep.SiteUnknown != 1 || rep.SiteMismatch != 0 {
		t.Fatalf(
			"kept=%d unknown=%d mismatch=%d, want 1/1/0",
			len(kept), rep.SiteUnknown, rep.SiteMismatch,
		)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{
		domain.NormalizeName(siteUnknownName),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatal("식별 불가 파일이 common_ledger 에 기록되었다")
	}
}

// TestRunner_SiteFilter_SetGateJudgmentUnchanged — 세트 키가 관측소를
// 포함하므로, 다른 site 의 제외가 선택된 site 의 게이트 판정을 바꾸지
// 않는다 (검토 v1 §3-2). dbon 은 완성 세트(mo+mn), sonp 는 미완성(mo).
//
// 판정 로직은 live 와 동일한 dry-run 으로 한 DB 에서 세 번 관측한다
// (dry-run 은 장부를 쓰지 않아 회차 간 오염이 없다).
func TestRunner_SiteFilter_SetGateJudgmentUnchanged(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	names := []string{siteDbonMO, siteDbonMN, siteSonpMO}
	kinds := []string{"mo", "mn"}

	run := func(sites []string) (int, CategoryReport) {
		t.Helper()

		r, jobs, _ := siteTestFixture(
			t, db, when, names, kinds,
			func(o *RunOptions) {
				o.DryRun = true
				o.Sites = sites
			},
		)

		kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
		if err != nil {
			t.Fatalf("Run(sites=%v): %v", sites, err)
		}

		return len(kept), report.Categories[0]
	}

	// 대조군 — 필터 없음: dbon 세트 통과 2건, sonp 미완성 보류 1건.
	kept, rep := run(nil)
	if kept != 2 || rep.SetHeld != 1 {
		t.Fatalf("대조군 kept=%d held=%d, want 2/1", kept, rep.SetHeld)
	}

	// dbon 선택 — sonp 제외가 dbon 의 완성 판정을 바꾸지 않는다.
	kept, rep = run([]string{"dbon"})
	if kept != 2 || rep.SetHeld != 0 || rep.SiteMismatch != 1 {
		t.Fatalf(
			"dbon 선택 kept=%d held=%d mismatch=%d, want 2/0/1",
			kept, rep.SetHeld, rep.SiteMismatch,
		)
	}

	// sonp 선택 — dbon 제외가 sonp 의 미완성(보류) 판정을 바꾸지 않는다.
	kept, rep = run([]string{"sonp"})
	if kept != 0 || rep.SetHeld != 1 || rep.SiteMismatch != 2 {
		t.Fatalf(
			"sonp 선택 kept=%d held=%d mismatch=%d, want 0/1/2",
			kept, rep.SetHeld, rep.SiteMismatch,
		)
	}
}

// TestRunner_SiteFilter_DryRunMatchesLive — 같은 입력에서 live 와
// dry-run 의 site 선택·집계가 같고, dry-run 은 장부를 바꾸지 않는다.
func TestRunner_SiteFilter_DryRunMatchesLive(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := siteTestWhen()

	names := []string{siteDbonMO, siteSonpMO, siteUnknownName}

	run := func(dry bool) (int, CategoryReport) {
		t.Helper()

		r, jobs, _ := siteTestFixture(
			t, db, when, names, nil,
			func(o *RunOptions) {
				o.DryRun = dry
				o.Sites = []string{"dbon"}
			},
		)

		kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
		if err != nil {
			t.Fatalf("Run(dry=%t): %v", dry, err)
		}

		return len(kept), report.Categories[0]
	}

	dryKept, dryRep := run(true)

	// dry-run 은 장부 불변 — 선택된 파일조차 기록되지 않는다.
	got, err := db.LookupCommon(ctx, xferTestCat, []string{
		domain.NormalizeName(siteDbonMO),
		domain.NormalizeName(siteSonpMO),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 0 {
		t.Fatalf("dry-run 이 장부를 바꿨다: %+v", got)
	}

	liveKept, liveRep := run(false)

	if dryKept != liveKept ||
		dryRep.SiteMismatch != liveRep.SiteMismatch ||
		dryRep.SiteUnknown != liveRep.SiteUnknown {
		t.Fatalf(
			"live/dry-run 선택 불일치: dry(kept=%d m=%d u=%d) live(kept=%d m=%d u=%d)",
			dryKept, dryRep.SiteMismatch, dryRep.SiteUnknown,
			liveKept, liveRep.SiteMismatch, liveRep.SiteUnknown,
		)
	}
}

// TestRunner_SiteFilter_EmptyDoesNotCountUnknown — Sites 생략 시
// 식별 불가 파일을 SiteUnknown 으로 세지 않는다. 세면 일반 자동 PUT
// 에 새 제외 사유가 생긴다 (SITE §4).
func TestRunner_SiteFilter_EmptyDoesNotCountUnknown(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	r, jobs, _ := siteTestFixture(
		t, db, when,
		[]string{siteUnknownName, siteDbonMO},
		nil,
		nil,
	)

	kept, report, err := r.Run(
		context.Background(), jobs,
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	rep := report.Categories[0]
	if rep.SiteUnknown != 0 || rep.SiteMismatch != 0 {
		t.Fatalf(
			"생략인데 site 집계 unknown=%d mismatch=%d",
			rep.SiteUnknown, rep.SiteMismatch,
		)
	}

	if len(kept) < 1 {
		t.Fatal("선택된 정상 파일도 후보에서 빠졌다")
	}
}

// TestRunner_SiteFilter_ChangedOutsideNotHashed — 선택 밖 파일이
// 장부에 있고 size 가 달라 변경 판정이 날 상황에서도 해시를 부르지
// 않는다. 필터를 Lookup 뒤에 두면 변경 해시가 먼저 돈다.
func TestRunner_SiteFilter_ChangedOutsideNotHashed(t *testing.T) {
	ctx := context.Background()
	when := siteTestWhen()
	mtime := when.Add(-time.Hour).Unix()
	sonpNorm := domain.NormalizeName(siteSonpMO)

	db, dbPath := xferTestDB(t)
	key := insertKnownForHashTest(t, db, sonpNorm, siteTestSize/2, mtime, siteTestHash, true)

	r, jobs, h := siteTestFixture(
		t, db, when,
		[]string{siteSonpMO, siteDbonMO},
		nil,
		func(o *RunOptions) { o.Sites = []string{"dbon"} },
	)

	_, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Categories[0].HashChanged.Files != 0 || h.calls != 1 {
		t.Fatalf(
			"선택 밖 변경 해시: changed=%d calls=%d, want 0/1",
			report.Categories[0].HashChanged.Files, h.calls,
		)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{sonpNorm})
	if err != nil {
		t.Fatal(err)
	}

	row := got[sonpNorm]
	if row.Size != siteTestSize/2 || row.Revision != key.Revision || row.ContentHash != siteTestHash {
		t.Fatalf("선택 밖 변경 행이 변했다: size=%d rev=%d hash=%q",
			row.Size, row.Revision, row.ContentHash)
	}

	pr := readPutRaw(t, dbPath, key)
	if pr.status != "VERIFIED" || pr.attempts != 1 {
		t.Fatalf("선택 밖 put 이력: status=%s attempts=%d", pr.status, pr.attempts)
	}
}

// TestRunner_SiteFilter_UnparsedSitesRejected — ParseSiteList 를 거치지
// 않은 목록은 Run 오류다. 공백만 있는 목록을 접어 빈 집합이 되면 필터
// 없음으로 확대되어 잘못된 --site 가 전 관측소 재전송이 되고, 9자리
// 코드는 정확 일치 실패로 조용히 0 건이 된다. 둘 다 침묵이 아니라
// 오류여야 한다.
func TestRunner_SiteFilter_UnparsedSitesRejected(t *testing.T) {
	for _, tc := range []struct {
		name  string
		sites []string
	}{
		{"blank", []string{"  ", "\t"}},
		{"nine_char", []string{"DBON00KOR"}},
		{"wildcard", []string{"DBO*"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _ := xferTestDB(t)
			when := siteTestWhen()

			r, jobs, _ := siteTestFixture(
				t, db, when,
				[]string{siteDbonMO, siteSonpMO},
				nil,
				func(o *RunOptions) { o.Sites = tc.sites },
			)

			kept, _, err := r.Run(
				context.Background(), jobs,
				scan.Range{From: when, To: when},
			)
			if err == nil {
				t.Fatalf("Sites=%q 가 통과했다: kept=%d", tc.sites, len(kept))
			}

			if !strings.Contains(err.Error(), "Sites") {
				t.Fatalf("err = %v, want Sites", err)
			}
		})
	}
}

// TestRunner_SiteFilter_RoutingErrorNotUnknown — 카테고리 라우팅 누락은
// SiteUnknown 으로 접지 않고 오류다. scan 이 아는 카테고리만 Run 에
// 들어오므로 visitBatch 에 직접 넣어 필터 분기를 친다.
func TestRunner_SiteFilter_RoutingErrorNotUnknown(t *testing.T) {
	db, _ := xferTestDB(t)
	when := siteTestWhen()

	r, _, _ := siteTestFixture(
		t, db, when,
		[]string{siteDbonMO},
		nil,
		func(o *RunOptions) { o.Sites = []string{"dbon"} },
	)
	r.siteSet = map[string]struct{}{"DBON": {}}

	rep := CategoryReport{ExtCount: map[string]int{}}
	var cands []Candidate
	var failed []failedRef
	var changed []ledger.NameRev
	var observed []string

	err := r.visitBatch(
		context.Background(),
		CategoryJob{Category: domain.Category("NOPE")},
		scan.Batch{
			Dir:  "in",
			When: when,
			Entries: []scan.Entry{{
				Name:  siteDbonMO,
				Size:  siteTestSize,
				MTime: when,
			}},
		},
		&rep,
		map[string]struct{}{},
		map[string]struct{}{},
		&cands,
		&failed,
		&changed,
		&observed,
	)
	if err == nil {
		t.Fatal("라우팅 누락인데 visitBatch 가 성공했다")
	}

	if !strings.Contains(err.Error(), "site filter") &&
		!strings.Contains(err.Error(), "site extraction") {
		t.Fatalf("err = %v, want site routing error", err)
	}

	if rep.SiteUnknown != 0 {
		t.Fatalf("라우팅 오류를 SiteUnknown=%d 로 접었다", rep.SiteUnknown)
	}
}
