// Package lock 은 프로세스 단위 중복 실행을 lock 디렉터리 하나로 막는다.
//
// 스케줄러는 주기적으로 프로그램을 실행한다. Deep Scan 이나 대량 전송
// 회차가 다음 회차와 겹치면 두 프로세스가 같은 장부를 보고 같은 후보를
// 계산하여 같은 파일을 이중 전송할 수 있다.
//
// SQLite 의 WAL 과 busy_timeout 은 DB 쓰기 충돌을 조정할 뿐,
// Lookup → 후보 선정 → 실제 SFTP 전송 사이에서 두 프로세스가 같은
// 후보를 선택하는 것을 막아주지 않는다.
//
// 이 패키지는 두 번째 프로세스가 작업 자체를 시작하지 못하게 하는
// 프로세스 수준의 단일 실행 보호를 제공한다.
//
// lock 경로는 호출자가 정한다.
// 중요한 것은 같은 Ledger 를 사용하는 실행들이 반드시 같은 lock 경로를
// 사용해야 한다는 것이다.
//
// lock 은 일반 파일 하나가 아니라 디렉터리로 표현한다.
//
//	<lock>/
//	  owner-<token>
//
// 디렉터리 생성은 os.Mkdir 의 원자적 생성으로 경쟁을 중재하고,
// owner 파일 이름에 획득 인스턴스별 token 을 포함한다.
//
// stale 탈취와 Release 에서 RemoveAll 은 절대 사용하지 않는다.
// 각 프로세스는 자신이 관측했거나 소유한 owner 파일만 삭제한 뒤,
// 비어 있는 lock 디렉터리만 os.Remove 로 제거한다.
//
// 이 구조는 다음 경쟁을 막는다.
//
//	A가 기존 stale owner 를 삭제
//	A가 새 lock 획득
//	B가 늦게 기존 path 를 삭제
//
// 일반 lock 파일 하나를 사용하는 구조에서는 B가 A의 새 lock 을
// 삭제할 수 있지만, 여기서는 A의 새 owner 파일 때문에 디렉터리가
// 비어 있지 않아 B의 os.Remove(lockDir)가 실패한다.
//
// 이 패키지는 config, domain, ledger, logging 을 import 하지 않는다.
// 경로와 stale 문턱은 인자로 받고, 탈취 사실은 반환값으로 알린다.
package lock

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const ownerPrefix = "owner-"

// noOwnerGrace 는 owner marker 가 하나도 없는 lock 디렉터리를
// stale 로 판정하기까지의 유예다.
//
// 이 상태에는 원인이 둘 있고 성격이 완전히 다르다.
//
//	Mkdir 성공 직후 owner 기록 전       마이크로초 단위의 정상적인 창
//	Release 에서 디렉터리 삭제 실패      아무도 잡지 않은 채 영구히 남음
//
// 두 번째를 staleAfter(수 시간)로 다루면 아무도 보유하지 않은 lock 때문에
// 그 시간만큼 매 회차 실행이 거부되고 전송이 멈춘다. 로그에는
// pid=0 started=unknown 만 남아 원인 추적도 어렵다.
// Windows 에서 백신이나 인덱서가 디렉터리 핸들을 잡으면 os.Remove 가
// 실패할 수 있으므로 가상의 상황이 아니다.
//
// 그렇다고 owner 없음을 즉시 stale 로 보면 첫 번째 창에서 남의 lock 을
// 파괴한다. 그 창이 마이크로초 단위라는 사실을 이용해, 별도의 짧은
// 유예를 둔다. 30초면 그 창보다 다섯 자리 이상 크고, 잘못된 차단은
// 한 회차 안에 스스로 풀린다.
//
// staleAfter 와 달리 config 로 노출하지 않는다.
// 이 값은 현장 회선 속도가 아니라 "디렉터리 생성과 파일 쓰기 사이의 간격"
// 이라는 기계적 사실에 대한 것이므로 기관마다 달라질 이유가 없다.
const noOwnerGrace = 30 * time.Second

