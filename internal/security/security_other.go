//go:build !windows

package security

import "errors"

// DPAPI 는 비 Windows 환경의 대응물이다.
//
// 평문은 그대로 통과시키고, enc: 값은 명시적으로 거부한다.
// 복호화할 수 없는 enc: 값을 리터럴로 통과시키면
// enc:AQAA... 같은 값을 실제 설정값으로 사용하는 잘못된 실행이
// 가능하기 때문이다.
//
// 이 구현의 역할은 다음과 같다.
//   - 개발/CI(Linux)에서 config 패키지가 컴파일·테스트되게 한다.
//   - 평문 config 로 동작하는 localfs 검증 경로를 지원한다.
//
// Linux 운영 배포의 자격증명 보호 방식은 추후 별도로 결정한다.
// Protector 인터페이스를 확장점으로 두어 config 등 상위 계층에
// 플랫폼별 보호 방식이 퍼지지 않도록 한다.
type DPAPI struct{}

// New 는 이 플랫폼의 구현을 반환한다.
func New() DPAPI { return DPAPI{} }

// Protect 는 비 Windows 환경에서 지원하지 않는다.
//
// 조용히 평문을 반환하면 "암호화된 줄 알았지만 실제로는 평문"인
// 상태가 되므로 명시적으로 실패한다.
func (DPAPI) Protect(string) (string, error) {
	return "", errors.New(
		"security: 이 플랫폼에서는 값 암호화를 지원하지 않는다 (Windows 전용)",
	)
}

// Resolve 는 config.Protector 를 구현한다.
//
//	평문 → (값, false, nil)
//	enc: → 오류
//
// 비 Windows 환경에서는 enc: 값을 복호화할 수 없으므로
// 리터럴 값으로 통과시키지 않고 명시적으로 실패한다.
func (DPAPI) Resolve(value string) (string, bool, error) {
	if isEncrypted(value) {
		return "", false, errors.New(
			"security: 이 플랫폼에서는 enc: 값을 복호화할 수 없다 (Windows 전용)",
		)
	}

	return value, false, nil
}
