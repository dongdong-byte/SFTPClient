package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"SFTPClient/internal/pathpl"
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
// 현재 구현 단계는 MVP 1 PUT 이다.
// DOWNLOAD 가 완성되면 이 값과 Mode 별 검증 흐름을 함께 수정한다.
//
// Mode=both 를 그대로 허용하면 DOWNLOAD 없이 PUT 만 동작하는
// 불완전한 실행이 될 수 있으므로 현재는 시작 시 거부한다.
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
}

// checkSFTP 는 [PUT.SFTP] 값 자체를 검사한다.
//
// Transport=sftp 인 경우에만 Validate 에서 호출된다.
// 파일이 실제로 존재하는지는 CheckEnvironment 에서 검사한다.
func (c *Config) checkSFTP(add addFunc) {
	s := c.Put.SFTP

	// 설정 파일에 평문 비밀번호를 두지 않으므로
	// MVP 1 에서는 publickey 인증만 지원한다.
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
		// Hourly 는 HourLayout 이 반드시 dir 또는 flat 이어야 한다.
		//
		// 정상 Load 경로에서는 항상 값이 채워지지만,
		// Config 를 직접 구성하는 테스트나 다른 진입점에서도
		// zero-value 가 조용히 통과하지 않도록 여기서 다시 검사한다.
		if cc.Category.IsHourly() && !cc.HourLayout.Valid() {
			add(
				"[PUT.%s] HourLayout = %q must be %q or %q",
				cc.Category,
				cc.HourLayout,
				HourLayoutDir,
				HourLayoutFlat,
			)
		}

		// Daily 에는 HourLayout 자체가 존재하지 않는 것이 불변식이다.
		//
		// 정상 INI Load 에서는 knownKeys 가 Daily 의 HourLayout 키를 거부하지만,
		// Config 를 직접 구성하는 경로에서도 같은 규칙을 유지한다.
		if cc.Category.IsDaily() && cc.HourLayout != "" {
			add(
				"[PUT.%s] HourLayout = %q is not valid for a daily category",
				cc.Category,
				cc.HourLayout,
			)
		}

		// 정상적인 Load 흐름에서는 mapConfig 가 nil Template 을 이미 오류로 처리한다.
		// Validate 를 직접 호출하는 경로에서도 어떤 Path 가 빠졌는지 명확히 보고한다.
		if cc.LocalPath == nil {
			add("[PUT.%s] LocalPath is nil", cc.Category)
		} else {
			// Enabled=false 여도 Path Template 구조는 검사한다.
			// 꺼둔 설정의 오타가 나중에 활성화할 때 처음 드러나는 것을 막는다.
			checkHourToken(add, cc, "LocalPath", cc.LocalPath)
		}

		if cc.RemotePath == nil {
			add("[PUT.%s] RemotePath is nil", cc.Category)
		} else {
			checkHourToken(add, cc, "RemotePath", cc.RemotePath)
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

// checkHourToken 은 (HH) 토큰의 유무가 Category·HourLayout 과 맞는지 검사한다.
//
// Daily:
//
//	(HH) 를 갖지 않는다.
//	Daily 경로에 시각 토큰이 들어가면 실제 Daily 저장 구조와 다른 경로를
//	조회하게 되어 파일을 조용히 놓칠 수 있으므로 시작 시 거부한다.
//
// Hourly + dir:
//
//	(HH) 가 반드시 있어야 한다.
//	Scanner 는 00~23을 각각 계산하여 해당 디렉터리를 나열한다.
//	빠지면 24개 시각이 모두 같은 디렉터리로 확장되어 같은 곳을 반복해서 읽는다.
//
// Hourly + flat:
//
//	(HH) 가 없어야 한다.
//	한 날짜 디렉터리를 한 번 나열하여 그 안의 24시간 파일을 함께 관측한다.
//
// 선언과 경로가 어긋나도 파일시스템 오류 없이 일부 동작할 수 있기 때문에
// 추측해서 보정하지 않고 시작 시 거부한다.
//
// HourLayout 이 유효하지 않은 경우(dir/flat 아님)는 checkCategories 의
// 가드가 별도로 보고하므로, 여기서는 (HH) 검사를 조용히 건너뛴다.
func checkHourToken(
	add addFunc,
	cc CategoryConfig,
	key string,
	tpl *pathpl.Template,
) {
	has := tpl.HasToken(pathpl.TokenHH)

	switch {
	case cc.Category.IsDaily():
		if has {
			add(
				"[PUT.%s] %s has (%s) but daily paths must not include the hour token: %s",
				cc.Category,
				key,
				pathpl.TokenHH,
				tpl.String(),
			)
		}

	case cc.Category.IsHourly():
		switch cc.HourLayout {
		case HourLayoutDir:
			if !has {
				add(
					"[PUT.%s] %s has no (%s): hourly dir layout requires the hour token: %s",
					cc.Category,
					key,
					pathpl.TokenHH,
					tpl.String(),
				)
			}

		case HourLayoutFlat:
			if has {
				add(
					"[PUT.%s] %s has (%s) but hourly flat layout must not include the hour token: %s",
					cc.Category,
					key,
					pathpl.TokenHH,
					tpl.String(),
				)
			}
		}
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
// Windows ACL / Linux permission 정책은 security 패키지에서
// 플랫폼별 구현으로 담당한다.
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
