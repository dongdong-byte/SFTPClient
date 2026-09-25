package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"SFTPClient/internal/domain"
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
// PUT 은 완료다. DOWNLOAD 는 커밋 계획(DOWNLOAD_COMMIT_PLAN v1)에 따라
// 구현 중이며, 커밋 1(설정)부터 6(보고)까지는 이 게이트를 false 로 두고
// 커밋 7(main 배선)에서 연다. 그동안 Mode=download 의 설정 로드·검증은
// 이미 동작하되 시작은 이 게이트가 거부한다.
//
// Mode=both 는 DOWNLOAD 개방 이후에도 별도 계획 전까지 계속 거부한다.
const downloadImplemented = false

// bothImplemented 는 DOWNLOAD 단독 실행과 별개인 BOTH 운영 게이트다.
//
// DOWNLOAD 커밋 7에서 downloadImplemented 만 true 로 바꿔도 BOTH가 함께
// 열리지 않게 분리한다. BOTH는 인계 대기·부분 실패·lock 범위를 별도
// 확정한 뒤 이 값만 독립적으로 연다 (DOWNLOAD 설계 v3 §8).
const bothImplemented = false

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

	// 공통 — Mode 와 무관하게 검사한다 (DOWNLOAD 커밋 계획 §2.3).
	c.checkMode(add)
	c.checkTransport(add)
	c.checkLockPath(add)
	c.checkScan(add)
	c.checkLog(add)

	// PUT — 실행할 방향의 값만 판정한다. Mode=download 에서 PUT 블록이
	// 함께 적혀 있어도(Put.Present) 그 값은 실행에 쓰이지 않으므로 보지
	// 않는다. 단 Ping-Pong 검사는 아래에서 두 블록의 경로만 대조한다.
	if c.judgesPut() {
		c.checkIngress(add)
		c.checkLedger(add)
		c.checkPut(add)

		// SFTP 전송을 실제로 선택한 경우에만 SFTP 설정을 검증한다.
		if c.General.Transport == "sftp" {
			c.checkSFTP(add)
		}

		c.checkCategories(add)
	}

	// DOWNLOAD — 게이트(downloadImplemented)와 별개로 값은 검증한다.
	// 게이트가 열리는 커밋에서 이 검사를 다시 손대지 않기 위해서다.
	if c.General.Mode.DoesDownload() {
		c.checkDownload(add)

		if c.General.Transport == "sftp" {
			c.checkSFTPSection(add, "DOWNLOAD.SFTP", c.Download.SFTP)
		}

		c.checkDownloadCategories(add)
	}

	// Ping-Pong — 두 블록이 모두 존재하면 Mode 와 관계없이 검사한다.
	if c.Put.Present && c.Download.Present {
		c.checkPingPong(add)
	}

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

// judgesPut 은 PUT 쪽 값을 판정할지 답한다.
//
// Mode.DoesPut() 이 기본이다. 알 수 없는 Mode 는 DoesPut 도 DoesDownload 도
// false 라 아무 방향도 판정하지 않게 되는데, 그러면 운영자는 Mode 오타
// 하나만 보고 고친 뒤 다음 실행에서 나머지 오류를 다시 만난다. 그래서
// 알 수 없는 Mode 는 DOWNLOAD 이전과 같이 PUT 쪽 검증을 그대로 수행해
// 오류를 최대한 모은다 (TestValidate_AccumulatesErrors 가 이 계약을 고정).
func (c *Config) judgesPut() bool {
	return c.General.Mode.DoesPut() || !c.General.Mode.Valid()
}

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

	if !bothImplemented && c.General.Mode == domain.ModeBoth {
		add(
			"[GENERAL] Mode = %s: BOTH is not implemented in this build; "+
				"use put or download",
			c.General.Mode,
		)
	}

	// LedgerPath 는 PUT 장부의 위치다. DOWNLOAD 전용 인스턴스는 장부를
	// 열지 않으므로 요구하지 않는다 (커밋 계획 §1.2).
	if c.judgesPut() && c.General.LedgerPath == "" {
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
	if c.General.RepostDownloaded {
		switch c.General.Mode {
		case domain.ModePut:
			// 기존 PUT 오류 문구는 운영 로그와 테스트 호환을 위해 유지한다.
			add(
				"[GENERAL] RepostDownloaded = true has no effect when Mode = %s",
				c.General.Mode,
			)

		case domain.ModeDownload:
			// DOWNLOAD v3는 origin·RepostDownloaded·PUT 장부를 사용하지
			// 않는다. 조용히 무시하면 중계가 켜졌다고 오해할 수 있다.
			add(
				"[GENERAL] RepostDownloaded = true is not supported when Mode = %s; "+
					"DOWNLOAD does not write origin or use the PUT ledger",
				c.General.Mode,
			)

		case domain.ModeBoth:
			// BOTH 자체가 위 게이트에서 거부된다. 인계 정책을 확정하기
			// 전에는 이 키의 의미도 열지 않는다.
			add(
				"[GENERAL] RepostDownloaded = true is not available until BOTH is implemented",
			)
		}
	}
}

