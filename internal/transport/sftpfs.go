package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	// dialTimeout 은 SSH 접속의 TCP 연결 + SSH handshake 전체 상한이다.
	//
	// 같은 국내망의 SFTPGo 는 정상 접속이 1초 미만이므로,
	// 전체 접속 과정이 10초를 넘기면 "느린 것"이 아니라
	// 접속 불가 상태로 본다.
	//
	// TCP 연결과 SSH handshake 는 각각 10초를 갖는 것이 아니라
	// 하나의 10초 예산을 공유한다 (dialSSH).
	//
	// config 키로 두지 않는다 (2026-08-31 확정). 운영 중 조정할
	// 근거가 관측되면 그때 [PUT.SFTP] 로 승격한다 — 승격 시
	// 구조체·knownKeys·validate·example·문서 다섯 곳이 함께 움직인다.
	dialTimeout = 10 * time.Second

	// posixRenameExt 는 덮어쓰기 rename 을 제공하는 SFTP 확장이다.
	//
	// 표준 Rename(SSH_FXP_RENAME) 은 대상이 존재하면 실패하는 서버가
	// 있어 Uploader.Rename 계약("newPath 가 이미 파일이면 덮어쓴다")을
	// 만족하지 못한다. revision 재전송에서 최종 이름이 이미 원격에
	// 있는 것은 정상 경로이므로, SFTPFS 는 이 확장만 사용한다.
	posixRenameExt = "posix-rename@openssh.com"
)

// SFTPDialOptions 는 DialSFTP 의 접속 인자다.
//
// config 패키지를 import 하지 않기 위한 자리다. transport 는 파일만
// 옮기는 계층이며, [PUT.SFTP] 라는 설정 구조를 알지 않는다.
// config.SFTPConfig → 이 구조체 매핑은 main 이 한다.
type SFTPDialOptions struct {
	Host string
	Port int
	User string

	// PrivateKeyPath 는 SSH 개인키 파일 경로다. 인증은 publickey 만
	// 지원한다 (config validate 가 그 외를 시작 시 거부한다).
	PrivateKeyPath string

	// KnownHostsPath 는 host key 검증 파일 경로다.
	// host key 검증을 비활성화하는 실행 경로는 두지 않는다
	// (ssh.InsecureIgnoreHostKey 금지).
	KnownHostsPath string
}

// SFTPFS 는 SFTP 서버로의 전송이다. put.Uploader 를 구조적으로 만족한다.
//
// LocalFS 와 달리 접속 상태(연결 핸들)를 가지므로 값이 아니라 포인터로
// 쓰며, 사용 후 Close 가 필요하다. 파일 작업 관점에서는 여전히
// 무상태다 — EnsureDir 캐시 등은 계약대로 호출자(put.Transfer)가 갖는다.
//
// Close 를 Uploader 계약에 넣지 않는다. put 은 접속 수명을 모르는 편이
// 맞고, LocalFS 에는 닫을 것이 없다. 수명 관리는 만든 쪽(main)이 한다.
//
// 동시성: *sftp.Client 는 요청을 다중화하므로 여러 goroutine 이 동시에
// 호출해도 된다 (Uploader 계약의 MaxWorkers 동시 호출 요구).
//
// ctx 에 대하여: pkg/sftp 의 개별 작업은 context 를 받지 않는다.
// 따라서 각 메서드는 작업 시작 전에 ctx 취소를 확인하고,
// UploadPart 는 추가로 청크 사이에서 ctx 를 확인한다.
//
// 이미 진행 중인 Stat, Rename, MkdirAll, Write 같은 SFTP 왕복 자체를
// ctx 취소로 즉시 중단시키지는 못한다. 해당 호출이 반환된 뒤 다음
// 취소 지점에서 중단된다.
type SFTPFS struct {
	client *sftp.Client
	conn   *ssh.Client

	// prog 는 원격 작업의 진전 계측이다 (UNIT3 — 무진행 감시).
	// 모든 원격 메서드가 enter/exit 로 감싸고, UploadPart 는 청크
	// 성공마다 beat 한다. 감시·판정은 이 타입이 아니라 WatchStall
	// (progress.go)과 main 의 배선이 한다 — transport 는 계측만 안다.
	prog progress

	// abortOnce 는 Abort 의 멱등성이다. watchdog 발화와 감시
	// goroutine 의 defer Abort 가 겹쳐도 ssh 를 두 번 닫지 않는다.
	abortOnce sync.Once
	aborted   atomic.Bool
}

