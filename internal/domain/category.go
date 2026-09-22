// Package domain 은 프로젝트의 핵심 개념과 값 규칙을 정의한다.
//
// 이 패키지는 어떤 internal 패키지도 import 하지 않는다. 의존 그래프의 최하단이다.
// 값 집합은 전부 internal/ledger/schema.sql 의 CHECK 제약과 1:1 로 대응하며,
// 다른 패키지는 상태 문자열 리터럴을 직접 쓰지 않고 여기 상수만 참조한다.
package domain

import (
	"fmt"
	"strings"
)

// Category 는 RINEX 데이터의 버전과 파일 주기를 나타낸다. (설계안 11)
//
// 값은 Scanner 가 파일에서 판정하지 않고 config.ini 의 [PUT.<CATEGORY>] 섹션에서
// 그대로 전달받는다. 정수(iota)가 아닌 문자열로 정의하여 선언 순서가 바뀌어도
// 저장값이 흔들리지 않게 한다. schema.sql common_ledger.category 와 같은 값이다.
//
// RINEX3 과 RINEX4 는 운영상 서로 다른 데이터 종류이므로 별도 Category 로 둔다.
// 다만 두 버전은 긴 파일명 규약을 공유하므로 파일명만으로 major version 을
// 확정하지 않는다. MatchesName 은 각 Category 의 Daily/Hourly 및
// short-name/long-name 계열 모순만 보수적으로 대조한다.
type Category string

const (
	CategoryRINEX2Daily  Category = "RINEX2_DAILY"
	CategoryRINEX2Hourly Category = "RINEX2_HOURLY"

	CategoryRINEX3Daily  Category = "RINEX3_DAILY"
	CategoryRINEX3Hourly Category = "RINEX3_HOURLY"

	CategoryRINEX4Daily  Category = "RINEX4_DAILY"
	CategoryRINEX4Hourly Category = "RINEX4_HOURLY"
)

// Categories 는 지원하는 전체 Category 를 선언 순서대로 돌려준다.
// config 순회와 테스트에서 사용한다.
func Categories() []Category {
	return []Category{
		CategoryRINEX2Daily,
		CategoryRINEX2Hourly,
		CategoryRINEX3Daily,
		CategoryRINEX3Hourly,
		CategoryRINEX4Daily,
		CategoryRINEX4Hourly,
	}
}

// ParseCategory 는 config.ini 의 섹션명에서 읽은 문자열을 Category 로 변환한다.
//
// 사람이 손으로 쓰는 값이므로 앞뒤 공백과 대소문자는 허용한다.
// 다만 저장되는 값은 항상 위 상수의 표기이다.
func ParseCategory(s string) (Category, error) {
	c := Category(strings.ToUpper(strings.TrimSpace(s)))

	for _, known := range Categories() {
		if c == known {
			return known, nil
		}
	}

	return "", fmt.Errorf("unknown category %q", s)
}

// String 은 Category 의 저장 표기를 반환한다.
func (c Category) String() string {
	return string(c)
}

// IsHourly 는 이 Category 가 시간 단위 파일인지 답한다.
//
// 세트 게이트·검증 등 주기 구분이 필요한 판정에 사용한다.
// Daily Category 에는 false 를 돌려준다.
func (c Category) IsHourly() bool {
	switch c {
	case CategoryRINEX2Hourly,
		CategoryRINEX3Hourly,
		CategoryRINEX4Hourly:
		return true
	default:
		return false
	}
}

// IsDaily 는 이 Category 가 일 단위 파일인지 답한다.
func (c Category) IsDaily() bool {
	switch c {
	case CategoryRINEX2Daily,
		CategoryRINEX3Daily,
		CategoryRINEX4Daily:
		return true
	default:
		return false
	}
}

// CategoryMatch 는 config 가 지정한 Category 와 파일명의 대조 결과이다.
//
// bool 로 두지 않는 이유는 "판정할 근거가 없음" 을 표현해야 하기 때문이다.
//
//	RINEX3/4(긴 파일명)  _01D_ / _01H_ 주기 필드로 대조한다.
//	                     파일명만으로 RINEX3 과 RINEX4 자체는 구분하지 않는다.
//	RINEX2(짧은 이름)    IGS 표준 ssssdddf.yyt 의 f(8번째 글자)로 대조한다.
//	                     f='0' 이면 Daily, 'a'~'x' 면 Hourly.
//
// 규칙은 표준으로 알려져 있으나, 현장에는 표준을 벗어나는 파일이 혼재할 수
// 있다(YONS060.20M 같은 7자 이름이 실제로 관측되었다). 확인되지 않은 형태
// 때문에 정상 파일을 거부하지 않기 위해, 형태가 확실하고 모순이 명확할 때만
// Mismatch 로 판정하고 근거가 부족하면 Unknown 으로 통과시킨다.
type CategoryMatch int

