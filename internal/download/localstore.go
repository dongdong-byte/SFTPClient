package download

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// OSLocalStore 는 LocalStore 의 운영 구현이다. 로컬 파일시스템을 그대로 쓴다.
//
// 상태가 없으므로 값으로 쓴다. main 이 Runner.Local 에 넣는다.
//
// ctx: os 파일 호출은 context 를 받지 않는다. 각 메서드는 시작 전에 취소를
// 확인할 뿐이며, 이미 시작한 로컬 호출을 중간에 끊지는 않는다.
type OSLocalStore struct{}

// EnsureDir 은 os.MkdirAll 이다. 같은 이름의 파일이 있으면 오류다.
func (OSLocalStore) EnsureDir(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}

	return nil
}

// Stat 은 os.Lstat 이다 (LocalStore.Stat 의 lstat 계약). 링크를 따라가지
// 않으므로 최종 경로 자리의 링크는 decideTarget 에서 비정상 대상이 된다.
func (OSLocalStore) Stat(ctx context.Context, path string) (fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return os.Lstat(path)
}

// Size 는 일반 파일의 크기다. 일반 파일이 아니면 오류다 — .part 자리의
// 디렉터리·링크를 크기 검증으로 통과시키지 않는다.
func (OSLocalStore) Size(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	fi, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}

	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("size: %q is not a regular file (%v)", path, fi.Mode().Type())
	}

	return fi.Size(), nil
}

// Rename 은 os.Rename 이다.
//
// 대상이 있으면 Windows·Linux 모두 덮어쓴다. 덮어쓰기 방지는 러너의 Rename
// 직전 재확인이 맡는다. 재확인과 Rename 사이에 외부 프로세스가 같은 이름을
// 만드는 극단적 경합은 "DOWNLOAD LocalPath 는 이 프로그램 전용" 운영 전제로
// 남긴다 (커밋 계획 §6.2).
func (OSLocalStore) Rename(ctx context.Context, oldPath, newPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return os.Rename(oldPath, newPath)
}

// Remove 는 일반 파일 하나를 지운다. 없으면 성공이다.
//
// 일반 파일이 아니면 지우지 않고 오류다. os.Remove 는 빈 디렉터리도
// 지우는데, .part 자리에 운영자가 만든 빈 폴더가 있으면 그것을 조용히
// 없애게 된다. 링크도 지우지 않는다 — 우리가 만든 것이 아니다.
//
// Windows에서는 실제 읽기 전용 정규 파일만 제한적으로 속성을 풀어 지운다.
// sharing violation 등 다른 삭제 실패에서는 권한을 바꾸지 않고 오류를 남겨
// 운영자가 대상을 확인한 뒤 조치하게 한다.
func (OSLocalStore) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	if err != nil {
		return err
	}

	if !fi.Mode().IsRegular() {
		return fmt.Errorf("remove: %q is not a regular file (%v)", path, fi.Mode().Type())
	}

	err = removeRegularFile(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return err
}

// Join 은 filepath.Join 이다.
func (OSLocalStore) Join(dir, name string) string {
	return filepath.Join(dir, name)
}
