package domain

import (
	"fmt"
	"strconv"
	"strings"
)

// hourCodeLen 은 시각 표기 길이다. 목적지 경로의 (HH) 토큰은 항상 두 자리
// 24시 표기("00"~"23")로 채운다. RINEX2 세션 문자('a'~'x')도 여기로 바꾼다.
const hourCodeLen = 2

// HourFromName 은 정규화된 파일명에서 관측 시작 시각(UTC, "00"~"23")을 추출한다.
//
// DOWNLOAD 목적지 경로의 (HH) 토큰을 채우는 재료다 (DOWNLOAD v3 §2.1-5, §4).
// PATH v3 에서 삭제한 (HH) 는 스캔 날짜에서 시각 폴더를 강제하는 토큰이었고,
// 이 함수가 뒷받침하는 (HH) 는 값의 출처가 파일명인 별개 토큰이다.
// 파일명 파생의 주인은 domain 하나라는 원칙에 따라 SiteFromName 옆에 둔다.
//
// 임의 파일명의 글자를 자르지 않는다 — SiteFromName 과 같은 파서가 확실하다고
// 판정한 이름에서만 추출하고, 카테고리 주기 정합은 MatchesName 이 가진 규칙을
// 그대로 쓴다. Daily 카테고리는 이름에 시각이 없으므로 항상 식별 불가다.
// Daily 목적지에 (HH) 를 적은 설정은 config 검증이 시작 단계에서 거부하므로
// 정상 운영에서는 이 갈래에 도달하지 않는다.
//
//	RINEX2 Hourly (짧은 이름):  parseShortName 이 definite 이고 MatchesName 이
//	                            OK 일 때 세션 문자 'a'~'x' → "00"~"23"
//	RINEX3·4 Hourly (긴 이름):  longSetKeyKind 가 ok 이고 MatchesName 이 OK 일
//	                            때 타임스탬프 필드 YYYYDDDHHMM 의 HH
//
// 반환 계약은 SiteFromName·SetKeyKind 와 같은 이원 구조다:
//
//	err != nil:
//	  라우팅이 누락된 카테고리다. 호출자는 오류를 전파해야 하며,
//	  식별 불가 파일로 취급하면 안 된다.
//
//	err == nil && !ok:
//	  이 카테고리의 이름 규약으로 시각을 식별할 수 없다.
//	  처리 방침(제외·집계)은 호출자 정책이다 — 결정 기록 §4.3-5 에 따라
//	  해당 파일만 제외하고 unidentified 로 센다. 전체를 되돌리는 fallback 은 없다.
//
// 사전 조건: normName 은 NormalizeName 을 거친 값이다(소문자).
func HourFromName(cat Category, normName string) (hh string, ok bool, err error) {
	switch cat {
	case CategoryRINEX2Daily,
		CategoryRINEX3Daily,
		CategoryRINEX4Daily:
		// Daily 이름에는 시각 필드가 없다. 짧은 이름의 세션 '0', 긴 이름의
		// 타임스탬프 HHMM 은 관측 시작이 아니라 하루의 시작을 뜻하므로
		// 시각 폴더의 재료로 쓰지 않는다.
		return "", false, nil

	case CategoryRINEX2Hourly:
		session, definite := parseShortName(normName)
		if !definite {
			return "", false, nil
		}

		// parseShortName 은 Category 판정에 불필요한 관측소 4자리와 DOY의
		// 값 범위를 의도적으로 검사하지 않는다. 하지만 HourFromName 은
		// 실제 목적지 폴더를 만드는 값의 출처이므로, 형태만 닮은 이름을
		// 정상 시각으로 승격시키면 안 된다.
		base := BaseName(normName)
		if !isASCIIAlnum(base[:siteCodeLen]) ||
			!validShortDayOfYear(base[4:7]) {
			return "", false, nil
		}

		// Daily 이름('0')이 Hourly 카테고리에 섞인 경우 등, 구조는 맞아도
		// 이 카테고리의 유효 이름이 아니면 식별 성공으로 세지 않는다.
		// SiteFromName 과 같은 이유다 (SITE §5).
		if cat.MatchesName(normName) != CategoryMatchOK {
			return "", false, nil
		}

		// MatchesName OK 인 Hourly 짧은 이름의 세션은 'a'~'x' 뿐이다.
		// 방어적으로 한 번 더 본다 — 여기서 어긋나면 위 두 함수의 계약이
		// 바뀐 것이므로 조용히 "00" 을 내보내지 않는다.
		if session < 'a' || session > 'x' {
			return "", false, nil
		}

		return fmt.Sprintf("%0*d", hourCodeLen, int(session-'a')), true, nil

	case CategoryRINEX3Hourly,
		CategoryRINEX4Hourly:
		if _, _, ok := longSetKeyKind(normName); !ok {
			return "", false, nil
		}

		// longSetKeyKind 는 주기 필드의 형태만 본다. 01D·15M 도 통과하므로
		// Hourly 정합은 MatchesName 으로 확인한다.
		if cat.MatchesName(normName) != CategoryMatchOK {
			return "", false, nil
		}

		// longSetKeyKind 성공으로 세 번째 필드가 11자리 숫자(YYYYDDDHHMM)임이
		// 보장된다. 여기서는 경로 값의 출처로 사용하기 전에 연도별 DOY와
		// HH:MM 범위까지 확인한다.
		fields := strings.SplitN(BaseName(normName), "_", longSetKeyPrefixFields)
		ts := fields[2]
		if !validLongObservationTime(ts) {
			return "", false, nil
		}

		hh = ts[7 : 7+hourCodeLen]

		return hh, true, nil

	default:
		// 새 카테고리를 추가하면 이 라우팅도 함께 갱신한다.
		// SiteFromName 과 같은 원칙 — 라우팅 누락을 조용한 식별 불가로
		// 접으면 새 카테고리의 (HH) 목적지가 오류 한 번 없이 전부
		// 제외되는 침묵 실패가 된다.
		return "", false, fmt.Errorf(
			"domain: hour extraction not defined for category %q",
			cat,
		)
	}
}

// validShortDayOfYear 는 RINEX2 짧은 이름의 DDD가 가능한 범위인지 본다.
// 연도는 두 자리라 세기 해석이 모호하므로 366의 윤년 여부까지 단정하지
// 않고, 어느 연도에도 존재하지 않는 000과 367 이상만 거부한다.
func validShortDayOfYear(ddd string) bool {
	return len(ddd) == 3 && isDigits(ddd) && ddd >= "001" && ddd <= "366"
}

// validLongObservationTime 은 RINEX3/4의 YYYYDDDHHMM가 실제 가능한
// 관측 시작 시각인지 확인한다. longSetKeyKind와 독립적으로 호출해도
// panic이나 잘못된 성공을 내지 않도록 길이와 숫자 형태도 함께 검사한다.
func validLongObservationTime(ts string) bool {
	if len(ts) != longTimestampLength || !isDigits(ts) {
		return false
	}

	year, _ := strconv.Atoi(ts[:4])
	doy, _ := strconv.Atoi(ts[4:7])
	hour, _ := strconv.Atoi(ts[7:9])
	minute, _ := strconv.Atoi(ts[9:11])

	if year == 0 || hour > 23 || minute > 59 {
		return false
	}

	maxDOY := 365
	if year%400 == 0 || (year%4 == 0 && year%100 != 0) {
		maxDOY = 366
	}

	return doy >= 1 && doy <= maxDOY
}
