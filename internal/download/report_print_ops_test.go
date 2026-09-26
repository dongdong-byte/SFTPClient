package download

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
)

// DOWNLOAD 커밋 6 리뷰에서 찾은 운영 치명 지점을 고정한다. 실제 러너 → Print
// 까지 이어 "고객 화면(console)"과 "로그 파일 전용(detail)"에 무엇이 남는지 본다.
//
//	(SITE) 패턴이 하나도 안 맞음 → 설정 오류다. 화면에 없으면 매시간
//	                               "대상없음"으로 조용히 끝난다 (PATH v4 D3)
//	종류미상 수신               → 받되 드러낸다. 단 기존 파일로 매시간 뜨면
//	                               기관 잡파일 하나가 화면에 영구히 남는다
//	기관 구조의 관찰            → 화면에 뜨지 않고 파일에는 반드시 남는다

// splitRun 은 러너와 Print 를 console·detail 두 버퍼로 나눠 돌린다.
func splitRun(t *testing.T, e *runEnv, jobs ...Job) (Report, string, string) {
	t.Helper()

	var console, detail bytes.Buffer

	cl := log.New(&syncWriter{w: &console}, "", 0)
	dl := log.New(&syncWriter{w: &detail}, "", 0)

	e.runner.Opts.Logger = cl
	e.runner.Opts.DetailLogger = dl

	rep, err := e.run(t, context.Background(), jobs...)
	if err != nil {
		t.Fatal(err)
	}

	rep.Range = RangeHot
	rep.Print(cl, dl)

	return rep, console.String(), detail.String()
}

// 원격 나열 실패는 파일 전송 실패가 0이어도 요청 범위를 완전히 확인하지
// 못한 운영 실패다. 커밋 7이 Report.Failed()로 종료 코드를 정할 때 성공으로
// 위장되지 않아야 한다. 요약용 파일 실패 수와는 분리해 둔다.
func TestReport_FailedIncludesScanErrors(t *testing.T) {
	t.Parallel()

	rep := Report{Categories: []CategoryReport{
		{Failed: 0, Scan: scan.Result{Errs: 2}},
		{Failed: 3, Scan: scan.Result{Errs: 1}},
	}}

	if got := rep.Failed(); got != 6 {
		t.Fatalf("Failed() = %d, want 6 (file failures 3 + scan failures 3)", got)
	}

	if got := rep.Categories[0].Failed; got != 0 {
		t.Fatalf("scan failure changed file-failure summary: %d", got)
	}
}

// 관측소 폴더가 5자리라 (SITE) 패턴에 하나도 안 맞는다. 화면에 경고와 맞지
// 않은 폴더가 나와야 하고, 같은 사실을 참고로 중복해 쓰지 않는다.
func TestPrint_NoSiteFolderMatchedWarnsOnScreen(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/SOCH0/soch268a.26o.gz", "a")
	e.remote.put(t, "/RNX/2026/268/PALM0/palm268a.26o.gz", "b")

	_, console, detail := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/(SITE)/", "(YYYY)/(DOY)/(SITE)/"))

	for _, want := range []string{
		"[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 대상없음",
		"[DOWNLOAD] 나열경고 종류=RINEX2_HOURLY 패턴폴더없음=2",
		"맞지않는폴더=/RNX/2026/268/PALM0",
		"맞지않는폴더=/RNX/2026/268/SOCH0",
	} {
		if !strings.Contains(console, want) {
			t.Errorf("console missing %q:\n%s", want, console)
		}
	}

	if strings.Contains(detail, "폴더불일치") {
		t.Errorf("the same fact must not be repeated as a note:\n%s", detail)
	}
}

// 일부만 안 맞으면(백업 폴더 등) 기관 구조의 잡음이다. 화면에는 요약만, 파일에는
// 참고가 남는다.
func TestPrint_PartialSiteMismatchIsFileOnly(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/SOCH/soch268a.26o.gz", "a")
	e.remote.put(t, "/RNX/2026/268/backup/old.26o.gz", "b")

	_, console, detail := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/(SITE)/", "(YYYY)/(DOY)/(SITE)/"))

	if strings.Contains(console, "나열경고") || strings.Contains(console, "backup") {
		t.Errorf("remote-structure noise reached the screen:\n%s", console)
	}

	if !strings.Contains(console, "수신=1") {
		t.Errorf("summary missing:\n%s", console)
	}

	if !strings.Contains(detail, "[DOWNLOAD] 나열참고 종류=RINEX2_HOURLY 폴더불일치=1") {
		t.Errorf("note must still be kept in the file:\n%s", detail)
	}
}

