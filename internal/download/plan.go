package download

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// DOWNLOAD 커밋 4 — 순수 후보·기존 대상 판정 (DOWNLOAD_COMMIT_PLAN v1 §5).
//
// 이 파일은 네트워크도 실제 파일 I/O 도 하지 않는다. 원격 Entry 하나를
// "받을 후보" 또는 "제외 사유"로, 로컬 최종 경로의 관측 하나를 "받음 /
// 기존 / 충돌 / 비정상 대상"으로 나누는 규칙만 고정한다. 실제 Stat·수신·
// Rename 은 러너(커밋 5)가 LocalStore·RemoteFS 로 한다.

// Reason 은 DOWNLOAD 파일 하나의 처리 결과 분류다.
//
// 값은 DOWNLOAD v3 §10.1 의 내부 키다. 한국어 표시명과 출력 순서는 보고
// 포맷(커밋 6)이 소유한다. 여기서는 집계 키로만 쓴다.
//
// PUT 의 verify.Reason 을 그대로 쓰지 않는 이유: DOWNLOAD 에는 verify 에
// 없는 사유(사이트·목적지·충돌)가 있고, verify 의 새 사유가 DOWNLOAD
// 집계에 조용히 섞이지 않도록 fromVerify 한 곳에서 명시적으로 옮긴다.
type Reason string

const (
	// 후보 판정(planner.evaluate) 단계의 제외 사유.
	ReasonPart         Reason = "part"          // 임시파일 (.part·.filepart)
	ReasonZero         Reason = "zero"          // 0바이트
	ReasonGrace        Reason = "grace"         // 유예중
	ReasonFutureMTime  Reason = "future_mtime"  // 미래시각
	ReasonMismatch     Reason = "mismatch"      // 종류불일치
	ReasonSiteExcluded Reason = "site_mismatch" // 사이트제외
	ReasonSiteUnknown  Reason = "site_unknown"  // 사이트식별불가

	// ReasonHourUnknown — LocalPath 에 (HH) 가 있는데 파일명에서 시각을
	// 뽑지 못했다 (시각식별불가).
	//
	// 커밋 계획 §7.1 의 표시 순서에는 없는 사유다. Category 검사는
	// CategoryMatchUnknown 을 통과시키므로, 이름 형태가 애매한 Hourly
	// 파일은 verify 를 지나 여기서 걸릴 수 있다. 사이트식별불가에 섞으면
	// 운영자가 엉뚱한 쪽(Sites 설정)을 본다. 보고 포맷(커밋 6)에서
	// 사이트식별불가 바로 뒤에 둔다. [판단]
	ReasonHourUnknown Reason = "hour_unknown"

	// 목적지 판정(decideTarget)·중복 제거(destSet) 단계.
	ReasonExists    Reason = "skipped_exists"        // 기존
	ReasonConflict  Reason = "conflict"              // 충돌
	ReasonBadTarget Reason = "irregular_target"      // 비정상대상
	ReasonDuplicate Reason = "duplicate_destination" // 목적지중복
)

// ErrUnmappedVerifyReason 은 verify 가 DOWNLOAD 가 모르는 사유를 돌려줬을
// 때다. verify 에 사유를 추가하면서 fromVerify 를 빠뜨리면 조용히 다른
// 사유로 접히지 않고 여기서 시끄럽게 실패한다 (SetKeyKind 의 default err
// 와 같은 원칙).
var ErrUnmappedVerifyReason = errors.New("download: unmapped verify reason")

// fromVerify 는 verify.Reason 을 DOWNLOAD 사유로 옮긴다.
//
// ReasonIsDir 는 대응하지 않는다. Scanner 는 일반 파일만 Batch 로 넘기므로
// (scan.Batch.Entries 계약) 여기 도달하면 계약 위반이다.
func fromVerify(r verify.Reason) (Reason, error) {
	switch r {
	case verify.ReasonPartFile:
		return ReasonPart, nil
	case verify.ReasonZeroSize:
		return ReasonZero, nil
	case verify.ReasonTooRecent:
		return ReasonGrace, nil
	case verify.ReasonFutureMTime:
		return ReasonFutureMTime, nil
	case verify.ReasonCategoryMismatch:
		return ReasonMismatch, nil
	default:
		return "", fmt.Errorf("%w: %v", ErrUnmappedVerifyReason, r)
	}
}

