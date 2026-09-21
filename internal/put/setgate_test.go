package put

import (
	"testing"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
)

// gateNames 는 게이트 판정 우주(observed)의 축약이다.
// 실파일 기반: 도봉(O·S만 — DOY 250 실장애), 수원(완성+선택 종),
// 표준 밖 이름(YONS — 실관측 유보 사례).
func gateNames() []string {
	return []string{
		"dbon2500.26o.gz", "dbon2500.26s.gz",
		"suwn2500.26g.gz", "suwn2500.26l.gz",
		"suwn2500.26n.gz", "suwn2500.26o.gz", "suwn2500.26s.gz",
		"yons060.20m",
	}
}

func mustGate(t *testing.T, required []string) *setGate {
	t.Helper()

	g, err := newSetGate(domain.CategoryRINEX2Daily, required, gateNames())
	if err != nil {
		t.Fatal(err)
	}

	return g
}

// TestSetGate_Holds 는 확정 의미론 두 건을 고정한다 (2026-09-10):
// ① 유보(파싱 불가)는 개별 통과, ② 미완성 세트는 선택 종 포함 전체 보류.
func TestSetGate_Holds(t *testing.T) {
	g := mustGate(t, []string{"g", "l", "n", "o"})

	tests := []struct {
		name string
		held bool
		why  string
	}{
		{"dbon2500.26o.gz", true, "미완성 세트의 필수 종"},
		{"dbon2500.26s.gz", true, "② 미완성 세트의 선택 종도 보류"},
		{"suwn2500.26o.gz", false, "완성 세트의 필수 종"},
		{"suwn2500.26s.gz", false, "완성 세트의 선택 종 (§7)"},
		{"yons060.20m", false, "① 유보 — 개별 통과"},
	}

	for _, tt := range tests {
		held, err := g.holds(tt.name)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}

		if held != tt.held {
			t.Errorf("%s: held=%v, want %v (%s)",
				tt.name, held, tt.held, tt.why)
		}
	}
}

// TestSetGate_PresenceIsObservationNotCandidacy 는 완성도가 후보가
// 아니라 관측(observed) 기준임을 고정한다 — G 가 VERIFIED 로 후보에서
// 빠져도 디스크에 실재하면 세트는 완성이다.
func TestSetGate_PresenceIsObservationNotCandidacy(t *testing.T) {
	// observed 에는 g 가 있다 (전송 완료된 실재 파일).
	g := mustGate(t, []string{"g", "l", "n", "o"})

	// 후보에는 없을 l·n·o 만 판정해도 세트는 완성으로 통과한다.
	for _, name := range []string{
		"suwn2500.26l.gz", "suwn2500.26n.gz", "suwn2500.26o.gz",
	} {
		if held, _ := g.holds(name); held {
			t.Errorf("%s: 관측된 형제가 있는데 보류됨", name)
		}
	}
}

// TestSetGate_SeoulSetAtomicityOn 은 서울시 운영값(G,L,N,O)으로
// 게이트를 켜도 구성이 실패하지 않고, Daily·Hourly 모두 같은
// 의미론으로 판정함을 고정한다. S 는 선택 종이라 필수 목록에 없다.
func TestSetGate_SeoulSetAtomicityOn(t *testing.T) {
	required := []string{"g", "l", "n", "o"}

	t.Run("daily", func(t *testing.T) {
		g, err := newSetGate(domain.CategoryRINEX2Daily, required, gateNames())
		if err != nil {
			t.Fatalf("서울시 Daily 게이트 구성 실패: %v", err)
		}

		if !g.on() {
			t.Fatal("RequiredKinds 가 있는데 게이트가 꺼졌다")
		}

		held, err := g.holds("dbon2500.26o.gz")
		if err != nil || !held {
			t.Errorf("도봉 미완성: held=%v err=%v, want held", held, err)
		}

		held, err = g.holds("suwn2500.26o.gz")
		if err != nil || held {
			t.Errorf("수원 완성: held=%v err=%v, want pass", held, err)
		}

		held, err = g.holds("suwn2500.26s.gz")
		if err != nil || held {
			t.Errorf("완성 세트의 S(선택 종): held=%v err=%v, want pass", held, err)
		}
	})

	t.Run("hourly", func(t *testing.T) {
		names := []string{
			"dbon250a.26o.gz", "dbon250a.26s.gz",
			"suwn250a.26g.gz", "suwn250a.26l.gz",
			"suwn250a.26n.gz", "suwn250a.26o.gz", "suwn250a.26s.gz",
		}

		g, err := newSetGate(domain.CategoryRINEX2Hourly, required, names)
		if err != nil {
			t.Fatalf("서울시 Hourly 게이트 구성 실패: %v", err)
		}

		held, err := g.holds("dbon250a.26o.gz")
		if err != nil || !held {
			t.Errorf("도봉 Hourly 미완성: held=%v err=%v, want held", held, err)
		}

		held, err = g.holds("suwn250a.26s.gz")
		if err != nil || held {
			t.Errorf("수원 Hourly 완성: held=%v err=%v, want pass", held, err)
		}
	})
}

// TestSetGate_OffIsInert 는 게이트 OFF 가 완전한 무간섭임을 고정한다.
func TestSetGate_OffIsInert(t *testing.T) {
	g := mustGate(t, nil)

	for _, name := range gateNames() {
		held, err := g.holds(name)
		if err != nil || held {
			t.Errorf("%s: OFF 인데 held=%v err=%v", name, held, err)
		}

		if key, _ := g.setKeyOf(name); key != "" {
			t.Errorf("%s: OFF 인데 SetKey=%q", name, key)
		}
	}
}

