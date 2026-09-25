package download

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// DOWNLOAD 커밋 4 리뷰에서 찾은 운영 치명 지점을 고정한다.
//
//	':' 가 든 원격 이름   → Windows 에서 0바이트 기존 파일이 생겨 진짜 파일을
//	                        매 회차 충돌로 영구히 못 받는다
//	RINEX2 세션 → (HH)    → 시각 폴더가 틀리면 파일이 엉뚱한 시각에 쌓이고
//	                        같은 설정의 재실행이 기존으로 인식하지 못한다
//	(SITE) 값의 대소문자  → 목적지 폴더 이름이 원격 표기가 아니라 대문자다

// 원격 서버(Linux)에서는 합법이지만 Windows 파일 이름으로 쓸 수 없는 이름은
// 후보가 되지 않고 Irregular 로 드러난다.
func TestEntryOf_WindowsIllegalNamesAreIrregular(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"soch268a.26o.gz:x", // NTFS 대체 데이터 스트림
		"a<b.gz", "a>b.gz", `a"b.gz`, "a|b.gz", "a?b.gz", "a*b.gz",
		"a\tb.gz", "a\nb.gz",
		"CONIN$", "conout$.log", "CLOCK$", "COM0", "LPT0.txt",
		"COM¹", "com².gz", "LPT³.dat",
	} {
		e := entryOf(fakeInfo{name: name, mode: 0o644})

		if e.Type != fs.ModeIrregular || e.IsRegular() {
			t.Errorf("name %q → %+v, want irregular", name, e)
		}
	}

	// 대조군: 정상 RINEX 이름은 그대로 일반 파일이다.
	for _, name := range []string{
		"soch268a.26o.gz",
		"SOCH00KOR_R_20262681300_01H_30S_MO.rnx.gz",
		"suw1268a.26d.Z",
	} {
		if e := entryOf(fakeInfo{name: name, mode: 0o644}); !e.IsRegular() {
			t.Errorf("name %q → %+v, want regular", name, e)
		}
	}
}

// 스트림 이름이 섞여 있어도 진짜 파일 하나만 후보가 된다 (Scanner → planner).
func TestPlan_StreamNameDoesNotShadowRealFile(t *testing.T) {
	m := &mapRemote{
		dirs: map[string][]fs.FileInfo{
			"/RNX/2026/268/": {
				fakeInfo{name: "soch268a.26o.gz:x", size: 5, mode: 0o644},
				fakeInfo{name: "soch268a.26o.gz", size: 9, mode: 0o644},
			},
		},
	}

	p, err := newPlanner(
		domain.CategoryRINEX2Hourly,
		mustTpl(t, "/down/(YYYY)/(DOY)/"),
		verify.Verifier{},
		nil,
		slashJoin,
	)
	if err != nil {
		t.Fatal(err)
	}

	var got []string

	res, err := scan.New(newSFTPLister(m)).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustTpl(t, "/RNX/(YYYY)/(DOY)/"),
		scan.Range{From: day268, To: day268},
		func(b scan.Batch) error {
			for _, e := range b.Entries {
				c, r, err := p.evaluate(b.Dir, e, b.When)
				if err != nil {
					return err
				}

				if r == "" {
					got = append(got, c.LocalPath)
				}
			}

			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(got, []string{"/down/2026/268/soch268a.26o.gz"}) {
		t.Fatalf("candidates = %v, want only the real file", got)
	}

	if res.Irregular != 1 {
		t.Errorf("Irregular = %d, want 1 (the stream name)", res.Irregular)
	}
}

// 위 가드가 필요한 근거: Windows 에서 ':' 가 든 이름으로 .part 를 쓰면 0바이트
// 기본 파일이 생긴다. 그러면 진짜 파일의 목적지 판정은 "크기가 다른 기존
// 파일 = 충돌"이 되어 자동으로는 영원히 받지 못한다.
func TestWindowsStreamNameWouldBlockRealFile(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("NTFS alternate data streams are Windows-only")
	}

	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "soch268a.26o.gz:x.part"), []byte("data"), 0o644); err != nil {
		t.Skipf("stream write not supported on this volume: %v", err)
	}

	fi, statErr := os.Lstat(filepath.Join(dir, "soch268a.26o.gz"))

	state, err := decideTarget(9, fi, statErr)
	if err != nil {
		t.Fatal(err)
	}

	if state != targetConflict {
		t.Fatalf("state = %v; the hazard this guard prevents did not reproduce", state)
	}
}

// RINEX2 Hourly 세션 문자 → (HH). 측위원 원본은 RINEX2 가 주력이다.
// 시각 폴더가 틀리면 파일이 엉뚱한 곳에 쌓이고, 재실행이 기존으로
// 인식하지 못해 같은 파일을 다른 폴더에 또 받는다.
func TestEvaluate_Rinex2SessionMapsToHourFolder(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/(SITE)/(HH)/", nil)

	cases := map[string]string{
		"soch268a.26o.gz": "/down/2026/268/SOCH/00/",
		"soch268n.26o.gz": "/down/2026/268/SOCH/13/",
		"soch268x.26d.Z":  "/down/2026/268/SOCH/23/",
	}

	for name, wantDir := range cases {
		c, r := evalOne(t, p, oldEntry(name, 10))
		if r != "" {
			t.Errorf("%s → %q, want candidate", name, r)
			continue
		}

		if c.LocalDir != wantDir || c.LocalPath != wantDir+name {
			t.Errorf("%s → dir %q path %q, want %q", name, c.LocalDir, c.LocalPath, wantDir+name)
		}
	}
}

// (SITE) 목적지 폴더는 파일명 표기가 아니라 대문자 관측소 코드다.
//
// Windows 에서는 기존 "soch" 폴더와 같은 폴더다. Linux 목적지에 기존 도구가
// 만든 소문자 관측소 폴더가 있으면 "SOCH" 폴더가 새로 생기고 기존 파일을
// 보지 못해 다시 받는다 — 이 동작을 고정해 두고 D14 운영 절차에 적는다.
func TestEvaluate_SiteFolderIsUppercaseCode(t *testing.T) {
	p := newTestPlanner(t, domain.CategoryRINEX2Hourly, "/down/(YYYY)/(DOY)/(SITE)/", nil)

	c, r := evalOne(t, p, oldEntry("soch268a.26o.gz", 10))
	if r != "" {
		t.Fatalf("reason = %q", r)
	}

	if c.LocalDir != "/down/2026/268/SOCH/" {
		t.Fatalf("LocalDir = %q, want upper-case site folder", c.LocalDir)
	}

	// 파일명은 원본 표기 그대로다.
	if c.Name != "soch268a.26o.gz" {
		t.Errorf("Name = %q, want the remote spelling", c.Name)
	}
}
