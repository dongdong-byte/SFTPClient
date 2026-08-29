// Package config 는 config.ini 를 읽어 실행에 사용할 설정으로 만든다.
//
// 세 단계로 나뉜다. 각 단계는 앞 단계의 결과만 사용한다.
//
//	ini.go       INI 문법 처리.   "이 파일이 문법에 맞는가"
//	load.go      키 매핑.         "이 값을 읽을 수 있는가"
//	validate.go  시작 시 검증.    "이 조합으로 돌려도 되는가"
//
// 위험한 설정은 스캔 도중이 아니라 시작 시점에 거부한다.
// 실행 중에 발견되면 원인을 찾기 어렵고, 그때는 이미 일부 파일이
// 잘못 전송된 뒤일 수 있다. (설계안 10.1)
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// maxLineBytes 는 한 줄의 상한이다.
//
// bufio.Scanner 의 기본 상한과 같은 값이지만 명시한다.
// config.ini 의 정상적인 줄은 경로를 포함해도 수백 바이트를 넘지 않으므로,
// 이 값을 넘었다는 것은 config.ini 가 아닌 파일을 지정했다는 뜻이다.
const maxLineBytes = 64 * 1024

// ErrSyntax 는 INI 문법 자체가 잘못되었을 때 반환된다.
//
// 값의 의미가 아니라 형식만 판정한다.
// "ScanDays 가 숫자가 아니다" 는 load.go 의 책임이고,
// "ScanDays 가 RetentionDays 보다 크다" 는 validate.go 의 책임이다.
var ErrSyntax = errors.New("config: ini syntax")

// iniFile 은 파싱이 끝난 INI 파일이다.
//
// 이 타입은 이 프로젝트를 알지 못한다. domain 도 pathpl 도 import 하지 않는다.
// 덕분에 주석·중복 키 같은 문법 사항을 설정 의미와 분리해 시험할 수 있다.
type iniFile struct {
	// sections 의 키는 foldKey 를 거친 값이다.
	sections map[string]*iniSection

	// order 는 파일에 나타난 순서이다. 알 수 없는 섹션 보고에 사용한다.
	order []string
}

// iniSection 은 [NAME] 하나와 그 아래의 키·값이다.
type iniSection struct {
	// name 은 파일에 적힌 원래 표기이다. 오류 메시지에 그대로 쓴다.
	name string

	// line 은 섹션 헤더가 나타난 줄 번호이다.
	line int

	pairs map[string]iniPair
	order []string
}

// iniPair 는 값 하나와 그 값이 적힌 줄 번호이다.
//
// 줄 번호를 함께 들고 다니는 이유는 오류 메시지 때문이다.
// 손으로 편집하는 파일이므로 "어느 줄이 잘못되었는가" 가 곧 수정 시간이다.
//
// key 는 파일에 적힌 원래 표기이다. iniSection.name 과 같은 이유로 보존한다.
// 이것이 없으면 알 수 없는 키를 보고할 때 foldKey 를 거친 대문자만 남아
// 사용자가 파일에서 눈으로 찾지 못한다. 같은 메시지 안에서 섹션은 원래 표기인데
// 키만 대문자가 되어 표기가 엇갈리기도 한다.
type iniPair struct {
	key   string
	value string
	line  int
}

