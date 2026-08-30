package domain

import "testing"

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Status
		wantErr bool
	}{
		{name: "PENDING", in: "PENDING", want: StatusPending},
		{name: "IN_PROGRESS", in: "IN_PROGRESS", want: StatusInProgress},
		{name: "VERIFIED", in: "VERIFIED", want: StatusVerified},
		{name: "FAILED", in: "FAILED", want: StatusFailed},
		{name: "소문자는 오류", in: "pending", wantErr: true},
		{name: "SUCCESS 는 쓰지 않는다", in: "SUCCESS", wantErr: true},
		{name: "공백 포함은 오류", in: " PENDING", wantErr: true},
		{name: "빈 문자열은 오류", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseStatus(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseStatus(%q) 오류를 기대했으나 %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseStatus(%q) 예상치 못한 오류: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseStatus(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestStatusValid(t *testing.T) {
	tests := []struct {
		in   Status
		want bool
	}{
		{StatusPending, true},
		{StatusInProgress, true},
		{StatusVerified, true},
		{StatusFailed, true},
		{Status("SUCCESS"), false},
		{Status(""), false},
		{Status("pending"), false},
	}

	for _, tt := range tests {
		if got := tt.in.Valid(); got != tt.want {
			t.Errorf("Status(%q).Valid() = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestStatusIsTerminal(t *testing.T) {
	tests := []struct {
		in   Status
		want bool
	}{
		{StatusPending, false},
		{StatusInProgress, false},
		{StatusFailed, false},
		{StatusVerified, true},
	}

	for _, tt := range tests {
		if got := tt.in.IsTerminal(); got != tt.want {
			t.Errorf("%q.IsTerminal() = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestStatusCanTransitionTo(t *testing.T) {
	all := []Status{
		StatusPending,
		StatusInProgress,
		StatusVerified,
		StatusFailed,
	}

	allowed := map[Status]map[Status]bool{
		StatusPending: {
			StatusInProgress: true,
			StatusFailed:     true,
		},
		StatusInProgress: {
			StatusVerified: true,
			StatusFailed:   true,
		},
		StatusFailed: {
			StatusInProgress: true,
		},
		StatusVerified: {},
	}

	for _, from := range all {
		for _, to := range all {
			want := allowed[from][to]
			got := from.CanTransitionTo(to)
			if got != want {
				t.Errorf(
					"%q.CanTransitionTo(%q) = %v, want %v",
					from,
					to,
					got,
					want,
				)
			}
		}
	}

	if StatusPending.CanTransitionTo(Status("UNKNOWN")) {
		t.Error("알 수 없는 상태로의 전이가 허용되면 안 된다")
	}
	if Status("UNKNOWN").CanTransitionTo(StatusPending) {
		t.Error("알 수 없는 상태에서 출발하는 전이가 허용되면 안 된다")
	}
}
