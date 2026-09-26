package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
)

// DOWNLOAD 커밋 1 — config 에 [DOWNLOAD] 추가 (DOWNLOAD_COMMIT_PLAN v1 §2).
//
// 이 파일의 테스트는 §2.5 완료 조건을 하나씩 고정한다. 실행 게이트
// downloadImplemented 가 아직 false 이므로, Mode=download 의 "정상" 설정은
// Validate 에서 게이트 오류 하나만 남아야 한다 (onlyGateError).

// downloadOnlyINI 는 PUT 블록·[INGRESS]·[LEDGER]·LedgerPath 가 없는
// DOWNLOAD 전용 설정이다. §2.5 "Mode=download 는 LedgerPath 와 [LEDGER]
// 없이 로드·검증할 수 있다"의 기준 입력이다.
func downloadOnlyINI() string {
	return `[GENERAL]
Mode = download
Transport = sftp
RepostDownloaded = false
LockStaleSeconds = 10800
LockPath = data/rinexclient.lock

[SCAN]
ScanRecentDays = 2
ScanDays = 7
DeepScanHour = 4
UseDirMtimeSkip = false

[DOWNLOAD]
Sites = soch, PALM
MaxWorkers = 4
GraceSeconds = 60

[DOWNLOAD.SFTP]
AuthMethod = publickey
Host = 10.0.0.9
Port = 2022
User = getter
PrivateKey = keys/id_ed25519
KnownHosts = keys/known_hosts

[DOWNLOAD.RINEX2_DAILY]
Enabled = true
RemotePath = /RNX/(YYYY)/(DOY)/
LocalPath = /down/r2d/(YYYY)/(DOY)/

[DOWNLOAD.RINEX2_HOURLY]
Enabled = true
RemotePath = /RNX/(YYYY)/(DOY)/(SITE)/(HH)/
LocalPath = /down/r2h/(YYYY)/(DOY)/(SITE)/(HH)/

[DOWNLOAD.RINEX3_DAILY]
Enabled = false
RemotePath = /RNX3/(YYYY)/(DOY)/
LocalPath = /down/r3d/(YYYY)/(DOY)/

[DOWNLOAD.RINEX3_HOURLY]
Enabled = false
RemotePath = /RNX3/(YYYY)/(DOY)/
LocalPath = /down/r3h/(YYYY)/(DOY)/

[DOWNLOAD.RINEX4_DAILY]
Enabled = false
RemotePath = /RNX4/(YYYY)/(DOY)/
LocalPath = /down/r4d/(YYYY)/(DOY)/

[DOWNLOAD.RINEX4_HOURLY]
Enabled = false
RemotePath = /RNX4/(YYYY)/(DOY)/
LocalPath = /down/r4h/(YYYY)/(DOY)/

[LOG]
Level = info
Dir = logs
RetentionDays = 30
`
}

// downloadBlockINI 는 기존 PUT 설정 뒤에 덧붙일 DOWNLOAD 블록이다.
// 기본값은 PUT 과 다른 서버·다른 경로라 Ping-Pong 검사를 통과한다.
func downloadBlockINI() string {
	return `
[DOWNLOAD]
Sites =
MaxWorkers = 2
GraceSeconds = 0

[DOWNLOAD.SFTP]
AuthMethod = publickey
Host = 10.0.0.9
Port = 22
User = getter
PrivateKey = keys/id_ed25519
KnownHosts = keys/known_hosts

[DOWNLOAD.RINEX2_DAILY]
Enabled = true
RemotePath = /incoming/r2d/(YYYY)/(DOY)/
LocalPath = /down/r2d/(YYYY)/(DOY)/

[DOWNLOAD.RINEX2_HOURLY]
Enabled = true
RemotePath = /incoming/r2h/(YYYY)/(DOY)/
LocalPath = /down/r2h/(YYYY)/(DOY)/

[DOWNLOAD.RINEX3_DAILY]
Enabled = false
RemotePath = /incoming/r3d/(YYYY)/(DOY)/
LocalPath = /down/r3d/(YYYY)/(DOY)/

[DOWNLOAD.RINEX3_HOURLY]
Enabled = false
RemotePath = /incoming/r3h/(YYYY)/(DOY)/
LocalPath = /down/r3h/(YYYY)/(DOY)/

[DOWNLOAD.RINEX4_DAILY]
Enabled = false
RemotePath = /incoming/r4d/(YYYY)/(DOY)/
LocalPath = /down/r4d/(YYYY)/(DOY)/

[DOWNLOAD.RINEX4_HOURLY]
Enabled = false
RemotePath = /incoming/r4h/(YYYY)/(DOY)/
LocalPath = /down/r4h/(YYYY)/(DOY)/
`
}