const (
	// CategoryMatchUnknown 은 파일명만으로 현재 Category 를 확정할 수 없음을 뜻한다.
	CategoryMatchUnknown CategoryMatch = iota

	// CategoryMatchOK 는 config 의 Category 와 파일명이 함의하는
	// 이름 계열 및 주기가 모순되지 않음을 뜻한다.
	CategoryMatchOK

	// CategoryMatchMismatch 는 config 의 Category 와 파일명이 명확하게
	// 모순됨을 뜻한다. 주기 불일치와 계열 혼입(short ↔ long)이 해당한다.
	CategoryMatchMismatch
)

// String 은 로그·테스트용 표기를 반환한다.
// int 기반 타입이므로 기본 출력은 0/1/2 가 되므로 명시적으로 이름을 돌린다.
func (m CategoryMatch) String() string {
	switch m {
	case CategoryMatchUnknown:
		return "Unknown"
	case CategoryMatchOK:
		return "OK"
	case CategoryMatchMismatch:
		return "Mismatch"
	default:
		return fmt.Sprintf("CategoryMatch(%d)", int(m))
	}
}

// longNamePeriodField 는 RINEX3/RINEX4 긴 파일명에서
// 파일 주기 후보 필드의 위치이다.
//
//	SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz
//	    0     1      2       3    4       5
//
// 항법 파일에는 샘플링 간격 필드가 없을 수 있지만,
// 주기 필드의 위치는 그대로이므로 인덱스 3 을 사용한다.
//
// 이 상수와 longNamePeriod 는 긴 파일명 전체의 문법 유효성을 검사하지 않는다.
// 현재 프로젝트가 Category 대조에 필요한 주기 필드만 보수적으로 확인한다.
const longNamePeriodField = 3

// MatchesName 은 config 가 지정한 Category 와 파일명이 함의하는
// 이름 계열·주기를 대조한다.
//
// config 오기입과 물리 디렉터리 혼입을 탐지하기 위한 장치이며,
// RINEX 파일 자체의 완전한 형식 검증기는 아니다.
//
// 입력 name 은 함수 내부에서 NormalizeName 을 적용한다.
//
// 주의:
// NormalizeName 은 .part 를 제거할 수 있으므로 MatchesName 자체는
// 임시 파일 여부를 판정하는 함수가 아니다.
// Ingress 에서는 domain.IsPartFile 을 먼저 검사한 뒤 MatchesName 을 호출한다.
//
// 기관은 버전별로 물리 폴더를 분리해 운영하지만,
// RINEX2 디렉터리에 긴 파일명 계열 파일이 혼입된 실제 사례가 있으므로
// 확실한 short-name ↔ long-name 혼입은 양방향 Mismatch 로 판정한다.
//
// RINEX3 과 RINEX4 는 Category 로는 구분하지만 파일명 규칙만으로
// 서로의 major version 을 판정하지 않는다. 따라서 두 Category 모두
// 동일한 long-name 대조 규칙을 사용한다.
func (c Category) MatchesName(name string) CategoryMatch {
	name = NormalizeName(name)

	switch c {
	case CategoryRINEX2Daily,
		CategoryRINEX2Hourly:
		return c.matchShortName(name)

	case CategoryRINEX3Daily,
		CategoryRINEX3Hourly,
		CategoryRINEX4Daily,
		CategoryRINEX4Hourly:
		return c.matchLongName(name)

	default:
		// ParseCategory 를 거치지 않은 값은 판정 근거가 없다.
		return CategoryMatchUnknown
	}
}

// matchLongName 은 긴 파일명 계열(RINEX3_*, RINEX4_*) Category 의 대조이다.
// name 은 NormalizeName 을 거친 값이어야 한다.
//
// RINEX3 과 RINEX4 의 major version 자체는 판정하지 않는다.
// long-name 계열 여부와 Daily/Hourly 주기만 대조한다.
func (c Category) matchLongName(name string) CategoryMatch {
	// 확실한 RINEX2 짧은 이름이 긴 이름 Category 에서 발견된 경우.
	// 판정 확신도가 반대 방향과 같으므로 계열 혼입으로 본다.
	if _, definite := parseShortName(name); definite {
		return CategoryMatchMismatch
	}

	switch longNamePeriod(name) {
	case "01d":
		if c.IsDaily() {
			return CategoryMatchOK
		}
		return CategoryMatchMismatch

	case "01h":
		if c.IsHourly() {
			return CategoryMatchOK
		}
		return CategoryMatchMismatch

	default:
		// 긴 파일명 계열이라고 확정할 근거가 부족하거나,
		// 15m 등 현재 프로젝트에서 의미가 확정되지 않은 주기이다.
		//
		// 확인되지 않은 형태를 억지로 거부하지 않고 Unknown 으로
		// 판정을 유보한다.
		return CategoryMatchUnknown
	}
}

