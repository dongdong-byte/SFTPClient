package domain

import "testing"

// TestRinexVersion_TotalOverCategories 는 지원하는 모든 Category 가
// 버전으로 매핑됨을 고정한다.
//
// 새 Category 상수를 추가하고 RinexVersion 의 switch 갈래를 빠뜨리면
// 이 테스트가 먼저 깨진다. 버전의 출처는 닫힌 열거형 하나이며
// config 에 버전 키를 두지 않는다. 전수 매핑이 빠지면 세트 게이트와
// 파서가 조용히 어긋난다.
func TestRinexVersion_TotalOverCategories(t *testing.T) {
	want := map[Category]int{
		CategoryRINEX2Daily:  2,
		CategoryRINEX2Hourly: 2,
		CategoryRINEX3Daily:  3,
		CategoryRINEX3Hourly: 3,
		CategoryRINEX4Daily:  4,
		CategoryRINEX4Hourly: 4,
	}

	for _, c := range Categories() {
		v, err := c.RinexVersion()
		if err != nil {
			t.Errorf("%s: RinexVersion() err=%v — switch 갈래 누락", c, err)
			continue
		}

		if v != want[c] {
			t.Errorf("%s: RinexVersion() = %d, want %d", c, v, want[c])
		}
	}

	if _, err := Category("BOGUS").RinexVersion(); err == nil {
		t.Error("미지원 Category 는 err 여야 한다")
	}
}

// TestSetKeyKind_ReturnContract 는 반환 계약의 세 갈래를 고정한다.
//
//	err != nil        라우팅 안 된 카테고리 (설정 공백 — 반드시 잡는다)
//	ok=false, err=nil 세트 소속 판정 불가 (정상 범위의 유보)
//	ok=true           도출 성공
//
// err 와 ok 는 처리 방식이 정반대이므로 합치지 않는다.
func TestSetKeyKind_ReturnContract(t *testing.T) {
	// 라우팅 공백 → err. 유보로 취급하면 안 된다.
	if _, _, _, err := SetKeyKind(Category("RINEX5_DAILY"), "x"); err == nil {
		t.Error("미지원 카테고리가 err 없이 통과했다")
	}

	// 유보 → (ok=false, err=nil). 오류로 취급하면 안 된다.
	if _, _, ok, err := SetKeyKind(
		CategoryRINEX2Daily, "YONS060.20M",
	); err != nil || ok {
		t.Errorf("유보는 (ok=false, err=nil): ok=%v err=%v", ok, err)
	}

	// 유보 시 부분 도출값이 새어 나가지 않는다.
	k, kd, ok, err := SetKeyKind(
		CategoryRINEX4Daily, "backup_old_2026_temp_mo.rnx.gz",
	)
	if err != nil || ok {
		t.Errorf("유보는 (ok=false, err=nil): ok=%v err=%v", ok, err)
	}
	if k != "" || kd != "" {
		t.Errorf("유보인데 값이 남아 있다: %q %q", k, kd)
	}
}

