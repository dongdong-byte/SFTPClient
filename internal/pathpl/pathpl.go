// Package pathpl 은 config.ini 의 경로 템플릿을 실제 경로로 확장한다.
//
// 기관별 폴더 구조 차이를 Core 코드의 기관명 조건문이 아니라 설정으로
// 처리하기 위한 장치이다. (설계안 10)
//
//	D:\RINEX-V3-H\(YYYY)\(DOY)\  +  2026-07-01 00:00 UTC
//	  → D:\RINEX-V3-H\2026\182\
//
// 입출력만 있는 순수 함수이다. 디렉터리를 읽지 않고 날짜를 순회하지도 않는다.
// 어떤 날짜 범위를 확장할지는 호출자(scan)가 정한다.
// PUT 과 DOWNLOAD 가 같은 코드를 공유한다.
//
// 토큰은 값의 출처로 두 종류다 (결정 문서 §4.2, DOWNLOAD v3 §2.1-5):
//
//	날짜 토큰  (YYYY) (YY) (DOY) (MM) (DD)   값 = 스캔 날짜.  Expand(when)
//	파일 토큰  (SITE) (HH)                   값 = 파일별 추출. ExpandFile(when, fields)
//
// pathpl 은 파일명을 읽지 않는다. 파일 토큰의 값을 파일명에서 뽑는 것은
// domain(SiteFromName·HourFromName)이고, 어느 경로 역할에 어느 토큰을
// 허용할지는 config 정책이다. pathpl 은 토큰의 문법과 치환만 안다.
package pathpl

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// 지원 토큰.
//
// 날짜 토큰의 값은 Expand·ExpandFile 이 받는 시각에서 나온다.
//
// 파일 토큰은 경로 토큰 커밋 3 에서 추가했다 (결정 기록 §4.2, PATH v4).
//
//	(SITE) 관측소 코드 4자리 (DBON). 목적지는 ExpandFile 이 채우고,
//	       원본은 PATH v4 패턴 탐색기가 그 위치의 폴더를 나열한다.
//	(HH)   관측 시작 시각 두 자리 (00~23). 값의 출처는 파일명이다.
//
// (HH) 이력: PATH v3 의 (HH) 는 스캔 날짜에서 시각을 계산해 자정 폴더만
// 보는 토큰이라 삭제했다. 여기의 (HH) 는 이름만 같은 다른 토큰이다.
// Expand 는 시계의 시로 채우지 않는다. 목적지는 ExpandFile, 원본은
// 폴더 나열(커밋 4~5)이다. 이 커밋 시점에 PUT 경로의 (SITE)·(HH) 를
// 막는 것은 pathpl 이 아니라 config 정책(커밋 2)이다. PUT 원본 허용은
// 커밋 6 이다.
const (
	TokenYYYY = "YYYY" // 4자리 연도.        2026
	TokenYY   = "YY"   // 2자리 연도.        26
	TokenDOY  = "DOY"  // 3자리 연중일수.    182, 001
	TokenMM   = "MM"   // 2자리 월.          07
	TokenDD   = "DD"   // 2자리 일.          01

	TokenSITE = "SITE" // 관측소 코드.       DBON   (파일 토큰)
	TokenHH   = "HH"   // 관측 시작 시각.    13     (파일 토큰)
)

// FileFields 는 파일 토큰의 값이다. 호출자가 파일마다 domain 에서 뽑아
// 채운다. 템플릿에 없는 토큰의 필드는 비워 두어도 된다.
type FileFields struct {
	Site string // (SITE)
	Hour string // (HH)
}

// ErrMissingFileField 는 템플릿이 요구하는 파일 토큰의 값이 비어 있을 때
// ExpandFile 이 반환한다.
//
// 빈 값을 그대로 넣으면 "D:\RNX\2026\250\\" 처럼 폴더 한 단이 사라진
// 경로가 조용히 만들어진다. 파일이 엉뚱한 곳에 쌓이고 오류는 없다.
// 결정 기록 §4.3-5 에 따라 호출자는 이 오류를 받은 파일만 제외·집계한다.
var ErrMissingFileField = errors.New("pathpl: file field missing")

// ErrBadFileField 는 파일 토큰의 값이 정해진 형식과 다를 때 ExpandFile 이
// 반환한다. SITE는 ASCII 영숫자 4자리, HH는 "00"~"23"이어야 한다.
// 경로 구분자와 "."·".."도 이 형식 검사에서 함께 거부된다.
// domain 이 주는 정상 값에서는 나올 수 없으므로 이 오류는 호출자의 계약
// 위반을 뜻한다.
var ErrBadFileField = errors.New("pathpl: invalid file field")

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
	return isDateToken(name) || isFileToken(name)
}

func isDateToken(name string) bool {
	switch name {
	case TokenYYYY, TokenYY, TokenDOY, TokenMM, TokenDD:
		return true
	default:
		return false
	}
}

