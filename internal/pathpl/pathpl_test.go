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
			name: "RINEX3 Hourly (날짜 폴더까지, 아래는 재귀)",
			tmpl: `D:\RINEX-V3-H\(YYYY)\(DOY)\`,
			want: `D:\RINEX-V3-H\2026\182\`,
		},
		{
			name: "원격 POSIX 경로",
			tmpl: "/RNXOutgoing/(YYYY)/(DOY)/",
			want: "/RNXOutgoing/2026/182/",
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
// 아래 시각은 연·일이 함께 달라지는 지점이라
// .UTC() 를 빠뜨리면 두 토큰이 동시에 틀린다.
func TestExpandForcesUTC(t *testing.T) {
	tpl, err := Parse("(YYYY)/(DOY)")
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	kst := time.FixedZone("KST", 9*60*60)

	// 2026-07-01 08:30 KST == 2026-06-30 23:30 UTC (DOY 181)
	local := time.Date(2026, time.July, 1, 8, 30, 0, 0, kst)

	const want = "2026/181"

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
		{name: "알 수 없는 토큰", in: `D:\(STATION)\`},
		{name: "오타 토큰", in: `D:\(DOI)\`},

		// (SITE)·(HH) 는 경로 토큰 커밋 3 부터 pathpl 이 아는 토큰이다.
		// PUT LocalPath 의 (HH) 는 커밋 6 부터 허용이다 (D1).
		// PUT RemotePath 의 (SITE)·(HH) 는 config 가 거부한다
		// (putRemoteForbiddenTokens, TestMapConfig_PutRemotePathFileTokensRejected).
		{name: "소문자 토큰", in: `D:\(yyyy)\`},
		{name: "소문자 파일 토큰", in: `D:\(site)\`},
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

// HasToken 은 템플릿에 토큰이 있는지만 답한다.
// 토큰 존재에 의미를 부여하는 것은 호출자다.
func TestHasToken(t *testing.T) {
	tests := []struct {
		name  string
		tmpl  string
		token string
		want  bool
	}{
		{
			name:  "DOY 확인",
			tmpl:  `D:\RINEX-V3-D\(YYYY)\(DOY)\`,
			token: TokenDOY,
			want:  true,
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
	const in = `D:\RINEX-V3-H\(YYYY)\(DOY)\`

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
	tpl, err := Parse(`D:\RINEX-V3-H\(YYYY)\(DOY)\`)
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
	tpl, err := Parse(`D:\RINEX-V3-H\(YYYY)\(DOY)\`)
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

// ---- 경로 토큰 커밋 3: 파일 토큰 (SITE)·(HH) ----

// 파일 토큰이 있는 템플릿도 Parse 는 받는다. 어느 역할에서 허용하는지는
// config 정책이다.
func TestParseAcceptsFileTokens(t *testing.T) {
	for _, in := range []string{
		`D:\RNX\(YYYY)\(DOY)\(SITE)\`,
		`D:\RNX\(YYYY)\(MM)(DD)\(SITE)\`,
		`/rnx/(SITE)/(YYYY)/(DOY)/(HH)/`,
		`D:\RNX\(SITE)(HH)\`,
		`D:\RNX\st(SITE)\`,
	} {
		if _, err := Parse(in); err != nil {
			t.Errorf("Parse(%q) = %v; want nil", in, err)
		}
	}
}

func TestFileTokens(t *testing.T) {
	tests := []struct {
		tmpl string
		want []string
	}{
		{`D:\RINEX-V3-D\(YYYY)\(DOY)\`, nil},
		{`D:\RNX\(YYYY)\(DOY)\(SITE)\`, []string{TokenSITE}},
		{`/rnx/(SITE)/(YYYY)/(DOY)/(HH)/`, []string{TokenSITE, TokenHH}},
		{`/rnx/(HH)/(SITE)/`, []string{TokenHH, TokenSITE}},
		{`/rnx/(SITE)/(SITE)/`, []string{TokenSITE}}, // 중복은 한 번
	}

	for _, tt := range tests {
		t.Run(tt.tmpl, func(t *testing.T) {
			tpl, err := Parse(tt.tmpl)
			if err != nil {
				t.Fatalf("Parse 실패: %v", err)
			}

			got := tpl.FileTokens()
			if len(got) != len(tt.want) {
				t.Fatalf("FileTokens() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("FileTokens() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestHasTokenFileTokens(t *testing.T) {
	tpl, err := Parse(`D:\RNX\(YYYY)\(DOY)\(SITE)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	if !tpl.HasToken(TokenSITE) {
		t.Error("HasToken(SITE) = false, want true")
	}
	if tpl.HasToken(TokenHH) {
		t.Error("HasToken(HH) = true, want false")
	}
}

