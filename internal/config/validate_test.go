package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

func mustTemplate(t *testing.T, value string) *pathpl.Template {
	t.Helper()

	tpl, err := pathpl.Parse(value)
	if err != nil {
		t.Fatalf("pathpl.Parse(%q): %v", value, err)
	}

	return tpl
}

func TestValidate_MaxWorkers(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "0 은 거부",
			mutate:  func(c *Config) { c.Put.MaxWorkers = 0 },
			wantErr: "[PUT] MaxWorkers",
		},
		{
			name:    "상한 초과(17)는 거부 — 40 같은 오타 방어",
			mutate:  func(c *Config) { c.Put.MaxWorkers = 17 },
			wantErr: "[PUT] MaxWorkers",
		},
		{
			name:    "경계값 1 은 허용",
			mutate:  func(c *Config) { c.Put.MaxWorkers = 1 },
			wantErr: "",
		},
		{
			name:    "경계값 16 은 허용",
			mutate:  func(c *Config) { c.Put.MaxWorkers = 16 },
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			tt.mutate(cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected validation error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf(
					"error = %v, want substring %q",
					err,
					tt.wantErr,
				)
			}
		})
	}
}

func validConfigForValidate(t *testing.T) *Config {
	t.Helper()

	return &Config{
		Path: "config.ini",

		General: GeneralConfig{
			Mode:             domain.ModePut,
			Transport:        "sftp",
			RepostDownloaded: false,
			LedgerPath:       "data/rinex_ledger.db",
			LockStale:        3 * time.Hour,
		},

		Scan: ScanConfig{
			RecentDays:      2,
			Days:            7,
			DeepScanHour:    4,
			UseDirMtimeSkip: false,
		},

		Ingress: IngressConfig{
			Grace: 60 * time.Second,
		},

		Ledger: LedgerConfig{
			RetentionDays: 60,
		},

		Put: PutConfig{
			MaxWorkers:     4,
			MaxRetries:     5,
			MaxFilesPerRun: 2000,

			SFTP: SFTPConfig{
				AuthMethod: "publickey",
				Host:       "192.168.0.1",
				Port:       22,
				User:       "rinexclient",
				PrivateKey: "keys/id_ed25519",
				KnownHosts: "keys/known_hosts",
			},

			Categories: []CategoryConfig{
				{
					Category:   domain.CategoryRINEX2Daily,
					Enabled:    true,
					LocalPath:  mustTemplate(t, "/local/r2d/(YYYY)/(DOY)/"),
					RemotePath: mustTemplate(t, "/remote/r2d/(YYYY)/(DOY)/"),
				},
				{
					Category:   domain.CategoryRINEX2Hourly,
					Enabled:    true,
					LocalPath:  mustTemplate(t, "/local/r2h/(YYYY)/(DOY)/(HH)/"),
					RemotePath: mustTemplate(t, "/remote/r2h/(YYYY)/(DOY)/(HH)/"),
				},
				{
					Category:   domain.CategoryRINEX3Daily,
					Enabled:    true,
					LocalPath:  mustTemplate(t, "/local/r3d/(YYYY)/(DOY)/"),
					RemotePath: mustTemplate(t, "/remote/r3d/(YYYY)/(DOY)/"),
				},
				{
					Category:   domain.CategoryRINEX3Hourly,
					Enabled:    true,
					LocalPath:  mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/(HH)/"),
					RemotePath: mustTemplate(t, "/remote/r3h/(YYYY)/(DOY)/(HH)/"),
				},
				{
					Category:   domain.CategoryRINEX4Daily,
					Enabled:    false,
					LocalPath:  mustTemplate(t, "/local/r4d/(YYYY)/(DOY)/"),
					RemotePath: mustTemplate(t, "/remote/r4d/(YYYY)/(DOY)/"),
				},
				{
					Category:   domain.CategoryRINEX4Hourly,
					Enabled:    false,
					LocalPath:  mustTemplate(t, "/local/r4h/(YYYY)/(DOY)/(HH)/"),
					RemotePath: mustTemplate(t, "/remote/r4h/(YYYY)/(DOY)/(HH)/"),
				},
			},
		},

		Log: LogConfig{
			Level:         "info",
			Dir:           "logs",
			RetentionDays: 30,
		},
	}
}