// isFileToken 은 값의 출처가 파일명인 토큰인지 답한다.
// 새 파일 토큰은 여기와 FileFields, fileFieldValue 세 곳에 함께 추가한다.
func isFileToken(name string) bool {
	switch name {
	case TokenSITE, TokenHH:
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
//
// 파일 토큰이 있는 목적지 템플릿에서는 부르지 않는다. 목적지는 ExpandFile을
// 쓴다. 원본 템플릿은 PATH v4의 패턴 탐색기가 파일 토큰 단을 해석하며,
// Expand 결과를 실제 스캔 루트로 사용해서는 안 된다.
//
// 그래도 도달하면 파일 토큰을 원문 "(SITE)" 그대로 남긴다. 빈 문자열로
// 접으면 폴더 한 단이 사라진 경로가 조용히 만들어지고, panic 으로 세우면
// 그 회차의 다른 카테고리까지 함께 죽는다. 원문 보존은 이후 패턴 탐색기가
// 토큰 단을 식별하기 위한 동작일 뿐이다. 일반 Scanner에 이 결과를 넘기면
// 실제로 이름이 "(SITE)"인 폴더를 찾다가 파일 0개로 조용히 끝날 수 있다.
func (t *Template) Expand(when time.Time) string {
	u := when.UTC()

	var b strings.Builder
	b.Grow(len(t.raw) + 8)

	for _, seg := range t.segments {
		switch {
		case seg.token == "":
			b.WriteString(seg.literal)
		case isFileToken(seg.token):
			b.WriteString("(" + seg.token + ")")
		default:
			b.WriteString(tokenValue(seg.token, u))
		}
	}

	return b.String()
}

// ExpandFile 은 날짜 토큰은 when 으로, 파일 토큰은 f 로 채운 경로를 만든다.
//
// DOWNLOAD 목적지 전용이다. 파일 하나마다 호출되며, 같은 템플릿을 여러
// goroutine 에서 동시에 확장해도 안전하다.
//
// 템플릿이 요구하는 파일 토큰의 값이 비어 있으면 ErrMissingFileField 다.
// SITE가 ASCII 영숫자 4자리가 아니거나 HH가 "00"~"23"이 아니면
// ErrBadFileField 다. 두 경우 모두 부분 경로를 돌려주지 않는다 — 호출자가
// 오류를 놓치고 반환 경로를 사용해도 파일이 어딘가에 쓰이면 안 된다.
//
// 템플릿에 없는 토큰의 필드는 무시한다. Daily 카테고리는 Hour 를 채울 수
// 없는데 목적지에 (HH) 가 없으면 그것으로 충분하다. (Daily 목적지의 (HH)
// 는 config 가 시작 단계에서 거부한다.)
//
// 날짜 토큰의 UTC 규칙은 Expand 와 같다.
func (t *Template) ExpandFile(when time.Time, f FileFields) (string, error) {
	u := when.UTC()

	var b strings.Builder
	b.Grow(len(t.raw) + 8)

	for _, seg := range t.segments {
		switch {
		case seg.token == "":
			b.WriteString(seg.literal)

		case isFileToken(seg.token):
			v := fileFieldValue(seg.token, f)
			if v == "" {
				return "", fmt.Errorf("%w: (%s) in %q", ErrMissingFileField, seg.token, t.raw)
			}
			if !validFileField(seg.token, v) {
				return "", fmt.Errorf("%w: (%s)=%q in %q", ErrBadFileField, seg.token, v, t.raw)
			}
			b.WriteString(v)

		default:
			b.WriteString(tokenValue(seg.token, u))
		}
	}

	return b.String(), nil
}

// validFileField 는 목적지 경로에 들어갈 파일 토큰 값을 검사한다.
// 값의 추출은 domain의 책임이지만, 실제 경로를 만드는 pathpl도 호출자
// 계약 위반을 마지막으로 막아 상위 폴더 이탈과 조용한 오경로를 방지한다.
func validFileField(token, value string) bool {
	switch token {
	case TokenSITE:
		if len(value) != 4 {
			return false
		}
		for i := 0; i < len(value); i++ {
			c := value[i]
			if (c < 'a' || c > 'z') &&
				(c < 'A' || c > 'Z') &&
				(c < '0' || c > '9') {
				return false
			}
		}
		return true

	case TokenHH:
		return len(value) == 2 &&
			value[0] >= '0' && value[0] <= '9' &&
			value[1] >= '0' && value[1] <= '9' &&
			value >= "00" && value <= "23"

	default:
		return false
	}
}

// fileFieldValue 는 파일 토큰 하나에 대응하는 필드 값을 돌려준다.
func fileFieldValue(token string, f FileFields) string {
	switch token {
	case TokenSITE:
		return f.Site
	case TokenHH:
		return f.Hour
	default:
		// isFileToken 이 이미 걸렀으므로 도달하지 않는다.
		return ""
	}
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
	default:
		// Parse 가 이미 걸렀으므로 도달하지 않는다.
		return ""
	}
}

// HasToken 은 템플릿에 해당 토큰이 있는지 답한다.
//
// pathpl 은 Category 를 알지 못한다. 토큰 존재에 의미를 부여하는 것은
// 호출자다.
func (t *Template) HasToken(token string) bool {
	for _, seg := range t.segments {
		if seg.token == token {
			return true
		}
	}
	return false
}

// FileTokens 는 템플릿에 쓰인 파일 토큰 이름을 등장 순서로, 중복 없이
// 돌려준다. 없으면 nil 이다.
//
// config 정책이 "이 경로 역할에 파일 토큰이 있는가", "Daily 목적지에 (HH)
// 가 있는가"를 묻는 데 쓴다. pathpl 은 그 질문의 답에 의미를 두지 않는다.
func (t *Template) FileTokens() []string {
	var out []string

	for _, seg := range t.segments {
		if seg.token == "" || !isFileToken(seg.token) {
			continue
		}

		dup := false
		for _, have := range out {
			if have == seg.token {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, seg.token)
		}
	}

	return out
}

// String 은 파싱에 사용한 템플릿 원문을 돌려준다.
// Parse 가 앞뒤 공백을 제거한 뒤의 값이다.
func (t *Template) String() string {
	return t.raw
}