// LocalStore 는 DOWNLOAD 가 로컬 목적지에 요구하는 능력이다
// (DOWNLOAD 커밋 계획 §5.2). 구현은 커밋 5 에서 둔다.
//
// 인터페이스는 사용하는 쪽(download)이 선언한다.
type LocalStore interface {
	// EnsureDir 은 dir 과 필요한 상위 디렉터리를 만든다. 이미 디렉터리로
	// 있으면 성공이다.
	EnsureDir(ctx context.Context, dir string) error

	// Stat 은 path 의 관측값을 돌려준다. lstat 의미여야 한다 — 링크를
	// 따라가면 최종 경로 자리의 링크가 "같은 크기 일반 파일"로 보여
	// 기존으로 넘어간다. 없으면 errors.Is(err, fs.ErrNotExist).
	Stat(ctx context.Context, path string) (fs.FileInfo, error)

	// Size 는 path 의 일반 파일 크기다. .part 크기 검증에 쓴다.
	Size(ctx context.Context, path string) (int64, error)

	// Rename 은 oldPath 를 newPath 로 바꾼다.
	//
	// 대상이 있을 때의 동작은 OS 마다 다를 수 있다(Go 의 os.Rename 은
	// Windows·Linux 모두 덮어쓴다). 러너는 Rename 직전에 최종 경로를 다시
	// 확인하며, 덮어쓰기 방지는 그 재확인의 몫이다 (커밋 계획 §6.2).
	Rename(ctx context.Context, oldPath, newPath string) error

	// Remove 는 파일 하나를 지운다. 없으면 성공이다.
	Remove(ctx context.Context, path string) error

	// Join 은 로컬 경로 규칙으로 dir 과 name 을 잇는다.
	Join(dir, name string) string
}

// Candidate 는 수신하기로 판정된 원격 파일 하나다.
type Candidate struct {
	Category domain.Category

	// RemotePath 는 원격 원본 경로다 ('/' 구분).
	RemotePath string

	// Name 은 원격에서 관측한 원본 이름이다. 로컬 최종 파일명도 이 이름
	// 그대로다 — 대소문자를 보존한다 (PUT 이 원격에 원본 이름을 쓰는 것과
	// 같은 방침, put/transfer.go). 정규화 이름은 판정에만 쓴다.
	Name string

	// Size 는 목록에서 관측한 원격 크기다. .part 크기 검증의 기준이다.
	Size int64

	MTime time.Time

	// LocalDir 은 목적지 디렉터리, LocalPath 는 최종 경로다.
	LocalDir  string
	LocalPath string

	// UnknownKind 는 파일명만으로 Category 를 확정할 수 없었다는 뜻이다
	// (domain.CategoryMatchUnknown). verify 는 이런 이름을 통과시키므로
	// 수신은 그대로 한다 — 기관 경로를 미리 알 수 없어, 이름이 특이한 진짜
	// RINEX 를 놓치는 쪽이 잡파일 몇 개보다 위험하다 (2026-09-25 결정).
	// 대신 보고에 종류미상으로 드러낸다 (report_print.go).
	UnknownKind bool
}

// PartPath 는 이 후보의 로컬 임시 경로다. 최종 경로와 같은 디렉터리의
// 같은 이름 + ".part" 다.
//
// 같은 디렉터리여야 하는 이유: 최종 전환이 같은 볼륨 안의 Rename 이어야
// 원자적이다. 다른 곳에 받은 뒤 옮기면 복사가 되어 도중 실패 시 반쯤 쓴
// 최종 파일이 남는다.
func (c Candidate) PartPath() string {
	return c.LocalPath + partSuffix
}

// partSuffix 는 로컬 .part 접미사다. domain.IsPartFile 이 인식하는 값이라
// 이 폴더를 나중에 PUT 원본으로 스캔해도 임시 파일로 제외된다.
const partSuffix = ".part"