// 종류를 알 수 없는 파일은 받되 그 회차 요약에 종류미상으로 드러나고,
// 경로는 파일에 남는다. 다음 회차(이미 받아 둔 기존)에는 화면에 다시 뜨지
// 않는다.
func TestPrint_UnknownKindShownOnceThenQuiet(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "rinex")
	e.remote.put(t, "/RNX/2026/268/readme.txt", "junk")

	job := e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/")

	rep, console, detail := splitRun(t, e, job)

	if cr := rep.Categories[0]; cr.Downloaded != 2 || cr.UnknownKind != 1 {
		t.Fatalf("report = downloaded %d unknown %d, want 2 and 1", cr.Downloaded, cr.UnknownKind)
	}

	if !strings.Contains(console, "원격=2 수신=2 종류미상=1 ") {
		t.Errorf("summary must show 종류미상 right after 수신:\n%s", console)
	}

	if !strings.Contains(detail, "[DOWNLOAD] 종류미상 원격=/RNX/2026/268/readme.txt") {
		t.Errorf("per-file unknown line missing from the file log:\n%s", detail)
	}

	if strings.Contains(console, "readme.txt") {
		t.Errorf("per-file unknown path must not reach the screen:\n%s", console)
	}

	// 다음 회차: 둘 다 기존이다. 종류미상이 화면에 다시 뜨지 않는다.
	rep, console, _ = splitRun(t, e, job)

	if cr := rep.Categories[0]; cr.UnknownKind != 0 || cr.Reasons[ReasonExists] != 2 {
		t.Fatalf("next run = %+v", cr)
	}

	if strings.Contains(console, "종류미상") {
		t.Errorf("already-received junk must not reappear every hour:\n%s", console)
	}
}

// 날짜 폴더의 파일은 받았는데 그 아래 폴더 나열이 권한으로 실패하면,
// 요약 줄만 보면 수신 성공이다. 화면 경고와 Report.Failed 가 같이 있어야
// 커밋 7 이 성공 종료로 끝내지 않는다. 실패는 그 Category 에서 멈추지 않고
// 다음 Category 수신은 계속된다.
func TestPrint_ListErrorStaysVisibleAndDoesNotStopNextCategory(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/R2H/2026/268/soch268a.26o.gz", "hourly")
	e.remote.put(t, "/R2H/2026/268/backup/hidden.26o.gz", "hidden")
	e.remote.put(t, "/R2D/2026/268/soch2680.26o.gz", "daily")

	e.remote.listErr = func(dir string) error {
		if strings.HasSuffix(strings.TrimRight(dir, "/"), "/backup") {
			return errors.New("permission denied")
		}

		return nil
	}

	rep, console, _ := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/R2H/(YYYY)/(DOY)/", "h/(YYYY)/(DOY)/"),
		e.job(t, domain.CategoryRINEX2Daily, "/R2D/(YYYY)/(DOY)/", "d/(YYYY)/(DOY)/"),
	)

	if rep.Failed() != 1 {
		t.Fatalf("Failed() = %d, want 1 (listing failure is an operational failure)", rep.Failed())
	}

	if h := rep.Categories[0]; h.Downloaded != 1 || h.Failed != 0 || h.Scan.Errs != 1 {
		t.Fatalf("hourly = downloaded %d fileFailed %d errs %d", h.Downloaded, h.Failed, h.Scan.Errs)
	}

	if d := rep.Categories[1]; d.Downloaded != 1 || d.Scan.Errs != 0 {
		t.Fatalf("daily = %+v, listing failure in hourly must not stop it", d)
	}

	if !strings.Contains(console, "종류=RINEX2_HOURLY 범위=최근 원격=1 수신=1 ") {
		t.Errorf("summary missing the received file:\n%s", console)
	}

	if strings.Contains(console, " 실패=") {
		t.Errorf("file-failure field must stay separate from the listing warning:\n%s", console)
	}

	for _, want := range []string{
		"[DOWNLOAD] 나열경고 종류=RINEX2_HOURLY 나열실패=1",
		"나열실패 종류=RINEX2_HOURLY 폴더=",
		"원인=permission denied",
		"종류=RINEX2_DAILY 범위=최근 원격=1 수신=1 ",
	} {
		if !strings.Contains(console, want) {
			t.Errorf("console missing %q:\n%s", want, console)
		}
	}
}

