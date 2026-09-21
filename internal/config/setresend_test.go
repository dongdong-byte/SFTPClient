package config

import (
	"errors"
	"strings"
	"testing"
)

// 커밋 2 (RESEND v4 §5) — ResendMinKinds 설정 읽기·검증.
//
// §5.3 검증 표를 그대로 고정한다:
//
//	게이트 ON,  키 있음 → RequiredKinds 문법 + 부분집합
//	게이트 ON,  키 없음 → nil (resend 단계 무조건 우회는 put 커밋의 몫)
//	게이트 OFF, 키 있음 → 시작 오류
//	게이트 OFF, 키 없음 → 변화 없음 (현재 배포 상태)
//
// put·cmd 는 이 커밋에서 수정하지 않으므로, 여기서는 SetPolicy 에
// 실리는 값까지만 고정한다.

// TestMapConfig_ResendMinKinds_Subset 은 정상 경로다 — 부분집합 목록이
// 소문자로 정규화되어 정책에 실린다.
func TestMapConfig_ResendMinKinds_Subset(t *testing.T) {
	input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = G,L,N,O
ResendMinKinds = O

[SET.RINEX3]
RequiredKinds = MO,MN
ResendMinKinds = mo

`)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	p2 := cfg.Set.Policy(2)
	if len(p2.ResendMinKinds) != 1 || p2.ResendMinKinds[0] != "o" {
		t.Errorf("버전 2 ResendMinKinds = %v, want [o] (소문자 정규화)",
			p2.ResendMinKinds)
	}

	p3 := cfg.Set.Policy(3)
	if len(p3.ResendMinKinds) != 1 || p3.ResendMinKinds[0] != "mo" {
		t.Errorf("버전 3 ResendMinKinds = %v, want [mo]", p3.ResendMinKinds)
	}
}

// TestMapConfig_ResendMinKinds_AbsentIsNil 은 키 부재가 오류도 기본값도
// 아닌 nil 임을 고정한다 (§5.3 2행 — 무조건 우회의 표현은 키 부재다).
func TestMapConfig_ResendMinKinds_AbsentIsNil(t *testing.T) {
	input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = G,L,N,O

`)

	cfg, _, err := mapConfigForTest(t, input)
	if err != nil {
		t.Fatalf("mapConfig() unexpected error: %v", err)
	}

	if got := cfg.Set.Policy(2).ResendMinKinds; got != nil {
		t.Errorf("키 부재인데 ResendMinKinds = %v, want nil", got)
	}
}

// TestMapConfig_ResendMinKinds_NotSubset 은 RequiredKinds 에 없는 종을
// 거부한다 — 그런 최소 종은 영원히 충족되지 않는 침묵 게이트가 된다.
func TestMapConfig_ResendMinKinds_NotSubset(t *testing.T) {
	input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = G,L,N
ResendMinKinds = O

`)

	_, _, err := mapConfigForTest(t, input)
	if !errors.Is(err, ErrBadValue) {
		t.Fatalf("error = %v, want ErrBadValue (부분집합 위반)", err)
	}

	if !strings.Contains(err.Error(), "RequiredKinds에 없습니다") {
		t.Errorf("부분집합 위반 사유가 메시지에 없다: %v", err)
	}
}

// TestMapConfig_ResendMinKinds_GateOffIsError 는 §5.3 3행이다 —
// 게이트 OFF 에서 키가 존재하면 조용한 무시가 아니라 시작 오류다.
func TestMapConfig_ResendMinKinds_GateOffIsError(t *testing.T) {
	input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = false
ResendMinKinds = O

`)

	_, _, err := mapConfigForTest(t, input)
	if !errors.Is(err, ErrBadValue) {
		t.Fatalf("error = %v, want ErrBadValue (게이트 OFF + 키 존재)", err)
	}
}

// TestMapConfig_ResendMinKinds_BadSyntax 는 값 문법이 RequiredKinds 와
// 동일함을 고정한다 (빈 항목·중복·영문 외 문자 + 목록 외 값 거부).
func TestMapConfig_ResendMinKinds_BadSyntax(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"빈 값", ""},
		{"false", "false"},
		{"true", "TRUE"},
		{"빈 항목", "o,,n"},
		{"중복", "o,O"},
		{"영문 외 문자", "MO.crx"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := withSetSections(t, `[SET.RINEX3]
RequiredKinds = MO,MN
ResendMinKinds = `+tc.value+`

`)

			_, _, err := mapConfigForTest(t, input)
			if !errors.Is(err, ErrBadValue) {
				t.Fatalf("value=%q: error = %v, want ErrBadValue",
					tc.value, err)
			}
		})
	}
}

// TestParseResendMinKinds_Direct 는 파서 단독 계약이다 — 로더 경유 없이
// 부분집합 판정과 정규화를 고정한다.
func TestParseResendMinKinds_Direct(t *testing.T) {
	got, err := parseResendMinKinds(" O , g ", []string{"g", "l", "n", "o"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"o", "g"}
	if len(got) != len(want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}

	for i := range want {
		if got[i] != want[i] {
			t.Errorf("kinds[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if _, err := parseResendMinKinds("x", []string{"g"}); err == nil {
		t.Error("부분집합 위반인데 오류가 없다")
	}
}
