package domain

import (
	"fmt"
	"strings"
)

// SITE 선택 공통 규칙 (SITE 설계 v1, RESEND v4 §6.3 「SITE 매칭 [확정]」).
//
// CLI 입력 파싱(ParseSiteList)과 파일명에서의 관측소 추출(SiteFromName)을
// 이 파일이 소유한다. PUT resend 와 향후 DOWNLOAD 가 같은 규칙을 공유한다
// (SITE §1). 선택 정책(게이트 우회·재시도·후보 제외 방식·Recover 범위)은
// 여기 두지 않는다 — 그 정책은 각 방향의 소유자가 정한다 (SITE §4).
//
// 매칭 계약: "파일에서 뽑은 코드와 정확 일치" (v4 §6.3). 두 함수 모두
// 대문자 4자리를 돌려주므로, 호출자는 문자열 동등 비교만 하면 되고
// 별도의 매칭 함수를 두지 않는다. 대소문자 무시는 여기의 대문자 정규화가
// 소유하며, 호출자가 다시 대소문자를 접지 않는다.

// siteCodeLen 은 관측소 코드 길이다. CLI 정석은 4자리이며(9자리 긴
// 식별자는 받지 않는다), RINEX3·4 긴 이름의 9자리 첫 필드에서도
// 앞 4자리만 관측소 코드로 쓴다.
const siteCodeLen = 4

// ParseSiteList 는 --site 인자 원문을 정규화한 관측소 코드 목록으로 바꾼다.
//
// 규칙 (v4 §6.3 SITE 매칭 [확정]):
//   - 쉼표 구분 복수 허용. 각 항목은 공백 제거·대소문자 무시로 다룬다.
//   - 각 값은 ASCII 영숫자 정확히 4자리다. 네 번째가 숫자인 코드(SUW1)도
//     허용한다. 9자리(DBON00KOR)·와일드카드·그 외 길이는 오류다.
//   - 중복은 제거한다. 같은 관측소를 두 번 적은 것은 의도가 명확하고
//     동작에 무해하므로, RequiredKinds 의 중복 오류와 달리 조용히 접는다.
//   - 빈 값·빈 목록 요소는 오류다. "--site 생략"(전 관측소)과 명시적
//     빈 값은 다르며, 생략은 호출자가 이 함수를 부르지 않는 것으로
//     표현한다.
//
// 반환 목록은 대문자이며 입력에 처음 등장한 순서를 유지한다.
//
// SITE v1 §3.1의 대문자 정규화를 따른다. 파일 추출값도 같은 형식으로
// 반환하여 추가 변환 없이 정확 일치 비교와 로그 표기에 사용한다.
func ParseSiteList(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))

	for _, p := range parts {
		// 대소문자 변환 전에 ASCII를 검사한다. 유니코드 변환으로
		// 비ASCII 문자가 ASCII로 바뀌어 잘못 허용되는 것을 막는다.
		s := strings.TrimSpace(p)
		if s == "" {
			return nil, fmt.Errorf(
				"domain: --site 에 빈 항목이 있습니다: %q", raw,
			)
		}

		if len(s) != siteCodeLen {
			return nil, fmt.Errorf(
				"domain: 관측소 코드는 정확히 4자리입니다 "+
					"(9자리 긴 식별자는 --site 값으로 받지 않습니다): %q", p,
			)
		}

		if !isASCIIAlnum(s) {
			return nil, fmt.Errorf(
				"domain: 관측소 코드에 영숫자 외 문자가 있습니다 "+
					"(와일드카드는 지원하지 않습니다): %q", p,
			)
		}

		s = strings.ToUpper(s)

		if seen[s] {
			continue
		}

		seen[s] = true
		out = append(out, s)
	}

	return out, nil
}

// isASCIIAlnum 은 s 의 모든 바이트가 ASCII 영문(대·소)·숫자인지 답한다.
// setkey_long.go 의 isAlnumOfLen 은 소문자만 받으므로 CLI 원문 검사
// (정규화 전)에는 쓸 수 없다.
func isASCIIAlnum(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return false
		}
	}

	return true
}

