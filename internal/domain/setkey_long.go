package domain

import "strings"

// longSetKeyKind는 RINEX3/RINEX4 긴 파일명에서 set_key와 kind를 도출한다.
//
//	DBON00KOR_R_20262500000_01D_30S_MO.crx.gz
//	  → set_key = dbon00kor_r_20262500000_01d, kind = mo
//
//	DBON00KOR_R_20262500000_01D_MN.rnx.gz
//	  → set_key = dbon00kor_r_20262500000_01d, kind = mn
//
// 지원하는 구조는 앞쪽 고정 4필드, 선택적인 레이트 필드,
// 마지막 데이터 종류 필드로 구성된다.
//
// 레이트 유무와 관계없이 앞 4필드로 set_key를 만들고,
// 마지막 필드에서 표현 형식 확장자를 제거해 kind를 추출한다.
//
// 판정 기준은 필드 수가 아니라 각 필드의 형태다. 수만 검사하면
// 언더스코어 4~5개짜리 임의 이름(backup_old_2026_temp_mo.rnx.gz)이
// 전부 세트 키를 받아 유령 세트를 만든다. parseShortName의 definite
// 원칙과 동일하게, 형태가 확실할 때만 세트 판정 재료로 쓰고
// 불확실하면 유보한다.
//
// 이 형태 지식의 소유자는 이 파일이다. category.go의 longNamePeriod는
// 주석에 명시된 대로 Category 대조용 주기 필드만 보수적으로 확인할 뿐
// 긴 파일명 전체의 문법을 알지 못하므로, 재사용할 기존 소유자가 없다.
//
// 파일명 구조만 확인하며 파일 내용, 실제 날짜의 유효성,
// RequiredKinds에 따른 세트 완성도는 검증하지 않는다.
//
// 판정할 수 없는 이름은 빈 값과 ok=false를 반환한다.
// 이는 전송 허용을 의미하지 않는다.
func longSetKeyKind(name string) (setKey, kind string, ok bool) {
	base := BaseName(name)
	fields := strings.Split(base, "_")

	if len(fields) != longSetKeyMinFields &&
		len(fields) != longSetKeyMaxFields {
		return "", "", false
	}

	// 앞 4필드(키 구성 필드)의 형태 검사.
	//
	//	DBON00KOR _ R _ 20262500000 _ 01D
	//	 관측소ID  소스   타임스탬프    주기
	//
	// 길이·문자 종류가 규격을 벗어나면 세트 소속을 확정할 근거가
	// 없으므로 유보한다. 값의 의미(실존 관측소, 유효 날짜)는
	// 검증하지 않는다.
	if !isAlnumOfLen(fields[0], longStationLength) {
		return "", "", false
	}

	if !isAlphaOfLen(fields[1], longSourceLength) {
		return "", "", false
	}

	if !isDigitsOfLen(fields[2], longTimestampLength) {
		return "", "", false
	}

	if !isPeriodShaped(fields[3]) {
		return "", "", false
	}

	// 6필드 구조에서는 레이트 필드도 같은 형태(숫자 2 + 영문 1)다.
	// 01S / 30S 등. 값의 의미는 검사하지 않는다.
	if len(fields) == longSetKeyMaxFields &&
		!isPeriodShaped(fields[longSetKeyPrefixFields]) {
		return "", "", false
	}

	last := fields[len(fields)-1]
	k, stripped := stripTypeExt(last)
	if !stripped || !isLongKind(k) {
		return "", "", false
	}

	return strings.Join(fields[:longSetKeyPrefixFields], "_"), k, true
}

const (
	// 관측소 ID, 데이터 소스, 타임스탬프, 기간.
	longSetKeyPrefixFields = 4

	// 앞 4필드 + 데이터 종류.
	longSetKeyMinFields = longSetKeyPrefixFields + 1

	// 앞 4필드 + 레이트 + 데이터 종류.
	longSetKeyMaxFields = longSetKeyMinFields + 1

	// 긴 파일명의 데이터 종류는 두 영문자로 표현된다.
	longKindLength = 2

	// 관측소 ID는 9자 영숫자다 (예: dbon00kor).
	longStationLength = 9

	// 데이터 소스는 1자 영문이다 (R=수신기, S=스트림, U=불명).
	longSourceLength = 1

	// 타임스탬프는 11자리 숫자다 (YYYYDDDHHMM).
	longTimestampLength = 11
)

// isLongKind는 데이터 종류의 구조만 검사한다.
// mo/mn/ms 같은 종류 목록이나 필수 여부는 여기서 제한하지 않는다.
// 입력은 BaseName을 통해 소문자로 정규화된 값이다.
func isLongKind(kind string) bool {
	if len(kind) != longKindLength {
		return false
	}

	for i := 0; i < len(kind); i++ {
		if kind[i] < 'a' || kind[i] > 'z' {
			return false
		}
	}

	return true
}

// stripTypeExt는 소문자로 정규화된 필드에서 표현 형식 확장자를 제거한다.
func stripTypeExt(field string) (dataType string, stripped bool) {
	if before, found := strings.CutSuffix(field, ".crx"); found {
		return before, true
	}
	if before, found := strings.CutSuffix(field, ".rnx"); found {
		return before, true
	}
	return "", false
}

// isPeriodShaped는 주기·레이트 필드 형태(숫자 2 + 영문 1)인지 답한다.
// 01d / 01h / 30s / 01s 등. Category와 주기의 정합은 MatchesName의
// 책임이므로 여기서는 형태만 본다.
func isPeriodShaped(s string) bool {
	return len(s) == 3 &&
		isDigitsOfLen(s[:2], 2) &&
		s[2] >= 'a' && s[2] <= 'z'
}

// isAlnumOfLen은 s가 지정 길이의 소문자 영숫자인지 답한다.
func isAlnumOfLen(s string, n int) bool {
	if len(s) != n {
		return false
	}

	for i := 0; i < len(s); i++ {
		ok := (s[i] >= 'a' && s[i] <= 'z') ||
			(s[i] >= '0' && s[i] <= '9')
		if !ok {
			return false
		}
	}

	return true
}

// isAlphaOfLen은 s가 지정 길이의 소문자 영문인지 답한다.
func isAlphaOfLen(s string, n int) bool {
	if len(s) != n {
		return false
	}

	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}

	return true
}

// isDigitsOfLen은 s가 지정 길이의 숫자인지 답한다.
func isDigitsOfLen(s string, n int) bool {
	if len(s) != n {
		return false
	}

	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}
