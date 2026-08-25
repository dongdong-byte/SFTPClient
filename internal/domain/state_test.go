package domain

import "testing"

func TestParseState(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    State
		wantErr bool
	}{
		{name: "READY", in: "READY", want: StateReady},
		{name: "CHANGED", in: "CHANGED", want: StateChanged},
		{name: "소문자는 오류", in: "ready", wantErr: true},
		{name: "공백 포함은 오류", in: " READY", wantErr: true},
		{name: "빈 문자열은 오류", in: "", wantErr: true},
		{name: "알 수 없는 값", in: "PENDING", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseState(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseState(%q) 오류를 기대했으나 %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseState(%q) 예상치 못한 오류: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseState(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStateValid(t *testing.T) {
	tests := []struct {
		in   State
		want bool
	}{
		{StateReady, true},
		{StateChanged, true},
		{State(""), false},
		{State("ready"), false},
		{State("PENDING"), false},
	}

	for _, tt := range tests {
		if got := tt.in.Valid(); got != tt.want {
			t.Errorf("State(%q).Valid() = %v, want %v", tt.in, got, tt.want)
		}
	}
}
