package domain

import "testing"

func TestParseOrigin(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Origin
		wantErr bool
	}{
		{name: "LOCAL", in: "LOCAL", want: OriginLocal},
		{name: "DOWNLOAD", in: "DOWNLOAD", want: OriginDownload},
		{name: "소문자는 오류", in: "local", wantErr: true},
		{name: "RECEIVER 는 사용하지 않는다", in: "RECEIVER", wantErr: true},
		{name: "공백 포함은 오류", in: " LOCAL", wantErr: true},
		{name: "빈 문자열은 오류", in: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseOrigin(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseOrigin(%q) 오류를 기대했으나 %q", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseOrigin(%q) 예상치 못한 오류: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseOrigin(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestOriginValid(t *testing.T) {
	tests := []struct {
		in   Origin
		want bool
	}{
		{OriginLocal, true},
		{OriginDownload, true},
		{Origin(""), false},
		{Origin("local"), false},
		{Origin("RECEIVER"), false},
	}

	for _, tt := range tests {
		if got := tt.in.Valid(); got != tt.want {
			t.Errorf("Origin(%q).Valid() = %v, want %v", tt.in, got, tt.want)
		}
	}
}
