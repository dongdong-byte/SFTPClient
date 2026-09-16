package pathpl

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// utc 는 테스트에서 쓸 UTC 시각을 만든다.
func utc(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, time.UTC)
}

// 실제 기관 디렉터리 구조로 확인한다.
//
//	D:\RINEX-V3-D\2026\182\
//	D:\RINEX-V3-H\2026\182\00\
func TestExpandRealPaths(t *testing.T) {
	// 2026년 7월 1일은 DOY 182 이다.
	when := utc(2026, time.July, 1, 0)

	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{
			name: "RINEX3 Daily",
			tmpl: `D:\RINEX-V3-D\(YYYY)\(DOY)\`,
			want: `D:\RINEX-V3-D\2026\182\`,
		},
		{
			name: "RINEX3 Hourly",
			tmpl: `D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\`,
			want: `D:\RINEX-V3-H\2026\182\00\`,
		},
		{
			name: "원격 POSIX 경로",
			tmpl: "/RNXOutgoing/(YYYY)/(DOY)/(HH)/",
			want: "/RNXOutgoing/2026/182/00/",
		},
		{
			name: "토큰 없는 flat RemotePath",
			tmpl: "/RNX2/",
			want: "/RNX2/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl, err := Parse(tt.tmpl)
			if err != nil {
				t.Fatalf("Parse(%q) 실패: %v", tt.tmpl, err)
			}

			if got := tpl.Expand(when); got != tt.want {
				t.Errorf("Expand() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestExpandLiteralRemotePath 는 (HH) 가 없는 템플릿이 When 과 무관하게
// 같은 경로를 내는지 본다. Hourly dir 소스 → flat 목적지 전송이
// RemotePath.Expand(c.When) 에서 오류 없이 한 폴더로 모인다.
func TestExpandLiteralRemotePath(t *testing.T) {
	tpl, err := Parse("/RNX2/")
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	hours := []int{0, 5, 6, 23}
	for _, h := range hours {
		when := utc(2026, time.September, 1, h)
		if got := tpl.Expand(when); got != "/RNX2/" {
			t.Errorf("Expand(hour=%d) = %q, want /RNX2/", h, got)
		}
	}
}

func TestExpandTokens(t *testing.T) {
	// 2026-01-05 09:00 UTC → DOY 005
	when := utc(2026, time.January, 5, 9)

	tests := []struct {
		token string
		want  string
	}{
		{TokenYYYY, "2026"},
		{TokenYY, "26"},
		{TokenDOY, "005"}, // 0채움
		{TokenMM, "01"},
		{TokenDD, "05"},
		{TokenHH, "09"},
	}

	for _, tt := range tests {
		t.Run(tt.token, func(t *testing.T) {
			tpl, err := Parse("(" + tt.token + ")")
			if err != nil {
				t.Fatalf("Parse 실패: %v", err)
			}

			if got := tpl.Expand(when); got != tt.want {
				t.Errorf("(%s) = %q, want %q", tt.token, got, tt.want)
			}
		})
	}
}

// DOY 는 연말·연초 경계에서 틀리기 쉽다.
func TestExpandDOYBoundaries(t *testing.T) {
	tpl, err := Parse("(YYYY)/(DOY)")
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{"연초", utc(2026, time.January, 1, 0), "2026/001"},
		{"평년 말일", utc(2026, time.December, 31, 23), "2026/365"},
		{"윤년 말일", utc(2024, time.December, 31, 0), "2024/366"},
		{"윤년 2월 29일", utc(2024, time.February, 29, 0), "2024/060"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tpl.Expand(tt.when); got != tt.want {
				t.Errorf("Expand() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 시각은 반드시 UTC 로 해석해야 한다.
//
// KST 기준으로 확장하면 하루 중 9시간(00:00~08:59 KST)이 전날 UTC 에 속해
// 엉뚱한 DOY 디렉터리를 보게 된다. 오류가 나지 않고 조용히 틀린다.
//
// 아래 시각은 연·월·일·시가 모두 달라지는 지점이라
// .UTC() 를 빠뜨리면 세 토큰이 동시에 틀린다.
func TestExpandForcesUTC(t *testing.T) {
	tpl, err := Parse("(YYYY)/(DOY)/(HH)")
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	kst := time.FixedZone("KST", 9*60*60)

	// 2026-07-01 08:30 KST == 2026-06-30 23:30 UTC (DOY 181)
	local := time.Date(2026, time.July, 1, 8, 30, 0, 0, kst)

	const want = "2026/181/23"

	if got := tpl.Expand(local); got != want {
		t.Errorf("Expand(KST) = %q, want %q (UTC 로 해석되지 않았다)", got, want)
	}
}

func TestParseRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "빈 문자열", in: ""},
		{name: "공백만", in: "   "},
		{name: "알 수 없는 토큰", in: `D:\(SITE)\`},
		{name: "오타 토큰", in: `D:\(DOI)\`},
		{name: "소문자 토큰", in: `D:\(yyyy)\`},
		{name: "앞뒤 공백이 섞인 토큰", in: `D:\( YYYY )\`},
		{name: "닫히지 않은 괄호", in: `D:\(YYYY\`},
		{name: "여는 괄호 누락", in: `D:\YYYY)\`},
		{name: "괄호가 포함된 디렉터리명", in: `D:\RINEX (백업)\(YYYY)\`},

		// 빈 토큰. isKnownToken("") 이 false 여야 걸린다.
		{name: "빈 토큰", in: `D:\()\`},

		// 중첩된 여는 괄호. name 이 "(YYYY" 가 되어 unknown 으로 걸린다.
		{name: "중첩된 여는 괄호", in: `D:\((YYYY)\`},

		// 정상 토큰을 처리한 뒤 남은 rest 에 짝 없는 ')' 가 있는 경우.
		// 루프 안의 checkLiteral 이 아니라 루프 종료 후 checkLiteral 이 잡는다.
		// 둘 중 하나만 있으면 뚫리는 경로이므로 고정한다.
		{name: "닫는 괄호가 하나 더", in: `D:\(YYYY))\`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.in)
			if !errors.Is(err, ErrInvalidTemplate) {
				t.Errorf("Parse(%q) = %v, %v; want ErrInvalidTemplate", tt.in, got, err)
			}
		})
	}
}

// 토큰이 없는 고정 경로도 유효하다.
func TestParseAcceptsLiteralOnly(t *testing.T) {
	const in = `D:\RINEX-V3-D\`

	tpl, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse(%q) 실패: %v", in, err)
	}

	if got := tpl.Expand(utc(2026, time.July, 1, 0)); got != in {
		t.Errorf("Expand() = %q, want %q", got, in)
	}
}

// config.ini 에 실수로 붙은 앞뒤 공백은 제거되어야 한다.
// 경로 중간에 있는 공백은 건드리지 않는다.
func TestParseTrimsSurroundingSpace(t *testing.T) {
	const (
		in   = `  D:\RINEX-V3-D\(YYYY)\(DOY)\  `
		want = `D:\RINEX-V3-D\2026\182\`
	)

	tpl, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse(%q) 실패: %v", in, err)
	}

	if got := tpl.String(); got != `D:\RINEX-V3-D\(YYYY)\(DOY)\` {
		t.Errorf("String() = %q, want trimmed template", got)
	}

	if got := tpl.Expand(utc(2026, time.July, 1, 0)); got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

// HasToken 은 템플릿에 토큰이 있는지만 답한다. Hourly LocalPath 의
// (HH) 필수 여부는 과도기 HourLayout 규칙이며 config 가 판정한다
// (GUIDELINES 9.3). Daily 경로의 (HH) 금지는 유지한다.
func TestHasToken(t *testing.T) {
	tests := []struct {
		name  string
		tmpl  string
		token string
		want  bool
	}{
		{
			name:  "Hourly 경로에 HH 가 있다",
			tmpl:  `D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\`,
			token: TokenHH,
			want:  true,
		},
		{
			name:  "Daily 경로에는 HH 가 없다",
			tmpl:  `D:\RINEX-V3-D\(YYYY)\(DOY)\`,
			token: TokenHH,
			want:  false,
		},
		{
			name:  "YYYY 확인",
			tmpl:  `D:\RINEX-V3-D\(YYYY)\(DOY)\`,
			token: TokenYYYY,
			want:  true,
		},
		{
			name:  "쓰지 않은 토큰",
			tmpl:  `D:\RINEX-V3-D\(YYYY)\(DOY)\`,
			token: TokenMM,
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl, err := Parse(tt.tmpl)
			if err != nil {
				t.Fatalf("Parse 실패: %v", err)
			}

			if got := tpl.HasToken(tt.token); got != tt.want {
				t.Errorf("HasToken(%q) = %v, want %v", tt.token, got, tt.want)
			}
		})
	}
}

func TestTemplateString(t *testing.T) {
	const in = `D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\`

	tpl, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	if got := tpl.String(); got != in {
		t.Errorf("String() = %q, want %q", got, in)
	}
}

// 같은 토큰이 여러 번 나와도 모두 치환되어야 한다.
func TestExpandRepeatedToken(t *testing.T) {
	tpl, err := Parse("(YYYY)/(DOY)/(YYYY)")
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	const want = "2026/182/2026"

	if got := tpl.Expand(utc(2026, time.July, 1, 0)); got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

// 토큰이 맞붙어 있으면 사이에 리터럴 조각이 생기지 않는다.
// Parse 의 `if open > 0` 분기가 이 경우를 담당한다.
func TestExpandAdjacentTokens(t *testing.T) {
	tpl, err := Parse("(YYYY)(DOY)")
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	const want = "2026182"

	if got := tpl.Expand(utc(2026, time.July, 1, 0)); got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

// Deep Scan 은 같은 템플릿을 Category 당 720회 확장한다.
// 확장이 Template 의 상태를 바꾸지 않는지 확인한다.
func TestExpandIsRepeatable(t *testing.T) {
	tpl, err := Parse(`D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	first := tpl.Expand(utc(2026, time.July, 1, 0))

	for range 10 {
		tpl.Expand(utc(2026, time.August, 25, 13))
	}

	if got := tpl.Expand(utc(2026, time.July, 1, 0)); got != first {
		t.Errorf("반복 확장 후 결과가 달라졌다: %q → %q", first, got)
	}
}

// Worker Pool 은 Category 당 하나의 *Template 을 공유한다.
//
// 지금은 Expand 가 segments 를 읽기만 하고 strings.Builder 가 지역 변수라
// 안전하지만, 나중에 Template 에 캐시 필드를 추가하면 조용히 깨진다.
// race 는 테스트 없이는 드러나지 않으므로 지금 고정한다.
//
//	go test ./internal/pathpl/ -race
func TestExpandIsConcurrentSafe(t *testing.T) {
	tpl, err := Parse(`D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	when := utc(2026, time.July, 1, 0)
	want := tpl.Expand(when)

	var wg sync.WaitGroup

	for range 8 {
		wg.Go(func() {
			for range 100 {
				if got := tpl.Expand(when); got != want {
					t.Errorf("Expand() = %q, want %q", got, want)
				}
			}
		})
	}

	wg.Wait()
}
