package domain

import "fmt"

// Origin 은 파일이 이 서버에 들어온 경로를 나타낸다.
//
// 설계안 9.1 은 RECEIVER / LOCAL / DOWNLOAD 세 값을 예시하지만,
// 현재 구현에서는 LOCAL / DOWNLOAD 두 값으로 통합한다.
//
// Scanner 는 디렉터리에 놓인 파일만 볼 뿐 누가 생성했는지 판별할 수 없으므로
// RECEIVER 와 LOCAL 을 신뢰성 있게 구분할 근거가 없고,
// 현재 처리 흐름에서도 두 값 사이의 동작 차이가 없다. (CONCEPT 4.3)
//
// 향후 RECEIVER 를 다시 도입해야 한다면 이 상수 집합과
// schema.sql 의 CHECK 제약을 함께 수정해야 한다.
// 그 경우 RECEIVER 값은 실제 파일에서 검증된 사실이 아니라
// config 등 외부 선언에 의존하는 메타데이터라는 점을 전제로 한다.
//
// schema.sql common_ledger.origin 의 CHECK 제약과 같은 값이다.
type Origin string

const (
	// OriginLocal 은 이 서버에 원래 존재하던 파일을 뜻한다.
	// 수신기, BNC, 다른 프로세스가 생성하거나 복사한 파일을 모두 포함한다.
	//
	// Scanner 는 디렉터리에 존재하는 파일만 볼 수 있으므로
	// 실제 생성 주체가 수신기인지 다른 로컬 프로세스인지 구분하지 않는다.
	OriginLocal Origin = "LOCAL"

	// OriginDownload 는 이 프로그램이 DOWNLOAD 흐름을 통해
	// 원격 서버에서 수신한 파일을 뜻한다.
	//
	// PUT 후보 선정에서는 기본적으로 제외하여
	// 다운로드한 파일이 다시 원격으로 전송되는 Ping-Pong 을 방지한다.
	// 중계 구성이 필요한 경우에만 config 의 RepostDownloaded 로 예외를 허용한다.
	OriginDownload Origin = "DOWNLOAD"
)

// ParseOrigin 은 DB 에서 읽은 문자열을 Origin 으로 변환한다.
//
// DB 의 Origin 값은 schema.sql CHECK 제약 및 domain 상수와 정확히 일치해야 한다.
// 잘못된 대소문자나 공백을 자동 보정하지 않는다.
// 스키마 버전 불일치나 비정상 데이터를 조용히 받아들이지 않기 위함이다.
func ParseOrigin(s string) (Origin, error) {
	v := Origin(s)
	switch v {
	case OriginLocal, OriginDownload:
		return v, nil
	default:
		return "", fmt.Errorf("unknown origin %q", s)
	}
}

// Valid 는 정의된 Origin 값인지 답한다.
//
// 이미 Origin 타입인 값의 유효성만 확인할 때 사용한다.
// 문자열에서 변환하는 경우에는 ParseOrigin 을 쓴다.
func (o Origin) Valid() bool {
	_, err := ParseOrigin(string(o))
	return err == nil
}
