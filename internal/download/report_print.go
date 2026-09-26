package download

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"SFTPClient/internal/scan"
)

// DOWNLOAD 커밋 6 — 보고 포맷 (DOWNLOAD_COMMIT_PLAN v1 §7, DOWNLOAD v3 §10).
//
// 표시 규칙만 여기 있다. 집계(Report)는 커밋 5 가 만든 숫자 그대로이며,
// 0 을 생략해도 Report 의 값은 지우지 않는다 (v3 §10.3).
//
// PUT 의 RunReport.Print 는 바꾸지 않는다 (v3 §10.4 — 후속 과제).

// Range 는 회차의 날짜 범위 종류다. main 이 Report.Range 에 넣는다
// (PUT 의 RunReport.Range 와 같은 방식).
type Range string

const (
	RangeHot  Range = "hot"  // [SCAN] ScanRecentDays
	RangeDeep Range = "deep" // [SCAN] ScanDays (DeepScanHour·--deep)
)

// label 은 범위의 표시명이다. 범위는 문맥 값이라 0 여부와 관계없이 항상
// 출력한다. 비어 있으면 "미지정"으로 드러낸다 — main 이 채우지 않은 회차를
// 조용히 "최근"으로 보이게 하지 않는다 (PUT 의 "unset" 과 같은 취지).
//
// "심층"은 [판단]이다. v3 §10.2 의 예시에는 "최근"만 있다. "전체"는 원격
// 전체를 뒤진 것처럼 읽혀 ScanDays 범위라는 사실과 어긋난다.
func (r Range) label() string {
	switch r {
	case RangeHot:
		return "최근"
	case RangeDeep:
		return "심층"
	case "":
		return "미지정"
	default:
		return string(r)
	}
}

// reasonLabels 는 제외·목적지 사유의 표시명과 출력 순서다 (v3 §10.1,
// 커밋 계획 §7.1). 원격·수신은 앞에, 실패·미착수는 뒤에 따로 붙는다.
//
// 시각식별불가(hour_unknown)는 계획 순서에 없던 사유다 — 커밋 4 에서 추가하며
// 사이트식별불가 바로 뒤에 두기로 했다.
//
// 여기 없는 사유가 Report 에 들어오면 사라지지 않고 줄 끝에 기타(키)=N 으로
// 나온다 (formatCategory). 새 사유를 추가하면서 이 표를 빠뜨려도 숫자가
// 보고에서 증발하지 않는다.
var reasonLabels = []struct {
	reason Reason
	label  string
}{
	{ReasonExists, "기존"},
	{ReasonSiteExcluded, "사이트제외"},
	{ReasonSiteUnknown, "사이트식별불가"},
	{ReasonHourUnknown, "시각식별불가"},
	{ReasonGrace, "유예중"},
	{ReasonFutureMTime, "미래시각"},
	{ReasonPart, "임시파일"},
	{ReasonZero, "0바이트"},
	{ReasonMismatch, "종류불일치"},
	{ReasonConflict, "충돌"},
	{ReasonBadTarget, "비정상대상"},
	{ReasonDuplicate, "목적지중복"},
}

// Print 는 Category 마다 요약 한 줄과 원격 나열의 이상을 남긴다.
//
// 줄은 두 로거로 나뉜다. 기준은 "현장 운영자가 조치할 수 있는가"다 [판단,
// 2026-09-26 요청]. 원격 폴더 구조는 기관 규칙이라 우리가 바꿀 수 없으므로,
// 그 구조에서 나오는 관찰은 고객 화면에 띄우지 않고 파일에만 남긴다.
//
//	console  요약 줄, 나열경고(나열실패·원격폴더없음)와 나열실패 상세
//	detail   나열참고(비정규항목·폴더불일치·폴더밖파일)와 비정규항목 상세
//
// main 은 console 에 화면+파일 로거, detail 에 파일 전용 로거를 넣는다.
// 그래서 파일에는 모든 줄이 남는다.
//
// console 이 nil 이면 log.Default(). detail 이 nil 이면 console 로 보낸다 —
// 줄을 버리지 않는다(테스트·도구에서 로거 하나로 전부 보려는 경우).
func (r Report) Print(console, detail *log.Logger) {
	if console == nil {
		console = log.Default()
	}

	if detail == nil {
		detail = console
	}

	for _, c := range r.Categories {
		console.Print(formatCategory(c, r.Range))

		for _, line := range formatScanWarnings(c) {
			console.Print(line)
		}

		for _, line := range formatScanNotes(c) {
			detail.Print(line)
		}
	}
}

