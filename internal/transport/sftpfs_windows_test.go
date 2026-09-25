//go:build windows

package transport

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRemoveReplaceableFile_FailureDoesNotChangePermissions(t *testing.T) {
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
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_READONLY,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)

	if err := removeReplaceableFile(part); err == nil {
		t.Fatal("remove unexpectedly succeeded while DELETE sharing was denied")
	}

	fi, err := os.Stat(part)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o222 != 0 {
		t.Fatalf("failed remove changed permissions: %v", fi.Mode())
	}
}
