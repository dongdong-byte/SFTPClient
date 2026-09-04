package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"SFTPClient/internal/security"
)

// secureSet 은 값 하나를 읽어 DPAPI(LocalMachine) 보호 값으로 만든 뒤
// enc: 형식 문자열을 stdout 으로 출력한다.
//
// 설치 시 1회 사용하는 provisioning 명령이다.
//
// 사용법 (Host, User, Port 각각 1회씩):
//
//	rinexclient.exe secure-set
//
// 값을 입력하고 Enter 를 누르면 출력된 enc:... 전체를
// config.ini 의 해당 값에 붙여 넣는다.
//
// 출력은 콘솔에서 직접 복사한다. PowerShell 의 > 리다이렉트는
// UTF-16(BOM) 파일을 만들어 붙여넣기 사고의 원인이 된다
// (known_hosts 전례). echo <값> | 파이프 입력도 쓰지 않는다 —
// 값이 셸 히스토리에 남는다.
//
// 반드시 해당 config.ini 를 실제로 사용할 Windows 환경에서 실행한다.
// LocalMachine 스코프이므로 다른 PC 또는 Windows 재설치 환경에서는
// 기존 암호문의 복호화를 보장하지 않는다.
//
// config.ini 자체는 수정하지 않는다.
// 파일을 자동 재작성하면 기존 주석·순서·서식을 훼손할 수 있으므로,
// secure-set 은 보호 값 생성만 담당하고 반영은 운영자가 직접 수행한다.
func secureSet() error {
	// 프롬프트는 stderr 로 출력한다.
	// stdout 에는 enc: 결과만 출력하여 결과 값을 구분하기 쉽게 한다.
	fmt.Fprintln(
		os.Stderr,
		"암호화할 값을 입력하고 Enter (결과 enc:... 는 stdout 으로 출력됩니다):",
	)

	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		if err := sc.Err(); err != nil {
			return fmt.Errorf("secure-set: 입력 읽기 실패: %w", err)
		}

		return fmt.Errorf("secure-set: 입력이 없다")
	}

	v := strings.TrimSpace(sc.Text())
	if v == "" {
		return fmt.Errorf("secure-set: 빈 값은 암호화하지 않는다")
	}

	enc, err := security.New().Protect(v)
	if err != nil {
		return fmt.Errorf("secure-set: %w", err)
	}

	fmt.Println(enc)

	return nil
}
