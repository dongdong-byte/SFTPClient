package config

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// DOWNLOAD 커밋 8 — 예제 설정이 실제 DOWNLOAD 검증과 맞는지 고정한다
// (DOWNLOAD_COMMIT_PLAN v1 §9.2 "예제 config 만 보고 DOWNLOAD 전용 설정을
// 구성할 수 있다", "문서의 토큰 허용 정책과 실제 config 검증이 일치한다").
//
// config.example.ini 의 주석이 설명하는 두 가지 사용법을 그대로 해 본다.
//
//  1. Mode 만 download 로 바꾼다 (PUT 블록은 남아 있다).
//  2. 주석이 "필요 없는 섹션"이라 한 것을 지우고 Mode=download 로 둔다.
//
// 둘 다 Validate 를 통과해야 한다. 예제 주석이나 검증 규칙 한쪽만 바뀌면
// 여기서 드러난다.

func readExampleINI(t *testing.T) string {
	t.Helper()

	b, err := os.ReadFile(exampleINIPath(t))
	if err != nil {
		t.Fatal(err)
	}

	return string(b)
}

func setMode(t *testing.T, ini, mode string) string {
	t.Helper()

	re := regexp.MustCompile(`(?m)^Mode = put\r?$`)
	if !re.MatchString(ini) {
		t.Fatal("config.example.ini has no `Mode = put` line")
	}

	return re.ReplaceAllString(ini, "Mode = "+mode)
}

// dropSection 은 [name] 머리부터 다음 섹션 머리 직전까지를 지운다.
func dropSection(t *testing.T, ini, name string) string {
	t.Helper()

	re := regexp.MustCompile(`(?ms)^\[` + regexp.QuoteMeta(name) + `\]\r?\n.*?(?:^\[|\z)`)

	loc := re.FindStringIndex(ini)
	if loc == nil {
		t.Fatalf("section [%s] not found in config.example.ini", name)
	}

	end := loc[1]
	if end < len(ini) {
		end-- // 다음 섹션의 '[' 는 남긴다
	}

	return ini[:loc[0]] + ini[end:]
}

func TestExampleINI_ModeDownloadIsValid(t *testing.T) {
	cfg, _, err := mapConfigForTest(t, setMode(t, readExampleINI(t), "download"))
	if err != nil {
		t.Fatalf("mapConfig: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("example ini with Mode=download must validate:\n%v", err)
	}

	if len(cfg.Download.EnabledCategories()) == 0 {
		t.Fatal("example ini enables no DOWNLOAD category")
	}
}

func TestExampleINI_DownloadOnlyLayoutIsValid(t *testing.T) {
	ini := setMode(t, readExampleINI(t), "download")

	// 예제 [DOWNLOAD] 머리 주석의 "필요 없는 섹션" 목록 그대로다.
	for _, name := range []string{
		"INGRESS", "LEDGER", "PUT", "PUT.SFTP",
		"PUT.RINEX2_DAILY", "PUT.RINEX2_HOURLY",
		"PUT.RINEX3_DAILY", "PUT.RINEX3_HOURLY",
		"PUT.RINEX4_DAILY", "PUT.RINEX4_HOURLY",
	} {
		ini = dropSection(t, ini, name)
	}

	ini = regexp.MustCompile(`(?m)^LedgerPath = .*\r?\n`).ReplaceAllString(ini, "")

	cfg, _, err := mapConfigForTest(t, ini)
	if err != nil {
		t.Fatalf("mapConfig: %v", err)
	}

	if cfg.Put.Present {
		t.Fatal("PUT block still present — the section list in the example comment is incomplete")
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("download-only layout described in the example must validate:\n%v", err)
	}
}

// config.example.ini는 Windows와 Linux 운영자가 그대로 참고하는 파일이다.
// Windows 절대경로를 Linux에서 쓰면 조용히 이상한 상대경로가 되고, DOWNLOAD
// 목적지를 외부 writer와 공유하면 Rename 경합으로 그 파일을 교체할 수 있다.
// 운영상 중요한 두 경고와 Linux 대체 예시가 문서 정리 때 사라지지 않게 한다.
func TestExampleINI_DocumentsCrossPlatformAndExclusiveDownloadPaths(t *testing.T) {
	ini := readExampleINI(t)

	for _, want := range []string{
		"D:\\... LocalPath 활성값은 Windows 배포 예시",
		"PUT 예:      /srv/rinex/source/(YYYY)/(DOY)/",
		"DOWNLOAD 예: /srv/rinex/download/(YYYY)/(DOY)/",
		"DOWNLOAD LocalPath는 이 프로그램만 쓰는 전용 폴더",
		"공유 수신 폴더나 외부 writer와 함께 쓰지 않는다",
		"이 블록이 하나라도 있으면 블록 전체를 파싱·검증",
	} {
		if !strings.Contains(ini, want) {
			t.Errorf("config.example.ini missing operational warning %q", want)
		}
	}
}
