package domain

import (
	"fmt"
	"strings"
	"testing"
)

// 커밋 3 (SITE v1 / RESEND v4 §6.3) — site 추출·매칭의 domain 공통 규칙.
//
// CLI·put 필터는 이 커밋에서 연결하지 않으므로, 여기서는 두 함수의
// 계약만 고정한다: ParseSiteList(4자리 영숫자 정석, 복수·중복·대소문자,
// 9자리 거부)와 SiteFromName(파서가 확실한 이름에서만 추출, 유보/오류
// 이원 계약).

func TestParseSiteList_Valid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"단일", "DBON", []string{"DBON"}},
		{"네 번째 숫자 허용 (SUW1)", "SUW1", []string{"SUW1"}},
		{"소문자 입력", "ihwa", []string{"IHWA"}},
		{"모든 자리에 숫자 허용", "1234", []string{"1234"}},
		{"앞자리 숫자 허용", "1a2b", []string{"1A2B"}},
		{
			"복수 + 공백 + 대소문자 혼용",
			" dbon , SUW1,IHWA ",
			[]string{"DBON", "SUW1", "IHWA"},
		},
		{
			"중복 제거 (대소문자 무시, 첫 등장 순서 유지)",
			"DBON,dbon,SUW1,DbOn",
			[]string{"DBON", "SUW1"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseSiteList(tc.raw)
			if err != nil {
				t.Fatalf("ParseSiteList(%q) unexpected error: %v", tc.raw, err)
			}

			if len(got) != len(tc.want) {
				t.Fatalf("ParseSiteList(%q) = %v, want %v", tc.raw, got, tc.want)
			}

			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("ParseSiteList(%q)[%d] = %q, want %q",
						tc.raw, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// 숫자 접미사가 날짜 첫 자리와 혼동되지 않고 모든 지원 형식에서
// CLI 선택과 정확히 매칭되는지 확인한다. 같은 앞 4자리의 긴 ID는 함께 선택한다.
func TestSiteNumericSuffixAcrossFormats(t *testing.T) {
	for digit := 0; digit <= 9; digit++ {
		code := fmt.Sprintf("SUW%d", digit)
		selected, err := ParseSiteList(" DBON, " + strings.ToLower(code) + "," + code)
		if err != nil || len(selected) != 2 || selected[1] != code {
			t.Fatalf("selection = %v, err = %v", selected, err)
		}
		for _, cat := range Categories() {
			var names []string
			switch cat {
			case CategoryRINEX2Daily:
				names = []string{code + "2500.26o", code + "2500.26d"}
			case CategoryRINEX2Hourly:
				names = []string{code + "250a.26o", code + "250x.26d"}
			default:
				period := "01D"
				if cat == CategoryRINEX3Hourly || cat == CategoryRINEX4Hourly {
					period = "01H"
				}
				names = []string{
					code + "00KOR_R_20262500000_" + period + "_30S_MO.crx",
					code + "01KOR_R_20262500000_" + period + "_MN.rnx",
				}
			}
			for _, name := range names {
				for _, ext := range []string{"", ".gz", ".Z", ".zip"} {
					t.Run(string(cat)+"/"+name+ext, func(t *testing.T) {
						site, ok, err := SiteFromName(cat, NormalizeName(name+ext))
						if err != nil || !ok || site != selected[1] || site == selected[0] {
							t.Fatalf("site=%q ok=%v err=%v; want %q, not %q", site, ok, err, selected[1], selected[0])
						}
					})
				}
			}
		}
	}
}

func TestParseSiteList_Invalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"9자리 긴 식별자 거부", "DBON00KOR"},
		{"3자리", "DBO"},
		{"5자리", "DBONX"},
		{"와일드카드", "DB*N"},
		{"영숫자 외 문자", "DB-N"},
		{"빈 값", ""},
		{"공백만", "   "},
		{"빈 항목", "dbon,,suw1"},
		{"복수 중 하나가 9자리", "dbon,DBON00KOR"},
		{"뒤 쉼표", "dbon,"},
		{"앞 쉼표", ",dbon"},
		{"내부 공백", "SU 1"},
		{"접미 패턴", "SUW1*"},
		// 4룬 표기 "DBOK" 은 UTF-8 6바이트라 길이 검사에서 먼저 걸린다.
		{"켈빈 기호 4룬 (바이트 길이 초과)", "DBO\u212A"},
		// 1ASCII+켈빈은 4바이트라 길이 검사를 통과한다. ToUpper 전에
		// ASCII 를 보지 않으면 U+212A → "K" 로 접혀 통과한다.
		{"켈빈 기호 4바이트 (ASCII 검사 경로)", "A\u212A"},
		{"전각 숫자", "１２３４"},
		{"한글", "도봉관측"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := ParseSiteList(tc.raw); err == nil {
				t.Fatalf("ParseSiteList(%q) = %v, want error", tc.raw, got)
			}
		})
	}
}

