package config

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

// ErrMissingKey 는 필요한 섹션이나 키가 없을 때 반환된다.
var ErrMissingKey = errors.New("config: missing key")

// ErrBadValue 는 값을 해석할 수 없을 때 반환된다.
//
// "해석할 수 없다" 와 "값의 조합이 위험하다" 는 다른 문제이다.
// 후자는 validate.go 가 ErrInvalidConfig 로 판정한다.
var ErrBadValue = errors.New("config: bad value")

// ErrUnknownKey 는 정의되지 않은 섹션이나 키가 있을 때 반환된다.
//
// 조용히 무시하지 않는다. 오타 난 키는 무시되면 그 설정이 기본값으로
// 도는 것과 구분되지 않는다. ScanDay = 7 이 무시된 채 ScanDays 가
// 다른 값으로 도는 상황을 아무도 알아채지 못한다.
var ErrUnknownKey = errors.New("config: unknown key")

// configFileName 은 설정 파일의 기본 이름이다.
const configFileName = "config.ini"

// DefaultPath 는 실행파일과 같은 디렉터리의 config.ini 경로를 돌려준다.
//
// 현재 작업 디렉터리를 기준으로 하지 않는 이유는 스케줄러 때문이다.
// schtasks 는 작업 디렉터리를 실행파일 위치와 다르게 잡을 수 있어
// "설정 파일이 없다" 로 끝나는 사고가 흔하다.
func DefaultPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("config: locate executable: %w", err)
	}

	return filepath.Join(filepath.Dir(exe), configFileName), nil
}

// Load 는 설정 파일을 읽어 검증까지 마친 Config 를 돌려준다.
//
//	parseINI           문법
//	→ mapConfig        키 매핑
//	→ Validate         값 조합
//	→ CheckEnvironment 파일·디렉터리 실재
//
// 네 단계 중 하나라도 실패하면 실행하지 않는다.
//
// p 는 보호된 설정값을 해석하는 Protector 다.
// main 이 security.New() 로 배선한다.
// config 는 enc: 접두어, base64, DPAPI 같은 저장·보호 방식은 모른다.
func Load(path string, p Protector) (*Config, error) {
	configPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve path %q: %w", path, err)
	}

	f, err := parseINIFile(configPath)
	if err != nil {
		return nil, err
	}

	cfg, err := mapConfig(f, configPath, p)
	if err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if err := cfg.CheckEnvironment(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFrom 은 파일이 아닌 스트림에서 읽는다. 테스트에서 사용한다.
//
// CheckEnvironment 는 수행하지 않는다.
// 키·개인키 같은 실제 파일을 요구하지 않고 값 검증만 시험하기 위함이다.
//
// path 는 상대 경로 해석의 기준이 되는 가상의 config.ini 경로이다.
func LoadFrom(r io.Reader, path string, p Protector) (*Config, error) {
	configPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: resolve path %q: %w", path, err)
	}

	f, err := parseINI(r)
	if err != nil {
		return nil, err
	}

	cfg, err := mapConfig(f, configPath, p)
	if err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// loader 는 매핑 중 발생한 오류를 모은다.
//
// 첫 오류에서 멈추지 않는 이유는 손으로 편집하는 파일이기 때문이다.
// 한 번에 한 줄씩 고치고 다시 실행하는 것보다 전부 보여주는 편이 낫다.
type loader struct {
	file *iniFile
	path string
	errs []error

	// prot 은 보호된 설정값을 해석하는 Protector 다.
	// mapConfig 가 배선한다.
	prot Protector

	// encrypted 는 어떤 (섹션/키) 값이 보호된 값이었는지 기록한다.
	// 키는 settingID 로 정규화하여 [put.sftp] 와 [PUT.SFTP] 같은
	// 대소문자 차이가 동일한 설정으로 수렴하게 한다.
	//
	// 현재 [PUT.SFTP] Host/User/Port 평문 경고가 이 기록을 조회한다.
	encrypted map[string]bool

	// missing 은 파일에 없어서 대체 섹션을 만들어 준 섹션 이름이다.
	// 키는 foldKey 를 거친 값이다.
	//
	// 섹션 하나가 통째로 없으면 그 안의 키도 전부 없으므로,
	// 섹션 누락 1건에 키 누락 여러 건이 따라붙는다.
	// [PUT.<CATEGORY>] 지원 섹션이 전부 빠지면
	// (현재 6섹션 × 키 3개) 누락 오류가 대량으로 쌓여
	// "전부 보여준다" 는 의도가 오히려 원인을 가린다.
	//
	// 따라서 섹션 누락은 한 번만 알리고, 그 섹션에서 읽는 키는
	// 오류를 쌓지 않는다.
	missing map[string]bool
}

func (l *loader) addf(format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf(format, args...))
}

