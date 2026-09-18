package put

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// TestRunner_MTimeDriftSameHashDoesNotRetransmit 는 운영 사고의 핵심
// 회귀 테스트다. VERIFIED 파일의 mtime 만 바뀌어도 내용 지문이 같으면
// revision/후보를 늘리지 않고 기준선만 옮겨야 한다.
func TestRunner_MTimeDriftSameHashDoesNotRetransmit(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	oldMTime := when.Add(-2 * time.Hour).Unix()
	newMTime := when.Add(-time.Hour).Unix()
	const name = "drift001.rnx.gz"
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	key := insertKnownForHashTest(t, db, name, 100, oldMTime, hash, true)
	h := &stubHasher{result: HashResult{
		Hash: hash, PreSize: 100, PostSize: 100,
		PreMTime: newMTime, PostMTime: newMTime, Bytes: 100,
	}}

	r := hashTestRunner(t, db, when, name, 100, newMTime, h)
	kept, report, err := r.Run(ctx, xferJobs(t), scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("mtime-only drift가 재전송 후보 %d건을 만들었다", len(kept))
	}
	if h.calls != 1 || report.Categories[0].MetadataOnly != 1 {
		t.Fatalf("drift 판정 불일치: calls=%d report=%+v", h.calls, report.Categories[0])
	}

	got, err := db.LookupCommon(ctx, xferTestCat, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if got[name].Revision != key.Revision || got[name].MTime != newMTime {
		t.Fatalf("기준선 이동 불일치: got=%+v originalRev=%d", got[name], key.Revision)
	}
}

// TestRunner_MTimeDriftWithoutHashRetransmitsOnce 는 v6 배포 직후의 빈 지문
// 행을 같은 내용으로 단정하지 않고 보수적으로 새 revision으로 보내는 계약이다.
func TestRunner_MTimeDriftWithoutHashRetransmitsOnce(t *testing.T) {
	db, _ := xferTestDB(t)
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	oldMTime := when.Add(-2 * time.Hour).Unix()
	newMTime := when.Add(-time.Hour).Unix()
	const name = "legacy01.rnx.gz"
	const hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	oldKey := insertKnownForHashTest(t, db, name, 100, oldMTime, "", true)
	h := &stubHasher{result: HashResult{
		Hash: hash, PreSize: 100, PostSize: 100,
		PreMTime: newMTime, PostMTime: newMTime, Bytes: 100,
	}}

	r := hashTestRunner(t, db, when, name, 100, newMTime, h)
	kept, _, err := r.Run(context.Background(), xferJobs(t), scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(kept) != 1 || kept[0].Key.Revision != oldKey.Revision+1 {
		t.Fatalf("빈 지문 drift 후보/ revision 불일치: %+v", kept)
	}

	got, err := db.LookupCommon(context.Background(), xferTestCat, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if got[name].ContentHash != hash {
		t.Fatalf("새 revision 지문 = %q, want %q", got[name].ContentHash, hash)
	}
}

// TestRunner_UnstableHashIsHeld 는 스캔 이후 또는 해시 도중 바뀐 파일이
// 후보가 되거나 common_ledger에 기록되지 않는지 고정한다.
func TestRunner_UnstableHashIsHeld(t *testing.T) {
	db, _ := xferTestDB(t)
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := when.Add(-time.Hour).Unix()
	const name = "moving01.rnx.gz"

	h := &stubHasher{result: HashResult{
		Hash:    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		PreSize: 100, PostSize: 101, PreMTime: mtime, PostMTime: mtime,
	}}
	r := hashTestRunner(t, db, when, name, 100, mtime, h)

	kept, report, err := r.Run(context.Background(), xferJobs(t), scan.Range{From: when, To: when})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(kept) != 0 || report.Categories[0].HashUnstable != 1 {
		t.Fatalf("unstable 파일이 보류되지 않음: kept=%+v report=%+v", kept, report.Categories[0])
	}
	got, err := db.LookupCommon(context.Background(), xferTestCat, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("unstable 신규 파일이 ledger에 기록됨: %+v", got)
	}
}

// TestRunner_VerifiedUnchangedStillObservedForSetGate 는 이미 지문이 있는
// VERIFIED 형제가 Unchanged 로 후보에서 빠져도 observed 에 남아
// 세트 게이트를 완성으로 유지하는지 고정한다.
func TestRunner_VerifiedUnchangedStillObservedForSetGate(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()
	when := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC) // DOY 250
	mtime := when.Add(-time.Hour).Unix()
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const gName = "suwn2500.26g.gz"
	const oName = "suwn2500.26o.gz"

	if _, err := db.UpsertCommon(ctx, ledger.CommonInput{
		FileName: gName, BaseName: domain.BaseName(gName),
		Category:          domain.CategoryRINEX2Daily,
		Size:              100,
		MTime:             mtime,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: mtime,
		ContentHash:       hash,
	}); err != nil {
		t.Fatal(err)
	}

	known, err := db.LookupCommon(ctx, domain.CategoryRINEX2Daily, []string{gName})
	if err != nil {
		t.Fatal(err)
	}

	gKey := ledger.PutKey{
		Category: domain.CategoryRINEX2Daily,
		FileName: gName,
		Revision: known[gName].Revision,
	}
	if err := db.InsertPendingBatch(ctx, []ledger.PutKey{gKey}); err != nil {
		t.Fatal(err)
	}
	if err := db.BeginPut(ctx, gKey, "/out/g", "/out/g.part", 100, 5); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishPut(
		ctx, gKey, 100,
		time.Unix(mtime, 0), time.Unix(mtime+1, 0),
	); err != nil {
		t.Fatal(err)
	}

	local, err := pathpl.Parse("in/(YYYY)/(DOY)/")
	if err != nil {
		t.Fatal(err)
	}

	remote, err := pathpl.Parse("out/(YYYY)/(DOY)/")
	if err != nil {
		t.Fatal(err)
	}

	dir := local.Expand(when)
	r := &Runner{
		Scanner: scan.New(fakeLister{dirs: map[string][]scan.Entry{
			dir: {
				{Name: gName, Size: 100, MTime: time.Unix(mtime, 0).UTC()},
				{Name: oName, Size: 100, MTime: time.Unix(mtime, 0).UTC()},
			},
		}}),
		DB:       db,
		Verifier: verify.Verifier{},
		Opts: RunOptions{
			MaxRetries: 5,
			Logger:     log.New(io.Discard, "", 0),
			Now:        func() time.Time { return when },
		},
	}

	kept, report, err := r.Run(
		ctx,
		[]CategoryJob{{
			Category:      domain.CategoryRINEX2Daily,
			LocalPath:     local,
			RemotePath:    remote,
			RequiredKinds: []string{"g", "o"},
		}},
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if report.Categories[0].SetHeld != 0 {
		t.Fatalf(
			"VERIFIED Unchanged 형제가 observed 에서 빠져 세트가 보류됨: held=%d kept=%+v",
			report.Categories[0].SetHeld,
			kept,
		)
	}

	if len(kept) != 1 || kept[0].Key.FileName != oName {
		t.Fatalf("신규 o 가 후보가 아님: %+v", kept)
	}
}

func hashTestRunner(t *testing.T, db *ledger.DB, when time.Time, name string, size, mtime int64, h Hasher) *Runner {
	t.Helper()
	jobs := xferJobs(t)
	dir := jobs[0].LocalPath.Expand(when)
	return &Runner{
		Scanner: scan.New(fakeLister{dirs: map[string][]scan.Entry{dir: {{
			Name: name, Size: size, MTime: time.Unix(mtime, 0).UTC(),
		}}}}),
		DB: db, Verifier: verify.Verifier{}, Hasher: h,
		Opts: RunOptions{MaxRetries: 5, Logger: log.New(io.Discard, "", 0), Now: func() time.Time { return when }},
	}
}

func insertKnownForHashTest(t *testing.T, db *ledger.DB, name string, size, mtime int64, hash string, verified bool) ledger.PutKey {
	t.Helper()
	ctx := context.Background()
	if _, err := db.UpsertCommon(ctx, ledger.CommonInput{
		FileName: name, BaseName: domain.BaseName(name), Category: xferTestCat,
		Size: size, MTime: mtime, Origin: domain.OriginLocal,
		IngressVerifiedAt: mtime, ContentHash: hash,
	}); err != nil {
		t.Fatal(err)
	}
	known, err := db.LookupCommon(ctx, xferTestCat, []string{name})
	if err != nil {
		t.Fatal(err)
	}
	key := ledger.PutKey{Category: xferTestCat, FileName: name, Revision: known[name].Revision}
	if verified {
		if err := db.InsertPendingBatch(ctx, []ledger.PutKey{key}); err != nil {
			t.Fatal(err)
		}
		if err := db.BeginPut(ctx, key, "/out/x", "/out/x.part", size, 5); err != nil {
			t.Fatal(err)
		}
		if err := db.FinishPut(ctx, key, size, time.Unix(mtime, 0), time.Unix(mtime+1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	return key
}
