package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
)

// 재귀 Scanner 의 운영 위험을 실제 파일시스템(LocalLister)으로 고정한다
// (PATH_DESIGN v3 §3·§5). fake 가 아니라 실제 os.ReadDir 를 쓰는 이유:
// 순회 순서와 링크 판정은 fake 가 흉내 내는 순간 검증 대상이 사라진다.

func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func scanOneDay(t *testing.T, root string) ([]Batch, Result) {
	t.Helper()

	var batches []Batch

	result, err := New(LocalLister{}).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, root+"/(YYYY)/(DOY)/"),
		Range{
			From: utcDate(2026, time.January, 1),
			To:   utcDate(2026, time.January, 1),
		},
		func(b Batch) error {
			batches = append(batches, b)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	return batches, result
}

func batchNames(b Batch) []string {
	names := make([]string, 0, len(b.Entries))
	for _, e := range b.Entries {
		names = append(names, e.Name)
	}

	return names
}

// TestScan_SeoulTreeFlatFirstDeterministic 는 서울시형(시각 하위 폴더)과
// 평면 잔재가 섞인 날짜 폴더에서 순회 순서가 규약대로인지 본다.
//
// 운영에서 터지는 지점: put 의 중복 가드는 "먼저 나온 파일이 이긴다"
// (runner.go seen). 순서가 규약(현재 폴더 파일 먼저 → 하위 폴더 이름순)을
// 벗어나거나 실행마다 달라지면, 같은 이름의 평면 파일과 HH 폴더 사본 중
// 어느 쪽이 전송되는지가 회차마다 바뀌어 크기가 다를 경우 매시간
// revision 이 오르는 재전송 루프가 된다 (PATH_DESIGN v3 §3.2).
func TestScan_SeoulTreeFlatFirstDeterministic(t *testing.T) {
	root := filepath.ToSlash(t.TempDir())
	day := root + "/2026/001"

	writeFile(t, day+"/dbon001a.26o", "flat")        // 평면 잔재
	writeFile(t, day+"/13/dbon001n.26o", "hour-13")  // 이름순으로 뒤
	writeFile(t, day+"/00/dbon001a.26o", "hour-00x") // 평면과 같은 이름, 다른 크기
	writeFile(t, day+"/00/aaaa001a.26o", "hour-00")

	for run := 0; run < 3; run++ {
		batches, result := scanOneDay(t, root)

		if len(batches) != 3 {
			t.Fatalf("run %d: batches = %d, want 3", run, len(batches))
		}

		wantDirs := []string{day, day + "/00", day + "/13"}
		wantNames := [][]string{
			{"dbon001a.26o"},
			{"aaaa001a.26o", "dbon001a.26o"},
			{"dbon001n.26o"},
		}

		for i, b := range batches {
			if strings.TrimRight(b.Dir, "/") != wantDirs[i] {
				t.Errorf("run %d: batch[%d].Dir = %q, want %q", run, i, b.Dir, wantDirs[i])
			}

			if got := batchNames(b); strings.Join(got, ",") != strings.Join(wantNames[i], ",") {
				t.Errorf("run %d: batch[%d] = %v, want %v", run, i, got, wantNames[i])
			}

			if !b.When.Equal(utcDate(2026, time.January, 1)) {
				t.Errorf("run %d: batch[%d].When = %v, want day 00:00 UTC", run, i, b.When)
			}
		}

		// 평면 파일(크기 4)이 첫 Batch 에 먼저 나온다 → 중복 가드에서 이긴다.
		if first := batches[0].Entries[0]; first.Size != int64(len("flat")) {
			t.Errorf("run %d: 첫 dbon001a.26o size = %d, want 평면 쪽(4)", run, first.Size)
		}

		// 폴더는 Batch 에 들어가지 않는다 (§4) — 날짜 1 + 하위 2 = 3 개 디렉터리.
		if result.Dirs != 3 || result.Files != 4 || result.Irregular != 0 {
			t.Errorf(
				"run %d: result = {Dirs:%d Files:%d Irregular:%d}, want {3 4 0}",
				run, result.Dirs, result.Files, result.Irregular,
			)
		}
	}
}

