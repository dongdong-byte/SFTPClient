//go:build windows

package security

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestDPAPIRoundTrip 은 Protect → Resolve 왕복을 확인한다.
// 실제 DPAPI 를 호출하므로 Windows 회차에서만 돈다.
func TestDPAPIRoundTrip(t *testing.T) {
	const want = "192.168.10.20"

	enc, err := New().Protect(want)
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}

	if !isEncrypted(enc) {
		t.Fatalf("Protect 결과가 enc: 로 시작하지 않는다: %q", enc)
	}

	plain, wasEnc, err := New().Resolve(enc)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !wasEnc {
		t.Fatal("wasEncrypted = false, want true")
	}

	if plain != want {
		t.Fatalf("plain = %q, want %q", plain, want)
	}
}

// TestDPAPIResolveTamperedBlob 은 base64 로는 유효하지만 내용이 훼손된
// blob 을 거부하는지 확인한다.
//
// CryptUnprotectData 실패 분기를 밟는 유일한 자동 테스트다 —
// 손상 base64/빈 blob 테스트는 그 앞 단계에서 걸러지므로 이 분기에
// 도달하지 않는다. 수동 검증 항목 "enc: 값 훼손 → 즉시 종료" 와
// "다른 PC 복사 → 복호화 실패" 가 지나는 코드 경로와 같다.
func TestDPAPIResolveTamperedBlob(t *testing.T) {
	enc, err := New().Protect("192.168.10.20")
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}

	// blob 을 디코드해 첫 바이트를 뒤집고 다시 인코드한다.
	// base64 는 유효하지만 DPAPI blob 으로는 훼손된 값이 된다.
	blob, err := base64.StdEncoding.DecodeString(
		strings.TrimPrefix(enc, encPrefix),
	)
	if err != nil {
		t.Fatalf("테스트 준비 실패 (base64 디코드): %v", err)
	}

	blob[0] ^= 0xFF

	tampered := encPrefix + base64.StdEncoding.EncodeToString(blob)

	if _, _, err := New().Resolve(tampered); err == nil {
		t.Fatal("훼손된 blob 을 오류 없이 복호화했다")
	}
}

// TestDPAPIResolvePlaintext 는 평문이 그대로 통과하는지 확인한다.
func TestDPAPIResolvePlaintext(t *testing.T) {
	plain, wasEnc, err := New().Resolve("hello")
	if err != nil || wasEnc || plain != "hello" {
		t.Fatalf(
			"Resolve(hello) = (%q, %v, %v), want (hello, false, nil)",
			plain, wasEnc, err,
		)
	}
}

// TestDPAPIResolveCorruptBase64 는 손상된 base64 를 거부하는지 확인한다.
func TestDPAPIResolveCorruptBase64(t *testing.T) {
	if _, _, err := New().Resolve("enc:!!!not-base64!!!"); err == nil {
		t.Fatal("손상된 base64 를 오류 없이 통과시켰다")
	}
}

// TestDPAPIResolveEmptyBlob 은 enc: 뒤에 내용이 없는 값을 거부하는지
// 확인한다.
func TestDPAPIResolveEmptyBlob(t *testing.T) {
	if _, _, err := New().Resolve("enc:"); err == nil {
		t.Fatal("빈 blob 을 오류 없이 통과시켰다")
	}
}

// TestDPAPIProtectEmpty 는 빈 값 암호화를 거부하는지 확인한다.
func TestDPAPIProtectEmpty(t *testing.T) {
	if _, err := New().Protect(""); err == nil {
		t.Fatal("빈 값을 오류 없이 암호화했다")
	}
}
