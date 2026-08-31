package put

import "context"

// Uploader 는 put 이 전송 계층에 요구하는 최소 파일 작업 계약이다.
//
// put 은 구체 구현(os, SFTP, SSH)을 모른다.
// transport 구현체는 이 계약만 만족하면 되고, 구현이 늘어나도 put 은
// 변하지 않는다. 인터페이스를 transport 가 아니라 put 에 두는 이유는
// put 이 소비자이기 때문이다 (GUIDELINES 4절 consumer-side 선언).
//
// 이 인터페이스는 파일 작업만 담당한다. 다음은 책임이 아니다.
//
//	Ledger 상태 전이 (BeginPut / FinishPut / FailPut)
//	Retry 판단과 MaxRetries
//	Worker Pool
//	Transfer Verification 의 업무 판정 (크기 조회만 하고, 일치는 호출자)
//
// 재시도 간격은 스케줄러가, 누적 상한은 put_ledger.attempts 가 소유한다.
// 동일 실행 안에서 즉시 재시도하지 않는다.
//
// 이 계약의 목적은 LocalFS 와 SFTPFS 가 같은 흐름에서 같은 결과를 내는
// 것이다. 두 구현이 갈릴 수 있는 지점은 각 메서드 주석에 명시한다.
// LocalFS 로 개발하는 동안에는 드러나지 않고 SFTP 로 바꾸는 순간
// 나타나는 차이가 실재하므로, 동작을 구현체 재량에 맡기지 않는다.
//
// 경로 인자 규칙:
//   - UploadPart 의 localPath 만 호스트 OS 경로다 (항상 os 로 연다).
//   - dir, partPath, path, oldPath, newPath, Join 의 인자는
//     구현체가 이해하는 목적지 경로다.
//     호출자는 목적지 경로를 구현체 규칙에 맞는 문자열로 넘긴다.
//     Windows 드라이브 세그먼트(E:)는 LocalFS 의 문제이며, SFTPFS 에
//     넘기지 않는다. GUIDELINES 9.2 의 드라이브 건너뛰기를 SFTP 구현
//     의무로 두지 않는다.
//
// 동시성: 하나의 Uploader 는 MaxWorkers 워커가 동시에 호출해도 된다.
// EnsureDir 중복 호출을 걷는 캐시는 호출자(transfer)가 가지며,
// 그 캐시의 동기화도 호출자 책임이다. 구현체는 무상태로 둔다.
type Uploader interface {
	// EnsureDir 은 dir 및 필요한 상위 디렉터리를 만든다.
	// dir 이 이미 디렉터리로 존재하면 성공이다.
	// 같은 이름의 파일이 있으면 오류다. "이미 있음" 은 디렉터리에만 적용한다.
	//
	// 기존 운영 스크립트도 put 전에 목적지 경로를 세그먼트 단위로
	// 재귀 생성하고 있었다. 이 절차가 없으면 첫 전송이 전부
	// "No such file (code 2)" 로 실패한다 (GUIDELINES 9.2).
	//
	// 같은 dir 로 반복 호출될 수 있다. 중복 호출을 걷어내는 캐시는
	// 구현체가 아니라 호출자(transfer)가 가진다. 구현체를 무상태로
	// 두어야 LocalFS 와 SFTPFS 가 같은 최적화를 각자 구현하지 않는다.
	EnsureDir(ctx context.Context, dir string) error

	// UploadPart 는 localPath 의 내용을 partPath 로 복사한다.
	// partPath 가 이미 존재하면 덮어쓴다.
	//
	// localPath 는 호스트 OS 경로, partPath 는 목적지 경로다.
	// 부모 디렉터리는 만들지 않는다. 없으면 오류다.
	// 디렉터리 생성은 EnsureDir 의 책임이며, 여기서 MkdirAll 을 하면
	// 9.2 누락이 Uploader 안에서 숨는다.
	//
	// 최종 이름이 아니라 .part 로 올리는 이유는 수신측에 중간 상태를
	// 노출하지 않기 위함이다 (설계안 8.1, 14). 최종 이름으로 직접 쓰면
	// 업로드 중인 불완전 파일이 정식 이름으로 존재하는 구간이 생기고,
	// 전송이 끊기면 그 잘린 파일이 정식 이름으로 그대로 남는다.
	//
	// 직전 실행이 남긴 불완전한 .part 는 보존하지 않는다.
	// 이어받기(resume)를 하지 않으므로 남은 바이트에 의미가 없다.
	//
	// 구현체는 복사 도중 ctx 취소를 확인해야 한다. io.Copy 는 ctx 를
	// 모르므로 청크 단위로 직접 검사하지 않으면 취소가 동작하지 않는다.
	// Daily 파일은 수십 MB 이고 회선이 느릴 수 있어 실제 지연이 된다.
	//
	// 오류 또는 취소로 중단된 경우 partPath 의 상태는 보장하지 않는다.
	// 잔여 .part 정리는 호출자(transfer)의 책임이며 여기서 하지 않는다.
	// 양쪽이 지우면 정리 시점이 두 곳으로 갈라져 추적할 수 없다.
	UploadPart(ctx context.Context, localPath, partPath string) error

	// Size 는 path 의 정규 파일 크기를 반환한다.
	// 대상이 디렉터리이면 오류다. Transfer Verification 은 파일 크기를 쓴다.
	//
	// 대상이 존재하지 않으면 errors.Is(err, fs.ErrNotExist) 가 true 인
	// 오류를 반환해야 한다. 구현체는 자신의 오류를 이 계약에 맞게
	// wrap 한다. 원문을 버리고 fs.ErrNotExist 만 반환하지 않는다.
	//
	// "없음" 과 "읽을 수 없음" 은 다른 사실이다. 전자는 전송이 실제로
	// 이루어지지 않았다는 뜻이고, 후자는 권한 또는 경로 설정 문제다.
	// 구현체마다 오류 형태가 다르면 이 판별이 LocalFS 에서만 되고
	// SFTP 에서는 안 되는 상태가 된다.
	//
	// 반환값은 크기 하나다. Transfer Verification 에는 크기와 존재
	// 여부만 있으면 된다 (설계안 8.1). mtime 이나 mode 가 필요해지면
	// 그때 별도 메서드를 추가한다.
	Size(ctx context.Context, path string) (int64, error)

	// Rename 은 oldPath 를 newPath 로 이름 변경한다.
	// PUT 에서는 size 검증을 마친 .part 를 최종 이름으로 전환할 때 쓴다.
	//
	// ★ newPath 가 이미 파일이면 덮어쓴다. 구현체가 OS·프로토콜과
	// 무관하게 이 결과를 보장해야 한다.
	// 성공 후 oldPath 는 없고 newPath 는 oldPath 의 내용이다.
	//
	// revision 이 오른 파일을 재전송할 때 최종 이름의 파일은 이미
	// 원격에 있다. 이것은 예외가 아니라 정상 경로다.
	// 여기서 실패하면 최초 전송만 성공하고 갱신분은 계속 실패하여
	// attempts 가 MaxRetries 에 닿고 자동 후보에서 빠진다.
	// 조용히 갱신분만 누락되는 형태가 되므로 계약으로 못박는다.
	//
	// 구현 주의
	//   - LocalFS 는 os.Rename 한 줄이다. Unix 는 대상 파일을 덮어쓴다.
	//     Windows 도 Go 1.5 이후 MoveFileEx(MOVEFILE_REPLACE_EXISTING)
	//     이라 대상 파일을 덮어쓴다. Win32 MoveFile(플래그 없음) 과
	//     혼동하지 않는다.
	//   - dest 삭제 후 재 rename 하는 fallback 은 쓰지 않는다.
	//     첫 실패가 EXDEV·권한이면 dest 만 지우고 source 는 못 옮겨
	//     최종 이름만 사라진다.
	//   - 모든 파일시스템 / SFTP 서버에 대해 "원자적 교체" 까지는
	//     계약하지 않는다. Windows 에서 os.Rename 은 원자적이 아닐 수
	//     있다. 요구하는 것은 대체가 성공한다는 것이다.
	//   - SFTP 표준 Rename(SSH_FXP_RENAME) 은 대상이 존재하면 실패한다.
	//     github.com/pkg/sftp 기준으로 Client.Rename 이 그 동작이며,
	//     덮어쓰기는 Client.PosixRename(posix-rename@openssh.com 확장)이다.
	//     sftpfs 는 PosixRename 을 쓴다. 서버가 확장을 지원하지 않으면
	//     DialSFTP 가 명확한 오류로 실패한다. 표준 Rename 대체와
	//     Remove→Rename fallback 은 두지 않는다 (2026-08-31 확정).
	Rename(ctx context.Context, oldPath, newPath string) error

	// Remove 는 path 의 파일을 삭제한다. 재귀가 아니며 디렉터리는 오류다.
	// 대상이 이미 존재하지 않아도 성공으로 취급한다.
	//
	// 판정은 Size 와 같은 규칙을 쓴다. errors.Is(err, fs.ErrNotExist)
	// 이면 nil 을 반환하고 그 외 오류는 그대로 올린다.
	// "없음" 판별용 오류도 wrap 을 유지한 뒤 버린다.
	// 두 계약이 한 규칙 위에 서야 구현체가 갈리지 않는다.
	//
	// 멱등이어야 하는 곳이 둘이다.
	//   - 전송 실패 직후의 잔여 .part 정리 (transfer)
	//   - 시작 시 IN_PROGRESS 회수의 .part 정리 (put_ledger.part_path)
	// 두 경로 모두 대상이 이미 없을 수 있고 그것은 오류가 아니다.
	Remove(ctx context.Context, path string) error

	// Join 은 구현체의 경로 구분자 규칙으로 경로를 결합한다.
	//
	// LocalFS 는 filepath.Join, SFTP 는 '/' 기반 path.Join 을 쓴다.
	// 경로 구분자 지식을 put 에 두지 않기 위한 메서드다.
	//
	// 유일하게 ctx 와 error 가 없는 순수 함수다. 성격이 다르므로
	// PathJoiner 로 분리할 수 있으나, 현재 구현체가 둘뿐이라 추상화만
	// 늘어난다. transport 가 셋 이상으로 늘거나 경로 정책이 복잡해지면
	// 그때 분리한다.
	Join(dir, name string) string
}
