// Package download 는 원격 SFTP 의 RINEX 파일을 로컬로 수신하는 DOWNLOAD
// 유스케이스의 주인이다 (DOWNLOAD 설계 v3 §3.1).
//
//	원격 경로 전개와 목록 조회    scan.Scanner + sftpLister (이 파일)
//	RINEX·SITE 검증, 목적지 결정  (커밋 4)
//	수신·크기 검증·최종 Rename    (커밋 5)
//	회차 보고                     (커밋 6)
//
// PUT 장부(common_ledger·put_ledger)와 put 패키지를 읽거나 호출하지 않는다.
// 바이트 이동은 transport 가 하고, download 는 필요한 최소 능력만
// RemoteFS 로 선언해 받는다 — transport 를 import 하지 않는다 (단방향 의존).
package download

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"strings"

	"SFTPClient/internal/scan"
)

// RemoteFS 는 DOWNLOAD 가 원격에 요구하는 능력 전부다
// (DOWNLOAD 커밋 계획 §4.1).
//
// 인터페이스는 사용하는 쪽(download)이 선언한다. 구현은 transport.SFTPFS 가
// 구조적으로 만족하고 main 이 배선한다 (put.Uploader 와 같은 방식).
//
// 원격 크기는 목록에서 받은 scan.Entry.Size 를 쓰므로 Size 메서드를 두지
// 않는다. 원격 Rename·Remove·쓰기도 없다 — DOWNLOAD 는 원격을 바꾸지 않는다.
// 접속 수명(Close·Abort)도 이 계약 밖이다. 만든 쪽(main)이 관리한다.
type RemoteFS interface {
	// ReadDir 은 원격 디렉터리 하나의 항목을 돌려준다.
	//
	// 없는 경로는 errors.Is(err, fs.ErrNotExist) 가 성립해야 한다. 그 외의
	// 오류(권한, 디렉터리가 아님, 목록 실패)는 fs.ErrNotExist 가 아니어야
	// 한다 — Scanner 가 전자만 빈 날짜 슬롯(Missing)으로 넘긴다.
	//
	// 항목의 Mode() 는 lstat 의미여야 한다. 링크를 따라가 대상의 종류로
	// 보고하면 링크된 폴더 아래까지 수신하게 된다.
	ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error)

	// DownloadPart 는 원격 remotePath 의 내용을 로컬 localPartPath 로
	// 처음부터 받는다. 크기 판정·Rename·실패 후 정리는 하지 않는다.
	DownloadPart(ctx context.Context, remotePath, localPartPath string) error
}

// sftpLister 는 RemoteFS.ReadDir 을 scan.DirLister 로 바꾸는 어댑터다
// (DOWNLOAD 커밋 계획 §4.2).
//
// 이 어댑터 덕분에 PUT 이 쓰는 scan.Scanner 를 수정 없이 원격에 쓴다.
// 날짜 전개, (SITE)·(HH) 패턴 단의 부모 나열, 재귀, 종류별 선별(진짜 폴더만
// 하강·일반 파일만 수집·그 외 Irregular), Missing·Unmatched 집계는 전부
// Scanner 의 것이다. 어댑터는 관측값을 옮기기만 하고 판정하지 않는다.
//
// 이름이 "sftp" 인 것은 용도(원격 SFTP 나열)를 뜻한다. 실제 의존은
// RemoteFS 뿐이다.
type sftpLister struct {
	remote RemoteFS
}

// newSFTPLister 는 remote 를 나열하는 scan.DirLister 를 만든다.
func newSFTPLister(remote RemoteFS) sftpLister {
	return sftpLister{remote: remote}
}

// errNilRemote 는 배선 누락(RemoteFS 가 nil)이다. 조용히 빈 목록을 돌려주면
// 모든 날짜가 "파일 없음"으로 끝나 원인을 알 수 없으므로 오류로 올린다.
var errNilRemote = errors.New("download: remote fs is nil")