// foldKey 는 섹션명과 키를 대소문자 구분 없이 비교하기 위한 형태로 만든다.
//
// 사람이 손으로 쓰는 파일이므로 [put.sftp] 와 [PUT.SFTP] 를 같게 다룬다.
// 원래 표기는 별도로 보존하여 오류 메시지에 그대로 보여준다.
func foldKey(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

// parseINI 는 INI 문서를 읽어 iniFile 로 만든다.
//
// 지원하는 문법
//
//	[SECTION]           섹션 헤더. 계층 표기(PUT.SFTP)도 이름의 일부일 뿐이다
//	key = value         값은 앞뒤 공백을 제거한다
//	; comment           줄 전체 주석
//	# comment           줄 전체 주석
//	key = value ; note  공백 뒤의 ; 또는 # 부터 줄 끝까지 주석
//
// 인라인 주석은 앞에 공백이 있을 때만 주석으로 본다.
// 공백 없이 붙은 ; 와 # 는 값의 일부이다. 경로에 들어갈 수 있기 때문이다.
//
// 거부하는 것
//
//	중복 섹션          나중 것이 앞 것을 덮으면 사람이 앞 것을 읽고 착각한다
//	중복 키            같은 이유
//	섹션 밖의 키       어느 섹션에 속하는지 알 수 없다
//	= 없는 줄
//	닫히지 않은 [
//	섹션 헤더 뒤의 군더더기
//	빈 섹션명
//	빈 키
//	64KiB 를 넘는 줄
//
// 따옴표로 감싼 값은 별도 문법으로 처리하지 않는다.
// 따옴표가 있으면 일반 문자로 취급한다.
//
// 관대하게 받아 넘기지 않는다.
// 조용히 무시된 한 줄이 곧 잘못된 설정으로 도는 실행이다.
func parseINI(r io.Reader) (*iniFile, error) {
	f := &iniFile{
		sections: make(map[string]*iniSection),
	}

	var current *iniSection

	sc := bufio.NewScanner(r)

	// 기본 상한을 그대로 쓰더라도 명시한다.
	// 이 값을 넘겼을 때 어떤 오류를 낼지가 아래에 정의되어 있으므로,
	// 상한이 어디서 오는지 코드에 남겨 둔다.
	sc.Buffer(make([]byte, 0, 64*1024), maxLineBytes)

	lineNo := 0

	for sc.Scan() {
		lineNo++

		line := sc.Text()

		if lineNo == 1 {
			// Windows 편집기가 붙이는 UTF-8 BOM.
			// 남겨두면 첫 섹션 헤더가 "\ufeff[GENERAL]" 이 되어
			// 원인을 알기 어려운 문법 오류가 된다.
			line = strings.TrimPrefix(line, "\ufeff")
		}

		// Scanner 는 \n 을 제거하지만 CRLF 입력에서는 환경에 따라
		// 끝의 \r 이 남을 수 있으므로 제거한다.
		line = strings.TrimRight(line, "\r")

		line = stripComment(line)
		line = strings.TrimSpace(line)

		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			sec, err := parseSectionHeader(line, lineNo)
			if err != nil {
				return nil, err
			}

			key := foldKey(sec.name)

			if prev, ok := f.sections[key]; ok {
				return nil, fmt.Errorf(
					"%w: line %d: duplicate section [%s] (first at line %d)",
					ErrSyntax,
					lineNo,
					sec.name,
					prev.line,
				)
			}

			f.sections[key] = sec
			f.order = append(f.order, key)
			current = sec

			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf(
				"%w: line %d: expected \"key = value\", got %q",
				ErrSyntax,
				lineNo,
				line,
			)
		}

		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])

		if key == "" {
			return nil, fmt.Errorf(
				"%w: line %d: empty key in %q",
				ErrSyntax,
				lineNo,
				line,
			)
		}

		if current == nil {
			return nil, fmt.Errorf(
				"%w: line %d: key %q appears before any [section]",
				ErrSyntax,
				lineNo,
				key,
			)
		}

		fk := foldKey(key)

		if prev, ok := current.pairs[fk]; ok {
			return nil, fmt.Errorf(
				"%w: line %d: duplicate key %q in [%s] (first at line %d)",
				ErrSyntax,
				lineNo,
				key,
				current.name,
				prev.line,
			)
		}

		current.pairs[fk] = iniPair{
			key:   key,
			value: value,
			line:  lineNo,
		}
		current.order = append(current.order, fk)
	}

	if err := sc.Err(); err != nil {
		// bufio.ErrTooLong 의 원문은 "bufio.Scanner: token too long" 이다.
		// 운영자에게 token 이 무엇인지 알릴 방법이 없으므로 바꿔 적는다.
		//
		// 문법 문제이므로 ErrSyntax 로 감싼다.
		// 그래야 호출부가 "문법 오류인가" 를 한 가지 방법으로 판별한다.
		//
		// lineNo 를 1 더하는 이유는 실패한 줄에서 Scan 이 false 를 돌려주어
		// 그 줄에 대한 증가가 일어나지 않았기 때문이다.
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, fmt.Errorf(
				"%w: line %d exceeds %d bytes; this is not a config.ini",
				ErrSyntax,
				lineNo+1,
				maxLineBytes,
			)
		}

		return nil, fmt.Errorf("config: read ini: %w", err)
	}

	return f, nil
}

