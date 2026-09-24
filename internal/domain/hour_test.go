package domain

import (
	"fmt"
	"strings"
	"testing"
)

// 경로 토큰 커밋 1 (DOWNLOAD v3 §13-1) — 파일명에서 시각(HH) 추출의 domain 규칙.
//
// 아직 호출자가 없다. 여기서는 HourFromName 의 계약만 고정한다:
// 파서가 확실한 Hourly 이름에서만 추출, Daily 는 항상 유보,
// 라우팅 누락은 err 라는 SiteFromName 과 같은 이원 계약.

func TestHourFromName_Short(t *testing.T) {
	cases := []struct {
		name   string
		file   string // 원본 표기 — NormalizeName 을 거쳐 전달한다
		wantHH string
	}{
		{"세션 a → 00", "SUW1250a.26o", "00"},
		{"세션 b → 01", "SUW1250b.26o", "01"},
		{"세션 m → 12", "DBON250m.26G", "12"},
		{"세션 x → 23 (마지막 세션)", "SUW1250x.26o", "23"},
		{"압축 확장자 .gz — NormalizeName 경유", "SUW1250k.26o.gz", "10"},
		{"압축 확장자 .Z (대문자)", "SONP250c.26O.Z", "02"},
		{"대문자 세션 문자 — NormalizeName 이 소문자로", "SUW1250X.26O", "23"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hh, ok, err := HourFromName(CategoryRINEX2Hourly, NormalizeName(tc.file))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ok {
				t.Fatalf("expected ok=true for %q", tc.file)
			}
			if hh != tc.wantHH {
				t.Fatalf("hh=%q, want %q", hh, tc.wantHH)
			}
		})
	}
}

// 세션 문자 'a'~'x' 24개 전수 — 대표값 몇 개만 보면 오프셋이 한 칸
// 밀린 구현(예: 'a'→"01")도 경계값이 우연히 맞으면 통과할 수 있다.
func TestHourFromName_ShortAllSessions(t *testing.T) {
	for i := 0; i < 24; i++ {
		session := byte('a' + i)
		name := NormalizeName("SUW1250" + string(session) + ".26o")
		want := fmt.Sprintf("%02d", i)

		hh, ok, err := HourFromName(CategoryRINEX2Hourly, name)
		if err != nil || !ok || hh != want {
			t.Fatalf("session %q: hh=%q ok=%v err=%v, want %q",
				session, hh, ok, err, want)
		}
	}
}

func TestHourFromName_Long(t *testing.T) {
	cases := []struct {
		name   string
		cat    Category
		file   string
		wantHH string
	}{
		{
			"RINEX3 Hourly 5필드, HH=00",
			CategoryRINEX3Hourly,
			"SUW100KOR_R_20262500000_01H_MO.rnx",
			"00",
		},
		{
			"RINEX3 Hourly 6필드(레이트 포함) + .gz, HH=13",
			CategoryRINEX3Hourly,
			"DBON00KOR_R_20260011300_01H_30S_MO.rnx.gz",
			"13",
		},
		{
			"RINEX3 Hourly HH=23",
			CategoryRINEX3Hourly,
			"SUW100KOR_R_20262502300_01H_MN.rnx",
			"23",
		},
		{
			"RINEX4 Hourly 도 같은 규칙",
			CategoryRINEX4Hourly,
			"SUW100KOR_S_20262500700_01H_MO.rnx",
			"07",
		},
		{
			"분(MM) 필드는 무시 — HH 만 본다",
			CategoryRINEX3Hourly,
			"SUW100KOR_R_20262500945_01H_MO.rnx",
			"09",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hh, ok, err := HourFromName(tc.cat, NormalizeName(tc.file))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ok {
				t.Fatalf("expected ok=true for %q", tc.file)
			}
			if hh != tc.wantHH {
				t.Fatalf("hh=%q, want %q", hh, tc.wantHH)
			}
		})
	}
}

// Daily 카테고리는 이름 구조가 완벽해도 항상 유보다.
// (Daily 목적지의 (HH) 는 config 검증이 거부하므로 정상 운영에서는
// 도달하지 않지만, 도달해도 "00" 같은 값을 만들어 내지 않아야 한다.)
func TestHourFromName_DailyAlwaysUnidentified(t *testing.T) {
	for _, tc := range []struct {
		cat  Category
		name string
	}{
		{CategoryRINEX2Daily, "SUW12500.26o"},
		{CategoryRINEX2Daily, "SUW1250a.26o"}, // Hourly 이름이 Daily 카테고리에
		{CategoryRINEX3Daily, "SUW100KOR_R_20262500000_01D_MO.rnx"},
		{CategoryRINEX3Daily, "SUW100KOR_R_20262501300_01H_MO.rnx"}, // Hourly 이름이 Daily 카테고리에
		{CategoryRINEX4Daily, "SUW100KOR_R_20262500000_01D_30S_MO.rnx.gz"},
	} {
		t.Run(string(tc.cat)+"/"+tc.name, func(t *testing.T) {
			hh, ok, err := HourFromName(tc.cat, NormalizeName(tc.name))
			if err != nil || ok || hh != "" {
				t.Fatalf("daily must be unidentified: hh=%q ok=%v err=%v", hh, ok, err)
			}
		})
	}
}

