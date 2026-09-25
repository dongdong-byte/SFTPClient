package pathpl

import (
	"strings"
	"testing"
	"time"
)

// 경로 토큰 커밋 4 — 템플릿의 폴더 단위 분해와 패턴 단 대조.
// scan(커밋 5)이 아직 부르지 않으므로 여기서 계약만 고정한다.

// assemble 은 Step 문서의 경로 재구성 규칙을 그대로 따른다.
// 패턴 단에는 names 에서 차례로 하나씩 넣는다.
func assemble(t *testing.T, steps []Step, names ...string) string {
	t.Helper()

	var b strings.Builder
	n := 0
	for _, s := range steps {
		if s.IsPattern() {
			if n >= len(names) {
				t.Fatalf("패턴 단이 %d개보다 많다", len(names))
			}
			b.WriteString(names[n])
			n++
			continue
		}
		b.WriteString(s.Fixed)
	}
	if n != len(names) {
		t.Fatalf("패턴 단 %d개, 이름 %d개", n, len(names))
	}

	return b.String()
}

func mustParse(t *testing.T, tmpl string) *Template {
	t.Helper()
	tpl, err := Parse(tmpl)
	if err != nil {
		t.Fatalf("Parse(%q) 실패: %v", tmpl, err)
	}
	return tpl
}

