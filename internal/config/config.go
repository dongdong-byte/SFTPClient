package config

import (
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
)

// Protector 는 config 가 필요로 하는 보호 설정값 해석 능력의 선언이다.
//
// 인터페이스는 사용하는 쪽(config)이 선언한다.
// 구현은 internal/security 가 제공하고 main 이 배선한다.
// config 는 enc: 접두어, base64, DPAPI 같은 저장·보호 방식은 모른다.
type Protector interface {
	// Resolve 는 보호된 값이면 평문으로 풀어 (평문, true, nil) 을,
	// 일반 값이면 그대로 (값, false, nil) 을 반환한다.
	Resolve(value string) (plain string, wasEncrypted bool, err error)
}

// Config 는 실행에 필요한 설정 전부이다.
//
// 실행 중 다시 읽지 않는다. Hot Reload 를 두지 않는 이유는,
// 스캔 도중에 경로나 범위가 바뀌면 그 실행의 결과가 어느 설정으로 나온 것인지
// 사후에 판정하기 어렵기 때문이다.
//
// 변경은 프로그램 종료 → config.ini 수정 → 재시작을 운영 원칙으로 한다.
type Config struct {
	// Path 는 이 설정을 읽어들인 config.ini 의 경로이다.
	// 로그와 오류 메시지, 상대 경로 해석 기준에 사용한다.
	Path string

	General GeneralConfig
	Scan    ScanConfig
	Ingress IngressConfig
	Ledger  LedgerConfig
	Put     PutConfig
	Log     LogConfig

	// Warnings 는 실행을 막지는 않지만 운영자가 알아야 하는 사항이다.
	// main 이 시작 시 [WARN] 으로 기록한다.
	// 현재 유일한 생산처: [PUT.SFTP] Host/User/Port 의 평문 저장.
	Warnings []string
}

