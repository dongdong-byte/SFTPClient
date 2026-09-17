package config

import (
	"errors"
	"strings"
	"testing"
)

// withBackfillKey 는 기본 픽스처의 [PUT] 에 MaxHashBackfillPerRun 을
// 주입한다. 기본 픽스처 자체에는 이 키가 없다 — 그것이 "키 부재" 케이스의
// 검증 수단이다 (기존 현장 config 과 동형).
func withBackfillKey(value string) string {
	return strings.Replace(
		validINIForLoadTest(),
		"MaxFilesPerRun = 2000",
		"MaxFilesPerRun = 2000\nMaxHashBackfillPerRun = "+value,
		1,
	)
}

// TestMaxHashBackfillPerRun_AbsentMeansDefault 는 키 부재가 오류도
// 0(백필 끔)도 아닌 기본값 500 임을 고정한다. (H14, 설계 v3 §3.1)
//
// 이 계약이 깨지면 실행파일만 교체한 운영 3기관 전부에서 백필이
// 조용히 꺼지거나(0 오독) 시작이 거부된다(필수 키 오독).
func TestMaxHashBackfillPerRun_AbsentMeansDefault(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("키 부재가 Load 오류를 냈다: %v", err)
	}

	if cfg.Put.MaxHashBackfillPerRun != DefaultMaxHashBackfillPerRun {
		t.Fatalf(
			"키 부재 = %d, want %d (기본값)",
			cfg.Put.MaxHashBackfillPerRun,
			DefaultMaxHashBackfillPerRun,
		)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("기본값이 Validate 를 통과하지 못함: %v", err)
	}
}

// TestMaxHashBackfillPerRun_ZeroDisables 는 명시된 0 이 부재와 구분되어
// 그대로 보존됨(백필 끔)을 고정한다.
func TestMaxHashBackfillPerRun_ZeroDisables(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, withBackfillKey("0"))
	if err != nil {
		t.Fatalf("mapConfig: %v", err)
	}

	if cfg.Put.MaxHashBackfillPerRun != 0 {
		t.Fatalf(
			"명시 0 = %d, want 0 (부재와 구분되어야 한다)",
			cfg.Put.MaxHashBackfillPerRun,
		)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("0 이 Validate 를 통과하지 못함: %v", err)
	}
}

// TestMaxHashBackfillPerRun_PositiveKept 는 양수 상한이 보존됨을 고정한다.
func TestMaxHashBackfillPerRun_PositiveKept(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, withBackfillKey("1200"))
	if err != nil {
		t.Fatalf("mapConfig: %v", err)
	}

	if cfg.Put.MaxHashBackfillPerRun != 1200 {
		t.Fatalf("양수 = %d, want 1200", cfg.Put.MaxHashBackfillPerRun)
	}
}

// TestMaxHashBackfillPerRun_NegativeRejected 는 음수가 validate 에서
// 거부됨을 고정한다.
func TestMaxHashBackfillPerRun_NegativeRejected(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, withBackfillKey("-1"))
	if err != nil {
		t.Fatalf("mapConfig: %v (음수 거부는 validate 의 몫)", err)
	}

	err = cfg.Validate()
	if err == nil {
		t.Fatal("음수가 Validate 를 통과했다")
	}
	if !strings.Contains(err.Error(), "MaxHashBackfillPerRun") {
		t.Fatalf("음수 거부가 다른 오류로 위장됨: %v", err)
	}
}

// TestMaxHashBackfillPerRun_NotAnIntegerRejected 는 비정수 값이
// 기본값으로 조용히 접히지 않고 Load 오류로 끝남을 고정한다.
func TestMaxHashBackfillPerRun_NotAnIntegerRejected(t *testing.T) {
	_, _, err := mapConfigForTest(t, withBackfillKey("many"))
	if !errors.Is(err, ErrBadValue) {
		t.Fatalf("비정수가 ErrBadValue 가 아님: %v", err)
	}
}

// TestMaxHashBackfillPerRun_EmptyRejected 는 키는 있는데 값이 비어 있으면
// 부재(500)로 접히지 않고 Load 오류가 됨을 고정한다.
func TestMaxHashBackfillPerRun_EmptyRejected(t *testing.T) {
	_, _, err := mapConfigForTest(t, withBackfillKey(""))
	if !errors.Is(err, ErrBadValue) {
		t.Fatalf("빈 값이 ErrBadValue 가 아님: %v", err)
	}
}
