package domain

import "testing"

func TestParseCategory(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Category
		wantErr bool
	}{
		{name: "정확한 표기", in: "RINEX3_HOURLY", want: CategoryRINEX3Hourly},
		{name: "소문자 허용", in: "rinex2_daily", want: CategoryRINEX2Daily},
		{name: "앞뒤 공백 허용", in: "  RINEX3_DAILY  ", want: CategoryRINEX3Daily},
		{name: "RINEX4 허용", in: "RINEX4_HOURLY", want: CategoryRINEX4Hourly},
		{name: "알 수 없는 값은 오류", in: "RINEX5_HOURLY", wantErr: true},
		{name: "빈 문자열은 오류", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCategory(tt.in)

			if tt.wantErr {
				if err == nil {
					t.Fatalf(
						"ParseCategory(%q) 오류를 기대했으나 %q 를 돌려주었다",
						tt.in,
						got,
					)
				}
				return
			}

			if err != nil {
				t.Fatalf(
					"ParseCategory(%q) 예상치 못한 오류: %v",
					tt.in,
					err,
				)
			}

			if got != tt.want {
				t.Errorf(
					"ParseCategory(%q) = %q, want %q",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}

// Categories 는 config 순회의 기준이 된다.
// 새 Category 상수를 추가하면서 이 목록에 넣는 것을 잊으면
// 해당 Category 의 config 섹션이 조용히 무시된다.
func TestCategories(t *testing.T) {
	got := Categories()

	if len(got) != 6 {
		t.Fatalf("Categories() 길이 = %d, want 6", len(got))
	}

	seen := make(map[Category]bool, len(got))
	for _, c := range got {
		if seen[c] {
			t.Errorf("Categories() 에 중복된 값이 있다: %q", c)
		}
		seen[c] = true

		if _, err := ParseCategory(string(c)); err != nil {
			t.Errorf("Categories() 가 파싱 불가능한 값을 포함한다: %q", c)
		}
	}
}

func TestCategoryIsHourly(t *testing.T) {
	tests := []struct {
		in   Category
		want bool
	}{
		{CategoryRINEX2Hourly, true},
		{CategoryRINEX3Hourly, true},
		{CategoryRINEX4Hourly, true},
		{CategoryRINEX2Daily, false},
		{CategoryRINEX3Daily, false},
		{CategoryRINEX4Daily, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			if got := tt.in.IsHourly(); got != tt.want {
				t.Errorf(
					"%q.IsHourly() = %v, want %v",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestCategoryIsDaily(t *testing.T) {
	tests := []struct {
		in   Category
		want bool
	}{
		{CategoryRINEX2Daily, true},
		{CategoryRINEX3Daily, true},
		{CategoryRINEX4Daily, true},
		{CategoryRINEX2Hourly, false},
		{CategoryRINEX3Hourly, false},
		{CategoryRINEX4Hourly, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			if got := tt.in.IsDaily(); got != tt.want {
				t.Errorf(
					"%q.IsDaily() = %v, want %v",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}

func TestCategoryMatchString(t *testing.T) {
	tests := []struct {
		in   CategoryMatch
		want string
	}{
		{CategoryMatchUnknown, "Unknown"},
		{CategoryMatchOK, "OK"},
		{CategoryMatchMismatch, "Mismatch"},
		{CategoryMatch(99), "CategoryMatch(99)"},
	}

	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("CategoryMatch(%d).String() = %q, want %q", int(tt.in), got, tt.want)
		}
	}
}

func TestCategoryMatchesName(t *testing.T) {
	tests := []struct {
		name     string
		category Category
		fileName string
		want     CategoryMatch
	}{
		{
			name:     "RINEX3 Hourly 일치",
			category: CategoryRINEX3Hourly,
			fileName: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
			want:     CategoryMatchOK,
		},
		{
			name:     "RINEX3 Daily 일치",
			category: CategoryRINEX3Daily,
			fileName: "sonp00kor_r_20260010000_01d_30s_mo.rnx.gz",
			want:     CategoryMatchOK,
		},
		{
			name:     "config 는 Daily 인데 파일은 Hourly",
			category: CategoryRINEX3Daily,
			fileName: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
			want:     CategoryMatchMismatch,
		},
		{
			name:     "config 는 Hourly 인데 파일은 Daily",
			category: CategoryRINEX3Hourly,
			fileName: "gumc00kor_r_20260040000_01d_mn.rnx.gz",
			want:     CategoryMatchMismatch,
		},
		{
			name:     "항법 파일은 샘플링 필드가 없어도 판정된다",
			category: CategoryRINEX3Hourly,
			fileName: "sonp00kor_r_20260010300_01h_mn.rnx.gz",
			want:     CategoryMatchOK,
		},
		{
			name:     "미확정 주기는 Unknown",
			category: CategoryRINEX3Hourly,
			fileName: "sonp00kor_r_20260010300_15m_01s_ms.rnx.gz",
			want:     CategoryMatchUnknown,
		},
		{
			name:     "RINEX2 Hourly 인데 세션 문자가 Daily('0') 이면 Mismatch",
			category: CategoryRINEX2Hourly,
			fileName: "sonp0010.26o",
			want:     CategoryMatchMismatch,
		},
		{
			name:     "RINEX4 는 긴 파일명 규칙으로 판정한다",
			category: CategoryRINEX4Daily,
			fileName: "sonp00kor_r_20260010000_01d_30s_mo.rnx.gz",
			want:     CategoryMatchOK,
		},
		{
			name:     "RINEX3 Category 인데 긴 파일명이 아니면 Unknown",
			category: CategoryRINEX3Hourly,
			fileName: "readme.txt",
			want:     CategoryMatchUnknown,
		},
		{
			name:     "대문자 파일명도 내부 정규화 후 판정",
			category: CategoryRINEX3Hourly,
			fileName: "SONP00KOR_R_20260010300_01H_01S_MS.RNX.GZ",
			want:     CategoryMatchOK,
		},
		{
			name:     "전체 경로와 part 접미사도 내부 정규화 후 판정",
			category: CategoryRINEX3Hourly,
			fileName: `D:\RINEX3\2026\001\03\SONP00KOR_R_20260010300_01H_01S_MS.RNX.GZ.part`,
			want:     CategoryMatchOK,
		},
		{
			name:     "디렉터리에 밑줄이 있어도 파일명만 판정한다",
			category: CategoryRINEX3Hourly,
			fileName: `Z:\RINEX_V3_D\2026\003\SONP00KOR_R_20260010300_01H_MN.rnx.gz`,
			want:     CategoryMatchOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.category.MatchesName(tt.fileName)
			if got != tt.want {
				t.Errorf(
					"%q.MatchesName(%q) = %v, want %v",
					tt.category,
					tt.fileName,
					got,
					tt.want,
				)
			}
		})
	}
}
