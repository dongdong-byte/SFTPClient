package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// ErrInvalidConfig 는 값은 읽혔으나 그 조합으로 실행할 수 없을 때 반환된다.
var ErrInvalidConfig = errors.New("config: invalid configuration")

// ErrEnvironment 는 설정이 가리키는 파일이나 디렉터리가 준비되지 않았을 때 반환된다.
//
// ErrInvalidConfig 와 나누는 이유는 고치는 방법이 다르기 때문이다.
// 전자는 config.ini 를 수정해야 하고, 후자는 서버에 파일을 놓아야 한다.
var ErrEnvironment = errors.New("config: environment not ready")

// downloadImplemented 는 이 빌드가 DOWNLOAD 방향을 수행할 수 있는지이다.
//
// MVP1 PUT 은 완료다. DOWNLOAD 와 BOTH 는 MVP3 이므로 현재는 false 다.
// DOWNLOAD 가 완성되면 이 값과 Mode 별 검증 흐름을 함께 수정한다.
//
// Mode=both 를 그대로 허용하면 DOWNLOAD 없이 PUT 만 동작하는
// 불완전한 실행이 될 수 있으므로 시작 시 거부한다.
const downloadImplemented = false

// Validate 는 값의 조합이 실행 가능한지 판정한다.
//
// 파일시스템을 건드리지 않는다.
// 실제 파일·디렉터리 존재 여부는 CheckEnvironment 가 담당한다.
//
// 나눠 두는 이유는 고치는 방법이 다르기 때문이다.
//
//	Validate         → config.ini 를 수정한다
//	CheckEnvironment → 서버의 파일·디렉터리 상태를 수정한다
//
// 첫 오류에서 멈추지 않고 가능한 오류를 전부 모아 돌려준다.
func (c *Config) Validate() error {
	var errs []error

	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	c.checkMode(add)
	c.checkTransport(add)
	c.checkScan(add)
	c.checkIngress(add)
	c.checkLedger(add)
	c.checkPut(add)

	// SFTP 전송을 실제로 선택한 경우에만 SFTP 설정을 검증한다.
	if c.General.Transport == "sftp" {
		c.checkSFTP(add)
	}

	c.checkCategories(add)
	c.checkLog(add)

	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf(
		"%w in %s:\n%w",
		ErrInvalidConfig,
		c.Path,
		errors.Join(errs...),
	)
}

type addFunc func(format string, args ...any)

// checkMode 는 [GENERAL] 설정의 실행 방향과 공통값을 검사한다.
func (c *Config) checkMode(add addFunc) {
	// Mode 가 정의된 값인지 먼저 확인한다.
	//
	// 정상 Load 흐름에서는 load.go 의 mode 가 ParseMode 로 이미 거른다.
	// 그럼에도 여기서 다시 보는 이유는 Mode 가 빈 값일 때
	// DoesPut 과 DoesDownload 가 모두 false 를 돌려주기 때문이다.
	// 그 상태로 통과하면 프로그램이 오류 없이 시작해 아무 방향도 수행하지 않는다.
	//
	// Config 를 직접 구성하는 경로(테스트, 향후 다른 진입점)에서
	// 이 조용한 실패가 나오지 않도록 막는다.
	if !c.General.Mode.Valid() {
		add("[GENERAL] Mode = %q is not a valid mode", c.General.Mode)
	}

	if !downloadImplemented && c.General.Mode.DoesDownload() {
		add(
			"[GENERAL] Mode = %s: DOWNLOAD is not implemented in this build; use put",
			c.General.Mode,
		)
	}

	if c.General.LedgerPath == "" {
		add("[GENERAL] LedgerPath is empty")
	}

	// LockStaleSeconds 의 양끝은 각각 다른 사고를 막는다.
	//
	// 하한 600초(10분): GraceSeconds 감각으로 60 같은 초급 값을 넣는
	// 오입력을 막는다. 그 값이 통과되면 1분 넘는 모든 정상 실행이
	// 탈취 대상이 되어 lock 이 없는 것보다 나쁘다(이중 전송).
	//
	// 상한 24시간: 지나치게 큰 오입력으로 크래시 후 장시간 전송이
	// 멈추는 상황을 방지한다.
	if c.General.LockStale < 600*time.Second || c.General.LockStale > 24*time.Hour {
		add(
			"[GENERAL] LockStaleSeconds must be between 600 and 86400, got %d",
			int64(c.General.LockStale/time.Second),
		)
	}

	// RepostDownloaded 는 DOWNLOAD 로 받은 파일을 다시 PUT 대상으로 삼을지의 설정이다.
	//
	// PUT 전용 인스턴스에는 DOWNLOAD 로 등록된 파일이 존재할 수 없으므로
	// true 로 두어도 아무 동작을 하지 않는다.
	if c.General.RepostDownloaded && !c.General.Mode.DoesDownload() {
		add(
			"[GENERAL] RepostDownloaded = true has no effect when Mode = %s",
			c.General.Mode,
		)
	}
}