// err 는 모인 오류를 하나로 묶는다. 없으면 nil 이다.
func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}

	return fmt.Errorf("%s:\n%w", l.path, errors.Join(l.errs...))
}

// section 은 섹션을 찾는다. 없으면 오류를 기록하고 빈 섹션을 돌려준다.
//
// nil 을 돌려주지 않는 이유는 호출부가 매번 nil 검사를 하지 않게 하기 위함이다.
// 대신 이름을 missing 에 남겨, 그 섹션에서 키를 읽을 때 오류가
// 중복으로 쌓이지 않게 한다.
func (l *loader) section(name string) *iniSection {
	s, ok := l.file.section(name)
	if !ok {
		l.addf("%w: section [%s] not found", ErrMissingKey, name)
		l.missing[foldKey(name)] = true

		return &iniSection{
			name:  name,
			pairs: make(map[string]iniPair),
		}
	}

	return s
}

// absent 는 키를 읽을 수 없다는 사실을 기록한다.
//
// 섹션 자체가 없어서 생긴 키 누락은 이미 섹션 오류로 보고했으므로
// 다시 쌓지 않는다.
func (l *loader) absent(s *iniSection, key string) {
	if l.missing[foldKey(s.name)] {
		return
	}

	l.addf("%w: [%s] %s", ErrMissingKey, s.name, key)
}

// settingID 는 섹션/키 조합을 비교·추적하기 위한 정규화 ID 를 만든다.
//
// INI 의 섹션명과 키는 대소문자를 구분하지 않으므로,
// 파일 원문 표기와 관계없이 같은 설정은 같은 ID 로 수렴해야 한다.
func settingID(section, key string) string {
	return foldKey(section) + "/" + foldKey(key)
}

// value 는 필수 설정값 접근의 단일 통로다.
//
// 필수 타입 리더(str/intVal/boolVal/durationSeconds/template/mode)가
// s.get 대신 이 함수를 거친다. 보호 값의 해석을 이 한 곳에 두고,
// 부재 오류와 복호화 오류의 생산처도 여기로 모은다.
//
// 복호화가 타입 변환보다 먼저 수행되므로:
//
//	Port = enc:...
//
// 역시 평문 문자열로 복호화된 뒤 intVal 에서 정수로 변환된다.
//
// 복호화 실패 시 오류를 여기서 기록하고 ("", false) 를 반환한다.
// 호출 리더는 기본값만 반환하므로 복호화 오류 뒤에
// "not an integer" 같은 2차 오류가 중복으로 쌓이지 않는다.
//
// hourLayout 은 선택 키이므로 이 통로를 사용하지 않는다.
// 키 부재가 정상이며 DefaultHourLayout 을 사용해야 하기 때문이다.
// 현재 보안 요구 대상인 Host/User/Port에는 영향을 주지 않는다.
func (l *loader) value(s *iniSection, key string) (string, bool) {
	v, ok := s.get(key)
	if !ok {
		l.absent(s, key)

		return "", false
	}

	plain, wasEnc, err := l.prot.Resolve(v)
	if err != nil {
		l.addf(
			"%w: line %d: [%s] %s: %v",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			err,
		)

		return "", false
	}

	if wasEnc {
		l.encrypted[settingID(s.name, key)] = true
	}

	return plain, true
}