// GeneralConfig 는 [GENERAL] 섹션이다.
type GeneralConfig struct {
	// Mode 는 이 인스턴스가 수행할 방향이다.
	//
	// domain.Mode 는 PUT / DOWNLOAD / BOTH 를 모두 유효한 도메인 값으로 두지만,
	// 현재 실행파일에서 실제로 사용할 수 있는 Mode 인지는 validate.go 가 판정한다.
	//
	// MVP 1 에서는 PUT 만 허용한다.
	Mode domain.Mode

	// Transport 는 실행에 사용할 전송 계층이다.
	// 지원 값은 "sftp"(운영)와 "localfs"(검증)이다.
	//
	// config.ini 에 반드시 명시하며 기본값은 두지 않는다.
	// --transport 가 지정되면 해당 실행에 한해 이 값을 덮어쓴다.
	Transport string

	// RepostDownloaded 는 DOWNLOAD 로 수신한 파일을 다시 PUT 대상으로 삼을지이다.
	//
	// 기본값은 false 이다.
	// BOTH 중계 구성에서만 true 로 사용할 수 있으며,
	// 해당 구성의 경로 중첩 검사는 BOTH 구현 시 활성화한다.
	RepostDownloaded bool

	// LedgerPath 는 Ledger DB 파일 경로이다.
	//
	// SQLite Ledger 는 프로그램이 실행되는 서버의 로컬 디스크에 두어야 한다.
	// RINEX 원본 파일은 SMB/NAS 에 있어도 되지만 Ledger DB 를 네트워크 공유에
	// 두는 것은 허용하지 않는다.
	//
	// 상대 경로는 load.go 가 config.ini 가 있는 디렉터리를 기준으로 해석하며,
	// 이 필드에는 해석이 끝난 값이 담긴다.
	// 호출부가 다시 해석하지 않는다. 네 곳(LedgerPath, PrivateKey, KnownHosts,
	// Log.Dir)이 같은 규칙을 쓰므로, 해석 시점을 한 곳으로 모아 둔다.
	LedgerPath string

	// LockStale 는 기존 lock 의 owner marker 가 이 나이를 넘으면
	// 직전 실행의 비정상 종료 잔재로 판정할 수 있게 하는 문턱이다.
	//
	// config.ini 에서는 LockStaleSeconds 라는 초 단위 정수로 입력하지만,
	// 로드 시점에 time.Duration 으로 변환하여 보관한다. Grace 와 같은
	// 방침이다 — 초 단위 정수 int 를 그대로 두면 사용처마다
	// time.Second 곱셈이 반복되고 한 곳은 빠뜨린다.
	//
	// 너무 짧으면: 아직 살아 있는 정당한 장기 실행(느린 회선 위
	// 대량 전송 회차)의 lock 을 탈취하여 이중 실행이 된다.
	// 너무 길면: 크래시 후 그 시간 동안 매시 실행이 거부되어
	// 전송이 조용히 멈춘다. validate 가 600초~24시간 범위를 강제한다.
	//
	// 단위 결정 (2026-08-30 확정) — Seconds 채택, Minutes 기각.
	// Minutes 안의 이득은 두 가지였다: ① ini 에서 180 이 3시간임이 바로
	// 읽힌다, ② GraceSeconds 감각으로 작은 값을 넣는 오입력이 안전한
	// 방향(분)으로 틀린다. 그러나 ①은 example.ini 의 환산 주석 한 줄로,
	// ②는 validate 하한 600초로 완전히 대체된다. 반면 Seconds 의 이득
	// (경과시간 키의 단위 규약을 GraceSeconds 와 함께 초 하나로 유지)은
	// Minutes 로는 대체할 수 없다. 대체 가능한 이득과 불가능한 이득이
	// 붙으면 후자가 이긴다. 참고 — ScanRecentDays·DeepScanHour 는
	// 달력 좌표이고 이 키와 GraceSeconds 는 경과시간이다. 계열이 다르므로
	// "키마다 단위가 제각각" 반례가 아니다. 이 단락은 같은 논쟁의
	// 세 번째 재론을 막기 위해 남긴다.
	LockStale time.Duration
}

// ScanConfig 는 [SCAN] 섹션이다.
type ScanConfig struct {
	// RecentDays 는 Hot Scan 범위(일)이다.
	// 정상 정기 실행마다 최근 이 기간을 탐색한다.
	//
	// config.ini 의 키 이름은 ScanRecentDays 이다.
	// 섹션명이 이미 SCAN 이므로 필드에서는 접두어를 뺀다.
	//
	// 기본값은 2일이다.
	RecentDays int

	// Days 는 Deep Scan 범위(일)이다.
	// 하루 1회 최근 이 기간 전체를 탐색한다.
	//
	// config.ini 의 키 이름은 ScanDays 이다.
	//
	// 기본값은 7일이다.
	// 정기 자동 탐색은 이 범위를 기준으로 하며,
	// 이 범위를 넘어선 과거 데이터 처리는 운영자가 별도로 판단한다.
	Days int

	// DeepScanHour 는 Deep Scan 을 수행할 시각(0~23)이다.
	//
	// 서버 로컬시간 기준이다. UTC 가 아니다.
	// 경로 토큰의 날짜·시각 계산만 UTC 를 사용한다.
	//
	// 두 기준이 다른 이유는 대상이 다르기 때문이다.
	// 경로는 관측 시각을 따르므로 UTC 이고,
	// 이 값은 "전송량이 적은 시간대" 라는 운영 개념이자 스케줄러와 맞물리므로
	// 로컬시간이다. UTC 로 두면 4가 KST 13시(한낮)가 되어 값과 의도가 어긋난다.
	//
	// 별도의 scan_state 테이블을 두지 않고,
	// 현재 실행 시각과 이 값을 비교하여 Deep Scan 여부를 결정한다.
	DeepScanHour int

	// UseDirMtimeSkip 은 디렉터리 mtime 을 이용하여
	// 내부 탐색을 생략할지 여부이다.
	//
	// 현재 설계에서는 false 를 사용한다.
	// 디렉터리 mtime 은 파일 생성뿐 아니라 삭제·정리 작업에서도 바뀔 수 있어
	// 오래된 디렉터리를 신규 데이터가 있는 것처럼 판단할 위험이 있다.
	// 로컬 보존이 10년이므로 정리 작업이 돌면 그 디렉터리가 통째로 스캔 대상이 되고,
	// 장부에 없는 파일이 전량 재전송된다.
	UseDirMtimeSkip bool
}