func TestSetKeyKind(t *testing.T) {
	tests := []struct {
		name string
		cat  Category
		in   string

		wantKey  string
		wantKind string
		wantOK   bool
	}{
		// ── RINEX2 짧은 파일명 (실파일: 도봉 DOY 250) ──────────
		{
			name:     "rinex2 daily O",
			cat:      CategoryRINEX2Daily,
			in:       "DBON2500.26O.gz",
			wantKey:  "dbon2500.26",
			wantKind: "o",
			wantOK:   true,
		},
		{
			name:     "rinex2 daily G, 경로 포함 입력",
			cat:      CategoryRINEX2Daily,
			in:       `D:\RINEX\250\DBON2500.26G.gz`,
			wantKey:  "dbon2500.26",
			wantKind: "g",
			wantOK:   true,
		},
		{
			name:     "rinex2 선택 종 S 도 도출은 된다 (완성 판정과 무관)",
			cat:      CategoryRINEX2Daily,
			in:       "DBON2500.26S.gz",
			wantKey:  "dbon2500.26",
			wantKind: "s",
			wantOK:   true,
		},
		{
			name:     "지리원 실데이터 z 종 (.zip 압축)",
			cat:      CategoryRINEX2Daily,
			in:       "chju2400.26z.zip",
			wantKey:  "chju2400.26",
			wantKind: "z",
			wantOK:   true,
		},
		{
			name:     "압축 없는 원본",
			cat:      CategoryRINEX2Daily,
			in:       "DBON2500.26N",
			wantKey:  "dbon2500.26",
			wantKind: "n",
			wantOK:   true,
		},
		{
			name:     "hourly 세션 문자 a — set_key 에 포함",
			cat:      CategoryRINEX2Hourly,
			in:       "DBON250a.26o.gz",
			wantKey:  "dbon250a.26",
			wantKind: "o",
			wantOK:   true,
		},
		{
			name:     "hourly 세션 문자 b 는 a 와 다른 세트",
			cat:      CategoryRINEX2Hourly,
			in:       "DBON250b.26o.gz",
			wantKey:  "dbon250b.26",
			wantKind: "o",
			wantOK:   true,
		},
		{
			name:   "표준 밖 7자 이름은 유보 (실관측 YONS060.20M)",
			cat:    CategoryRINEX2Daily,
			in:     "YONS060.20M",
			wantOK: false,
		},
		{
			name:   "rinex2 카테고리에 긴 이름 — 유보",
			cat:    CategoryRINEX2Daily,
			in:     "DBON00KOR_R_20262500000_01D_MN.rnx.gz",
			wantOK: false,
		},

		// ── RINEX3/4 긴 파일명 (실파일: 서울시 DBON, 측위원 DOKD) ──
		{
			name:     "rinex4 MO — 레이트(30S) 있음, 6필드",
			cat:      CategoryRINEX4Daily,
			in:       "DBON00KOR_R_20262500000_01D_30S_MO.crx.gz",
			wantKey:  "dbon00kor_r_20262500000_01d",
			wantKind: "mo",
			wantOK:   true,
		},
		{
			name:     "rinex4 MN — 항법은 레이트 생략, 5필드 (핵심 케이스)",
			cat:      CategoryRINEX4Daily,
			in:       "DBON00KOR_R_20262500000_01D_MN.rnx.gz",
			wantKey:  "dbon00kor_r_20262500000_01d",
			wantKind: "mn",
			wantOK:   true,
		},
		{
			name:     "rinex4 MS — MO 와 같은 set_key",
			cat:      CategoryRINEX4Daily,
			in:       "DBON00KOR_R_20262500000_01D_30S_MS.rnx.gz",
			wantKey:  "dbon00kor_r_20262500000_01d",
			wantKind: "ms",
			wantOK:   true,
		},
		{
			name:     "측위원 DOKD 도 동일 규칙",
			cat:      CategoryRINEX4Daily,
			in:       "DOKD00KOR_R_20262500000_01D_MN.rnx.gz",
			wantKey:  "dokd00kor_r_20262500000_01d",
			wantKind: "mn",
			wantOK:   true,
		},
		{
			name:     "rinex3 카테고리도 같은 긴 이름 파서 (버전 비구분)",
			cat:      CategoryRINEX3Hourly,
			in:       "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz",
			wantKey:  "sonp00kor_r_20260010300_01h",
			wantKind: "ms",
			wantOK:   true,
		},
		{
			name:     "압축 없는 .rnx 원본",
			cat:      CategoryRINEX4Daily,
			in:       "DBON00KOR_R_20262500000_01D_MN.rnx",
			wantKey:  "dbon00kor_r_20262500000_01d",
			wantKind: "mn",
			wantOK:   true,
		},
		{
			name:   "긴 카테고리에 짧은 이름 — 유보",
			cat:    CategoryRINEX4Daily,
			in:     "DBON2500.26O.gz",
			wantOK: false,
		},

		// ── 형태 검사: 필드 "수"만으로 통과하면 안 되는 이름들 ──
		// (2026-09-10 리뷰에서 실증된 느슨함의 회귀 방지)
		{
			name:   "5필드 임의 이름 — 유령 세트 금지",
			cat:    CategoryRINEX4Daily,
			in:     "backup_old_2026_temp_mo.rnx.gz",
			wantOK: false,
		},
		{
			name:   "타임스탬프 자릿수 이탈(7자리)",
			cat:    CategoryRINEX4Daily,
			in:     "old_dbon00kor_r_2026250_mo.rnx.gz",
			wantOK: false,
		},
		{
			name:   "주기 자리 형태 이탈(zzz)",
			cat:    CategoryRINEX4Daily,
			in:     "dbon00kor_x_20262500000_zzz_mo.rnx.gz",
			wantOK: false,
		},
		{
			name:   "관측소 ID 길이 이탈(8자)",
			cat:    CategoryRINEX4Daily,
			in:     "DBON0KOR_R_20262500000_01D_MN.rnx.gz",
			wantOK: false,
		},
		{
			name:   "레이트 자리 형태 이탈(6필드의 5번째)",
			cat:    CategoryRINEX4Daily,
			in:     "DBON00KOR_R_20262500000_01D_QCX_MO.crx.gz",
			wantOK: false,
		},
		{
			name:   "표현 형식 없는 마지막 필드 — 유보 (err 아님)",
			cat:    CategoryRINEX4Daily,
			in:     "DBON00KOR_R_20262500000_01D_MN.gz",
			wantOK: false,
		},
		{
			name:   "화이트리스트 밖 표현 형식(.tab) — 유보 (err 아님)",
			cat:    CategoryRINEX4Daily,
			in:     "DBON00KOR_R_20262500000_01D_MO.tab.gz",
			wantOK: false,
		},
		{
			name:   "kind 자리 3글자 — 유보",
			cat:    CategoryRINEX4Daily,
			in:     "DBON00KOR_R_20262500000_01D_MOX.rnx.gz",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, kind, ok, err := SetKeyKind(tt.cat, tt.in)

			// 이 테이블은 전부 지원 카테고리이므로 err 는 항상 nil.
			// 파일명이 이상해도 err 로 승격되면 계약 위반이다.
			if err != nil {
				t.Fatalf("SetKeyKind(%s, %q) err=%v — 유보는 err 가 아니다",
					tt.cat, tt.in, err)
			}

			if ok != tt.wantOK {
				t.Fatalf("SetKeyKind(%s, %q) ok=%v, want %v",
					tt.cat, tt.in, ok, tt.wantOK)
			}

			if !ok {
				if key != "" || kind != "" {
					t.Errorf("유보인데 값이 남음: %q %q", key, kind)
				}

				return
			}

			if key != tt.wantKey || kind != tt.wantKind {
				t.Errorf("SetKeyKind(%s, %q) = (%q, %q), want (%q, %q)",
					tt.cat, tt.in, key, kind, tt.wantKey, tt.wantKind)
			}
		})
	}
}

