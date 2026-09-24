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
LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/
RemotePath = /remote/rinex2/hourly/(YYYY)/(DOY)/

[PUT.RINEX3_DAILY]
Enabled = true
LocalPath = /local/rinex3/daily/(YYYY)/(DOY)/
RemotePath = /remote/rinex3/daily/(YYYY)/(DOY)/

[PUT.RINEX3_HOURLY]
Enabled = true
LocalPath = /local/rinex3/hourly/(YYYY)/(DOY)/
RemotePath = /remote/rinex3/hourly/(YYYY)/(DOY)/

[PUT.RINEX4_DAILY]
Enabled = false
LocalPath = /local/rinex4/daily/(YYYY)/(DOY)/
RemotePath = /remote/rinex4/daily/(YYYY)/(DOY)/

[PUT.RINEX4_HOURLY]
Enabled = false
LocalPath = /local/rinex4/hourly/(YYYY)/(DOY)/
RemotePath = /remote/rinex4/hourly/(YYYY)/(DOY)/

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

// TestMapConfig_RemovedHourLayoutKeyIsRejected 는 제거된 HourLayout 키가
// 남은 옛 config 가 ErrUnknownKey 로 거부되는지 본다 (PATH_DESIGN v3 §1-4,
// §7.1 T6). knownKeys 에서 키를 뺀 것만으로 얻는 동작이며, 별도 거부
// 코드를 두지 않는다(§2-E). 배포 시 바이너리와 config 를 함께 교체해야
// 하는 이유가 이 동작이다(§7).
func TestMapConfig_RemovedHourLayoutKeyIsRejected(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"[PUT.RINEX2_HOURLY]\nEnabled = true",
		"[PUT.RINEX2_HOURLY]\nHourLayout = dir\nEnabled = true",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("removed HourLayout key: expected error")
	}

	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("error = %v, want ErrUnknownKey", err)
	}

	if !strings.Contains(strings.ToUpper(err.Error()), "HOURLAYOUT") {
		t.Errorf("error does not identify HourLayout: %v", err)
	}
}

// TestMapConfig_RemovedHourTokenIsRejected 는 제거된 (HH) 토큰이 남은
// 옛 config 가 로드 단계에서 거부되는지 본다 (PATH_DESIGN v3 §1-3,
// §7.1 T6). 조용히 자정 폴더만 스캔하는 오동작 대신 시작에서 선다.
//
// 거부의 주인은 경로 토큰 커밋 2 부터 config 의 putPathForbiddenTokens
// 정책이다. 그 전에는 pathpl 이 (HH) 를 모르는 것에 의존했는데, 커밋 3
// 에서 pathpl 이 파일명 출처의 (HH) 를 알게 되면 그 의존은 사라진다.
// 이 테스트는 어느 커밋에서도 통과해야 한다 — 그것이 "보호막을 먼저
// 깐다"는 커밋 순서의 검증이다.
func TestMapConfig_RemovedHourTokenIsRejected(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/",
		"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/(HH)/",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("removed (HH) token: expected error")
	}

	if !strings.Contains(err.Error(), "HH") {
		t.Errorf("error does not mention HH: %v", err)
	}

	if !errors.Is(err, ErrBadValue) {
		t.Errorf("expected ErrBadValue, got: %v", err)
	}
}

// TestMapConfig_PutPathFileTokensRejected 는 PUT 경로에 파일명 출처
// 토큰((SITE)·(HH))이 있으면 LocalPath·RemotePath 어느 쪽이든 시작에서
// 거부되는지 본다 (DOWNLOAD v3 §7.1, 경로 토큰 커밋 2).
//
// 오류 문구는 토큰 이름과 "PUT paths" 를 함께 담아야 한다. 커밋 3 뒤에는
// 같은 토큰이 DOWNLOAD 목적지에서는 정상이므로, 운영자가 "토큰이 틀렸다"
// 가 아니라 "이 방향에서는 안 된다" 로 읽을 수 있어야 한다.
func TestMapConfig_PutPathFileTokensRejected(t *testing.T) {
	cases := []struct {
		name  string
		from  string
		to    string
		token string
	}{
		{
			"LocalPath (SITE)",
			"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/",
			"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/(SITE)/",
			"SITE",
		},
		{
			"RemotePath (SITE)",
			"RemotePath = /remote/rinex2/hourly/(YYYY)/(DOY)/",
			"RemotePath = /remote/rinex2/hourly/(YYYY)/(DOY)/(SITE)/",
			"SITE",
		},
		{
			"RemotePath (HH)",
			"RemotePath = /remote/rinex2/hourly/(YYYY)/(DOY)/",
			"RemotePath = /remote/rinex2/hourly/(YYYY)/(DOY)/(HH)/",
			"HH",
		},
		{
			"Daily 카테고리도 같은 정책",
			"LocalPath = /local/rinex2/daily/(YYYY)/(DOY)/",
			"LocalPath = /local/rinex2/daily/(YYYY)/(DOY)/(SITE)/",
			"SITE",
		},
		{
			"폴더 이름 중간에 붙여 쓴 토큰",
			"RemotePath = /remote/rinex3/daily/(YYYY)/(DOY)/",
			"RemotePath = /remote/rinex3/daily/(YYYY)/(DOY)/st(SITE)/",
			"SITE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := strings.Replace(validINIForLoadTest(), tc.from, tc.to, 1)
			if input == validINIForLoadTest() {
				t.Fatalf("test fixture: %q not found in validINIForLoadTest()", tc.from)
			}

			_, _, err := mapConfigForTest(t, input)
			if err == nil {
				t.Fatalf("file token (%s) in PUT path: expected error", tc.token)
			}

			if !errors.Is(err, ErrBadValue) {
				t.Errorf("expected ErrBadValue, got: %v", err)
			}

			msg := err.Error()
			if !strings.Contains(msg, "("+tc.token+")") {
				t.Errorf("error does not name token (%s): %v", tc.token, err)
			}

			if !strings.Contains(msg, "PUT paths") {
				t.Errorf("error does not say the rejection is a PUT-path policy: %v", err)
			}

			// PUT 만 쓰는 현장 운영자가 "DOWNLOAD 를 켜야 하나" 로
			// 오독하지 않도록 방향 설명 대신 고칠 방법을 적는다.
			if strings.Contains(msg, "DOWNLOAD") {
				t.Errorf("PUT-path error must not mention DOWNLOAD: %v", err)
			}

			if !strings.Contains(msg, forbiddenTokenHints[tc.token]) {
				t.Errorf("error does not carry the fix hint for (%s): %v", tc.token, err)
			}
		})
	}
}