// IngressConfig 는 [INGRESS] 섹션이다.
type IngressConfig struct {
	// Grace 는 파일의 mtime 이 현재 시각으로부터 이만큼 지난 경우에만
	// Ingress 후보로 인정하기 위한 시간이다.
	//
	// config.ini 에서는 GraceSeconds 라는 초 단위 정수로 입력하지만,
	// Config 내부에서는 time.Duration 으로 보유한다.
	// 이후 호출부가 단위를 다시 해석하지 않게 하기 위함이다.
	//
	// Grace == 0 이면 mtime 나이 검사를 사용하지 않는다.
	// 음수는 validate.go 가 거부한다.
	//
	// Grace 는 전송 중이거나 최근 변경된 파일을 너무 빨리 처리하는 것을
	// 줄이는 방어선이다. 0바이트 파일은 별도의 size > 0 검증이 막는다.
	//
	// 이미 전송된 파일이 이후 size 또는 mtime 이 달라지면 Ledger 의
	// revision 증가가 최종 회수 장치로 동작한다.
	Grace time.Duration
}

// LedgerConfig 는 [LEDGER] 섹션이다.
type LedgerConfig struct {
	// RetentionDays 는 Ledger 행 보존기간(일)이다.
	//
	// 반드시 Scan.Days 보다 길어야 한다.
	// 그렇지 않으면 디스크에는 파일이 남아 있는데 Ledger 에서만 행이 삭제되어
	// 이후 Deep Scan 에서 신규 파일로 판정되고 재전송될 수 있다.
	//
	// 따라서 이 값은 프로그램이 과거 파일의 전송 이력을 신뢰할 수 있는
	// 최소 보존 범위를 결정한다.
	RetentionDays int
}

// PutConfig 는 [PUT] 과 그 하위 섹션의 설정이다.
type PutConfig struct {
	// MaxWorkers 는 PUT 전송 병렬도이다.
	//
	// Scan 은 항상 순차로 수행한다.
	// 전송만 bounded Worker Pool 로 병렬 처리하며 MVP 1 기본값은 4이다.
	MaxWorkers int

	// MaxRetries 는 동일 (category, file_name, revision) 에 허용하는
	// 누적 자동 전송 시도 상한이다. 첫 시도를 포함한다.
	// MaxRetries = 5 면 총 5회까지 자동 시도한다.
	//
	// 의미 재정의 (2026-08-30 확정) — 이전 의미는 "같은 실행 안에서
	// N번 재시도"였다. 폐기 이유: 프로그램은 매시 실행되므로 스케줄러가
	// 이미 1시간 간격 retry 를 제공한다. 실행 안 재시도는 backoff 장치를
	// 요구하고, 일시 장애(회선)에는 1시간 뒤가 지금 3초 뒤보다 낫다.
	// 권한 오류·잘못된 RemotePath 같은 영구 실패는 몇 번을 시도해도
	// 해결되지 않으므로, 누적 5회면 자동 복구가 아니라 운영 장애다.
	// 상한 도달 항목은 FAILED 로 남고 자동 후보에서 빠진다.
	// (db failed put 으로 관측 — 설계안 9.2)
	//
	// 이름을 MaxAttempts 로 바꾸지 않는 이유: 의미상 더 정확하지만
	// 키 개명은 변경 범위만 키운다. 주석과 example.ini 로 못박는다.
	// (2026-08-30 필드명을 ini 키와 일치시키기 위해 MaxAttempts 에서 통일함)
	MaxRetries int

	// MaxFilesPerRun 은 한 번의 실행에서 실제 전송할 최대 파일 수이다.
	// 0 이면 제한하지 않는다.
	//
	// seed 누락, 대량 복사에 따른 mtime 일괄 변경 등으로
	// 예상보다 많은 파일이 한 번에 전송 대상으로 선정될 경우
	// 회선과 대상 서버를 보호하기 위한 최후 방어선이다.
	MaxFilesPerRun int

	// SFTP 는 [PUT.SFTP] 접속 설정이다.
	SFTP SFTPConfig

	// Categories 는 PUT Category 설정 전체이다.
	//
	// domain.Categories() 와 같은 순서로 채우며,
	// Enabled=false 인 Category 도 포함한다.
	//
	// 비활성 Category 의 경로도 Load 단계에서 Path Template 문법 자체는
	// 검사하여 잘못된 config.ini 가 조용히 남지 않게 한다.
	Categories []CategoryConfig
}

