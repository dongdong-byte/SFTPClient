package config

import (
	"strings"
	"testing"
	"time"
)

// withStallKey 는 기본 픽스처의 [PUT.SFTP] 에 StallTimeoutSeconds 를
// 주입한다. 기본 픽스처 자체에는 이 키가 없다 — 그것이 "키 부재" 케이스의
// 검증 수단이다 (기존 현장 config 과 동형, withBackfillKey 전례).
func withStallKey(t *testing.T, value string) string {
	base := validINIForLoadTest()

	const anchor = "KnownHosts"
	idx := strings.Index(base, anchor)
	if idx < 0 {
		t.Fatal("픽스처에서 [PUT.SFTP] KnownHosts 를 찾지 못했다")
	}

	lineEnd := strings.Index(base[idx:], "\n")
	if lineEnd < 0 {
		t.Fatal("KnownHosts 줄 끝을 찾지 못했다")
	}

	insert := idx + lineEnd
	return base[:insert] +
		"\nStallTimeoutSeconds = " + value +
		base[insert:]
}

// TestStallTimeout_AbsentMeansDefault 는 키 부재가 오류도 0도 아닌
// 기본값 30초임을 고정한다. (UNIT3 — MaxHashBackfillPerRun H14 와
// 같은 계약)
//
// 이 계약이 깨지면 실행파일만 교체한 운영 3기관 전부에서 시작이
// 거부되거나(필수 키 오독 — 첫 납품분이 실제로 낸 회귀) 감시 문턱이
// 0 으로 접혀 validate 가 거부한다.
func TestStallTimeout_AbsentMeansDefault(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("키 부재가 Load 오류를 냈다: %v", err)
	}

	want := DefaultStallTimeoutSeconds * time.Second
	if cfg.Put.SFTP.StallTimeout != want {
		t.Fatalf(
			"키 부재 = %v, want %v (기본값)",
			cfg.Put.SFTP.StallTimeout,
			want,
		)
	}
}

// TestStallTimeout_PresentKept 는 명시 값이 그대로 보존됨을 고정한다.
func TestStallTimeout_PresentKept(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, withStallKey(t, "60"))
	if err != nil {
		t.Fatalf("mapConfig: %v", err)
	}

	if cfg.Put.SFTP.StallTimeout != 60*time.Second {
		t.Fatalf(
			"명시 60 = %v, want 60s",
			cfg.Put.SFTP.StallTimeout,
		)
	}
}

// TestStallTimeout_ValidateRange 는 validate 의 양끝을 고정한다.
// 명시 0(감시 사실상 해제 시도)과 601(상한 초과)은 거부, 경계값
// 5·600 은 허용. 상한 600 의 근거: 스톨 좀비가 stale 탈취(이중 실행,
// UNIT3 v3 §2.1)에 도달하기 전에 감시가 반드시 먼저 발화해야 한다.
func TestStallTimeout_ValidateRange(t *testing.T) {
	cases := []struct {
		name  string
		value time.Duration
		ok    bool
	}{
		{"명시 0 거부", 0, false},
		{"하한 미만 4초 거부", 4 * time.Second, false},
		{"경계 5초 허용", 5 * time.Second, true},
		{"경계 600초 허용", 600 * time.Second, true},
		{"상한 초과 601초 거부", 601 * time.Second, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfigForValidate(t)
			cfg.Put.SFTP.StallTimeout = tc.value

			err := cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected validation error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("StallTimeout = %v 이 거부되지 않았다", tc.value)
			}
		})
	}
}