// planner 는 원격 Entry 를 후보와 제외 사유로 나누는 순수 판정기다.
//
// Category 하나에 하나씩 만든다. 필드는 실행 중 바뀌지 않는다.
type planner struct {
	category domain.Category
	local    *pathpl.Template
	verifier verify.Verifier

	// sites 는 [DOWNLOAD] Sites 의 집합이다. 비면 전체 관측소다.
	sites map[string]struct{}

	// join 은 LocalStore.Join 이다. 로컬 경로 규칙을 이 파일이 가정하지
	// 않게 주입한다.
	join func(dir, name string) string

	needSite bool // sites 가 있거나 LocalPath 에 (SITE) 가 있다
	needHour bool // LocalPath 에 (HH) 가 있다
}

// newPlanner 는 planner 를 만든다.
//
// sites 는 config 가 ParseSiteList 로 이미 정규화한 대문자 목록이다.
// 여기서 다시 NormalizeSiteList 를 거친다 — Config 를 거치지 않는 호출이
// 소문자를 넘기면 정확 일치에 실패해 오류 없이 전부 사이트제외가 된다
// (put.RunOptions.Sites 와 같은 방어).
func newPlanner(
	category domain.Category,
	local *pathpl.Template,
	verifier verify.Verifier,
	sites []string,
	join func(dir, name string) string,
) (*planner, error) {
	if local == nil {
		return nil, errors.New("download: planner: LocalPath template is nil")
	}

	if join == nil {
		return nil, errors.New("download: planner: join is nil")
	}

	set := make(map[string]struct{}, len(sites))

	if len(sites) > 0 {
		codes, err := domain.NormalizeSiteList(sites)
		if err != nil {
			return nil, fmt.Errorf("download: planner: sites: %w", err)
		}

		for _, c := range codes {
			set[c] = struct{}{}
		}
	}

	return &planner{
		category: category,
		local:    local,
		verifier: verifier,
		sites:    set,
		join:     join,
		needSite: len(set) > 0 || local.HasToken(pathpl.TokenSITE),
		needHour: local.HasToken(pathpl.TokenHH),
	}, nil
}