// 실제 목적지 형태로 확인한다.
//
//	측위원        D:\RNX\(YYYY)\(DOY)\(SITE)\
//	지질자원연구원 형태  D:\RNX\(YYYY)\(MM)(DD)\(SITE)\  (DOY 없이 월일)
//	시각 폴더 쓰는 현장  .../(SITE)/(HH)/
func TestExpandFileRealPaths(t *testing.T) {
	// 2026년 9월 7일은 DOY 250 이다.
	when := utc(2026, time.September, 7, 0)
	fields := FileFields{Site: "DBON", Hour: "13"}

	tests := []struct {
		name string
		tmpl string
		want string
	}{
		{
			name: "측위원: 관측소 폴더",
			tmpl: `D:\RNX\(YYYY)\(DOY)\(SITE)\`,
			want: `D:\RNX\2026\250\DBON\`,
		},
		{
			name: "지질자원연구원 형태: (MM)(DD) 붙여 쓰기 + 관측소",
			tmpl: `D:\RNX\(YYYY)\(MM)(DD)\(SITE)\`,
			want: `D:\RNX\2026\0907\DBON\`,
		},
		{
			name: "관측소가 날짜 앞에",
			tmpl: `/rnx/(SITE)/(YYYY)/(DOY)/`,
			want: `/rnx/DBON/2026/250/`,
		},
		{
			name: "시각 폴더",
			tmpl: `D:\RNX\(YYYY)\(DOY)\(SITE)\(HH)\`,
			want: `D:\RNX\2026\250\DBON\13\`,
		},
		{
			name: "파일 토큰 붙여 쓰기",
			tmpl: `D:\RNX\(SITE)(HH)\`,
			want: `D:\RNX\DBON13\`,
		},
		{
			name: "폴더 이름 중간",
			tmpl: `D:\RNX\st_(SITE)_rnx\`,
			want: `D:\RNX\st_DBON_rnx\`,
		},
		{
			name: "파일 토큰 없는 템플릿은 Expand 와 같다",
			tmpl: `D:\RINEX-V3-D\(YYYY)\(DOY)\`,
			want: `D:\RINEX-V3-D\2026\250\`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl, err := Parse(tt.tmpl)
			if err != nil {
				t.Fatalf("Parse 실패: %v", err)
			}

			got, err := tpl.ExpandFile(when, fields)
			if err != nil {
				t.Fatalf("ExpandFile 실패: %v", err)
			}
			if got != tt.want {
				t.Errorf("ExpandFile() = %q, want %q", got, tt.want)
			}
		})
	}
}

// 템플릿에 없는 토큰의 필드는 비어 있어도 된다 — Daily 카테고리는 Hour 를
// 채울 수 없지만 목적지에 (HH) 가 없으면 정상이다.
func TestExpandFileIgnoresUnusedFields(t *testing.T) {
	tpl, err := Parse(`D:\RNX\(YYYY)\(DOY)\(SITE)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	got, err := tpl.ExpandFile(utc(2026, time.September, 7, 0), FileFields{Site: "DBON"})
	if err != nil {
		t.Fatalf("ExpandFile 실패: %v", err)
	}
	if want := `D:\RNX\2026\250\DBON\`; got != want {
		t.Errorf("ExpandFile() = %q, want %q", got, want)
	}
}

// 요구하는 필드가 비면 부분 경로 없이 오류다.
func TestExpandFileMissingField(t *testing.T) {
	when := utc(2026, time.September, 7, 0)

	tests := []struct {
		name   string
		tmpl   string
		fields FileFields
	}{
		{"SITE 누락", `D:\RNX\(YYYY)\(DOY)\(SITE)\`, FileFields{}},
		{"HH 누락", `D:\RNX\(YYYY)\(DOY)\(SITE)\(HH)\`, FileFields{Site: "DBON"}},
		{"둘 다 누락", `D:\RNX\(SITE)\(HH)\`, FileFields{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl, err := Parse(tt.tmpl)
			if err != nil {
				t.Fatalf("Parse 실패: %v", err)
			}

			got, err := tpl.ExpandFile(when, tt.fields)
			if !errors.Is(err, ErrMissingFileField) {
				t.Fatalf("ExpandFile() = %q, %v; want ErrMissingFileField", got, err)
			}
			if got != "" {
				t.Errorf("오류 시 부분 경로를 돌려주면 안 된다: %q", got)
			}
		})
	}
}

// 목적지 경로를 만드는 마지막 계층에서도 파일 필드 계약을 검사한다.
// 특히 "."·".."과 경로 구분자는 상위/하위 폴더로 경로를 바꿀 수 있다.
func TestExpandFileRejectsBadFields(t *testing.T) {
	when := utc(2026, time.September, 7, 0)

	tests := []struct {
		name   string
		tmpl   string
		fields FileFields
	}{
		{"SITE 짧음", `D:\RNX\(SITE)\`, FileFields{Site: "ABC"}},
		{"SITE 김", `D:\RNX\(SITE)\`, FileFields{Site: "ABCDE"}},
		{"SITE 점", `D:\RNX\(SITE)\`, FileFields{Site: "."}},
		{"SITE 상위 폴더", `D:\RNX\(SITE)\`, FileFields{Site: ".."}},
		{"SITE 슬래시", `D:\RNX\(SITE)\`, FileFields{Site: `DB/ON`}},
		{"SITE 역슬래시", `D:\RNX\(SITE)\`, FileFields{Site: `DB\ON`}},
		{"SITE 기호", `D:\RNX\(SITE)\`, FileFields{Site: "DB-N"}},
		{"SITE 비ASCII", `D:\RNX\(SITE)\`, FileFields{Site: "관측소1"}},
		{"HH 한 자리", `D:\RNX\(HH)\`, FileFields{Hour: "7"}},
		{"HH 범위 초과", `D:\RNX\(HH)\`, FileFields{Hour: "24"}},
		{"HH 문자", `D:\RNX\(HH)\`, FileFields{Hour: "ab"}},
		{"HH 경로 구분자", `D:\RNX\(HH)\`, FileFields{Hour: `0/`}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl, err := Parse(tt.tmpl)
			if err != nil {
				t.Fatalf("Parse 실패: %v", err)
			}

			got, err := tpl.ExpandFile(when, tt.fields)
			if !errors.Is(err, ErrBadFileField) {
				t.Errorf("ExpandFile() = %q, %v; want ErrBadFileField", got, err)
			}
			if got != "" {
				t.Errorf("오류 시 부분 경로를 돌려주면 안 된다: %q", got)
			}
		})
	}
}

// Expand(when) 은 파일 토큰을 채울 수 없다. 빈 문자열로 접지는 않지만,
// 이 결과를 일반 Scanner에 넘기면 파일 0개로 조용히 끝날 수 있다.
// PATH v4 원본 패턴 탐색기만 원문 토큰을 해석해야 한다.
func TestExpandKeepsFileTokensLiteral(t *testing.T) {
	tpl, err := Parse(`D:\RNX\(YYYY)\(DOY)\(SITE)\(HH)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	got := tpl.Expand(utc(2026, time.September, 7, 0))
	if want := `D:\RNX\2026\250\(SITE)\(HH)\`; got != want {
		t.Errorf("Expand() = %q, want %q", got, want)
	}
}

// 날짜 토큰의 UTC 규칙은 ExpandFile 에도 그대로 적용된다.
func TestExpandFileForcesUTC(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	// KST 2026-09-08 03:00 = UTC 2026-09-07 18:00 → DOY 250 이어야 한다.
	when := time.Date(2026, time.September, 8, 3, 0, 0, 0, kst)

	tpl, err := Parse(`D:\RNX\(YYYY)\(DOY)\(SITE)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	got, err := tpl.ExpandFile(when, FileFields{Site: "DBON"})
	if err != nil {
		t.Fatalf("ExpandFile 실패: %v", err)
	}
	if want := `D:\RNX\2026\250\DBON\`; got != want {
		t.Errorf("ExpandFile() = %q, want %q", got, want)
	}
}

func TestExpandFileIsConcurrentSafe(t *testing.T) {
	tpl, err := Parse(`D:\RNX\(YYYY)\(DOY)\(SITE)\`)
	if err != nil {
		t.Fatalf("Parse 실패: %v", err)
	}

	when := utc(2026, time.September, 7, 0)
	sites := []string{"DBON", "SUW1", "IHWA", "SONP"}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			site := sites[i%len(sites)]
			got, err := tpl.ExpandFile(when, FileFields{Site: site})
			if err != nil || got != `D:\RNX\2026\250\`+site+`\` {
				t.Errorf("ExpandFile(%s) = %q, %v", site, got, err)
			}
		}(i)
	}
	wg.Wait()
}
