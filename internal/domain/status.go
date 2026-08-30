package domain

import "fmt"

// Status 는 파일 하나의 전송 이력 상태이다. (설계안 9.3)
//
// 설계안 8.1 과 13.2 는 SUCCESS, 9.3 은 VERIFIED 로 표기가 엇갈린다.
// 상태 전이를 명시적으로 정의한 9.3 을 따라 VERIFIED 로 통일한다.
// SUCCESS 라는 문자열을 코드 어디에도 두지 않는다.
//
// schema.sql put_ledger.status 의 CHECK 제약과 같은 값이다.
type Status string

const (
	// StatusPending — 전송 후보로 선정되었으나 아직 시작하지 않았다.
	StatusPending Status = "PENDING"
	// StatusInProgress — 업로드 중이다.
	// 이 상태로 전환할 때 remote_path 와 part_path 를 같은 트랜잭션 안에서
	// 기록해야 중단 시 잔여 .part 파일을 찾을 수 있다. (설계안 9.3, 13.2)
	StatusInProgress Status = "IN_PROGRESS"
	// StatusVerified — 목적지 파일의 존재와 Size 대조까지 마친 상태이다. (설계안 8.1)
	// 전송 함수가 오류 없이 끝났다는 사실만으로는 이 상태가 되지 않는다.
	StatusVerified Status = "VERIFIED"
	// StatusFailed — 전송 또는 검증에 실패한 상태이다.
	// 후보 선정 시 재시도 대상으로 다시 선택될 수 있다.
	StatusFailed Status = "FAILED"
)

// ParseStatus 는 DB 에서 읽은 문자열을 Status 로 변환한다.
//
// DB 의 Status 값은 schema.sql CHECK 제약 및 domain 상수와 정확히 일치해야 한다.
// 잘못된 대소문자나 공백을 자동 보정하지 않는다.
// 스키마 버전 불일치나 비정상 데이터를 조용히 받아들이지 않기 위함이다.
func ParseStatus(s string) (Status, error) {
	v := Status(s)
	switch v {
	case StatusPending,
		StatusInProgress,
		StatusVerified,
		StatusFailed:
		return v, nil
	default:
		return "", fmt.Errorf("unknown status %q", s)
	}
}

// CanTransitionTo 는 현재 상태에서 next 로 전이할 수 있는지 답한다.
//
// 기본 전이는 다음과 같다.
//
//	PENDING → IN_PROGRESS
//	PENDING → FAILED
//	IN_PROGRESS → VERIFIED
//	IN_PROGRESS → FAILED
//
// PENDING → FAILED 를 허용하는 이유:
// 업로드 시작 전 실패(Path Template 확장 실패, 로컬 파일 소실 등)는
// .part 를 만들지 않는 전이이므로 IN_PROGRESS 를 거칠 수 없다.
// IN_PROGRESS 는 remote_path·part_path 를 기록하는 시점과 대응하므로,
// .part 가 없는 실패를 그 상태로 올리면 재시작 시 잔여 .part 정리 절차가
// 존재하지 않는 파일을 찾으려 한다.
//
// 여기에 운영 복구·재시도를 위해 다음 전이를 허용한다.
//
//	IN_PROGRESS → FAILED
//	  비정상 종료 후 잔여 .part 를 지운 뒤 FailPut 으로 되돌리는 회수 경로다.
//	  설계안 9.3 의 "PENDING 으로 되돌린다" 는 폐기했다.
//	  attempts 집계와 BeginPut(FAILED→IN_PROGRESS) 재시도와 맞추기 위함이다.
//	  (GUIDELINES 5절 「2026-08-30 확정」 ②, schema.sql put_ledger.status 주석)
//
//	FAILED → IN_PROGRESS
//	  실패한 동일 revision 을 재시도하는 경로이다 (BeginPut).
//
// VERIFIED 는 해당 revision 의 종료 상태이다.
// 같은 파일이 갱신되어 다시 전송되는 경우 기존 상태를 되돌리지 않고,
// revision 이 증가한 새로운 전송 이력으로 처리한다.
func (s Status) CanTransitionTo(next Status) bool {
	switch s {
	case StatusPending:
		return next == StatusInProgress || next == StatusFailed
	case StatusInProgress:
		return next == StatusVerified || next == StatusFailed
	case StatusFailed:
		return next == StatusInProgress
	case StatusVerified:
		return false
	default:
		return false
	}
}

// IsTerminal 은 해당 revision 에서 더 이상 상태 전이가 없는지 답한다.
//
// VERIFIED 는 최종 상태이다.
// FAILED 는 재시도할 수 있으므로 최종 상태가 아니다.
func (s Status) IsTerminal() bool {
	return s == StatusVerified
}

// Valid 는 정의된 Status 값인지 답한다.
func (s Status) Valid() bool {
	_, err := ParseStatus(string(s))
	return err == nil
}
