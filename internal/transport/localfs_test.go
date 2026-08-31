package transport

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"SFTPClient/internal/put"
)

// 생산 코드는 put 을 import 하지 않는다.
// 구조적 만족은 테스트에서만 고정한다.
var _ put.Uploader = LocalFS{}

func TestLocalFSEnsureDirIdempotent(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "a", "b")
	fsys := LocalFS{}
	ctx := context.Background()

	if err := fsys.EnsureDir(ctx, dir); err != nil {
		t.Fatalf("EnsureDir() 첫 호출: %v", err)
	}

	if err := fsys.EnsureDir(ctx, dir); err != nil {
		t.Fatalf("EnsureDir() 재호출: %v", err)
	}
}

func TestLocalFSEnsureDirRejectsFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "notdir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := LocalFS{}.EnsureDir(context.Background(), path)
	if err == nil {
		t.Fatal("파일이 있는 경로에 EnsureDir() 이 성공했다")
	}
}

func TestLocalFSUploadPartOverwrites(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	src := filepath.Join(root, "src.bin")
	part := filepath.Join(root, "dst.part")
	ctx := context.Background()
	fsys := LocalFS{}

	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(part, []byte("stale-part-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := fsys.UploadPart(ctx, src, part); err != nil {
		t.Fatalf("UploadPart() 실패: %v", err)
	}

	got, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("덮어쓰기 실패: got %q", got)
	}
}

func TestLocalFSUploadPartRequiresParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	src := filepath.Join(root, "src.bin")
	part := filepath.Join(root, "missing", "dst.part")

	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := LocalFS{}.UploadPart(context.Background(), src, part)
	if err == nil {
		t.Fatal("부모 없는 UploadPart() 가 성공했다")
	}
}

func TestLocalFSSizeNotExist(t *testing.T) {
	t.Parallel()

	_, err := LocalFS{}.Size(
		context.Background(),
		filepath.Join(t.TempDir(), "no-such"),
	)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Size(없음) = %v, want fs.ErrNotExist", err)
	}
}

func TestLocalFSSizeRejectsDir(t *testing.T) {
	t.Parallel()

	_, err := LocalFS{}.Size(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("디렉터리 Size() 가 성공했다")
	}

	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("디렉터리를 없음으로 보고했다: %v", err)
	}
}

func TestLocalFSRenameOverwritesExisting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldPath := filepath.Join(root, "file.part")
	newPath := filepath.Join(root, "file.rnx")
	ctx := context.Background()
	fsys := LocalFS{}

	if err := os.WriteFile(oldPath, []byte("revision-2"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(newPath, []byte("revision-1"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := fsys.Rename(ctx, oldPath, newPath); err != nil {
		t.Fatalf("Rename() 덮어쓰기 실패: %v", err)
	}

	got, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(got, []byte("revision-2")) {
		t.Fatalf("덮어쓴 내용 = %q, want revision-2", got)
	}

	if _, err := os.Stat(oldPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("oldPath 가 남아 있다: %v", err)
	}
}

func TestLocalFSRemoveMissingIsNil(t *testing.T) {
	t.Parallel()

	err := LocalFS{}.Remove(
		context.Background(),
		filepath.Join(t.TempDir(), "gone.part"),
	)
	if err != nil {
		t.Fatalf("없는 파일 Remove() = %v, want nil", err)
	}
}

func TestLocalFSRemoveRejectsDir(t *testing.T) {
	t.Parallel()

	err := LocalFS{}.Remove(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("디렉터리 Remove() 가 성공했다")
	}
}

func TestLocalFSUploadPartCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := LocalFS{}.UploadPart(ctx, "src", "dst")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("취소된 ctx UploadPart() = %v, want Canceled", err)
	}
}