// 파일 토큰이 없는 템플릿은 고정 단 하나이고 Expand 와 같다 (I2).
// scan 이 이 경우 지금과 완전히 같은 경로를 만드는 근거다.
func TestStepsWithoutFileTokensEqualsExpand(t *testing.T) {
	when := utc(2026, time.September, 7, 0)

	for _, tmpl := range []string{
		`D:\RINEX-V3-D\(YYYY)\(DOY)\`,
		`D:\RINEX-V3-H\(YYYY)\(DOY)`,
		`/remote/rinex2/hourly/(YYYY)/(DOY)/`,
		`/occ/data/gps/temp/(YYYY)/(MM)(DD)/`,
		`/RNX/`,
		`D:\`,
		`relative/(YY)/(DOY)/`,
		`(YYYY)`,
	} {
		t.Run(tmpl, func(t *testing.T) {
			tpl := mustParse(t, tmpl)
			steps := tpl.Steps(when)

			if len(steps) != 1 || steps[0].IsPattern() {
				t.Fatalf("Steps() = %+v; want 고정 단 하나", steps)
			}
			if got, want := steps[0].Fixed, tpl.Expand(when); got != want {
				t.Errorf("Fixed = %q, want Expand = %q", got, want)
			}
		})
	}
}

// 커밋 계획 §1 의 네 가지 모양.
func TestStepsShapes(t *testing.T) {
	// 2026년 9월 7일은 DOY 250 이다.
	when := utc(2026, time.September, 7, 0)

	tests := []struct {
		name      string
		tmpl      string
		fixed     []string // 고정 단의 Fixed. 패턴 단 자리는 ""
		names     []string // 패턴 단에 넣을 폴더 이름
		wantPath  string
		wantField FileFields
	}{
		{
			name:      "관측소가 날짜 위",
			tmpl:      `/data/(SITE)/(YYYY)/(DOY)/`,
			fixed:     []string{"/data/", "", "/2026/250/"},
			names:     []string{"DBON"},
			wantPath:  "/data/DBON/2026/250/",
			wantField: FileFields{Site: "DBON"},
		},
		{
			name:      "관측소가 날짜 아래",
			tmpl:      `/data/(YYYY)/(DOY)/(SITE)/`,
			fixed:     []string{"/data/2026/250/", "", "/"},
			names:     []string{"DBON"},
			wantPath:  "/data/2026/250/DBON/",
			wantField: FileFields{Site: "DBON"},
		},
		{
			name:      "폴더 이름 중간, 월일 표기",
			tmpl:      `/data/rnx_(SITE)/(YYYY)/(MM)(DD)/`,
			fixed:     []string{"/data/", "", "/2026/0907/"},
			names:     []string{"rnx_DBON"},
			wantPath:  "/data/rnx_DBON/2026/0907/",
			wantField: FileFields{Site: "DBON"},
		},
		{
			name:      "서울시 옛 설정: 시각 폴더",
			tmpl:      `C:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\`,
			fixed:     []string{`C:\RINEX-V2-H\2026\250\`, "", `\`},
			names:     []string{"13"},
			wantPath:  `C:\RINEX-V2-H\2026\250\13\`,
			wantField: FileFields{Hour: "13"},
		},
		{
			name:      "패턴 단 둘",
			tmpl:      `/data/(SITE)/(YYYY)/(DOY)/(HH)/`,
			fixed:     []string{"/data/", "", "/2026/250/", "", "/"},
			names:     []string{"DBON", "13"},
			wantPath:  "/data/DBON/2026/250/13/",
			wantField: FileFields{Site: "DBON", Hour: "13"},
		},
		{
			name:      "붙여 쓴 토큰",
			tmpl:      `D:\RNX\(SITE)(HH)\`,
			fixed:     []string{`D:\RNX\`, "", `\`},
			names:     []string{"DBON13"},
			wantPath:  `D:\RNX\DBON13\`,
			wantField: FileFields{Site: "DBON", Hour: "13"},
		},
		{
			name:      "날짜 토큰과 같은 폴더",
			tmpl:      `/data/(DOY)_(SITE)/`,
			fixed:     []string{"/data/", "", "/"},
			names:     []string{"250_DBON"},
			wantPath:  "/data/250_DBON/",
			wantField: FileFields{Site: "DBON"},
		},
		{
			name:      "끝에 구분자 없음",
			tmpl:      `/data/(YYYY)/(DOY)/(SITE)`,
			fixed:     []string{"/data/2026/250/", ""},
			names:     []string{"DBON"},
			wantPath:  "/data/2026/250/DBON",
			wantField: FileFields{Site: "DBON"},
		},
		{
			name:      "첫 폴더가 패턴 (상대 경로)",
			tmpl:      `(SITE)/(YYYY)/`,
			fixed:     []string{"", "/2026/"},
			names:     []string{"DBON"},
			wantPath:  "DBON/2026/",
			wantField: FileFields{Site: "DBON"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := mustParse(t, tt.tmpl)
			steps := tpl.Steps(when)

			if len(steps) != len(tt.fixed) {
				t.Fatalf("단 수 = %d, want %d: %+v", len(steps), len(tt.fixed), steps)
			}
			for i, s := range steps {
				if tt.fixed[i] == "" {
					if !s.IsPattern() {
						t.Errorf("단 %d: 패턴 단이어야 한다: %+v", i, s)
					}
					continue
				}
				if s.IsPattern() || s.Fixed != tt.fixed[i] {
					t.Errorf("단 %d: Fixed = %q, want %q", i, s.Fixed, tt.fixed[i])
				}
			}

			// 패턴 단마다 이름을 대조하고 값을 합친다.
			var merged FileFields
			n := 0
			for _, s := range steps {
				if !s.IsPattern() {
					continue
				}
				f, ok := s.Match(tt.names[n])
				if !ok {
					t.Fatalf("Match(%q) = false", tt.names[n])
				}
				m, ok := merged.Merge(f)
				if !ok {
					t.Fatalf("Merge 충돌: %+v + %+v", merged, f)
				}
				merged = m
				n++
			}
			if merged != tt.wantField {
				t.Errorf("뽑힌 값 = %+v, want %+v", merged, tt.wantField)
			}

			if got := assemble(t, steps, tt.names...); got != tt.wantPath {
				t.Errorf("재구성 = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

// 대조 규칙: 대소문자, 폭, 범위, 남는 글자.
func TestStepMatch(t *testing.T) {
	when := utc(2026, time.September, 7, 0)

	pattern := func(tmpl string) Step {
		t.Helper()
		for _, s := range mustParse(t, tmpl).Steps(when) {
			if s.IsPattern() {
				return s
			}
		}
		t.Fatalf("%q 에 패턴 단이 없다", tmpl)
		return Step{}
	}

	site := pattern(`/data/(SITE)/`)
	hour := pattern(`/data/(HH)/`)
	mid := pattern(`/data/rnx_(SITE)_v2/`)
	both := pattern(`/data/(SITE)(HH)/`)
	dated := pattern(`/data/(DOY)_(SITE)/`)
	twice := pattern(`/data/(SITE)_(SITE)/`)

	tests := []struct {
		name   string
		step   Step
		in     string
		want   FileFields
		wantOK bool
	}{
		{"SITE 대문자", site, "DBON", FileFields{Site: "DBON"}, true},
		{"SITE 소문자 → 대문자 정규화 (R2)", site, "dbon", FileFields{Site: "DBON"}, true},
		{"SITE 혼합", site, "DbOn", FileFields{Site: "DBON"}, true},
		{"SITE 숫자 포함", site, "SUW1", FileFields{Site: "SUW1"}, true},
		{"SITE 3자리", site, "DBO", FileFields{}, false},
		{"SITE 5자리", site, "DBON1", FileFields{}, false},
		{"SITE 기호", site, "DB-N", FileFields{}, false},
		{"SITE 빈 이름", site, "", FileFields{}, false},
		{"패턴 밖 폴더 (D3)", site, "backup", FileFields{}, false},
		{"구분자가 든 이름", site, "DB/N", FileFields{}, false},

		{"HH 00", hour, "00", FileFields{Hour: "00"}, true},
		{"HH 23", hour, "23", FileFields{Hour: "23"}, true},
		{"HH 24", hour, "24", FileFields{}, false},
		{"HH 한 자리", hour, "1", FileFields{}, false},
		{"HH 세 자리", hour, "013", FileFields{}, false},
		{"HH 글자", hour, "ab", FileFields{}, false},

		{"글자 조각 정확 일치", mid, "rnx_DBON_v2", FileFields{Site: "DBON"}, true},
		{"글자 조각은 대소문자 구분", mid, "RNX_DBON_v2", FileFields{}, false},
		{"글자 조각 누락", mid, "rnx_DBON", FileFields{}, false},
		{"글자 조각 초과", mid, "rnx_DBON_v2x", FileFields{}, false},

		{"붙여 쓴 토큰", both, "DBON13", FileFields{Site: "DBON", Hour: "13"}, true},
		{"붙여 쓴 토큰, HH 범위 밖", both, "DBON99", FileFields{}, false},
		{"붙여 쓴 토큰, 짧음", both, "DBON1", FileFields{}, false},

		{"날짜 조각은 when 값과 정확 일치", dated, "250_DBON", FileFields{Site: "DBON"}, true},
		{"날짜 조각 불일치 (다른 날)", dated, "251_DBON", FileFields{}, false},

		{"같은 토큰 두 번, 같은 값", twice, "DBON_DBON", FileFields{Site: "DBON"}, true},
		{"같은 토큰 두 번, 대소문자만 다름", twice, "DBON_dbon", FileFields{Site: "DBON"}, true},
		{"같은 토큰 두 번, 다른 값", twice, "DBON_SUW1", FileFields{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.step.Match(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("Match(%q) ok = %v, want %v", tt.in, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("Match(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// 고정 단에서 Match 는 항상 false 다.
func TestStepMatchOnFixedStep(t *testing.T) {
	steps := mustParse(t, `/data/(YYYY)/`).Steps(utc(2026, time.September, 7, 0))
	if _, ok := steps[0].Match("2026"); ok {
		t.Error("고정 단의 Match 는 false 여야 한다")
	}
}

// 단 사이의 일관성은 Merge 가 잡는다: "/(SITE)/.../(SITE)/" 에서 DBON 아래
// SUW1 로 내려가면 안 된다.
func TestFileFieldsMerge(t *testing.T) {
	tests := []struct {
		name   string
		a, b   FileFields
		want   FileFields
		wantOK bool
	}{
		{"빈 것끼리", FileFields{}, FileFields{}, FileFields{}, true},
		{"한쪽만", FileFields{Site: "DBON"}, FileFields{}, FileFields{Site: "DBON"}, true},
		{"서로 다른 필드", FileFields{Site: "DBON"}, FileFields{Hour: "13"}, FileFields{Site: "DBON", Hour: "13"}, true},
		{"같은 값", FileFields{Site: "DBON"}, FileFields{Site: "DBON"}, FileFields{Site: "DBON"}, true},
		{"Site 충돌", FileFields{Site: "DBON"}, FileFields{Site: "SUW1"}, FileFields{}, false},
		{"Hour 충돌", FileFields{Hour: "13"}, FileFields{Hour: "14"}, FileFields{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.a.Merge(tt.b)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("Merge = %+v, %v; want %+v, %v", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

// 날짜 토큰의 UTC 규칙은 Steps 에도 그대로 적용된다.
func TestStepsForcesUTC(t *testing.T) {
	kst := time.FixedZone("KST", 9*60*60)
	// KST 2026-09-08 03:00 = UTC 2026-09-07 18:00 → DOY 250 이어야 한다.
	when := time.Date(2026, time.September, 8, 3, 0, 0, 0, kst)

	steps := mustParse(t, `/data/(SITE)/(YYYY)/(DOY)/`).Steps(when)
	if got := assemble(t, steps, "DBON"); got != "/data/DBON/2026/250/" {
		t.Errorf("재구성 = %q", got)
	}
}

// 패턴 단의 Match 는 뽑힌 값을 ExpandFile 에 그대로 넣을 수 있어야 한다.
// 원본에서 찾은 관측소로 목적지를 채우는 흐름(중계 구성)의 연결점이다.
func TestStepMatchFeedsExpandFile(t *testing.T) {
	when := utc(2026, time.September, 7, 0)

	src := mustParse(t, `/remote/(SITE)/(YYYY)/(DOY)/`)
	dst := mustParse(t, `D:\RNX\(YYYY)\(DOY)\(SITE)\`)

	var found FileFields
	for _, s := range src.Steps(when) {
		if !s.IsPattern() {
			continue
		}
		f, ok := s.Match("dbon")
		if !ok {
			t.Fatal("Match 실패")
		}
		found = f
	}

	got, err := dst.ExpandFile(when, found)
	if err != nil {
		t.Fatalf("ExpandFile 실패: %v", err)
	}
	if want := `D:\RNX\2026\250\DBON\`; got != want {
		t.Errorf("ExpandFile = %q, want %q", got, want)
	}
}

func TestStepsIsRepeatable(t *testing.T) {
	tpl := mustParse(t, `/data/(SITE)/(YYYY)/(DOY)/`)
	when := utc(2026, time.September, 7, 0)

	first := assemble(t, tpl.Steps(when), "DBON")
	for i := 0; i < 3; i++ {
		if got := assemble(t, tpl.Steps(when), "DBON"); got != first {
			t.Fatalf("%d번째 호출 = %q, 처음 = %q", i+2, got, first)
		}
	}
}