// replaceLine 은 ini 원문에서 "Key = ..." 한 줄을 통째로 바꾼다.
// 한 섹션 안의 키만 다룬다 — 같은 이름의 키가 여러 섹션에 있으면
// section 헤더 이후 첫 등장만 바꾼다.
func replaceLine(t *testing.T, ini, section, key, newLine string) string {
	t.Helper()

	head := strings.Index(ini, "["+section+"]")
	if head < 0 {
		t.Fatalf("section [%s] not in test ini", section)
	}

	rest := ini[head:]
	lines := strings.Split(rest, "\n")

	for i, line := range lines {
		if i > 0 && strings.HasPrefix(line, "[") {
			break
		}

		if strings.HasPrefix(strings.TrimSpace(line), key) {
			lines[i] = newLine
			return ini[:head] + strings.Join(lines, "\n")
		}
	}

	t.Fatalf("key %s not in [%s]", key, section)
	return ""
}

// mustBeValid 는 정상 DOWNLOAD 설정이 Validate 를 오류 없이 통과하는지 본다.
//
// 커밋 1~6 동안은 "게이트 오류 하나만 남는다"(onlyGateError)가 기준이었다.
// 커밋 7 에서 게이트가 열려 기준이 "오류 없음"이 되었다.
func mustBeValid(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("expected a valid configuration, got:\n%v", err)
	}
}

func mustNotContain(t *testing.T, err error, text string) {
	t.Helper()

	if err != nil && strings.Contains(err.Error(), text) {
		t.Errorf("error must not contain %q:\n%v", text, err)
	}
}

func mustContain(t *testing.T, err error, text string) {
	t.Helper()

	if err == nil || !strings.Contains(err.Error(), text) {
		t.Errorf("error = %v, want substring %q", err, text)
	}
}

// ── §2.5-1 기존 PUT config 는 그대로 ────────────────────────────────