// SFTPConfig 는 [PUT.SFTP] 섹션이다.
//
// 접속 정보를 방향([PUT] / [DOWNLOAD]) 아래에 두는 이유는
// 향후 BOTH 모드에서 송신 서버와 수신 서버가 서로 다를 수 있기 때문이다.
type SFTPConfig struct {
	// AuthMethod 는 인증 방식이다.
	// MVP 1 에서는 publickey 만 허용하며 validate.go 가 그 외를 거부한다.
	AuthMethod string

	// Host / Port / User 는 config.ini 에서 enc: 보호 값을 지원한다.
	// 복호화는 load 의 value() 가 수행하며, 이 필드에는 평문이 담긴다.
	// 따라서 이 값들을 로그·오류 메시지에 그대로 찍지 않도록 주의한다.
	Host string
	Port int
	User string

	// PrivateKey 는 SSH 개인키 파일 경로이다.
	// 설정 파일에 평문 비밀번호를 저장하지 않는다.
	//
	// LedgerPath 와 같이 load.go 에서 해석이 끝난 절대 경로가 담긴다.
	PrivateKey string

	// KnownHosts 는 SSH host key 검증 파일 경로이다.
	//
	// 비어 있거나 파일이 존재하지 않으면 시작 시 거부한다.
	// host key 검증을 비활성화하는 실행 경로는 두지 않는다.
	//
	// LedgerPath 와 같이 load.go 에서 해석이 끝난 절대 경로가 담긴다.
	KnownHosts string
}

