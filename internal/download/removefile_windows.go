//go:build windows

package download

import "golang.org/x/sys/windows"

// removeRegularFile은 일반 삭제가 ACCESS_DENIED이고 현재 이름이 실제
// 읽기 전용 정규 파일일 때만 속성을 풀어 한 번 재시도한다. os.Remove처럼
// sharing violation 등 다른 실패에서 속성을 바꾸지 않는다.
func removeRegularFile(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err = windows.DeleteFile(p); err == nil || err != windows.ERROR_ACCESS_DENIED {
		return err
	}

	attrs, attrErr := windows.GetFileAttributes(p)
	if attrErr != nil || attrs&windows.FILE_ATTRIBUTE_READONLY == 0 ||
		attrs&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return err
	}
	if attrErr = windows.SetFileAttributes(p, attrs&^windows.FILE_ATTRIBUTE_READONLY); attrErr != nil {
		return err
	}
	if err = windows.DeleteFile(p); err != nil {
		_ = windows.SetFileAttributes(p, attrs)
	}
	return err
}