// str 은 문자열 값을 읽는다. 빈 값도 그대로 돌려준다.
//
// 비어 있으면 안 되는 키인지는 validate.go 가 판정한다.
func (l *loader) str(s *iniSection, key string) string {
	v, ok := l.value(s, key)
	if !ok {
		return ""
	}

	return v
}

// intVal 은 정수 값을 읽는다.
func (l *loader) intVal(s *iniSection, key string) int {
	v, ok := l.value(s, key)
	if !ok {
		return 0
	}

	n, err := strconv.Atoi(v)
	if err != nil {
		l.addf(
			"%w: line %d: [%s] %s = %q is not an integer",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			v,
		)

		return 0
	}

	return n
}

// boolVal 은 참·거짓 값을 읽는다.
//
// true/false 외에 yes/no, on/off, 1/0 을 허용한다.
// 전부 뜻이 분명한 표기이므로 관대해도 조용히 틀릴 여지가 없다.
func (l *loader) boolVal(s *iniSection, key string) bool {
	v, ok := l.value(s, key)
	if !ok {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "yes", "on", "1":
		return true

	case "false", "no", "off", "0":
		return false

	default:
		l.addf(
			"%w: line %d: [%s] %s = %q is not a boolean "+
				"(use true/false, yes/no, on/off, 1/0)",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			v,
		)

		return false
	}
}

// durationSeconds 는 초 단위 정수를 time.Duration 으로 읽는다.
//
// 단순히 time.Duration(seconds) * time.Second 로 변환하면
// 지나치게 큰 값에서 overflow 되어 음수나 엉뚱한 값으로 바뀔 수 있다.
//
// 음수 자체는 여기서 거부하지 않는다.
// 숫자로서 읽을 수 있으므로 Load 는 성공시키고,
// GraceSeconds < 0 이 위험한 설정이라는 판정은 validate.go 가 담당한다.
func (l *loader) durationSeconds(s *iniSection, key string) time.Duration {
	v, ok := l.value(s, key)
	if !ok {
		return 0
	}

	seconds, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		l.addf(
			"%w: line %d: [%s] %s = %q is not an integer",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			v,
		)

		return 0
	}

	const nanosPerSecond = int64(time.Second)

	maxSeconds := int64(math.MaxInt64) / nanosPerSecond
	minSeconds := int64(math.MinInt64) / nanosPerSecond

	if seconds > maxSeconds || seconds < minSeconds {
		l.addf(
			"%w: line %d: [%s] %s = %q is outside time.Duration range",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			v,
		)

		return 0
	}

	return time.Duration(seconds) * time.Second
}

// template 은 경로 템플릿을 읽어 파싱한다.
//
// 문자열이 아니라 파싱 결과를 들고 있어야 스캔 도중이 아니라
// 시작 시점에 문법 오류가 드러난다.
func (l *loader) template(s *iniSection, key string) *pathpl.Template {
	v, ok := l.value(s, key)
	if !ok {
		return nil
	}

	tpl, err := pathpl.Parse(v)
	if err != nil {
		l.addf(
			"%w: line %d: [%s] %s: %v",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			err,
		)

		return nil
	}

	return tpl
}