// ErrHeld 는 다른 인스턴스가 lock 을 보유 중인 것으로 간주되어
// 획득하지 못했음을 뜻한다.
//
// 반드시 실제 프로세스가 살아 있다는 뜻은 아니다.
// MVP1 에서는 PID 생존 확인을 하지 않고 lock 나이만으로 판정한다.
//
// 스케줄러 실행이 겹쳐 발생하는 정상적인 양보일 수 있으므로,
// 호출자는 errors.Is(err, ErrHeld) 로 구분하여 조용히 종료할 수 있다.
var ErrHeld = errors.New("lock: held by another process")

// ErrLost 는 Release 시점에 이 Lock 의 owner marker 가 더 이상 존재하지
// 않아 소유권을 잃었다고 판단했음을 뜻한다.
//
// 원인은 하나다. 이 프로세스가 오래 실행되는 동안 다른 인스턴스가
// stale 로 판정하여 lock 을 탈취했다.
//
// 이 신호가 실제 운영에서 발생한다면 LockStaleMinutes 가 정상적인
// 최장 실행 시간보다 짧은지 우선 확인해야 한다.
//
// 이 오류는 오직 owner marker 부재에서만 나온다.
// 디렉터리 정리 단계의 문제를 여기 섞으면 운영자가 엉뚱한 값을 조정한다.
var ErrLost = errors.New("lock: no longer owned by this process")

// ErrInvalidPath 는 lock 경로 입력이 비어 있거나 공백만일 때 반환된다.
//
// ledger.Open 이 빈 DB 경로를 거부하는 것과 같은 입구 검사이다.
// 경로가 비면 Mkdir 실패 원인이 모호해지므로 여기서 막는다.
var ErrInvalidPath = errors.New("lock: empty path")

// Info 는 lock 에 기록된 소유자 정보이다.
type Info struct {
	PID     int
	Started time.Time
	Token   string
}

// Lock 은 획득에 성공한 lock 이다.
// 작업 종료 시 Release 를 호출해야 한다.
type Lock struct {
	// TookOver 는 Acquire 과정에서 stale 로 판정된 기존 lock 을
	// 관측한 뒤 새 lock 을 획득했음을 뜻한다.
	//
	// 이전 실행의 크래시·강제 종료·정전 때문일 수도 있지만,
	// LockStaleMinutes 가 실제 정상 실행 시간보다 짧아서 살아 있는
	// 실행을 stale 로 오판한 경우일 수도 있다.
	//
	// 따라서 호출자는 장애 확정이 아니라 운영 경고 신호로 다룬다.
	TookOver bool

	// Prev 는 TookOver 가 true 일 때 관측했던 이전 lock 정보이다.
	// 손상된 owner 파일은 일부 필드가 제로값일 수 있다.
	//
	// owner marker 없이 디렉터리만 남아 있던 경우에는 전체가 제로값이다.
	// 그 상태 자체가 "정상 Release 가 끝까지 가지 못했다" 는 신호다.
	Prev Info

	path  string
	token string

	// ownerPath 는 이 Lock 만의 marker 경로다.
	// Release 는 공용 path 를 판단해서 지우지 않고 이 파일만 지운다.
	ownerPath string
}

// Path 는 이 Lock 이 사용하는 절대 경로를 돌려준다.
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}

	return l.path
}

// snapshot 은 기존 lock 을 한 시점에서 관측한 결과이다.
type snapshot struct {
	info      Info
	ownerPath string
	age       time.Duration

	// hasOwner 는 owner marker 를 실제로 찾았는지이다.
	//
	// ownerPath 의 빈 문자열 여부로도 알 수 있지만, stale 문턱이
	// 두 갈래(staleAfter / noOwnerGrace)로 나뉘는 판단의 근거이므로
	// 이름으로 드러낸다.
	hasOwner bool
}

