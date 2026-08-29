package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
)

func validINIForLoadTest() string {
	return `[GENERAL]
Mode = put
RepostDownloaded = false
LedgerPath = data/rinex_ledger.db

[SCAN]
ScanRecentDays = 2
ScanDays = 7
DeepScanHour = 4
UseDirMtimeSkip = false

[INGRESS]
GraceSeconds = 60

[LEDGER]
RetentionDays = 60

[PUT]
MaxWorkers = 4
MaxRetries = 3
MaxFilesPerRun = 2000

[PUT.SFTP]
AuthMethod = PuBlIcKeY
Host = 192.168.0.1
Port = 22
User = rinexclient
PrivateKey = keys/id_ed25519
KnownHosts = keys/known_hosts

[PUT.RINEX2_DAILY]
Enabled = true
LocalPath = /local/rinex2/daily/(YYYY)/(DOY)/
RemotePath = /remote/rinex2/daily/(YYYY)/(DOY)/

[PUT.RINEX2_HOURLY]
Enabled = true
LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/(HH)/
RemotePath = /remote/rinex2/hourly/(YYYY)/(DOY)/(HH)/

[PUT.RINEX3_DAILY]
Enabled = true
LocalPath = /local/rinex3/daily/(YYYY)/(DOY)/
RemotePath = /remote/rinex3/daily/(YYYY)/(DOY)/

[PUT.RINEX3_HOURLY]
Enabled = true
LocalPath = /local/rinex3/hourly/(YYYY)/(DOY)/(HH)/
RemotePath = /remote/rinex3/hourly/(YYYY)/(DOY)/(HH)/

[PUT.RINEX4_DAILY]
Enabled = false
LocalPath = /local/rinex4/daily/(YYYY)/(DOY)/
RemotePath = /remote/rinex4/daily/(YYYY)/(DOY)/

[PUT.RINEX4_HOURLY]
Enabled = false
LocalPath = /local/rinex4/hourly/(YYYY)/(DOY)/(HH)/
RemotePath = /remote/rinex4/hourly/(YYYY)/(DOY)/(HH)/

[LOG]
Level = INFO
Dir = logs
RetentionDays = 30
`
}

func mapConfigForTest(
	t *testing.T,
	input string,
) (*Config, string, error) {
	t.Helper()

	f, err := parseINI(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseINI() unexpected error: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.ini")

	cfg, err := mapConfig(f, path)
	return cfg, path, err
}

func TestMapConfig_Normal(t *testing.T) {
	cfg, path, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if cfg.General.Mode != domain.ModePut {
		t.Errorf("Mode = %q, want %q", cfg.General.Mode, domain.ModePut)
	}

	// 외부 키 MaxRetries → 내부 필드 MaxAttempts.
	if cfg.Put.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", cfg.Put.MaxAttempts)
	}

	if cfg.Put.SFTP.AuthMethod != "publickey" {
		t.Errorf(
			"AuthMethod = %q, want %q",
			cfg.Put.SFTP.AuthMethod,
			"publickey",
		)
	}

	if cfg.Log.Level != "info" {
		t.Errorf("Log.Level = %q, want %q", cfg.Log.Level, "info")
	}

	if cfg.Ingress.Grace != 60*time.Second {
		t.Errorf(
			"Ingress.Grace = %v, want %v",
			cfg.Ingress.Grace,
			60*time.Second,
		)
	}

	configDir := filepath.Dir(path)

	wantLedger := filepath.Clean(
		filepath.Join(configDir, "data/rinex_ledger.db"),
	)
	if cfg.General.LedgerPath != wantLedger {
		t.Errorf(
			"LedgerPath = %q, want %q",
			cfg.General.LedgerPath,
			wantLedger,
		)
	}

	wantKey := filepath.Clean(
		filepath.Join(configDir, "keys/id_ed25519"),
	)
	if cfg.Put.SFTP.PrivateKey != wantKey {
		t.Errorf(
			"PrivateKey = %q, want %q",
			cfg.Put.SFTP.PrivateKey,
			wantKey,
		)
	}

	wantKnownHosts := filepath.Clean(
		filepath.Join(configDir, "keys/known_hosts"),
	)
	if cfg.Put.SFTP.KnownHosts != wantKnownHosts {
		t.Errorf(
			"KnownHosts = %q, want %q",
			cfg.Put.SFTP.KnownHosts,
			wantKnownHosts,
		)
	}

	wantLogDir := filepath.Clean(
		filepath.Join(configDir, "logs"),
	)
	if cfg.Log.Dir != wantLogDir {
		t.Errorf(
			"Log.Dir = %q, want %q",
			cfg.Log.Dir,
			wantLogDir,
		)
	}
}

