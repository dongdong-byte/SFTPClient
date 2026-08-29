// Package verify 는 scan 이 관측한 파일이 지금 전송해도 되는
// 완성 상태인지를 판정한다. 설계안 7 의 Ingress Verification 이다.
//
//	scan    사실   어떤 파일이 어디에, 크기·시각이 얼마인가
//	verify  판정   이 파일이 지금 전송해도 되는 완성 상태인가   ← 여기
//	ledger  기록   이미 보냈는가
//
// CONCEPT 의 불변식 "common_ledger 에 행이 있다 = Ingress 통과" 를 지키는
// 문지기다. 거부된 파일은 행을 만들지 않으며 다음 Scan 에서 다시 판정된다.
//
// 흐름상 위치는 List → Batch Lookup → 메모리 대조 → (신규·변경만) Verify
// → Upsert 이다. 전량이 아니라 신규·변경분에만 돈다. (SCAN DESIGN 6절)
//
// 3계층 방어(CONCEPT 4.4)에서 이 패키지가 맡는 것은 가운데뿐이다.
// size > 0 과 Grace Time 이 여기, 뚫린 것의 회수는 revision(ledger)이다.
// 완벽할 필요가 없으므로 얇게 간다.
//
// 하지 않는 것: Size 안정 재확인(실행 주기 1시간 > 성장 구간이라 효과 없음),
// 형식검사·압축 해제·헤더 파싱(SFTP 전송 프로그램이 RINEX parser 가 되는 길),
// 파일 존재 확인(scan 이 방금 나열로 관측했다), Windows 파일 잠금 검사
// (Linux 에 대응물이 없어 미채택. CHANGED 누적 관측 후 재검토),
// ledger 접근, 후보 판정, config import, 로그 출력.
//
// 판정 규칙의 주인은 여기가 아니다. 파일명 ↔ Category 대조 규칙은
// domain.MatchesName 이, .part 판정은 domain.IsPartFile 이 소유하며
// 이 패키지는 호출만 한다. 파일시스템도 시계도 직접 보지 않으므로
// (Now 주입) 실제 파일 없이 표 기반 테스트가 성립한다.
package verify

import (
	"fmt"
	"time"

	"SFTPClient/internal/domain"
)

// Input 은 판정 대상 파일 하나의 관측값이다.
//
// scan.Entry 를 import 하지 않는다. scan 과 verify 가 서로 모르게 두고,
// scan.Entry → Input 변환은 조립자인 put 이 한다. (consumer-side 원칙)
type Input struct {
	// Name 은 관측된 원본 이름이다. 정규화는 domain 이 내부에서 한다.
	Name string

	// Size 는 바이트 크기이다.
	Size int64

	// MTime 은 최종 수정시각이다.
	MTime time.Time

	// IsDir 은 이 항목이 디렉터리인지 나타낸다.
	IsDir bool

	// Category 는 config 섹션이 지정한 기대값이다.
	// 파일에서 추론한 값이 아니다.
	Category domain.Category
}

// Reason 은 판정 결과이다. ReasonNone 이 통과다.
//
// (ok bool, reason Reason) 으로 나누지 않는 이유는 reason == ReasonNone 과
// ok == true 가 같은 사실이라 주인이 둘이 되기 때문이다.
// UpsertCommon 이 revision 을 반환하지 않기로 한 것과 같은 방침이다.
//
// bool 이 아니라 열거형인 이유는 요약 로그다. 호출자는
//
//	rejected: grace=12 zero=1 mismatch=2 future=0
//
// 형태로 원인별 건수를 집계할 수 있고, Mismatch 와 FutureMTime 은
// 운영자가 확인해야 하는 신호이므로 다른 거부 사유와 구분할 필요가 있다.
//
// iota 를 사용하며 DB 에 저장하지 않는다. ReasonNone 을 제로값으로 두되,
// Verify 는 모든 거부 조건에서 즉시 해당 Reason 을 반환하고 끝까지
// 통과한 경우에만 ReasonNone 을 반환한다.
//
// 호출자는 반드시 Verify 의 반환값을 판정 결과로 사용해야 하며,
// 별도로 zero-value Reason 을 만들어 검증 결과로 간주하지 않는다.
type Reason int