// List 는 원격 디렉터리 하나를 scan.Entry 목록으로 옮긴다.
//
// 필드 대응은 scan.LocalLister 와 같다:
//
//	Name   FileInfo.Name()
//	Size   FileInfo.Size()      (디렉터리의 값은 쓰이지 않는다)
//	MTime  FileInfo.ModTime()
//	Type   Mode() & fs.ModeType (종류 비트만. 권한 비트는 버린다)
//	IsDir  Type 에 fs.ModeDir 가 있는지
//
// 링크는 Mode 에 fs.ModeSymlink 로 오므로 IsDir=false, Type=ModeSymlink 가
// 되어 Scanner 가 따라가지 않고 Irregular 로 센다. 폴더 이름(00~23, 관측소
// 코드 등)은 해석하지 않는다 — 패턴 대조는 Scanner 의 Match 몫이다.
//
// 어댑터가 스스로 막는 경우는 로컬 경로 한 단으로 쓸 수 없는 이름뿐이다.
// 항목을 버리지 않고 fs.ModeIrregular 로 바꿔 Scanner 의 Irregular 집계에
// 드러낸다:
//
//   - 빈 이름, ".", "..", '/'·'\'·NUL 포함. 그대로 넘기면 하강 경로나
//     나중의 로컬 목적지가 다른 곳을 가리킨다.
//   - Windows 파일 이름에 쓸 수 없는 글자(< > : " | ? *)와 제어 문자.
//     ':' 는 NTFS 대체 데이터 스트림으로 해석되어 진짜 파일을 영구히 막는다
//     (isSinglePathElement 주석).
//   - 끝이 공백·점인 이름. Windows 는 그 글자를 지우고 연다. 원격의
//     "a.gz." 와 "a.gz" 가 로컬의 같은 파일을 덮어쓴다.
//   - CON·NUL·COM1 같은 예약 장치 이름(확장자 포함). Windows 는
//     "D:\RNX\NUL.gz" 를 파일이 아니라 NUL 장치로 연다. 수신이 성공한
//     것처럼 보이고 데이터는 사라진다.
//
// Mode()==0 은 바꾸지 않는다. fs.FileMode 에서 종류 비트가 없는 값은 정상
// 일반 파일이고 권한 000 파일도 같은 값이다. 또한 SFTP v3 서버가 선택적인
// permissions 속성을 생략하면 pkg/sftp 가 mode 0 을 돌려줄 수 있다. 이를
// 비정규로 추측하면 읽을 수 있는 정상 파일까지 전부 수신 대상에서 빠진다.
//
// 오류는 감싸지 않고 그대로 올린다. fs.ErrNotExist 판별과 취소 판별은
// RemoteFS 의 계약이며 Scanner 가 그대로 쓴다. 일부 항목만 옮긴 불완전한
// 목록을 돌려주는 경로는 없다.
func (l sftpLister) List(ctx context.Context, dir string) ([]scan.Entry, error) {
	if ctx == nil {
		return nil, errors.New("download: list: context is nil")
	}

	if l.remote == nil {
		return nil, errNilRemote
	}

	infos, err := l.remote.ReadDir(ctx, dir)
	if err != nil {
		return nil, err
	}

	entries := make([]scan.Entry, 0, len(infos))

	for _, fi := range infos {
		if fi == nil {
			// 계약 위반 구현에서만 나온다. 무엇이었는지 알 수 없는 항목을
			// 파일이나 폴더로 추측하지 않는다.
			return nil, errors.New("download: list: remote returned a nil entry")
		}

		entries = append(entries, entryOf(fi))
	}

	// SFTP READDIR 순서는 서버 구현과 실행마다 달라질 수 있다. Scanner 는
	// 하위 폴더는 정렬하지만 같은 디렉터리의 Batch.Entries 는 받은 순서를
	// 유지한다. 대소문자만 다른 두 이름처럼 같은 목적지로 수렴하는 파일의
	// 승자가 매 회차 바뀌지 않도록 DOWNLOAD 경계에서 이름순으로 고정한다.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// entryOf 는 FileInfo 하나를 scan.Entry 로 옮긴다. (List 주석의 대응표)
func entryOf(fi fs.FileInfo) scan.Entry {
	name := fi.Name()
	mode := fi.Mode()
	typ := mode & fs.ModeType

	if !isSinglePathElement(name) {
		typ = fs.ModeIrregular
	}

	return scan.Entry{
		Name:  name,
		Size:  fi.Size(),
		MTime: fi.ModTime(),
		IsDir: typ&fs.ModeDir != 0,
		Type:  typ,
	}
}

// isSinglePathElement 는 name 이 경로 한 단으로 안전한 이름인지 답한다.
//
// '\' 도 막는다. 원격이 Linux 여도 이 이름은 로컬(Windows) 목적지 경로에
// 그대로 쓰이며, 거기서 '\' 는 구분자다. RINEX 파일명·폴더명에는 나오지
// 않는 글자다.
func isSinglePathElement(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}

	if strings.ContainsAny(name, "/\\\x00") {
		return false
	}

	// Windows 파일 이름에 쓸 수 없는 글자와 제어 문자. Linux 원격에서는
	// 합법이라 서버가 실제로 줄 수 있다. 특히 ':' 는 NTFS 대체 데이터 스트림
	// 구분자다. 원격 "a.gz:x" 를 받으면 로컬에 0바이트 "a.gz" 가 생기고
	// 내용은 숨은 스트림에 들어간다. 그 뒤 진짜 "a.gz" 는 크기가 다른 기존
	// 파일 때문에 매 회차 충돌로 남아 영원히 받지 못한다.
	if strings.ContainsAny(name, `<>:"|?*`) {
		return false
	}

	for _, r := range name {
		if r < 0x20 {
			return false
		}
	}

	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return false
	}

	return !isWindowsReservedName(name)
}

// isWindowsReservedName 은 Windows 가 경로의 마지막 단으로 보면 장치로
// 여는 이름인지 답한다. 첫 '.' 앞만 본다. "NUL.gz" 도 NUL 이다.
// 판정은 대소문자를 접는다. Linux 에서도 같은 이름은 받지 않는다.
// RINEX 이름에는 없고, 받으면 Windows 목적지에서 데이터가 사라진다.
func isWindowsReservedName(name string) bool {
	base := name
	if i := strings.IndexByte(name, '.'); i >= 0 {
		base = name[:i]
	}

	base = strings.TrimRight(base, " ")

	upper := strings.ToUpper(base)

	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return true
	}

	for _, prefix := range []string{"COM", "LPT"} {
		if !strings.HasPrefix(upper, prefix) {
			continue
		}

		// Windows 는 ASCII 숫자 장치명뿐 아니라 ISO-8859-1의 위첨자
		// ¹²³도 COM/LPT 장치 별칭으로 인식한다. 0도 일부 Win32 경로에서
		// 장치로 해석될 수 있어 보수적으로 막는다. RINEX 이름에는 없다.
		switch strings.TrimPrefix(upper, prefix) {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
			"¹", "²", "³":
			return true
		}
	}

	return false
}