// checkTransport 는 실행에 사용할 전송 계층을 검사한다.
//
// 기본값으로 조용히 전송을 시작하지 않는다.
// config.ini 에서 sftp 또는 localfs 를 명시해야 한다.
//
// --transport 가 지정된 경우에는 main 에서 이 값을 해당 실행에 한해
// 덮어쓰는 용도로 사용한다.
func (c *Config) checkTransport(add addFunc) {
	if c.General.Transport != "sftp" && c.General.Transport != "localfs" {
		add(
			"[GENERAL] Transport = %q must be \"sftp\" or \"localfs\"",
			c.General.Transport,
		)
	}
}

// checkScan 은 Hot / Deep Scan 범위와 실행 시각을 검사한다.
func (c *Config) checkScan(add addFunc) {
	if c.Scan.RecentDays < 1 {
		add(
			"[SCAN] ScanRecentDays = %d must be at least 1",
			c.Scan.RecentDays,
		)
	}

	if c.Scan.Days < 1 {
		add(
			"[SCAN] ScanDays = %d must be at least 1",
			c.Scan.Days,
		)
	}

	// Deep Scan 은 Hot Scan 범위를 포함해야 한다.
	// 그렇지 않으면 하루 1회 수행하는 넓은 Scan 이라는 의미가 깨진다.
	if c.Scan.Days < c.Scan.RecentDays {
		add(
			"[SCAN] ScanDays = %d must be at least ScanRecentDays = %d",
			c.Scan.Days,
			c.Scan.RecentDays,
		)
	}

	if c.Scan.DeepScanHour < 0 || c.Scan.DeepScanHour > 23 {
		add(
			"[SCAN] DeepScanHour = %d must be between 0 and 23",
			c.Scan.DeepScanHour,
		)
	}
}

// checkIngress 는 [INGRESS] 설정을 검사한다.
func (c *Config) checkIngress(add addFunc) {
	// Grace=0 은 검사를 끄는 의미로 허용한다.
	// 음수만 잘못된 설정이다.
	if c.Ingress.Grace < 0 {
		add("[INGRESS] GraceSeconds must not be negative")
	}
}

// checkLedger 는 Ledger 보존기간의 불변식을 검사한다.
func (c *Config) checkLedger(add addFunc) {
	if c.Ledger.RetentionDays < 1 {
		add(
			"[LEDGER] RetentionDays = %d must be at least 1",
			c.Ledger.RetentionDays,
		)
	}

	// 이 불변식이 깨지면 디스크에는 파일이 남아 있는데
	// Ledger 에서만 행이 삭제될 수 있다.
	//
	// 이후 Scan 에서 해당 파일이 신규로 판정되어 재전송되므로
	// RetentionDays 는 반드시 ScanDays 보다 길어야 한다.
	if c.Ledger.RetentionDays <= c.Scan.Days {
		add(
			"[LEDGER] RetentionDays = %d must be greater than [SCAN] ScanDays = %d "+
				"(otherwise files still on disk but purged from the ledger are resent)",
			c.Ledger.RetentionDays,
			c.Scan.Days,
		)
	}
}