// CategoryConfig 는 [PUT.<CATEGORY>] 섹션 하나의 설정이다.
type CategoryConfig struct {
	// Category 는 이 섹션이 담당하는 RINEX Category 이다.
	//
	// Scanner 가 파일명에서 Category 를 추론하지 않는다.
	// config.ini 의 섹션명이 Category 를 결정하고,
	// verify 단계에서 파일명이 그 Category 와 일치하는지 대조한다.
	Category domain.Category

	// Enabled 가 false 이면 정기 Scan 대상에서 제외한다.
	Enabled bool

	// LocalPath 와 RemotePath 는 문자열이 아니라
	// 시작 시 파싱이 끝난 Path Template 이다.
	//
	// 문법 오류를 Scan 도중이 아니라 프로그램 시작 시 발견하고,
	// 실행 중에는 Parse 를 반복하지 않고 Expand 만 수행한다.
	//
	// Hourly Category 의 LocalPath (HH) 사용 여부는 HourLayout 이 결정한다.
	//
	//	HourLayout=dir  → LocalPath 에 (HH) 필수
	//	HourLayout=flat → LocalPath 에 (HH) 금지
	//
	// RemotePath 는 Hourly 에서 (HH) 유무를 강제하지 않는다.
	// 소스 dir + 목적지 flat 조합(서울시 Hourly → /RNX2/)을 허용하기 위함이다.
	//
	// Daily Category 는 LocalPath / RemotePath 모두 (HH) 를 포함하면 안 된다.
	// 선언과 LocalPath 의 일치는 validate.go 가 판정한다.
	//
	// 두 필드 모두 nil 이 아님이 Load 성공의 조건이다.
	// Expand 는 포인터 리시버이므로 nil 이면 호출 시점에 panic 이 된다.
	LocalPath  *pathpl.Template
	RemotePath *pathpl.Template

	// HourLayout 은 Hourly Category 의 디렉터리 배치이다 (dir | flat).
	//
	// Hourly 섹션에서만 읽는다. load 가 생략 시 DefaultHourLayout(dir) 로
	// 채운다. Daily Category 에서는 사용하지 않으며 zero-value("") 로 남는다.
	//
	// 값은 LocalPath 스캔 배치에만 적용된다. RemotePath 의 (HH) 유무는
	// 이 키와 독립이다 (소스 dir + 목적지 flat 허용, 2026-09-02).
	//
	// scan 은 이 값을 직접 보지 않고 LocalPath 의 (HH) 토큰 유무로 순회를
	// 결정한다. HourLayout 은 dir/flat 선언과 LocalPath 의 (HH) 유무가
	// 일치하는지 validate 가 시작 시 교차 검증하는 데 쓰인다.
	HourLayout HourLayout
}

// LogConfig 는 [LOG] 섹션이다.
type LogConfig struct {
	// Level 은 debug / info / warn / error 중 하나이다.
	Level string

	// Dir 은 로그 파일 저장 디렉터리이다.
	// LedgerPath 와 같이 load.go 에서 해석이 끝난 절대 경로가 담긴다.
	Dir string

	// RetentionDays 는 로그 파일 보존기간(일)이다.
	// Ledger.RetentionDays 와는 별개의 값이다.
	RetentionDays int
}

// LogLevels 는 지원하는 로그 수준을 돌려준다.
//
// 반환 슬라이스를 호출자가 수정해도 다른 호출에 영향을 주지 않도록
// 매번 새로운 슬라이스를 반환한다.
func LogLevels() []string {
	return []string{
		"debug",
		"info",
		"warn",
		"error",
	}
}

// EnabledCategories 는 Enabled 인 PUT Category 설정만
// 선언 순서대로 돌려준다.
//
// 실행 흐름에서 Categories 를 직접 순회하면 Enabled 검사를 빠뜨려
// 비활성 Category 를 스캔할 수 있으므로 이 함수를 사용한다.
//
// 수신자가 PutConfig 인 이유는 이 함수가 Put 밖의 값을 보지 않기 때문이다.
// Lookup 과 나란히 두어 Category 조회 경로를 한곳에 모은다.
func (p PutConfig) EnabledCategories() []CategoryConfig {
	out := make([]CategoryConfig, 0, len(p.Categories))

	for _, cc := range p.Categories {
		if cc.Enabled {
			out = append(out, cc)
		}
	}

	return out
}

// Lookup 은 해당 Category 의 PUT 설정을 찾는다.
//
// 존재하지 않으면 false 를 돌려준다.
// Enabled 여부는 보지 않는다. 비활성 Category 도 설정 자체는 존재한다.
//
// 이름을 Category 로 두지 않는 이유는 CategoryConfig.Category 필드와 섞여
// 호출부에서 cfg.Put.Category(...).Category 같은 표기가 나오기 때문이다.
func (p PutConfig) Lookup(cat domain.Category) (CategoryConfig, bool) {
	for _, cc := range p.Categories {
		if cc.Category == cat {
			return cc, true
		}
	}

	return CategoryConfig{}, false
}
