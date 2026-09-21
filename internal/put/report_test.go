package put

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// UNIT4 (관측 가시화) 회귀 테스트.
//
// §9-1: evaluated 는 판정 네 출구의 합과 같고, 판정에 도달하지 못한
//        입력(거부·중복·임시·절단)은 분모 밖이다.
// §9-2: 0건·분모 0·모집단(mode/range)이 요약 줄 하나로 식별된다.
// §9-4: exhausted ≥ 1 이면 기존 WARN 이 나오고 예시는 상한으로 제한된다
//        (기존 동작의 회귀 고정 — 새 동작이 아니다).

// TestCategoryReportEvaluatedIdentity 는 분모 정의를 고정한다.
// 판정 밖 계수(거부·스킵·후보·절단)를 아무리 채워도 분모가 움직이지
// 않아야 한다 — 이 항등식이 깨지면 suppressed_ratio 전체가 무효다.
func TestCategoryReportEvaluatedIdentity(t *testing.T) {
	c := CategoryReport{
		Unchanged:    7,
		New:          5,
		Changed:      3,
		MetadataOnly: 2,

		// 전부 분모 밖이어야 하는 값들.
		Candidates:       99,
		Retries:          9,
		SkippedPart:      4,
		SkippedDuplicate: 4,
		SkippedDirs:      4,
		HashUnstable:     6,
		HashFailed:       6,
		Rejected:         map[string]int{"part": 11, "grace": 3},
	}

	if got, want := c.evaluated(), 7+5+3+2; got != want {
		t.Fatalf("evaluated() = %d, want %d (네 출구의 합)", got, want)
	}
}

// TestCategoryReportSuppressedRatio 는 비율 표시 계약을 고정한다:
// 분모 0 은 0% 가 아니라 n/a 다 (§3 — "정상 0%" 오독 방지).
func TestCategoryReportSuppressedRatio(t *testing.T) {
	zero := CategoryReport{}
	if got := zero.suppressedRatio(); got != "n/a" {
		t.Fatalf("분모 0 = %q, want \"n/a\"", got)
	}

	c := CategoryReport{Unchanged: 80, MetadataOnly: 20}
	if got := c.suppressedRatio(); got != "20.0%" {
		t.Fatalf("20/100 = %q, want \"20.0%%\"", got)
	}
}