const (
	// ReasonNone 은 모든 판정을 통과했음을 뜻한다.
	ReasonNone Reason = iota

	// ReasonIsDir — 디렉터리는 전송 대상이 아니다.
	ReasonIsDir

	// ReasonPartFile — .part 임시 접미사가 붙어 있다.
	//
	// 주 방어선은 put 이 Batch Lookup 목록을 만들 때의 사전 제외다.
	// .part 는 정규화하면 최종 파일명과 같은 키가 되어 IN 조회에서
	// 충돌하므로 lookup 앞에서 빠져야 한다. 여기의 검사는 Verify 를
	// 직접 호출하는 경로를 위한 2차 방어이며, 정상 흐름에서
	// part= 집계는 항상 0 이다. (VERIFY DESIGN 5절)
	ReasonPartFile

	// ReasonFutureMTime — mtime 이 현재보다 Grace 를 넘게 미래다.
	//
	// NAS 와 서버의 시계 어긋남 신호다. 이 파일은 나이 검사를 영구히
	// 통과하지 못한 채 매 스캔 재거부되다가 ScanDays 창을 조용히
	// 빠져나가 누락될 수 있으므로, grace= 에 섞지 않고 따로 센다.
	ReasonFutureMTime

	// ReasonTooRecent — 방금 쓰였다. Grace Time 미충족.
	//
	// 다음 Scan 에서 다시 판정되는 정상적인 대기 상태다.
	ReasonTooRecent

	// ReasonZeroSize — 크기가 0 이하라 유효한 전송 대상이 아니다.
	//
	// 실제 파일시스템에서 음수 크기는 관측되지 않지만,
	// Verifier 를 직접 호출하면서 잘못된 Input 이 전달되더라도
	// ledger 의 size > 0 계약과 일치하도록 0바이트와 동일하게 거부한다.
	//
	// 0바이트를 막는 것은 revision 이 아니라 이 검사다. (CONCEPT 4.4)
	ReasonZeroSize

	// ReasonCategoryMismatch — 파일명이 config 의 Category 와 명확히
	// 모순된다. config 오기입 또는 디렉터리 혼입(지리원 실사례)이다.
	// Unknown 은 여기 해당하지 않고 통과한다.
	ReasonCategoryMismatch
)

// OK 는 이 결과가 통과인지 답한다.
func (r Reason) OK() bool {
	return r == ReasonNone
}

// String 은 요약 로그의 집계 키를 반환한다.
//
// domain.CategoryMatch.String 과 달리 소문자 단문이다.
// 이 값은 사람이 읽는 서술이 아니라 rejected: grace=12 zero=1 형태의
// key=count 집계에 그대로 쓰이기 때문이다.
func (r Reason) String() string {
	switch r {
	case ReasonNone:
		return "none"
	case ReasonIsDir:
		return "dir"
	case ReasonPartFile:
		return "part"
	case ReasonFutureMTime:
		return "future"
	case ReasonTooRecent:
		return "grace"
	case ReasonZeroSize:
		return "zero"
	case ReasonCategoryMismatch:
		return "mismatch"
	default:
		// 숫자를 그대로 노출한다. domain.CategoryMatch.String 과 같은 방침이다.
		//
		// "unknown" 같은 고정 문자열을 돌려주면 Reason 을 추가하면서 이
		// switch 를 빠뜨렸을 때 집계가 unknown=340 으로 나온다. 정상적인
		// 집계 키처럼 보여 그대로 넘어가고, 둘을 빠뜨리면 합산되어
		// 어느 것이 누락됐는지도 알 수 없다. reason(7) 은 즉시 눈에 띈다.
		return fmt.Sprintf("reason(%d)", int(r))
	}
}

// Verifier 는 Ingress 판정기다.
//
// 값 리시버를 쓴다. 상태를 바꾸지 않고, 리소스를 들지 않으며,
// 크기가 16바이트라 복사 비용이 포인터와 다르지 않다.
// domain 의 값 타입들과 같은 성격이고 ledger.DB 처럼 복사하면 안 되는
// 자원을 감싸지 않는다.
//
// 값 리시버라서 얻는 것:
//
//	제로값이 그대로 유효하다.       Verifier{} 는 Grace=0, Now=nil 로 의미가 있다
//	nil 리시버 panic 이 없다.
//	주소를 잡을 수 없는 곳에서도 부를 수 있다.
//	  verifiers[cat].Verify(in)   ← Category 별로 다른 Grace 를 둘 경우
//	Verifier 와 *Verifier 가 모두 인터페이스를 만족한다.
type Verifier struct {
	// Grace 는 mtime 이 이 시간만큼 지난 파일만 통과시키는 나이 문턱이다.
	// config 의 [INGRESS] GraceSeconds 에서 온다.
	//
	// 0 이면 mtime 나이 검사(Future 포함)를 사용하지 않는다.
	// (config.IngressConfig.Grace 주석과 같은 계약)
	Grace time.Duration

	// Now 는 테스트 주입용 현재 시각이다. nil 이면 time.Now 를 쓴다.
	Now func() time.Time
}