// evaluate 는 원격 Entry 하나를 판정한다 (커밋 계획 §5.1).
//
//	rawName
//	  → IsPartFile(rawName)                       임시파일
//	  → NormalizeName(rawName)
//	  → verify.Verifier                           유예중·미래시각·0바이트·종류불일치
//	  → SiteFromName      (needSite 일 때만)      사이트식별불가
//	  → Sites 필터        (Sites 가 있을 때만)    사이트제외
//	  → HourFromName      (needHour 일 때만)      시각식별불가
//	  → ExpandFile
//
// 반환은 셋 중 하나다:
//
//	(후보, "", nil)     받을 후보
//	(_, 사유, nil)      제외. 다음 회차에 다시 판정된다
//	(_, "", err)        구현·계약 결함 (라우팅 누락, verify 새 사유,
//	                    domain 이 준 값을 pathpl 이 거부). 러너는 이 파일을
//	                    실패로 세고 원인을 로그에 남긴다
//
// IsPartFile 을 NormalizeName 보다 먼저 부른다. NormalizeName 은 ".part" 를
// 떼므로 순서가 바뀌면 "suw1250a.26o.part" 가 완성 파일처럼 SITE·HH 추출에
// 성공한다 (D18, DOWNLOAD v3 §5.1). verify 도 IsPartFile 을 보지만 그 앞에서
// 직접 거른다 — 이 순서의 주인은 DOWNLOAD 이고 verify 내부 순서가 아니다.
//
// SiteFromName 을 Sites 도 (SITE) 도 없을 때 부르지 않는 이유: 그때 관측소
// 식별은 쓸 곳이 없다. 부르면 verify 가 통과시킨 파일(CategoryMatchUnknown
// 등)이 "사이트식별불가"라는 새 제외 사유로 빠진다. PUT 의 site 필터와 같은
// 규칙이다 (SITE v1 §4, put/runner.go).
func (p *planner) evaluate(dir string, e scan.Entry, when time.Time) (Candidate, Reason, error) {
	raw := e.Name

	if domain.IsPartFile(raw) {
		return Candidate{}, ReasonPart, nil
	}

	norm := domain.NormalizeName(raw)

	// verify 에는 원본 이름을 넘긴다. MatchesName 이 내부에서 정규화하며,
	// PUT 러너와 같은 입력이다.
	if r := p.verifier.Verify(verify.Input{
		Name:     raw,
		Size:     e.Size,
		MTime:    e.MTime,
		IsDir:    e.IsDir,
		Category: p.category,
	}); !r.OK() {
		reason, err := fromVerify(r)
		return Candidate{}, reason, err
	}

	var fields pathpl.FileFields

	if p.needSite {
		site, ok, err := domain.SiteFromName(p.category, norm)
		if err != nil {
			return Candidate{}, "", fmt.Errorf("site of %q: %w", raw, err)
		}

		if !ok {
			return Candidate{}, ReasonSiteUnknown, nil
		}

		if len(p.sites) > 0 {
			if _, want := p.sites[site]; !want {
				return Candidate{}, ReasonSiteExcluded, nil
			}
		}

		fields.Site = site
	}

	if p.needHour {
		hh, ok, err := domain.HourFromName(p.category, norm)
		if err != nil {
			return Candidate{}, "", fmt.Errorf("hour of %q: %w", raw, err)
		}

		if !ok {
			return Candidate{}, ReasonHourUnknown, nil
		}

		fields.Hour = hh
	}

	localDir, err := p.local.ExpandFile(when, fields)
	if err != nil {
		// domain 이 준 값만 넣으므로 정상 흐름에서는 나오지 않는다.
		// 나오면 두 패키지의 계약이 어긋난 것이다.
		return Candidate{}, "", fmt.Errorf("destination of %q: %w", raw, err)
	}

	return Candidate{
		Category:    p.category,
		RemotePath:  remoteJoin(dir, raw),
		Name:        raw,
		Size:        e.Size,
		MTime:       e.MTime,
		LocalDir:    localDir,
		LocalPath:   p.join(localDir, raw),
		UnknownKind: p.category.MatchesName(raw) == domain.CategoryMatchUnknown,
	}, "", nil
}

// remoteJoin 은 원격 디렉터리와 이름을 SFTP 규칙('/')으로 잇는다.
// Batch.Dir 는 끝 '/' 가 있을 수도 없을 수도 있다.
func remoteJoin(dir, name string) string {
	return strings.TrimRight(dir, "/") + "/" + name
}

// targetState 는 로컬 최종 경로를 관측한 결과다.
type targetState int

const (
	// targetAbsent — 최종 파일이 없다. 받는다.
	targetAbsent targetState = iota + 1

	// targetExists — 같은 크기의 일반 파일이 있다. 기존으로 센다.
	targetExists

	// targetConflict — 다른 크기의 일반 파일이 있다. 덮어쓰지 않는다.
	targetConflict

	// targetIrregular — 디렉터리·링크·장치 등 일반 파일이 아닌 것이 있다.
	targetIrregular
)

// reason 은 받지 않는 상태의 집계 사유다. targetAbsent 는 사유가 없다.
func (s targetState) reason() Reason {
	switch s {
	case targetExists:
		return ReasonExists
	case targetConflict:
		return ReasonConflict
	case targetIrregular:
		return ReasonBadTarget
	default:
		return ""
	}
}

