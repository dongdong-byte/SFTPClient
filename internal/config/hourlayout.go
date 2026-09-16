package config

import (
	"fmt"
	"strings"
)

// HourLayout 은 Hourly Category 의 물리 디렉터리 배치이다.
//
// 서울시 긴급 대응용 과도기다. MVP2에서 제거하고 범위 제한 재귀 탐색으로
// 교체한다. 이 키를 재강화하거나 기관이 늘 때마다 값을 추가하지 않는다
// (GUIDELINES 9.3).
//
//	dir :  ...\(DOY)\(HH)\   시각별 하위 디렉터리
//	flat:  ...\(DOY)\        한 날짜 폴더에 24시간 파일
//
// 서울시 2026-09: 소스는 dir, 목적지 RemotePath 는 (HH) 없는 flat (/RNX2/)
// 이 흔하다. HourLayout 은 LocalPath 만 보고, RemotePath 는 강제하지 않는다.
//
// 두 경우 모두 파일 자체는 Hourly 이며, 실제 시각은 파일명에 인코딩된다.
//
// RINEX2 short name:
//   - 세션 문자 a~x 가 시간(00~23)을 나타낸다.
//
// RINEX3/4 long name:
//   - 시작 시각 필드(YYYYDDDHHMM)에서 실제 시각을 확인할 수 있다.
//   - _01H_ 필드는 실제 시각이 아니라 파일 주기를 의미한다.
//
// 따라서 물리 디렉터리 배치는 Category(주기)와 독립된 축이다.
//
// 이 값은 config.ini 의 각 [PUT.<CATEGORY>_HOURLY] 섹션에서만 읽으며
// DB 에 저장하지 않는다.
//
// scan 은 이 값을 직접 참조하지 않고 LocalPath 의 (HH) 토큰 유무로
// 순회 횟수를 결정한다. config validate 가 HourLayout 선언과 LocalPath 를
// 먼저 교차 검증하므로 scan 이 config.HourLayout 에 직접 의존할 필요가 없다.
//
// 역할은 선언과 LocalPath 의 일치를 시작 시 강제하는 것이다.
// validate 가 아래를 교차 검증한다(checkHourToken). RemotePath 는 대상이 아니다.
//
//	dir  인데 LocalPath 에 (HH) 없음  → 거부
//	flat 인데 LocalPath 에 (HH) 있음  → 거부
//
// 이렇게 하면 "(HH) 없으면 flat 으로 간주" 같은 암묵적 완화 때문에
// 설정 오타가 조용히 통과하는 일을 막을 수 있다.
type HourLayout string

const (
	// HourLayoutDir 은 시각별 하위 디렉터리 배치이다.
	//
	// LocalPath 에 (HH) 가 있으며 Scanner 가 이를 00~23으로 확장하여
	// 날짜당 24개 디렉터리를 나열한다. RemotePath 의 (HH) 는 강제하지 않는다
	// (과도기. GUIDELINES 9.3).
	HourLayoutDir HourLayout = "dir"

	// HourLayoutFlat 은 한 디렉터리에 24시간 파일이 함께 놓이는 배치이다.
	//
	// LocalPath 에 (HH) 가 없으며 Scanner 는 날짜 디렉터리를 한 번만 나열한다.
	// RemotePath 의 (HH) 는 강제하지 않는다 (과도기. GUIDELINES 9.3).
	HourLayoutFlat HourLayout = "flat"
)

// DefaultHourLayout 은 키를 생략한 Hourly 섹션의 기본값이다.
//
// dir 을 기본으로 두는 이유는 종전 동작(시각별 24회 순회)과 같기 때문이다.
// flat 은 항상 명시해야 하므로 키 생략이 조용히 평면 구조로 바뀌지 않는다.
//
// 예를 들어 flat 경로에서 HourLayout 키를 생략하면 기본값은 dir 이 되고,
// LocalPath 에 (HH) 가 없으므로 validate 단계에서 즉시 거부된다.
const DefaultHourLayout = HourLayoutDir

// ParseHourLayout 은 config.ini 값을 HourLayout 으로 변환한다.
//
// 사람이 직접 작성하는 설정값이므로 앞뒤 공백과 대소문자는 허용한다.
//
// 빈 문자열과 알 수 없는 값은 오류다.
// "키 생략" 과 "키는 있으나 값이 비어 있음" 은 서로 다르며,
// 키 생략 시 기본값 적용은 load 계층에서 담당한다.
func ParseHourLayout(s string) (HourLayout, error) {
	v := HourLayout(strings.ToLower(strings.TrimSpace(s)))

	switch v {
	case HourLayoutDir, HourLayoutFlat:
		return v, nil
	default:
		return "", fmt.Errorf(
			"unknown HourLayout %q (want %q or %q)",
			s,
			HourLayoutDir,
			HourLayoutFlat,
		)
	}
}

// String 은 config 표기값을 반환한다.
func (h HourLayout) String() string {
	return string(h)
}

// Valid 는 정의된 HourLayout 값인지 답한다.
//
// 정상적인 config Load 경로를 거친 값은 항상 유효해야 한다.
// 다만 테스트나 다른 코드에서 Config 구조체를 직접 생성할 수 있으므로,
// zero-value 또는 잘못된 값을 validate 단계에서 잡기 위해 제공한다.
func (h HourLayout) Valid() bool {
	switch h {
	case HourLayoutDir, HourLayoutFlat:
		return true
	default:
		return false
	}
}