// formatCategory 는 Category 하나의 요약 줄이다.
//
//	[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 원격=412 수신=37 기존=357 … 실패=1 소요=2.4s
//	[DOWNLOAD] 종류=RINEX2_HOURLY 범위=최근 대상없음 소요=18ms
//
// 종류·범위·소요는 항상 나온다. 처리 집계(원격·수신·사유·실패·미착수)는 0 이면
// 빠지고, 전부 0 이면 대상없음 하나로 바뀐다. 사유는 reasonLabels 순서다 —
// map 순회 순서에 영향을 받지 않는다.
func formatCategory(c CategoryReport, rng Range) string {
	var fields []string

	add := func(label string, n int) {
		if n != 0 {
			fields = append(fields, fmt.Sprintf("%s=%d", label, n))
		}
	}

	add("원격", c.Scan.Files)
	add("수신", c.Downloaded)

	// 종류미상은 수신의 부분집합이라 수신 바로 뒤에 둔다. 이번 회차에 새로
	// 받은 것만 세므로 잡파일이 한 번 들어온 회차에만 뜬다 (2026-09-25 결정:
	// 받되 드러낸다). 파일별 경로는 detail 로그에 있다.
	add("종류미상", c.UnknownKind)

	known := make(map[Reason]bool, len(reasonLabels))
	for _, rl := range reasonLabels {
		known[rl.reason] = true
		add(rl.label, c.Reasons[rl.reason])
	}

	add("실패", c.Failed)

	// 미착수는 계획 순서에 없던 값이다 (커밋 5 추가). 회차 취소에서만 나오고
	// 실패가 아니므로 실패 뒤에 둔다.
	add("미착수", c.NotStarted)

	// 표에 없는 사유 — 이름순으로 고정해 끝에 붙인다.
	var unknown []string
	for reason, n := range c.Reasons {
		if !known[reason] && n != 0 {
			unknown = append(unknown, fmt.Sprintf("기타(%s)=%d", reason, n))
		}
	}

	sort.Strings(unknown)
	fields = append(fields, unknown...)

	body := "대상없음"
	if len(fields) > 0 {
		body = strings.Join(fields, " ")
	}

	return fmt.Sprintf(
		"[DOWNLOAD] 종류=%s 범위=%s %s 소요=%s",
		c.Category,
		rng.label(),
		body,
		formatDuration(c.Duration),
	)
}

// formatScanWarnings 는 조치가 필요한 원격 나열 이상을 알린다 (화면+파일).
//
// 요약 줄의 "대상없음"이 나열 실패를 가리지 않게 하기 위해 따로 둔다 — 목록을
// 읽지 못한 폴더는 원격 0 건으로 보인다. 이상이 없으면 아무 줄도 없다.
//
//	[DOWNLOAD] 나열경고 종류=… 나열실패=1 원격폴더없음=2
//	[DOWNLOAD] 나열실패 종류=… 폴더=… 원인=…
//
// 원격폴더없음(Missing)은 조회한 폴더가 하나도 없을 때만 낸다 (Dirs == 0).
// 오늘 날짜 폴더가 아직 없는 것은 Hot 범위에서 매시간 정상이므로 경고하지
// 않는다. 반대로 범위 전체에서 폴더가 하나도 없으면 RemotePath 오기일 가능성이
// 크고, 그대로 두면 매 회차 "대상없음"으로 조용히 끝난다. [판단]
//
// 패턴폴더없음은 (SITE)·(HH) 자리에서 폴더를 봤는데 패턴에 맞는 것이 하나도
// 없을 때다 (PatternRoots == 0 이면서 Unmatched 또는 폴더밖파일이 있음). 일부만
// 안 맞으면 기관 구조의 잡음이라 참고(파일 전용)로 두지만, 전부 안 맞으면
// 토큰 위치나 관측소 폴더 이름 규칙이 설정과 다른 설정 오류다. 화면에 띄우지
// 않으면 매 회차 "대상없음"으로 조용히 끝난다. PATH v4 D3 와 PUT 의
// [SCAN][WARN] no folder matched 와 같은 기준이다.
func formatScanWarnings(c CategoryReport) []string {
	s := c.Scan

	noPattern := patternNothingMatched(s)

	patternMisses := 0
	if noPattern {
		patternMisses = s.Unmatched + s.PatternParentFiles
	}

	counts := nonZeroFields([]field{
		{"나열실패", s.Errs},
		{"원격폴더없음", missingWhenNothingFound(s)},
		{"패턴폴더없음", patternMisses},
	})

	if len(counts) == 0 {
		return nil
	}

	lines := []string{fmt.Sprintf(
		"[DOWNLOAD] 나열경고 종류=%s %s",
		c.Category,
		strings.Join(counts, " "),
	)}

	for _, f := range s.Failures {
		lines = append(lines, fmt.Sprintf(
			"[DOWNLOAD] 나열실패 종류=%s 폴더=%s 원인=%v",
			c.Category, f.Dir, f.Err,
		))
	}

	if noPattern {
		for _, p := range s.UnmatchedDetails {
			lines = append(lines, fmt.Sprintf(
				"[DOWNLOAD] 패턴폴더없음 종류=%s 맞지않는폴더=%s "+
					"(RemotePath 의 토큰 위치와 폴더 이름을 확인)",
				c.Category, p,
			))
		}

		for _, p := range s.PatternParentFileDetails {
			lines = append(lines, fmt.Sprintf(
				"[DOWNLOAD] 패턴폴더없음 종류=%s 폴더밖파일=%s "+
					"(토큰 위치가 한 단계 어긋났을 수 있음)",
				c.Category, p,
			))
		}
	}

	return lines
}