// checkPut 은 [PUT] 실행 제한값을 검사한다.
func (c *Config) checkPut(add addFunc) {
	// MaxWorkers 의 양끝은 각각 다른 사고를 막는다.
	//
	// 하한 1: 0/음수면 워커가 뜨지 않아 전송이 조용히 0건이 된다.
	// 상한 16: 과도한 병렬도를 설정하는 오입력을 막는다.
	if c.Put.MaxWorkers < 1 || c.Put.MaxWorkers > 16 {
		add(
			"[PUT] MaxWorkers = %d must be between 1 and 16",
			c.Put.MaxWorkers,
		)
	}

	// MaxRetries 는 동일 revision 의 누적 자동 시도 상한이다.
	// 첫 시도를 포함한다. 최소 한 번은 시도해야 하므로 0 도 거부한다.
	if c.Put.MaxRetries < 1 {
		add(
			"[PUT] MaxRetries = %d must be at least 1",
			c.Put.MaxRetries,
		)
	}

	if c.Put.MaxFilesPerRun < 0 {
		add(
			"[PUT] MaxFilesPerRun = %d must not be negative (use 0 for unlimited)",
			c.Put.MaxFilesPerRun,
		)
	}

	// 부재는 loader 가 이미 기본값(500)으로 접었으므로 여기 도달하는
	// 음수는 명시적 오기입이다. (UNIT2 설계 v3 §3.1: 음수 → 거부)
	if c.Put.MaxHashBackfillPerRun < 0 {
		add(
			"[PUT] MaxHashBackfillPerRun = %d must not be negative "+
				"(use 0 to disable backfill)",
			c.Put.MaxHashBackfillPerRun,
		)
	}
}

// checkSFTP 는 [PUT.SFTP] 값 자체를 검사한다.
//
// Transport=sftp 인 경우에만 Validate 에서 호출된다.
// 파일이 실제로 존재하는지는 CheckEnvironment 에서 검사한다.
func (c *Config) checkSFTP(add addFunc) {
	s := c.Put.SFTP

	// 설정 파일에 평문 비밀번호를 두지 않으므로
	// 설정 파일에 평문 비밀번호를 두지 않으므로 publickey 만 지원한다.
	//
	// load.go 가 이 값을 소문자로 정규화하므로
	// 여기 도달하는 값은 이미 소문자이다.
	if s.AuthMethod != "publickey" {
		add(
			"[PUT.SFTP] AuthMethod = %q: only \"publickey\" is supported",
			s.AuthMethod,
		)
	}

	if s.Host == "" {
		add("[PUT.SFTP] Host is empty")
	}

	if s.User == "" {
		add("[PUT.SFTP] User is empty")
	}

	if s.Port < 1 || s.Port > 65535 {
		add(
			"[PUT.SFTP] Port = %d must be between 1 and 65535",
			s.Port,
		)
	}

	if s.PrivateKey == "" {
		add("[PUT.SFTP] PrivateKey is empty")
	}

	// host key 검증을 비활성화하는 실행 경로는 두지 않는다.
	if s.KnownHosts == "" {
		add(
			"[PUT.SFTP] KnownHosts is empty; host key verification is required",
		)
	}

	// StallTimeoutSeconds 의 양끝은 각각 다른 사고를 막는다.
	//
	// 하한 5초: 정상적인 원격 왕복·일시 정체를 무진행으로 오인해
	// 매 회차를 끊는 오입력을 막는다. 그 값이 통과되면 감시가 없는
	// 것보다 나쁘다 (정상 전송 파괴).
	//
	// 상한 600초는 과도한 대기를 제한한다. lock 나이와 무진행 시간은
	// 기준이 달라 이 범위만으로 stale 탈취 이전 종료를 보장하지 않는다.
	if s.StallTimeout < 5*time.Second || s.StallTimeout > 600*time.Second {
		add(
			"[PUT.SFTP] StallTimeoutSeconds must be between 5 and 600, got %d",
			int64(s.StallTimeout/time.Second),
		)
	}
}

// checkCategories 는 Category 설정과 Path Template 의 관계를 검사한다.
func (c *Config) checkCategories(add addFunc) {
	enabled := 0

	// 활성화된 두 Category 가 완전히 같은 LocalPath 를 가리키면
	// 동일한 물리 파일을 서로 다른 Category 로 처리할 가능성이 있다.
	//
	// 한계: 템플릿 원문을 비교하므로 표기만 다르고 결과가 같은 경우는 잡지 못한다.
	seenLocal := make(map[string]string)

	for _, cc := range c.Put.Categories {
		// 정상적인 Load 흐름에서는 mapConfig 가 nil Template 을 이미 오류로 처리한다.
		// Validate 를 직접 호출하는 경로에서도 어떤 Path 가 빠졌는지 명확히 보고한다.
		if cc.LocalPath == nil {
			add("[PUT.%s] LocalPath is nil", cc.Category)
		}

		if cc.RemotePath == nil {
			add("[PUT.%s] RemotePath is nil", cc.Category)
		}

		if !cc.Enabled {
			continue
		}

		enabled++

		// LocalPath 가 없다는 오류는 위에서 이미 기록했다.
		// nil 을 String() 하지 않기 위한 방어이다.
		if cc.LocalPath == nil {
			continue
		}

		local := cc.LocalPath.String()

		if prev, ok := seenLocal[local]; ok {
			add(
				"[PUT.%s] LocalPath is identical to [PUT.%s]: %s",
				cc.Category,
				prev,
				local,
			)
		} else {
			seenLocal[local] = cc.Category.String()
		}
	}

	// 활성 Category 가 하나도 없으면 프로그램은 오류 없이 실행되고
	// 아무 파일도 처리하지 않는 상태가 된다.
	if enabled == 0 {
		add("no category is enabled; the program would do nothing")
	}
}