// staleThreshold 는 이 관측 상태에 적용할 stale 문턱을 고른다.
//
// owner 가 있으면 실행 시간에 대한 판단이므로 호출자가 준 staleAfter 를,
// 없으면 디렉터리 생성과 owner 기록 사이의 창에 대한 판단이므로
// noOwnerGrace 를 쓴다. 두 문턱은 재는 대상이 서로 다르다.
func (s snapshot) staleThreshold(staleAfter time.Duration) time.Duration {
	if s.hasOwner {
		return staleAfter
	}

	return noOwnerGrace
}

// Acquire 는 path 에 lock 을 획득한다.
//
// staleAfter 는 기존 lock 을 stale 로 판단하는 나이 문턱이며
// 반드시 양수여야 한다.
//
// path 는 비어 있으면 안 된다. 상대 경로는 절대 경로로 정규화한다.
// 같은 Ledger 를 쓰는 실행은 같은 path 문자열(또는 같은 절대 경로)을
// 써야 한다. cwd 가 다르면 상대 경로의 절대값이 달라지므로,
// 가능하면 호출자가 절대 경로를 넘기는 편이 안전하다.
//
// 기본 흐름:
//
//  1. os.Mkdir(path) 로 원자적 획득 시도
//
//     성공
//     → owner-<token> 기록
//     → 획득 완료
//
//     이미 존재
//     → 기존 owner 와 나이 확인
//
//  2. age <= 문턱
//     → ErrHeld
//
//  3. age > 문턱
//     → 관측했던 owner 파일만 제거
//     → 비어 있는 lock 디렉터리만 제거
//     → os.Mkdir 로 재시도 한 번
//
// 문턱은 owner marker 유무에 따라 갈린다. staleThreshold 주석 참조.
//
// stale 탈취 경쟁에서도 마지막 획득은 os.Mkdir 가 원자적으로 중재한다.
//
// 두 경쟁자가 같은 stale owner 를 관측해도 owner 파일 삭제는 하나만
// 성공한다. 늦은 경쟁자는 다른 프로세스가 새 owner 를 만든 디렉터리를
// RemoveAll 하지 않으므로 새 lock 을 파괴할 수 없다.
//
// 현재는 lock 나이만으로 stale 을 판정한다.
// PID 생존 확인을 AND 조건으로 추가하면 정확도를 높일 수 있지만
// Windows/Linux 별도 구현과 PID 재사용 문제가 남으므로 MVP1 에서는
// 포함하지 않는다.
//
// 이 판정이 너무 짧으면 살아 있는 실행을 탈취해 이중 전송이 발생할 수
// 있으므로 staleAfter 는 실제 최장 실행 시간보다 넉넉하게 설정한다.
func Acquire(path string, staleAfter time.Duration) (*Lock, error) {
	if staleAfter <= 0 {
		return nil, fmt.Errorf(
			"lock: staleAfter must be positive, got %v",
			staleAfter,
		)
	}

	absPath, err := normalizePath(path)
	if err != nil {
		return nil, err
	}

	token, err := newToken()
	if err != nil {
		return nil, fmt.Errorf("lock: generate token: %w", err)
	}

	// 1차 획득 시도.
	l, created, err := tryCreate(absPath, token)
	if err != nil {
		return nil, err
	}

	if created {
		return l, nil
	}

	// 기존 lock 을 관측한다.
	prev, err := readSnapshot(absPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}

		// 첫 Mkdir 이후 기존 소유자가 정상 Release 한 경쟁이다.
		// 정확히 한 번 다시 획득을 시도한다.
		l, created, err = tryCreate(absPath, token)
		if err != nil {
			return nil, err
		}

		if !created {
			return nil, fmt.Errorf(
				"%w: acquire race",
				ErrHeld,
			)
		}

		return l, nil
	}

	threshold := prev.staleThreshold(staleAfter)

	if prev.age <= threshold {
		if !prev.hasOwner {
			// owner 기록 전의 짧은 창이다. 다음 회차에 자연히 풀린다.
			return nil, fmt.Errorf(
				"%w: lock directory has no owner marker yet (age=%s)",
				ErrHeld,
				prev.age.Round(time.Second),
			)
		}

		return nil, fmt.Errorf(
			"%w: pid=%d started=%s age=%s",
			ErrHeld,
			prev.info.PID,
			formatTime(prev.info.Started),
			prev.age.Round(time.Second),
		)
	}

	// stale 로 관측했다.
	//
	// 여기서는 절대로 RemoveAll 하지 않는다.
	// 관측했던 owner marker 만 삭제한 후 빈 디렉터리만 제거한다.
	//
	// 다른 stale 경쟁자가 먼저 owner 를 지웠다면 우리는 디렉터리를
	// 지우지 않고 그대로 재획득 경쟁으로 넘어간다.
	if err := clearStale(absPath, prev); err != nil {
		return nil, err
	}

	// stale 정리 후 단 한 번 재시도한다.
	l, created, err = tryCreate(absPath, token)
	if err != nil {
		return nil, err
	}

	if !created {
		// 다른 경쟁자가 stale takeover 경쟁에서 먼저 획득했다.
		return nil, fmt.Errorf(
			"%w: lost takeover race",
			ErrHeld,
		)
	}

	l.TookOver = true
	l.Prev = prev.info

	return l, nil
}