// DialSFTP 는 SFTP 서버에 접속해 SFTPFS 를 만든다.
//
// 접속 시점에 posix-rename 확장 지원을 확인하고, 미지원으로 판정되면
// 즉시 실패한다 (2026-08-31 확정: 명확한 오류로 실패, fallback 없음).
// 확인은 광고 → 기능 탐침의 2단계다 (본문 주석 참조).
//
// 판정을 Rename 호출 시점이 아니라 접속 시점에 두는 이유는 attempts
// 예산 보호다 — 진짜 미지원 서버에서 Rename 시점 판정은 매시
// MaxFilesPerRun 개 파일이 업로드까지 마친 뒤 실패하며 attempts 를
// 소모한다. attempts 는 파일의 문제에 쓰는 예산이지 환경의 문제에
// 쓰는 예산이 아니다 (preflight attempts 결정과 동일 원칙).
//
// 표준 Rename 대체와 Remove→Rename fallback 은 두지 않는다.
// 전자는 revision 재전송("대상 존재")만 조용히 누락시키고, 후자는
// Remove 성공 후 Rename 실패 시 원격의 멀쩡한 옛 파일까지 잃는다
// (Uploader.Rename 계약에서 기각한 것과 같은 근거).
func DialSFTP(opts SFTPDialOptions) (*SFTPFS, error) {
	keyBytes, err := os.ReadFile(opts.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("sftp: read private key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(keyBytes)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, fmt.Errorf(
				"sftp: private key %q 는 passphrase 로 보호되어 있다. "+
					"배치 실행용으로는 passphrase 없는 키가 필요하다: %w",
				opts.PrivateKeyPath, err,
			)
		}

		return nil, fmt.Errorf("sftp: parse private key: %w", err)
	}

	// 파일이 없거나 형식이 깨진 경우가 첫 배포에서 가장 흔하다.
	// knownhosts 의 원문 오류만으로는 무엇을 해야 하는지 알 수 없어
	// 조치를 함께 적는다.
	//
	// 키 불일치(서버 키가 바뀐 경우)는 여기가 아니라 dialSSH 의
	// handshake 오류로 나온다.
	hostKeyCallback, err := knownhosts.New(opts.KnownHostsPath)
	if err != nil {
		return nil, fmt.Errorf(
			"sftp: known_hosts %q: %w "+
				"(예: ssh-keyscan -p %d %s >> %s)",
			opts.KnownHostsPath,
			err,
			opts.Port,
			opts.Host,
			opts.KnownHostsPath,
		)
	}

	sshCfg := &ssh.ClientConfig{
		User:            opts.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: hostKeyCallback,
	}

	addr := net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port))

	conn, tcpConn, err := dialSSH(addr, sshCfg)
	if err != nil {
		return nil, err
	}

	client, err := sftp.NewClient(conn)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sftp: open subsystem: %w", err)
	}

	// posix-rename 지원 확인은 2단계다: 광고 → 기능 탐침.
	//
	// 1) 광고: 서버가 SSH_FXP_VERSION 에서 확장을 광고하면 신뢰한다.
	// 2) 탐침: 광고는 의무가 아니다. SFTPGo 2.7.5 가 posix-rename 을
	//    처리할 수 있으면서도 광고하지 않는 것이 2026-08-31 로컬
	//    검증에서 확인됐다 (HasExtension 만 믿으면 거짓 음성 —
	//    멀쩡한 서버로의 전송이 통째로 막힌다). 광고가 없으면
	//    존재할 수 없는 경로로 PosixRename 을 한 번 보내 서버의
	//    행동으로 판별한다.
	//      - "no such file" 계열 → 확장은 동작한다 (대상이 없었을 뿐)
	//      - 그 외 응답          → 미지원으로 보고 시끄럽게 실패
	//
	// 광고는 서버의 말이고 탐침은 행동이다. 계약이 요구하는 것은
	// 행동이므로 행동을 검사한다. 탐침 경로는 나노초 타임스탬프라
	// 실존할 수 없고, rename 시도일 뿐이라 서버에 아무것도 만들지
	// 않으며, 비용은 왕복 1회다.
	//
	// 최종 심판은 계약 테스트의 RenameOverwritesExisting 이다 —
	// 탐침이 잘못 통과시킨 서버는 거기서 실파일 덮어쓰기로 드러난다.
	if _, advertised := client.HasExtension(posixRenameExt); !advertised {
		probe := ".sftpclient-posix-rename-probe-" +
			strconv.FormatInt(time.Now().UnixNano(), 10)

		probeErr := client.PosixRename(probe, probe+"-dst")
		if probeErr != nil && !isNotExist(probeErr) {
			_ = client.Close()
			_ = conn.Close()

			return nil, fmt.Errorf(
				"sftp: 서버 %s 가 %s 확장을 지원하지 않는 것으로 "+
					"판정됐다 (탐침 응답: %v). Uploader.Rename 계약(대상 "+
					"존재 시 덮어쓰기)을 만족할 수 없으므로 전송을 시작하지 "+
					"않는다. 서버(SFTPGo) 설정/버전을 확인하라",
				addr, posixRenameExt, probeErr,
			)
		}
	}

	// ★ deadline 해제는 필수이며, 위치는 handshake 직후가 아니라
	// SFTP 초기화(NewClient)·posix-rename 탐침까지 마친 뒤다
	// (UNIT3 v3 §2-5). 남겨두면 접속 시작 10초 뒤부터 정상적인
	// 파일 읽기·쓰기도 i/o timeout 으로 실패한다.
	if err := tcpConn.SetDeadline(time.Time{}); err != nil {
		_ = client.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("sftp: clear handshake deadline: %w", err)
	}

	return &SFTPFS{client: client, conn: conn}, nil
}

