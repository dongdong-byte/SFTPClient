// Package scanwindow 는 resend 스캔 창의 날짜 정책을 계산한다
// (resend 설계 v4 §3.3·§3.5).
//
//	today  = 실행 시각의 UTC 날짜
//	limit  = today − (RetentionDays − 2)      ; Retention 한계
//	origin = 장부 운영 시작일 (ledger.OperationOrigin, §3.5)
//
//	자동 창:  From = max(limit, origin),  To = today − ScanDays
//	수동:     From >= limit, To <= today, From <= To  (위반 시 거부,
//	          origin 은 수동에 적용하지 않는다 — §3.5)
//
// §3.3 의 "한계 계산은 하나의 함수로 두고 자동·수동·경로 범용화가 같이
// 쓴다"가 이 패키지다. Auto 와 ValidateManual 은 각자 식을 다시 쓰지
// 않고 RetentionLimit 을 호출한다.
//
// 배치 근거 (커밋 5 검토 v1 §1 — 재론 방지):
//   - put 이 아니다. put 이 시간 정책(Retention 여유일, origin 하한)을
//     소유하게 되고, put 이 쓰지 않는 ValidateManual 까지 품는다.
//     기존 책임 배치는 "창 이름은 main 이 알고, put 은 받은 scan.Range
//     만 스캔한다"이며 Hot/Deep 도 그렇게 되어 있다.
//   - cmd 가 아니다. main.go 에 비즈니스 로직을 두지 않는 프로젝트
//     원칙과, v4 §9 경로 범용화가 같은 limit 을 재사용한다는 확정
//     때문이다. main 패키지는 다른 internal 패키지가 import 할 수 없다.
//   - scan 부착도 기각. scan 은 "Hot / Deep / resend 도 구분하지
//     않는다. 차이는 Range 뿐"이 계약이다. 정책 함수를 붙이면 나열
//     패키지가 resend 시간 정책을 알게 된다. scan.Range 를 반환값으로
//     쓰는 것과 정책을 scan 에 두는 것은 다르다.
//
// 순수성 계약: 이 패키지는 time.Now() 를 부르지 않고 로그를 남기지
// 않으며 ledger·config 를 import 하지 않는다. today·origin 은 호출자가
// 확보한 값으로 넘긴다 (origin 은 ledger.OperationOrigin 결과 — 조회
// 오류는 호출자가 전파하고, zero 값이나 Retention 한계로 대체하지
// 않는다). retentionDays·scanDays 는 config validate 를 통과한 값
// (양수, RetentionDays > ScanDays)을 전제한다. 잘못된 설정을 여기서
// 빈 창으로 숨기지 않는다 — 검증의 주인은 config 다.
//
// 모든 계산은 UTC 날짜(자정) 기준이다. 자정이 아닌 시각이 들어와도
// 내부에서 UTC 자정으로 내린다.
package scanwindow

import (
	"errors"
	"fmt"
	"time"

	"SFTPClient/internal/scan"
)

// retentionMargin 은 Retention 한계의 여유 일수다 (v4 §3.3).
//
// 여유 1일(수학적 안전선 RetentionDays−1 에서 하루 더)은 "관측일 ≤
// 입고 시각" 가정이 날짜 경계·시계 오차·파일명 오기로 조금 어긋나는
// 경우를 흡수한다. Retention Cleanup 이 지우기 직전의 날을 resend 로
// 건드리면, 장부에서만 지워진 파일이 신규로 판정되어 재전송된다.
const retentionMargin = 2

// ValidateManual 의 센티널 오류. 커밋 8 의 CLI 가 메시지·종료 코드를
// 정할 때 문자열 매칭을 하지 않도록 값으로 구분한다 (검토 v1 §1.3).
var (
	ErrFromAfterTo          = errors.New("scanwindow: from is after to")
	ErrBeforeRetentionLimit = errors.New("scanwindow: from is before retention limit")
	ErrToInFuture           = errors.New("scanwindow: to is in the future")
	// ErrInvalidOrigin — origin 이 운영 시작일로 쓸 수 없다.
	// unix 0·zero Time 을 UTC 날짜로 접으면 0001/1970 이 되어
	// max(limit, origin) 이 limit 이 되고, 설치 직후 자동 창이
	// Retention 구간 전체를 연다. 빈 창으로 숨기지 않고 거부한다.
	ErrInvalidOrigin = errors.New("scanwindow: origin is not a valid operating start date")
)

// EmptyReason 은 자동 창이 비었을 때의 사유다.
//
// bool 로 두지 않는 이유(검토 v1 §1.3): 호출자가 로그 수준을 가리려면
// "왜 비었는지"가 필요한데, bool 이면 origin 과 limit 을 다시 비교하게
// 되어 같은 계산이 두 곳에 생긴다. 사유를 값으로 돌려주면 v4 §3.3
// (WARN)과 §3.5(INFO)의 로그 수준 충돌이 데이터로 닫힌다.
type EmptyReason int

const (
	// EmptyNone — 창이 비지 않았다.
	EmptyNone EmptyReason = iota

	// EmptyRetentionTooShort — limit > To, 즉 RetentionDays <
	// ScanDays + 2. 자동 창을 확보하지 못한 설정이다. 호출자는
	// WARN 을 남긴다 (§3.3).
	EmptyRetentionTooShort

	// EmptyBeforeOrigin — 설정은 창을 허용하나(limit <= To) 운영
	// 시작일이 아직 창에 닿지 않았다(origin > To). 운영 시작 후
	// ScanDays 가 지나기 전의 정상 상태다. 호출자는 INFO 를 남긴다
	// (§3.5).
	EmptyBeforeOrigin
)