func TestSiteFromName_MalformedStructure(t *testing.T) {
	for _, tc := range []struct {
		cat  Category
		name string
	}{
		{CategoryRINEX2Daily, ""},
		{CategoryRINEX2Daily, "SUW1"},
		{CategoryRINEX2Daily, "SUW1xx00.26o"},
		{CategoryRINEX2Hourly, "SUW1250z.26o"},
		{CategoryRINEX2Daily, "SUW12500.2xo"},
		{CategoryRINEX2Daily, "SUW12500.260"},
		{CategoryRINEX3Daily, "SUW1_R_20262500000_01D_MO.rnx"},
		{CategoryRINEX3Daily, "SUW100KOR_RR_20262500000_01D_MO.rnx"},
		{CategoryRINEX3Daily, "SUW100KOR_R_2026250000X_01D_MO.rnx"},
		{CategoryRINEX3Daily, "SUW100KOR_R_20262500000_BAD_MO.rnx"},
		{CategoryRINEX4Hourly, "SUW100KOR_R_20262500000_01H_BAD_MO.rnx"},
		{CategoryRINEX4Daily, "SUW100KOR_R_20262500000_01D_MO.txt"},
		{CategoryRINEX2Daily, "SUW1250a.26o"},
		{CategoryRINEX2Hourly, "SUW12500.26o"},
		{CategoryRINEX3Daily, "SUW100KOR_R_20262500000_01H_MN.rnx"},
		{CategoryRINEX3Hourly, "SUW100KOR_R_20262500000_01D_MO.rnx"},
		{CategoryRINEX3Daily, "DBON00KOR_R_20260010300_15M_01S_MS.rnx.gz"},
	} {
		t.Run(string(tc.cat)+"/"+tc.name, func(t *testing.T) {
			site, ok, err := SiteFromName(tc.cat, NormalizeName(tc.name))
			if err != nil || ok || site != "" {
				t.Fatalf("malformed name: site=%q ok=%v err=%v", site, ok, err)
			}
		})
	}
}

func TestSiteFromName_Short(t *testing.T) {
	cases := []struct {
		name     string
		cat      Category
		file     string // 원본 표기 — NormalizeName 을 거쳐 전달한다
		wantSite string
		wantOK   bool
	}{
		{
			"RINEX2 Daily 짧은 이름",
			CategoryRINEX2Daily,
			"DBON2500.26G.gz",
			"DBON", true,
		},
		{
			"압축 확장자 .Z (대문자) — NormalizeName 경유로 처리",
			CategoryRINEX2Daily,
			"SONP2500.26O.Z",
			"SONP", true,
		},
		{
			"RINEX2 Hourly + 네 번째 숫자 관측소",
			CategoryRINEX2Hourly,
			"SUW1250a.26o",
			"SUW1", true,
		},
		{
			"RINEX2 네 번째 숫자 + Hatanaka d + 대문자 .Z",
			CategoryRINEX2Daily,
			"SUW12500.26D.Z",
			"SUW1", true,
		},
		// parseShortName 은 ssss 문자를 검사하지 않는다. site 추출은
		// 영숫자를 따로 요구해 "식별 불가"로 유보해야 한다 — 그렇지 않으면
		// 불일치로 잘못 집계된다 (SITE §5).
		{
			"RINEX2 관측소 자리에 하이픈 — 유보",
			CategoryRINEX2Daily,
			"DB-N2500.26O",
			"", false,
		},
		{
			"RINEX2 관측소 자리에 밑줄 — 유보",
			CategoryRINEX2Hourly,
			"____250a.26o",
			"", false,
		},
		{
			"RINEX2 관측소 자리에 공백 — 유보",
			CategoryRINEX2Daily,
			"db n2500.26o",
			"", false,
		},
		{
			"7자 예외 이름(YONS060.20M) — 유보",
			CategoryRINEX2Daily,
			"YONS060.20M",
			"", false,
		},
		{
			"짧은 이름 규약 밖 — 유보",
			CategoryRINEX2Daily,
			"notarinexfile.dat",
			"", false,
		},
		{
			"긴 이름이 RINEX2 카테고리로 들어옴 — 유보",
			CategoryRINEX2Daily,
			"DBON00KOR_R_20262500000_01D_MO.crx.gz",
			"", false,
		},
		{
			"Hourly 세션이 Daily 카테고리 — 유보",
			CategoryRINEX2Daily,
			"SUW1250a.26O",
			"", false,
		},
		{
			"Daily 세션이 Hourly 카테고리 — 유보",
			CategoryRINEX2Hourly,
			"SUW12500.26O",
			"", false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site, ok, err := SiteFromName(tc.cat, NormalizeName(tc.file))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if ok != tc.wantOK || site != tc.wantSite {
				t.Fatalf("SiteFromName(%s, %q) = (%q, %v), want (%q, %v)",
					tc.cat, tc.file, site, ok, tc.wantSite, tc.wantOK)
			}
		})
	}
}