// Verify 는 파일 하나의 Ingress 통과 여부를 판정한다.
//
// 순서는 싼 것 → 비싼 것, 사실 → 추정이며, 순서가 곧 진단의 정확성이다.
//
//	1 IsDir
//	2 PartFile
//	3 FutureMTime   mtime - now > Grace
//	4 TooRecent     now - mtime ≤ Grace
//	5 ZeroSize
//	6 CategoryMismatch
//
// Future(3) 를 Grace(4) 보다 먼저 본다. mtime > now 면 now - mtime 이
// 음수라 Grace 조건에 항상 걸리므로, 뒤에 두면 도달 불가능한 죽은 코드가
// 되고 시계 이상이 전부 grace= 에 섞여 이 검사를 둔 목적이 사라진다.
//
// Future 의 문턱은 strict(mtime > now)가 아니라 Grace 다. NAS 와 서버의
// 시계는 수 초 어긋나는 것이 정상이라, 방금 쓰인 파일이 +2초 미래로
// 보이는 일은 흔하다. strict 로 잡으면 future= 가 정상 파일로 매시간
// 오염되어 신호 가치가 죽는다. Grace 이내의 미래는 "방금 쓰임"
// (TooRecent)으로 흡수하고, 그것을 넘는 미래만 시계 이상으로 분류한다.
//
// Grace(4) 를 Zero(5) 보다 먼저 본다. 0바이트의 실제 원인은 "선행
// 프로그램이 만들고 1시간 내 채워지는 중"이다(CONCEPT 4.4). Zero 를
// 먼저 보면 그 파일들이 전부 zero= 로 집계되어 운영자가 "0바이트 12개
// = 데이터 이상"으로 읽는다. 실제로는 "12개가 쓰이는 중 = 정상"이므로
// 진단이 반대를 가리킨다. Grace 를 먼저 보면 grace=12 로 정확해지고,
// Grace 를 지나고도 0바이트인 것만 zero= 에 남아 진짜 신호가 된다.
func (v Verifier) Verify(in Input) Reason {
	if in.IsDir {
		return ReasonIsDir
	}

	if domain.IsPartFile(in.Name) {
		return ReasonPartFile
	}

	// Grace == 0 은 나이 검사를 끄는 의미다. 음수는 config.Validate 가
	// 거부하지만, Verifier 를 직접 구성하는 경로에서도 음수가 검사를
	// 뒤집지 않도록 0 과 같게 취급한다.
	if v.Grace > 0 {
		// now 는 이 블록에서 한 번만 읽는다.
		// Future 와 TooRecent 가 서로 다른 시각을 보면 두 판정 사이에
		// 틈이 생겨 어느 쪽에도 걸리지 않는 파일이 나올 수 있다.
		now := time.Now()
		if v.Now != nil {
			now = v.Now()
		}

		switch {
		case in.MTime.Sub(now) > v.Grace:
			return ReasonFutureMTime

		case now.Sub(in.MTime) <= v.Grace:
			// 통과하려면 나이가 Grace 를 초과해야 한다.
			// 경계값(나이 == Grace)은 아직 대기다. 실행 주기가
			// 1시간이므로 한 판정의 경계 1초는 다음 실행이 흡수한다.
			return ReasonTooRecent
		}
	}

	// Size < 0 은 실제 파일시스템에서는 관측되지 않는 값이지만,
	// 잘못된 Input 이 직접 전달되더라도 유효한 파일 크기가 아니므로
	// 0과 동일하게 거부한다. ledger 의 size > 0 계약과도 일치한다.
	if in.Size <= 0 {
		return ReasonZeroSize
	}

	if in.Category.MatchesName(in.Name) == domain.CategoryMatchMismatch {
		return ReasonCategoryMismatch
	}

	return ReasonNone
}
