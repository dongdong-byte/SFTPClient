// Package pathpl 은 config.ini 의 경로 템플릿을 실제 경로로 확장한다.
//
// 기관별 폴더 구조 차이를 Core 코드의 기관명 조건문이 아니라 설정으로
// 처리하기 위한 장치이다. (설계안 10)
//
//	D:\RINEX-V3-H\(YYYY)\(DOY)\(HH)\  +  2026-07-01 00:00 UTC
//	  → D:\RINEX-V3-H\2026\182\00\
//
// 입출력만 있는 순수 함수이다. 디렉터리를 읽지 않고 날짜를 순회하지도 않는다.
// 어떤 시각들을 확장할지는 호출자(scan)가 정한다.
// PUT 과 DOWNLOAD 가 같은 코드를 공유한다.
package pathpl

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 지원 토큰. 설계안 10 의 기본 Token 후보 중 경로에 실제로 쓰이는 것들이다.
//
// (SITE) 는 포함하지 않는다. 실제 기관 경로가 관측소별 디렉터리를 두지 않고
// 한 디렉터리에 여러 관측소 파일을 함께 보관하기 때문이다.
// 관측소별 조회는 file_name 이 관측소 ID 로 시작하므로 SQL 에서 처리한다.
const (
	TokenYYYY = "YYYY" // 4자리 연도.        2026
	TokenYY   = "YY"   // 2자리 연도.        26
	TokenDOY  = "DOY"  // 3자리 연중일수.    182, 001
	TokenMM   = "MM"   // 2자리 월.          07
	TokenDD   = "DD"   // 2자리 일.          01
	TokenHH   = "HH"   // 2자리 시.          00
)

// ErrInvalidTemplate 은 템플릿 문법이 잘못되었을 때 반환된다.
//
// config 로드 시점에 실패하도록 하기 위한 것이다. 스캔 도중에 발견되면
// 원인을 찾기 어렵고, 설계안 10.1 은 위험한 설정을 시작 시 거부하도록 한다.
var ErrInvalidTemplate = errors.New("pathpl: invalid template")

// Template 은 파싱이 끝난 경로 템플릿이다.
//
// Parse 로 한 번 만들고 Expand 를 반복 호출한다.
// Deep Scan 은 같은 템플릿을 Category 당 720회 확장하므로
// 매번 문자열을 훑지 않고 미리 쪼개 둔다.
//
// 값이 바뀌지 않으므로 여러 goroutine 에서 동시에 Expand 해도 안전하다.
type Template struct {
	raw string

	// segments 는 리터럴과 토큰을 번갈아 담지 않고 각 조각의 성격을 함께 갖는다.
	segments []segment
}

type segment struct {
	// token 이 빈 문자열이면 literal 을 그대로 출력한다.
	// 아니면 token 에 해당하는 값으로 치환한다.
	literal string
	token   string
}

// Parse 는 템플릿 문자열을 검사하고 확장 가능한 형태로 만든다.
//
// 앞뒤 공백은 제거한다. config.ini 에 실수로 붙은 공백이 경로에
// 남아 "디렉터리가 없다" 로 끝나는 것을 막기 위함이다.
// 토큰 이름 안의 공백(예: "( YYYY )")은 제거하지 않고 거부한다.
//
// 알 수 없는 토큰은 오류이다. 그대로 두면 (DOI) 라는 이름의 디렉터리를
// 찾다가 "파일이 하나도 없다" 로 끝나 원인 파악이 어렵다.
// 소문자 (yyyy) 도 거부한다. 관대하게 받으면 config 에 두 표기가 섞인다.
//
// 한계: 경로에 괄호가 포함된 디렉터리명(예: "RINEX (백업)")은 토큰으로
// 해석되어 거부된다. 현재 대상 기관 경로에는 괄호가 없어 그대로 둔다.
// 필요해지면 이스케이프 규칙을 추가한다.
func Parse(s string) (*Template, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: empty", ErrInvalidTemplate)
	}

	t := &Template{raw: s}
	rest := s

	for {
		open := strings.IndexByte(rest, '(')
		if open < 0 {
			break
		}

		end := strings.IndexByte(rest[open:], ')')
		if end < 0 {
			return nil, fmt.Errorf("%w: unclosed %q in %q",
				ErrInvalidTemplate, rest[open:], s)
		}
		end += open

		name := rest[open+1 : end]
		if !isKnownToken(name) {
			return nil, fmt.Errorf("%w: unknown token (%s) in %q",
				ErrInvalidTemplate, name, s)
		}

		if open > 0 {
			literal := rest[:open]
			if err := checkLiteral(literal, s); err != nil {
				return nil, err
			}
			t.segments = append(t.segments, segment{literal: literal})
		}
		t.segments = append(t.segments, segment{token: name})

		rest = rest[end+1:]
	}

	if rest != "" {
		if err := checkLiteral(rest, s); err != nil {
			return nil, err
		}
		t.segments = append(t.segments, segment{literal: rest})
	}

	return t, nil
}