func TestDownload_AbsentInPutConfig_UnchangedBehaviour(t *testing.T) {
	cfg, path, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if cfg.Download.Present {
		t.Error("Download.Present = true for a config without [DOWNLOAD]")
	}

	if !cfg.Put.Present {
		t.Error("Put.Present = false for Mode=put")
	}

	// 경고는 종전과 같이 [PUT.SFTP] 세 건뿐이다.
	if len(cfg.Warnings) != 3 {
		t.Errorf("Warnings = %v, want exactly 3 [PUT.SFTP] warnings", cfg.Warnings)
	}

	for _, w := range cfg.Warnings {
		if !strings.HasPrefix(w, "[PUT.SFTP]") {
			t.Errorf("unexpected warning %q", w)
		}
	}

	// LockPath 생략 + put → 종전처럼 LedgerPath+".lock".
	want := filepath.Join(filepath.Dir(path), "data", "rinex_ledger.db") + ".lock"
	if got := cfg.LockFile(); got != want {
		t.Errorf("LockFile() = %q, want %q", got, want)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

// ── §2.5-2 Mode=download 는 LedgerPath·[LEDGER] 없이 로드·검증 ──────

func TestDownload_LoadsWithoutLedger(t *testing.T) {
	cfg, path, err := mapConfigForTest(t, downloadOnlyINI())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if !cfg.Download.Present {
		t.Fatal("Download.Present = false")
	}

	if cfg.Put.Present {
		t.Error("Put.Present = true although no PUT section exists")
	}

	if cfg.General.LedgerPath != "" {
		t.Errorf("LedgerPath = %q, want empty", cfg.General.LedgerPath)
	}

	if cfg.Download.MaxWorkers != 4 {
		t.Errorf("MaxWorkers = %d, want 4", cfg.Download.MaxWorkers)
	}

	if !cfg.Download.GraceSet || cfg.Download.Grace != 60*time.Second {
		t.Errorf(
			"Grace = %v (set=%v), want 60s set",
			cfg.Download.Grace,
			cfg.Download.GraceSet,
		)
	}

	// Sites 는 대문자 정규화·공백 제거.
	if !reflect.DeepEqual(cfg.Download.Sites, []string{"SOCH", "PALM"}) {
		t.Errorf("Sites = %v, want [SOCH PALM]", cfg.Download.Sites)
	}

	if cfg.Download.SFTP.Host != "10.0.0.9" || cfg.Download.SFTP.Port != 2022 {
		t.Errorf("SFTP = %+v, want 10.0.0.9:2022", cfg.Download.SFTP)
	}

	if cfg.Download.SFTP.StallTimeout != DefaultStallTimeoutSeconds*time.Second {
		t.Errorf("StallTimeout default not applied: %v", cfg.Download.SFTP.StallTimeout)
	}

	// 경고는 [DOWNLOAD.SFTP] 쪽만 나온다.
	for _, w := range cfg.Warnings {
		if !strings.HasPrefix(w, "[DOWNLOAD.SFTP]") {
			t.Errorf("unexpected warning %q", w)
		}
	}

	// LockPath 명시 → 그 경로 (config 기준 절대경로).
	want := filepath.Join(filepath.Dir(path), "data", "rinexclient.lock")
	if got := cfg.LockFile(); got != want {
		t.Errorf("LockFile() = %q, want %q", got, want)
	}

	mustBeValid(t, cfg.Validate())
}

// §2.5-6 DOWNLOAD RemotePath 에 (SITE)·(HH) 가 있어도 로드된다.
func TestDownload_RemotePathFileTokensLoad(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, downloadOnlyINI())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	cc, ok := cfg.Download.Lookup(domain.CategoryRINEX2Hourly)
	if !ok {
		t.Fatal("RINEX2_HOURLY not found")
	}

	if got := cc.RemotePath.FileTokens(); !reflect.DeepEqual(got, []string{"SITE", "HH"}) {
		t.Errorf("RemotePath.FileTokens() = %v, want [SITE HH]", got)
	}

	if got := cc.LocalPath.FileTokens(); !reflect.DeepEqual(got, []string{"SITE", "HH"}) {
		t.Errorf("LocalPath.FileTokens() = %v, want [SITE HH]", got)
	}

	enabled := cfg.Download.EnabledCategories()
	if len(enabled) != 2 {
		t.Errorf("EnabledCategories() = %d, want 2", len(enabled))
	}
}

// ── §2.5-3 LockPath ────────────────────────────────────────────────

func TestDownload_LockPathRequired(t *testing.T) {
	ini := strings.Replace(downloadOnlyINI(), "LockPath = data/rinexclient.lock\n", "", 1)

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if cfg.LockFile() != "" {
		t.Errorf("LockFile() = %q, want empty (nothing to derive from)", cfg.LockFile())
	}

	err = cfg.Validate()
	mustContain(t, err, "[GENERAL] LockPath is required when Mode = DOWNLOAD")
}

func TestLockFile_ExplicitWinsOverLedgerDerivation(t *testing.T) {
	cfg := validConfigForValidate(t)
	cfg.General.LockPath = "/elsewhere/run.lock"

	if got := cfg.LockFile(); got != "/elsewhere/run.lock" {
		t.Errorf("LockFile() = %q, want explicit LockPath", got)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("explicit LockPath in put mode must validate: %v", err)
	}
}

// LedgerPath 가 우연히 적혀 있어도 download 의 lock 은 거기서 접지 않는다.
func TestDownload_LockNotDerivedFromStrayLedgerPath(t *testing.T) {
	ini := strings.Replace(
		downloadOnlyINI(),
		"LockPath = data/rinexclient.lock\n",
		"LedgerPath = data/rinex_ledger.db\n",
		1,
	)

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if cfg.General.LedgerPath == "" {
		t.Fatal("stray LedgerPath should still be read")
	}
	if got := cfg.LockFile(); got != "" {
		t.Fatalf("LockFile() = %q, want empty in download mode", got)
	}

	mustContain(t, cfg.Validate(), "[GENERAL] LockPath is required")
}

// ── §2.5-4 Sites·Grace·Daily (HH) 각각 정확한 오류 ──────────────────

func TestDownload_SitesFormat(t *testing.T) {
	tests := []struct {
		name    string
		sites   string
		wantErr string
		want    []string
	}{
		{name: "생략(빈 값)은 전체", sites: "Sites =", want: nil},
		{name: "정상", sites: "Sites = dbon,SUW1", want: []string{"DBON", "SUW1"}},
		{name: "중복은 접는다", sites: "Sites = DBON,dbon", want: []string{"DBON"}},
		{name: "9자리 거부", sites: "Sites = DBON00KOR", wantErr: "[DOWNLOAD] Sites"},
		{name: "와일드카드 거부", sites: "Sites = DB*N", wantErr: "[DOWNLOAD] Sites"},
		{name: "빈 항목 거부", sites: "Sites = DBON,,SUW1", wantErr: "[DOWNLOAD] Sites"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ini := replaceLine(t, downloadOnlyINI(), "DOWNLOAD", "Sites", tt.sites)

			cfg, _, err := mapConfigForTest(t, ini)

			if tt.wantErr != "" {
				if !errors.Is(err, ErrBadValue) {
					t.Fatalf("error = %v, want ErrBadValue", err)
				}

				mustContain(t, err, tt.wantErr)
				return
			}

			if err != nil {
				t.Fatalf("mapConfig() unexpected error: %v", err)
			}

			if !reflect.DeepEqual(cfg.Download.Sites, tt.want) {
				t.Errorf("Sites = %v, want %v", cfg.Download.Sites, tt.want)
			}
		})
	}
}

