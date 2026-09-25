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

// SFTPFS 는 SFTP 서버로의 전송이다. put.Uploader 를 구조적으로 만족하고,
// DOWNLOAD 용 원시 호출(ReadDir·DownloadPart)도 제공한다. download 패키지는
// 자기 쪽에 필요한 최소 인터페이스(RemoteFS)를 선언해 이것을 쓴다 (커밋 3).
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
// ctx 에 대하여: ReadDir 은 pkg/sftp 의 ReadDirContext 로 취소를 전달한다.
// 나머지 개별 작업은 context 를 받지 않으므로 작업 시작 전에 확인하고,
// UploadPart·DownloadPart 는 추가로 청크 사이에서 ctx 를 확인한다.
//
// 이미 진행 중인 Stat, Rename, MkdirAll, Write 같은 SFTP 왕복 자체를
// ctx 취소로 즉시 중단시키지는 못한다. 해당 호출이 반환된 뒤 다음
// 취소 지점에서 중단된다.
type SFTPFS struct {
	client *sftp.Client

	// conn 은 SFTP 세션 아래의 전송 연결이다. 운영에서는 *ssh.Client,
	// in-process 테스트에서는 pipe 를 닫는 closer 다 (DOWNLOAD 커밋 계획 §3.4).
	//
	// 구체 타입이 아니라 io.Closer 로 두는 이유는 Abort 계약 때문이다.
	// Abort 의 뜻은 "이 연결을 닫아 그 위에 블록된 모든 원격 호출을 깨운다"
	// 하나이고, 필요한 능력도 Close 하나다. 테스트가 conn == nil 로 두고
	// Abort 를 무동작으로 만들면 stall 경로의 해제 계약을 시험하지 못한다.
	// 그래서 테스트도 실제로 블록을 깨우는 closer 를 같은 자리에 넣는다.
	conn io.Closer

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

// DialSFTP 는 PUT 용으로 SFTP 서버에 접속해 SFTPFS 를 만든다.
//
// 접속 시점에 posix-rename 확장 지원을 확인하고, 미지원으로 판정되면
// 즉시 실패한다 (2026-08-31 확정: 명확한 오류로 실패, fallback 없음).
// 확인은 광고 → 기능 탐침의 2단계다 (requirePosixRename 주석 참조).
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
	return dialSFTP(opts, requirePosixRename)
}

// DialSFTPReadOnly 는 DOWNLOAD 용으로 SFTP 서버에 접속한다
// (DOWNLOAD 커밋 계획 §3.1).
//
// DialSFTP 와 다른 점은 posix-rename 검사를 하지 않는다는 것 하나다.
// DOWNLOAD 는 원격을 나열(ReadDir)하고 읽기(DownloadPart)만 하며, 최종
// Rename 은 로컬 파일시스템에서 한다. 원격 Rename 을 쓰지 않는 방향이
// 원격 Rename 확장 때문에 접속을 거부당하면, 읽기 전용 계정이나 확장이
// 없는 서버에서 멀쩡한 수신이 통째로 막힌다.
//
// TCP·SSH handshake·known_hosts·키 인증·SFTP subsystem 초기화·deadline
// 해제는 DialSFTP 와 같은 dialSFTP 를 거친다. 두 경로가 갈라지는 지점은
// 방향별 접속 후 검사(afterConnect) 하나뿐이다.
//
// "읽기 전용"은 이 접속의 용도를 뜻한다. 반환된 SFTPFS 의 쓰기 메서드
// (UploadPart·Rename 등)를 막지는 않는다 — 원격 쓰기를 하지 않는 것은
// 호출자(download)의 계약이며, 서버 계정 권한이 최종 방어선이다.
func DialSFTPReadOnly(opts SFTPDialOptions) (*SFTPFS, error) {
	return dialSFTP(opts, nil)
}

// afterConnectFunc 는 SFTP 초기화 직후, 접속 deadline 이 아직 걸려 있는
// 동안 방향별로 수행할 검사다. 오류를 돌려주면 접속을 닫고 실패한다.
// 원격 왕복을 할 수 있으므로 dialTimeout 예산 안에서 실행된다.
type afterConnectFunc func(client *sftp.Client, addr string) error

