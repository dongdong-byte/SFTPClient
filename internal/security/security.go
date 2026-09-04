// Package security 는 설정 값의 OS 자격증명 보호를 담당한다.
//
// 보호 값의 저장 형식:
//
//	enc:<base64(DPAPI blob)>
//
// 이 형식의 소유자는 security 패키지 하나다.
// config 는 enc: 접두어, base64, DPAPI 를 알지 않고
// config.Protector.Resolve 만 호출한다.
//
// Windows 구현은 DPAPI LocalMachine 스코프를 사용한다.
// 상세 설계 및 기각 이력은 docs/SFTPClient_SECURITY_ENC_DESIGN.md 를 따른다.
//
// 이 패키지의 오류 메시지는 한국어다.
// 운영자가 config.ini 암호문을 이 PC에서 다시 만들어야 하는 상황을
// 바로 읽히게 하기 위함이며, 다른 패키지의 영어 오류와 섞이는 것은 수용한다.
package security

import "strings"

// encPrefix 는 보호된 설정 값의 접두어다.
//
// Protect 가 붙이고 Resolve 가 판정하므로,
// "무엇이 보호 값인가"에 대한 정의는 이 패키지에만 존재한다.
const encPrefix = "enc:"

// isEncrypted 는 값이 보호 형식인지 판정한다.
//
// 여기서는 접두어만 판정한다.
// base64 유효성 및 실제 복호화 가능 여부는 Resolve 가 검증한다.
func isEncrypted(value string) bool {
	return strings.HasPrefix(value, encPrefix)
}