// checkLiteral 은 리터럴 조각에 짝 없는 닫는 괄호가 있는지 본다.
// 여는 괄호를 빠뜨린 오타(예: "YYYY)")를 잡는다.
func checkLiteral(literal, whole string) error {
	if strings.IndexByte(literal, ')') >= 0 {
		return fmt.Errorf("%w: unexpected ')' in %q", ErrInvalidTemplate, whole)
	}
	return nil
}

func isKnownToken(name string) bool {
	switch name {
	case TokenYYYY, TokenYY, TokenDOY, TokenMM, TokenDD, TokenHH:
		return true
	default:
		return false
	}
}

// Expand 는 주어진 시각으로 토큰을 채운 경로를 만든다.
//
// 시각은 반드시 UTC 로 해석한다. RINEX3 파일명의 시각 필드가 UTC 이고
// 디렉터리 구조도 그것을 따르기 때문이다. 호출자가 로컬 시각을 넘기더라도
// 여기서 변환하므로 안전하다.
//
// KST 로 해석하면 하루 중 9시간(00:00~08:59 KST)이 전날 UTC 에 속해
// 엉뚱한 DOY 디렉터리를 보게 된다. 오류가 나지 않고 조용히 틀리는 종류라
// 호출자의 규율에 맡기지 않는다.
//
// 경로 구분자는 손대지 않는다. config 에 적힌 그대로 둔다.
func (t *Template) Expand(when time.Time) string {
	u := when.UTC()

	var b strings.Builder
	b.Grow(len(t.raw) + 8)

	for _, seg := range t.segments {
		if seg.token == "" {
			b.WriteString(seg.literal)
			continue
		}
		b.WriteString(tokenValue(seg.token, u))
	}

	return b.String()
}

// tokenValue 는 토큰 하나에 대응하는 값을 만든다. when 은 UTC 여야 한다.
func tokenValue(token string, when time.Time) string {
	switch token {
	case TokenYYYY:
		return fmt.Sprintf("%04d", when.Year())
	case TokenYY:
		return fmt.Sprintf("%02d", when.Year()%100)
	case TokenDOY:
		return fmt.Sprintf("%03d", when.YearDay())
	case TokenMM:
		return fmt.Sprintf("%02d", int(when.Month()))
	case TokenDD:
		return fmt.Sprintf("%02d", when.Day())
	case TokenHH:
		return fmt.Sprintf("%02d", when.Hour())
	default:
		// Parse 가 이미 걸렀으므로 도달하지 않는다.
		return ""
	}
}

// HasToken 은 템플릿에 해당 토큰이 있는지 답한다.
//
// config 검증에서 사용한다. Hourly Category 의 경로에 (HH) 가 빠지면
// 0시부터 23시까지가 모두 같은 디렉터리로 확장되어 같은 곳을 24번 읽고
// 시간별 하위 폴더는 보지 못한다. 경로가 실제로 존재하기 때문에
// 오류도 나지 않고 파일도 일부 발견되어 아무도 알아채지 못한다.
//
// pathpl 은 Category 를 알지 못한다. 규칙은 config 가 세운다.
//
//	if cat.IsHourly() && !tpl.HasToken(pathpl.TokenHH) { 거부 }
func (t *Template) HasToken(token string) bool {
	for _, seg := range t.segments {
		if seg.token == token {
			return true
		}
	}
	return false
}

// String 은 파싱에 사용한 템플릿 원문을 돌려준다.
// Parse 가 앞뒤 공백을 제거한 뒤의 값이다.
func (t *Template) String() string {
	return t.raw
}