// dialSFTP 는 DialSFTP·DialSFTPReadOnly 의 공통 접속 경로다.
// afterConnect 가 nil 이면 검사를 건너뛴다.
func dialSFTP(
	opts SFTPDialOptions,
	afterConnect afterConnectFunc,
) (*SFTPFS, error) {
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

	if afterConnect != nil {
		if err := afterConnect(client, addr); err != nil {
			_ = client.Close()
			_ = conn.Close()

			return nil, err
		}
	}

	// ★ deadline 해제는 필수이며, 위치는 handshake 직후가 아니라
	// SFTP 초기화(NewClient)·접속 후 검사(posix-rename 탐침)까지 마친 뒤다
	// (UNIT3 v3 §2-5). 남겨두면 접속 시작 10초 뒤부터 정상적인
	// 파일 읽기·쓰기도 i/o timeout 으로 실패한다. DOWNLOAD 도 같다.
	if err := tcpConn.SetDeadline(time.Time{}); err != nil {
		_ = client.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("sftp: clear handshake deadline: %w", err)
	}

	return &SFTPFS{client: client, conn: conn}, nil
}

// requirePosixRename 은 PUT 접속의 사후 검사다. 서버가 posix-rename
// 확장을 처리하지 못하면 오류를 돌려준다. (DialSFTP 주석 참조)
func requirePosixRename(client *sftp.Client, addr string) error {
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
			return fmt.Errorf(
				"sftp: 서버 %s 가 %s 확장을 지원하지 않는 것으로 "+
					"판정됐다 (탐침 응답: %v). Uploader.Rename 계약(대상 "+
					"존재 시 덮어쓰기)을 만족할 수 없으므로 전송을 시작하지 "+
					"않는다. 서버(SFTPGo) 설정/버전을 확인하라",
				addr, posixRenameExt, probeErr,
			)
		}
	}

	return nil
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

	var clientErr, connErr error
	if s.client != nil {
		clientErr = ignoreBenignClose(s.client.Close())
	}
	if s.conn != nil {
		connErr = ignoreBenignClose(s.conn.Close())
	}

	return errors.Join(clientErr, connErr)
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
		if s.conn != nil {
			_ = s.conn.Close()
		}
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

// ReadDir 은 원격 디렉터리 dir 의 항목을 돌려준다 (DOWNLOAD 커밋 계획 §3.2).
//
// DOWNLOAD 의 원격 나열 어댑터(download 패키지, 커밋 3)가 scan.DirLister 로
// 바꿔 쓰는 원시 호출이다. 여기서는 판정하지 않는다:
//
//   - 항목 순서를 보장하지 않는다. 서버가 준 순서 그대로다.
//   - 파일 종류 비트를 보존한다. 서버의 READDIR 응답은 lstat 기준이므로
//     심볼릭 링크는 fs.ModeSymlink 로 나오고 IsDir()==false 다. 링크를
//     따라가 디렉터리로 바꾸지 않는다 — 링크·비정규 대상의 처리는
//     scan.Scanner 의 Irregular 정책이 정한다.
//   - "." ".." 은 pkg/sftp 클라이언트가 이미 걸러 준다.
//
// 없는 경로는 errors.Is(err, fs.ErrNotExist) 가 성립한다. Scanner 는 이것을
// 오류가 아니라 빈 날짜 슬롯(Missing)으로 집계한다. 권한 오류 등 그 외는
// 그대로 오류다 — "없음"과 "읽을 수 없음"은 다른 사실이다.
//
// 원격 호출 전체를 진행 중 작업으로 계측한다. 목록 응답이 오지 않으면
// WatchStall 이 발화하고 Abort 가 이 호출을 깨운다.
func (s *SFTPFS) ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.prog.enter()
	defer s.prog.exit()

	entries, err := s.client.ReadDirContext(ctx, dir)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		// 일부 서버(특히 Windows 기반 구현)는 디렉터리 자리에 파일이
		// 있어도 NO_SUCH_FILE 을 돌려준다. 그대로 감싸면 Scanner 가 실제
		// 원격 구조 오류를 빈 날짜 슬롯(Missing)으로 조용히 넘긴다.
		// 반대로 디렉터리는 있는데 목록만 NO_SUCH_FILE 이면, 그것도
		// Missing 으로 넘기면 그 날짜 파일이 통째로 빠진다.
		// 정상 나열에는 왕복을 늘리지 않고, 실패했을 때만 Lstat 으로
		// 재확인해 두 경우를 갈라낸다.
		fi, statErr := s.client.Lstat(dir)
		return nil, classifyReadDirFailure(dir, err, fi, statErr)
	}

	return entries, nil
}