func TestDownload_GraceSeconds(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{
			name: "키 없음 → 시작 거부 (부재를 기본값으로 접지 않는다)",
			mutate: func(ini string) string {
				return strings.Replace(ini, "GraceSeconds = 60\n", "", 1)
			},
			wantErr: "[DOWNLOAD] GraceSeconds is required when Mode = DOWNLOAD",
		},
		{
			name: "음수 → 거부",
			mutate: func(ini string) string {
				return replaceLine(t, ini, "DOWNLOAD", "GraceSeconds", "GraceSeconds = -1")
			},
			wantErr: "[DOWNLOAD] GraceSeconds must not be negative",
		},
		{
			name: "명시적 0 → 검사 끔, 허용",
			mutate: func(ini string) string {
				return replaceLine(t, ini, "DOWNLOAD", "GraceSeconds", "GraceSeconds = 0")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, _, err := mapConfigForTest(t, tt.mutate(downloadOnlyINI()))
			if err != nil {
				t.Fatalf("mapConfig() unexpected error: %v", err)
			}

			err = cfg.Validate()

			if tt.wantErr == "" {
				if !cfg.Download.GraceSet || cfg.Download.Grace != 0 {
					t.Errorf("Grace = %v set=%v, want 0 set", cfg.Download.Grace, cfg.Download.GraceSet)
				}

				mustBeValid(t, err)
				return
			}

			mustContain(t, err, tt.wantErr)
		})
	}
}

// PUT 의 [INGRESS] GraceSeconds 와 DOWNLOAD 의 GraceSeconds 는 별개 값이다.
func TestDownload_GraceIndependentFromIngress(t *testing.T) {
	ini := validINIForLoadTest() + downloadBlockINI()

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if cfg.Ingress.Grace != 60*time.Second {
		t.Errorf("Ingress.Grace = %v, want 60s", cfg.Ingress.Grace)
	}

	if cfg.Download.Grace != 0 || !cfg.Download.GraceSet {
		t.Errorf("Download.Grace = %v set=%v, want 0 set", cfg.Download.Grace, cfg.Download.GraceSet)
	}
}