// hourLayout 은 HourLayout 값을 읽는다. Hourly 섹션에서만 호출한다.
//
// 선택 키다. 다른 필수 키와 달리 누락 시 absent 를 호출하지 않고
// DefaultHourLayout(dir) 을 반환한다. 이는 기존 동작을 유지하기 위함이다.
//
// 이 리더는 l.value 를 거치지 않는다. 키 부재가 정상인 선택값이므로
// 필수 값용 value() 를 사용하면 누락 오류가 발생하기 때문이다.
//
// 현재 암호화 요구 대상은 [PUT.SFTP] Host/User/Port 이므로
// HourLayout 에 보호 값 해석을 적용하지 않는다.
//
// flat 은 항상 명시해야 하므로 생략이 조용히 평면으로 바뀌지 않는다.
// 평면 배포에서 이 키를 빠뜨리면 LocalPath 에 (HH) 가 없어
// validate 가 "dir 인데 (HH) 없음" 으로 거부한다.
//
// 키가 있는데 값이 dir/flat 이 아니면(빈 값 포함) ErrBadValue 다.
func (l *loader) hourLayout(s *iniSection, key string) HourLayout {
	v, ok := s.get(key)
	if !ok {
		return DefaultHourLayout
	}

	h, err := ParseHourLayout(v)
	if err != nil {
		l.addf(
			"%w: line %d: [%s] %s: %v",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			err,
		)

		return DefaultHourLayout
	}

	return h
}

// resolvePath 는 config.ini 에 적힌 일반 파일시스템 경로를 확정한다.
//
// 절대 경로는 그대로 사용하고,
// 상대 경로는 현재 작업 디렉터리(cwd)가 아니라
// config.ini 가 있는 디렉터리를 기준으로 해석한다.
//
// Path Template(LocalPath / RemotePath)에는 사용하지 않는다.
// 그것들은 pathpl.Template 로 별도 처리한다.
func resolvePath(configPath, value string) string {
	if value == "" {
		return ""
	}

	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}

	return filepath.Clean(
		filepath.Join(filepath.Dir(configPath), value),
	)
}

