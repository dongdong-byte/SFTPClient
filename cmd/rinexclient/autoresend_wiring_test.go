package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/ledger"

	_ "modernc.org/sqlite"
)

// TestRun_AutoResendRunsAfterPut 는 정시 회차(run)가 실제로
//
//	① put(Hot) Run → ① Transfer → ② 자동 resend Run → ② Transfer
//
// 순서로 이어지는지를 localfs 로 끝까지 실행해 고정한다 (resend 설계 v4 §6.1).
//
// resendBudget 단위 테스트는 예산 계산만 본다. main 배선이 빠지거나
// (예: main.go 가 커밋에서 누락) ② 가 ① 앞으로 옮겨져도 그 테스트는
// 통과한다. 이 테스트는 run() 을 그대로 호출하므로 두 사고를 모두 잡는다.
//
// 배치:
//
//	today       Hot 창   → ① 이 보내야 한다
//	today−10    자동 창  → ② 가 보내야 한다 (ScanDays=7 밖, limit 안)
//	today−34    창 밖    → 아무도 보내면 안 된다 (limit = today−33, R=35)
//
// origin 은 today−60 으로 미리 기록해 §3.5 하한이 창을 막지 않게 한다.
//
// run() 은 flag.CommandLine 에 플래그를 정의하므로 한 테스트 프로세스에서
// 한 번만 호출할 수 있다. run() 을 부르는 테스트는 이것 하나로 둔다.
func TestRun_AutoResendRunsAfterPut(t *testing.T) {
	// t.TempDir 을 쓰지 않는다 — run() 이 연 로그 파일 핸들을 닫지 않아
	// Windows 에서 TempDir 정리가 실패로 보고된다. 정리는 최선 노력이다.
	root, err := os.MkdirTemp("", "autoresend-wiring-*")
	if err != nil {
		t.Fatal(err)
	}

	origArgs := os.Args
	t.Cleanup(func() {
		os.Args = origArgs
		log.SetOutput(os.Stderr)
		_ = os.RemoveAll(root)
	})

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)

	hotDay := today
	resendDay := today.AddDate(0, 0, -10)
	outsideDay := today.AddDate(0, 0, -34)
	origin := today.AddDate(0, 0, -60)

	localRoot := filepath.Join(root, "local")
	remoteRoot := filepath.Join(root, "remote")
	dataDir := filepath.Join(root, "data")
	logDir := filepath.Join(root, "logs")
	ledgerPath := filepath.Join(dataDir, "ledger.db")

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}

	hotName := writeDaily(t, localRoot, "hotx", hotDay)
	resendName := writeDaily(t, localRoot, "oldx", resendDay)
	outsideName := writeDaily(t, localRoot, "outx", outsideDay)

	seedOrigin(t, ledgerPath, origin)

	cfgPath := filepath.Join(root, "config.ini")
	writeConfig(t, cfgPath, localRoot, remoteRoot, ledgerPath, logDir)

	os.Args = []string{"rinexclient", "--config", cfgPath}

	if err := run(); err != nil {
		t.Fatalf("run() = %v", err)
	}

	// ── 전송 결과 ─────────────────────────────────────────────
	if !remoteExists(remoteRoot, hotDay, hotName) {
		t.Errorf("① Hot 파일이 전송되지 않았다: %s", hotName)
	}

	if !remoteExists(remoteRoot, resendDay, resendName) {
		t.Errorf("② 자동 resend 가 연결되지 않았다 — today−10 파일 미전송: %s", resendName)
	}

	if remoteExists(remoteRoot, outsideDay, outsideName) {
		t.Errorf("Retention 한계 밖(today−34) 파일이 전송되었다: %s", outsideName)
	}

	// ── 순서 ────────────────────────────────────────────────
	lines := readLogLines(t, logDir)

	hotSummary := indexOf(lines, 0, "range=hot")
	putXfer := indexOf(lines, hotSummary+1, "[XFER]")
	resendStart := indexOf(lines, putXfer+1, "[RESEND] auto")
	resendSummary := indexOf(lines, resendStart+1, "range=resend")
	resendXfer := indexOf(lines, resendSummary+1, "[XFER]")

	if hotSummary < 0 || putXfer < 0 || resendStart < 0 ||
		resendSummary < 0 || resendXfer < 0 {
		t.Fatalf(
			"기대한 순서(① 요약 → ① XFER → [RESEND] auto → ② 요약 → ② XFER)를 "+
				"찾지 못했다: hot=%d putXfer=%d start=%d resend=%d resendXfer=%d\n%s",
			hotSummary, putXfer, resendStart, resendSummary, resendXfer,
			strings.Join(lines, "\n"),
		)
	}

	// §7 시작 로그의 창은 today−33 ~ today−7 이다 (R=35, ScanDays=7).
	wantWindow := fmt.Sprintf(
		"from=%s to=%s budget=3999",
		today.AddDate(0, 0, -33).Format(time.DateOnly),
		today.AddDate(0, 0, -7).Format(time.DateOnly),
	)
	if !strings.Contains(lines[resendStart], wantWindow) {
		t.Errorf("② 시작 로그 = %q, want 포함 %q", lines[resendStart], wantWindow)
	}

	// ② 요약이 ① 요약보다 앞에 있으면 안 된다 (resend 가 put 앞에서 실행).
	if first := indexOf(lines, 0, "range=resend"); first < hotSummary {
		t.Errorf("② 요약(line %d)이 ① 요약(line %d)보다 먼저다", first, hotSummary)
	}

	if !strings.Contains(lines[putXfer], "verified=1") {
		t.Errorf("① XFER 가 Hot 파일 1건만 보내야 한다: %s", lines[putXfer])
	}

	if !strings.Contains(lines[resendXfer], "verified=1") {
		t.Errorf("② XFER 가 today−10 파일 1건만 보내야 한다: %s", lines[resendXfer])
	}
}