func TestDownload_MaxWorkersRange(t *testing.T) {
	for _, bad := range []string{"0", "17", "-3"} {
		t.Run("MaxWorkers="+bad, func(t *testing.T) {
			ini := replaceLine(t, downloadOnlyINI(), "DOWNLOAD", "MaxWorkers", "MaxWorkers = "+bad)

			cfg, _, err := mapConfigForTest(t, ini)
			if err != nil {
				t.Fatalf("mapConfig() unexpected error: %v", err)
			}

			mustContain(t, cfg.Validate(), "[DOWNLOAD] MaxWorkers = "+bad+" must be between 1 and 16")
		})
	}
}

func TestDownload_DailyHourToken(t *testing.T) {
	tests := []struct {
		name    string
		section string
		key     string
		line    string
		wantErr string
	}{
		{
			name:    "Daily LocalPath 의 (HH) 는 거부",
			section: "DOWNLOAD.RINEX2_DAILY",
			key:     "LocalPath",
			line:    "LocalPath = /down/r2d/(YYYY)/(DOY)/(HH)/",
			wantErr: "[DOWNLOAD.RINEX2_DAILY] LocalPath uses (HH) but the category is daily",
		},
		{
			name:    "비활성 Daily 도 검사한다 (잠복 설정 방지)",
			section: "DOWNLOAD.RINEX4_DAILY",
			key:     "LocalPath",
			line:    "LocalPath = /down/r4d/(YYYY)/(DOY)/(HH)/",
			wantErr: "[DOWNLOAD.RINEX4_DAILY] LocalPath uses (HH)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ini := replaceLine(t, downloadOnlyINI(), tt.section, tt.key, tt.line)

			cfg, _, err := mapConfigForTest(t, ini)
			if err != nil {
				t.Fatalf("mapConfig() unexpected error: %v", err)
			}

			mustContain(t, cfg.Validate(), tt.wantErr)
		})
	}
}

// DOWNLOAD 원본 RemotePath의 (HH)는 파일명에서 채우는 값이 아니라 원격
// 폴더를 나열하는 패턴이다. Daily 파일명에 시각이 없어도 사용할 수 있다.
func TestDownload_DailyRemotePathHourTokenAllowed(t *testing.T) {
	ini := replaceLine(
		t,
		downloadOnlyINI(),
		"DOWNLOAD.RINEX2_DAILY",
		"RemotePath",
		"RemotePath = /RNX/(YYYY)/(DOY)/(HH)/",
	)

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	mustBeValid(t, cfg.Validate())
}

func TestDownload_NoEnabledCategory(t *testing.T) {
	ini := downloadOnlyINI()
	ini = replaceLine(t, ini, "DOWNLOAD.RINEX2_DAILY", "Enabled", "Enabled = false")
	ini = replaceLine(t, ini, "DOWNLOAD.RINEX2_HOURLY", "Enabled", "Enabled = false")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	err = cfg.Validate()
	mustContain(t, err, "[DOWNLOAD] no category is enabled")
	// PUT 쪽 문구는 그대로 남고 DOWNLOAD 에 재사용되지 않는다.
	mustNotContain(t, err, "\nno category is enabled")
}

func TestDownload_DuplicateSourcePath(t *testing.T) {
	ini := replaceLine(
		t,
		downloadOnlyINI(),
		"DOWNLOAD.RINEX2_HOURLY",
		"RemotePath",
		"RemotePath = /RNX/(YYYY)/(DOY)/",
	)

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	mustContain(
		t,
		cfg.Validate(),
		"[DOWNLOAD.RINEX2_HOURLY] RemotePath is identical to [DOWNLOAD.RINEX2_DAILY]",
	)
}