// mapConfig 는 파싱된 INI 를 Config 로 옮긴다.
//
// 값의 의미는 판정하지 않는다. 읽을 수 있는지만 본다.
//
// 단, 상대경로 → config.ini 기준 절대경로 변환과
// 초 → time.Duration 변환처럼 타입 자체를 확정하기 위해 필요한 변환은
// 이 단계에서 수행한다.
//
// p 는 Load/LoadFrom 이 관통 배선한다.
// 여기서 nil 을 한 번에 잡아 두 진입점 모두를 보호한다.
func mapConfig(f *iniFile, path string, p Protector) (*Config, error) {
	if p == nil {
		return nil, fmt.Errorf("config: Protector 가 nil 이다 (배선 누락)")
	}

	l := &loader{
		file:      f,
		path:      path,
		prot:      p,
		encrypted: make(map[string]bool),
		missing:   make(map[string]bool),
	}

	l.checkUnknown()

	cfg := &Config{
		Path: path,
	}

	general := l.section("GENERAL")
	cfg.General.Mode = l.mode(general, "Mode")
	cfg.General.Transport = strings.ToLower(
		strings.TrimSpace(l.str(general, "Transport")),
	)
	cfg.General.RepostDownloaded = l.boolVal(general, "RepostDownloaded")
	cfg.General.LedgerPath = resolvePath(
		path,
		l.str(general, "LedgerPath"),
	)
	cfg.General.LockStale = l.durationSeconds(general, "LockStaleSeconds")

	scan := l.section("SCAN")
	cfg.Scan.RecentDays = l.intVal(scan, "ScanRecentDays")
	cfg.Scan.Days = l.intVal(scan, "ScanDays")
	cfg.Scan.DeepScanHour = l.intVal(scan, "DeepScanHour")
	cfg.Scan.UseDirMtimeSkip = l.boolVal(scan, "UseDirMtimeSkip")

	ingress := l.section("INGRESS")
	cfg.Ingress.Grace = l.durationSeconds(ingress, "GraceSeconds")

	ledger := l.section("LEDGER")
	cfg.Ledger.RetentionDays = l.intVal(ledger, "RetentionDays")

	put := l.section("PUT")
	cfg.Put.MaxWorkers = l.intVal(put, "MaxWorkers")
	cfg.Put.MaxRetries = l.intVal(put, "MaxRetries")
	cfg.Put.MaxFilesPerRun = l.intVal(put, "MaxFilesPerRun")

	sftp := l.section("PUT.SFTP")

	// Log.Level 과 같이 소문자로 정규화한다.
	// 손으로 쓰는 값이므로 publickey / PublicKey / PUBLICKEY 가 섞인다.
	// validate.go 는 정규화된 값만 보고 판정한다.
	cfg.Put.SFTP.AuthMethod = strings.ToLower(l.str(sftp, "AuthMethod"))
	cfg.Put.SFTP.Host = l.str(sftp, "Host")
	cfg.Put.SFTP.Port = l.intVal(sftp, "Port")
	cfg.Put.SFTP.User = l.str(sftp, "User")
	cfg.Put.SFTP.PrivateKey = resolvePath(
		path,
		l.str(sftp, "PrivateKey"),
	)
	cfg.Put.SFTP.KnownHosts = resolvePath(
		path,
		l.str(sftp, "KnownHosts"),
	)

	// 평문 접속 설정 경고.
	//
	// "어떤 키가 기관의 평문 금지 대상인가"라는 정책은 이 매핑부가 안다.
	// 범용 복호화인 l.value 는 개별 키의 보안 정책을 알지 않는다.
	//
	// Transport=sftp 일 때만 경고한다.
	// mapConfig 는 [PUT.SFTP]를 Transport 분기 없이 읽으므로,
	// 조건이 없으면 localfs 검증에서도 불필요한 경고가 발생한다.
	//
	// settingID 를 사용하므로 [put.sftp], [PUT.SFTP] 같은
	// 대소문자 차이와 관계없이 같은 설정으로 판정한다.
	//
	// 평문을 거부하지 않고 경고만 하는 이유는 개발·테스트 환경에서는
	// 평문 config 사용을 허용하기 때문이다.
	if cfg.General.Transport == "sftp" {
		for _, key := range []string{"Host", "User", "Port"} {
			if !l.encrypted[settingID("PUT.SFTP", key)] {
				cfg.Warnings = append(
					cfg.Warnings,
					fmt.Sprintf(
						"[PUT.SFTP] %s 가 평문으로 저장되어 있다 — "+
							"rinexclient.exe secure-set 으로 암호화를 권장한다",
						key,
					),
				)
			}
		}
	}

	cfg.Put.Categories = l.categories()

	logSec := l.section("LOG")
	cfg.Log.Level = strings.ToLower(l.str(logSec, "Level"))
	cfg.Log.Dir = resolvePath(
		path,
		l.str(logSec, "Dir"),
	)
	cfg.Log.RetentionDays = l.intVal(logSec, "RetentionDays")

	if err := l.err(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// mode 는 Mode 값을 읽는다.
// domain 이 공백과 대소문자를 흡수한다.
func (l *loader) mode(s *iniSection, key string) domain.Mode {
	v, ok := l.value(s, key)
	if !ok {
		return ""
	}

	m, err := domain.ParseMode(v)
	if err != nil {
		l.addf(
			"%w: line %d: [%s] %s: %v",
			ErrBadValue,
			s.lineOf(key),
			s.name,
			key,
			err,
		)

		return ""
	}

	return m
}

// categories 는 [PUT.<CATEGORY>] 섹션을 domain.Categories() 순서대로 읽는다.
//
// 지원 Category 섹션이 모두 있어야 한다.
// 빠뜨린 것을 "꺼진 것" 으로 해석하지 않는다.
// 끄려면 Enabled = false 를 명시한다.
// 그래야 설정 파일만 보고 어떤 Category 를 다루는 인스턴스인지 알 수 있다.
//
// Enabled 가 false 여도 경로 템플릿은 파싱한다.
// 나중에 켤 때 오타가 그때 드러나는 것을 막는다.
func (l *loader) categories() []CategoryConfig {
	cats := domain.Categories()
	out := make([]CategoryConfig, 0, len(cats))

	for _, cat := range cats {
		name := "PUT." + cat.String()
		s := l.section(name)

		cc := CategoryConfig{
			Category:   cat,
			Enabled:    l.boolVal(s, "Enabled"),
			LocalPath:  l.template(s, "LocalPath"),
			RemotePath: l.template(s, "RemotePath"),
		}

		// HourLayout 은 Hourly 섹션에서만 읽는다(선택 키, 기본 dir).
		// Daily 는 배치 개념이 없으므로 읽지 않으며 zero-value("") 로 둔다.
		// Daily 섹션의 HourLayout 키는 knownKeys 에서 허용하지 않으므로
		// 적으면 ErrUnknownKey 로 거부된다.
		if cat.IsHourly() {
			cc.HourLayout = l.hourLayout(s, "HourLayout")
		}

		out = append(out, cc)
	}

	return out
}

// knownKeys 는 섹션별로 허용되는 키 목록이다.
//
// 이 표가 config.example.ini 와 어긋나면 두 가지 사고가 난다.
// 여기에 없는 키를 example 에 적으면 실행이 거부되고,
// 여기에 있는 키를 example 에서 빠뜨리면 새 서버 설치 때 누락된다.
//
// config_test.go 가 example 파일로 이 표를 대조한다.
func knownKeys() map[string][]string {
	m := map[string][]string{
		"GENERAL": {
			"Mode",
			"Transport",
			"RepostDownloaded",
			"LedgerPath",
			"LockStaleSeconds",
		},
		"SCAN": {
			"ScanRecentDays",
			"ScanDays",
			"DeepScanHour",
			"UseDirMtimeSkip",
		},
		"INGRESS": {
			"GraceSeconds",
		},
		"LEDGER": {
			"RetentionDays",
		},
		"PUT": {
			"MaxWorkers",
			"MaxRetries",
			"MaxFilesPerRun",
		},
		"PUT.SFTP": {
			"AuthMethod",
			"Host",
			"Port",
			"User",
			"PrivateKey",
			"KnownHosts",
		},
		"LOG": {
			"Level",
			"Dir",
			"RetentionDays",
		},
	}

	for _, cat := range domain.Categories() {
		keys := []string{
			"Enabled",
			"LocalPath",
			"RemotePath",
		}

		// HourLayout 은 Hourly 섹션에서만 의미가 있다.
		// Daily 섹션에 적으면 ErrUnknownKey 로 거부되어,
		// "Daily 에 배치를 지정하려 한" 혼동을 시작 시 드러낸다.
		if cat.IsHourly() {
			keys = append(keys, "HourLayout")
		}

		m["PUT."+cat.String()] = keys
	}

	return m
}

// checkUnknown 은 정의되지 않은 섹션과 키를 오류로 기록한다.
//
// 오타를 조용히 넘기지 않기 위한 검사이다.
// 값을 읽는 쪽에서는 "그 키가 없다" 로만 보이므로 원인을 알 수 없다.
//
// 비교는 foldKey 를 거쳐 한다.
// iniSection.keys 는 사용자가 파일에서 찾을 수 있도록 원래 표기를 돌려주므로,
// 그 값을 그대로 대조하면 ScanDays 조차 알 수 없는 키로 판정된다.
// 대조는 folded, 보고는 원래 표기로 나눈다.
func (l *loader) checkUnknown() {
	known := knownKeys()

	for _, name := range l.file.sectionNames() {
		folded := foldKey(name)

		keys, ok := known[folded]
		if !ok {
			l.addf(
				"%w: unknown section [%s]",
				ErrUnknownKey,
				name,
			)

			continue
		}

		allowed := make(map[string]bool, len(keys))
		for _, key := range keys {
			allowed[foldKey(key)] = true
		}

		sec, _ := l.file.section(name)

		for _, key := range sec.keys() {
			if allowed[foldKey(key)] {
				continue
			}

			l.addf(
				"%w: line %d: [%s] has unknown key %q",
				ErrUnknownKey,
				sec.lineOf(key),
				name,
				key,
			)
		}
	}
}
