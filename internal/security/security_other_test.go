//go:build !windows

package security

import "testing"

// TestOtherRejectsEncrypted 는 비 Windows 에서 enc: 값을 거부하는지
// 확인한다. 조용한 리터럴 통과는 무증상 오작동이 된다.
func TestOtherRejectsEncrypted(t *testing.T) {
	if _, _, err := New().Resolve("enc:AQAA"); err == nil {
		t.Fatal("비 Windows 에서 enc: 값을 오류 없이 통과시켰다")
	}
}

// TestOtherPassesPlaintext 는 평문이 그대로 통과하는지 확인한다.
func TestOtherPassesPlaintext(t *testing.T) {
	const want = "192.168.0.1"

	plain, wasEnc, err := New().Resolve(want)
	if err != nil || wasEnc || plain != want {
		t.Fatalf(
			"Resolve(%q) = (%q, %v, %v), want (%q, false, nil)",
			want, plain, wasEnc, err, want,
		)
	}
}

// TestOtherProtectUnsupported 는 비 Windows 에서 Protect 가 실패하는지
// 확인한다.
func TestOtherProtectUnsupported(t *testing.T) {
	if _, err := New().Protect("x"); err == nil {
		t.Fatal("비 Windows 에서 Protect 가 성공했다")
	}
}
