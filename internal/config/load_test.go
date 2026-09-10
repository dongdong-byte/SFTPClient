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
Transport = sftp
RepostDownloaded = false
LedgerPath = data/rinex_ledger.db
LockStaleSeconds = 10800

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
MaxRetries = 5
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

	cfg, err := mapConfig(f, path, fakeProtector{})
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

	if cfg.Put.MaxRetries != 5 {
		t.Errorf("MaxRetries = %d, want 5", cfg.Put.MaxRetries)
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

	if cfg.General.LockStale != 3*time.Hour {
		t.Errorf(
			"LockStale = %v, want %v",
			cfg.General.LockStale,
			3*time.Hour,
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
				prot: fakeProtector{},
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
		prot: fakeProtector{},
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

// ─────────────────────────────────────────────────────────────────────────────
// Set Completeness Gate 로더 동작 (유닛 2)
// ─────────────────────────────────────────────────────────────────────────────

// withSetSections 는 validINIForLoadTest 의 [LOG] 앞에 SET 설정을 끼운다.
func withSetSections(t *testing.T, sections string) string {
	t.Helper()

	base := validINIForLoadTest()

	if !strings.Contains(base, "[LOG]") {
		t.Fatal("픽스처에 [LOG] 섹션이 없다")
	}

	return strings.Replace(base, "[LOG]", sections+"\n[LOG]", 1)
}

// TestMapConfig_SetAbsentIsOff 는 [SET.*] 섹션 부재가 오류가 아니라
// 게이트 OFF 임을 고정한다.
//
// 배포된 기관들의 config.ini 에는 이 섹션이 없다. 실행파일 교체만으로
// 배포가 끝나는 성질이 이 동작에 의존한다. (load.go setConfig 주석)
func TestMapConfig_SetAbsentIsOff(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	for _, v := range []int{2, 3, 4} {
		p := cfg.Set.Policy(v)

		if p.Enabled {
			t.Errorf("버전 %d: 섹션 부재인데 Enabled", v)
		}

		if p.RequiredKinds != nil {
			t.Errorf("버전 %d: 섹션 부재인데 kinds=%v", v, p.RequiredKinds)
		}
	}
}

// TestMapConfig_SetPolicies 는 버전별 on / false 명시 / 부재 혼합을 고정한다.
func TestMapConfig_SetPolicies(t *testing.T) {
	input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = G,L,N,O

[SET.RINEX3]
RequiredKinds = false

`)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	p2 := cfg.Set.Policy(2)
	if !p2.Enabled {
		t.Fatal("버전 2: 목록 설정인데 Enabled=false")
	}

	want := []string{"g", "l", "n", "o"}
	if len(p2.RequiredKinds) != len(want) {
		t.Fatalf("버전 2 kinds = %v, want %v", p2.RequiredKinds, want)
	}

	for i, k := range want {
		if p2.RequiredKinds[i] != k {
			t.Errorf("버전 2 kinds[%d] = %q, want %q (소문자 정규화)",
				i, p2.RequiredKinds[i], k)
		}
	}

	// false 명시와 섹션 부재는 같은 OFF 다.
	if cfg.Set.Policy(3).Enabled {
		t.Error("버전 3: false 명시인데 Enabled")
	}

	if cfg.Set.Policy(4).Enabled {
		t.Error("버전 4: 섹션 부재인데 Enabled")
	}
}

// TestMapConfig_SetSectionWithoutKey 는 섹션 머리만 있고 RequiredKinds 가
// 없는 절반짜리 설정이 조용한 OFF 가 아니라 시작 오류임을 고정한다.
//
// 부재=OFF 의 배포 논리는 "섹션이 아예 없는" 경우만 정당화한다.
// (load.go setConfig 주석)
func TestMapConfig_SetSectionWithoutKey(t *testing.T) {
	input := withSetSections(t, `[SET.RINEX2]

`)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("섹션만 있고 RequiredKinds 없음: expected error")
	}

	if !errors.Is(err, ErrMissingKey) {
		t.Errorf("error = %v, want ErrMissingKey", err)
	}
}

// TestMapConfig_SetBadValues 는 허용하지 않는 RequiredKinds 값들이
// 시작 오류로 잡힘을 고정한다. (§4, §11)
func TestMapConfig_SetBadValues(t *testing.T) {
	tests := []struct {
		name string
		val  string
	}{
		{"빈 값", ""},
		{"true", "true"},
		{"빈 항목", "g,,o"},
		{"끝 콤마", "g,l,"},
		{"중복 kind", "g,l,G"},
		{"표현 형식 포함 (§11)", "MO.crx,MN.rnx"},
		{"영문 외 문자", "g,1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := withSetSections(t,
				"[SET.RINEX2]\nRequiredKinds = "+tt.val+"\n\n")

			_, _, err := mapConfigForTest(t, input)
			if err == nil {
				t.Fatalf("RequiredKinds = %q: expected error", tt.val)
			}

			if !errors.Is(err, ErrBadValue) {
				t.Errorf("error = %v, want ErrBadValue", err)
			}
		})
	}
}

// TestParseRequiredKinds_Normalization 은 표기 유연성(공백·대소문자)과
// 단일 종 목록을 고정한다. (위성센터: O 단독 제공 사례)
func TestParseRequiredKinds_Normalization(t *testing.T) {
	kinds, enabled, err := parseRequiredKinds(" MO , MN ")
	if err != nil || !enabled {
		t.Fatalf("공백 섞인 목록: enabled=%v err=%v", enabled, err)
	}

	if len(kinds) != 2 || kinds[0] != "mo" || kinds[1] != "mn" {
		t.Errorf("kinds = %v, want [mo mn]", kinds)
	}

	kinds, enabled, err = parseRequiredKinds("O")
	if err != nil || !enabled || len(kinds) != 1 || kinds[0] != "o" {
		t.Errorf("단일 종: kinds=%v enabled=%v err=%v", kinds, enabled, err)
	}

	if _, enabled, err = parseRequiredKinds(" False "); err != nil || enabled {
		t.Errorf("대소문자 무관 false: enabled=%v err=%v", enabled, err)
	}
}
