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
type Category string

const (
	CategoryRINEX2Daily  Category = "RINEX2_DAILY"
	CategoryRINEX2Hourly Category = "RINEX2_HOURLY"
	CategoryRINEX3Daily  Category = "RINEX3_DAILY"
	CategoryRINEX3Hourly Category = "RINEX3_HOURLY"
)

// Categories 는 지원하는 전체 Category 를 선언 순서대로 돌려준다.
// config 순회와 테스트에서 사용한다.
func Categories() []Category {
	return []Category{
		CategoryRINEX2Daily,
		CategoryRINEX2Hourly,
		CategoryRINEX3Daily,
		CategoryRINEX3Hourly,
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

// IsHourly 는 이 Category 가 시간 단위 파일인지 답한다.
//
// Scanner 가 (HH) 디렉터리를 0~23 순회할지 결정하는 데 사용한다.
// Daily 면 그 레벨이 존재하지 않으므로 순회하지 않는다.
func (c Category) IsHourly() bool {
	return c == CategoryRINEX2Hourly || c == CategoryRINEX3Hourly
}

// CategoryMatch 는 config 가 지정한 Category 와 파일명의 대조 결과이다.
//
// bool 로 두지 않는 이유는 "판정할 근거가 없음" 을 표현해야 하기 때문이다.
// RINEX3 긴 파일명에는 _01D_ / _01H_ 주기 필드가 있어 현재 규칙으로
// 확실하게 대조할 수 있다.
//
// RINEX2 는 별도의 파일명 규칙을 사용하지만, 현재 프로젝트에서 실제 유입되는
// RINEX2 파일명 규칙을 아직 검증하지 않았으므로 MVP 1 에서는 판정하지 않는다.
// 확인되지 않은 규칙 때문에 정상 파일을 거부하지 않는다.
type CategoryMatch int

const (
	// CategoryMatchUnknown 은 파일명만으로 현재 Category 를 확정할 수 없음을 뜻한다.
	CategoryMatchUnknown CategoryMatch = iota

	// CategoryMatchOK 는 config 의 Category 와 파일명이 함의하는 주기가 일치함을 뜻한다.
	CategoryMatchOK

	// CategoryMatchMismatch 는 config 의 Category 와 파일명이 함의하는 주기가
	// 명확하게 불일치함을 뜻한다.
	CategoryMatchMismatch
)

// rinex3PeriodField 는 RINEX3 긴 파일명에서 파일 주기 필드의 위치이다.
//
//	SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz
//	    0     1      2       3    4       5
//
// 항법 파일에는 샘플링 간격 필드가 없을 수 있지만,
// 주기 필드의 위치는 그대로이므로 인덱스 3 을 사용한다.
const rinex3PeriodField = 3

// CategoryMatchesName 은 config 가 지정한 Category 와 파일명이 함의하는 주기를
// 대조한다. Config 오기입 탐지 장치이다.
//
// 입력 name 은 함수 내부에서 NormalizeName 을 적용한다.
// 따라서 호출자는 대소문자, 경로 구분자, .part 여부를 별도로 정규화할 필요가 없다.
//
// 현재 MVP 1 에서는 RINEX3 의 _01D_ / _01H_ 만 확실한 규칙으로 판정한다.
// RINEX2 또는 15M 등 아직 프로젝트에서 의미가 확정되지 않은 주기는
// CategoryMatchUnknown 을 반환하여 정상 파일 누락을 방지한다.
func (c Category) CategoryMatchesName(name string) CategoryMatch {
	name = NormalizeName(name)

	if c != CategoryRINEX3Daily && c != CategoryRINEX3Hourly {
		return CategoryMatchUnknown
	}

	fields := strings.Split(name, "_")
	if len(fields) <= rinex3PeriodField {
		// RINEX3 Category 인데 긴 파일명 형식이 아니다.
		// 파일명 형식 자체의 유효성 검사는 verify 패키지의 책임이다.
		return CategoryMatchUnknown
	}

	switch fields[rinex3PeriodField] {
	case "01d":
		if c == CategoryRINEX3Daily {
			return CategoryMatchOK
		}
		return CategoryMatchMismatch

	case "01h":
		if c == CategoryRINEX3Hourly {
			return CategoryMatchOK
		}
		return CategoryMatchMismatch

	default:
		// 15M 등 현재 프로젝트에서 의미가 확정되지 않은 주기는
		// 불일치로 단정하지 않고 판정을 유보한다.
		return CategoryMatchUnknown
	}
}
