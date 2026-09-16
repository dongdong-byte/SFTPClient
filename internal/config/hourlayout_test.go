package config

import (
	"errors"
	"strings"
	"testing"
)

// TestValidate_HourLayout 은 HourLayout 과 (HH) 토큰의 교차 검증을 본다.
//
// 현재 코드의 과도기 동작이다. MVP2에서 HourLayout 을 제거하면
// 이 교차 검증도 함께 교체한다. 이 테스트가 있다고 해서 Hourly (HH)
// 필수 검증을 재강화하지 않는다 (GUIDELINES 9.3).
//
// 기준 config 의 RINEX3_HOURLY(인덱스 3)를 변형하여 각 조합을 만든다.
// validConfigForValidate 는 hourly 항목을 dir + (HH) 경로로 채워 둔다.
func TestValidate_HourLayout(t *testing.T) {
	const idx = 3 // RINEX3_HOURLY

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string // "" 이면 통과 기대
	}{
		{
			name: "1 dir 소스 + dir 목적지 → 통과",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutDir
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, `Z:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\`)
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/RNX2/(YYYY)/(DOY)/(HH)/")
			},
			wantErr: "",
		},
		{
			name: "2 dir 소스 + flat 목적지 → 통과 (서울시 Hourly)",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutDir
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, `Z:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\`)
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/RNX2/")
			},
			wantErr: "",
		},
		{
			name: "3 flat 소스 + flat 목적지 → 통과 (지리원)",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutFlat
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/")
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/RNX2/")
			},
			wantErr: "",
		},
		{
			name: "4 dir 소스인데 LocalPath에 (HH) 없음 → 거부",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutDir
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/")
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/RNX2/")
			},
			wantErr: "dir layout requires",
		},
		{
			name: "5 flat 소스인데 LocalPath에 (HH) 있음 → 거부",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutFlat
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/(HH)/")
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/RNX2/")
			},
			wantErr: "flat layout must not include",
		},
		{
			name: "6 Daily LocalPath에 (HH) 있음 → 거부",
			mutate: func(c *Config) {
				c.Put.Categories[0].LocalPath =
					mustTemplate(t, "/local/r2d/(YYYY)/(DOY)/(HH)/")
			},
			wantErr: "daily paths must not include",
		},
		{
			name: "6 Daily RemotePath에 (HH) 있음 → 거부",
			mutate: func(c *Config) {
				c.Put.Categories[0].RemotePath =
					mustTemplate(t, "/remote/r2d/(YYYY)/(DOY)/(HH)/")
			},
			wantErr: "daily paths must not include",
		},
		{
			name: "Hourly RemotePath (HH) 유무는 HourLayout 과 무관 → 통과",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutFlat
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/")
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/remote/r3h/(YYYY)/(DOY)/(HH)/")
			},
			wantErr: "",
		},
		{
			name: "flat + LocalPath (HH) 있음 → 거부 (RemotePath 는 무관)",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutFlat
				// LocalPath 기본값에 (HH) 가 있다.
			},
			wantErr: "flat layout must not include",
		},
		{
			name: "dir + LocalPath (HH) 없음 → 거부 (평면인데 dir 로 오설정)",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = HourLayoutDir
				c.Put.Categories[idx].LocalPath =
					mustTemplate(t, "/local/r3h/(YYYY)/(DOY)/")
				c.Put.Categories[idx].RemotePath =
					mustTemplate(t, "/remote/r3h/(YYYY)/(DOY)/")
			},
			wantErr: "dir layout requires",
		},
		{
			name: "HourLayout 미지정(zero-value) → 거부",
			mutate: func(c *Config) {
				c.Put.Categories[idx].HourLayout = ""
			},
			wantErr: "HourLayout",
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

// TestParseHourLayout 는 값 변환의 경계를 본다.
func TestParseHourLayout(t *testing.T) {
	ok := []struct {
		in   string
		want HourLayout
	}{
		{"dir", HourLayoutDir},
		{"flat", HourLayoutFlat},
		{"  DIR  ", HourLayoutDir}, // 공백·대소문자 허용
		{"Flat", HourLayoutFlat},
	}
	for _, c := range ok {
		got, err := ParseHourLayout(c.in)
		if err != nil {
			t.Errorf("ParseHourLayout(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseHourLayout(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	bad := []string{"", "  ", "sideways", "hour", "1"}
	for _, in := range bad {
		if _, err := ParseHourLayout(in); err == nil {
			t.Errorf("ParseHourLayout(%q): expected error", in)
		}
	}
}

// TestMapConfig_HourLayoutOnDailyIsUnknownKey 는 Daily 섹션에 HourLayout 을
// 적으면 ErrUnknownKey 로 거부되는지 본다. Daily 에는 배치 개념이 없다.
func TestMapConfig_HourLayoutOnDailyIsUnknownKey(t *testing.T) {
	ini := strings.Replace(
		validINIForLoadTest(),
		"[PUT.RINEX2_DAILY]\nEnabled = true",
		"[PUT.RINEX2_DAILY]\nEnabled = true\nHourLayout = dir",
		1,
	)

	_, _, err := mapConfigForTest(t, ini)
	if err == nil {
		t.Fatal("expected error for HourLayout on a daily section")
	}
	if !errors.Is(err, ErrUnknownKey) {
		t.Errorf("error = %v, want ErrUnknownKey", err)
	}
}

// TestMapConfig_HourLayoutBadValue 는 알 수 없는 HourLayout 값이
// ErrBadValue 로 거부되는지 본다.
func TestMapConfig_HourLayoutBadValue(t *testing.T) {
	ini := strings.Replace(
		validINIForLoadTest(),
		"[PUT.RINEX3_HOURLY]\nEnabled = true",
		"[PUT.RINEX3_HOURLY]\nEnabled = true\nHourLayout = sideways",
		1,
	)

	_, _, err := mapConfigForTest(t, ini)
	if err == nil {
		t.Fatal("expected error for bad HourLayout value")
	}
	if !errors.Is(err, ErrBadValue) {
		t.Errorf("error = %v, want ErrBadValue", err)
	}
}

// TestMapConfig_HourLayoutOmittedDefaultsDir 는 Hourly 섹션에서 HourLayout 을
// 생략하면 기본값 dir 로 채워지는지 본다. (HH) 경로와 dir 이 일치하므로
// mapConfig 는 성공한다.
func TestMapConfig_HourLayoutOmittedDefaultsDir(t *testing.T) {
	// validINIForLoadTest 의 hourly 섹션에는 HourLayout 이 없다.
	cfg, _, err := mapConfigForTest(t, validINIForLoadTest())
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	for _, cc := range cfg.Put.Categories {
		if cc.Category.IsHourly() && cc.HourLayout != HourLayoutDir {
			t.Errorf(
				"[%s] HourLayout = %q, want %q (default)",
				cc.Category, cc.HourLayout, HourLayoutDir,
			)
		}
	}
}
