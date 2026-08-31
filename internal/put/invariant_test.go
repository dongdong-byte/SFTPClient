package put

// transfer.go 의 !c.IsRetry 가드가 의존하는 불변식을 심판한다.
//
//	불변식: put_ledger 행이 FAILED 인 후보는 IsRetry 뿐이다.
//
// transfer_test.go 의 A·B 케이스는 Candidate 를 손으로 만들기 때문에
// "IsRetry 가 실제로 FAILED 행과 일대일인가" 를 증명하지 못한다.
// 불변식은 runner.go 의 후보 필터가 만드는 것이므로, Runner.Run 을
// 실제로 태워 put_ledger 상태별 IsRetry 를 단언한다.
//
// runner.go 후보 필터에 경로가 추가되어 FAILED 행이 IsRetry=false 로
// 후보가 되는 날, transfer 의 가드가 뚫려 회차가 중단된다 —
// 그 전에 이 테스트가 먼저 깨진다.

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// fakeLister 는 scan.DirLister 의 테스트 구현이다.
// 등록되지 않은 디렉터리는 fs.ErrNotExist — scan 은 이를 빈 슬롯
// (Missing)으로 접는다.
type fakeLister struct {
	dirs map[string][]scan.Entry
}

func (f fakeLister) List(_ context.Context, dir string) ([]scan.Entry, error) {
	entries, ok := f.dirs[dir]
	if !ok {
		return nil, fmt.Errorf("fake list %q: %w", dir, fs.ErrNotExist)
	}

	return entries, nil
}

func TestRunnerCandidateIsRetryInvariant(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()

	// 세 파일이 같은 시각 디렉터리에 있다. put_ledger 상태만 다르다.
	const (
		fNoRow   = "norow001.rnx.gz" // put 행 없음        → IsRetry=false
		fPending = "pend0001.rnx.gz" // PENDING 고아       → IsRetry=false
		fFailed  = "failed01.rnx.gz" // FAILED attempts=1  → IsRetry=true
	)

	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	// 세 파일 모두 common_ledger 에 등록한다 (Unchanged 경로로 태우기
	// 위해 scan Entry 와 size·mtime 을 일치시킨다). Unchanged 인데
	// put 행이 없거나 PENDING·FAILED 인 상태 — 정확히 판정표의
	// 재개/재시도 분기다.
	keys := map[string]ledger.PutKey{}

	for _, name := range []string{fNoRow, fPending, fFailed} {
		if _, err := db.UpsertCommon(ctx, ledger.CommonInput{
			FileName:          name, // 이미 소문자 = 정규형
			BaseName:          domain.BaseName(name),
			Category:          xferTestCat,
			Size:              100,
			MTime:             mtime.UTC().Unix(),
			Origin:            domain.OriginLocal,
			IngressVerifiedAt: mtime.UTC().Unix(),
		}); err != nil {
			t.Fatalf("UpsertCommon(%q) 실패: %v", name, err)
		}

		known, err := db.LookupCommon(ctx, xferTestCat, []string{name})
		if err != nil {
			t.Fatalf("LookupCommon() 실패: %v", err)
		}

		keys[name] = ledger.PutKey{
			Category: xferTestCat,
			FileName: name,
			Revision: known[name].Revision,
		}
	}

	// PENDING 고아: 등록만 하고 착수하지 않은 직전 실행의 흔적.
	if err := db.InsertPendingBatch(
		ctx, []ledger.PutKey{keys[fPending]},
	); err != nil {
		t.Fatalf("InsertPendingBatch() 실패: %v", err)
	}

	// FAILED attempts=1: 공개 API 체인으로 실제 전이와 같은 행을 만든다.
	if err := db.InsertPendingBatch(
		ctx, []ledger.PutKey{keys[fFailed]},
	); err != nil {
		t.Fatalf("InsertPendingBatch(failed) 실패: %v", err)
	}

	if err := db.BeginPut(
		ctx, keys[fFailed], "/out/x", "/out/x.part", 100, 5,
	); err != nil {
		t.Fatalf("BeginPut() 실패: %v", err)
	}

	if err := db.FailPut(ctx, keys[fFailed], "seed failure"); err != nil {
		t.Fatalf("FailPut() 실패: %v", err)
	}

	// fake 디렉터리: LocalPath 템플릿을 실제로 Expand 해서 키를 만든다.
	// 문자열을 손으로 쓰면 pathpl 의 출력 형식 변화에 조용히 어긋난다.
	jobs := xferJobs(t)
	dir := jobs[0].LocalPath.Expand(when)

	entries := make([]scan.Entry, 0, 3)
	for _, name := range []string{fNoRow, fPending, fFailed} {
		entries = append(entries, scan.Entry{
			Name:  name,
			Size:  100,
			MTime: mtime,
		})
	}

	r := &Runner{
		Scanner: scan.New(fakeLister{
			dirs: map[string][]scan.Entry{dir: entries},
		}),
		DB:       db,
		Verifier: verify.Verifier{}, // Grace=0: mtime 나이 검사 비활성 (config 규약)
		Opts: RunOptions{
			MaxRetries: 5,
			Logger:     xferRunner(db).Opts.Logger,
			Now:        func() time.Time { return when },
		},
	}

	rng := scan.Range{From: when, To: when}

	kept, report, err := r.Run(ctx, jobs, rng)
	if err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}

	if len(kept) != 3 {
		t.Fatalf(
			"후보 수 = %d, want 3 (report=%+v)", len(kept), report,
		)
	}

	wantRetry := map[string]bool{
		fNoRow:   false,
		fPending: false,
		fFailed:  true,
	}

	seen := map[string]bool{}

	for _, c := range kept {
		want, ok := wantRetry[c.Key.FileName]
		if !ok {
			t.Fatalf("예상 밖 후보: %q", c.Key.FileName)
		}

		seen[c.Key.FileName] = true

		if c.IsRetry != want {
			t.Errorf(
				"%s: IsRetry = %v, want %v — 불변식이 깨졌다. "+
					"transfer 의 !IsRetry 가드가 이 전제 위에 서 있다",
				c.Key.FileName, c.IsRetry, want,
			)
		}

		// 불변식의 다른 반쪽: live 후보의 revision 은 확정값이다.
		if c.RevisionPending || c.Key.Revision < 1 {
			t.Errorf(
				"%s: RevisionPending=%v Revision=%d — live 불변식 위반",
				c.Key.FileName, c.RevisionPending, c.Key.Revision,
			)
		}
	}

	for name := range wantRetry {
		if !seen[name] {
			t.Errorf("%s 가 후보에 없다", name)
		}
	}

	if report.Categories[0].Retries != 1 {
		t.Errorf(
			"Retries = %d, want 1", report.Categories[0].Retries,
		)
	}
}