// Release 는 자신이 획득한 lock 을 해제한다.
//
// 핵심은 "읽어서 token 을 비교한 뒤 공용 path 를 삭제"하지 않는 것이다.
// 그것은 비교와 삭제 사이에 다른 인스턴스가 lock 을 탈취할 수 있는
// TOCTOU 경쟁을 만든다.
//
// 대신 Lock 이 소유한 token 전용 owner 파일:
//
//	owner-<my-token>
//
// 만 직접 삭제한다.
//
// stale takeover 가 이미 발생했다면 이 파일은 존재하지 않으므로
// ErrLost 가 되고, 새 소유자의 owner 파일은 건드리지 않는다.
//
// 자기 owner 를 성공적으로 제거한 뒤에는 lock 디렉터리가 비어 있을
// 때만 os.Remove 로 제거한다. RemoveAll 은 사용하지 않는다.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}

	// token 전용 marker 만 삭제한다.
	//
	// 소유권 반납은 이 한 줄로 끝난다.
	// 이후 디렉터리 정리는 뒷정리일 뿐 소유권과 무관하다.
	if err := os.Remove(l.ownerPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf(
				"%w: owner marker is gone",
				ErrLost,
			)
		}

		return fmt.Errorf(
			"lock: remove owner marker: %w",
			err,
		)
	}

	// 자기 marker 를 제거한 뒤 비어 있는 디렉터리만 제거한다.
	//
	// 예상치 못한 파일이 들어 있거나 다른 owner 가 존재한다면
	// os.Remove 가 실패하므로 남의 lock 을 파괴하지 않는다.
	//
	// 디렉터리가 이미 없는 것은 오류가 아니다. 다른 인스턴스가
	// 빈 디렉터리를 먼저 정리했다는 뜻이고, 소유권은 위에서 정상적으로
	// 반납되었다. 이것을 ErrLost 로 올리면 호출자가 "탈취당했다" 로
	// 읽어 LockStaleMinutes 를 엉뚱하게 조정한다.
	if err := os.Remove(l.path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf(
			"lock: remove directory %q after release "+
				"(directory may not be empty; inspect manually): %w",
			l.path,
			err,
		)
	}

	return nil
}

// normalizePath 는 빈 경로를 거부하고 절대 경로로 정규화한다.
//
// ledger.dsn 과 같이 TrimSpace 후 빈 문자열을 입구에서 막는다.
// Clean + Abs 로 구분자가 달라도 같은 위치를 가리키게 한다.
func normalizePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrInvalidPath
	}

	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("lock: absolute path %q: %w", path, err)
	}

	return abs, nil
}

