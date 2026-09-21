package config

import (
	"fmt"
	"strings"

	"SFTPClient/internal/domain"
)

// SetConfig는 [SET.RINEXx] 섹션들이 정하는 버전별 세트 완성도 정책이다.
// 필수 kind는 운영 설정으로 결정하며 파일명 해석 규칙과 분리한다.
type SetConfig struct {
	// 섹션이 없는 버전은 저장하지 않는다.
	// Policy는 부재를 게이트 OFF로 처리한다.
	policies map[int]SetPolicy
}

// SetPolicy는 한 RINEX 버전의 세트 완성도 정책이다.
type SetPolicy struct {
	Version int

	// RequiredKinds가 CSV 목록이면 true다.
	// false이거나 섹션/키가 없으면 false다.
	// config에 별도의 Enabled 키는 두지 않는다.
	Enabled bool

	// 소문자로 정규화된 필수 kind 목록이다.
	// Enabled가 false이면 nil이다.
	RequiredKinds []string

	// ResendMinKinds는 resend 단계의 조건부 게이트 우회 최소 종 목록이다
	// (RESEND 설계 v4 §5). RequiredKinds의 부분집합이며 소문자로 정규화된다.
	//
	//	nil            게이트 ON이면 resend 단계는 게이트를 무조건 우회한다
	//	               (§5.3 2행). 게이트 OFF면 의미 없음.
	//	비어 있지 않음  미완성 세트라도 이 목록의 모든 종이 있으면 resend
	//	               단계에서 전송한다. RequiredKinds와 같은 AND 의미다.
	//
	// 게이트 OFF(Enabled=false)에서 이 키를 적으면 로더가 시작 오류로
	// 거부하므로, Enabled=false이면 이 값은 항상 nil이다 (§5.3 3행).
	ResendMinKinds []string
}

// Policy는 검증된 RINEX 버전에 해당하는 정책을 반환한다.
// 설정이 없으면 게이트 OFF로 처리한다.
// 호출자는 Category.RinexVersion()의 오류를 먼저 처리해야 한다.
func (s SetConfig) Policy(version int) SetPolicy {
	if p, ok := s.policies[version]; ok {
		return p
	}

	return SetPolicy{Version: version, Enabled: false}
}

// setVersions는 지원 카테고리에서 중복 없는 RINEX 버전 목록을 도출한다.
// 버전 매핑 오류는 설정 부재로 취급하지 않고 호출자에게 반환한다.
func setVersions() ([]int, error) {
	seen := make(map[int]bool)
	var out []int

	for _, c := range domain.Categories() {
		v, err := c.RinexVersion()
		if err != nil {
			return nil, fmt.Errorf(
				"config: SET 버전 목록 구성 실패: %w", err,
			)
		}

		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}

	return out, nil
}

// setSectionName은 버전에 대응하는 SET 섹션명을 만든다.
// knownKeys 등록과 로드에서 같은 규칙을 사용한다.
func setSectionName(version int) string {
	return fmt.Sprintf("SET.RINEX%d", version)
}

// parseRequiredKinds는 RequiredKinds 원문을 해석한다.
//
//	false    → 게이트 OFF
//	CSV 목록 → 게이트 ON, 소문자로 정규화
//	빈 값    → 오류
//	true     → 오류
//
// 목록의 각 kind는 소문자 영문자만 허용하며, 중복을 거부한다.
//
// 영문자 제한의 근거: kind 값의 산출처는 domain의 두 파서뿐이고,
// 둘 다 영문자만 내놓는다 (짧은 이름의 파일타입 t는 parseShortName이
// 영문으로 강제, 긴 이름의 종은 isLongKind가 2영문으로 강제).
// 따라서 영문 밖 kind(MO.crx 의 '.', 숫자 등)를 목록에 적으면 어떤
// 파일과도 매칭되지 않아, 오류 한 번 없이 영원히 완성되지 않는
// 게이트가 된다. 그 실수는 실행 중 침묵이 아니라 시작 시 오류로
// 잡는다. 표현 형식(.rnx/.crx)을 kind에 넣지 않는 것은 확정이다.
//
// 중복 거부의 근거: 중복은 동작에는 무해하지만 의도를 알 수 없는
// 설정이므로 조용히 흡수하지 않는다 (빈 값·true를 오류로 삼는 것과
// 같은 계열).
//
// 실재 kind 목록(o/g/mo 등)은 하드코딩하지 않는다.
// 섹션/키 부재는 로더에서 처리하며, 명시적인 빈 값과 구분한다.
func parseRequiredKinds(raw string) (kinds []string, enabled bool, err error) {
	v := strings.TrimSpace(raw)

	switch strings.ToLower(v) {
	case "":
		return nil, false, fmt.Errorf(
			"RequiredKinds가 비어 있습니다 (false 또는 kind 목록을 적으세요)",
		)

	case "false":
		return nil, false, nil

	case "true":
		return nil, false, fmt.Errorf(
			"RequiredKinds = true는 허용하지 않습니다 " +
				"(필수 kind 목록을 적거나 false로 끄세요)",
		)
	}

	out, err := parseKindList(v, "RequiredKinds")
	if err != nil {
		return nil, false, err
	}

	return out, true, nil
}

