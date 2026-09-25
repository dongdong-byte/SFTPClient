//go:build windows

package download

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// 첫 Remove 가 sharing violation 으로 실패해도 .part의 읽기 전용 속성을
// 풀지 않는다. 과거의 무조건 Chmod fallback은 실패한 파일을 쓰기 가능으로
// 바꾼 뒤 두 번째 Remove까지 시도했다.
func TestOSLocalStore_RemoveFailureDoesNotChangePermissions(t *testing.T) {
	dir := t.TempDir()
	part := filepath.Join(dir, "readonly.part")

	if err := os.WriteFile(part, []byte("important"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(part, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(part, 0o644)
	})
	if fi, err := os.Stat(part); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm()&0o222 != 0 {
		t.Fatalf("test setup did not make file read-only: %v", fi.Mode())
	}

	p, err := windows.UTF16PtrFromString(part)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, // DELETE 공유 없음
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_READONLY,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	if err := (OSLocalStore{}).Remove(context.Background(), part); err == nil {
		t.Fatal("Remove unexpectedly succeeded while DELETE sharing was denied")
	}

	fi, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o222 != 0 {
		t.Fatalf("failed remove changed permissions: %v", fi.Mode())
	}
}