// dialSSH 는 TCP 연결과 SSH handshake 가 하나의 dialTimeout 예산을
// 공유하도록 접속한다.
//
// ssh.Dial 을 사용하지 않고 TCP 연결을 직접 만든 뒤
// ssh.NewClientConn 을 호출한다. 이렇게 해야 TCP 연결을 포함한
// 전체 handshake 과정에 하나의 deadline 을 적용할 수 있다.
//
// 배치 프로그램에서 handshake 가 무기한 매달리는 것은 단순히
// 느린 문제가 아니다. 프로세스가 lock 을 쥔 채 살아 있으면 이후
// 회차가 ErrHeld 로 종료될 수 있으므로 접속 단계에 명확한 상한을 둔다.
// 반환된 net.Conn 에는 dialTimeout deadline 이 걸린 채다. 해제는
// 호출자(DialSFTP)가 SFTP 초기화·탐침까지 마친 뒤 수행한다 —
// NewClient 와 posix-rename 탐침도 원격 왕복이므로 접속 예산 안에
// 있어야 한다 (UNIT3 v3 §2-5: 이 구간은 종래 dialTimeout 밖이었다).
func dialSSH(addr string, cfg *ssh.ClientConfig) (*ssh.Client, net.Conn, error) {
	// TCP 연결과 SSH handshake 가 공유할 절대 deadline 을
	// TCP 연결을 시작하기 전에 계산한다.
	deadline := time.Now().Add(dialTimeout)

	tcpConn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("sftp: dial %s: %w", addr, err)
	}

	// TCP 연결에 이미 사용한 시간을 포함하여 남은 시간만
	// SSH handshake 에 사용할 수 있다.
	if err := tcpConn.SetDeadline(deadline); err != nil {
		_ = tcpConn.Close()
		return nil, nil, fmt.Errorf("sftp: set handshake deadline: %w", err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(tcpConn, addr, cfg)
	if err != nil {
		_ = tcpConn.Close()
		return nil, nil, fmt.Errorf("sftp: handshake %s: %w", addr, err)
	}

	return ssh.NewClient(sshConn, chans, reqs), tcpConn, nil
}

// Close 는 SFTP 세션과 SSH 연결을 닫는다. sftp → ssh 순서다.
//
// 서버가 먼저 끊은 뒤 닫으면 io.EOF 가 나오는데, 그것은 정상 종료의
// 다른 모습일 뿐 운영자가 볼 사건이 아니다. 매 실행마다 WARN 이
// 찍히면 진짜 신호가 묻힌다.
//
// 필터는 Join 결과가 아니라 구성원 각각에 건다. errors.Is 는 Join 된
// 오류의 구성원 중 하나만 맞아도 true 이므로, 합친 뒤에 거르면
// client 쪽 EOF 가 conn 쪽의 실제 오류까지 함께 삼킨다.
func (s *SFTPFS) Close() error {
	// Abort 가 이미 ssh 를 닫았으면 sftp.Client.Close 의 드레인을
	// 기다리지 않는다. 스톨 상태에서 그 대기는 끝나지 않을 수 있고,
	// 그러면 main defer 가 lock 을 쥔 채 멈춘다.
	if s.aborted.Load() {
		return nil
	}

	return errors.Join(
		ignoreBenignClose(s.client.Close()),
		ignoreBenignClose(s.conn.Close()),
	)
}

// Abort 는 스톨 탈출 전용 강제 종료다 (UNIT3 v3 §3.2).
//
// 평소 Close(sftp → ssh 순)와 분리하는 이유: sftp.Client.Close 가
// 진행 중 요청의 드레인을 기다리면 스톨 상태에서는 그 대기 자체가
// 끝나지 않을 수 있다 (v3 §2-3). Abort 는 ssh 연결을 즉시 닫아
// 그 위의 모든 대기 중 호출(Write/Stat/Rename/Remove)을 에러로
// 깨운다. 깨어난 워커는 기존 실패 경로(failOne → failPending)를 탄다.
//
// 멱등이다. Abort 이후 main defer 의 Close 는 aborted 를 보고
// sftp 드레인을 건너뛴다.
func (s *SFTPFS) Abort() {
	s.abortOnce.Do(func() {
		s.aborted.Store(true)
		_ = s.conn.Close()
	})
}

// Progress 는 무진행 감시(WatchStall)가 읽는 계측이다.
// ProgressSource 를 만족한다.
func (s *SFTPFS) Progress() (inFlight int64, idle time.Duration) {
	return s.prog.snapshot()
}

// ignoreBenignClose 는 정상 종료의 다른 모습(io.EOF, 이미 닫힌 연결)을
// nil 로 바꾼다. 그 외의 Close 오류는 버리지 않는다 — LocalFS 가
// dst.Close 오류를 보존하는 것과 같은 원칙이다.
func ignoreBenignClose(err error) error {
	if err == nil ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, net.ErrClosed) {
		return nil
	}

	return err
}