// Mode=download 에서는 [DOWNLOAD.SFTP] 값을 검증하고 [PUT.SFTP] 는 요구하지 않는다.
func TestDownload_SFTPSectionValidated(t *testing.T) {
	ini := replaceLine(t, downloadOnlyINI(), "DOWNLOAD.SFTP", "Host", "Host =")
	ini = replaceLine(t, ini, "DOWNLOAD.SFTP", "Port", "Port = 70000")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	err = cfg.Validate()
	mustContain(t, err, "[DOWNLOAD.SFTP] Host is empty")
	mustContain(t, err, "[DOWNLOAD.SFTP] Port = 70000 must be between 1 and 65535")
	mustNotContain(t, err, "[PUT.SFTP]")
	mustNotContain(t, err, "[PUT] MaxWorkers")
	mustNotContain(t, err, "[LEDGER]")
}

// ── 블록 존재 규칙 ─────────────────────────────────────────────────

// 절반만 적힌 DOWNLOAD 블록은 Mode=put 이어도 시끄럽게 거부한다.
func TestDownload_HalfBlockInPutModeIsLoud(t *testing.T) {
	ini := validINIForLoadTest() + "\n[DOWNLOAD]\nMaxWorkers = 4\nGraceSeconds = 0\n"

	_, _, err := mapConfigForTest(t, ini)
	if !errors.Is(err, ErrMissingKey) {
		t.Fatalf("error = %v, want ErrMissingKey", err)
	}

	mustContain(t, err, "section [DOWNLOAD.SFTP] not found")
	mustContain(t, err, "section [DOWNLOAD.RINEX2_DAILY] not found")
}

// PUT 블록이 통째로 있는 download 설정(템플릿 복사 후 Mode 만 바꾼 경우)은
// 로드되되 PUT 값은 판정하지 않는다.
func TestDownload_PutBlockPresentButNotJudged(t *testing.T) {
	ini := replaceLine(t, validINIForLoadTest(), "GENERAL", "Mode", "Mode = download")
	ini = replaceLine(t, ini, "GENERAL", "LockStaleSeconds",
		"LockStaleSeconds = 10800\nLockPath = data/rinexclient.lock")
	// PUT 쪽에 위험한 값을 넣어도 download 는 판정하지 않는다.
	ini = replaceLine(t, ini, "PUT", "MaxWorkers", "MaxWorkers = 99")
	ini = replaceLine(t, ini, "LEDGER", "RetentionDays", "RetentionDays = 1")
	ini += downloadBlockINI()

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if !cfg.Put.Present || !cfg.Download.Present {
		t.Fatalf("Present put=%v download=%v, want both", cfg.Put.Present, cfg.Download.Present)
	}

	if cfg.Put.MaxWorkers != 99 {
		t.Errorf("PUT block values should still be read: MaxWorkers = %d", cfg.Put.MaxWorkers)
	}

	mustBeValid(t, cfg.Validate())
}

// 알 수 없는 키는 DOWNLOAD 섹션에서도 종전 장치가 잡는다.
func TestDownload_UnknownKeyRejected(t *testing.T) {
	ini := strings.Replace(downloadOnlyINI(), "MaxWorkers = 4\n", "MaxWorkers = 4\nMaxWorker = 4\n", 1)

	_, _, err := mapConfigForTest(t, ini)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("error = %v, want ErrUnknownKey", err)
	}

	mustContain(t, err, `[DOWNLOAD] has unknown key "MaxWorker"`)
}

// ── §2.5-7 Ping-Pong (D11) ─────────────────────────────────────────

