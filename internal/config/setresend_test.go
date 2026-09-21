package config

import (
	"errors"
	"fmt"
	"reflect"
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

	if !strings.Contains(err.Error(), "게이트가 꺼져") {
		t.Errorf("게이트 OFF 사유가 메시지에 없다: %v", err)
	}
}

// TestMapConfig_ResendMinKinds_NotInPUTSFTP 는 키가 [SET.RINEXx] 에만
// 있음을 고정한다. [PUT.SFTP] 에 두면 knownKeys 가 거부한다.
func TestMapConfig_ResendMinKinds_NotInPUTSFTP(t *testing.T) {
	input := strings.Replace(
		validINIForLoadTest(),
		"[PUT.SFTP]\nAuthMethod",
		"[PUT.SFTP]\nResendMinKinds = O\nAuthMethod",
		1,
	)

	_, _, err := mapConfigForTest(t, input)
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("error = %v, want ErrUnknownKey ([PUT.SFTP] 오배치)", err)
	}

	if !strings.Contains(err.Error(), "ResendMinKinds") {
		t.Errorf("거부된 키 이름이 메시지에 없다: %v", err)
	}
}

// TestMapConfig_ResendMinKinds_BadSyntax 는 값 문법이 RequiredKinds 와
// 동일함을 고정한다 (빈 항목·중복·영문 외 문자 + 목록 외 값 거부).
func TestMapConfig_ResendMinKinds_BadSyntax(t *testing.T) {
	cases := []struct {
		name   string
		value  string
		reason string
	}{
		{"빈 값", "", "허용하지 않습니다"},
		{"false", "false", "허용하지 않습니다"},
		{"true", "TRUE", "허용하지 않습니다"},
		{"빈 항목", "mo,,mn", "빈 항목"},
		{"앞 쉼표", ",mo", "빈 항목"},
		{"뒤 쉼표", "mo,", "빈 항목"},
		{"중복", "mo,MO", "중복"},
		{"영문 외 문자", "MO.crx", "영문자 외 문자"},
		{"숫자", "mo1", "영문자 외 문자"},
		{"한글", "관측", "영문자 외 문자"},
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
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error = %v, want syntax reason %q", err, tc.reason)
			}
		})
	}
}

// 모든 버전에서 키의 존재 여부와 게이트 상태를 독립적으로 검증한다.
// resend 설정을 읽어도 평소 RequiredKinds 정책은 그대로여야 한다.
func TestMapConfig_ResendMinKinds_AllVersions(t *testing.T) {
	versions, err := setVersions()
	if err != nil {
		t.Fatalf("setVersions(): %v", err)
	}

	for _, version := range versions {
		for _, tc := range []struct {
			name     string
			required string
			minLine  string
			want     []string
			wantErr  bool
		}{
			{"equal", "MO,MN", "ResendMinKinds = mn, MO", []string{"mn", "mo"}, false},
			{"on_absent", "MO,MN", "", nil, false},
			{"off_absent", "false", "", nil, false},
			{"off_present", "false", "ResendMinKinds = mo", nil, true},
			{"off_empty", "false", "ResendMinKinds =", nil, true},
		} {
			t.Run(fmt.Sprintf("RINEX%d/%s", version, tc.name), func(t *testing.T) {
				input := withSetSections(t, fmt.Sprintf("[SET.RINEX%d]\nRequiredKinds = %s\n%s\n", version, tc.required, tc.minLine))
				cfg, _, err := mapConfigForTest(t, input)
				if tc.wantErr {
					if !errors.Is(err, ErrBadValue) {
						t.Fatalf("error = %v, want ErrBadValue", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				p := cfg.Set.Policy(version)
				if !reflect.DeepEqual(p.ResendMinKinds, tc.want) {
					t.Errorf("ResendMinKinds = %v, want %v", p.ResendMinKinds, tc.want)
				}
				var required []string
				if tc.required != "false" {
					required = []string{"mo", "mn"}
				}
				if p.Enabled != (tc.required != "false") || !reflect.DeepEqual(p.RequiredKinds, required) {
					t.Errorf("ordinary gate policy changed: %+v", p)
				}
			})
		}
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

// 서울시 현장 세트 원자성 (GUIDELINES 12절: RequiredKinds = G,L,N,O).
// S 는 선택 종이라 목록에 넣지 않는다. ResendMinKinds 는 아직 없다.
const seoulRINEX2RequiredKinds = "G,L,N,O"

// TestMapConfig_SeoulSetAtomicityOn 은 게이트를 켠 서울시 설정이
// ResendMinKinds 도입 이후에도 시작 오류가 아님을 고정한다.
func TestMapConfig_SeoulSetAtomicityOn(t *testing.T) {
	t.Run("키_없음_현재_현장", func(t *testing.T) {
		input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = `+seoulRINEX2RequiredKinds+`

[SET.RINEX3]
RequiredKinds = false

[SET.RINEX4]
RequiredKinds = false

`)

		cfg, _, err := mapConfigForTest(t, input)
		if err != nil {
			t.Fatalf("서울시 게이트 ON 설정이 로드 실패: %v", err)
		}

		p2 := cfg.Set.Policy(2)
		if !p2.Enabled {
			t.Fatal("서울시 RINEX2 게이트가 꺼졌다")
		}

		want := []string{"g", "l", "n", "o"}
		if !reflect.DeepEqual(p2.RequiredKinds, want) {
			t.Errorf("RequiredKinds = %v, want %v", p2.RequiredKinds, want)
		}

		if p2.ResendMinKinds != nil {
			t.Errorf("ResendMinKinds 키 없는데 %v — 평소 게이트를 건드리면 안 된다",
				p2.ResendMinKinds)
		}

		if cfg.Set.Policy(3).Enabled || cfg.Set.Policy(4).Enabled {
			t.Error("RINEX3/4 는 서울시에서 게이트 OFF 여야 한다")
		}
	})

	t.Run("권장_ResendMinKinds", func(t *testing.T) {
		input := withSetSections(t, `[SET.RINEX2]
RequiredKinds = `+seoulRINEX2RequiredKinds+`
ResendMinKinds = O

[SET.RINEX3]
RequiredKinds = false

`)

		cfg, _, err := mapConfigForTest(t, input)
		if err != nil {
			t.Fatalf("서울시 게이트 ON + ResendMinKinds=O 가 로드 실패: %v", err)
		}

		p2 := cfg.Set.Policy(2)
		if !p2.Enabled || !reflect.DeepEqual(p2.RequiredKinds, []string{"g", "l", "n", "o"}) {
			t.Errorf("평소 게이트가 바뀌었다: %+v", p2)
		}

		if len(p2.ResendMinKinds) != 1 || p2.ResendMinKinds[0] != "o" {
			t.Errorf("ResendMinKinds = %v, want [o]", p2.ResendMinKinds)
		}
	})
}
