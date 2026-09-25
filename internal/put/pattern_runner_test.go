package put

import (
	"bytes"
	"context"
	"io/fs"
	"log"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// 경로 토큰 커밋 5 — 원본 패턴 단(PATH v4)이 PUT 러너와 만났을 때의
// 운영상 치명적인 경우. config 는 커밋 6 전까지 PUT LocalPath 의 파일
// 토큰을 거부하므로 여기서는 CategoryJob 을 직접 만든다.

func patternJobs(t *testing.T, local string) []CategoryJob {
	t.Helper()

	jobs := xferJobs(t)

	tpl, err := pathpl.Parse(local)
	if err != nil {
		t.Fatalf("pathpl.Parse(%q) 실패: %v", local, err)
	}
	jobs[0].LocalPath = tpl

	return jobs
}

func patternRunner(t *testing.T, lister scan.DirLister, logs *bytes.Buffer, when time.Time) *Runner {
	t.Helper()

	db, _ := xferTestDB(t)

	return &Runner{
		Scanner:  scan.New(lister),
		DB:       db,
		Verifier: verify.Verifier{},
		Opts: RunOptions{
			MaxRetries: 5,
			Logger:     log.New(logs, "", 0),
			Now:        func() time.Time { return when },
		},
	}
}

// 치명: 같은 이름의 파일이 두 관측소 폴더에 있다(수신기 설정 오류, D2).
// 크기가 다르면 두 파일이 번갈아 common_ledger 를 갱신해 매 회차
// revision +1 → 재전송이 끝없이 반복될 수 있다. 기존 중복 가드(seen)가
// 패턴 루트 사이에서도 먼저 본 하나만 후보로 두는지 고정한다.
func TestRunner_SameNameAcrossSiteFoldersDoesNotLoopRevision(t *testing.T) {
	const name = "dup00001.rnx.gz"

	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/": {
			{Name: "DBON", IsDir: true, Type: fs.ModeDir},
			{Name: "SUW1", IsDir: true, Type: fs.ModeDir},
		},
		"in/DBON/2026/001/": {{Name: name, Size: 100, MTime: mtime}},
		"in/SUW1/2026/001/": {{Name: name, Size: 200, MTime: mtime}},
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)
	jobs := patternJobs(t, "in/(SITE)/(YYYY)/(DOY)/")
	rng := scan.Range{From: when, To: when}

	for run := 1; run <= 3; run++ {
		kept, report, err := r.Run(context.Background(), jobs, rng)
		if err != nil {
			t.Fatalf("run %d: Run() 실패: %v", run, err)
		}

		if len(kept) != 1 || kept[0].Size != 100 {
			t.Fatalf("run %d: 후보 = %+v, want DBON 쪽 하나 (Size 100)", run, kept)
		}

		if got := report.Categories[0].SkippedDuplicate; got != 1 {
			t.Errorf("run %d: SkippedDuplicate = %d, want 1", run, got)
		}

		known, err := r.DB.LookupCommon(context.Background(), xferTestCat, []string{name})
		if err != nil {
			t.Fatalf("run %d: LookupCommon() 실패: %v", run, err)
		}
		if rev := known[name].Revision; rev != 1 {
			t.Fatalf("run %d: revision = %d, want 1 (두 폴더가 번갈아 장부를 갱신한다)", run, rev)
		}
	}
}