// matchShortName 은 짧은 이름 계열(RINEX2_*) Category 의 대조이다.
// name 은 NormalizeName 을 거친 값이어야 한다.
func (c Category) matchShortName(name string) CategoryMatch {
	// 확실한 긴 파일명이 RINEX2 Category 에서 발견된 경우.
	// 실제 현장에서 관측된 혼입 사례를 잡는 갈래이다.
	//
	// "확실한" 의 기준은 현재 프로젝트가 사용하는 01D / 01H 로
	// 보수적으로 제한한다. 15m 등 미확정 주기는 Unknown 으로 통과한다.
	switch longNamePeriod(name) {
	case "01d", "01h":
		return CategoryMatchMismatch
	}

	session, definite := parseShortName(name)
	if !definite {
		return CategoryMatchUnknown
	}

	// f='0' 이면 Daily, 'a'~'x' 면 Hourly.
	switch {
	case session == '0':
		if c == CategoryRINEX2Daily {
			return CategoryMatchOK
		}
		return CategoryMatchMismatch

	default:
		// parseShortName 이 definite 로 판정한 나머지는 'a'~'x' 뿐이다.
		if c == CategoryRINEX2Hourly {
			return CategoryMatchOK
		}
		return CategoryMatchMismatch
	}
}

// longNamePeriod 는 긴 파일명 계열에서 기대하는 위치의
// 주기 후보 필드를 돌려준다.
//
// 이 함수는 전체 파일명이 유효한 RINEX long filename 인지를 검증하지 않는다.
// '_' 로 분리했을 때 Category 대조에 필요한 위치의 값만 확인한다.
// 필드 수가 부족하면 빈 문자열을 돌려준다.
func longNamePeriod(name string) string {
	fields := strings.Split(name, "_")
	if len(fields) <= longNamePeriodField {
		return ""
	}

	return fields[longNamePeriodField]
}

// parseShortName 은 name 이 확실한 RINEX2 짧은 파일명인지 판정한다.
//
//	ssssdddf.yyt        DBON0010.26G  →  dbon0010.26g
//	    ↑   ↑
//	    │   yy 숫자 2자리 + 파일타입 1자리(영문자)
//	    f   세션 문자. '0'=Daily, 'a'~'x'=Hourly
//
// BaseName 을 거친다. NormalizeName 만 쓰면 압축 확장자(.gz 등)가 남아
// '.' 분리 조각이 3개가 되고 형태 판정이 어긋난다.
//
// definite 는 형태가 8+3 으로 정확하고 ddd·yy 가 숫자이며,
// 파일타입이 영문자이고 세션 문자가 유효('0' 또는 'a'~'x')할 때만 true 이다.
//
// definite 가 아닌 이름은 대조 근거로 쓰지 않는다.
// YONS060.20M 같은 7자 예외가 실재하므로 형태가 조금이라도 어긋나면
// Mismatch 로 단정하지 않고 Unknown 으로 판정을 유보한다.
func parseShortName(name string) (session byte, definite bool) {
	base := BaseName(name)

	parts := strings.Split(base, ".")
	if len(parts) != 2 ||
		len(parts[0]) != 8 ||
		len(parts[1]) != 3 {
		return 0, false
	}

	// ssss(0~3) 관측소 ID 는 검사하지 않는다.
	// 관측소 표기 규칙은 기관마다 다를 수 있고,
	// 나머지 필드가 모두 맞으면 Category 대조 근거로 충분하다.
	if !isDigits(parts[0][4:7]) ||
		!isDigits(parts[1][:2]) {
		return 0, false
	}

	// 파일타입 t.
	// 현재 여기서는 구체적인 RINEX 파일 종류(o/n/g/l/m/s 등)는 제한하지 않는다.
	// 다만 확실한 short-name 으로 판단하려면 영문자여야 한다.
	// NormalizeName 을 거쳤으므로 소문자만 본다.
	if t := parts[1][2]; t < 'a' || t > 'z' {
		return 0, false
	}

	f := parts[0][7]
	if f != '0' && (f < 'a' || f > 'x') {
		return 0, false
	}

	return f, true
}

// isDigits 는 s 의 모든 바이트가 ASCII 숫자인지 답한다.
func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return true
}