// EnsureDir 은 dir 및 필요한 상위 디렉터리를 만든다.
func (s *SFTPFS) EnsureDir(ctx context.Context, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.prog.enter()
	defer s.prog.exit()

	if err := s.client.MkdirAll(dir); err != nil {
		// MkdirAll 은 Stat → Mkdir 순서라 그 사이에 창이 있다.
		// 워커 여럿이 같은 시각 디렉터리로 동시에 들어오면 한쪽이
		// "이미 존재" 로 실패한다. 계약("이미 디렉터리로 존재하면
		// 성공")상 이것은 성공이므로 재확인해서 흡수한다.
		//
		// 흡수하지 않으면 EnsureDir 실패가 transferErr 가 되는데,
		// 그 시점은 BeginPut 이 이미 성공한 뒤라 attempts 를 소모한다.
		// 즉 동시성 때문에 retry budget 이 깎인다.
		//
		// 같은 이름의 일반 파일이 있는 경우는 여전히 오류다.
		// IsDir 확인이 그 구분을 유지한다.
		fi, statErr := s.client.Stat(dir)
		if statErr == nil && fi.IsDir() {
			return nil
		}

		return fmt.Errorf("ensure dir: %w", err)
	}

	return nil
}

// UploadPart 는 localPath(호스트 OS 경로)의 내용을
// partPath(원격 경로)로 복사한다.
//
// io.Copy / File.ReadFrom 을 쓰지 않는 이유는 ctx 취소 때문이다.
// 한 덩어리로 복사하면 취소가 파일 하나 끝날 때까지 동작하지 않는다.
// 대신 청크 경계마다 왕복이 한 번 직렬화되므로 처리량은 손해다.
// 실측에서 느리면 취소를 포기하지 말고 uploadChunk 를 키우는
// 방향으로 조정한다.
func (s *SFTPFS) UploadPart(
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
		_ = src.Close()
	}()

	// 원격 구간(.part 열기 ~ 닫기)을 진행 중 작업으로 계측한다.
	//
	// exit 의 defer 를 dst.Close 의 defer 보다 먼저 등록해 마지막
	// (원격) Close 까지 창 안에 둔다. 청크 사이의 로컬 src.Read 도
	// 이 창 안에 있으므로, 로컬 읽기가 NAS 에서 블록되면 발화는
	// 하되 ssh close 로 풀리지는 않는다 — 알려진 잔여이며 이 유닛의
	// 범위 밖이다 (UNIT3 v3 §3.4 분리, §6-5).
	s.prog.enter()
	defer s.prog.exit()

	dst, err := s.client.OpenFile(
		partPath,
		os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
	)
	if err != nil {
		return wrapNotExist("create .part", err)
	}
	defer func() {
		if cerr := dst.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close .part: %w", cerr)
		}
	}()

	buf := make([]byte, uploadChunk)

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("upload canceled: %w", ctxErr)
		}

		n, readErr := src.Read(buf)

		if n > 0 {
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

			// 청크가 실제로 쓰였다 = 진전. 큰 파일이 오래 걸려도
			// 청크가 이어지는 한 스톨이 아니다 (파일당 총시간
			// 제한을 기각한 결정의 구현체, UNIT3 v3 §3.3).
			s.prog.beat()
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
func (s *SFTPFS) Size(ctx context.Context, p string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	s.prog.enter()
	defer s.prog.exit()

	fi, err := s.client.Stat(p)
	if err != nil {
		return 0, wrapNotExist("stat", err)
	}

	if !fi.Mode().IsRegular() {
		return 0, fmt.Errorf("stat: %q is not a regular file", p)
	}

	return fi.Size(), nil
}

// Rename 은 oldPath 를 newPath 로 전환한다. 대상이 존재하면 덮어쓴다.
//
// PosixRename 만 쓴다. 확장 지원 여부는 DialSFTP 가 접속 시점에
// (광고 → 탐침으로) 이미 판정했으므로 여기서 다시 판별하거나
// 대체 경로로 빠지지 않는다.
func (s *SFTPFS) Rename(
	ctx context.Context,
	oldPath string,
	newPath string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.prog.enter()
	defer s.prog.exit()

	if err := s.client.PosixRename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// Remove 는 path 의 파일을 삭제한다. 멱등이며 디렉터리는 오류다.
func (s *SFTPFS) Remove(ctx context.Context, p string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	s.prog.enter()
	defer s.prog.exit()

	fi, err := s.client.Lstat(p)
	if err != nil {
		if isNotExist(err) {
			return nil
		}

		// 여기까지 왔으면 "없음" 은 이미 걸러졌으므로
		// wrapNotExist 를 쓰지 않는다.
		return fmt.Errorf("remove: stat: %w", err)
	}

	if fi.IsDir() {
		return fmt.Errorf("remove: %q is a directory", p)
	}

	// Lstat 과 Remove 사이에 대상이 사라질 수 있으므로
	// 여기서도 "이미 없음" 을 성공으로 받는다.
	if err := s.client.Remove(p); err != nil {
		if isNotExist(err) {
			return nil
		}

		return fmt.Errorf("remove: %w", err)
	}

	return nil
}

// Join 은 SFTP 경로 규칙('/')으로 dir 과 name 을 결합한다.
func (*SFTPFS) Join(dir, name string) string {
	return path.Join(dir, name)
}

// isNotExist 는 err 가 "대상 없음" 인지를 판별한다.
//
// pkg/sftp 가 이미 fs.ErrNotExist / os.ErrNotExist 로 정규화해 주는
// 경로와 그렇지 않고 StatusError 를 그대로 올리는 경로가 섞여 있으므로
// 둘 다 본다.
func isNotExist(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}

	var se *sftp.StatusError
	if errors.As(err, &se) {
		return se.FxCode() == sftp.ErrSSHFxNoSuchFile
	}

	return false
}

// wrapNotExist 는 err 를 감싸되, "대상 없음" 이면
// errors.Is(err, fs.ErrNotExist) 가 성립하도록 fs.ErrNotExist 를
// 체인에 추가한다. 원문 오류는 버리지 않는다.
//
// Uploader 계약이 요구하는 정규화다. "없음"(전송이 실제로 안 됨)과
// "읽을 수 없음"(권한·경로 설정)은 다른 사실이며, 이 판별이 LocalFS
// 에서만 되고 SFTP 에서는 안 되는 상태를 만들지 않는다.
func wrapNotExist(op string, err error) error {
	if isNotExist(err) {
		return fmt.Errorf("%s: %w: %w", op, fs.ErrNotExist, err)
	}

	return fmt.Errorf("%s: %w", op, err)
}