// TestSetKeyKind_SetGrouping 은 실파일 세트가 하나의 set_key 로
// 묶이는지 세트 단위로 확인한다. 게이트와 백필이 전제하는 성질이다.
func TestSetKeyKind_SetGrouping(t *testing.T) {
	rinex2 := []string{
		"DBON2500.26G.gz",
		"DBON2500.26L.gz",
		"DBON2500.26N.gz",
		"DBON2500.26O.gz",
		"DBON2500.26S.gz",
	}

	keys := map[string]bool{}
	kinds := map[string]bool{}

	for _, f := range rinex2 {
		key, kind, ok, err := SetKeyKind(CategoryRINEX2Daily, f)
		if err != nil || !ok {
			t.Fatalf("%q: 실파일이 유보/오류 — ok=%v err=%v", f, ok, err)
		}

		keys[key] = true
		kinds[kind] = true
	}

	if len(keys) != 1 {
		t.Errorf("RINEX2 세트가 하나의 키로 묶이지 않음: %v", keys)
	}

	for _, k := range []string{"g", "l", "n", "o", "s"} {
		if !kinds[k] {
			t.Errorf("RINEX2 kind %q 누락: %v", k, kinds)
		}
	}

	// MO(6필드)와 MN(5필드)이 같은 키가 되는 것이 핵심이다.
	// "뒤에서 절단" 식 구현은 여기서 깨진다.
	rinex4 := []string{
		"DBON00KOR_R_20262500000_01D_30S_MO.crx.gz",
		"DBON00KOR_R_20262500000_01D_30S_MS.rnx.gz",
		"DBON00KOR_R_20262500000_01D_MN.rnx.gz",
	}

	keys = map[string]bool{}

	for _, f := range rinex4 {
		key, _, ok, err := SetKeyKind(CategoryRINEX4Daily, f)
		if err != nil || !ok {
			t.Fatalf("%q: 실파일이 유보/오류 — ok=%v err=%v", f, ok, err)
		}

		keys[key] = true
	}

	if len(keys) != 1 {
		t.Errorf("RINEX4 세트가 하나의 키로 묶이지 않음 "+
			"(MN 레이트 생략 처리 확인): %v", keys)
	}

	// 시간별 세트는 타임스탬프가 달라 서로 다른 키가 된다.
	h3, _, ok, err := SetKeyKind(
		CategoryRINEX3Hourly,
		"SONP00KOR_R_20260010300_01H_01S_MO.crx.gz",
	)
	if err != nil || !ok {
		t.Fatalf("h3: 실파일이 유보/오류 — ok=%v err=%v", ok, err)
	}

	h4, _, ok, err := SetKeyKind(
		CategoryRINEX3Hourly,
		"SONP00KOR_R_20260010400_01H_01S_MO.crx.gz",
	)
	if err != nil || !ok {
		t.Fatalf("h4: 실파일이 유보/오류 — ok=%v err=%v", ok, err)
	}

	if h3 == h4 {
		t.Errorf("다른 시간대가 같은 세트로 묶임: %q", h3)
	}
}