// tryCreate 는 lock 디렉터리를 원자적으로 생성하고,
// 획득 인스턴스 고유의 owner marker 를 기록한다.
//
// path 가 이미 존재하면 (nil, false, nil),
// 획득에 성공하면 (*Lock, true, nil)을 반환한다.
//
// os.Mkdir 자체가 같은 경로에 대한 생성 경쟁을 원자적으로 중재한다.
func tryCreate(
	path string,
	token string,
) (*Lock, bool, error) {
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, false, nil
		}

		return nil, false, fmt.Errorf(
			"lock: create directory %q: %w",
			path,
			err,
		)
	}

	ownerPath := filepath.Join(
		path,
		ownerPrefix+token,
	)

	f, err := os.OpenFile(
		ownerPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		// Mkdir 에 성공한 프로세스만 여기 들어올 수 있으므로
		// owner 생성 실패 시 자기 디렉터리를 정리한다.
		_ = os.Remove(path)

		return nil, false, fmt.Errorf(
			"lock: create owner marker %q: %w",
			ownerPath,
			err,
		)
	}

	started := time.Now().UTC()

	content := fmt.Sprintf(
		"pid=%d\nstarted=%s\ntoken=%s\n",
		os.Getpid(),
		started.Format(time.RFC3339Nano),
		token,
	)

	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(ownerPath)
		_ = os.Remove(path)

		return nil, false, fmt.Errorf(
			"lock: write owner marker %q: %w",
			ownerPath,
			err,
		)
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(ownerPath)
		_ = os.Remove(path)

		return nil, false, fmt.Errorf(
			"lock: close owner marker %q: %w",
			ownerPath,
			err,
		)
	}

	return &Lock{
		path:      path,
		token:     token,
		ownerPath: ownerPath,
	}, true, nil
}

// readSnapshot 은 현재 lock 디렉터리를 관측하여
// 소유자 정보와 나이를 반환한다.
//
// 정상 상태에서는 owner-<token> 파일이 정확히 하나 존재한다.
//
// owner 파일 내용의 started 를 읽을 수 있으면 그 시각을 기준으로 하고,
// started 가 손상되었으면 owner 파일의 수정시각을 기준으로 한다.
//
// 디렉터리만 만들어지고 owner 기록 전에 프로세스가 죽은 경우처럼
// owner 파일이 하나도 없으면 lock 디렉터리의 수정시각을 사용하며,
// hasOwner=false 로 표시하여 호출자가 다른 문턱을 쓰게 한다.
//
// owner marker 가 둘 이상 존재하면 정상적인 lock 상태가 아니므로
// 임의로 하나를 선택하지 않고 오류를 반환한다.
// 이것은 양보(ErrHeld)가 아니라 사람이 봐야 하는 상태다.
// 복구는 lock 디렉터리를 수동으로 확인·정리한 뒤 재실행한다.
func readSnapshot(path string) (snapshot, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return snapshot{}, err
	}

	var ownerName string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		if !strings.HasPrefix(entry.Name(), ownerPrefix) {
			continue
		}

		if ownerName != "" {
			return snapshot{}, fmt.Errorf(
				"lock: multiple owner markers in %q; "+
					"inspect the lock directory manually and remove leftovers",
				path,
			)
		}

		ownerName = entry.Name()
	}

	// owner 파일 생성 전에 프로세스가 죽었거나,
	// Release 가 owner 는 지웠지만 디렉터리를 지우지 못한 경우다.
	if ownerName == "" {
		st, err := os.Stat(path)
		if err != nil {
			return snapshot{}, err
		}

		return snapshot{
			age:      time.Since(st.ModTime()),
			hasOwner: false,
		}, nil
	}

	ownerPath := filepath.Join(path, ownerName)

	raw, err := os.ReadFile(ownerPath)
	if err != nil {
		return snapshot{}, err
	}

	info := parseInfo(string(raw))

	// owner 파일 이름의 token 은 디렉터리 구조 자체가 보장하는
	// 소유권 표식이다. 내용이 일부 손상되어 token 행을 읽지 못했으면
	// 파일명에서 복구한다.
	if info.Token == "" {
		info.Token = strings.TrimPrefix(
			ownerName,
			ownerPrefix,
		)
	}

	basis := info.Started

	if basis.IsZero() {
		st, err := os.Stat(ownerPath)
		if err != nil {
			return snapshot{}, err
		}

		basis = st.ModTime()
	}

	return snapshot{
		info:      info,
		ownerPath: ownerPath,
		age:       time.Since(basis),
		hasOwner:  true,
	}, nil
}

