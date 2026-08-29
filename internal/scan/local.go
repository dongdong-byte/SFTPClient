package scan

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// LocalLister 는 로컬 파일시스템을 나열하는 DirLister 이다.
//
// 상태가 없으므로 값으로 사용한다.
//
//	sc := scan.New(scan.LocalLister{})
//
// SMB 네트워크 공유도 OS 에서는 일반 파일시스템 경로로 접근하므로
// 같은 구현을 사용한다.
//
// MVP 2 의 원격 SFTP 나열은 DirLister 의 별도 구현으로 교체한다.
type LocalLister struct{}

// List 는 os.ReadDir 결과를 Entry 로 옮긴다.
//
// 의미에 따른 필터링은 하지 않는다.
// 0바이트, .part, 하위 디렉터리도 관측한 그대로 Entry 로 만든다.
// 전송 가능한 파일인지 판정하는 것은 verify 의 책임이다.
//
// os.ReadDir 자체는 context 취소를 지원하지 않는다.
// 따라서 호출 전과 각 항목 처리 사이에서 취소 여부를 확인한다.
//
// DirEntry.Info 의 실제 비용은 OS 와 파일시스템에 따라 달라질 수 있다.
// 특히 SMB/NAS 환경에서는 성능을 실측하여 판단한다.
func (LocalLister) List(
	ctx context.Context,
	dir string,
) ([]Entry, error) {
	if ctx == nil {
		return nil, fmt.Errorf(
			"%w: context is nil",
			ErrInvalidInput,
		)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	des, err := os.ReadDir(dir)
	if err != nil {
		// *os.PathError 를 그대로 올린다.
		//
		// Scanner 에서
		//
		//	errors.Is(err, fs.ErrNotExist)
		//
		// 로 존재하지 않는 디렉터리를 정상적인 빈 슬롯과
		// 구분할 수 있어야 한다.
		return nil, err
	}

	entries := make([]Entry, 0, len(des))

	for _, de := range des {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		info, err := de.Info()
		if err != nil {
			// Info 호출 중 context 가 취소되었다면
			// 일반적인 파일시스템 오류보다 취소를 우선한다.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}

			// ReadDir 와 Info 사이에서 항목이 삭제된 경우이다.
			//
			// 이미 사라진 항목은 현재 Scan 에서 처리할 대상도 없으므로
			// 해당 항목만 건너뛴다.
			//
			// 이것은 0바이트/.part 같은 의미 기반 필터링과는 다르다.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}

			// 권한 오류 등 다른 실패가 발생하면 이 디렉터리의
			// 나열 결과가 완전하다고 보장할 수 없으므로 실패시킨다.
			//
			// 일부 Entries 만 성공한 것처럼 반환하면 호출자가
			// 완전한 목록으로 오인할 수 있다.
			return nil, err
		}

		entries = append(entries, Entry{
			Name:  de.Name(),
			Size:  info.Size(),
			MTime: info.ModTime(),
			IsDir: info.IsDir(),
		})
	}

	return entries, nil
}
