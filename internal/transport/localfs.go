// Package transport 는 파일을 옮기는 계층이다.
//
// 이 패키지는 파일만 옮긴다. PUT 정책·Ledger·MaxRetries·상태 전이를
// 모르고, put 을 import 하지 않는다 (put.Uploader 를 구조적으로 만족).
// 재시도도 하지 않는다 — 재시도 간격은 매시 스케줄러가, 누적 상한은
// put_ledger.attempts 가 소유한다.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	// uploadChunk 는 UploadPart 의 복사 단위다 (LocalFS·SFTPFS 공용).
	//
	// io.Copy 는 ctx 를 모르므로 청크 사이에서 직접 취소를 검사한다
	// (Uploader 계약). Daily 파일은 수십 MB 라 한 덩어리로 복사하면
	// 취소가 파일 하나 끝날 때까지 동작하지 않는다.
	//
	// 1MiB 는 왕복/syscall 횟수와 취소 반응성 사이의 절충이다.
	// SFTPFS 에서는 청크마다 직렬 왕복이 생기므로 처리량 대가가 있다.
	// 실측에서 느리면 취소를 포기하지 말고 이 값을 키운다.
	uploadChunk = 1 << 20

	// dirPerm 은 EnsureDir 이 만드는 디렉터리 권한이다.
	// Windows 에서는 대체로 무시되며 Linux 대응(MVP 5) 때 재검토한다.
	dirPerm = 0o755

	// partPerm 은 UploadPart 가 만드는 .part 파일 권한이다.
	// 실제 값은 umask 의 영향을 받는다.
	partPerm = 0o644
)

// LocalFS 는 로컬 파일시스템으로의 전송이다.
//
// 무상태이며 동시 호출 가능하다.
//
// 존재 이유:
//
//  1. SFTP 서버 없이 전송 상태 머신 전체
//     (.part → Size 대조 → Rename → 최종 검증)를 실행·테스트한다.
//  2. 이후 SFTPFS 가 같은 put.Uploader 계약을 지키는지 비교하는
//     기준 구현이 된다.
//
// LocalFS 는 PUT 정책, Ledger 상태, Retry 정책을 알지 않는다.
// 순수하게 파일시스템 작업만 수행한다.
//
// put.Uploader 만족은 구조적(structural)이며 여기서 단언할 수 없다.
// transport 가 put 을 import 하지 않기 때문이다.
// 컴파일 타임 확인은 localfs_test.go 에 둔다.
type LocalFS struct{}

// EnsureDir 은 dir 및 필요한 상위 디렉터리를 만든다.
//
// 계약:
//   - 이미 디렉터리로 존재하면 성공한다.
//   - 경로 중간 또는 최종 위치에 같은 이름의 일반 파일이 있으면 오류다.
//
// os.MkdirAll 이 두 동작을 그대로 제공한다. 경로 성분 중 하나가
// 디렉터리가 아니면 오류를 반환하므로 별도 검사를 두지 않는다.
func (LocalFS) EnsureDir(ctx context.Context, dir string) error {
	// os.MkdirAll 자체는 context 취소를 지원하지 않지만,
	// 이미 취소된 실행이 새로운 파일 작업을 시작하는 것은 막는다.
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("ensure dir: %w", err)
	}

	return nil
}

// UploadPart 는 localPath 의 내용을 partPath 로 복사한다.
//
// 부모 디렉터리는 만들지 않는다.
// 디렉터리 생성은 EnsureDir 의 책임이며, 여기서 MkdirAll 을 해버리면
// 호출 순서 누락이 transport 내부에 숨게 된다.
//
// 기존 .part 는 truncate 후 처음부터 덮어쓴다.
// 이전 실행에서 남은 .part 는 불완전한 사본일 수 있으므로
// 이어쓰기나 보존을 하지 않는다.
//
// 복사 중 실패하거나 context 가 취소되면 partPath 는 일부만 기록된
// 상태로 남을 수 있다. 잔여 .part 정리는 호출자(put.Transfer)가
// best-effort Remove 로 처리한다.
//
// named return 을 쓰는 이유는 dst.Close 를 한 곳에서만 하기 위함이다.
// 반환 경로마다 Close 를 흩어 두면 분기를 추가할 때 한 곳을 빠뜨려
// 파일 디스크립터가 샌다.
func (LocalFS) UploadPart(
	ctx context.Context,
	localPath string,
	partPath string,
) (err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local: %w", err)
	}
	defer func() {
		// 읽기 전용 source 의 Close 실패는 전송 결과를 바꾸는 근거로
		// 사용하지 않는다. 실제 기록 대상인 dst 의 Close 오류만 반영한다.
		_ = src.Close()
	}()

	dst, err := os.OpenFile(
		partPath,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		partPerm,
	)
	if err != nil {
		return fmt.Errorf("create .part: %w", err)
	}
	defer func() {
		// Close 오류를 버리지 않는다.
		// 파일시스템/장치의 지연 write 오류가 Close 시점에 드러난다.
		//
		// 이후 Size 대조도 수행하지만, 정상적으로 Close 조차 되지 않은
		// 파일을 검증 대상으로 넘기지 않는 편이 명확하다.
		//
		// 앞선 오류가 있으면 그것을 우선한다. 원인은 그쪽이고
		// Close 실패는 대개 그 결과이기 때문이다.
		if cerr := dst.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close .part: %w", cerr)
		}
	}()

	buf := make([]byte, uploadChunk)

	for {
		// io.Copy 를 한 번에 사용하면 context 취소가 복사 종료까지
		// 반영되지 않는다. 1MiB 청크 사이에서 취소를 검사한다.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("upload canceled: %w", ctxErr)
		}

		n, readErr := src.Read(buf)

		// 읽은 바이트를 먼저 처리한 뒤 readErr 를 본다.
		// Read 는 n > 0 과 io.EOF 를 함께 돌려줄 수 있으므로,
		// 순서를 바꾸면 마지막 청크가 통째로 유실된다.
		if n > 0 {
			// os.File.Write 는 일반적으로 전체 slice 를 쓰지만,
			// io.Writer 계약상 short write 가능성을 배제할 수 없으므로
			// n 바이트가 모두 기록될 때까지 처리한다.
			for written := 0; written < n; {
				m, writeErr := dst.Write(buf[written:n])
				if writeErr != nil {
					return fmt.Errorf("write .part: %w", writeErr)
				}

				if m == 0 {
					return fmt.Errorf("write .part: %w", io.ErrShortWrite)
				}

				written += m
			}
		}

		if errors.Is(readErr, io.EOF) {
			return nil
		}

		if readErr != nil {
			return fmt.Errorf("read local: %w", readErr)
		}
	}
}