// 치명: 관측소 폴더 이름이 패턴과 전부 다르면(5자리 등) 파일 0개로
// 조용히 끝난다. 러너는 이 경우에만 WARN 을 남겨야 한다.
func TestRunner_WarnsWhenNoFolderMatchesPattern(t *testing.T) {
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/": {
			{Name: "DBON1", IsDir: true, Type: fs.ModeDir},
			{Name: "SUW10", IsDir: true, Type: fs.ModeDir},
		},
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)

	kept, _, err := r.Run(
		context.Background(),
		patternJobs(t, "in/(SITE)/(YYYY)/(DOY)/"),
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("후보 = %d, want 0", len(kept))
	}

	out := logs.String()
	for _, want := range []string{
		"no folder matched the LocalPath token pattern",
		"unmatched=2",
		`unmatched path="in/DBON1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("로그에 %q 가 없다:\n%s", want, out)
		}
	}
}

// 치명: 토큰 위치를 한 단계 잘못 적어 패턴 부모에 실제 데이터 파일만
// 있으면 Unmatched 폴더가 하나도 없다. PatternParentFiles 신호까지 경고
// 조건에 포함하지 않으면 전송 0건으로 조용히 끝난다.
func TestRunner_WarnsWhenPatternParentContainsOnlyFiles(t *testing.T) {
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/": {{Name: "dbon001a.26o.gz", Size: 10}},
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)

	kept, _, err := r.Run(
		context.Background(),
		patternJobs(t, "in/(SITE)/(YYYY)/(DOY)/"),
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("후보=%d, want 0", len(kept))
	}

	out := logs.String()
	for _, want := range []string{
		"no folder matched the LocalPath token pattern",
		"unmatched=0 parent_files=1",
		`pattern parent file="in/dbon001a.26o.gz"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("로그에 %q가 없다:\n%s", want, out)
		}
	}
}

// 거짓 경고 방지: 정상 관측소 루트가 하나라도 있으면 같은 부모의 평면
// 파일은 잡음일 수 있다. 파일은 집계하되 운영 WARN은 내지 않는다.
func TestRunner_NoWarnWhenPatternRootAndParentFileCoexist(t *testing.T) {
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/": {
			{Name: "README.txt", Size: 1},
			{Name: "DBON", IsDir: true, Type: fs.ModeDir},
		},
		"in/DBON/2026/001/": {{Name: "a0000001.rnx.gz", Size: 10, MTime: mtime}},
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)

	kept, _, err := r.Run(
		context.Background(),
		patternJobs(t, "in/(SITE)/(YYYY)/(DOY)/"),
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("후보=%d, want 1", len(kept))
	}
	if strings.Contains(logs.String(), "token pattern") ||
		strings.Contains(logs.String(), "pattern parent file") {
		t.Errorf("정상 루트가 있으면 패턴 경고를 내지 않는다:\n%s", logs.String())
	}
}

// 거짓 경고 방지: 관측소 폴더는 맞았고 backup 폴더만 따로 있으면
// 경고하지 않는다 (D3). 매 회차 경고가 뜨면 운영자가 경고를 무시하게 된다.
func TestRunner_NoWarnWhenSomeFolderMatches(t *testing.T) {
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/": {
			{Name: "DBON", IsDir: true, Type: fs.ModeDir},
			{Name: "backup", IsDir: true, Type: fs.ModeDir},
		},
		"in/DBON/2026/001/": {{Name: "a0000001.rnx.gz", Size: 10, MTime: mtime}},
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)

	kept, _, err := r.Run(
		context.Background(),
		patternJobs(t, "in/(SITE)/(YYYY)/(DOY)/"),
		scan.Range{From: when, To: when},
	)
	if err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}
	if len(kept) != 1 {
		t.Fatalf("후보 = %d, want 1", len(kept))
	}
	if strings.Contains(logs.String(), "unmatched") {
		t.Errorf("일부가 맞았으면 경고하지 않는다:\n%s", logs.String())
	}
}

// 기존 설정(파일 토큰 없음)은 새 경고 줄이 절대 나오지 않는다 (I2).
func TestRunner_TokenFreeConfigHasNoPatternLog(t *testing.T) {
	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)

	var logs bytes.Buffer
	r := patternRunner(t, fakeLister{dirs: map[string][]scan.Entry{}}, &logs, when)

	if _, _, err := r.Run(context.Background(), xferJobs(t), scan.Range{From: when, To: when}); err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}
	if strings.Contains(logs.String(), "unmatched") || strings.Contains(logs.String(), "token pattern") {
		t.Errorf("토큰 없는 설정에서 패턴 로그가 나왔다:\n%s", logs.String())
	}
}
