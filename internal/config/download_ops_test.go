package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/lock"
)

// DOWNLOAD 커밋 1 리뷰에서 찾은 운영 치명 지점을 고정한다.
//
//	LockPath 오설정   → 매 회차 lock 실패, 또는 ErrHeld 로 조용히 exit 0
//	Ping-Pong "."      → 같은 폴더인데 표기만 달라 순환을 놓침
//	평문 경고 범위     → 쓰지 않는 방향의 WARN 이 매 회차 쌓여 진짜 경고를 가림

// ── LockPath 가 장부·로그와 겹침 ───────────────────────────────────

func TestValidate_LockPathMustNotCollide(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "LockPath == LedgerPath (put)",
			mutate: func(c *Config) {
				c.General.LockPath = "data/rinex_ledger.db"
			},
			wantErr: "must not be LedgerPath or its parent",
		},
		{
			name: "대소문자·구분자만 다른 LedgerPath",
			mutate: func(c *Config) {
				c.General.LockPath = "DATA/./Rinex_Ledger.db"
			},
			wantErr: "must not be LedgerPath or its parent",
		},
		{
			name: "LockPath 가 LedgerPath 의 부모",
			mutate: func(c *Config) {
				c.General.LockPath = filepath.Dir(c.General.LedgerPath)
			},
			wantErr: "must not be LedgerPath or its parent",
		},
		{
			name: "LockPath == [LOG] Dir",
			mutate: func(c *Config) {
				c.General.LockPath = c.Log.Dir
			},
			wantErr: "must not be [LOG] Dir or its parent",
		},
		{
			name: "LockPath 가 [LOG] Dir 의 부모",
			mutate: func(c *Config) {
				c.General.LockPath = filepath.Dir(c.Log.Dir)
			},
			wantErr: "must not be [LOG] Dir or its parent",
		},
		{
			name: "LockPath 가 LedgerPath 옆 전용 경로면 통과",
			mutate: func(c *Config) {
				c.General.LockPath = "data/rinexclient.lock"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			tt.mutate(cfg)

			err := cfg.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}

			mustContain(t, err, "[GENERAL] LockPath")
			mustContain(t, err, tt.wantErr)
		})
	}
}

// download 전용 설정에서 LockPath = logs 는 가장 그럴듯한 오기입이다
// ("lock 을 어느 폴더에 둘까"로 읽힌다).
func TestDownload_LockPathEqualsLogDirRejected(t *testing.T) {
	ini := replaceLine(t, downloadOnlyINI(), "GENERAL", "LockPath", "LockPath = logs")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	mustContain(t, cfg.Validate(), "must not be [LOG] Dir or its parent")
}

// ── LockPath 자리가 lock 으로 쓸 수 있는가 (CheckEnvironment) ─────────

