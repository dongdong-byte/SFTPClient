package domain

import (
	"fmt"
	"strings"
)

// Mode 는 이 인스턴스가 수행할 동작 방향이다. (설계안 6)
//
// config.ini 의 [GENERAL] Mode 에서 읽는다.
// 실행 중 Hot Reload 하지 않는다.
// 변경은 프로그램 종료 → config.ini 수정 → 재시작이 운영 원칙이다.
//
// Category / Origin / State / Status 와 달리 DB 에 저장되지 않는다.
// 따라서 schema.sql 의 CHECK 제약과 대응하지 않는다.
type Mode string

const (
	// ModePut 은 LocalPath 를 Scan 하여 원격으로 송신한다.
	ModePut Mode = "PUT"

	// ModeDownload 는 원격에서 파일을 수신하여 LocalPath 에 저장한다.
	ModeDownload Mode = "DOWNLOAD"

	// ModeBoth 는 DOWNLOAD 와 PUT 을 모두 수행한다.
	//
	// 실제 실행 순서와 Ping-Pong 방지 정책은 상위 orchestration/config 계층이
	// 결정한다. Mode 자체는 방향이라는 도메인 개념만 표현한다.
	ModeBoth Mode = "BOTH"
)

// Modes 는 지원하는 전체 Mode 를 선언 순서대로 반환한다.
//
// 여기 포함되어 있다는 것은 도메인 값으로 유효하다는 뜻이다.
// 현재 MVP 에서 실제 실행 가능한 Mode 인지는 config 또는 실행 계층이 판정한다.
func Modes() []Mode {
	return []Mode{
		ModePut,
		ModeDownload,
		ModeBoth,
	}
}

// ParseMode 는 config.ini 에서 읽은 문자열을 Mode 로 변환한다.
//
// 사람이 직접 작성하는 설정값이므로 앞뒤 공백과 대소문자는 허용한다.
// 반환값은 항상 위 상수 중 하나이다.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToUpper(strings.TrimSpace(s)))

	for _, known := range Modes() {
		if m == known {
			return known, nil
		}
	}

	return "", fmt.Errorf("unknown mode %q", s)
}

// Origin·State·Status 의 Valid 는 완전 일치를 요구하지만
// Mode 는 ParseMode 와 같은 기준이라 공백·대소문자를 허용한다.
// Mode 는 config 입력값이고 나머지는 DB 에서 읽는 값이기 때문이다.

// Valid 는 정의된 Mode 값인지 답한다.
//
// 이미 Mode 타입인 값의 유효성만 확인할 때 사용한다.
// 문자열에서 변환하는 경우에는 ParseMode 를 쓴다.
//
// 이 검사가 필요한 이유는 DoesPut 과 DoesDownload 가
// 알 수 없는 값에 대해 오류가 아니라 false 를 돌려주기 때문이다.
// Mode 가 빈 값이면 두 판정이 모두 false 가 되어
// 프로그램이 오류 없이 시작한 뒤 아무 방향도 수행하지 않는다.
func (m Mode) Valid() bool {
	switch m {
	case ModePut, ModeDownload, ModeBoth:
		return true
	default:
		return false
	}
}

// String 은 Mode 의 표기를 반환한다.
func (m Mode) String() string {
	return string(m)
}

// DoesPut 은 이 Mode 가 PUT 동작을 포함하는지 답한다.
//
// 호출부에서 ModePut 과 직접 비교하면 ModeBoth 를 빠뜨릴 수 있으므로
// 방향 판정은 이 함수에 모은다.
func (m Mode) DoesPut() bool {
	return m == ModePut || m == ModeBoth
}

// DoesDownload 는 이 Mode 가 DOWNLOAD 동작을 포함하는지 답한다.
func (m Mode) DoesDownload() bool {
	return m == ModeDownload || m == ModeBoth
}