func TestValidate_ValidConfig(t *testing.T) {
	cfg := validConfigForValidate(t)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() unexpected error: %v", err)
	}
}

func TestValidate_Mode(t *testing.T) {
	tests := []struct {
		name    string
		mode    domain.Mode
		wantErr bool
	}{
		{
			name:    "PUT",
			mode:    domain.ModePut,
			wantErr: false,
		},
		{
			name:    "DOWNLOAD 미구현",
			mode:    domain.ModeDownload,
			wantErr: true,
		},
		{
			name:    "BOTH 미구현",
			mode:    domain.ModeBoth,
			wantErr: true,
		},
		{
			name:    "빈 Mode",
			mode:    domain.Mode(""),
			wantErr: true,
		},
		{
			// Valid()이 ParseMode를 다시 호출하면 이 값이
			// 잘못 valid=true가 되는 회귀를 잡는다.
			name:    "정규화되지 않은 직접 생성 Mode",
			mode:    domain.Mode(" put "),
			wantErr: true,
		},
		{
			name:    "알 수 없는 Mode",
			mode:    domain.Mode("SYNC"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			cfg.General.Mode = tt.mode

			err := cfg.Validate()

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected validation error")
				}

				if !errors.Is(err, ErrInvalidConfig) {
					t.Errorf("error = %v, want ErrInvalidConfig", err)
				}

				return
			}

			if err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidate_RepostDownloadedWithoutDownload(t *testing.T) {
	cfg := validConfigForValidate(t)

	cfg.General.Mode = domain.ModePut
	cfg.General.RepostDownloaded = true

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error")
	}

	if !strings.Contains(err.Error(), "RepostDownloaded") {
		t.Errorf("error does not mention RepostDownloaded: %v", err)
	}
}

func TestValidate_ScanDaysIncludesRecentDays(t *testing.T) {
	cfg := validConfigForValidate(t)

	cfg.Scan.RecentDays = 7
	cfg.Scan.Days = 2

	err := cfg.Validate()
	if err == nil {
		t.Fatal("ScanDays < ScanRecentDays: expected error")
	}

	if !strings.Contains(err.Error(), "ScanDays") {
		t.Errorf("error does not mention ScanDays: %v", err)
	}
}

func TestValidate_DeepScanHour(t *testing.T) {
	tests := []struct {
		name    string
		value   int
		wantErr bool
	}{
		{"-1 거부", -1, true},
		{"0 허용", 0, false},
		{"23 허용", 23, false},
		{"24 거부", 24, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			cfg.Scan.DeepScanHour = tt.value

			err := cfg.Validate()

			if tt.wantErr {
				if err == nil {
					t.Fatalf(
						"DeepScanHour=%d: expected error",
						tt.value,
					)
				}
				return
			}

			if err != nil {
				t.Fatalf(
					"DeepScanHour=%d: unexpected error: %v",
					tt.value,
					err,
				)
			}
		})
	}
}

func TestValidate_RetentionMustExceedScanDays(t *testing.T) {
	tests := []struct {
		name      string
		retention int
		scanDays  int
		wantErr   bool
	}{
		{
			name:      "더 큼",
			retention: 60,
			scanDays:  7,
			wantErr:   false,
		},
		{
			name:      "같음",
			retention: 7,
			scanDays:  7,
			wantErr:   true,
		},
		{
			name:      "더 작음",
			retention: 6,
			scanDays:  7,
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)

			cfg.Ledger.RetentionDays = tt.retention
			cfg.Scan.Days = tt.scanDays

			err := cfg.Validate()

			if tt.wantErr && err == nil {
				t.Fatal("expected validation error")
			}

			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestValidate_HourToken(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, cfg *Config)
	}{
		{
			name: "Hourly LocalPath에 HH 없음",
			mutate: func(t *testing.T, cfg *Config) {
				cfg.Put.Categories[1].LocalPath =
					mustTemplate(t, "/local/r2h/(YYYY)/(DOY)/")
			},
		},
		{
			name: "Daily RemotePath에 HH 있음",
			mutate: func(t *testing.T, cfg *Config) {
				cfg.Put.Categories[2].RemotePath =
					mustTemplate(t, "/remote/r3d/(YYYY)/(DOY)/(HH)/")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			tt.mutate(t, cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatal("expected validation error")
			}

			if !strings.Contains(err.Error(), "HH") {
				t.Errorf("error does not mention HH: %v", err)
			}
		})
	}
}