func TestResolvePath(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.ini")
	absolute := filepath.Join(t.TempDir(), "absolute", "file.txt")

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "상대경로",
			value: filepath.Join("data", "rinex.db"),
			want: filepath.Join(
				filepath.Dir(configPath),
				"data",
				"rinex.db",
			),
		},
		{
			name:  "절대경로",
			value: absolute,
			want:  absolute,
		},
		{
			name:  "빈 값",
			value: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolvePath(configPath, tt.value)

			if got != filepath.Clean(tt.want) && tt.want != "" {
				t.Errorf("resolvePath() = %q, want %q", got, filepath.Clean(tt.want))
			}

			if tt.want == "" && got != "" {
				t.Errorf("resolvePath() = %q, want empty", got)
			}
		})
	}
}

func TestMapConfig_GraceOverflow(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"GraceSeconds = 60",
		"GraceSeconds = 9223372037",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("GraceSeconds overflow: expected error")
	}

	if !errors.Is(err, ErrBadValue) {
		t.Errorf("error = %v, want ErrBadValue", err)
	}
}

func TestLoaderBoolVal(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"true", "true", true},
		{"false", "false", false},
		{"yes", "yes", true},
		{"no", "no", false},
		{"on", "on", true},
		{"off", "off", false},
		{"1", "1", true},
		{"0", "0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &iniSection{
				name: "TEST",
				line: 1,
				pairs: map[string]iniPair{
					foldKey("Flag"): {
						value: tt.in,
						line:  2,
					},
				},
			}

			l := &loader{
				path: "test.ini",
			}

			got := l.boolVal(s, "Flag")

			if err := l.err(); err != nil {
				t.Fatalf("boolVal(%q) unexpected error: %v", tt.in, err)
			}

			if got != tt.want {
				t.Errorf("boolVal(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestLoaderBoolVal_Invalid(t *testing.T) {
	s := &iniSection{
		name: "TEST",
		line: 1,
		pairs: map[string]iniPair{
			foldKey("Flag"): {
				value: "maybe",
				line:  2,
			},
		},
	}

	l := &loader{
		path: "test.ini",
	}

	_ = l.boolVal(s, "Flag")

	err := l.err()
	if err == nil {
		t.Fatal("invalid boolean: expected error")
	}

	if !errors.Is(err, ErrBadValue) {
		t.Errorf("error = %v, want ErrBadValue", err)
	}
}

func TestMapConfig_MissingSection(t *testing.T) {
	const block = `[SCAN]
ScanRecentDays = 2
ScanDays = 7
DeepScanHour = 4
UseDirMtimeSkip = false

`

	input := strings.Replace(
		validINIForLoadTest(),
		block,
		"",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("missing [SCAN]: expected error")
	}

	if !errors.Is(err, ErrMissingKey) {
		t.Errorf("error = %v, want ErrMissingKey", err)
	}

	if !strings.Contains(err.Error(), "section [SCAN] not found") {
		t.Errorf("error does not identify missing [SCAN]: %v", err)
	}
}

func TestMapConfig_UnknownKey(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"Mode = put",
		"Mode = put\nScanDay = 7",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("unknown key: expected error")
	}

	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("error = %v, want ErrUnknownKey", err)
	}

	if !strings.Contains(err.Error(), "SCANDAY") &&
		!strings.Contains(err.Error(), "ScanDay") {
		t.Errorf("error does not identify unknown key: %v", err)
	}
}

func TestMapConfig_UnknownSection(t *testing.T) {
	input := validINIForLoadTest() + `
[PUT.WRONG]
Enabled = true
`

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("unknown section: expected error")
	}

	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("error = %v, want ErrUnknownKey", err)
	}

	if !strings.Contains(err.Error(), "PUT.WRONG") {
		t.Errorf("error does not identify unknown section: %v", err)
	}
}

func TestMapConfig_CategoryOrder(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	want := domain.Categories()

	if len(cfg.Put.Categories) != len(want) {
		t.Fatalf(
			"Categories len = %d, want %d",
			len(cfg.Put.Categories),
			len(want),
		)
	}

	for i, cat := range want {
		if cfg.Put.Categories[i].Category != cat {
			t.Errorf(
				"Categories[%d] = %q, want %q",
				i,
				cfg.Put.Categories[i].Category,
				cat,
			)
		}
	}
}

func TestMapConfig_DisabledCategoryStillParsesTemplate(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		`Enabled = true
LocalPath = /local/rinex2/daily/(YYYY)/(DOY)/`,
		`Enabled = false
LocalPath = /local/rinex2/daily/(UNKNOWN)/(DOY)/`,
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("invalid template in disabled category: expected error")
	}

	if !errors.Is(err, ErrBadValue) {
		t.Errorf("error = %v, want ErrBadValue", err)
	}
}

func TestMapConfig_AccumulatesErrors(t *testing.T) {
	input := validINIForLoadTest()

	input = strings.Replace(
		input,
		"ScanRecentDays = 2",
		"ScanRecentDays = abc",
		1,
	)

	input = strings.Replace(
		input,
		"Port = 22",
		"Port = xyz",
		1,
	)

	input = strings.Replace(
		input,
		"MaxWorkers = 4",
		"MaxWorkers = nope",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("multiple bad values: expected error")
	}

	for _, key := range []string{
		"ScanRecentDays",
		"Port",
		"MaxWorkers",
	} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not contain %q: %v", key, err)
		}
	}
}
