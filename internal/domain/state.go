package domain

import "fmt"

// State 는 common_ledger 에 등록된 파일의 입고 상태이다.
//
// 주의: 이 값은 전송 후보를 거르는 필터가 아니다.
// READY 와 CHANGED 모두 전송 대상이 될 수 있으며,
// 재전송 여부는 put_ledger 와의 revision 매칭으로 결정한다. (CONCEPT 4.5)
//
// State 를 두는 목적은 운영 가시성이다.
// 동일 파일명이 size 또는 mtime 변화로 재관측된 경우 CHANGED 로 기록하여
// 파일 재생성·보정 현상이 실제 운영에서 얼마나 발생하는지 확인할 수 있다.
//
// schema.sql common_ledger.state 의 CHECK 제약과 같은 값이다.
type State string

const (
	// StateReady 는 Ingress 검증을 통과하여 처음 등록된 파일의 상태이다.
	StateReady State = "READY"

	// StateChanged 는 이미 등록된 파일이 서로 다른 size 또는 mtime 으로
	// 재관측되어 revision 이 증가한 상태이다.
	//
	// 한 번 CHANGED 가 된 파일은 이후 다시 READY 로 되돌리지 않는다.
	StateChanged State = "CHANGED"
)

// ParseState 는 DB 에서 읽은 문자열을 State 로 변환한다.
//
// DB 의 State 값은 schema.sql CHECK 제약 및 domain 상수와 정확히 일치해야 한다.
// 잘못된 대소문자나 공백을 자동 보정하지 않는다.
// 스키마 버전 불일치나 비정상 데이터를 조용히 받아들이지 않기 위함이다.
func ParseState(s string) (State, error) {
	v := State(s)
	switch v {
	case StateReady, StateChanged:
		return v, nil
	default:
		return "", fmt.Errorf("unknown state %q", s)
	}
}

// Valid 는 정의된 State 값인지 답한다.
//
// 이미 State 타입인 값의 유효성만 확인할 때 사용한다.
// 문자열에서 변환하는 경우에는 ParseState 를 쓴다.
func (s State) Valid() bool {
	_, err := ParseState(string(s))
	return err == nil
}
