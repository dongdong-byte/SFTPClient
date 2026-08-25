package domain

import "testing"

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "대문자를 소문자로 통일한다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
		},
		{
			name: "Windows 경로를 제거한다",
			in:   `D:\RINEX3\2026\001\03\SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz`,
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
		},
		{
			name: "POSIX 경로를 제거한다",
			in:   "/RNXOutgoing/2026/001/03/SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
		},
		{
			name: ".part 접미사를 제거한다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz.part",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
		},
		{
			name: "대문자 .PART 도 제거한다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz.PART",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
		},
		{
			name: "압축 확장자는 유지한다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.Z",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx.z",
		},
		{
			name: "비압축 파일은 그대로 둔다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeName(tt.in); got != tt.want {
				t.Errorf(
					"NormalizeName(%q) = %q, want %q",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}

// 경로 비의존성은 BOTH 모드에서 동일 파일을 동일한 식별자로
// 인식하기 위한 핵심 조건이므로 별도 테스트로 명시한다. (설계안 9.1)
func TestNormalizeNameIsPathIndependent(t *testing.T) {
	const fileName = "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz"

	paths := []string{
		`D:\RINEX3\2026\001\03\` + fileName,
		`E:\DOWNLOAD\RINEX3\2026\001\03\` + fileName,
		"/RNX/2026/001/03/" + fileName,
		"/RNXOutgoing/2026/001/03/" + fileName,
		fileName,
	}

	want := NormalizeName(paths[0])

	for _, p := range paths[1:] {
		if got := NormalizeName(p); got != want {
			t.Errorf(
				"경로에 따라 식별자가 달라졌다\n  %q\n  → %q, want %q",
				p,
				got,
				want,
			)
		}
	}
}

// .part 만 있고 본체가 없는 파일은 정규화하면 빈 문자열이 된다.
// 빈 file_name 은 PK 로 유효하지 않으며 DDL 의
// CHECK (file_name = lower(file_name)) 도 걸러내지 못한다.
// Scanner 가 IsPartFile 로 먼저 제외하는 것이 유일한 방어선이다.
func TestNormalizeNameOnPartOnlyName(t *testing.T) {
	const in = ".part"

	if !IsPartFile(in) {
		t.Fatalf(
			"IsPartFile(%q) = false, 이 경로가 뚫리면 빈 file_name 이 등록된다",
			in,
		)
	}

	if got := NormalizeName(in); got != "" {
		t.Errorf("NormalizeName(%q) = %q, want %q", in, got, "")
	}
}

func TestBaseName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: ".gz 를 제거한다",
			in:   "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
		{
			name: ".z 를 제거한다",
			in:   "sonp00kor_r_20260010300_01h_01s_ms.rnx.z",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
		{
			name: "대문자 압축 확장자도 제거한다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.Z",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
		{
			name: "경로가 포함되어도 처리한다",
			in:   `D:\RINEX3\SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz`,
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
		{
			name: ".part 가 붙어 있어도 처리한다",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz.part",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
		{
			name: "압축되지 않았으면 그대로 둔다",
			in:   "sonp00kor_r_20260010300_01h_01s_ms.rnx",
			want: "sonp00kor_r_20260010300_01h_01s_ms.rnx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := BaseName(tt.in); got != tt.want {
				t.Errorf(
					"BaseName(%q) = %q, want %q",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}

// .rnx 와 .rnx.gz 가 같은 base_name 으로 묶여야
// 압축/비압축 형태의 동시 유입을 관측할 수 있다. (CONCEPT 4.7)
func TestBaseNameGroupsCompressedAndPlain(t *testing.T) {
	plain := BaseName("SONP00KOR_R_20260010300_01H_01S_MS.rnx")
	gzipped := BaseName("SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz")

	if plain != gzipped {
		t.Errorf(
			"두 형태의 base_name 이 다르다: %q vs %q",
			plain,
			gzipped,
		)
	}
}

// base_name 은 식별자가 아니므로 압축/비압축을 묶지만,
// file_name 은 식별자이므로 둘을 구분해야 한다.
// 이 구분이 사라지면 서로 다른 데이터가 하나로 합쳐져 영구 누락된다. (CONCEPT 4.2)
func TestNormalizeNameKeepsCompressedAndPlainDistinct(t *testing.T) {
	plain := NormalizeName("SONP00KOR_R_20260010300_01H_01S_MS.rnx")
	gzipped := NormalizeName("SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz")

	if plain == gzipped {
		t.Errorf(
			"압축 여부가 다른 파일이 같은 식별자를 갖는다: %q",
			plain,
		)
	}
}

func TestIsPartFile(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{
			name: "소문자 part",
			in:   "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz.part",
			want: true,
		},
		{
			name: "대문자 PART",
			in:   "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz.PART",
			want: true,
		},
		{
			name: "Windows 경로의 part",
			in:   `D:\RINEX3\SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz.part`,
			want: true,
		},
		{
			name: "POSIX 경로의 part",
			in:   "/rinex/SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz.part",
			want: true,
		},
		{
			name: "정상 파일",
			in:   "sonp00kor_r_20260010300_01h_01s_ms.rnx.gz",
			want: false,
		},
		{
			name: "part 가 파일명 중간에 있으면 임시 파일이 아니다",
			in:   "sonp_part_20260010300_01h_mn.rnx.gz",
			want: false,
		},
		{
			name: "part 로 시작하는 이름은 임시 파일이 아니다",
			in:   "partial.rnx",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPartFile(tt.in); got != tt.want {
				t.Errorf(
					"IsPartFile(%q) = %v, want %v",
					tt.in,
					got,
					tt.want,
				)
			}
		})
	}
}
