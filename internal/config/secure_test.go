package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProtector 는 config 테스트 전용이다. DPAPI 없이 형식만 흉내 낸다.
//
//	fake:<값>  → 복호화 성공으로 취급, <값> 반환
//	bad:<값>   → 복호화 실패
//	그 외      → 평문 통과
//
// 프로덕션 API 로 export 하지 않는다 — 테스트 도우미다.
type fakeProtector struct{}

func (fakeProtector) Resolve(v string) (string, bool, error) {
	switch {
	case strings.HasPrefix(v, "fake:"):
		return strings.TrimPrefix(v, "fake:"), true, nil

	case strings.HasPrefix(v, "bad:"):
		return "", false, errors.New("decrypt failed (test)")

	default:
		return v, false, nil
	}
}

// #P1 — 이번 작업의 핵심 회귀 가드.
// Port = enc(암호문) 이 복호화된 뒤 정수 변환되는지 본다.
// 순서가 뒤집히면 intVal 이 "fake:2222" 를 파싱하다 실패한다.
func TestMapConfig_EncryptedPortDecryptsBeforeIntParse(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"Port = 22",
		"Port = fake:2222",
		1,
	)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Put.SFTP.Port != 2222 {
		t.Errorf("Port = %d, want 2222", cfg.Put.SFTP.Port)
	}
}

// 이중 오류 가드.
// 복호화 실패 시 오류는 1건이어야 하고, 그 위에 정수 변환 실패
// ("not an integer") 가 겹치면 안 된다.
func TestMapConfig_DecryptFailureSingleError(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"Port = 22",
		"Port = bad:x",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if err == nil {
		t.Fatal("decrypt failure: expected error")
	}

	if !errors.Is(err, ErrBadValue) {
		t.Errorf("error = %v, want ErrBadValue", err)
	}

	if strings.Contains(err.Error(), "not an integer") {
		t.Errorf("복호화 실패에 정수 변환 오류가 겹쳤다: %v", err)
	}
}

// foldKey 회귀 가드.
// 섹션을 [put.sftp] 소문자로 쓰고 Host 를 암호화해도, 저장·조회가
// 모두 foldKey 를 거치므로 Host 평문 경고가 나오면 안 된다.
func TestMapConfig_LowercaseSectionFoldKeyGuard(t *testing.T) {
	input := validINIForLoadTest()
	input = strings.Replace(input, "[PUT.SFTP]", "[put.sftp]", 1)
	input = strings.Replace(
		input,
		"Host = 192.168.0.1",
		"Host = fake:192.168.0.1",
		1,
	)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Put.SFTP.Host != "192.168.0.1" {
		t.Errorf("Host = %q, want 192.168.0.1", cfg.Put.SFTP.Host)
	}

	for _, w := range cfg.Warnings {
		if strings.Contains(w, "Host") {
			t.Errorf("Host 가 암호화됐는데 평문 경고가 나옴: %v", w)
		}
	}
}

// Transport=sftp + 평문 3키 → 경고 3건 (Host/User/Port 각각).
func TestMapConfig_SftpPlaintextWarnsAllThree(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Warnings) != 3 {
		t.Fatalf("경고 수 = %d, want 3: %v", len(cfg.Warnings), cfg.Warnings)
	}

	for _, k := range []string{"Host", "User", "Port"} {
		found := false
		for _, w := range cfg.Warnings {
			if strings.Contains(w, k) {
				found = true
			}
		}

		if !found {
			t.Errorf("경고에 %s 없음: %v", k, cfg.Warnings)
		}
	}
}

// Transport=sftp + 전부 암호화 → 경고 0건.
func TestMapConfig_SftpAllEncryptedNoWarn(t *testing.T) {
	input := validINIForLoadTest()
	input = strings.Replace(input, "Host = 192.168.0.1", "Host = fake:192.168.0.1", 1)
	input = strings.Replace(input, "User = rinexclient", "User = fake:rinexclient", 1)
	input = strings.Replace(input, "Port = 22", "Port = fake:22", 1)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Warnings) != 0 {
		t.Errorf("전부 암호화인데 경고가 나옴: %v", cfg.Warnings)
	}
}

// Transport=localfs → 평문이어도 경고 0건 (소음 방지).
func TestMapConfig_LocalfsNoPlaintextWarning(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"Transport = sftp",
		"Transport = localfs",
		1,
	)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(cfg.Warnings) != 0 {
		t.Errorf("localfs 인데 경고가 나옴: %v", cfg.Warnings)
	}
}

// nil Protector → 배선 누락 오류.
func TestMapConfig_NilProtector(t *testing.T) {
	f, err := parseINI(strings.NewReader(validINIForLoadTest()))
	if err != nil {
		t.Fatalf("parseINI: %v", err)
	}

	path := filepath.Join(t.TempDir(), "config.ini")

	if _, err := mapConfig(f, path, nil); err == nil {
		t.Fatal("nil Protector: expected error")
	}
}