// parseSectionHeader 는 "[NAME]" 한 줄을 섹션으로 만든다.
func parseSectionHeader(line string, lineNo int) (*iniSection, error) {
	if !strings.HasSuffix(line, "]") {
		// 닫는 괄호가 있는데 줄 끝이 아니면 뒤에 군더더기가 붙은 것이다.
		//
		//	[GENERAL] foo
		//
		// 이것을 "unclosed" 라고 하면 사용자가 없는 괄호를 찾게 된다.
		// 두 경우를 나누어 알린다.
		if strings.IndexByte(line, ']') > 0 {
			return nil, fmt.Errorf(
				"%w: line %d: unexpected text after section header %q",
				ErrSyntax,
				lineNo,
				line,
			)
		}

		return nil, fmt.Errorf(
			"%w: line %d: unclosed section header %q",
			ErrSyntax,
			lineNo,
			line,
		)
	}

	name := strings.TrimSpace(line[1 : len(line)-1])
	if name == "" {
		return nil, fmt.Errorf(
			"%w: line %d: empty section name",
			ErrSyntax,
			lineNo,
		)
	}

	if strings.ContainsAny(name, "[]") {
		return nil, fmt.Errorf(
			"%w: line %d: section name %q contains a bracket",
			ErrSyntax,
			lineNo,
			name,
		)
	}

	return &iniSection{
		name:  name,
		line:  lineNo,
		pairs: make(map[string]iniPair),
	}, nil
}

// stripComment 는 주석 부분을 잘라낸다.
//
// 줄 첫 글자가 ; 또는 # 이면 줄 전체가 주석이다.
// 그 외에는 앞에 공백이나 탭이 있을 때만 주석으로 본다.
//
// 공백 없이 붙은 경우를 값으로 두는 이유는 경로 때문이다.
// 드물지만 디렉터리명에 # 이 들어갈 수 있고, 그것을 주석으로 잘라내면
// 경로가 조용히 짧아져 "디렉터리가 없다" 로 끝난다.
//
// 다만 이 규칙이 지키는 것은 붙어 있는 경우뿐이다.
//
//	D:\RINEX#2\     보존된다
//	D:\RINEX #2\    "D:\RINEX" 로 잘린다
//
// 대상 기관 경로에 # 과 ; 가 없음을 확인했으므로 이 한계를 그대로 둔다.
// 필요해지면 따옴표 문법을 추가하고 이 주석도 함께 고친다.
//
// UTF-8 이어도 안전하다. 다바이트 문자의 각 바이트는 0x80 이상이므로
// ';'(0x3B) 이나 '#'(0x23) 과 같아질 수 없다.
func stripComment(line string) string {
	for i := 0; i < len(line); i++ {
		c := line[i]

		if c != ';' && c != '#' {
			continue
		}

		if i == 0 {
			return ""
		}

		if p := line[i-1]; p == ' ' || p == '\t' {
			return line[:i]
		}
	}

	return line
}

// parseINIFile 은 경로에서 INI 를 읽는다.
func parseINIFile(path string) (*iniFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %q: %w", path, err)
	}

	defer func() {
		// 읽기 전용이므로 Close 실패가 데이터에 영향을 주지 않는다.
		_ = f.Close()
	}()

	parsed, err := parseINI(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return parsed, nil
}

// section 은 이름으로 섹션을 찾는다. 대소문자를 구분하지 않는다.
func (f *iniFile) section(name string) (*iniSection, bool) {
	s, ok := f.sections[foldKey(name)]
	return s, ok
}

// sectionNames 는 파일에 나타난 순서대로 섹션의 원래 표기를 돌려준다.
// 알 수 없는 섹션을 보고할 때 사용한다.
func (f *iniFile) sectionNames() []string {
	names := make([]string, 0, len(f.order))

	for _, key := range f.order {
		names = append(names, f.sections[key].name)
	}

	return names
}

// get 은 키의 값을 돌려준다. 대소문자를 구분하지 않는다.
// 값이 빈 문자열인 것과 키가 없는 것을 구분해야 하므로 bool 을 함께 돌려준다.
func (s *iniSection) get(key string) (string, bool) {
	p, ok := s.pairs[foldKey(key)]
	return p.value, ok
}

// lineOf 는 키가 적힌 줄 번호를 돌려준다.
// 키가 없으면 섹션 헤더의 줄 번호를 돌려준다.
func (s *iniSection) lineOf(key string) int {
	if p, ok := s.pairs[foldKey(key)]; ok {
		return p.line
	}

	return s.line
}

// keys 는 파일에 나타난 순서대로 키의 원래 표기를 돌려준다.
//
// 알 수 없는 키를 보고할 때 사용하므로, foldKey 를 거친 대문자가 아니라
// 사용자가 파일에서 눈으로 찾을 수 있는 표기를 돌려준다.
//
// 조회용 값이 아니다. 반환값을 pairs 의 키로 다시 쓰려면 foldKey 를 거친다.
// 조회는 get 이 대소문자를 흡수하므로 그쪽을 쓴다.
func (s *iniSection) keys() []string {
	out := make([]string, 0, len(s.order))

	for _, fk := range s.order {
		out = append(out, s.pairs[fk].key)
	}

	return out
}
