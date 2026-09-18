package put

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// TestRunner_FilepartIsExcludedUntilRenamed 는 UNIT5 의 동작 계약이다.
//
// 상류 도구(WinSCP)가 쓰는 중이거나 끊겨 남긴 .filepart 는 크기·나이와
// 무관하게 후보가 되지 않고 장부에도 오르지 않는다. 같은 디렉터리의
// 완성 파일은 기존 절차대로 후보가 된다.
func TestRunner_FilepartIsExcludedUntilRenamed(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	// 오래되고 크기가 있는 .filepart — 끊긴 전송의 잔여물처럼 보이게 한다.
	// Grace·Zero 검사만으로는 걸러지지 않는 조건이다.
	oldMTime := time.Unix(when.Add(-24*time.Hour).Unix(), 0).UTC()
	mtime := when.Add(-time.Hour).Unix()

	const (
		done     = "SONP00KOR_R_20260010300_01H_01S_MO.rnx.gz"
		inFlight = "SUWN00KOR_R_20260010300_01H_01S_MO.rnx.gz.filepart"
		upper    = "DAEJ00KOR_R_20260010300_01H_01S_MO.RNX.GZ.FILEPART"
	)

	jobs := xferJobs(t)
	dir := jobs[0].LocalPath.Expand(when)

	h := &stubHasher{result: HashResult{
		Hash:    "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		PreSize: 100, PostSize: 100, PreMTime: mtime, PostMTime: mtime, Bytes: 100,
	}}

	r := &Runner{
		Scanner: scan.New(fakeLister{dirs: map[string][]scan.Entry{dir: {
			{Name: done, Size: 100, MTime: time.Unix(mtime, 0).UTC()},
			{Name: inFlight, Size: 5000, MTime: oldMTime},
			{Name: upper, Size: 5000, MTime: oldMTime},
		}}}),
		DB:       db,
		Verifier: verify.Verifier{Grace: time.Minute, Now: func() time.Time { return when }},
		Hasher:   h,
		Opts: RunOptions{
			MaxRetries: 5,
			Logger:     log.New(io.Discard, "", 0),
			Now:        func() time.Time { return when },
		},
	}

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	cat := report.Categories[0]
	if cat.SkippedPart != 2 || cat.Rejected["part"] != 2 {
		t.Fatalf("임시 파일 제외 집계 불일치: skipped_part=%d rejected=%v",
			cat.SkippedPart, cat.Rejected)
	}

	if len(kept) != 1 || kept[0].Key.FileName != "sonp00kor_r_20260010300_01h_01s_mo.rnx.gz" {
		t.Fatalf("완성 파일만 후보여야 한다: %+v", kept)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{
		"suwn00kor_r_20260010300_01h_01s_mo.rnx.gz.filepart",
		"daej00kor_r_20260010300_01h_01s_mo.rnx.gz.filepart",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf(".filepart 가 장부에 등록됨: %+v", got)
	}
}

// TestRunner_FilepartBecomesCandidateAfterRename 는 상류가 임시 이름을
// 최종 이름으로 바꾼 다음 회차가 기존 절차로 후보를 만든다는 계약이다.
//
// 1회차의 .filepart 행과 2회차의 완성본은 식별자가 다르다.
// 접미사를 떼어 같은 키로 합치지 않는다.
func TestRunner_FilepartBecomesCandidateAfterRename(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := when.Add(-time.Hour).Unix()
	jobs := xferJobs(t)
	dir := jobs[0].LocalPath.Expand(when)

	const (
		tempName  = "SUWN00KOR_R_20260010300_01H_01S_MO.rnx.gz.filepart"
		finalName = "SUWN00KOR_R_20260010300_01H_01S_MO.rnx.gz"
		finalKey  = "suwn00kor_r_20260010300_01h_01s_mo.rnx.gz"
		tempKey   = "suwn00kor_r_20260010300_01h_01s_mo.rnx.gz.filepart"
	)

	entries := map[string][]scan.Entry{dir: {{
		Name:  tempName,
		Size:  100,
		MTime: time.Unix(mtime, 0).UTC(),
	}}}
	h := filepartStubHasher(mtime)
	r := filepartTestRunner(t, db, when, entries, h)

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("1회차 Run: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("1회차 후보 = %+v, want 없음", kept)
	}
	if report.Categories[0].SkippedPart != 1 {
		t.Fatalf("1회차 skipped_part=%d, want 1", report.Categories[0].SkippedPart)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{tempKey, finalKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("1회차 장부 등록: %+v", got)
	}

	entries[dir] = []scan.Entry{{
		Name:  finalName,
		Size:  100,
		MTime: time.Unix(mtime, 0).UTC(),
	}}

	kept, _, err = r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("2회차 Run: %v", err)
	}
	if len(kept) != 1 || kept[0].Key.FileName != finalKey {
		t.Fatalf("2회차 후보 = %+v, want %q", kept, finalKey)
	}

	got, err = db.LookupCommon(ctx, xferTestCat, []string{tempKey, finalKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[tempKey]; ok {
		t.Fatalf("임시 이름이 장부에 남음: %+v", got)
	}
	if _, ok := got[finalKey]; !ok {
		t.Fatalf("완성본이 장부에 없음: %+v", got)
	}
}

// TestRunner_ExistingFilepartLedgerRowIsNotReselected 는 유닛 5 배포
// 전에 이미 올라간 *.filepart 행이, 디스크에 그대로 있어도 PENDING
// 고아로 재선정되지 않는다는 계약이다. 행 자체는 지우지 않는다.
func TestRunner_ExistingFilepartLedgerRowIsNotReselected(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	oldMTime := time.Unix(when.Add(-24*time.Hour).Unix(), 0).UTC()
	mtime := oldMTime.Unix()
	jobs := xferJobs(t)
	dir := jobs[0].LocalPath.Expand(when)

	const (
		diskName  = "SUWN00KOR_R_20260010300_01H_01S_MO.rnx.gz.filepart"
		ledgerKey = "suwn00kor_r_20260010300_01h_01s_mo.rnx.gz.filepart"
		done      = "SONP00KOR_R_20260010300_01H_01S_MO.rnx.gz"
		doneKey   = "sonp00kor_r_20260010300_01h_01s_mo.rnx.gz"
	)

	if _, err := db.UpsertCommon(ctx, ledger.CommonInput{
		FileName:          ledgerKey,
		BaseName:          domain.BaseName(ledgerKey),
		Category:          xferTestCat,
		Size:              5000,
		MTime:             mtime,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: mtime,
	}); err != nil {
		t.Fatalf("UpsertCommon: %v", err)
	}

	known, err := db.LookupCommon(ctx, xferTestCat, []string{ledgerKey})
	if err != nil {
		t.Fatal(err)
	}
	orphan := ledger.PutKey{
		Category: xferTestCat,
		FileName: ledgerKey,
		Revision: known[ledgerKey].Revision,
	}
	if err := db.InsertPendingBatch(ctx, []ledger.PutKey{orphan}); err != nil {
		t.Fatalf("InsertPendingBatch: %v", err)
	}

	doneMTime := when.Add(-time.Hour).Unix()
	h := filepartStubHasher(doneMTime)
	r := filepartTestRunner(t, db, when, map[string][]scan.Entry{dir: {
		{Name: diskName, Size: 5000, MTime: oldMTime},
		{Name: done, Size: 100, MTime: time.Unix(doneMTime, 0).UTC()},
	}}, h)

	kept, report, err := r.Run(ctx, jobs, scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Categories[0].SkippedPart != 1 {
		t.Fatalf("skipped_part=%d, want 1", report.Categories[0].SkippedPart)
	}
	if len(kept) != 1 || kept[0].Key.FileName != doneKey {
		t.Fatalf("후보 = %+v, want 완성본만", kept)
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{ledgerKey})
	if err != nil {
		t.Fatal(err)
	}
	if got[ledgerKey].Revision != orphan.Revision {
		t.Fatalf("고아 행 revision 변경: %+v", got[ledgerKey])
	}

	nr := ledger.NameRev{FileName: ledgerKey, Revision: orphan.Revision}
	states, err := db.LookupPut(ctx, xferTestCat, []ledger.NameRev{nr})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := states[nr]
	if !ok || st.Status != domain.StatusPending {
		t.Fatalf("고아 put 상태 = %+v ok=%v, want PENDING", st, ok)
	}
}

func filepartStubHasher(mtime int64) *stubHasher {
	return &stubHasher{result: HashResult{
		Hash:      "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
		PreSize:   100,
		PostSize:  100,
		PreMTime:  mtime,
		PostMTime: mtime,
		Bytes:     100,
	}}
}

func filepartTestRunner(
	t *testing.T,
	db *ledger.DB,
	when time.Time,
	entries map[string][]scan.Entry,
	h *stubHasher,
) *Runner {
	t.Helper()

	return &Runner{
		Scanner:  scan.New(fakeLister{dirs: entries}),
		DB:       db,
		Verifier: verify.Verifier{Grace: time.Minute, Now: func() time.Time { return when }},
		Hasher:   h,
		Opts: RunOptions{
			MaxRetries: 5,
			Logger:     log.New(io.Discard, "", 0),
			Now:        func() time.Time { return when },
		},
	}
}