// clearStale 은 Acquire 가 실제로 관측한 stale 상태만 안전하게 정리한다.
//
// ownerPath 가 있으면 그 특정 marker 만 삭제한다.
//
// 두 stale 경쟁자가 같은 marker 를 관측했을 때:
//   - 하나만 marker 삭제에 성공한다.
//   - 늦은 쪽은 ErrNotExist 를 보며 디렉터리를 삭제하지 않는다.
//
// 따라서 늦은 프로세스가 먼저 획득한 새 owner 를 삭제하는 일이 없다.
//
// owner marker 가 없는 stale 디렉터리는 빈 디렉터리일 때만 제거한다.
// 그 사이 다른 프로세스가 owner 를 만들었다면 디렉터리가 비어 있지 않아
// os.Remove 가 실패하므로 남의 lock 을 파괴하지 않는다.
// RemoveAll 은 절대 사용하지 않는다.
//
// 예상 밖 파일 때문에 디렉터리가 비어 있지 않으면 hard error 로 올린다.
// 운영자는 lock 디렉터리를 수동으로 확인해야 한다.
func clearStale(
	path string,
	s snapshot,
) error {
	if s.ownerPath != "" {
		if err := os.Remove(s.ownerPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// 다른 stale 경쟁자 또는 기존 소유자가 먼저 marker 를
				// 제거했다.
				//
				// 이 경우 현재 path 가 누구 소유인지 알 수 없으므로
				// 여기서는 디렉터리를 절대 삭제하지 않는다.
				return nil
			}

			return fmt.Errorf(
				"lock: remove stale owner marker: %w",
				err,
			)
		}

		// 우리가 관측했던 owner marker 를 실제로 제거한 경우에만
		// 비어 있는 디렉터리 제거를 시도한다.
		if err := os.Remove(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}

			return fmt.Errorf(
				"lock: remove stale directory %q "+
					"(not empty or in use; inspect the lock directory manually): %w",
				path,
				err,
			)
		}

		return nil
	}

	// owner marker 없이 디렉터리만 남은 경우.
	// 비어 있을 때만 삭제된다.
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf(
			"lock: remove stale empty directory %q "+
				"(not empty or in use; inspect the lock directory manually): %w",
			path,
			err,
		)
	}

	return nil
}

// parseInfo 는 owner marker 의 key=value 줄들을 관대하게 읽는다.
//
// 손상된 줄은 건너뛴다.
// 읽을 수 있는 정보만이라도 보존하면 stale takeover 로그가 더 유용하다.
func parseInfo(s string) Info {
	var info Info

	for _, line := range strings.Split(s, "\n") {
		key, value, found := strings.Cut(
			strings.TrimSpace(line),
			"=",
		)
		if !found {
			continue
		}

		switch key {
		case "pid":
			if pid, err := strconv.Atoi(value); err == nil {
				info.PID = pid
			}

		case "started":
			if t, err := time.Parse(
				time.RFC3339Nano,
				value,
			); err == nil {
				info.Started = t
			}

		case "token":
			info.Token = value
		}
	}

	return info
}

// newToken 은 획득 인스턴스마다 고유한 128-bit 무작위 식별자를 만든다.
func newToken() (string, error) {
	b := make([]byte, 16)

	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}

// formatTime 은 로그용 시각 표현이다.
// 손상된 owner 에서 Started 를 읽지 못한 경우 "unknown" 을 반환한다.
func formatTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}

	return t.Format(time.RFC3339)
}