func TestCheckEnvironment_LockPathSlot(t *testing.T) {
	base := t.TempDir()

	envFor := func(lockPath string) error {
		cfg := validConfigForValidate(t)
		cfg.Path = filepath.Join(base, "config.ini")
		cfg.General.LedgerPath = filepath.Join(base, "rinex_ledger.db")
		cfg.General.LockPath = lockPath
		cfg.General.Transport = "localfs" // 키 파일 검사는 이 테스트 밖이다
		return cfg.CheckEnvironment()
	}

	t.Run("없으면 통과 (Acquire 가 만든다)", func(t *testing.T) {
		if err := envFor(filepath.Join(base, "fresh.lock")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("빈 디렉터리(owner 기록 전·정리 잔재)는 통과", func(t *testing.T) {
		p := filepath.Join(base, "empty.lock")
		mustMkdir(t, p)

		if err := envFor(p); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("다른 회차가 실제로 쥔 lock 은 통과", func(t *testing.T) {
		// 실제 lock 패키지로 획득해 owner 접두어가 config 쪽 상수와
		// 어긋나지 않았는지도 함께 고정한다.
		p := filepath.Join(base, "held.lock")

		l, err := lock.Acquire(p, time.Hour)
		if err != nil {
			t.Fatalf("lock.Acquire: %v", err)
		}
		t.Cleanup(func() { _ = l.Release() })

		if err := envFor(p); err != nil {
			t.Fatalf("a lock held by another run must not fail startup: %v", err)
		}
	})

	t.Run("owner 접두어만 흉내 낸 파일은 거부", func(t *testing.T) {
		p := filepath.Join(base, "fake-owner.lock")
		mustMkdir(t, p)
		mustWrite(t, filepath.Join(p, "owner-notes.txt"))

		err := envFor(p)
		if !errors.Is(err, ErrEnvironment) {
			t.Fatalf("error = %v, want ErrEnvironment", err)
		}

		mustContain(t, err, "invalid lock content")
	})

	t.Run("32자리지만 hex가 아닌 owner 파일은 거부", func(t *testing.T) {
		p := filepath.Join(base, "bad-hex.lock")
		mustMkdir(t, p)
		mustWrite(t, filepath.Join(p, "owner-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"))

		mustContain(t, envFor(p), "invalid lock content")
	})

	t.Run("대문자 hex owner 파일은 실제 생성 형식이 아니므로 거부", func(t *testing.T) {
		p := filepath.Join(base, "upper-hex.lock")
		mustMkdir(t, p)
		mustWrite(t, filepath.Join(p, "owner-ABCDEF0123456789ABCDEF0123456789"))

		mustContain(t, envFor(p), "invalid lock content")
	})

	t.Run("owner 이름의 디렉터리는 거부", func(t *testing.T) {
		p := filepath.Join(base, "owner-dir.lock")
		mustMkdir(t, filepath.Join(p, "owner-0123456789abcdef0123456789abcdef"))

		mustContain(t, envFor(p), "invalid lock content")
	})

	t.Run("owner marker가 둘이면 거부", func(t *testing.T) {
		p := filepath.Join(base, "two-owners.lock")
		mustMkdir(t, p)
		mustWrite(t, filepath.Join(p, "owner-0123456789abcdef0123456789abcdef"))
		mustWrite(t, filepath.Join(p, "owner-fedcba9876543210fedcba9876543210"))

		mustContain(t, envFor(p), "multiple entries")
	})

	t.Run("일반 파일이면 거부", func(t *testing.T) {
		p := filepath.Join(base, "afile")
		mustWrite(t, p)

		err := envFor(p)
		if !errors.Is(err, ErrEnvironment) {
			t.Fatalf("error = %v, want ErrEnvironment", err)
		}

		mustContain(t, err, "is not a directory")
	})

	t.Run("내용이 든 기존 폴더(data·logs 등)는 거부", func(t *testing.T) {
		p := filepath.Join(base, "logs")
		mustMkdir(t, p)
		mustWrite(t, filepath.Join(p, "rinexclient-2026-09-25.log"))

		err := envFor(p)
		if !errors.Is(err, ErrEnvironment) {
			t.Fatalf("error = %v, want ErrEnvironment", err)
		}

		mustContain(t, err, "existing directory with other content")
		mustContain(t, err, "rinexclient-2026-09-25.log")
	})
}

// 위 거부가 왜 필요한지의 근거: 방금 파일이 생긴 기존 폴더를 LockPath 로
// 쓰면 lock 패키지는 그것을 "owner 없는 새 lock" 으로 보고 ErrHeld 를
// 돌려준다. main 은 ErrHeld 를 정상 양보로 보고 exit 0 하므로 전송이
// 멈춘 사실이 로그 한 줄 말고는 드러나지 않는다.
func TestLockPath_ExistingFolderWouldSilentlyYield(t *testing.T) {
	p := filepath.Join(t.TempDir(), "logs")
	mustMkdir(t, p)
	mustWrite(t, filepath.Join(p, "today.log"))

	_, err := lock.Acquire(p, time.Hour)
	if !errors.Is(err, lock.ErrHeld) {
		t.Fatalf("lock.Acquire on a busy folder = %v, want ErrHeld (silent yield)", err)
	}

	// 폴더 내용은 건드리지 않는다 (RemoveAll 금지 계약).
	if _, err := os.Stat(filepath.Join(p, "today.log")); err != nil {
		t.Fatalf("lock must not touch folder content: %v", err)
	}
}

// ── Ping-Pong: 같은 폴더의 다른 표기 ─────────────────────────────────

func TestRemoteScope_DotSegmentsNormalized(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"/remote/./rinex2/daily/(YYYY)/", "/remote/rinex2/daily/(YYYY)/", true},
		{"/remote/x/../rinex2/(YYYY)/", "/remote/rinex2/(YYYY)/", true},
		{"/remote//rinex2/(YYYY)/", "/remote/rinex2/(YYYY)/", true},
		{"/../remote/(YYYY)/", "/remote/(YYYY)/", true}, // 루트 위로 가지 않는다
		{"/remote/x/../rinex3/(YYYY)/", "/remote/rinex2/(YYYY)/", false},
	}

	for _, tt := range tests {
		t.Run(tt.a+" vs "+tt.b, func(t *testing.T) {
			a, b := remoteScopeOf(tt.a), remoteScopeOf(tt.b)

			if got := a.overlaps(b); got != tt.want {
				t.Errorf("overlaps = %v, want %v (a=%+v b=%+v)", got, tt.want, a, b)
			}

			if got := b.overlaps(a); got != tt.want {
				t.Errorf("overlaps (reversed) = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPingPong_DotSegmentStillRejected(t *testing.T) {
	ini := validINIForLoadTest() + downloadBlockINI()
	ini = replaceLine(t, ini, "DOWNLOAD.SFTP", "Host", "Host = 192.168.0.1")
	ini = replaceLine(t, ini, "DOWNLOAD.RINEX2_DAILY", "RemotePath",
		"RemotePath = /remote/./rinex2/daily/(YYYY)/(DOY)/")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	mustContain(t, cfg.Validate(), "ping-pong")
}

// ── 평문 경고는 실행할 방향에만 ──────────────────────────────────────

func TestWarnings_OnlyForExecutedDirection(t *testing.T) {
	t.Run("Mode=put + DOWNLOAD 블록 → PUT 경고만", func(t *testing.T) {
		cfg, _, err := mapConfigForTest(t, validINIForLoadTest()+downloadBlockINI())
		if err != nil {
			t.Fatalf("mapConfig() unexpected error: %v", err)
		}

		assertWarningsOnly(t, cfg.Warnings, "[PUT.SFTP]", 3)
	})

	t.Run("Mode=download + PUT 블록 → DOWNLOAD 경고만", func(t *testing.T) {
		ini := replaceLine(t, validINIForLoadTest(), "GENERAL", "Mode", "Mode = download")
		ini = replaceLine(t, ini, "GENERAL", "LockStaleSeconds",
			"LockStaleSeconds = 10800\nLockPath = data/rinexclient.lock")
		ini += downloadBlockINI()

		cfg, _, err := mapConfigForTest(t, ini)
		if err != nil {
			t.Fatalf("mapConfig() unexpected error: %v", err)
		}

		assertWarningsOnly(t, cfg.Warnings, "[DOWNLOAD.SFTP]", 3)
	})
}

// 템플릿을 복사한 PUT 전용 설치처: 예시 파일 그대로면 DOWNLOAD 블록이
// PUT 시작을 막지 않아야 하고, 쓰지 않는 DOWNLOAD 접속 정보로 경고가
// 늘지 않아야 한다.
func TestExampleINI_PutOnlyCopyNotBlockedByDownloadBlock(t *testing.T) {
	data, err := os.ReadFile(exampleINIPath(t))
	if err != nil {
		t.Fatalf("read example: %v", err)
	}

	cfg, err := LoadFrom(
		strings.NewReader(string(data)),
		filepath.Join(t.TempDir(), "config.ini"),
		fakeProtector{},
	)
	if err != nil {
		t.Fatalf("example must load in Mode=put: %v", err)
	}

	if sameEndpoint(cfg.Put.SFTP, cfg.Download.SFTP) {
		t.Error("example [PUT.SFTP] and [DOWNLOAD.SFTP] share host:port; " +
			"a PUT-only copy is one RemotePath edit away from a ping-pong rejection")
	}

	for _, w := range cfg.Warnings {
		if strings.HasPrefix(w, "[DOWNLOAD.SFTP]") {
			t.Errorf("Mode=put must not warn about the unused DOWNLOAD block: %q", w)
		}
	}
}

func assertWarningsOnly(t *testing.T, warnings []string, prefix string, n int) {
	t.Helper()

	if len(warnings) != n {
		t.Errorf("Warnings = %v, want %d with prefix %s", warnings, n, prefix)
	}

	for _, w := range warnings {
		if !strings.HasPrefix(w, prefix) {
			t.Errorf("unexpected warning %q (want only %s)", w, prefix)
		}
	}
}

// Mode=download 는 원격 GET 만 한다. Transport=localfs 는 게이트가
// 열리는 커밋에서 원격 구현 없이 기동하므로 지금 거부한다.
func TestDownload_LocalFSTransportRejected(t *testing.T) {
	ini := replaceLine(t, downloadOnlyINI(), "GENERAL", "Transport", "Transport = localfs")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	err = cfg.Validate()
	mustContain(t, err, "Transport = localfs is not supported when Mode = DOWNLOAD")
	mustContain(t, err, "DOWNLOAD is not implemented")
}

// PUT 의 localfs 는 그대로다. DOWNLOAD 거부가 기존 전송 선택을 건드리면
// 안 된다.
func TestPut_LocalFSTransportStillAllowed(t *testing.T) {
	cfg := validConfigForValidate(t)
	cfg.General.Transport = "localfs"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Mode=put Transport=localfs must validate: %v", err)
	}
}

// 같은 주소의 다른 표기(대소문자, 앞자리 0, IPv6 대괄호)와
// 슬래시만 다른 원격 경로는 순환을 놓치면 안 된다.
func TestPingPong_EquivalentEndpointAndPath(t *testing.T) {
	const overlap = "RemotePath = /remote/rinex2/daily/(YYYY)/(DOY)/"

	tests := []struct {
		name     string
		putHost  string
		dlHost   string
		dlRemote string
	}{
		{
			name:     "호스트명 대소문자",
			putHost:  "sftp.example.com",
			dlHost:   "SFTP.Example.COM",
			dlRemote: overlap,
		},
		{
			name:     "IPv4 앞자리 0",
			putHost:  "192.168.0.1",
			dlHost:   "192.168.000.001",
			dlRemote: overlap,
		},
		{
			name:     "IPv6 대괄호",
			putHost:  "[::1]",
			dlHost:   "::1",
			dlRemote: overlap,
		},
		{
			name:     "역슬래시 경로",
			putHost:  "192.168.0.1",
			dlHost:   "192.168.0.1",
			dlRemote: `RemotePath = \remote\rinex2\daily\(YYYY)\(DOY)\`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ini := validINIForLoadTest() + downloadBlockINI()
			ini = replaceLine(t, ini, "PUT.SFTP", "Host", "Host = "+tt.putHost)
			ini = replaceLine(t, ini, "DOWNLOAD.SFTP", "Host", "Host = "+tt.dlHost)
			ini = replaceLine(t, ini, "DOWNLOAD.RINEX2_DAILY", "RemotePath", tt.dlRemote)

			cfg, _, err := mapConfigForTest(t, ini)
			if err != nil {
				t.Fatalf("mapConfig() unexpected error: %v", err)
			}

			mustContain(t, cfg.Validate(), "ping-pong")
		})
	}
}

// 호스트명과 IP, 서로 다른 IPv6 는 별개 서버로 둔다.
func TestSameHost_DoesNotAliasNameToIP(t *testing.T) {
	if sameHost("localhost", "127.0.0.1") {
		t.Fatal("localhost and 127.0.0.1 must stay distinct")
	}

	if sameHost("::1", "::2") {
		t.Fatal("::1 and ::2 must stay distinct")
	}

	if !sameHost("10.0.0.09", "10.0.0.9") {
		t.Fatal("leading-zero IPv4 must match the canonical address")
	}
}

// LockPath 를 생략한 PUT 은 LedgerPath+".lock" 을 쓴다.
// 그 자리가 일반 파일이거나 내용 있는 폴더면 Acquire 가 매 회차 실패하거나
// ErrHeld 로 조용히 끝나므로, 명시적 LockPath 와 같이 시작에서 거부한다.
func TestCheckEnvironment_DerivedLockSlot(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	mustMkdir(t, data)

	cfg := validConfigForValidate(t)
	cfg.General.Transport = "localfs"
	cfg.General.LockPath = ""
	cfg.General.LedgerPath = filepath.Join(data, "rinex_ledger.db")

	if err := cfg.CheckEnvironment(); err != nil {
		t.Fatalf("missing derived lock must pass: %v", err)
	}

	t.Run("파생 경로가 일반 파일이면 거부", func(t *testing.T) {
		mustWrite(t, cfg.LockFile())

		err := cfg.CheckEnvironment()
		if !errors.Is(err, ErrEnvironment) {
			t.Fatalf("error = %v, want ErrEnvironment", err)
		}

		mustContain(t, err, "is not a directory")
		mustContain(t, err, "rinex_ledger.db.lock")
	})
}

// 파생 lock 경로가 로그·데이터 폴더처럼 파일이 들어 있으면
// lock.Acquire 는 ErrHeld 로 양보하고 main 은 exit 0 한다.
func TestCheckEnvironment_DerivedLockBusyFolder(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	mustMkdir(t, data)

	cfg := validConfigForValidate(t)
	cfg.General.Transport = "localfs"
	cfg.General.LockPath = ""
	cfg.General.LedgerPath = filepath.Join(data, "rinex_ledger.db")

	slot := cfg.LockFile()
	mustMkdir(t, slot)
	mustWrite(t, filepath.Join(slot, "today.log"))

	err := cfg.CheckEnvironment()
	if !errors.Is(err, ErrEnvironment) {
		t.Fatalf("error = %v, want ErrEnvironment", err)
	}

	mustContain(t, err, "existing directory with other content")
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()

	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p string) {
	t.Helper()

	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}
