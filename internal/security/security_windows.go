//go:build windows

package security

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// DPAPI 는 Windows Data Protection API 기반 구현이다.
//
// LocalMachine 스코프: 같은 Windows 환경에서는 실행 계정과 무관하게
// 복호화할 수 있다. 다른 PC 또는 Windows 재설치 환경에서는 기존 암호문의
// 복호화를 보장하지 않는다.
//
// 기각: CurrentUser 스코프 — 암호화 계정과 schtasks 실행 계정이
// 달라지면 복호화 실패로 재프로비저닝이 필요해 운영 계정 결합도가
// 증가한다. 요구 수준("평문 저장 금지") 대비 운영 리스크 과다.
//
// 기각: optionalEntropy 상수 내장 — entropy 는 결국 배포 폴더의
// 바이너리 안에 박히므로 "같은 PC 다른 프로세스" 방어가 성립하지
// 않는다(난독화 수준). secure-set 과 로더가 상수를 공유하는 관리
// 포인트만 늘어난다. 사용하지 않는다(nil).
//
// 상세 결정 근거는 docs/SFTPClient_SECURITY_ENC_DESIGN.md 를 따른다.
type DPAPI struct{}

// New 는 이 플랫폼의 구현을 반환한다.
//
// Windows/비-Windows 가 같은 이름 New 를 제공하므로, 배선하는 main 은
// 빌드 태그를 의식하지 않고 security.New() 만 호출한다.
func New() DPAPI { return DPAPI{} }

// Protect 는 평문을 enc: 형식 암호문으로 만든다. secure-set 전용이다.
//
// 반드시 값이 사용될 그 PC 에서 실행해야 한다.
// 다른 PC 또는 Windows 재설치 환경에서는 기존 암호문의 복호화를
// 보장하지 않는다.
func (DPAPI) Protect(plain string) (string, error) {
	if plain == "" {
		return "", fmt.Errorf("security: 빈 값은 암호화하지 않는다")
	}

	data := []byte(plain)

	in := windows.DataBlob{
		Size: uint32(len(data)),
		Data: &data[0],
	}

	var out windows.DataBlob

	// LOCAL_MACHINE: 실행 계정과 무관하게 복호화.
	// UI_FORBIDDEN: 무인 실행이므로 어떤 경우에도 UI 를 띄우지 않는다.
	err := windows.CryptProtectData(
		&in,
		nil, // name
		nil, // optionalEntropy — 사용하지 않는다 (기각 사유는 타입 주석)
		0,   // reserved
		nil, // promptStruct
		windows.CRYPTPROTECT_LOCAL_MACHINE|windows.CRYPTPROTECT_UI_FORBIDDEN,
		&out,
	)
	if err != nil {
		return "", fmt.Errorf("security: CryptProtectData: %w", err)
	}

	blob, err := takeDPAPIBlob(out)
	if err != nil {
		return "", err
	}

	return encPrefix + base64.StdEncoding.EncodeToString(blob), nil
}

// Resolve 는 config.Protector 를 구현한다.
//
//	enc: 값     → 복호화하여 (평문, true, nil)
//	평문         → 그대로 (값, false, nil)
//	복호화 실패 → ("", false, err)
//
// enc: 접두어가 붙은 값을 복호화하지 못했을 때 리터럴로 통과시키지 않는다.
// 그러면 enc:AQAA... 를 실제 설정값으로 사용하는 잘못된 실행이 가능하기 때문이다.
func (DPAPI) Resolve(value string) (string, bool, error) {
	if !isEncrypted(value) {
		return value, false, nil
	}

	blob, err := base64.StdEncoding.DecodeString(
		strings.TrimPrefix(value, encPrefix),
	)
	if err != nil {
		return "", false, fmt.Errorf(
			"security: enc: 값의 base64 가 손상되었다: %w",
			err,
		)
	}

	if len(blob) == 0 {
		return "", false, fmt.Errorf("security: enc: 뒤에 암호문이 없다")
	}

	in := windows.DataBlob{
		Size: uint32(len(blob)),
		Data: &blob[0],
	}

	var out windows.DataBlob

	// 스코프 플래그는 복호화 시 주지 않는다 — 스코프는 blob 에
	// 새겨져 있으며, 복호화 쪽 플래그는 UI_FORBIDDEN 만 유효하다.
	err = windows.CryptUnprotectData(
		&in,
		nil, // name
		nil, // optionalEntropy — Protect 와 동일하게 사용하지 않는다
		0,   // reserved
		nil, // promptStruct
		windows.CRYPTPROTECT_UI_FORBIDDEN,
		&out,
	)
	if err != nil {
		return "", false, fmt.Errorf(
			"security: 복호화 실패 — 다른 PC/Windows 환경에서 생성된 "+
				"암호문이거나 암호문이 손상되었을 수 있다. "+
				"이 PC에서 secure-set으로 다시 생성하라: %w",
			err,
		)
	}

	plain, err := takeDPAPIBlob(out)
	if err != nil {
		return "", false, err
	}

	return string(plain), true, nil
}

// takeDPAPIBlob 은 CryptProtectData / CryptUnprotectData 가 성공한 뒤의
// 출력 blob 을 Go 메모리로 복사하고 LocalAlloc 버퍼를 해제한다.
//
// Data 가 nil 이거나 Size 가 0 이면 unsafe.Slice 가 panic 할 수 있으므로
// 복사 전에 거부한다. 정상 Host/User/Port 왕복에서는 타지 않는 방어다.
func takeDPAPIBlob(out windows.DataBlob) ([]byte, error) {
	if out.Data != nil {
		defer func() {
			_, _ = windows.LocalFree(
				windows.Handle(unsafe.Pointer(out.Data)),
			)
		}()
	}

	if out.Data == nil || out.Size == 0 {
		return nil, fmt.Errorf("security: DPAPI 출력이 비어 있다")
	}

	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))

	return buf, nil
}