// decideTarget 은 로컬 최종 경로의 관측(Stat 결과)으로 받을지를 정한다
// (커밋 계획 §5.2, DOWNLOAD v3 §5.2).
//
//	없음 (fs.ErrNotExist)       → targetAbsent
//	일반 파일, 크기 같음        → targetExists
//	일반 파일, 크기 다름        → targetConflict   (자동 덮어쓰기 없음, D8)
//	디렉터리·링크·비정규        → targetIrregular  (기존으로 오인하지 않음)
//	그 외 Stat 오류(권한 등)    → err              (판정값으로 숨기지 않는다)
//
// Stat 이 lstat 의미라는 전제(LocalStore.Stat)가 있어야 링크가 여기서
// 비정규로 나뉜다. 러너는 수신 전과 Rename 직전에 같은 함수를 쓴다.
func decideTarget(remoteSize int64, fi fs.FileInfo, statErr error) (targetState, error) {
	if statErr != nil {
		if errors.Is(statErr, fs.ErrNotExist) {
			return targetAbsent, nil
		}

		return 0, statErr
	}

	if fi == nil {
		return 0, errors.New("download: stat returned neither info nor error")
	}

	if !fi.Mode().IsRegular() {
		return targetIrregular, nil
	}

	if fi.Size() == remoteSize {
		return targetExists, nil
	}

	return targetConflict, nil
}

// destSet 은 한 회차 안에서 이미 배정된 로컬 최종 경로의 집합이다
// (커밋 계획 §5.3).
//
// 서로 다른 원격 경로가 같은 로컬 최종 경로로 수렴할 수 있다 — 원본에서
// (SITE) 폴더 두 곳에 같은 파일이 있는데 목적지에 (SITE) 가 없는 경우,
// 원본 날짜 폴더 아래 00·13 두 곳에 사본이 있는 경우, 두 Category 의
// LocalPath 가 같은 경우 등. 둘 다 워커에 보내면 같은 .part 를 두 워커가
// 동시에 쓴다. 그래서 워커에 보내기 전에 걸러낸다.
//
// Scanner 가 먼저 발견한 항목이 이긴다. DOWNLOAD 의 sftpLister 가 각 목록을
// 이름순으로 고정하고 Scanner 가 날짜 오름차순·하위 폴더 이름순으로
// 순회하므로 승자도 매 회차 같다.
//
// 키는 대소문자를 접은 경로다. [판단] Windows 파일시스템은 대소문자를
// 구분하지 않아 "SOCH….gz" 와 "soch….gz" 가 같은 파일이다. Linux 에서는
// 다른 파일이지만, 원격의 두 이름이 대소문자만 다른 RINEX 는 정상 운영에서
// 나오지 않는 이상 신호이므로 둘째를 목적지중복으로 드러내는 편이 낫다.
// 구분자 차이('/'·'\')도 같은 파일이므로 접는다. Windows 가 각 경로 단의
// 끝에서 무시하는 점·공백도 제거하고 .·.. 및 중복 구분자를 정리한다.
//
// 동시성: 러너의 producer(단일 goroutine)만 쓴다. 잠금이 없다.
type destSet struct {
	seen map[string]string // key → 먼저 배정한 원격 경로
}

func newDestSet() *destSet {
	return &destSet{seen: make(map[string]string)}
}

// claim 은 c 의 최종 경로를 배정한다. 이미 배정된 경로면 false 와 함께
// 먼저 배정된 원격 경로를 돌려준다 (파일별 로그용).
func (d *destSet) claim(c Candidate) (ok bool, winner string) {
	key := destKey(c.LocalPath)

	if prev, dup := d.seen[key]; dup {
		return false, prev
	}

	d.seen[key] = c.RemotePath

	return true, ""
}

// destKey 는 destSet 의 비교 키다 (destSet 주석).
func destKey(localPath string) string {
	slashed := strings.ReplaceAll(localPath, `\`, "/")
	wasUNC := strings.HasPrefix(slashed, "//")
	parts := strings.Split(slashed, "/")

	for i, part := range parts {
		// 정확한 .·..은 path.Clean 이 의미대로 처리해야 한다. 그 외의
		// 후행 점·공백은 Win32가 파일을 열 때 제거하는 별칭이다.
		if part != "." && part != ".." {
			parts[i] = strings.TrimRight(part, " .")
		}
	}

	key := path.Clean(strings.Join(parts, "/"))
	if wasUNC && strings.HasPrefix(key, "/") && !strings.HasPrefix(key, "//") {
		key = "/" + key
	}

	return strings.ToLower(key)
}