// String 은 로그 표기용이다.
func (r EmptyReason) String() string {
	switch r {
	case EmptyNone:
		return "none"
	case EmptyRetentionTooShort:
		return "retention_too_short"
	case EmptyBeforeOrigin:
		return "before_origin"
	default:
		return fmt.Sprintf("unknown(%d)", int(r))
	}
}

// AutoResult 는 자동 resend 창 계산 결과다.
// Empty == (Why != EmptyNone) 을 보장한다. Empty 면 Range 는 zero 값이다.
type AutoResult struct {
	Range scan.Range
	Empty bool
	Why   EmptyReason
}

// RetentionLimit 은 resend 가 닿을 수 있는 가장 오래된 UTC 날짜다.
//
//	limit = today − (RetentionDays − 2)
//
// 자동(Auto)·수동(ValidateManual)·경로 범용화(v4 §9)가 이 함수 하나를
// 공유한다 — "하나의 함수" 확정(§3.3)이 코드로 보이는 지점이다.
func RetentionLimit(today time.Time, retentionDays int) time.Time {
	return utcDay(today).AddDate(0, 0, -(retentionDays - retentionMargin))
}

// Auto 는 자동 resend 창을 계산한다 (v4 §3.3·§3.5).
//
//	From = max(limit, origin),  To = today − ScanDays
//
// 양끝 포함이므로 RetentionDays = ScanDays + 2 이면 limit == To 인
// 1일짜리 유효 창이다. 빈 창은 RetentionDays < ScanDays + 2 (설정) 또는
// origin > To (운영 초기)에서 나며, 두 원인이 겹치면 설정 원인이
// 이긴다 — EmptyRetentionTooShort 가 INFO 뒤에 가려지면 자동 창이
// 영구히 없는 설정이 조용히 지나간다.
//
// origin 당일부터 포함한다(§3.5). 운영 시작 후 정확히 ScanDays 째에
// origin 당일 1일짜리 창이 처음 열린다.
//
// origin 의 UTC 연도가 2000 미만이면 오류다. zero Time·unix epoch 를
// "limit 보다 옛 origin"으로 취급하면 하한이 Retention 한계가 되어
// 첫 설치 자동 resend 가 수십 일을 신규로 보낸다. 오늘이나 limit 으로
// 대체하지 않는다 — 호출자가 OperationOrigin 오류를 전파하는 것과
// 같은 원칙이다.
func Auto(
	today time.Time,
	origin time.Time,
	retentionDays int,
	scanDays int,
) (AutoResult, error) {
	o := utcDay(origin)
	if o.Year() < 2000 {
		return AutoResult{}, fmt.Errorf(
			"%w: %s",
			ErrInvalidOrigin,
			o.Format(time.DateOnly),
		)
	}

	limit := RetentionLimit(today, retentionDays)
	to := utcDay(today).AddDate(0, 0, -scanDays)

	if limit.After(to) {
		return AutoResult{Empty: true, Why: EmptyRetentionTooShort}, nil
	}

	from := limit
	if o.After(from) {
		from = o
	}

	if from.After(to) {
		return AutoResult{Empty: true, Why: EmptyBeforeOrigin}, nil
	}

	return AutoResult{
		Range: scan.Range{From: from, To: to},
		Why:   EmptyNone,
	}, nil
}

// ValidateManual 은 수동 resend 기간을 검증한다 (v4 §3.3).
//
//	From >= limit, To <= today, From <= To
//
// origin 은 적용하지 않는다 — 운영 시작 전 누락분을 보내는 정규 경로가
// 수동 resend 다(§3.5). origin 이전 기간의 관측(before_origin= 로그)은
// 커밋 8 의 CLI 몫이다.
//
// Retention 밖을 강제로 여는 옵션은 두지 않는다. 위반은 거부하고
// 종료한다(§3.3) — 판정은 여기서, 메시지·종료는 CLI 에서.
//
// 날짜 문자열 해석은 CLI 책임이다. 이 함수는 time.Time 만 받는다.
func ValidateManual(
	from time.Time,
	to time.Time,
	today time.Time,
	retentionDays int,
) error {
	f := utcDay(from)
	t := utcDay(to)
	d := utcDay(today)
	limit := RetentionLimit(today, retentionDays)

	switch {
	case f.After(t):
		return fmt.Errorf(
			"%w: from=%s to=%s",
			ErrFromAfterTo,
			f.Format(time.DateOnly),
			t.Format(time.DateOnly),
		)

	case f.Before(limit):
		return fmt.Errorf(
			"%w: from=%s limit=%s (RetentionDays=%d)",
			ErrBeforeRetentionLimit,
			f.Format(time.DateOnly),
			limit.Format(time.DateOnly),
			retentionDays,
		)

	case t.After(d):
		return fmt.Errorf(
			"%w: to=%s today=%s",
			ErrToInFuture,
			t.Format(time.DateOnly),
			d.Format(time.DateOnly),
		)
	}

	return nil
}

// utcDay 는 시각을 UTC 기준 해당 날짜의 자정으로 내린다.
// scan.truncateToUTCDay 와 같은 규칙이다 (비공개라 재사용 불가).
func utcDay(t time.Time) time.Time {
	u := t.UTC()

	return time.Date(
		u.Year(), u.Month(), u.Day(),
		0, 0, 0, 0,
		time.UTC,
	)
}
