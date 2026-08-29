package config

import (
	"errors"
	"strings"
	"testing"
)

func TestParseINI_Basic(t *testing.T) {
	input := `
; full line comment
# another full line comment

[General]
Mode = put
MixedKey = Value
Equation = a=b=c
Empty =
Hash = abc#def
Semi = abc;def
Inline = value ; comment
InlineHash = value # comment
`

	f, err := parseINI(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseINI() unexpected error: %v", err)
	}

	sec, ok := f.section("GENERAL")
	if !ok {
		t.Fatal("section GENERAL not found")
	}

	// 섹션 조회는 대소문자를 구분하지 않는다.
	if _, ok := f.section("general"); !ok {
		t.Error("section lookup should be case-insensitive")
	}

	tests := []struct {
		key  string
		want string
	}{
		{"Mode", "put"},
		{"mixedkey", "Value"},
		{"Equation", "a=b=c"},
		{"Empty", ""},
		{"Hash", "abc#def"},
		{"Semi", "abc;def"},
		{"Inline", "value"},
		{"InlineHash", "value"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, ok := sec.get(tt.key)
			if !ok {
				t.Fatalf("get(%q) key not found", tt.key)
			}

			if got != tt.want {
				t.Errorf("get(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}

	// 빈 값과 키 없음은 구분되어야 한다.
	if got, ok := sec.get("Empty"); !ok || got != "" {
		t.Errorf("Empty = (%q, %v), want (\"\", true)", got, ok)
	}

	if _, ok := sec.get("Missing"); ok {
		t.Error("missing key returned ok=true")
	}
}

func TestParseINI_PreservesSectionNameAndKeyOrder(t *testing.T) {
	input := `[General]
Mode = put
MixedKey = value
`

	f, err := parseINI(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseINI() unexpected error: %v", err)
	}

	names := f.sectionNames()
	if len(names) != 1 {
		t.Fatalf("sectionNames() len = %d, want 1", len(names))
	}

	if names[0] != "General" {
		t.Errorf("section name = %q, want %q", names[0], "General")
	}

	sec, ok := f.section("GENERAL")
	if !ok {
		t.Fatal("section GENERAL not found")
	}

	// keys() 는 오류 메시지용으로 파일에 적힌 원래 표기를 돌려준다.
	// 조회용 fold 키와 섞지 않는다. 조회는 get 이 대소문자를 흡수한다.
	want := []string{"Mode", "MixedKey"}
	got := sec.keys()

	if len(got) != len(want) {
		t.Fatalf("keys() len = %d, want %d", len(got), len(want))
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("keys()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseINI_WindowsAndEOF(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "UTF-8 BOM",
			input: "\ufeff[GENERAL]\nMode = put\n",
		},
		{
			name:  "CRLF",
			input: "[GENERAL]\r\nMode = put\r\n",
		},
		{
			name:  "마지막 줄 개행 없음",
			input: "[GENERAL]\nMode = put",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, err := parseINI(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("parseINI() unexpected error: %v", err)
			}

			sec, ok := f.section("GENERAL")
			if !ok {
				t.Fatal("section GENERAL not found")
			}

			got, ok := sec.get("Mode")
			if !ok {
				t.Fatal("Mode key not found")
			}

			if got != "put" {
				t.Errorf("Mode = %q, want %q", got, "put")
			}
		})
	}
}

func TestParseINI_DuplicateSection(t *testing.T) {
	input := `[GENERAL]
[general]
`

	_, err := parseINI(strings.NewReader(input))
	if err == nil {
		t.Fatal("duplicate section: expected error")
	}

	if !errors.Is(err, ErrSyntax) {
		t.Errorf("error = %v, want ErrSyntax", err)
	}

	if !strings.Contains(err.Error(), "first at line 1") {
		t.Errorf("error should contain first section line: %v", err)
	}
}

func TestParseINI_DuplicateKey(t *testing.T) {
	input := `[GENERAL]
Mode = put
mode = both
`

	_, err := parseINI(strings.NewReader(input))
	if err == nil {
		t.Fatal("duplicate key: expected error")
	}

	if !errors.Is(err, ErrSyntax) {
		t.Errorf("error = %v, want ErrSyntax", err)
	}

	if !strings.Contains(err.Error(), "first at line 2") {
		t.Errorf("error should contain first key line: %v", err)
	}
}

func TestParseINI_SyntaxErrors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name:  "섹션 밖 키",
			input: "Mode = put\n",
		},
		{
			name: "등호 없는 줄",
			input: `[GENERAL]
Mode put
`,
		},
		{
			name: "빈 키",
			input: `[GENERAL]
= put
`,
		},
		{
			name:  "닫히지 않은 섹션",
			input: "[GENERAL\n",
		},
		{
			name:  "빈 섹션명",
			input: "[]\n",
		},
		{
			name:  "섹션명 내부 괄호",
			input: "[[GENERAL]]\n",
		},
		{
			name:  "섹션 헤더 뒤 군더더기",
			input: "[GENERAL] trailing\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseINI(strings.NewReader(tt.input))
			if err == nil {
				t.Fatal("expected syntax error")
			}

			if !errors.Is(err, ErrSyntax) {
				t.Errorf("error = %v, want ErrSyntax", err)
			}
		})
	}
}

func TestParseINI_LongLine(t *testing.T) {
	// maxLineBytes 를 넘는 줄은 config.ini 가 아니라고 보고 ErrSyntax 로 거부한다.
	// bufio 원문("token too long")은 운영자에게 의미가 없으므로 바꾼다.
	input := "[GENERAL]\nValue = " + strings.Repeat("a", 70*1024)

	_, err := parseINI(strings.NewReader(input))
	if err == nil {
		t.Fatal("long line: expected error")
	}

	if !errors.Is(err, ErrSyntax) {
		t.Errorf("error = %v, want ErrSyntax", err)
	}

	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("error should mention line size limit: %v", err)
	}
}

func TestIniSectionLineOf(t *testing.T) {
	input := `[GENERAL]
Mode = put
`

	f, err := parseINI(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parseINI() unexpected error: %v", err)
	}

	sec, ok := f.section("GENERAL")
	if !ok {
		t.Fatal("section GENERAL not found")
	}

	if got := sec.lineOf("Mode"); got != 2 {
		t.Errorf("lineOf(Mode) = %d, want 2", got)
	}

	// 없는 키는 섹션 헤더 줄을 가리킨다.
	if got := sec.lineOf("Missing"); got != 1 {
		t.Errorf("lineOf(Missing) = %d, want section line 1", got)
	}
}