func TestSiteFromName_Long(t *testing.T) {
	cases := []struct {
		name     string
		cat      Category
		file     string
		wantSite string
		wantOK   bool
	}{
		{
			"RINEX3 6필드 (레이트 포함)",
			CategoryRINEX3Daily,
			"DBON00KOR_R_20262500000_01D_30S_MO.crx.gz",
			"DBON", true,
		},
		{
			"RINEX3 5필드 (레이트 없음) + 네 번째 숫자 관측소",
			CategoryRINEX3Hourly,
			"SUW100KOR_R_20262500000_01H_MN.rnx.gz",
			"SUW1", true,
		},
		{
			"긴 이름 + 압축 확장자 .Z",
			CategoryRINEX3Daily,
			"SONP00KOR_R_20262500000_01D_MO.crx.Z",
			"SONP", true,
		},
		{
			"RINEX4 도 같은 긴 이름 규칙",
			CategoryRINEX4Daily,
			"IHWA00KOR_R_20262500000_01D_MO.rnx.gz",
			"IHWA", true,
		},
		{
			"필드 수만 맞는 임의 이름 — 유보 (유령 site 방지)",
			CategoryRINEX3Daily,
			"backup_old_2026_temp_mo.rnx.gz",
			"", false,
		},
		{
			"짧은 이름이 RINEX3 카테고리로 들어옴 — 유보",
			CategoryRINEX3Daily,
			"DBON2500.26G.gz",
			"", false,
		},
		{
			"긴 이름 구조는 맞지만 Hourly 주기가 Daily 카테고리 — 유보",
			CategoryRINEX3Daily,
			"DBON00KOR_R_20262500000_01H_MO.crx.gz",
			"", false,
		},
		{
			"15m 주기 — MatchesName 유보 이름은 site 식별 성공이 아니다",
			CategoryRINEX3Daily,
			"DBON00KOR_R_20260010300_15M_01S_MS.rnx.gz",
			"", false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site, ok, err := SiteFromName(tc.cat, NormalizeName(tc.file))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if ok != tc.wantOK || site != tc.wantSite {
				t.Fatalf("SiteFromName(%s, %q) = (%q, %v), want (%q, %v)",
					tc.cat, tc.file, site, ok, tc.wantSite, tc.wantOK)
			}
		})
	}
}

// TestSiteFromName_UnknownCategoryIsError 는 라우팅 누락이 유보가 아니라
// 오류임을 고정한다 (SetKeyKind 의 err/유보 이원 계약과 동일).
// 조용한 식별 불가로 접으면 새 카테고리의 site 필터가 전부 제외되는
// 침묵 실패가 된다.
func TestSiteFromName_UnknownCategoryIsError(t *testing.T) {
	_, ok, err := SiteFromName(Category("BOGUS"), "dbon2500.26g.gz")
	if err == nil {
		t.Fatal("미지원 카테고리인데 오류가 없다")
	}

	if ok {
		t.Fatal("오류와 함께 ok=true 를 반환했다")
	}

	if !strings.Contains(err.Error(), "BOGUS") {
		t.Errorf("오류에 카테고리가 없다: %v", err)
	}
}

// TestSiteFromName_MatchContract 는 매칭 계약(정확 일치, 대문자 통일)을
// 양쪽 함수에 걸쳐 고정한다 — CLI 입력과 파일 추출이 같은 정규화를
// 거치므로 호출자는 문자열 동등 비교만 하면 된다.
func TestSiteFromName_MatchContract(t *testing.T) {
	sites, err := ParseSiteList("DBON")
	if err != nil {
		t.Fatal(err)
	}

	site, ok, err := SiteFromName(
		CategoryRINEX3Daily,
		NormalizeName("DBON00KOR_R_20262500000_01D_MO.crx.gz"),
	)
	if err != nil || !ok {
		t.Fatalf("추출 실패: site=%q ok=%v err=%v", site, ok, err)
	}

	if sites[0] != site {
		t.Fatalf("CLI 정규화(%q)와 파일 추출(%q)이 일치하지 않는다",
			sites[0], site)
	}
}
