package pathpl

import (
	"strings"
	"time"
)

// 원본(스캔 경로)의 파일 토큰은 "채우기"가 아니라 "찾기"다
// (PATH v4 커밋 계획 §2). 목적지는 파일명에서 뽑은 값으로 ExpandFile 이
// 채우지만, 원본은 파일을 아직 보기 전이라 값이 없다. 그 위치의 부모
// 폴더를 나열해 패턴에 맞는 폴더만 따라 내려가야 한다.
//
// 이 파일은 그 나열의 재료만 만든다. 템플릿을 폴더 단위 "단계"로 나누고,
// 파일 토큰이 든 단을 폴더 이름 하나를 받아 맞는지 답하는 matcher 로
// 제공한다. 실제 나열(DirLister)과 하강은 scan 의 일이다(커밋 5).
// pathpl 은 여전히 디렉터리를 읽지 않는다.

// Step 은 템플릿을 폴더 단위로 나눈 한 단이다. 두 종류 중 하나다.
//
//	고정 단  Fixed 에 경로 조각이 있다. 글자·날짜 토큰만으로 이루어진
//	         폴더들과 그 사이의 구분자가 스캔 날짜로 채워진 문자열이다.
//	         연속된 고정 폴더는 하나의 Fixed 로 합쳐진다.
//	패턴 단  파일 토큰이 하나 이상 든 폴더 하나다. Fixed 는 비어 있고
//	         Match 로 폴더 이름을 대조한다.
//
// 경로 재구성 규칙: 단계를 순서대로 돌며 고정 단은 Fixed 를, 패턴 단은
// Match 에 맞은 폴더 이름을 그대로 이어 붙이면 원문 템플릿과 같은 표기의
// 경로가 된다. 구분자는 모두 고정 단 안에 보존되어 있으므로 호출자가
// 구분자를 끼워 넣지 않는다.
//
//	/data/(SITE)/(YYYY)/(DOY)/   (when = 2026-09-07)
//	→ [Fixed "/data/"] [Pattern (SITE)] [Fixed "/2026/250/"]
//	→ "/data/" + "DBON" + "/2026/250/"
//
// 파일 토큰이 없는 템플릿은 고정 단 하나이고 그 Fixed 는 Expand(when) 과
// 같다. scan 이 이 경우 지금과 완전히 같은 경로를 만드는 근거다(I2).
type Step struct {
	// Fixed 는 고정 단의 경로 조각이다. 패턴 단이면 빈 문자열이다.
	Fixed string

	// pattern 은 패턴 단의 조각이다. 고정 단이면 nil 이다.
	// 날짜 토큰은 이미 값으로 바뀌어 literal 조각이 되어 있고,
	// token 조각은 파일 토큰뿐이다.
	pattern []piece
}

type piece struct {
	literal string
	token   string
}

// IsPattern 은 파일 토큰이 든 단인지 답한다.
func (s Step) IsPattern() bool {
	return s.pattern != nil
}

// Match 는 폴더 이름 하나가 이 패턴 단에 맞는지 보고, 맞으면 뽑힌 값을
// 돌려준다. 고정 단에서 부르면 항상 false 다.
//
// 대조 규칙 (PATH v4 커밋 계획 §4.3):
//
//	글자·날짜 토큰  정확히 일치. 대소문자 구분
//	(SITE)          영숫자 4자리. 대소문자 무시, 값은 대문자로 정규화
//	(HH)            숫자 2자리 "00"~"23"
//
// 모든 조각의 폭이 고정이라 왼쪽에서 오른쪽으로 한 번 훑으면 끝난다.
// 되돌아가기가 없으므로 "(SITE)(HH)" 처럼 붙여 쓴 토큰도 모호하지 않다.
// 이름이 남거나 모자라면 불일치다.
//
// 한 단에 같은 토큰이 두 번 있으면(예: "(SITE)_(SITE)") 두 값이 같아야
// 한다. 단 사이의 일관성("/(SITE)/.../(SITE)/")은 FileFields.Merge 로
// 호출자가 본다.
//
// name 은 폴더 이름 하나다. 구분자가 들어 있으면 글자 조각과 일치할 수
// 없으므로 자연히 불일치다.
func (s Step) Match(name string) (FileFields, bool) {
	if !s.IsPattern() || name == "" {
		return FileFields{}, false
	}

	var f FileFields
	rest := name

	for _, p := range s.pattern {
		if p.token == "" {
			if !strings.HasPrefix(rest, p.literal) {
				return FileFields{}, false
			}
			rest = rest[len(p.literal):]
			continue
		}

		w := fileTokenWidth(p.token)
		if len(rest) < w {
			return FileFields{}, false
		}
		v := rest[:w]
		rest = rest[w:]

		switch p.token {
		case TokenSITE:
			v = strings.ToUpper(v)
			if !validFileField(TokenSITE, v) {
				return FileFields{}, false
			}
			if f.Site != "" && f.Site != v {
				return FileFields{}, false
			}
			f.Site = v

		case TokenHH:
			if !validFileField(TokenHH, v) {
				return FileFields{}, false
			}
			if f.Hour != "" && f.Hour != v {
				return FileFields{}, false
			}
			f.Hour = v

		default:
			// Parse 와 isFileToken 이 걸렀으므로 도달하지 않는다.
			return FileFields{}, false
		}
	}

	if rest != "" {
		return FileFields{}, false
	}

	return f, true
}