func TestValidate_DisabledCategoryStillChecksHourToken(t *testing.T) {
	cfg := validConfigForValidate(t)

	// RINEX3_HOURLY
	cfg.Put.Categories[3].Enabled = false
	cfg.Put.Categories[3].LocalPath =
		mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/")

	err := cfg.Validate()
	if err == nil {
		t.Fatal("disabled hourly category without HH: expected error")
	}

	if !strings.Contains(err.Error(), "HH") {
		t.Errorf("error does not mention HH: %v", err)
	}
}

func TestValidate_AuthMethodAndKnownHosts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(cfg *Config)
	}{
		{
			name: "지원하지 않는 AuthMethod",
			mutate: func(cfg *Config) {
				cfg.Put.SFTP.AuthMethod = "password"
			},
		},
		{
			name: "KnownHosts 빈 값",
			mutate: func(cfg *Config) {
				cfg.Put.SFTP.KnownHosts = ""
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			tt.mutate(cfg)

			if err := cfg.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidate_NoEnabledCategory(t *testing.T) {
	cfg := validConfigForValidate(t)

	for i := range cfg.Put.Categories {
		cfg.Put.Categories[i].Enabled = false
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("no enabled category: expected error")
	}

	if !strings.Contains(err.Error(), "no category is enabled") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_DuplicateLocalPath(t *testing.T) {
	cfg := validConfigForValidate(t)

	cfg.Put.Categories[2].LocalPath =
		cfg.Put.Categories[0].LocalPath

	err := cfg.Validate()
	if err == nil {
		t.Fatal("duplicate LocalPath: expected error")
	}

	if !strings.Contains(err.Error(), "LocalPath is identical") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_LedgerPathEmpty(t *testing.T) {
	cfg := validConfigForValidate(t)
	cfg.General.LedgerPath = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("empty LedgerPath: expected error")
	}

	if !strings.Contains(err.Error(), "LedgerPath is empty") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidate_LockStale(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr string
	}{
		{
			name: "LockStaleSeconds 가 600 미만이면 거부 (GraceSeconds 감각 오입력)",
			mutate: func(c *Config) {
				c.General.LockStale = 60 * time.Second
			},
			wantErr: "[GENERAL] LockStaleSeconds",
		},
		{
			name: "LockStaleSeconds 가 24시간 초과면 거부",
			mutate: func(c *Config) {
				c.General.LockStale = 25 * time.Hour
			},
			wantErr: "[GENERAL] LockStaleSeconds",
		},
		{
			name: "경계값 600초는 허용",
			mutate: func(c *Config) {
				c.General.LockStale = 600 * time.Second
			},
			wantErr: "",
		},
		{
			name: "경계값 86400초는 허용",
			mutate: func(c *Config) {
				c.General.LockStale = 24 * time.Hour
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			tt.mutate(cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected validation error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf(
					"error = %v, want substring %q",
					err,
					tt.wantErr,
				)
			}
		})
	}
}

func TestValidate_AccumulatesErrors(t *testing.T) {
	cfg := validConfigForValidate(t)

	cfg.General.Mode = domain.Mode("INVALID")
	cfg.Scan.RecentDays = 0
	cfg.Ledger.RetentionDays = 0
	cfg.Put.MaxWorkers = 0
	cfg.Put.SFTP.Host = ""
	cfg.Log.Level = "verbose"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected multiple validation errors")
	}

	want := []string{
		"Mode",
		"ScanRecentDays",
		"RetentionDays",
		"MaxWorkers",
		"Host",
		"Level",
	}

	for _, text := range want {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("error does not contain %q:\n%v", text, err)
		}
	}
}

func TestCheckEnvironment(t *testing.T) {
	t.Run("정상", func(t *testing.T) {
		cfg := validConfigForValidate(t)

		base := t.TempDir()
		dataDir := filepath.Join(base, "data")

		if err := os.Mkdir(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}

		key := filepath.Join(base, "id_ed25519")
		knownHosts := filepath.Join(base, "known_hosts")

		if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(knownHosts, []byte("host"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg.General.LedgerPath =
			filepath.Join(dataDir, "rinex_ledger.db")
		cfg.Put.SFTP.PrivateKey = key
		cfg.Put.SFTP.KnownHosts = knownHosts

		if err := cfg.CheckEnvironment(); err != nil {
			t.Fatalf("CheckEnvironment() unexpected error: %v", err)
		}
	})

	t.Run("Ledger 부모 디렉터리 없음", func(t *testing.T) {
		cfg := validConfigForValidate(t)

		base := t.TempDir()

		key := filepath.Join(base, "id_ed25519")
		knownHosts := filepath.Join(base, "known_hosts")

		if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(knownHosts, []byte("host"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg.General.LedgerPath =
			filepath.Join(base, "missing", "rinex_ledger.db")
		cfg.Put.SFTP.PrivateKey = key
		cfg.Put.SFTP.KnownHosts = knownHosts

		err := cfg.CheckEnvironment()
		if err == nil {
			t.Fatal("expected environment error")
		}

		if !errors.Is(err, ErrEnvironment) {
			t.Errorf("error = %v, want ErrEnvironment", err)
		}
	})

	t.Run("PrivateKey 없음", func(t *testing.T) {
		cfg := validConfigForValidate(t)

		base := t.TempDir()
		dataDir := filepath.Join(base, "data")

		if err := os.Mkdir(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}

		knownHosts := filepath.Join(base, "known_hosts")
		if err := os.WriteFile(knownHosts, []byte("host"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg.General.LedgerPath =
			filepath.Join(dataDir, "rinex_ledger.db")
		cfg.Put.SFTP.PrivateKey =
			filepath.Join(base, "missing_key")
		cfg.Put.SFTP.KnownHosts = knownHosts

		err := cfg.CheckEnvironment()
		if err == nil {
			t.Fatal("expected environment error")
		}

		if !errors.Is(err, ErrEnvironment) {
			t.Errorf("error = %v, want ErrEnvironment", err)
		}
	})

	t.Run("KnownHosts 없음", func(t *testing.T) {
		cfg := validConfigForValidate(t)

		base := t.TempDir()
		dataDir := filepath.Join(base, "data")

		if err := os.Mkdir(dataDir, 0o755); err != nil {
			t.Fatal(err)
		}

		key := filepath.Join(base, "id_ed25519")
		if err := os.WriteFile(key, []byte("key"), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg.General.LedgerPath =
			filepath.Join(dataDir, "rinex_ledger.db")
		cfg.Put.SFTP.PrivateKey = key
		cfg.Put.SFTP.KnownHosts =
			filepath.Join(base, "missing_known_hosts")

		err := cfg.CheckEnvironment()
		if err == nil {
			t.Fatal("expected environment error")
		}

		if !errors.Is(err, ErrEnvironment) {
			t.Errorf("error = %v, want ErrEnvironment", err)
		}
	})
}

func TestValidate_Transport(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "빈 값은 거부 — 기본 전송 계층은 없다",
			mutate:  func(c *Config) { c.General.Transport = "" },
			wantErr: "[GENERAL] Transport",
		},
		{
			name:    "오타는 거부",
			mutate:  func(c *Config) { c.General.Transport = "sfpt" },
			wantErr: "[GENERAL] Transport",
		},
		{
			name:    "sftp 허용",
			mutate:  func(c *Config) { c.General.Transport = "sftp" },
			wantErr: "",
		},
		{
			name:    "localfs 허용",
			mutate:  func(c *Config) { c.General.Transport = "localfs" },
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			tt.mutate(cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected validation error: %v", err)
				}
				return
			}

			if err == nil {
				t.Fatal("expected validation error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