// (SITE) 를 파일이 있는 층에 적으면 부모에는 파일만 보인다. 기록 없이 끝나면
// 매 회차 대상없음으로 수신이 멈춘다. 화면 경고와 파일 경로가 있어야 한다.
// 없는 폴더 자체는 종료 실패가 아니다 (2026-09-26 판단).
func TestPrint_TokenOneLevelOffWarnsWithTheFilePath(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "flat")

	rep, console, detail := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/(SITE)/", "(YYYY)/(DOY)/(SITE)/"))

	if rep.Failed() != 0 || rep.Downloaded() != 0 {
		t.Fatalf("report Failed=%d Downloaded=%d, want a warning without a failure exit",
			rep.Failed(), rep.Downloaded())
	}

	for _, want := range []string{
		"종류=RINEX2_HOURLY 범위=최근 대상없음",
		"[DOWNLOAD] 나열경고 종류=RINEX2_HOURLY 패턴폴더없음=1",
		"폴더밖파일=/RNX/2026/268/soch268a.26o.gz",
	} {
		if !strings.Contains(console, want) {
			t.Errorf("console missing %q:\n%s", want, console)
		}
	}

	if strings.Contains(detail, "폴더밖파일") || strings.Contains(detail, "나열참고") {
		t.Errorf("the screen warning must not be repeated as a file-only note:\n%s", detail)
	}

	if len(e.localTree(t)) != 0 {
		t.Fatalf("local tree = %v, a mis-placed remote file must not be received", e.localTree(t))
	}
}

// 범위 안의 날짜 폴더가 하나도 없으면 RemotePath 오기일 수 있다.
// 경고는 화면에 남기되, 없는 폴더만으로 종료 실패로 세지 않는다.
func TestPrint_NoDateFolderWarnsWithoutFailingTheRun(t *testing.T) {
	e := newRunEnv(t)

	rep, console, detail := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/NOPE/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))

	if rep.Failed() != 0 {
		t.Fatalf("Failed() = %d, missing folders are a warning, not an exit failure", rep.Failed())
	}

	if cr := rep.Categories[0]; cr.Scan.Missing == 0 || cr.Scan.Dirs != 0 {
		t.Fatalf("scan = %+v, want missing folders and no listed directory", cr.Scan)
	}

	if !strings.Contains(console, "대상없음") ||
		!strings.Contains(console, "[DOWNLOAD] 나열경고 종류=RINEX2_HOURLY 원격폴더없음=") {
		t.Fatalf("console = %s", console)
	}

	if strings.Contains(detail, "나열참고") {
		t.Errorf("missing folders are a screen warning, not a file-only note:\n%s", detail)
	}
}

// 대문자 RINEX 이름은 정규화하면 종류가 맞다. 종류미상으로 세면 정상 수신이
// 매 회차의 첫 다운로드마다 잡파일 경고로 보인다.
func TestPrint_UppercaseRinexIsNotUnknownKind(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/SOCH268A.26O.GZ", "rinex")

	rep, console, detail := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))

	if cr := rep.Categories[0]; cr.Downloaded != 1 || cr.UnknownKind != 0 {
		t.Fatalf("report = downloaded %d unknown %d", cr.Downloaded, cr.UnknownKind)
	}

	if strings.Contains(console, "종류미상") || strings.Contains(detail, "종류미상") {
		t.Errorf("uppercase RINEX counted as unknown:\n%s\n%s", console, detail)
	}
}

// Hourly 자리에 놓인 Daily 이름은 받지 않고 종류불일치로 남긴다.
// 종류미상으로 통과하면 주기가 다른 파일이 시간 폴더에 섞인다.
func TestPrint_DailyNameInHourlyIsMismatchNotDownloaded(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch2680.26o.gz", "daily-shaped")
	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "hourly")

	rep, console, _ := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))

	cr := rep.Categories[0]
	if cr.Downloaded != 1 || cr.UnknownKind != 0 || cr.Reasons[ReasonMismatch] != 1 {
		t.Fatalf("report = downloaded %d unknown %d reasons %v", cr.Downloaded, cr.UnknownKind, cr.Reasons)
	}

	if !strings.Contains(console, "원격=2 수신=1 종류불일치=1 ") {
		t.Errorf("summary = %s", console)
	}

	if strings.Contains(console, "종류미상") {
		t.Errorf("mismatch must not be reported as unknown kind:\n%s", console)
	}

	for _, f := range e.localTree(t) {
		if strings.Contains(f, "soch2680") {
			t.Errorf("daily-shaped file was received: %s", f)
		}
	}
}

// RINEX 이름만 받은 회차에는 종류미상 필드가 없다 (0 생략).
func TestPrint_NoUnknownKindFieldForRinexOnly(t *testing.T) {
	e := newRunEnv(t)

	e.remote.put(t, "/RNX/2026/268/soch268a.26o.gz", "rinex")

	_, console, detail := splitRun(t, e,
		e.job(t, domain.CategoryRINEX2Hourly, "/RNX/(YYYY)/(DOY)/", "(YYYY)/(DOY)/"))

	if strings.Contains(console, "종류미상") || strings.Contains(detail, "종류미상") {
		t.Errorf("종류미상 printed for a RINEX-only run:\n%s\n%s", console, detail)
	}
}