// Size 는 path 의 정규 파일 크기를 반환한다.
//
// 디렉터리는 Transfer Verification 대상이 아니므로 오류다.
//
// 파일이 존재하지 않는 경우 os.Stat 오류를 %w 로 보존하므로
//
//	errors.Is(err, fs.ErrNotExist)
//
// 가 true 가 된다. 이는 put.Uploader 계약이며,
// 이후 SFTPFS 도 동일한 의미로 오류를 정규화해야 한다.
func (LocalFS) Size(ctx context.Context, path string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	fi, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat: %w", err)
	}

	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("stat: %q is not a regular file", path)
	}

	return fi.Size(), nil
}

// Rename 은 oldPath 를 newPath 로 전환한다.
//
// PUT 에서는 검증이 끝난 .part 를 최종 이름으로 바꾸는 데 사용한다.
//
// os.Rename 만 쓴다. Unix 와 Windows(Go 1.5+, MoveFileEx
// MOVEFILE_REPLACE_EXISTING) 모두 대상이 파일이면 덮어쓴다.
//
// dest 를 지운 뒤 다시 rename 하는 fallback 은 두지 않는다.
// 첫 실패가 "대상 존재"가 아니라 EXDEV·권한이면 dest 만 사라지고
// source 는 그대로라, revision 재전송 경로에서 최종 파일만 잃는다.
func (LocalFS) Rename(
	ctx context.Context,
	oldPath string,
	newPath string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// Remove 는 path 의 파일을 삭제한다.
//
// 멱등적이다. 대상이 이미 존재하지 않으면 성공으로 취급한다.
//
// 디렉터리는 오류다. os.Remove 는 빈 디렉터리도 지우므로,
// 계약("재귀가 아니며 디렉터리는 오류")을 지키려면 종류를 먼저 본다.
// 실무에서 partPath 가 디렉터리일 일은 없지만, LocalFS 와 SFTPFS 가
// 같은 계약 테스트를 통과해야 하므로 동작을 맞춰 둔다.
//
// 심볼릭 링크는 링크 자체를 지운다. 따라서 Lstat 을 쓴다.
//
// 사용처:
//   - 정상적으로 포착한 전송 실패의 잔여 .part cleanup
//   - 이후 IN_PROGRESS startup recovery
//
// 두 경로 모두 "이미 없음" 은 정상 상태이므로 오류가 아니다.
func (LocalFS) Remove(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("remove: stat: %w", err)
	}

	if fi.IsDir() {
		return fmt.Errorf("remove: %q is a directory", path)
	}

	// Lstat 과 Remove 사이에 대상이 사라질 수 있으므로
	// 여기서도 "이미 없음" 을 성공으로 받는다.
	if err := os.Remove(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("remove: %w", err)
	}

	return nil
}

// Join 은 LocalFS 경로 규칙으로 dir 과 name 을 결합한다.
//
// LocalFS 는 호스트 OS 경로를 사용하므로 filepath.Join 을 쓴다.
// 이후 SFTPFS 는 '/' 기반 path.Join 을 사용한다.
//
// 이렇게 해서 put 패키지가 Windows '\' 또는 SFTP '/' 규칙을
// 직접 알 필요가 없게 한다.
func (LocalFS) Join(dir, name string) string {
	return filepath.Join(dir, name)
}