// writeDaily 는 RINEX2 Daily 관측 파일 하나를 (YYYY)\(DOY) 디렉터리에 만든다.
// mtime 은 Grace(60초)를 넉넉히 넘긴 과거로 둔다.
func writeDaily(t *testing.T, localRoot, station string, day time.Time) string {
	t.Helper()

	name := fmt.Sprintf(
		"%s%03d0.%02do.gz",
		station,
		day.YearDay(),
		day.Year()%100,
	)

	dir := dayDir(localRoot, day)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("rinex "+name), 0o644); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	return name
}

func dayDir(base string, day time.Time) string {
	return filepath.Join(
		base,
		fmt.Sprintf("%04d", day.Year()),
		fmt.Sprintf("%03d", day.YearDay()),
	)
}

func remoteExists(remoteRoot string, day time.Time, name string) bool {
	_, err := os.Stat(filepath.Join(dayDir(remoteRoot, day), name))
	return err == nil
}

// seedOrigin 은 스키마를 만든 뒤 operation_origin 을 미리 기록한다.
// 빈 장부는 origin 을 기록하지 않고 "오늘"로 계산하므로(커밋 4), 그대로
// 두면 자동 창이 before_origin 으로 비어 ② 를 관측할 수 없다.
func seedOrigin(t *testing.T, ledgerPath string, origin time.Time) {
	t.Helper()

	ctx := context.Background()

	db, err := ledger.Open(ctx, ledgerPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	if _, err := raw.ExecContext(
		ctx,
		`INSERT INTO schema_meta (key, value, updated_at)
		 VALUES ('operation_origin', ?, strftime('%s', 'now'));`,
		origin.Format("2006-01-02"),
	); err != nil {
		t.Fatal(err)
	}
}

func writeConfig(t *testing.T, path, localRoot, remoteRoot, ledgerPath, logDir string) {
	t.Helper()

	// DeepScanHour 를 현재 시각의 반대편에 두어 ① 을 Hot 창으로 고정한다.
	deepHour := (time.Now().Hour() + 12) % 24

	// OS 구분자로 조립한다 — 다음 설치처가 Linux 이므로 백슬래시를
	// 하드코딩하지 않는다. 비활성 카테고리의 D:\ 경로는 스캔되지 않는다.
	sep := string(os.PathSeparator)
	local := localRoot + sep + "(YYYY)" + sep + "(DOY)" + sep
	remote := filepath.ToSlash(remoteRoot) + `/(YYYY)/(DOY)/`

	ini := fmt.Sprintf(`[GENERAL]
Mode = put
Transport = localfs
RepostDownloaded = false
LedgerPath = %s
LockStaleSeconds = 10800

[SCAN]
ScanRecentDays = 2
ScanDays = 7
DeepScanHour = %d
UseDirMtimeSkip = false

[INGRESS]
GraceSeconds = 60

[LEDGER]
RetentionDays = 35

[PUT]
MaxWorkers = 2
MaxRetries = 5
MaxFilesPerRun = 4000
MaxHashBackfillPerRun = 500

[PUT.SFTP]
AuthMethod = publickey
Host = 127.0.0.1
Port = 22
User = test
PrivateKey = keys/id_ed25519
KnownHosts = keys/known_hosts
StallTimeoutSeconds = 30

[PUT.RINEX2_DAILY]
Enabled    = true
LocalPath  = %s
RemotePath = %s

[PUT.RINEX2_HOURLY]
Enabled    = false
HourLayout = dir
LocalPath  = D:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\
RemotePath = /RINEX2Outgoing/Hourly/(YYYY)/(DOY)/(HH)/

[PUT.RINEX3_DAILY]
Enabled    = false
LocalPath  = D:\RINEX-V3-D\(YYYY)\(DOY)\
RemotePath = /RINEX3Outgoing/Daily/(YYYY)/(DOY)/

[PUT.RINEX3_HOURLY]
Enabled    = false
HourLayout = dir
LocalPath  = D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\
RemotePath = /RINEX3Outgoing/Hourly/(YYYY)/(DOY)/(HH)/

[PUT.RINEX4_DAILY]
Enabled = false
LocalPath = D:\RINEX-V4-D\(YYYY)\(DOY)\
RemotePath = /RNXOutgoing/V4/(YYYY)/(DOY)/

[PUT.RINEX4_HOURLY]
Enabled = false
HourLayout = dir
LocalPath = D:\RINEX-V4-H\(YYYY)\(DOY)\(HH)\
RemotePath = /RNXOutgoing/V4/(YYYY)/(DOY)/(HH)/

[LOG]
Level = info
Dir = %s
RetentionDays = 30
`,
		ledgerPath,
		deepHour,
		local,
		remote,
		logDir,
	)

	if err := os.WriteFile(path, []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readLogLines(t *testing.T, logDir string) []string {
	t.Helper()

	files, err := filepath.Glob(filepath.Join(logDir, "rinexclient_*.log"))
	if err != nil || len(files) == 0 {
		t.Fatalf("로그 파일이 없다 (dir=%s): %v", logDir, err)
	}

	var lines []string

	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}

		lines = append(lines, strings.Split(string(b), "\n")...)
	}

	return lines
}

// indexOf 는 from 이후 sub 를 포함하는 첫 줄의 인덱스다. 없거나 from<0 이면 -1.
func indexOf(lines []string, from int, sub string) int {
	if from < 0 {
		return -1
	}

	for i := from; i < len(lines); i++ {
		if strings.Contains(lines[i], sub) {
			return i
		}
	}

	return -1
}