// 구조가 어긋나거나 카테고리 주기가 맞지 않는 이름은 유보(ok=false, err=nil)다.
// SiteFromName 의 MalformedStructure 와 같은 원칙이다.
func TestHourFromName_Unidentified(t *testing.T) {
	for _, tc := range []struct {
		cat  Category
		name string
	}{
		// 짧은 이름
		{CategoryRINEX2Hourly, ""},
		{CategoryRINEX2Hourly, "SUW1"},
		{CategoryRINEX2Hourly, "SUW12500.26o"}, // Daily 세션 '0' 이 Hourly 카테고리에
		{CategoryRINEX2Hourly, "SUW1250z.26o"}, // 세션 'z' 는 규약 밖
		{CategoryRINEX2Hourly, "SUW1xx0a.26o"}, // ddd 가 숫자 아님
		{CategoryRINEX2Hourly, "SUW1250a.2xo"}, // yy 가 숫자 아님
		{CategoryRINEX2Hourly, "SUW1250a.260"}, // 파일타입이 영문 아님
		{CategoryRINEX2Hourly, "YONS060.20M"},  // 7자 예외 형태
		{CategoryRINEX2Hourly, "____250a.26o"}, // 관측소 코드가 영숫자 아님
		{CategoryRINEX2Hourly, "DB-N250a.26o"}, // 관측소 코드에 기호 포함
		{CategoryRINEX2Hourly, "SUW1000a.26o"}, // DOY=000
		{CategoryRINEX2Hourly, "SUW1367a.26o"}, // DOY=367
		// 긴 이름
		{CategoryRINEX3Hourly, "SUW1_R_20262500000_01H_MO.rnx"},             // 관측소 4자
		{CategoryRINEX3Hourly, "SUW100KOR_RR_20262500000_01H_MO.rnx"},       // 소스 2자
		{CategoryRINEX3Hourly, "SUW100KOR_R_2026250000X_01H_MO.rnx"},        // 타임스탬프에 문자
		{CategoryRINEX3Hourly, "SUW100KOR_R_202625000_01H_MO.rnx"},          // 타임스탬프 9자리
		{CategoryRINEX3Hourly, "SUW100KOR_R_20262500000_01D_MO.rnx"},        // Daily 주기가 Hourly 카테고리에
		{CategoryRINEX3Hourly, "DBON00KOR_R_20260010300_15M_01S_MS.rnx.gz"}, // 15M 은 주기 유보
		{CategoryRINEX3Hourly, "SUW100KOR_R_20262500000_01H_MO.txt"},        // 확장자 규약 밖
		{CategoryRINEX3Hourly, "SUW100KOR_R_20262502400_01H_MO.rnx"},        // HH=24 는 실재하지 않음
		{CategoryRINEX4Hourly, "SUW100KOR_R_20262509900_01H_MO.rnx"},        // HH=99
		{CategoryRINEX3Hourly, "SUW100KOR_R_20260001200_01H_MO.rnx"},        // DOY=000
		{CategoryRINEX3Hourly, "SUW100KOR_R_20263661200_01H_MO.rnx"},        // 평년 DOY=366
		{CategoryRINEX3Hourly, "SUW100KOR_R_20263671200_01H_MO.rnx"},        // DOY=367
		{CategoryRINEX3Hourly, "SUW100KOR_R_20262501260_01H_MO.rnx"},        // MM=60
		{CategoryRINEX3Hourly, "SUW100KOR_R_00002501200_01H_MO.rnx"},        // 연도=0000
		{CategoryRINEX3Hourly, "backup_old_2026_temp_mo.rnx.gz"},            // 임의 이름
	} {
		t.Run(string(tc.cat)+"/"+tc.name, func(t *testing.T) {
			hh, ok, err := HourFromName(tc.cat, NormalizeName(tc.name))
			if err != nil || ok || hh != "" {
				t.Fatalf("must be unidentified: hh=%q ok=%v err=%v", hh, ok, err)
			}
		})
	}
}

func TestHourFromName_LongLeapDay(t *testing.T) {
	hh, ok, err := HourFromName(
		CategoryRINEX3Hourly,
		NormalizeName("SUW100KOR_R_20243662359_01H_MO.rnx"),
	)
	if err != nil || !ok || hh != "23" {
		t.Fatalf("leap-year DOY 366: hh=%q ok=%v err=%v", hh, ok, err)
	}
}

// 닫힌 열거형 전수: 모든 Category 가 라우팅되어 있어야 한다.
// 새 상수를 추가하고 switch 갈래를 빠뜨리면 이 테스트가 먼저 깨진다
// (setkey_test 의 TotalOverCategories 와 같은 역할).
func TestHourFromName_TotalOverCategories(t *testing.T) {
	for _, cat := range Categories() {
		t.Run(string(cat), func(t *testing.T) {
			_, _, err := HourFromName(cat, "anything")
			if err != nil {
				t.Fatalf("category %q is not routed: %v", cat, err)
			}
		})
	}
}

// 라우팅 밖의 값은 유보가 아니라 err 다 — 침묵 실패 방지.
func TestHourFromName_UnknownCategoryIsError(t *testing.T) {
	_, ok, err := HourFromName(Category("RINEX9_HOURLY"), "suw1250a.26o")
	if err == nil {
		t.Fatal("expected err for unrouted category")
	}
	if ok {
		t.Fatal("ok must be false when err != nil")
	}
	if got := err.Error(); !strings.Contains(got, `"RINEX9_HOURLY"`) {
		t.Fatalf("err should name the category: %s", got)
	}
}