// TestSetGate_RoutingGapIsError 는 라우팅 공백이 유보가 아니라
// 시끄러운 오류임을 고정한다 (err/유보 이원 계약).
func TestSetGate_RoutingGapIsError(t *testing.T) {
	if _, err := newSetGate(
		domain.Category("RINEX9_DAILY"), []string{"g"}, nil,
	); err == nil {
		t.Fatal("미지원 카테고리가 err 없이 통과했다")
	}
}

// TestSetGate_HeldSets 는 보류 요약을 고정한다 — 선택 종만 도착한
// 세트도 보류이므로 포함되고(전체 보류 의미론), 완성 세트는 제외,
// have 는 사전순·missing 은 설정 순서다.
func TestSetGate_HeldSets(t *testing.T) {
	g := mustGate(t, []string{"g", "l", "n", "o"})

	hs := g.heldSets()
	if len(hs) != 1 {
		t.Fatalf("held sets = %d, want 1 (도봉만): %+v", len(hs), hs)
	}

	got := hs[0]
	if got.SetKey != "dbon2500.26" {
		t.Errorf("SetKey = %q", got.SetKey)
	}

	wantHave := []string{"o", "s"}
	wantMissing := []string{"g", "l", "n"}

	for i, k := range wantHave {
		if got.Have[i] != k {
			t.Errorf("Have = %v, want %v", got.Have, wantHave)
			break
		}
	}

	for i, k := range wantMissing {
		if got.Missing[i] != k {
			t.Errorf("Missing = %v, want %v", got.Missing, wantMissing)
			break
		}
	}
}

// TestSetGate_SetKeyOf 는 경계 절단 동행 규칙을 고정한다 —
// 선택 종도 세트 키를 갖고(전체 보류의 파생), 유보는 빈 키다.
func TestSetGate_SetKeyOf(t *testing.T) {
	g := mustGate(t, []string{"g", "l", "n", "o"})

	if k, _ := g.setKeyOf("suwn2500.26s.gz"); k != "suwn2500.26" {
		t.Errorf("선택 종 SetKey = %q, want suwn2500.26", k)
	}

	if k, _ := g.setKeyOf("yons060.20m"); k != "" {
		t.Errorf("유보 SetKey = %q, want \"\"", k)
	}
}

func cutCand(cat domain.Category, name, key string) Candidate {
	return Candidate{
		Key:    ledger.PutKey{Category: cat, FileName: name, Revision: 1},
		SetKey: key,
	}
}

// TestDropSplitSets 는 MaxFilesPerRun 절단선에 걸린 세트가 통째로
// 다음 회차로 이월됨을 고정한다 (§13.1).
func TestDropSplitSets(t *testing.T) {
	c2 := domain.CategoryRINEX2Daily

	kept := []Candidate{
		cutCand(c2, "aaaa2500.26g.gz", "aaaa2500.26"),
		cutCand(c2, "aaaa2500.26l.gz", "aaaa2500.26"),
		cutCand(c2, "bbbb2500.26g.gz", "bbbb2500.26"),
		cutCand(c2, "yons060.20m", ""), // 유보 — 절단 조정 불참
	}

	cut := []Candidate{
		cutCand(c2, "bbbb2500.26l.gz", "bbbb2500.26"), // bbbb 쪼개짐
		cutCand(c2, "cccc2500.26g.gz", "cccc2500.26"),
	}

	out, moved := dropSplitSets(kept, cut)

	if moved != 1 || len(out) != 3 {
		t.Fatalf("moved=%d len=%d, want 1/3", moved, len(out))
	}

	for _, c := range out {
		if c.SetKey == "bbbb2500.26" {
			t.Errorf("쪼개진 세트의 멤버가 남아 있다: %+v", c)
		}
	}
}

// TestDropSplitSets_CategoryScoped 는 SetKey 가 같아도 카테고리가
// 다르면 다른 세트임을 고정한다 — RINEX3/4 는 같은 긴 파일명 규칙을
// 공유하므로 setID 가 카테고리를 포함해야 한다.
func TestDropSplitSets_CategoryScoped(t *testing.T) {
	key := "dbon00kor_r_20262500000_01d"

	kept := []Candidate{
		cutCand(domain.CategoryRINEX3Daily, "x_mo.rnx.gz", key),
	}

	cut := []Candidate{
		cutCand(domain.CategoryRINEX4Daily, "x_mn.rnx.gz", key),
	}

	if _, moved := dropSplitSets(kept, cut); moved != 0 {
		t.Fatal("카테고리가 다른데 세트로 묶였다")
	}
}

// TestDropSplitSets_UngatedCutIsInert 는 SetKey 없는 파일만 잘린
// 경우 조정이 일어나지 않음을 고정한다 (게이트 OFF 절단 불변).
func TestDropSplitSets_UngatedCutIsInert(t *testing.T) {
	kept := []Candidate{
		cutCand(domain.CategoryRINEX2Daily, "aaaa2500.26g.gz", "aaaa2500.26"),
	}

	cut := []Candidate{
		cutCand(domain.CategoryRINEX2Daily, "zzzz060.20m", ""),
	}

	out, moved := dropSplitSets(kept, cut)
	if moved != 0 || len(out) != 1 {
		t.Fatalf("moved=%d len=%d, want 0/1", moved, len(out))
	}
}