// fileTokenWidth 는 파일 토큰이 폴더 이름에서 차지하는 고정 폭이다.
// validFileField 의 길이 규칙과 같아야 한다.
func fileTokenWidth(token string) int {
	switch token {
	case TokenSITE:
		return 4
	case TokenHH:
		return 2
	default:
		return 0
	}
}

// Merge 는 두 단에서 뽑힌 값을 합친다. 같은 필드가 서로 다른 값으로
// 채워져 있으면 false 다 — "/(SITE)/.../(SITE)/" 에서 DBON 아래
// SUW1 폴더로 내려가는 것을 막는 데 쓴다. 빈 필드는 상대 값을 받는다.
func (f FileFields) Merge(g FileFields) (FileFields, bool) {
	out := f

	if g.Site != "" {
		if out.Site != "" && out.Site != g.Site {
			return FileFields{}, false
		}
		out.Site = g.Site
	}

	if g.Hour != "" {
		if out.Hour != "" && out.Hour != g.Hour {
			return FileFields{}, false
		}
		out.Hour = g.Hour
	}

	return out, true
}

// Steps 는 템플릿을 폴더 단위 단계로 나눈다. 날짜 토큰은 when 으로 채운다.
//
// 폴더의 경계는 '/' 와 '\' 다. 구분자 자체와 드라이브("C:")·루트("/")는
// 앞뒤 고정 단에 원문 그대로 들어간다. 연속된 고정 폴더는 하나의 고정
// 단으로 합쳐지므로 파일 토큰이 없는 템플릿은 단 하나만 나오고 그 Fixed
// 는 Expand(when) 과 같다.
//
// 날짜 토큰이 파일 토큰과 같은 폴더에 있으면("(DOY)_(SITE)") 그 폴더는
// 패턴 단이고 날짜 부분은 값으로 바뀐 글자 조각이 된다.
//
// 날짜 토큰의 UTC 규칙은 Expand 와 같다. 같은 템플릿을 여러 goroutine 에서
// 동시에 불러도 안전하다.
func (t *Template) Steps(when time.Time) []Step {
	u := when.UTC()

	var (
		steps []Step

		// fixed 는 아직 단으로 확정하지 않은 고정 텍스트다.
		fixed strings.Builder

		// folder 는 지금 모으는 폴더의 조각이다. 구분자를 만나면 닫는다.
		folder  []piece
		hasFile bool
	)

	closeFolder := func() {
		if hasFile {
			if fixed.Len() > 0 {
				steps = append(steps, Step{Fixed: fixed.String()})
				fixed.Reset()
			}
			steps = append(steps, Step{pattern: folder})
		} else {
			for _, p := range folder {
				fixed.WriteString(p.literal)
			}
		}
		folder = nil
		hasFile = false
	}

	for _, seg := range t.segments {
		switch {
		case seg.token == "":
			lit := seg.literal
			for {
				i := strings.IndexAny(lit, `/\`)
				if i < 0 {
					if lit != "" {
						folder = append(folder, piece{literal: lit})
					}
					break
				}
				if i > 0 {
					folder = append(folder, piece{literal: lit[:i]})
				}
				closeFolder()
				fixed.WriteByte(lit[i])
				lit = lit[i+1:]
			}

		case isFileToken(seg.token):
			folder = append(folder, piece{token: seg.token})
			hasFile = true

		default:
			folder = append(folder, piece{literal: tokenValue(seg.token, u)})
		}
	}

	closeFolder()
	if fixed.Len() > 0 {
		steps = append(steps, Step{Fixed: fixed.String()})
	}

	return steps
}