// parseKindList는 CSV kind 목록의 공통 문법이다.
// RequiredKinds와 ResendMinKinds가 같은 규칙을 사용한다 (RESEND v4 §5.3 —
// "값 문법은 RequiredKinds와 동일"). keyName은 오류 메시지 표기용이다.
func parseKindList(raw, keyName string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))

	for _, p := range parts {
		k := strings.ToLower(strings.TrimSpace(p))
		if k == "" {
			return nil, fmt.Errorf(
				"%s에 빈 항목이 있습니다: %q", keyName, raw,
			)
		}

		for i := 0; i < len(k); i++ {
			if k[i] < 'a' || k[i] > 'z' {
				return nil, fmt.Errorf(
					"%s의 kind %q에 영문자 외 문자가 있습니다 "+
						"(표현 형식 .crx/.rnx는 kind가 아닙니다 — "+
						"예: MO.crx가 아니라 MO)", keyName, p,
				)
			}
		}

		if seen[k] {
			return nil, fmt.Errorf(
				"%s에 kind %q가 중복입니다", keyName, k,
			)
		}

		seen[k] = true
		out = append(out, k)
	}

	return out, nil
}

// parseResendMinKinds는 ResendMinKinds 원문을 해석한다 (RESEND v4 §5).
//
//	kind 목록          → 소문자 정규화. RequiredKinds의 부분집합이어야 한다.
//	빈 값·true·false   → 오류. "우회 조건 없음(무조건 우회)"은 값이 아니라
//	                     키 부재로 표현한다 (§5.3 2행). false로 적으면
//	                     운영자가 무엇을 껐다고 믿는지 알 수 없으므로
//	                     조용히 해석하지 않는다.
//
// required는 같은 섹션에서 이미 파싱된 RequiredKinds(게이트 ON) 목록이다.
// 부분집합 검사에 실패하면 오류다 — RequiredKinds에 없는 종은 게이트
// 판정에 존재하지 않으므로, 그런 최소 종은 영원히 충족되지 않는
// 침묵 게이트가 된다 (parseRequiredKinds의 영문자 제한과 같은 계열).
func parseResendMinKinds(raw string, required []string) ([]string, error) {
	v := strings.TrimSpace(raw)

	switch strings.ToLower(v) {
	case "", "false", "true":
		return nil, fmt.Errorf(
			"ResendMinKinds = %q 는 허용하지 않습니다 "+
				"(kind 목록만 허용 — 무조건 우회를 원하면 키 자체를 지우세요)",
			raw,
		)
	}

	out, err := parseKindList(v, "ResendMinKinds")
	if err != nil {
		return nil, err
	}

	requiredSet := make(map[string]bool, len(required))
	for _, k := range required {
		requiredSet[k] = true
	}

	for _, k := range out {
		if !requiredSet[k] {
			return nil, fmt.Errorf(
				"ResendMinKinds의 kind %q 가 RequiredKinds에 없습니다 "+
					"(부분집합이어야 합니다 — RESEND v4 §5.3)", k,
			)
		}
	}

	return out, nil
}
