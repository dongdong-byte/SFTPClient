package domain

import "fmt"

// SetKeyKind는 카테고리와 파일명에서 세트 키와 데이터 종류를 도출한다.
//
// 파일명 파생 규칙은 domain에서 관리한다.
// 게이트, Ledger 기록, 마이그레이션 백필은 이 함수를 공통으로 사용한다.
//
// 반환 계약:
//
//	err != nil:
//	  지원하지 않거나 라우팅이 누락된 카테고리다.
//	  호출자는 오류를 전파해야 하며, 파싱 불가 파일로 취급하면 안 된다.
//
//	err == nil && !ok:
//	  해당 계열의 파일명으로 해석할 수 없어 세트 소속을 판정하지 못했다.
//	  알려진 비표준 이름과 잘못된 이름이 모두 포함될 수 있다.
//	  이 결과는 "세트 대상이 아님"이나 "전송 허용"을 뜻하지 않는다.
//
//	err == nil && ok:
//	  setKey와 kind를 도출했다. 두 값은 소문자다.
//	  kind에는 표현 형식(.rnx/.crx)이나 압축 확장자가 포함되지 않는다.
//
// 이 함수는 값을 도출할 뿐 전송 여부를 결정하지 않는다.
// 판정 불가(ok=false) 파일을 포함해 각 결과를 어떻게 다룰지는
// 호출자의 정책이 정한다 — 게이트 동작은 put 패키지가,
// Ledger 기록 방식은 ledger 패키지가 소유한다.
//
// 사전 조건:
//   - config는 Category와 명시적 RinexVersion의 일치를 검증해야 한다.
//   - 전송 경로는 임시 파일(.part)을 이 함수 호출 전에 제외해야 한다.
//     내부 정규화가 .part를 제거하므로 이 함수는 임시 파일 검출용이 아니다.
//
// 이 함수는 파일명에서 값을 도출할 뿐, 파일 내용이나 세트 완성도를 검증하지 않는다.
func SetKeyKind(
	cat Category,
	name string,
) (setKey, kind string, ok bool, err error) {
	switch cat {
	case CategoryRINEX2Daily,
		CategoryRINEX2Hourly:
		setKey, kind, ok = shortSetKeyKind(name)

	case CategoryRINEX3Daily,
		CategoryRINEX3Hourly,
		CategoryRINEX4Daily,
		CategoryRINEX4Hourly:
		// RINEX3/4는 같은 긴 파일명 파서를 사용한다.
		// 필수 kind 목록은 파서가 아닌 RequiredKinds에서 결정한다.
		setKey, kind, ok = longSetKeyKind(name)

	default:
		// 새 카테고리를 추가하면 이 라우팅도 함께 갱신한다.
		return "", "", false, fmt.Errorf(
			"domain: set_key/kind not defined for category %q",
			cat,
		)
	}

	if !ok {
		// 실패 시 부분적으로 도출된 값이 호출자에게 전달되지 않게 한다.
		return "", "", false, nil
	}

	return setKey, kind, true, nil
}

// RinexVersion은 지원하는 Category에 대응하는 RINEX major version을 반환한다.
//
// config에 명시된 RinexVersion을 대조할 기준값이다.
// Category는 문자열 기반 타입이므로 상수 외의 값도 전달될 수 있다.
// 지원하지 않는 값이나 매핑이 누락된 카테고리는 오류로 반환한다.
func (c Category) RinexVersion() (int, error) {
	switch c {
	case CategoryRINEX2Daily,
		CategoryRINEX2Hourly:
		return 2, nil

	case CategoryRINEX3Daily,
		CategoryRINEX3Hourly:
		return 3, nil

	case CategoryRINEX4Daily,
		CategoryRINEX4Hourly:
		return 4, nil

	default:
		return 0, fmt.Errorf(
			"domain: rinex version not defined for category %q",
			c,
		)
	}
}