// SiteFromName 은 정규화된 파일명에서 관측소 코드(대문자 4자리)를 추출한다.
//
// 임의 파일명의 앞 글자를 자르지 않는다 — 기존 파일명 파서가 확실하다고
// 판정한 이름에서만 추출한다. 구조 판정은 세트 키와 같은 파서를 재사용하고,
// Daily/Hourly 소속은 MatchesName 이 이미 가진 규칙을 그대로 쓴다.
// 구조는 맞는데 카테고리 주기가 다른 이름(Daily 자리에 Hourly 세션 등)과
// 15m 처럼 주기가 유보인 이름은 식별 성공으로 세지 않는다. 짧은 이름의
// 관측소 문자 검사는 여기서 추가하므로, 세트 키가 있어도 site 는
// 식별 불가일 수 있다.
//
//	RINEX2 (짧은 이름):  parseShortName 이 definite 이고 MatchesName 이
//	                     OK 일 때 앞 4자
//	RINEX3·4 (긴 이름):  longSetKeyKind 가 ok 이고 MatchesName 이 OK 일 때
//	                     첫 필드(9자리)의 앞 4자. 9자리 전체를 대조하지 않는다.
//
// 반환 계약은 SetKeyKind 와 같은 이원 구조다:
//
//	err != nil:
//	  라우팅이 누락된 카테고리다. 호출자는 오류를 전파해야 하며,
//	  식별 불가 파일로 취급하면 안 된다.
//
//	err == nil && !ok:
//	  이 카테고리의 이름 규약으로 관측소를 식별할 수 없다.
//	  전송 거부·허용을 뜻하지 않는다. 처리 방침(제외·집계)은 호출자
//	  정책이다 — 전체 대상으로 되돌리는 fallback 은 없다 (SITE §4).
//
// 사전 조건: name 은 NormalizeName 을 거친 값이다(소문자).
// put 리포트의 stationOf(관측용 앞 4자 자르기)와 다르다 — 그쪽은 분포
// 집계일 뿐이고, 이쪽은 선택 필터의 판정 재료라 형태 확신이 필요하다.
func SiteFromName(cat Category, normName string) (site string, ok bool, err error) {
	switch cat {
	case CategoryRINEX2Daily,
		CategoryRINEX2Hourly:
		if _, definite := parseShortName(normName); !definite {
			return "", false, nil
		}

		// Daily/Hourly 혼입은 구조가 맞아도 이 카테고리의 유효 이름이 아니다.
		// MatchesName 이 Mismatch 인 파일을 식별 성공으로 세면, 이후
		// site 필터가 "불일치"와 "식별 불가"를 뒤바꾼다 (SITE §5).
		if cat.MatchesName(normName) != CategoryMatchOK {
			return "", false, nil
		}

		// parseShortName 성공으로 BaseName 첫 조각의 길이 8이 보장된다.
		// 단 parseShortName 은 ssss(관측소) 문자를 검사하지 않는다
		// (Category 대조에는 불필요하다는 그쪽 주석 참조). 여기서는 site
		// 판정 재료이므로 영숫자를 따로 요구한다 — 없으면 "db-n"·"____"
		// 같은 값이 식별 성공으로 나가 "식별 불가"가 아니라 "불일치"로
		// 잘못 집계된다(SITE §5). 긴 이름은 longSetKeyKind 가 이미 검사한다.
		site := BaseName(normName)[:siteCodeLen]
		if !isASCIIAlnum(site) {
			return "", false, nil
		}

		return strings.ToUpper(site), true, nil

	case CategoryRINEX3Daily,
		CategoryRINEX3Hourly,
		CategoryRINEX4Daily,
		CategoryRINEX4Hourly:
		if _, _, ok := longSetKeyKind(normName); !ok {
			return "", false, nil
		}

		// longSetKeyKind 는 주기 형태(숫자2+영문1)만 보고 01D/01H 와
		// 카테고리의 정합은 보지 않는다. 15m 도 형태는 통과한다.
		// site 선택은 MatchesName 이 OK 인 이름만 식별한다.
		if cat.MatchesName(normName) != CategoryMatchOK {
			return "", false, nil
		}

		// longSetKeyKind 성공으로 첫 필드가 9자리 영숫자임이 보장된다.
		fields := strings.SplitN(BaseName(normName), "_", 2)

		return strings.ToUpper(fields[0][:siteCodeLen]), true, nil

	default:
		// 새 카테고리를 추가하면 이 라우팅도 함께 갱신한다.
		// SetKeyKind 의 err/유보 이원 계약과 같은 원칙 — 라우팅 누락을
		// 조용한 식별 불가로 접으면 새 카테고리의 site 필터가 오류 한 번
		// 없이 전부 제외되는 침묵 실패가 된다.
		return "", false, fmt.Errorf(
			"domain: site extraction not defined for category %q",
			cat,
		)
	}
}
