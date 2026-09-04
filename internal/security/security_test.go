package security

import "testing"

func TestIsEncrypted(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  bool
	}{
		{"enc 접두어", "enc:AQAA", true},
		{"enc 접두어만", "enc:", true},
		{"평문 IP", "192.168.0.1", false},
		{"접두어를 닮은 값", "encoder", false},
		{"빈 값", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEncrypted(tc.value); got != tc.want {
				t.Fatalf(
					"isEncrypted(%q) = %v, want %v",
					tc.value, got, tc.want,
				)
			}
		})
	}
}