// TestRunReportMode 는 모집단 식별자의 우선순위를 고정한다 —
// seed 는 dryrun 과 겹쳐도 seed 다 (§4: 어느 분포와도 섞지 않는다).
func TestRunReportMode(t *testing.T) {
	cases := []struct {
		name string
		rr   RunReport
		want string
	}{
		{"live", RunReport{}, "live"},
		{"dryrun", RunReport{DryRun: true}, "dryrun"},
		{"seed", RunReport{SeedMode: true}, "seed"},
		{"seed 가 dryrun 에 우선", RunReport{SeedMode: true, DryRun: true}, "seed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rr.mode(); got != tc.want {
				t.Fatalf("mode() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRunReportPrintIdentifiesPopulation 은 §9-2 를 고정한다:
// 요약 줄 하나만 보고 모집단(mode·range)과 분모·비율을 읽을 수 있다.
// Range 미설정은 조용한 기본값이 아니라 unset 으로 찍힌다.
func TestRunReportPrintIdentifiesPopulation(t *testing.T) {
	var buf bytes.Buffer

	rr := RunReport{
		SeedMode: true,
		Range:    "deep",
		Categories: []CategoryReport{{
			Category:     xferTestCat,
			Unchanged:    80,
			MetadataOnly: 20,
		}},
	}

	rr.Print(log.New(&buf, "", 0))

	out := buf.String()
	for _, want := range []string{
		"mode=seed",
		"range=deep",
		"evaluated=100",
		"suppressed_ratio=20.0%",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("요약에 %q 가 없다:\n%s", want, out)
		}
	}

	// 미설정 Range + 판정 0건 회차: unset / n/a 로 시끄럽게.
	buf.Reset()
	empty := RunReport{Categories: []CategoryReport{{Category: xferTestCat}}}
	empty.Print(log.New(&buf, "", 0))

	out = buf.String()
	for _, want := range []string{
		"mode=live",
		"range=unset",
		"evaluated=0",
		"suppressed_ratio=n/a",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("빈 회차 요약에 %q 가 없다:\n%s", want, out)
		}
	}
}

// TestExhaustedWarnRegression 은 §9-4 (기존 동작의 회귀 고정)다:
// 소진 제외가 1건 이상이면 [PUT][WARN] 이 나오고, 파일명 예시는
// exhaustedExampleCap(5) 로 제한되며 초과분은 "외 N건" 으로 접힌다.
//
// 시드는 invariant_test 와 같은 공개 API 체인(InsertPendingBatch →
// BeginPut → FailPut)으로 만든다. MaxRetries=1 이므로 attempts=1 이
// 곧 소진이다.
func TestExhaustedWarnRegression(t *testing.T) {
	db, _ := xferTestDB(t)
	ctx := context.Background()

	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	const total = 7 // 예시 상한 5 + "외 2건"

	names := make([]string, 0, total)
	for i := 0; i < total; i++ {
		names = append(names, fmt.Sprintf("xfile%03d.rnx.gz", i))
	}

	entries := make([]scan.Entry, 0, total)

	for _, name := range names {
		if _, err := db.UpsertCommon(ctx, ledger.CommonInput{
			FileName:          name,
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

		key := ledger.PutKey{
			Category: xferTestCat,
			FileName: name,
			Revision: known[name].Revision,
		}

		if err := db.InsertPendingBatch(
			ctx, []ledger.PutKey{key},
		); err != nil {
			t.Fatalf("InsertPendingBatch(%q) 실패: %v", name, err)
		}

		if err := db.BeginPut(
			ctx, key, "/out/x", "/out/x.part", 100, 5,
		); err != nil {
			t.Fatalf("BeginPut(%q) 실패: %v", name, err)
		}

		if err := db.FailPut(ctx, key, "seed failure"); err != nil {
			t.Fatalf("FailPut(%q) 실패: %v", name, err)
		}

		entries = append(entries, scan.Entry{
			Name:  name,
			Size:  100,
			MTime: mtime,
		})
	}

	jobs := xferJobs(t)
	dir := jobs[0].LocalPath.Expand(when)

	var buf bytes.Buffer

	r := &Runner{
		Scanner: scan.New(fakeLister{
			dirs: map[string][]scan.Entry{dir: entries},
		}),
		DB:       db,
		Verifier: verify.Verifier{}, // Grace=0
		Opts: RunOptions{
			MaxRetries: 1, // attempts=1 = 소진
			Logger:     log.New(&buf, "", 0),
			Now:        func() time.Time { return when },
		},
	}

	rng := scan.Range{From: when, To: when}

	kept, report, err := r.Run(ctx, jobs, rng)
	if err != nil {
		t.Fatalf("Run() 실패: %v", err)
	}

	if len(kept) != 0 {
		t.Fatalf("소진 파일이 후보에 남았다: %d건", len(kept))
	}

	if got := report.Categories[0].ExcludedExhausted; got != total {
		t.Fatalf("ExcludedExhausted = %d, want %d", got, total)
	}

	out := buf.String()

	// 검증 대상은 WARN "줄"이다. 버퍼 전체에서 파일명을 세면 러너의
	// 다른 계측 줄에 등장하는 같은 이름까지 합산된다 (첫 납품 테스트가
	// 실제로 그렇게 깨졌다 — 전체 19개 관측). 줄을 분리해 고정한다.
	var warnLine string

	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "[PUT][WARN]") &&
			strings.Contains(line, "exhausted=") {
			warnLine = line
			break
		}
	}

	if warnLine == "" {
		t.Fatalf("소진 WARN 줄이 없다:\n%s", out)
	}

	if !strings.Contains(
		warnLine, fmt.Sprintf("exhausted=%d", total),
	) {
		t.Fatalf("WARN 줄의 건수가 다르다: %s", warnLine)
	}

	// 예시는 상한 5개까지만, 초과분은 "외 2건" 으로 접힌다.
	if got := strings.Count(warnLine, "xfile"); got != exhaustedExampleCap {
		t.Errorf(
			"WARN 줄의 예시 파일명 %d개, want %d (exhaustedExampleCap): %s",
			got, exhaustedExampleCap, warnLine,
		)
	}

	if !strings.Contains(warnLine, "외 2건") {
		t.Errorf("초과분 접힘(\"외 2건\")이 없다: %s", warnLine)
	}
}
