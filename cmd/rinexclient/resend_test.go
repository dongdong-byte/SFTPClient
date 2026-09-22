package main

import (
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/domain"
)

// resend 서브커맨드의 실행 전 해석 (v4 §6.3) 테스트.
//
// 고정하는 계약:
//   - 날짜는 YYYY-MM-DD 와 YYYY-DDD(연-DOY) 두 형식만, UTC 자정.
//     평년의 DOY 366 등 존재하지 않는 날짜는 거부한다.
//   - 로그 이중 표기는 2026-09-01(244) 형태다.
//   - --category 생략 = 활성 전부, 지정 = 유효·활성만 허용(아니면
//     실행 전 오류), 중복은 조용히 제거.
//
// 락 대기 루프(10초 간격, 상한 LockStaleSeconds)와 0건 exit 0 은
// 시계·프로세스가 얽혀 단위 테스트 대신 수동 검증 절차(커밋 9 문서)로
// 확인한다.

func TestParseObsDate(t *testing.T) {
	valid := []struct {
		in   string
		want time.Time
	}{
		{"2026-09-01", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-244", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-001", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		{"2026-365", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)},
		// 윤년의 366.
		{"2024-366", time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)},
		{" 2026-09-01 ", time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
	}

	for _, tt := range valid {
		got, err := parseObsDate(tt.in)
		if err != nil {
			t.Errorf("parseObsDate(%q) 오류: %v", tt.in, err)
			continue
		}

		if !got.Equal(tt.want) || got.Location() != time.UTC {
			t.Errorf(
				"parseObsDate(%q) = %v, want %v (UTC)",
				tt.in, got, tt.want,
			)
		}
	}

	invalid := []string{
		"",
		"2026-366",   // 평년의 366
		"2026-000",   // DOY 하한 밖
		"2026-999",   // DOY 상한 밖
		"2026-9-1",   // 자릿수 부족
		"2026/09/01", // 구분자
		"20260901",   // 구분자 없음
		"2026-02-30", // 존재하지 않는 날
		"09-01",      // 연도 없음
		"2026-244-1", // 꼬리
		"abcd-efg",   // 문자
	}

	for _, in := range invalid {
		if _, err := parseObsDate(in); err == nil {
			t.Errorf("parseObsDate(%q) 가 통과했다", in)
		}
	}
}

func TestObsDateLabel(t *testing.T) {
	tests := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), "2026-09-01(244)"},
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-01-01(001)"},
		{time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC), "2024-12-31(366)"},
	}

	for _, tt := range tests {
		if got := obsDateLabel(tt.in); got != tt.want {
			t.Errorf("obsDateLabel = %q, want %q", got, tt.want)
		}
	}
}

func TestSelectCategories(t *testing.T) {
	enabled := []config.CategoryConfig{
		{Category: domain.CategoryRINEX2Daily},
		{Category: domain.CategoryRINEX3Hourly},
	}

	// 생략 — 활성 전부.
	got, err := selectCategories(enabled, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("생략: got %d cats, err=%v, want 2/nil", len(got), err)
	}

	// 지정 — 대소문자·공백 관용, 중복 조용히 제거.
	got, err = selectCategories(enabled, " rinex2_daily , RINEX2_DAILY ")
	if err != nil || len(got) != 1 ||
		got[0].Category != domain.CategoryRINEX2Daily {
		t.Fatalf("지정: got %+v, err=%v", got, err)
	}

	// 유효하지만 비활성 — 실행 전 오류.
	if _, err := selectCategories(enabled, "RINEX2_HOURLY"); err == nil ||
		!strings.Contains(err.Error(), "활성") {
		t.Fatalf("비활성 카테고리가 통과했다: %v", err)
	}

	// 존재하지 않는 카테고리 — 실행 전 오류.
	if _, err := selectCategories(enabled, "RINEX9_DAILY"); err == nil {
		t.Fatal("미지 카테고리가 통과했다")
	}

	// 빈 항목 — 실행 전 오류.
	if _, err := selectCategories(enabled, "RINEX2_DAILY,,"); err == nil {
		t.Fatal("빈 항목이 통과했다")
	}

	// 활성이 하나도 없는데 생략 — 오류.
	if _, err := selectCategories(nil, ""); err == nil {
		t.Fatal("빈 활성 목록이 통과했다")
	}
}
