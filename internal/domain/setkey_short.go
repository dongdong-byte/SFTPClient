package domain

import "strings"

// shortSetKeyKind는 RINEX2 짧은 파일명에서 set_key와 kind를 도출한다.
//
//	DBON2500.26G.gz → set_key = dbon2500.26, kind = g
//
// 세션 코드('0'=일별, 'a'~'x'=시간별)는 set_key에 포함한다.
// 서로 다른 시간대의 세트가 하나로 묶이는 것을 방지한다.
//
// 형식 판정은 parseShortName을 재사용한다.
// 해당 함수는 세트 키 추출에 필요한 구조를 확인하며,
// 관측소 ID나 실제 날짜 범위까지 검증하지는 않는다.
//
// 파싱할 수 없는 이름은 세트 소속을 판정할 수 없으므로
// 빈 값과 ok=false를 반환한다. 이는 전송 허용을 의미하지 않는다.
func shortSetKeyKind(name string) (setKey, kind string, ok bool) {
	if _, definite := parseShortName(name); !definite {
		return "", "", false
	}

	// parseShortName 성공으로 '.' 구분 두 조각과
	// 각 조각의 길이 8, 3이 보장된다.
	base := BaseName(name)
	parts := strings.Split(base, ".")

	stem := parts[0]   // ssssdddf
	suffix := parts[1] // yyt

	setKey = stem + "." + suffix[:2]
	kind = suffix[2:]

	return setKey, kind, true
}
