package put

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// TestRunner_FlatAndHourCopyDoNotLoopRevision 는 재귀가 같은 이름의
// 평면 파일과 시각 하위 폴더 사본을 함께 주울 때, 평면 쪽 하나만 후보가
// 되고 반복 실행해도 revision 이 오르지 않는지 본다 (PATH_DESIGN v3 §3).
//
// 운영에서 터지는 지점: 서울시 전환기처럼 평면 잔재와 HH 폴더 사본이
// 공존하고 크기가 다르면, 두 파일이 번갈아 common_ledger 를 갱신하는 순간
// 매시간 revision +1 → 재전송이 끝없이 반복된다. 이 테스트는 재귀 Scanner 와
// 기존 put 중복 가드(runner.go seen)가 함께 그 루프를 막는다는 것을 고정한다.
func TestRunner_FlatAndHourCopyDoNotLoopRevision(t *testing.T) {
	const name = "dup00001.rnx.gz"

	db, _ := xferTestDB(t)
	ctx := context.Background()

	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	jobs := xferJobs(t)
	dayDir := jobs[0].LocalPath.Expand(when) // "in/2026/001/"
	hourDir := "in/2026/001/03"              // Scanner.joinChild 결과와 같은 표기

	lister := fakeLister{dirs: map[string][]scan.Entry{
		dayDir: {
			{Name: name, Size: 100, MTime: mtime}, // 평면
			{Name: "03", IsDir: true, Type: fs.ModeDir},
		},
		hourDir: {
			{Name: name, Size: 200, MTime: mtime}, // 같은 이름, 다른 크기
		},
	}}

	newRunner := func() *Runner {
		return &Runner{
			Scanner:  scan.New(lister),
			DB:       db,
			Verifier: verify.Verifier{},
			Opts: RunOptions{
				MaxRetries: 5,
				Logger:     xferRunner(db).Opts.Logger,
				Now:        func() time.Time { return when },
			},
		}
	}

	rng := scan.Range{From: when, To: when}

	for run := 1; run <= 3; run++ {
		kept, report, err := newRunner().Run(ctx, jobs, rng)
		if err != nil {
			t.Fatalf("run %d: Run() 실패: %v", run, err)
		}

		if len(kept) != 1 {
			t.Fatalf("run %d: 후보 = %d, want 1 (report=%+v)", run, len(kept), report)
		}

		c := kept[0]

		if c.Size != 100 || c.LocalPath != filepath.Join(dayDir, name) {
			t.Errorf(
				"run %d: 후보 = {Size:%d LocalPath:%q}, want 평면 파일 {100 %q}",
				run, c.Size, c.LocalPath, filepath.Join(dayDir, name),
			)
		}

		if got := report.Categories[0].SkippedDuplicate; got != 1 {
			t.Errorf("run %d: SkippedDuplicate = %d, want 1", run, got)
		}

		known, err := db.LookupCommon(ctx, xferTestCat, []string{name})
		if err != nil {
			t.Fatalf("run %d: LookupCommon() 실패: %v", run, err)
		}

		if rev := known[name].Revision; rev != 1 {
			t.Fatalf("run %d: revision = %d, want 1 (평면/사본이 번갈아 장부를 갱신한다)", run, rev)
		}
	}
}