// checkLog 는 [LOG] 설정을 검사한다.
func (c *Config) checkLog(add addFunc) {
	if !slices.Contains(LogLevels(), c.Log.Level) {
		add(
			"[LOG] Level = %q must be one of %v",
			c.Log.Level,
			LogLevels(),
		)
	}

	if c.Log.Dir == "" {
		add("[LOG] Dir is empty")
	}

	if c.Log.RetentionDays < 1 {
		add(
			"[LOG] RetentionDays = %d must be at least 1",
			c.Log.RetentionDays,
		)
	}
}

// CheckEnvironment 는 설정이 가리키는 파일과 디렉터리가
// 실제 서버에 준비되어 있는지 검사한다.
//
// Validate 와 분리한 이유는 고치는 방법이 다르기 때문이다.
// 이쪽 오류는 config.ini 값의 조합이 아니라 서버 상태를 고쳐야 한다.
//
// 여기서는 LocalPath 의 특정 날짜/시간 디렉터리를 검사하지 않는다.
// 아직 자료가 들어오지 않은 정상 슬롯일 수 있으며,
// Scan 단계에서 fs.ErrNotExist 를 정상적인 빈 슬롯으로 처리한다.
//
// 개인키의 OS별 상세 권한 검사도 여기서 하지 않는다.
// Linux 전용 권한 강제는 선제 구현하지 않으며, 공식 보안점검에서
// 구체적인 요구가 나온 경우에만 적용한다.
//
// Log.Dir 도 검사하지 않는다. 로거가 시작 시 스스로 생성한다.
// 생성 실패는 로거가 보고하며, 그 시점에는 아직 아무 파일도 전송하지 않았다.
func (c *Config) CheckEnvironment() error {
	var errs []error

	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	// SQLite 는 DB 파일은 만들 수 있지만 존재하지 않는 상위 디렉터리까지
	// 자동으로 생성하지는 않는다.
	//
	// Ledger DB 파일 자체는 첫 실행 때 아직 없을 수 있으므로
	// 부모 디렉터리만 검사한다.
	if c.General.LedgerPath != "" {
		if err := requireDir(filepath.Dir(c.General.LedgerPath)); err != nil {
			add("[GENERAL] LedgerPath: %v", err)
		}
	}

	// SFTP 를 실제 전송 계층으로 선택한 경우에만
	// 개인키와 known_hosts 파일의 존재를 요구한다.
	if c.General.Transport == "sftp" {
		if err := requireFile(c.Put.SFTP.PrivateKey); err != nil {
			add("[PUT.SFTP] PrivateKey: %v", err)
		}

		if err := requireFile(c.Put.SFTP.KnownHosts); err != nil {
			add("[PUT.SFTP] KnownHosts: %v", err)
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf(
		"%w for %s:\n%w",
		ErrEnvironment,
		c.Path,
		errors.Join(errs...),
	)
}

// requireDir 는 path 가 실제 디렉터리인지 검사한다.
func requireDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}

	return nil
}

// requireFile 는 path 가 실제 파일인지 검사한다.
func requireFile(path string) error {
	if path == "" {
		// 정상적인 Load 흐름에서는 Validate 가 먼저 빈 값을 거부한다.
		// 직접 호출된 경우에도 같은 오류를 두 번 보고하지 않도록 한다.
		return nil
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}

	if info.IsDir() {
		return fmt.Errorf("%q is a directory, want a file", path)
	}

	return nil
}