func TestPingPong_RejectsSameServerOverlappingPaths(t *testing.T) {
	tests := []struct {
		name       string
		host, port string
		dlRemote   string // DOWNLOAD.RINEX2_DAILY RemotePath
		wantErr    bool
	}{
		{
			name: "같은 서버 + PUT 목적지 == DOWNLOAD 원본 → 거부",
			host: "192.168.0.1", port: "22",
			dlRemote: "/remote/rinex2/daily/(YYYY)/(DOY)/",
			wantErr:  true,
		},
		{
			name: "같은 서버 + DOWNLOAD 원본이 PUT 목적지의 상위 → 거부",
			host: "192.168.0.1", port: "22",
			dlRemote: "/remote/(YYYY)/",
			wantErr:  true,
		},
		{
			name: "같은 서버 + PUT 목적지가 DOWNLOAD 원본의 상위 → 거부",
			host: "192.168.0.1", port: "22",
			dlRemote: "/remote/rinex2/daily/archive/(YYYY)/(DOY)/",
			wantErr:  true,
		},
		{
			name: "같은 서버, 대소문자만 다름 → 보수적으로 거부",
			host: "192.168.0.1", port: "22",
			dlRemote: "/Remote/RINEX2/Daily/(YYYY)/(DOY)/",
			wantErr:  true,
		},
		{
			name: "같은 서버 + 겹치지 않는 형제 경로 → 허용",
			host: "192.168.0.1", port: "22",
			dlRemote: "/remote/rinex2/incoming/(YYYY)/(DOY)/",
			wantErr:  false,
		},
		{
			name: "같은 서버 + 이름 접두어만 같은 형제(rinex2 vs rinex2x) → 허용",
			host: "192.168.0.1", port: "22",
			dlRemote: "/remote/rinex2x/(YYYY)/(DOY)/",
			wantErr:  false,
		},
		{
			name: "다른 포트 → 허용",
			host: "192.168.0.1", port: "2222",
			dlRemote: "/remote/rinex2/daily/(YYYY)/(DOY)/",
			wantErr:  false,
		},
		{
			name: "다른 호스트 → 허용",
			host: "10.0.0.9", port: "22",
			dlRemote: "/remote/rinex2/daily/(YYYY)/(DOY)/",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ini := validINIForLoadTest() + downloadBlockINI()
			ini = replaceLine(t, ini, "DOWNLOAD.SFTP", "Host", "Host = "+tt.host)
			ini = replaceLine(t, ini, "DOWNLOAD.SFTP", "Port", "Port = "+tt.port)
			ini = replaceLine(t, ini, "DOWNLOAD.RINEX2_DAILY", "RemotePath", "RemotePath = "+tt.dlRemote)

			cfg, _, err := mapConfigForTest(t, ini)
			if err != nil {
				t.Fatalf("mapConfig() unexpected error: %v", err)
			}

			// Mode=put 인데도 두 블록이 있으면 검사한다.
			err = cfg.Validate()

			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}

			mustContain(t, err, "[PUT.RINEX2_DAILY] RemotePath")
			mustContain(t, err, "[DOWNLOAD.RINEX2_DAILY] RemotePath")
			mustContain(t, err, "ping-pong")
		})
	}
}

// 비활성 Category 는 순환 판정에 넣지 않는다 — 실행되지 않는 경로다.
func TestPingPong_IgnoresDisabledCategories(t *testing.T) {
	ini := validINIForLoadTest() + downloadBlockINI()
	ini = replaceLine(t, ini, "DOWNLOAD.SFTP", "Host", "Host = 192.168.0.1")
	// RINEX4_DAILY 는 PUT·DOWNLOAD 모두 Enabled=false 다.
	ini = replaceLine(t, ini, "DOWNLOAD.RINEX4_DAILY", "RemotePath",
		"RemotePath = /remote/rinex4/daily/(YYYY)/(DOY)/")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestRemoteScope(t *testing.T) {
	tests := []struct {
		raw     string
		dirs    []string
		partial string
	}{
		{"/RNXOutgoing/(YYYY)/(DOY)/", []string{"rnxoutgoing"}, ""},
		{"/RNX/(YYYY)/(DOY)/", []string{"rnx"}, ""},
		{"/data/rnx_(SITE)/(YYYY)/", []string{"data"}, "rnx_"},
		{"/archive/rinex2/", []string{"archive", "rinex2"}, ""},
		{"/archive/rinex2", []string{"archive", "rinex2"}, ""},
		{"/", nil, ""},
		{"(YYYY)/(DOY)/", nil, ""},
		{"C:\\RNX\\(YYYY)\\", []string{"c:", "rnx"}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got := remoteScopeOf(tt.raw)

			if !reflect.DeepEqual(got.dirs, tt.dirs) || got.partial != tt.partial {
				t.Errorf(
					"remoteScopeOf(%q) = {%v %q}, want {%v %q}",
					tt.raw, got.dirs, got.partial, tt.dirs, tt.partial,
				)
			}
		})
	}
}