// patternNothingMatched 는 패턴 단에서 부모 폴더는 봤는데 맞는 폴더가 하나도
// 없었는지 답한다. 파일 토큰이 없는 템플릿에서는 세 값이 모두 0 이라 false 다.
func patternNothingMatched(s scan.Result) bool {
	return s.PatternRoots == 0 && (s.Unmatched > 0 || s.PatternParentFiles > 0)
}

// formatScanNotes 는 원격 폴더 구조에서 나온 관찰을 남긴다 (파일 전용).
//
// 기관의 원격 규칙에서 생기는 것이라 우리 쪽에서 고칠 수 없다. 오류가 아니므로
// "경고" 대신 "참고"다. 받을 수 없는 파일을 놓친 것은 아니다 — 수집 대상이
// 아니어서 건너뛴 항목이다.
//
//	[DOWNLOAD] 나열참고 종류=… 비정규항목=2 폴더불일치=1 폴더밖파일=3
//	[DOWNLOAD] 비정규항목 종류=… 경로=… 형태=…
//
// 상세 줄은 비정규항목만 낸다. 폴더불일치·폴더밖파일은 백업 폴더 같은 무해한
// 경우가 흔해 건수만 낸다. [판단]
func formatScanNotes(c CategoryReport) []string {
	s := c.Scan

	// 패턴이 하나도 안 맞았으면 폴더불일치·폴더밖파일은 이미 화면 경고
	// (패턴폴더없음)로 나갔다. 같은 사실을 참고로 한 번 더 쓰지 않는다.
	unmatched, parentFiles := s.Unmatched, s.PatternParentFiles
	if patternNothingMatched(s) {
		unmatched, parentFiles = 0, 0
	}

	counts := nonZeroFields([]field{
		{"비정규항목", s.Irregular},
		{"폴더불일치", unmatched},
		{"폴더밖파일", parentFiles},
	})

	if len(counts) == 0 {
		return nil
	}

	lines := []string{fmt.Sprintf(
		"[DOWNLOAD] 나열참고 종류=%s %s",
		c.Category,
		strings.Join(counts, " "),
	)}

	for _, ir := range s.IrregularDetails {
		lines = append(lines, fmt.Sprintf(
			"[DOWNLOAD] 비정규항목 종류=%s 경로=%s 형태=%v",
			c.Category, ir.Path, ir.Type,
		))
	}

	return lines
}

// missingWhenNothingFound 는 범위 안에서 폴더를 하나도 찾지 못했을 때만
// Missing 을 돌려준다. (formatScanWarnings 주석)
func missingWhenNothingFound(s scan.Result) int {
	if s.Dirs == 0 {
		return s.Missing
	}

	return 0
}

// field 는 출력 필드 하나(표시명과 값)다.
type field struct {
	label string
	n     int
}

// nonZeroFields 는 0 이 아닌 필드만 "이름=값"으로 돌려준다. 순서는 입력 순서다.
func nonZeroFields(fs []field) []string {
	var out []string

	for _, f := range fs {
		if f.n != 0 {
			out = append(out, fmt.Sprintf("%s=%d", f.label, f.n))
		}
	}

	return out
}

// formatDuration 은 소요를 사람이 읽기 좋게 줄인다.
//
//	18.3ms → 18ms,  2.412s → 2.4s,  0 → 0s
func formatDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "0s"
	case d < time.Millisecond:
		return d.Round(time.Microsecond).String()
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	default:
		return d.Round(100 * time.Millisecond).String()
	}
}