// classifyReadDirFailure 는 ReadDir 실패를 Scanner 가 보는 오류로 나눈다.
//
//	없는 경로                         → fs.ErrNotExist (Missing)
//	있는 파일·링크                    → 디렉터리가 아님 (Missing 아님)
//	있는 디렉터리인데 목록만 없음     → Missing 아님. 하루치가 조용히 빠진다
//	있음 확인 자체가 다른 오류        → 그 확인 오류
func classifyReadDirFailure(dir string, listErr error, fi fs.FileInfo, statErr error) error {
	switch {
	case statErr == nil && fi != nil && !isListableDir(fi):
		return fmt.Errorf("read dir: %q is not a directory", dir)

	case statErr == nil && fi != nil && isNotExist(listErr):
		// %w 로 잇지 않는다. listErr 가 fs.ErrNotExist 이면 Scanner 가
		// 이 디렉터리를 빈 날짜(Missing)로 넘겨 그 날짜 파일을 건너뛴다.
		return fmt.Errorf("read dir: %q exists but listing failed: %v", dir, listErr)

	case isNotExist(listErr) && statErr != nil && !isNotExist(statErr):
		return fmt.Errorf("read dir: verify path kind: %w", statErr)

	default:
		return wrapNotExist("read dir", listErr)
	}
}

// isListableDir 는 나열해도 되는 진짜 디렉터리인지 답한다.
// 링크는 대상이 디렉터리여도 따라가지 않는다.
func isListableDir(fi fs.FileInfo) bool {
	mode := fi.Mode()
	return mode.IsDir() && mode&fs.ModeSymlink == 0
}

// DownloadPart 는 원격 remotePath 의 내용을 로컬 localPartPath(호스트 OS
// 경로)로 복사한다 (DOWNLOAD 커밋 계획 §3.3, DOWNLOAD v3 §6).
//
// 처리 순서:
//
//  1. 원격 파일을 읽기 전용으로 연다.
//  2. 이전 회차의 정규 .part 를 제거하고 O_CREATE|O_EXCL 로 새로 연다.
//     이어받지 않으며, 링크를 따라 다른 파일을 자르지 않는다 (D9).
//  3. 청크 경계마다 ctx 취소를 확인한다.
//  4. 읽은 바이트 전체가 기록될 때까지 short write 를 처리한다.
//  5. 청크 기록 성공마다 진전(beat)을 기록한다.
//  6. 끝까지 받으면 로컬 .part 를 Sync 해 디스크에 내린다.
//  7. Read·Write·Sync·Close 오류를 버리지 않는다. 주 오류가 있으면 그
//     원인을 앞에 두고 Close 오류는 errors.Join 으로 덧붙인다.
//
// 원격을 먼저 여는 이유: 원격에 파일이 없으면(목록 이후 사라짐) 로컬에
// 빈 .part 를 남기지 않는다. 그 경우 오류는 fs.ErrNotExist 가 성립한다.
//
// 하지 않는 일 (DOWNLOAD v3 §6.2): 크기 판정, 최종 이름 Rename, 실패 후
// .part 삭제, 목적지 디렉터리 생성. 전부 호출자(download 러너) 몫이다.
// 따라서 실패로 반환되면 로컬 .part 가 부분 내용으로 남아 있을 수 있다.
//
// 오류 판별 주의: 로컬 쪽 오류도 os 가 준 원문을 보존하므로, 목적지
// 디렉터리가 없으면 그 오류도 fs.ErrNotExist 가 성립한다. 호출자는
// EnsureDir 을 먼저 하고, DownloadPart 의 fs.ErrNotExist 로 원격 부재를
// 판정해 분기하지 않는다 — DOWNLOAD 실패는 원인과 무관하게 다음 회차가
// 다시 시도한다(커밋 계획 §1.1-6). 원인은 오류 문구의 "open remote" /
// "create local .part" 머리로 구분된다.
//
// 계측 창: 원격 Open 부터 원격 Close 까지를 진행 중 작업으로 본다.
// 청크 사이의 로컬 Write 도 이 창 안이라 로컬 디스크가 블록되면 발화는
// 하되 ssh close 로 풀리지는 않는다. UploadPart 의 로컬 Read 와 같은
// 알려진 잔여다 (UNIT3 v3 §3.4, §6-5).
func (s *SFTPFS) DownloadPart(
	ctx context.Context,
	remotePath string,
	localPartPath string,
) (err error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	// exit 의 defer 를 src.Close 의 defer 보다 먼저 등록해 마지막 원격
	// Close 까지 창 안에 둔다 (defer 는 역순 실행).
	s.prog.enter()
	defer s.prog.exit()

	src, err := s.client.Open(remotePath)
	if err != nil {
		return wrapNotExist("open remote", err)
	}
	defer closeInto(&err, src, "close remote")

	dst, err := openLocalPart(localPartPath)
	if err != nil {
		return fmt.Errorf("create local .part: %w", err)
	}
	defer closeInto(&err, dst, "close local .part")

	if err := copyDownload(ctx, dst, src, s.prog.beat); err != nil {
		return err
	}

	// 디스크에 내린 뒤에야 성공이다. 호출자는 이 반환 직후 크기를
	// 비교하고 최종 이름으로 Rename 한다. Sync 없이 Rename 하면
	// 정전·강제 종료 뒤 "크기는 맞는데 내용이 0 으로 채워진" 최종
	// 파일이 남을 수 있고, 다음 회차는 크기가 같으니 기존으로 건너뛴다.
	if serr := dst.Sync(); serr != nil {
		return fmt.Errorf("sync local .part: %w", serr)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("download canceled: %w", ctxErr)
	}

	return nil
}