func TestRemoteScope_Overlaps(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/a/(YYYY)/", "/a/(YYYY)/", true},
		{"/a/(YYYY)/", "/a/b/(YYYY)/", true},
		{"/a/b/(YYYY)/", "/a/(YYYY)/", true},
		{"/a/b/(YYYY)/", "/a/c/(YYYY)/", false},
		{"/a/b(SITE)/", "/a/bx/(YYYY)/", true},  // 미완성 단 "b" 는 "bx" 의 접두어
		{"/a/b(SITE)/", "/a/cx/(YYYY)/", false}, // 접두어 아님
		{"/a/b(SITE)/", "/a/(YYYY)/", true},     // 상대가 그 깊이 전체
		{"/a/b(SITE)/", "/a/bc(SITE)/", true},   // 둘 다 미완성, 한쪽이 접두어
		{"/a/b(SITE)/", "/a/c(SITE)/", false},   // 둘 다 미완성, 접두어 아님
		{"/(YYYY)/", "/anything/(DOY)/", true},  // 루트 전체
		{"/a/(YYYY)/", "/ab/(YYYY)/", false},    // 완성 단은 정확 일치만
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a, b := remoteScopeOf(tt.a), remoteScopeOf(tt.b)

			if got := a.overlaps(b); got != tt.want {
				t.Errorf("overlaps = %v, want %v", got, tt.want)
			}

			// 대칭이어야 한다.
			if got := b.overlaps(a); got != tt.want {
				t.Errorf("overlaps (reversed) = %v, want %v", got, tt.want)
			}
		})
	}
}

// ── CheckEnvironment 의 Mode 분리 ──────────────────────────────────

func TestDownload_CheckEnvironment(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, downloadOnlyINI())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	base := filepath.Dir(cfg.Path)

	t.Run("LockPath 부모와 DOWNLOAD 키 파일만 요구한다", func(t *testing.T) {
		err := cfg.CheckEnvironment()
		if !errors.Is(err, ErrEnvironment) {
			t.Fatalf("error = %v, want ErrEnvironment", err)
		}

		mustContain(t, err, "[GENERAL] LockPath:")
		mustContain(t, err, "[DOWNLOAD.SFTP] PrivateKey:")
		mustContain(t, err, "[DOWNLOAD.SFTP] KnownHosts:")
		mustNotContain(t, err, "[GENERAL] LedgerPath")
		mustNotContain(t, err, "[PUT.SFTP]")
	})

	t.Run("준비되면 통과", func(t *testing.T) {
		for _, dir := range []string{"data", "keys"} {
			if err := os.MkdirAll(filepath.Join(base, dir), 0o755); err != nil {
				t.Fatal(err)
			}
		}

		for _, f := range []string{"keys/id_ed25519", "keys/known_hosts"} {
			if err := os.WriteFile(filepath.Join(base, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}

		if err := cfg.CheckEnvironment(); err != nil {
			t.Fatalf("CheckEnvironment() unexpected error: %v", err)
		}
	})
}

// knownKeys 의 DOWNLOAD 섹션이 PUT 과 같은 키 집합을 갖는지 고정한다.
// 두 접속 섹션이 서로 어긋나면 템플릿 복사·Mode 전환 운영이 깨진다.
func TestKnownKeys_DownloadMirrorsPut(t *testing.T) {
	known, err := knownKeys()
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(known["PUT.SFTP"], known["DOWNLOAD.SFTP"]) {
		t.Errorf("[PUT.SFTP] %v != [DOWNLOAD.SFTP] %v", known["PUT.SFTP"], known["DOWNLOAD.SFTP"])
	}

	for _, cat := range domain.Categories() {
		p, d := known["PUT."+cat.String()], known["DOWNLOAD."+cat.String()]
		if !reflect.DeepEqual(p, d) {
			t.Errorf("[PUT.%s] %v != [DOWNLOAD.%s] %v", cat, p, cat, d)
		}
	}

	if !reflect.DeepEqual(known["DOWNLOAD"], []string{"Sites", "MaxWorkers", "GraceSeconds"}) {
		t.Errorf("[DOWNLOAD] keys = %v", known["DOWNLOAD"])
	}
}
