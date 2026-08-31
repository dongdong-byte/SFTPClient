package transport

import (
	"path"
	"testing"

	"SFTPClient/internal/put"
)

// 생산 코드는 put 을 import 하지 않는다.
// 구조적 만족은 테스트에서만 고정한다.
var _ put.Uploader = (*SFTPFS)(nil)

func TestSFTPFSJoinUsesSlash(t *testing.T) {
	t.Parallel()

	got := (*SFTPFS)(nil).Join("a/b", "c.rnx")
	want := path.Join("a/b", "c.rnx")
	if got != want {
		t.Fatalf("Join() = %q, want %q", got, want)
	}

	if got != "a/b/c.rnx" {
		t.Fatalf("Join() = %q, want slash-separated path", got)
	}
}
