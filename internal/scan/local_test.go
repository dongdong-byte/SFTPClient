package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalLister_List(t *testing.T) {
	root := t.TempDir()

	normalPath := filepath.Join(root, "normal.rnx.gz")
	zeroPath := filepath.Join(root, "zero.rnx.gz")
	partPath := filepath.Join(root, "upload.part")
	subdirPath := filepath.Join(root, "subdir")

	if err := os.WriteFile(
		normalPath,
		[]byte("1234567890"),
		0o600,
	); err != nil {
		t.Fatalf("normal file 생성 실패: %v", err)
	}

	if err := os.WriteFile(
		zeroPath,
		nil,
		0o600,
	); err != nil {
		t.Fatalf("zero file 생성 실패: %v", err)
	}

	if err := os.WriteFile(
		partPath,
		[]byte("partial"),
		0o600,
	); err != nil {
		t.Fatalf("part file 생성 실패: %v", err)
	}

	if err := os.Mkdir(
		subdirPath,
		0o755,
	); err != nil {
		t.Fatalf("subdir 생성 실패: %v", err)
	}

	lister := LocalLister{}

	entries, err := lister.List(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf(
			"entries = %d, want 4",
			len(entries),
		)
	}

	// 이름으로 찾는다.
	// os.ReadDir 의 정렬 순서에 테스트가 불필요하게 의존하지 않는다.
	got := make(map[string]Entry, len(entries))

	for _, entry := range entries {
		if _, exists := got[entry.Name]; exists {
			t.Fatalf(
				"duplicate Entry name %q",
				entry.Name,
			)
		}

		got[entry.Name] = entry
	}

	tests := []struct {
		name  string
		path  string
		isDir bool
	}{
		{
			name:  "normal.rnx.gz",
			path:  normalPath,
			isDir: false,
		},
		{
			name:  "zero.rnx.gz",
			path:  zeroPath,
			isDir: false,
		},
		{
			name:  "upload.part",
			path:  partPath,
			isDir: false,
		},
		{
			name:  "subdir",
			path:  subdirPath,
			isDir: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := got[tt.name]
			if !ok {
				t.Fatalf(
					"Entry %q not found",
					tt.name,
				)
			}

			info, err := os.Stat(tt.path)
			if err != nil {
				t.Fatalf(
					"os.Stat(%q): %v",
					tt.path,
					err,
				)
			}

			if entry.Name != tt.name {
				t.Errorf(
					"Name = %q, want %q",
					entry.Name,
					tt.name,
				)
			}

			if entry.IsDir != tt.isDir {
				t.Errorf(
					"IsDir = %v, want %v",
					entry.IsDir,
					tt.isDir,
				)
			}

			if entry.Size != info.Size() {
				t.Errorf(
					"Size = %d, want %d",
					entry.Size,
					info.Size(),
				)
			}

			if !entry.MTime.Equal(info.ModTime()) {
				t.Errorf(
					"MTime = %v, want %v",
					entry.MTime,
					info.ModTime(),
				)
			}
		})
	}

	// Scan 단계에서 0바이트를 제거하면 안 된다.
	if entry := got["zero.rnx.gz"]; entry.Size != 0 {
		t.Errorf(
			"zero.rnx.gz Size = %d, want 0",
			entry.Size,
		)
	}

	// .part 역시 LocalLister 에서는 제거하지 않는다.
	if _, ok := got["upload.part"]; !ok {
		t.Error(".part file was filtered by LocalLister")
	}

	// 하위 디렉터리도 관측 사실로 전달한다.
	if entry := got["subdir"]; !entry.IsDir {
		t.Error("subdir must be returned with IsDir=true")
	}
}

func TestLocalLister_EmptyDirectory(t *testing.T) {
	root := t.TempDir()

	entries, err := (LocalLister{}).List(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf(
			"entries = %d, want 0",
			len(entries),
		)
	}
}

func TestLocalLister_MissingDirectoryPreservesNotExist(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(
		root,
		"does-not-exist",
	)

	_, err := (LocalLister{}).List(
		context.Background(),
		missing,
	)

	if err == nil {
		t.Fatal("missing directory: expected error")
	}

	// Scanner 가 이 판정으로 Missing 을 계산하므로
	// LocalLister 가 오류 종류를 잃어버리면 안 된다.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(
			"error = %v, want fs.ErrNotExist",
			err,
		)
	}
}

func TestLocalLister_PathIsFile(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(
		root,
		"not-a-directory.txt",
	)

	if err := os.WriteFile(
		path,
		[]byte("data"),
		0o600,
	); err != nil {
		t.Fatalf("test file 생성 실패: %v", err)
	}

	_, err := (LocalLister{}).List(
		context.Background(),
		path,
	)

	if err == nil {
		t.Fatal("file path: expected error")
	}

	// OS별 구체 오류 문자열까지 계약으로 묶지 않는다.
	// 오류가 발생한다는 사실만 LocalLister 계약이다.
}

func TestLocalLister_ContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	cancel()

	root := t.TempDir()

	_, err := (LocalLister{}).List(
		ctx,
		root,
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"List() error = %v, want context.Canceled",
			err,
		)
	}
}

func TestLocalLister_NilContext(t *testing.T) {
	root := t.TempDir()

	_, err := (LocalLister{}).List(
		nil,
		root,
	)

	if err == nil {
		t.Fatal("nil context: expected error")
	}

	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf(
			"error = %v, want ErrInvalidInput",
			err,
		)
	}
}