func copyDownload(ctx context.Context, dst io.Writer, src io.Reader, beat func()) error {
	buf := make([]byte, uploadChunk)

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("download canceled: %w", ctxErr)
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			if werr := writeFull(dst, buf[:n]); werr != nil {
				return fmt.Errorf("write local .part: %w", werr)
			}
			beat()

			// io.Reader 는 마지막 호출에서 (n > 0, io.EOF)를 함께 돌려줄
			// 수 있다. 쓰는 동안 취소된 회차를 EOF 성공으로 오인하지 않는다.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return fmt.Errorf("download canceled: %w", ctxErr)
			}
		}

		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read remote: %w", readErr)
		}
	}
}

// openLocalPart 는 매 회차의 .part 를 새 inode/파일로 만든다.
//
// 기존 정규 파일을 O_TRUNC 로 바로 열면 그것이 다른 파일의 hard link 인
// 경우 원본까지 잘린다. 심볼릭 링크라면 링크 대상이 잘린다. 먼저 이름만
// 제거한 뒤 O_EXCL 로 새 파일을 만들면 두 경우 모두 다른 대상을 건드리지
// 않는다. 제거와 생성 사이에 누군가 같은 이름을 만들더라도 O_EXCL 이
// 실패하므로 그 대상을 따라가지 않는다. 디렉터리·장치 등 비정규 대상은
// 제거하지 않고 명시적으로 거부한다.
func openLocalPart(localPartPath string) (*os.File, error) {
	fi, err := os.Lstat(localPartPath)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("existing path is not a regular file: %q", localPartPath)
		}
		if err := removeReplaceableFile(localPartPath); err != nil {
			return nil, err
		}
	case errors.Is(err, fs.ErrNotExist):
		// 처음 수신하는 파일이다.
	case err != nil:
		return nil, fmt.Errorf("inspect existing path: %w", err)
	}

	return os.OpenFile(
		localPartPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o644,
	)
}

// removeReplaceableFile 은 다음 수신이 덮어쓸 정규 파일을 지운다.
//
// 먼저 이름만 제거한다. Windows에서는 실제 읽기 전용 정규 파일일 때만
// 제한적으로 속성을 풀어 재시도한다. sharing violation 등 다른 실패에서는
// 속성을 바꾸지 않고 운영자가 확인하도록 오류를 그대로 남긴다.
func removeReplaceableFile(path string) error {
	err := removeRegularFile(path)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return nil
	}

	return fmt.Errorf("remove existing file: %w", err)
}

// writeFull 은 p 전체가 w 에 기록될 때까지 Write 를 반복한다.
//
// io.Writer 계약상 n < len(p) 이면 오류가 함께 와야 하지만, 그 계약을
// 어기는 구현(0, nil)도 무한 루프 대신 io.ErrShortWrite 로 끝낸다.
func writeFull(w io.Writer, p []byte) error {
	for written := 0; written < len(p); {
		m, err := w.Write(p[written:])
		if err != nil {
			return err
		}

		if m == 0 {
			return io.ErrShortWrite
		}

		written += m
	}

	return nil
}

// closeInto 는 defer 에서 c 를 닫고 그 오류를 *errp 에 반영한다.
//
//	*errp == nil, Close 오류 → Close 오류가 결과가 된다
//	*errp != nil, Close 오류 → 주 오류를 앞에 두고 errors.Join 으로 보존
//	Close 성공             → *errp 불변
//
// 주 오류를 덮어쓰지 않는다. 운영자가 먼저 봐야 하는 것은 전송이 왜
// 실패했는가이고, 그 뒤의 Close 실패는 부수 사실이다. 반대로 주 오류가
// 없을 때의 Close 실패는 버리지 않는다 — 로컬 Close 는 쓰기 반영 실패를
// 알리는 마지막 지점일 수 있다.
func closeInto(errp *error, c io.Closer, op string) {
	cerr := c.Close()
	if cerr == nil {
		return
	}

	cerr = fmt.Errorf("%s: %w", op, cerr)

	if *errp == nil {
		*errp = cerr
		return
	}

	*errp = errors.Join(*errp, cerr)
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