// checkLockPath 는 [GENERAL] LockPath 의 호환 규칙을 검사한다.
//
//	LockPath 명시         → 그 경로
//	생략 + Mode=put      → LedgerPath + ".lock" 으로 접힌다 (종전 동작)
//	생략 + download/both → 시작 거부
//
// DOWNLOAD 에서 LedgerPath 가 우연히 적혀 있더라도 그 값으로 접지 않는다.
// 장부를 쓰지 않는 방향의 lock 을 장부 경로에 묶어 두면, 나중에 LedgerPath
// 를 지우는 순간 lock 위치가 조용히 바뀐다.
func (c *Config) checkLockPath(add addFunc) {
	if lp := c.General.LockPath; lp != "" {
		// lock 은 파일이 아니라 디렉터리이고, lock.Acquire 는 이미 있는
		// 경로를 "남이 쥔 lock" 으로 관측한다. 그래서 LockPath 가 장부 파일
		// 이나 로그 디렉터리를 가리키면 매 회차 획득에 실패한다. 특히 로그
		// 디렉터리는 로거가 방금 파일을 만들어 mtime 이 새것이라 owner 없는
		// lock 의 30초 유예에 걸리고, 그러면 ErrHeld 로 "조용히 종료(exit 0)"
		// 되어 전송이 멈춘 것을 아무도 모른다. 시작 단계에서 거부한다.
		if c.General.LedgerPath != "" &&
			pathContains(lp, c.General.LedgerPath) {
			add(
				"[GENERAL] LockPath = %q must not be LedgerPath or its parent "+
					"(the lock is a directory; use e.g. data\\rinexclient.lock)",
				lp,
			)
		}

		if c.Log.Dir != "" && pathContains(lp, c.Log.Dir) {
			add(
				"[GENERAL] LockPath = %q must not be [LOG] Dir or its parent "+
					"(the lock is a directory of its own; use e.g. data\\rinexclient.lock)",
				lp,
			)
		}

		return
	}

	// 알 수 없는 Mode 는 checkMode 가 이미 보고했다. 그 위에 이 오류를
	// 겹치면 Mode 를 고치면 사라질 오류로 운영자를 헷갈리게 한다.
	if c.General.Mode.Valid() && c.General.Mode != domain.ModePut {
		add(
			"[GENERAL] LockPath is required when Mode = %s "+
				"(the lock is derived from LedgerPath only for put)",
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
		return
	}

	// DOWNLOAD 는 원격 SFTP 목록·GET 만 한다. localfs 를 허용하면
	// 커밋 7에서 게이트가 열리는 순간 원격 구현 없이 기동한다.
	// --transport 로 덮어쓰는 경로는 main 이 같은 조합을 다시 거부해야 한다.
	if c.General.Mode.DoesDownload() && c.General.Transport == "localfs" {
		add(
			"[GENERAL] Transport = localfs is not supported when Mode = %s; "+
				"DOWNLOAD reads a remote SFTP server",
			c.General.Mode,
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
	c.checkSFTPSection(add, "PUT.SFTP", c.Put.SFTP)
}

// checkSFTPSection 은 [PUT.SFTP] / [DOWNLOAD.SFTP] 를 같은 규칙으로 검사한다.
// section 은 오류 문구에 들어가는 섹션 이름이다. PUT 의 기존 문구는
// 한 글자도 바뀌지 않는다 — 섹션 이름만 매개변수다.
func (c *Config) checkSFTPSection(
	add addFunc,
	section string,
	s SFTPConfig,
) {
	// 설정 파일에 평문 비밀번호를 두지 않으므로 publickey 만 지원한다.
	//
	// load.go 가 이 값을 소문자로 정규화하므로
	// 여기 도달하는 값은 이미 소문자이다.
	if s.AuthMethod != "publickey" {
		add(
			"[%s] AuthMethod = %q: only \"publickey\" is supported",
			section,
			s.AuthMethod,
		)
	}

	if s.Host == "" {
		add("[%s] Host is empty", section)
	}

	if s.User == "" {
		add("[%s] User is empty", section)
	}

	if s.Port < 1 || s.Port > 65535 {
		add(
			"[%s] Port = %d must be between 1 and 65535",
			section,
			s.Port,
		)
	}

	if s.PrivateKey == "" {
		add("[%s] PrivateKey is empty", section)
	}

	// host key 검증을 비활성화하는 실행 경로는 두지 않는다.
	if s.KnownHosts == "" {
		add(
			"[%s] KnownHosts is empty; host key verification is required",
			section,
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
			"[%s] StallTimeoutSeconds must be between 5 and 600, got %d",
			section,
			int64(s.StallTimeout/time.Second),
		)
	}
}

// checkCategories 는 PUT Category 설정과 Path Template 의 관계를 검사한다.
func (c *Config) checkCategories(add addFunc) {
	enabled := c.checkCategorySet(add, "PUT", c.Put.Categories, putRoles)

	// 활성 Category 가 하나도 없으면 프로그램은 오류 없이 실행되고
	// 아무 파일도 처리하지 않는 상태가 된다.
	if enabled == 0 {
		add("no category is enabled; the program would do nothing")
	}
}

// checkDownloadCategories 는 DOWNLOAD Category 설정을 검사한다.
func (c *Config) checkDownloadCategories(add addFunc) {
	enabled := c.checkCategorySet(
		add,
		"DOWNLOAD",
		c.Download.Categories,
		downloadRoles,
	)

	if enabled == 0 {
		add("[DOWNLOAD] no category is enabled; the program would do nothing")
	}

	// DOWNLOAD RemotePath 는 SFTP 경로라 구분자가 '/' 하나다. Windows 운영자가
	// 습관대로 '\' 를 쓰면 서버는 그것을 이름의 글자로 보아 "없음"으로 답하고,
	// Scanner 는 매일 빈 날짜(Missing)로 세어 오류 없이 수신 0건이 계속된다.
	// 비활성 Category 도 잠복 설정을 남기지 않도록 함께 본다.
	//
	// PUT RemotePath 에는 적용하지 않는다. 운영 중인 PUT 설정을 새로 거부할
	// 수 있어 별도 판단이 필요하다.
	for _, cc := range c.Download.Categories {
		if cc.RemotePath == nil || !strings.Contains(cc.RemotePath.String(), `\`) {
			continue
		}

		add(
			"[DOWNLOAD.%s] RemotePath uses '\\': %s; "+
				"SFTP paths use '/' only (e.g. /RNX/(YYYY)/(DOY)/)",
			cc.Category,
			cc.RemotePath.String(),
		)
	}
}

// categoryRoles 는 방향별로 어느 키가 원본이고 어느 키가 목적지인지다.
//
// 검사 규칙은 두 방향이 같고 키의 역할만 뒤바뀐다:
//
//	원본(스캔 경로)   활성 Category 간 동일 원문 금지 — 같은 물리 파일을
//	                  서로 다른 Category 로 처리할 가능성
type categoryRoles struct {
	source                string // 원본 키 이름 ("LocalPath" / "RemotePath")
	sourceOf              func(CategoryConfig) *pathpl.Template
	destination           string
	destOf                func(CategoryConfig) *pathpl.Template
	rejectDailySourceHour bool
	rejectDailyDestHour   bool
}

var putRoles = categoryRoles{
	source:                "LocalPath",
	sourceOf:              func(cc CategoryConfig) *pathpl.Template { return cc.LocalPath },
	destination:           "RemotePath",
	destOf:                func(cc CategoryConfig) *pathpl.Template { return cc.RemotePath },
	rejectDailySourceHour: true,
}

var downloadRoles = categoryRoles{
	source:              "RemotePath",
	sourceOf:            func(cc CategoryConfig) *pathpl.Template { return cc.RemotePath },
	destination:         "LocalPath",
	destOf:              func(cc CategoryConfig) *pathpl.Template { return cc.LocalPath },
	rejectDailyDestHour: true,
}

// checkCategorySet 은 [<dir>.<CATEGORY>] 집합 하나를 검사하고
// 활성 Category 수를 돌려준다. PUT 의 기존 오류 문구는 그대로다.
func (c *Config) checkCategorySet(
	add addFunc,
	dir string,
	cats []CategoryConfig,
	roles categoryRoles,
) int {
	enabled := 0

	// 활성화된 두 Category 가 완전히 같은 원본 경로를 가리키면
	// 동일한 물리 파일을 서로 다른 Category 로 처리할 가능성이 있다.
	//
	// 한계: 템플릿 원문을 비교하므로 표기만 다르고 결과가 같은 경우는 잡지 못한다.
	seenSource := make(map[string]string)

	for _, cc := range cats {
		// 정상적인 Load 흐름에서는 mapConfig 가 nil Template 을 이미 오류로 처리한다.
		// Validate 를 직접 호출하는 경로에서도 어떤 Path 가 빠졌는지 명확히 보고한다.
		if cc.LocalPath == nil {
			add("[%s.%s] LocalPath is nil", dir, cc.Category)
		}

		if cc.RemotePath == nil {
			add("[%s.%s] RemotePath is nil", dir, cc.Category)
		}

		// 비활성 Category 도 경로의 역할 규칙은 검사한다. 나중에 Enabled만
		// true로 바꿨을 때 그제야 배포가 실패하는 잠복 설정을 남기지 않는다.
		// nil 오류는 위에서 이미 기록했으므로 HasToken 호출만 방어한다.
		//
		// Daily 목적지에 (HH)가 있으면 파일명에서 값을 꺼낼 수 없어
		// ExpandFile이 파일마다 실패한다. PUT 원본 LocalPath도 기존 PATH v4
		// 계약에 따라 거부한다. 반면 DOWNLOAD 원본 RemotePath의 (HH)는
		// 원격 폴더를 나열하는 패턴이므로 Daily 파일명에 시각이 없어도
		// 사용할 수 있다(커밋 계획 §2.2: 원본 파일 토큰 허용).
		//
		// PUT 은 목적지(RemotePath)에 파일 토큰 자체가 금지라 load 단계에서
		// 이미 걸러지므로, 여기서는 원본만 실질적으로 검사된다.
		if cc.Category.IsDaily() {
			if roles.rejectDailySourceHour {
				c.rejectDailyHour(add, dir, cc, roles.source, roles.sourceOf(cc))
			}
			if roles.rejectDailyDestHour {
				c.rejectDailyHour(add, dir, cc, roles.destination, roles.destOf(cc))
			}
		}

		if !cc.Enabled {
			continue
		}

		enabled++

		// 원본 경로가 없다는 오류는 위에서 이미 기록했다.
		// nil 을 String() 하지 않기 위한 방어이다.
		src := roles.sourceOf(cc)
		if src == nil {
			continue
		}

		key := src.String()

		if prev, ok := seenSource[key]; ok {
			add(
				"[%s.%s] %s is identical to [%s.%s]: %s",
				dir,
				cc.Category,
				roles.source,
				dir,
				prev,
				key,
			)
		} else {
			seenSource[key] = cc.Category.String()
		}
	}

	return enabled
}

// rejectDailyHour 는 Daily Category 의 경로 하나에 (HH) 가 있으면 오류를
// 기록한다. PUT LocalPath 의 기존 문구를 그대로 쓴다.
func (c *Config) rejectDailyHour(
	add addFunc,
	dir string,
	cc CategoryConfig,
	key string,
	tpl *pathpl.Template,
) {
	if tpl == nil || !tpl.HasToken(pathpl.TokenHH) {
		return
	}

	add(
		"[%s.%s] %s uses (HH) but the category is daily; "+
			"daily files have no hour folders: %s",
		dir,
		cc.Category,
		key,
		tpl.String(),
	)
}

// checkDownload 는 [DOWNLOAD] 실행 제한값을 검사한다.
func (c *Config) checkDownload(add addFunc) {
	d := c.Download

	// MaxWorkers 는 PUT 과 같은 운영 범위다 (커밋 계획 §2.1).
	if d.MaxWorkers < 1 || d.MaxWorkers > 16 {
		add(
			"[DOWNLOAD] MaxWorkers = %d must be between 1 and 16",
			d.MaxWorkers,
		)
	}

	// GraceSeconds 는 부재를 기본값으로 접지 않는다. 원격 시계 차이는
	// 현장 확인 전에는 안전한 기본값이 없다. 끄려면 0 을 명시한다.
	if !d.GraceSet {
		add(
			"[DOWNLOAD] GraceSeconds is required when Mode = %s "+
				"(use 0 to disable the grace check)",
			c.General.Mode,
		)
	} else if d.Grace < 0 {
		add("[DOWNLOAD] GraceSeconds must not be negative")
	}

	// Sites 는 load 가 이미 ParseSiteList 로 걸렀다. Config 를 직접
	// 구성하는 경로에서도 같은 규칙이 성립하도록 한 번 더 본다.
	if len(d.Sites) > 0 {
		if _, err := domain.NormalizeSiteList(d.Sites); err != nil {
			add("[DOWNLOAD] Sites: %v", err)
		}
	}
}

// checkPingPong 은 PUT 목적지와 DOWNLOAD 원본이 같은 서버의 겹치는 경로를
// 가리키는 순환 설정을 거부한다 (DOWNLOAD v3 §3.4·§7.1-5, 커밋 계획 §2.4, D11).
//
// PUT 이 올린 파일을 DOWNLOAD 가 다시 받고, 그 파일이 다시 PUT 후보가 되는
// 무한 순환은 실행 중에는 알아채기 어렵다. 두 블록이 모두 존재하면 Mode 와
// 관계없이 보수적으로 검사한다 — 오늘 Mode=put 인 인스턴스가 내일 Mode 만
// 바꿔 BOTH 로 갈 때 그제야 터지는 설정을 남기지 않는다.
//
// 판정 재료:
//
//  1. 두 SFTP 가 같은 정규화된 host:port 를 가리킨다.
//  2. 활성 PUT RemotePath 와 활성 DOWNLOAD RemotePath 의 정적 범위가
//     같거나 한쪽이 다른 쪽을 포함할 가능성이 있다 (remoteScope 주석).
//
// 한계: 서버 별칭(IP 와 호스트명)·심볼릭 링크·서로 다른 config 두 개를
// 통한 순환은 한 인스턴스의 정적 검사로 보장하지 않는다.
func (c *Config) checkPingPong(add addFunc) {
	if !sameEndpoint(c.Put.SFTP, c.Download.SFTP) {
		return
	}

	for _, pc := range c.Put.EnabledCategories() {
		if pc.RemotePath == nil {
			continue
		}

		dest := remoteScopeOf(pc.RemotePath.String())

		for _, dc := range c.Download.EnabledCategories() {
			if dc.RemotePath == nil {
				continue
			}

			src := remoteScopeOf(dc.RemotePath.String())

			if !dest.overlaps(src) {
				continue
			}

			add(
				"[PUT.%s] RemotePath %s and [DOWNLOAD.%s] RemotePath %s "+
					"are on the same server and may overlap; "+
					"uploads could be downloaded again (ping-pong)",
				pc.Category,
				pc.RemotePath.String(),
				dc.Category,
				dc.RemotePath.String(),
			)
		}
	}
}

// sameEndpoint 는 두 접속 설정이 같은 서버를 가리키는지 답한다.
// 호스트는 공백 제거·대소문자 무시로, 포트는 값으로 비교한다.
func sameEndpoint(a, b SFTPConfig) bool {
	return sameHost(a.Host, b.Host) && a.Port == b.Port
}

// sameHost 는 두 접속 호스트가 같은 서버 표기인지 답한다.
//
// 호스트명은 대소문자를 접는다. IP 는 net.ParseIP 로 맞춘 뒤, Go 가
// 거부하는 앞자리 0 IPv4(192.168.000.001)도 같은 주소로 본다.
// 한쪽만 그런 표기이면 문자열 비교는 순환을 놓친다.
// 대괄호로 감싼 IPv6([::1])도 벗긴 뒤 비교한다.
//
// localhost 와 127.0.0.1, 호스트명과 IP 는 여기서 묶지 않는다.
// 그 별칭은 한 인스턴스의 정적 검사로 보장하지 않는다.
func sameHost(a, b string) bool {
	ca := canonicalHost(a)
	cb := canonicalHost(b)

	if ia, ib := net.ParseIP(ca), net.ParseIP(cb); ia != nil && ib != nil {
		return ia.Equal(ib)
	}

	return strings.EqualFold(ca, cb)
}

func canonicalHost(host string) string {
	host = strings.TrimSpace(host)
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}

	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}

	if ip := parseIPv4Loose(host); ip != nil {
		return ip.String()
	}

	return host
}

// parseIPv4Loose 는 앞자리 0 이 있는 IPv4 만 받는다.
// net.ParseIP 는 그 표기를 거부하므로 Ping-Pong 비교에서만 보완한다.
func parseIPv4Loose(host string) net.IP {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return nil
	}

	var out [4]byte

	for i, p := range parts {
		if p == "" || len(p) > 3 {
			return nil
		}

		n := 0
		for _, c := range p {
			if c < '0' || c > '9' {
				return nil
			}

			n = n*10 + int(c-'0')
		}

		if n > 255 {
			return nil
		}

		out[i] = byte(n)
	}

	return net.IPv4(out[0], out[1], out[2], out[3]).To4()
}

// remoteScope 는 원격 경로 템플릿의 정적 범위다.
//
// 템플릿에서 첫 토큰 이전의 고정 prefix 만 본다. 토큰 이후는 날짜·관측소에
// 따라 달라지므로 정적으로 비교할 수 없고, 보수적으로 "그 아래 전부"로 본다.
//
//	/RNXOutgoing/(YYYY)/(DOY)/   → dirs [rnxoutgoing]          partial ""
//	/RNX/(YYYY)/(DOY)/           → dirs [rnx]                  partial ""
//	/data/rnx_(SITE)/(YYYY)/     → dirs [data]                 partial "rnx_"
//	/archive/rinex2/             → dirs [archive rinex2]       partial ""
//
// partial 은 토큰이 폴더 이름 중간에 붙어 마지막 단이 미완성일 때의
// 그 앞부분이다. 이 단은 "이 접두어로 시작하는 폴더들"을 뜻하므로
// 상대 단이 그 접두어로 시작하면 겹칠 수 있다고 본다.
//
// 대소문자는 접는다. SFTPGo 가 Windows 볼륨 위에 있으면 대소문자를
// 구분하지 않으므로, 구분한다고 가정하면 순환을 놓친다. 접어서 오탐이
// 나는 쪽이 놓치는 쪽보다 낫다 (보수적 거부).
type remoteScope struct {
	dirs    []string
	partial string
}

func remoteScopeOf(raw string) remoteScope {
	fixed, _, _ := strings.Cut(raw, "(")
	fixed = strings.ToLower(strings.ReplaceAll(fixed, "\\", "/"))

	trailingSlash := strings.HasSuffix(fixed, "/")

	// "." 은 건너뛰고 ".." 은 한 단 올라간다. 정규화하지 않으면
	// "/remote/./rinex2/" 가 "/remote/rinex2/" 와 다른 범위로 보여 같은
	// 폴더의 순환을 놓친다. 루트 위로는 올라가지 않는다.
	var dirs []string
	for _, seg := range strings.Split(fixed, "/") {
		switch seg {
		case "", ".":
		case "..":
			if len(dirs) > 0 {
				dirs = dirs[:len(dirs)-1]
			}
		default:
			dirs = append(dirs, seg)
		}
	}

	scope := remoteScope{dirs: dirs}

	// 토큰이 있고, 토큰 직전이 구분자가 아니면 마지막 단은 미완성이다.
	// 토큰이 없으면(완전 고정 경로) 마지막 단은 완성된 폴더 이름이다.
	if strings.Contains(raw, "(") && !trailingSlash && len(dirs) > 0 {
		scope.partial = dirs[len(dirs)-1]
		scope.dirs = dirs[:len(dirs)-1]
	}

	return scope
}

// overlaps 는 두 정적 범위가 같거나 한쪽이 다른 쪽을 포함할 수 있는지 답한다.
//
// 완성된 단을 앞에서부터 대조하다 다르면 겹치지 않는다. 완성된 단이 모두
// 같으면 짧은 쪽이 긴 쪽을 포함하므로 겹친다 — 단, 짧은 쪽에 미완성 단이
// 있으면 긴 쪽의 다음 단이 그 접두어로 시작할 때만 겹친다.
func (a remoteScope) overlaps(b remoteScope) bool {
	n := min(len(a.dirs), len(b.dirs))

	for i := 0; i < n; i++ {
		if a.dirs[i] != b.dirs[i] {
			return false
		}
	}

	switch {
	case len(a.dirs) < len(b.dirs):
		return a.partial == "" || strings.HasPrefix(b.dirs[n], a.partial)

	case len(b.dirs) < len(a.dirs):
		return b.partial == "" || strings.HasPrefix(a.dirs[n], b.partial)

	default:
		// 완성된 단의 깊이가 같다. 둘 다 미완성 단이 있으면 한쪽이
		// 다른 쪽의 접두어일 때만 겹친다. 한쪽이라도 미완성 단이 없으면
		// 그쪽이 이 깊이 전체를 뜻하므로 겹친다.
		if a.partial != "" && b.partial != "" {
			return strings.HasPrefix(a.partial, b.partial) ||
				strings.HasPrefix(b.partial, a.partial)
		}

		return true
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
	//
	// Mode=download 는 장부를 열지 않으므로 LedgerPath 가 우연히 적혀
	// 있어도 그 디렉터리를 요구하지 않는다.
	if c.judgesPut() && c.General.LedgerPath != "" {
		if err := requireDir(filepath.Dir(c.General.LedgerPath)); err != nil {
			add("[GENERAL] LedgerPath: %v", err)
		}
	}

	// lock.Acquire 는 lock 디렉터리는 만들지만 그 부모까지 만들지는 않는다.
	// 명시적 LockPath 의 부모는 여기서 본다. 생략한 PUT 의 파생 경로
	// (LedgerPath+".lock") 부모는 위 LedgerPath 검사와 같다.
	//
	// 자리는 명시 경로와 파생 경로 모두 본다. 파생 경로를 빼면
	// rinex_ledger.db.lock 이 일반 파일이거나 내용 있는 폴더일 때
	// 매 회차 Acquire 가 실패하거나 ErrHeld 로 조용히 끝난다.
	if c.General.LockPath != "" {
		if err := requireDir(filepath.Dir(c.General.LockPath)); err != nil {
			add("[GENERAL] LockPath: %v", err)
		}
	}

	if lockPath := c.LockFile(); lockPath != "" {
		if err := requireLockSlot(lockPath); err != nil {
			add("[GENERAL] LockPath: %v", err)
		}
	}

	// SFTP 를 실제 전송 계층으로 선택한 경우에만, 실행할 방향의
	// 개인키와 known_hosts 파일의 존재를 요구한다.
	if c.General.Transport == "sftp" {
		if c.judgesPut() {
			c.requireKeyFiles(add, "PUT.SFTP", c.Put.SFTP)
		}

		if c.General.Mode.DoesDownload() {
			c.requireKeyFiles(add, "DOWNLOAD.SFTP", c.Download.SFTP)
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

// requireKeyFiles 는 접속 섹션 하나의 개인키·known_hosts 실재를 검사한다.
func (c *Config) requireKeyFiles(add addFunc, section string, s SFTPConfig) {
	if err := requireFile(s.PrivateKey); err != nil {
		add("[%s] PrivateKey: %v", section, err)
	}

	if err := requireFile(s.KnownHosts); err != nil {
		add("[%s] KnownHosts: %v", section, err)
	}
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

// lock owner 이름 계약은 lock.newToken(128-bit → 32자리 hex)과 맞춘다.
// config 가 lock 을 import하지 않으므로 값과 형식을 여기서 검증하고,
// 실제 lock.Acquire를 사용하는 테스트로 두 패키지의 계약을 고정한다.
const (
	lockOwnerPrefix   = "owner-"
	lockOwnerHexChars = 32
)

// requireLockSlot 은 LockPath 가 lock 디렉터리로 쓸 수 있는 자리인지 본다.
//
//	없음                          → 정상 (Acquire 가 만든다)
//	디렉터리, owner-* 만 있거나 빔 → 정상 (지금 다른 회차가 쥐었거나 잔재)
//	일반 파일                      → 거부 (Acquire 가 매 회차 실패)
//	다른 파일이 든 디렉터리        → 거부 (data·logs 같은 기존 폴더를 적은 것)
//
// 마지막 경우를 그대로 두면 lock 패키지는 그 폴더를 "owner 없는 lock"
// 으로 관측한다. 폴더 mtime 이 30초보다 오래되면 비지 않은 폴더를 지우려다
// 매 회차 오류로 끝나고, 방금 파일이 생긴 폴더면 ErrHeld 로 조용히 exit 0
// 한다. 어느 쪽이든 전송이 멈추므로 시작 단계에서 원인을 알려 준다.
// RemoveAll 을 하지 않는 lock 패키지가 그 폴더 내용을 지우지는 않는다.
func requireLockSlot(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}

	if !info.IsDir() {
		return fmt.Errorf(
			"%q exists and is not a directory; "+
				"LockPath must name a lock directory the program creates by itself",
			path,
		)
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}

	if len(entries) > 1 {
		return fmt.Errorf(
			"%q contains multiple entries; a valid lock directory has at most one owner marker",
			path,
		)
	}

	for _, e := range entries {
		if !validLockOwnerEntry(e) {
			return fmt.Errorf(
				"%q is an existing directory with other content or invalid lock content (e.g. %q); "+
					"LockPath must name a dedicated lock directory such as data\\rinexclient.lock",
				path,
				e.Name(),
			)
		}
	}

	return nil
}

// validLockOwnerEntry 는 lock 패키지가 실제로 만드는 owner marker만 받는다.
// prefix만 보면 owner-notes.txt 같은 사용자 파일을 stale owner로 오인해
// lock.clearStale이 삭제할 수 있으므로 32자리 hex와 일반 파일까지 확인한다.
func validLockOwnerEntry(e os.DirEntry) bool {
	name := e.Name()
	if !strings.HasPrefix(name, lockOwnerPrefix) {
		return false
	}

	token := strings.TrimPrefix(name, lockOwnerPrefix)
	if len(token) != lockOwnerHexChars {
		return false
	}
	if token != strings.ToLower(token) {
		return false
	}

	if _, err := hex.DecodeString(token); err != nil {
		return false
	}

	info, err := e.Info()
	return err == nil && info.Mode().IsRegular()
}

// pathContains 는 parent와 child가 같거나 child가 parent 아래인지 답한다.
//
// LockPath가 LedgerPath나 Log.Dir의 부모여도 lock.Acquire가 일반 데이터
// 디렉터리를 lock 디렉터리로 오인하므로 정확한 일치만 보는 것으로 부족하다.
// filepath.Rel로 경계 단위를 보므로 "data"와 "database"는 포함 관계가
// 아니다. Windows 안전을 위해 비교는 대소문자를 접는다.
func pathContains(parent, child string) bool {
	parent = filepath.Clean(parent)
	child = filepath.Clean(child)

	if strings.EqualFold(parent, child) {
		return true
	}

	rel, err := filepath.Rel(parent, child)
	if err != nil || rel == "." || filepath.IsAbs(rel) {
		return false
	}

	first := rel
	if i := strings.IndexRune(rel, filepath.Separator); i >= 0 {
		first = rel[:i]
	}

	return !strings.EqualFold(first, "..")
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