// TestMapConfig_RemovedHourTokenHint 는 옛 (HH) 가 남은 PUT 현장(서울시형)의
// 오류가 재배포 절차와 같은 조치 — (HH) 제거, 시간 폴더는 재귀 수집 — 를
// 알려 주는지 본다 (PATH v3 §7).
func TestMapConfig_RemovedHourTokenHint(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/",
		"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/(HH)/",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("expected error")
	}

	for _, want := range []string{"remove it", "recursively"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not tell the operator to %q: %v", want, err)
		}
	}
}

// TestForbiddenTokenHints_Complete 는 금지 목록의 모든 토큰에 조치 문구가
// 있는지 고정한다. 목록에 토큰을 추가하고 문구를 빠뜨리면 오류 끝이
// 빈 문자열이 되어 운영자가 무엇을 할지 알 수 없다.
func TestForbiddenTokenHints_Complete(t *testing.T) {
	for _, list := range [][]string{
		putPathForbiddenTokens,
		downloadSourceForbiddenTokens,
	} {
		for _, name := range list {
			if forbiddenTokenHints[name] == "" {
				t.Errorf("token (%s) has no fix hint", name)
			}
		}
	}
}

// TestPathRolePolicies_FileTokens 는 역할별 금지 목록이 파일명 출처 토큰
// 두 개를 모두 담고 있는지 고정한다. DOWNLOAD 원본 목록은 커밋 2 시점에
// 호출자가 없으므로, 이 테스트가 그 정책의 유일한 사용처다 — 로더가
// 생기기 전에 누가 목록을 비우면 여기서 먼저 깨진다.
func TestPathRolePolicies_FileTokens(t *testing.T) {
	for name, list := range map[string][]string{
		"putPathForbiddenTokens":        putPathForbiddenTokens,
		"downloadSourceForbiddenTokens": downloadSourceForbiddenTokens,
	} {
		for _, want := range []string{"SITE", "HH"} {
			found := false
			for _, got := range list {
				if got == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s must forbid (%s)", name, want)
			}
		}
	}
}

// TestMapConfig_PutPathFileTokenSkipsParseError 는 금지 토큰이 있으면
// 같은 줄에 대해 문법 오류를 겹쳐 쌓지 않는지 본다. 커밋 2 시점에는
// pathpl 도 (SITE) 를 모르므로 정책 검사를 건너뛰면 "unknown token" 이
// 함께 나온다 — 운영자가 두 가지를 고쳐야 하는 줄 알게 된다.
func TestMapConfig_PutPathFileTokenSkipsParseError(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/",
		"LocalPath = /local/rinex2/hourly/(YYYY)/(DOY)/(SITE)/",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("expected error")
	}

	if strings.Contains(err.Error(), "unknown token") {
		t.Errorf("policy rejection must not be doubled by a parse error: %v", err)
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

		if p.ResendMinKinds != nil {
			t.Errorf("버전 %d: 섹션 부재인데 ResendMinKinds=%v", v, p.ResendMinKinds)
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

	if p2.ResendMinKinds != nil {
		t.Errorf("버전 2: 키 부재인데 ResendMinKinds=%v", p2.ResendMinKinds)
	}

	// false 명시와 섹션 부재는 같은 OFF 다.
	p3 := cfg.Set.Policy(3)
	if p3.Enabled {
		t.Error("버전 3: false 명시인데 Enabled")
	}
	if p3.ResendMinKinds != nil {
		t.Errorf("버전 3: 게이트 OFF 인데 ResendMinKinds=%v", p3.ResendMinKinds)
	}

	p4 := cfg.Set.Policy(4)
	if p4.Enabled {
		t.Error("버전 4: 섹션 부재인데 Enabled")
	}
	if p4.ResendMinKinds != nil {
		t.Errorf("버전 4: 섹션 부재인데 ResendMinKinds=%v", p4.ResendMinKinds)
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
