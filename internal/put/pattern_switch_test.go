package put

import (
	"bytes"
	"context"
	"io/fs"
	"testing"
	"time"

	"SFTPClient/internal/scan"
)

// markSent 는 후보를 실제 전송과 같은 공개 전이로 VERIFIED 로 만든다
// (Run 이 이미 등록한 PENDING → IN_PROGRESS → VERIFIED). SeedVerified 는
// 기존 행이 있으면 아무것도 하지 않으므로(ON CONFLICT DO NOTHING) 쓰지
// 않는다 — 쓰면 PENDING 이 남아 "재전송"으로 잘못 보인다.
func markSent(t *testing.T, r *Runner, c Candidate) {
	t.Helper()

	ctx := context.Background()
	at := time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC)

	if err := r.DB.BeginPut(ctx, c.Key, "/out/"+c.Key.FileName, "/out/"+c.Key.FileName+".part", c.Size, 5); err != nil {
		t.Fatalf("BeginPut(%s) 실패: %v", c.Key.FileName, err)
	}
	if err := r.DB.FinishPut(ctx, c.Key, c.Size, at, at); err != nil {
		t.Fatalf("FinishPut(%s) 실패: %v", c.Key.FileName, err)
	}
}

// 경로 토큰 커밋 6 — 운영 동작이 처음 바뀌는 커밋의 치명 경우.
//
// 서울시는 PATH v3 배포 때 LocalPath 에서 (HH)\ 를 지웠거나(재귀), 옛
// 설정 그대로 남아 있다. 커밋 6 부터 두 설정이 모두 로드되므로, 현장에서
// 설정을 이쪽저쪽으로 바꿔 재배포하는 일이 실제로 생긴다.
//
// 그때 이미 보낸 파일이 다시 후보가 되면 스캔 범위(최대 수십 일) 전량
// 재전송이다. PUT 장부는 경로가 아니라 (category, 파일명)으로 식별하므로
// (커밋 계획 I1) 경로 모양이 바뀌어도 후보가 0 이어야 한다.
func TestRunner_SwitchingHourTokenDoesNotResend(t *testing.T) {
	const (
		f03 = "seou001d.rnx.gz"
		f13 = "seou001n.rnx.gz"
	)

	when := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 13, 30, 0, 0, time.UTC)

	hour03 := []scan.Entry{{Name: f03, Size: 100, MTime: mtime}}
	hour13 := []scan.Entry{{Name: f13, Size: 200, MTime: mtime}}

	// 같은 디스크를 두 방식으로 본다.
	//
	//	재귀    in/2026/001/  → joinChild 로 "in/2026/001/03" (끝 구분자 없음)
	//	(HH)   in/2026/001/  → 패턴 루트 "in/2026/001/03/" (끝 구분자 보존)
	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/2026/001/": {
			{Name: "03", IsDir: true, Type: fs.ModeDir},
			{Name: "13", IsDir: true, Type: fs.ModeDir},
		},
		"in/2026/001/03":  hour03,
		"in/2026/001/13":  hour13,
		"in/2026/001/03/": hour03,
		"in/2026/001/13/": hour13,
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)
	ctx := context.Background()
	rng := scan.Range{From: when, To: when}

	recursive := patternJobs(t, "in/(YYYY)/(DOY)/")
	hourToken := patternJobs(t, "in/(YYYY)/(DOY)/(HH)/")

	// 1회차: PATH v3 설정(재귀)으로 두 파일을 찾고, 전송 완료로 기록한다.
	kept, _, err := r.Run(ctx, recursive, rng)
	if err != nil {
		t.Fatalf("재귀 1회차 Run() 실패: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("재귀 1회차 후보 = %d, want 2", len(kept))
	}

	for _, c := range kept {
		markSent(t, r, c)
	}

	// 2회차: 서울시 옛 설정((HH))으로 재배포. 같은 파일이므로 후보 0.
	// 3회차: 다시 재귀 설정으로 되돌림. 역시 후보 0.
	for _, step := range []struct {
		name string
		jobs []CategoryJob
	}{
		{"(HH) 설정으로 전환", hourToken},
		{"재귀 설정으로 복귀", recursive},
	} {
		kept, _, err := r.Run(ctx, step.jobs, rng)
		if err != nil {
			t.Fatalf("%s: Run() 실패: %v", step.name, err)
		}
		if len(kept) != 0 {
			t.Fatalf("%s: 후보 = %d, want 0 (이미 보낸 파일 재전송)", step.name, len(kept))
		}

		known, err := r.DB.LookupCommon(ctx, xferTestCat, []string{f03, f13})
		if err != nil {
			t.Fatalf("%s: LookupCommon() 실패: %v", step.name, err)
		}
		for _, name := range []string{f03, f13} {
			if rev := known[name].Revision; rev != 1 {
				t.Fatalf("%s: %s revision = %d, want 1", step.name, name, rev)
			}
		}
	}
}

// (SITE) 를 새로 도입하는 기관이 기존 평면 설정에서 관측소 폴더 설정으로
// 옮기는 경우. 스캔하는 디렉터리가 달라져도 파일명이 같으면 재전송 없다.
func TestRunner_SwitchingToSiteTokenDoesNotResend(t *testing.T) {
	const name = "dbon0010.rnx.gz"

	when := time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC)
	mtime := time.Date(2026, 1, 1, 2, 30, 0, 0, time.UTC)

	entry := []scan.Entry{{Name: name, Size: 100, MTime: mtime}}

	// 같은 파일이 운영자가 폴더를 재배치하기 전(평면)과 후(관측소 폴더)에
	// 각각 보인다 — 재배치 직후 첫 회차 상황이다.
	lister := fakeLister{dirs: map[string][]scan.Entry{
		"in/2026/001/":      entry,
		"in/":               {{Name: "DBON", IsDir: true, Type: fs.ModeDir}},
		"in/DBON/2026/001/": entry,
	}}

	var logs bytes.Buffer
	r := patternRunner(t, lister, &logs, when)
	ctx := context.Background()
	rng := scan.Range{From: when, To: when}

	kept, _, err := r.Run(ctx, patternJobs(t, "in/(YYYY)/(DOY)/"), rng)
	if err != nil || len(kept) != 1 {
		t.Fatalf("평면 1회차: kept=%d err=%v", len(kept), err)
	}
	c := kept[0]
	markSent(t, r, c)

	kept, _, err = r.Run(ctx, patternJobs(t, "in/(SITE)/(YYYY)/(DOY)/"), rng)
	if err != nil {
		t.Fatalf("(SITE) 회차 Run() 실패: %v", err)
	}
	if len(kept) != 0 {
		t.Fatalf("(SITE) 회차 후보 = %d, want 0 (이미 보낸 파일 재전송)", len(kept))
	}
}
