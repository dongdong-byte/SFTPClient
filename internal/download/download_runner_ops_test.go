package download

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"SFTPClient/internal/domain"
)

// DOWNLOAD 커밋 5 리뷰에서 찾은 운영 치명 지점을 고정한다.
//
//	나열 뒤 원격이 커짐     → GraceSeconds=0 에서 쓰는 중인 파일을 받는 경우다.
//	                          잘린 파일이 최종화되면 다음 회차부터 "충돌"로 남아
//	                          자동으로는 영원히 복구되지 않는다
//	나열 뒤 원격이 사라짐   → 빈 .part·최종 파일이 남으면 다음 회차가 기존으로
//	                          오인하거나 PUT 이 0바이트를 본다
//	목적지 폴더 자리에 파일 → 한 Category 의 로컬 문제가 회차 전체를 멈추면
//	                          멀쩡한 Category 까지 수신이 끊긴다

// GraceSeconds=0 이고 원본이 제자리에서 계속 쓰이면, 나열 때의 크기와
// 수신한 크기가 달라진다. 이때 최종화하지 않고 실패로 남겨 다음 회차가
// 안정된 크기로 다시 받아야 한다.
func TestRun_RemoteGrowingAfterListingIsNotFinalized(t *testing.T) {
	e := newRunEnv(t)

	const remote = "/RNX/2026/268/soch268a.26o.gz"

	e.remote.put(t, remote, "first-half")

	// 나열 이후, 복사 직전에 원본이 더 쓰인다.
	e.remote.before = func(_ context.Context, r, _ string) error {
		f, err := os.OpenFile(e.remote.localOf(r), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()

		_, err = f.WriteString("+second-half")

		return err
	}

	job := e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/")

	rep, err := e.run(t, context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	if cr := rep.Categories[0]; cr.Failed != 1 || cr.Downloaded != 0 {
		t.Fatalf("growing remote: report = %+v, want 1 failure and no download", cr)
	}

	if tree := e.localTree(t); len(tree) != 0 {
		t.Fatalf("local tree = %v, a truncated file must not be finalized", tree)
	}

	// 원본이 안정되면 다음 회차가 온전한 파일을 받는다.
	e.remote.before = nil

	rep, err = e.run(t, context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	if cr := rep.Categories[0]; cr.Downloaded != 1 || cr.Failed != 0 {
		t.Fatalf("next run: report = %+v", cr)
	}

	if got := mustRead(t, e.localFile("2026/268/soch268a.26o.gz")); got != "first-half+second-half" {
		t.Fatalf("final content = %q", got)
	}
}

// 나열과 수신 사이에 원격 파일이 사라지면 그 파일만 실패하고, 로컬에는
// 아무것도 남지 않는다. 다음 회차는 목록에 없으니 조용히 끝난다.
func TestRun_RemoteVanishedAfterListing(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "a")
	e.remote.put(t, "/RNX/2026/268/soch268b.26o.gz", "b")

	e.remote.before = func(_ context.Context, r, _ string) error {
		if strings.HasSuffix(r, "soch268a.26o.gz") {
			if err := os.Remove(e.remote.localOf(r)); err != nil {
				return err
			}
		}

		return nil
	}

	job := e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/")

	rep, err := e.run(t, context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	cr := rep.Categories[0]
	if cr.Failed != 1 || cr.Downloaded != 1 {
		t.Fatalf("report = %+v, want 1 failure and 1 download", cr)
	}

	if len(cr.Failures) != 1 || !errors.Is(cr.Failures[0].Err, fs.ErrNotExist) {
		t.Errorf("failure = %v, want the not-exist cause kept", cr.Failures)
	}

	if tree := e.localTree(t); !reflect.DeepEqual(tree, []string{"2026/268/soch268b.26o.gz"}) {
		t.Fatalf("local tree = %v, want only the surviving file (no empty .part or final)", tree)
	}

	// 다음 회차: 사라진 파일은 목록에 없다. 실패가 반복되지 않는다.
	e.remote.before = nil

	rep, err = e.run(t, context.Background(), job)
	if err != nil {
		t.Fatal(err)
	}

	if cr := rep.Categories[0]; cr.Failed != 0 || cr.Downloaded != 0 || cr.Reasons[ReasonExists] != 1 {
		t.Fatalf("next run: report = %+v", cr)
	}
}

// 한 Category 의 목적지 폴더 자리에 일반 파일이 있어 폴더를 만들 수 없어도
// 그 Category 의 파일만 실패하고, 다음 Category 는 정상으로 받는다.
// 회차 자체는 오류로 끝나지 않는다 (파일 단위 실패는 다음 회차의 몫).
func TestRun_LocalDirBlockedFailsOnlyThatCategory(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/R2H/2026/268/soch268a.26o.gz", "hourly")
	e.remote.put(t, "/R2D/2026/268/soch2680.26o.gz", "daily")

	// Hourly 목적지의 (SITE) 폴더 자리에 파일이 있다.
	blocker := e.localFile("h/2026/268/SOCH")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(blocker, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := e.run(t, context.Background(),
		e.job(t, domain.CategoryRINEX2Hourly, "/R2H/(YYYY)/(DOY)/", "h/(YYYY)/(DOY)/(SITE)/"),
		e.job(t, domain.CategoryRINEX2Daily, "/R2D/(YYYY)/(DOY)/", "d/(YYYY)/(DOY)/"),
	)
	if err != nil {
		t.Fatalf("a per-file local failure must not abort the run: %v", err)
	}

	if len(rep.Categories) != 2 {
		t.Fatalf("categories = %d, want both processed", len(rep.Categories))
	}

	if h := rep.Categories[0]; h.Failed != 1 || h.Downloaded != 0 {
		t.Errorf("hourly = %+v, want 1 failure", h)
	}

	if d := rep.Categories[1]; d.Downloaded != 1 || d.Failed != 0 {
		t.Errorf("daily = %+v, want 1 download", d)
	}

	if got := mustRead(t, blocker); got != "not a folder" {
		t.Errorf("the blocking file was modified: %q", got)
	}

	if !strings.Contains(e.logs.String(), "[DOWNLOAD] 실패 원격=/R2H/2026/268/soch268a.26o.gz") {
		t.Errorf("per-file failure log missing:\n%s", e.logs.String())
	}
}
