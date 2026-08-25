package domain

import "testing"

func TestParseMode(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Mode
		wantErr bool
	}{
		{name: "정확한 표기", in: "PUT", want: ModePut},
		{name: "설계안 예시의 소문자", in: "both", want: ModeBoth},
		{name: "앞뒤 공백 허용", in: "  DOWNLOAD  ", want: ModeDownload},
		{name: "대소문자 혼용", in: "Put", want: ModePut},
		{name: "알 수 없는 값은 오류", in: "SYNC", wantErr: true},
		{name: "빈 문자열은 오류", in: "", wantErr: true},
		{name: "공백만은 오류", in: "   ", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMode(tt.in)

			if tt.wantErr {
				if err == nil {
					t.Fatalf(
						"ParseMode(%q) 오류를 기대했으나 %q 를 돌려주었다",
						tt.in,
						got,
					)
				}
				return
			}

			if err != nil {
				t.Fatalf(
					"ParseMode(%q) 예상치 못한 오류: %v",
					tt.in,
					err,
				)
			}

			if got != tt.want {
				t.Errorf(
					"ParseMode(%q) = %q, want %q",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}

// Modes 는 config 검증의 기준이 된다.
// 새 Mode 상수를 추가하면서 이 목록에 넣는 것을 잊으면
// 해당 값이 config 에서 조용히 거부된다.
func TestModes(t *testing.T) {
	got := Modes()

	if len(got) != 3 {
		t.Fatalf("Modes() 길이 = %d, want 3", len(got))
	}

	seen := make(map[Mode]bool, len(got))

	for _, m := range got {
		if seen[m] {
			t.Errorf("Modes() 에 중복된 값이 있다: %q", m)
		}
		seen[m] = true

		if _, err := ParseMode(string(m)); err != nil {
			t.Errorf("Modes() 가 파싱 불가능한 값을 포함한다: %q", m)
		}
	}
}

// BOTH 를 빠뜨린 분기는 오류 없이 한쪽 동작만 중단시킨다.
// 세 Mode × 두 판정을 전부 고정한다.
func TestModeDoes(t *testing.T) {
	tests := []struct {
		in           Mode
		wantPut      bool
		wantDownload bool
	}{
		{ModePut, true, false},
		{ModeDownload, false, true},
		{ModeBoth, true, true},

		// 파싱을 거치지 않은 값은 어느 쪽도 수행하지 않는다.
		{Mode(""), false, false},
		{Mode("put"), false, false},
		{Mode("SYNC"), false, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.in), func(t *testing.T) {
			if got := tt.in.DoesPut(); got != tt.wantPut {
				t.Errorf(
					"%q.DoesPut() = %v, want %v",
					tt.in,
					got,
					tt.wantPut,
				)
			}

			if got := tt.in.DoesDownload(); got != tt.wantDownload {
				t.Errorf(
					"%q.DoesDownload() = %v, want %v",
					tt.in,
					got,
					tt.wantDownload,
				)
			}
		})
	}
}

func TestModeString(t *testing.T) {
	tests := []struct {
		in   Mode
		want string
	}{
		{ModePut, "PUT"},
		{ModeDownload, "DOWNLOAD"},
		{ModeBoth, "BOTH"},
	}

	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("Mode(%q).String() = %q, want %q", tt.in, got, tt.want)
		}
	}
}