// TestScan_SortsSubdirsItself 는 DirLister 반환 순서와 무관하게
// Scanner 자체가 v3 §3.2의 하위 폴더 이름순을 보장하는지 본다.
func TestScan_SortsSubdirsItself(t *testing.T) {
	lister := &fakeLister{fn: func(_ context.Context, dir string) ([]Entry, error) {
		if isDateDir(dir) {
			return []Entry{
				{Name: "13", IsDir: true, Type: os.ModeDir},
				// Type 과 충돌하는 IsDir 를 믿고 링크를 내려가면
				// 날짜 폴더 밖으로 새어 나갈 수 있다.
				{Name: "link", IsDir: true, Type: os.ModeSymlink},
				{Name: "00", IsDir: true, Type: os.ModeDir},
			}, nil
		}

		return []Entry{{Name: "same.rnx", Size: 1}}, nil
	}}

	var dirs []string
	result, err := New(lister).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustScanTemplate(t, "/data/(YYYY)/(DOY)"),
		Range{From: utcDate(2026, time.January, 1), To: utcDate(2026, time.January, 1)},
		func(b Batch) error {
			dirs = append(dirs, b.Dir)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan() unexpected error: %v", err)
	}

	want := []string{"/data/2026/001/00", "/data/2026/001/13"}
	if strings.Join(dirs, ",") != strings.Join(want, ",") {
		t.Fatalf("batch dirs = %v, want %v", dirs, want)
	}
	if result.Irregular != 1 {
		t.Fatalf("Irregular = %d, want 1 (IsDir/Type 충돌 링크)", result.Irregular)
	}
}

// TestScan_JunctionInsideDateDirIsNotFollowed 는 날짜 폴더 안의 junction 을
// 재귀가 따라가지 않고, 파일로도 넘기지 않고, 집계만 하는지 본다.
//
// 운영에서 터지는 지점: 날짜 폴더 안에 보관 루트를 가리키는 링크가 있을 때
// 따라가면 장부에 없는 옛 파일이 전부 신규로 판정되어 대량 재전송되고,
// 파일로 넘기면 폴더가 장부에 올라 매시간 FAILED 가 반복된다. 링크 판정이
// LocalLister 계약(커밋 1)만으로 지켜지는 것이 아니라 Scanner 의 하강
// 조건까지 이어져야 막힌다 (PATH_DESIGN v3 §5.2 ①②).
func TestScan_JunctionInsideDateDirIsNotFollowed(t *testing.T) {
	base := t.TempDir()
	root := filepath.ToSlash(base)

	archive := filepath.Join(base, "archive")
	writeFile(t, filepath.Join(archive, "old2016a.16o"), "old")
	writeFile(t, filepath.Join(archive, "2016", "001", "old2016b.16o"), "old")

	writeFile(t, root+"/2026/001/dbon001a.26o", "today")

	mkJunction(t, filepath.Join(base, "2026", "001", "archive-link"), archive)

	batches, result := scanOneDay(t, root)

	for _, b := range batches {
		for _, e := range b.Entries {
			if strings.HasPrefix(e.Name, "old2016") {
				t.Fatalf("junction 을 따라가 보관 파일 %q 를 수집했다 (Dir=%q)", e.Name, b.Dir)
			}

			if e.Name == "archive-link" {
				t.Fatalf("junction 을 파일로 넘겼다 (Type=%v)", e.Type)
			}
		}
	}

	if len(batches) != 1 || len(batches[0].Entries) != 1 {
		t.Fatalf("batches = %+v, want 날짜 폴더의 dbon001a.26o 하나", batches)
	}

	if result.Irregular != 1 {
		t.Errorf("Irregular = %d, want 1", result.Irregular)
	}

	if result.Dirs != 1 {
		t.Errorf("Dirs = %d, want 1 (junction 아래로 내려가면 안 된다)", result.Dirs)
	}

	if len(result.IrregularDetails) != 1 ||
		!strings.HasSuffix(result.IrregularDetails[0].Path, "archive-link") {
		t.Errorf("IrregularDetails = %+v, want archive-link 1건", result.IrregularDetails)
	}
}
