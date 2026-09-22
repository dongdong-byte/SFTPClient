package scan

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLocalLister_List(t *testing.T) {
	root := t.TempDir()

	normalPath := filepath.Join(root, "normal.rnx.gz")
	zeroPath := filepath.Join(root, "zero.rnx.gz")
	partPath := filepath.Join(root, "upload.part")
	subdirPath := filepath.Join(root, "subdir")

	if err := os.WriteFile(
		normalPath,
		[]byte("1234567890"),
		0o600,
	); err != nil {
		t.Fatalf("normal file 생성 실패: %v", err)
	}

	if err := os.WriteFile(
		zeroPath,
		nil,
		0o600,
	); err != nil {
		t.Fatalf("zero file 생성 실패: %v", err)
	}

	if err := os.WriteFile(
		partPath,
		[]byte("partial"),
		0o600,
	); err != nil {
		t.Fatalf("part file 생성 실패: %v", err)
	}

	if err := os.Mkdir(
		subdirPath,
		0o755,
	); err != nil {
		t.Fatalf("subdir 생성 실패: %v", err)
	}

	lister := LocalLister{}

	entries, err := lister.List(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 4 {
		t.Fatalf(
			"entries = %d, want 4",
			len(entries),
		)
	}

	// 이름으로 찾는다.
	// os.ReadDir 의 정렬 순서에 테스트가 불필요하게 의존하지 않는다.
	got := make(map[string]Entry, len(entries))

	for _, entry := range entries {
		if _, exists := got[entry.Name]; exists {
			t.Fatalf(
				"duplicate Entry name %q",
				entry.Name,
			)
		}

		got[entry.Name] = entry
	}

	tests := []struct {
		name        string
		path        string
		isDir       bool
		wantRegular bool
	}{
		{
			name:        "normal.rnx.gz",
			path:        normalPath,
			isDir:       false,
			wantRegular: true,
		},
		{
			name:        "zero.rnx.gz",
			path:        zeroPath,
			isDir:       false,
			wantRegular: true,
		},
		{
			name:        "upload.part",
			path:        partPath,
			isDir:       false,
			wantRegular: true,
		},
		{
			name:        "subdir",
			path:        subdirPath,
			isDir:       true,
			wantRegular: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, ok := got[tt.name]
			if !ok {
				t.Fatalf(
					"Entry %q not found",
					tt.name,
				)
			}

			info, err := os.Stat(tt.path)
			if err != nil {
				t.Fatalf(
					"os.Stat(%q): %v",
					tt.path,
					err,
				)
			}

			if entry.Name != tt.name {
				t.Errorf(
					"Name = %q, want %q",
					entry.Name,
					tt.name,
				)
			}

			if entry.IsDir != tt.isDir {
				t.Errorf(
					"IsDir = %v, want %v",
					entry.IsDir,
					tt.isDir,
				)
			}

			if entry.Size != info.Size() {
				t.Errorf(
					"Size = %d, want %d",
					entry.Size,
					info.Size(),
				)
			}

			if !entry.MTime.Equal(info.ModTime()) {
				t.Errorf(
					"MTime = %v, want %v",
					entry.MTime,
					info.ModTime(),
				)
			}

			// Type 은 관측 사실이다. 일반 파일은 종류 비트가 없고(0),
			// 디렉터리는 fs.ModeDir 를 갖는다.
			if entry.IsRegular() != tt.wantRegular {
				t.Errorf(
					"IsRegular() = %v, want %v (Type=%v)",
					entry.IsRegular(),
					tt.wantRegular,
					entry.Type,
				)
			}

			if wantType := info.Mode().Type(); entry.Type != wantType {
				t.Errorf(
					"Type = %v, want %v",
					entry.Type,
					wantType,
				)
			}
		})
	}

	// Scan 단계에서 0바이트를 제거하면 안 된다.
	if entry := got["zero.rnx.gz"]; entry.Size != 0 {
		t.Errorf(
			"zero.rnx.gz Size = %d, want 0",
			entry.Size,
		)
	}

	// .part 역시 LocalLister 에서는 제거하지 않는다.
	if _, ok := got["upload.part"]; !ok {
		t.Error(".part file was filtered by LocalLister")
	}

	// 하위 디렉터리도 관측 사실로 전달한다.
	if entry := got["subdir"]; !entry.IsDir {
		t.Error("subdir must be returned with IsDir=true")
	}

	// 디렉터리의 IsDir 와 Type 은 같은 사실의 두 표현이므로 어긋나면 안 된다.
	if entry := got["subdir"]; entry.Type&fs.ModeDir == 0 {
		t.Errorf(
			"subdir Type = %v, want fs.ModeDir bit",
			entry.Type,
		)
	}
}

// TestLocalLister_SymlinkIsReportedAsItself 는 링크가 대상이 아니라
// 링크 자신으로 보고되는지 본다 (PATH_DESIGN v3 §5).
//
//   - IsDir = false        → "IsDir 이면 하강" 조건이 링크를 따라가지 않는다
//   - Type 에 ModeSymlink  → "일반 파일만 수집" 조건이 링크를 후보에서 뺀다
//
// 이 lstat 의미가 깨지면 재귀 Scanner 의 방어 계층 ①②(§5.2)가 함께
// 무너지므로 계약으로 고정한다.
//
// Windows 에서 symlink 생성은 관리자 권한 또는 개발자 모드가 필요하다.
// 생성에 실패하면 환경 문제이므로 Skip 한다 (v3 §7.1 T7).
func TestLocalLister_SymlinkIsReportedAsItself(t *testing.T) {
	root := t.TempDir()

	targetFile := filepath.Join(root, "target.rnx.gz")
	targetDir := filepath.Join(root, "targetdir")

	if err := os.WriteFile(
		targetFile,
		[]byte("1234567890"),
		0o600,
	); err != nil {
		t.Fatalf("target file 생성 실패: %v", err)
	}

	if err := os.Mkdir(targetDir, 0o755); err != nil {
		t.Fatalf("target dir 생성 실패: %v", err)
	}

	fileLink := filepath.Join(root, "link-to-file")
	dirLink := filepath.Join(root, "link-to-dir")

	if err := os.Symlink(targetFile, fileLink); err != nil {
		t.Skipf(
			"symlink 생성 불가(권한/개발자 모드 필요), 건너뜀: %v",
			err,
		)
	}

	if err := os.Symlink(targetDir, dirLink); err != nil {
		t.Skipf(
			"dir symlink 생성 불가, 건너뜀: %v",
			err,
		)
	}

	entries, err := (LocalLister{}).List(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	got := make(map[string]Entry, len(entries))
	for _, entry := range entries {
		got[entry.Name] = entry
	}

	for _, name := range []string{"link-to-file", "link-to-dir"} {
		entry, ok := got[name]
		if !ok {
			t.Fatalf("Entry %q not found", name)
		}

		if entry.IsDir {
			t.Errorf(
				"%s: IsDir = true, want false (링크를 폴더로 취급하면 재귀가 따라간다)",
				name,
			)
		}

		if entry.Type&fs.ModeSymlink == 0 {
			t.Errorf(
				"%s: Type = %v, want fs.ModeSymlink bit",
				name,
				entry.Type,
			)
		}

		if entry.IsRegular() {
			t.Errorf(
				"%s: IsRegular() = true, want false (링크를 파일로 취급하면 전송이 매번 실패한다)",
				name,
			)
		}
	}

	// 링크의 대상들 자체는 정상적으로 각자의 종류로 보고된다.
	if entry := got["target.rnx.gz"]; !entry.IsRegular() {
		t.Errorf(
			"target.rnx.gz IsRegular() = false, want true (Type=%v)",
			entry.Type,
		)
	}

	if entry := got["targetdir"]; !entry.IsDir {
		t.Error("targetdir IsDir = false, want true")
	}
}

// mkJunction 은 Windows directory junction 을 만든다.
//
// symlink 와 달리 junction 은 관리자 권한·개발자 모드 없이 만들 수 있어
// 운영 PC 와 같은 일반 계정에서도 이 테스트가 실제로 돈다.
// GNSS 통합서버 이력(2017-07-05, 디스크 부족 시 VHD + mklink)처럼
// 운영 저장소에서 실제로 쓰이는 링크 종류다.
func mkJunction(t *testing.T, link, target string) {
	t.Helper()

	if runtime.GOOS != "windows" {
		t.Skip("junction 은 Windows 전용")
	}

	// 인자를 한 문자열로 붙이고 따옴표를 넣으면 cmd 가 구문을 잘못 읽는다.
	// 인자 배열로 넘기면 경로에 공백이 없는 TempDir 에서 mklink 가 동작한다.
	out, err := exec.Command(
		"cmd", "/c", "mklink", "/J", link, target,
	).CombinedOutput()
	if err != nil {
		// Windows 운영 안전을 지키는 핵심 테스트다. 생성 실패를
		// Skip 하면 junction 판정 회귀가 있어도 CI 가 통과할 수 있다.
		t.Fatalf("junction 생성 실패: %v (%s)", err, out)
	}
}

// TestLocalLister_JunctionIsNeitherDirNorRegular 는 날짜 폴더 안에서
// 발견한 junction 이 폴더도 일반 파일도 아닌 항목으로 보고되는지 본다
// (PATH_DESIGN v3 §5.1).
//
// 운영에서 터지는 지점:
//   - IsDir=true 로 보고되면 재귀가 junction 을 따라가 날짜 폴더 밖
//     (예: 10년치 보관 루트)을 스캔해 대량 재전송한다.
//   - IsRegular=true 로 보고되면 폴더가 "파일"로 장부에 올라 매시간
//     열기 실패 → FAILED 를 반복한다.
//
// Go 1.23 이후 mount point 는 fs.ModeIrregular, GODEBUG=winsymlink=0
// 이면 fs.ModeSymlink 로 보고된다. 어느 쪽이든 이 두 조건만 지키면
// 안전하므로 특정 비트를 계약으로 묶지 않는다.
func TestLocalLister_JunctionIsNeitherDirNorRegular(t *testing.T) {
	root := t.TempDir()

	archive := filepath.Join(root, "archive")
	if err := os.Mkdir(archive, 0o755); err != nil {
		t.Fatalf("archive 생성 실패: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(archive, "old2016.rnx.gz"),
		[]byte("old"),
		0o600,
	); err != nil {
		t.Fatalf("archive 파일 생성 실패: %v", err)
	}

	day := filepath.Join(root, "2026", "265")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatalf("날짜 폴더 생성 실패: %v", err)
	}

	mkJunction(t, filepath.Join(day, "link"), archive)

	entries, err := (LocalLister{}).List(context.Background(), day)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1 (junction 하나)", len(entries))
	}

	e := entries[0]

	if e.Name != "link" {
		t.Errorf(
			"name = %q, want %q (대상 파일명이 보이면 junction 을 따라간 것이다)",
			e.Name,
			"link",
		)
	}

	// os.Stat 은 junction 을 따라 디렉터리로 본다. List 가 Stat 으로
	// 바뀌면 아래 IsDir 검사가 깨져야 하므로, Stat 쪽 사실도 고정한다.
	st, err := os.Stat(filepath.Join(day, "link"))
	if err != nil {
		t.Fatalf("Stat(junction) 실패: %v", err)
	}

	if !st.IsDir() {
		t.Fatal("Stat(junction) IsDir = false, want true (이 OS 에서 Stat 이 junction 을 따라가지 않는다)")
	}

	if e.IsDir {
		t.Errorf("junction IsDir = true, want false (재귀가 날짜 폴더 밖을 따라간다)")
	}

	if e.Type&fs.ModeDir != 0 {
		t.Errorf("junction Type = %v, want no ModeDir bit", e.Type)
	}

	if e.IsRegular() {
		t.Errorf(
			"junction IsRegular() = true, want false (폴더가 파일로 장부에 오른다, Type=%v)",
			e.Type,
		)
	}
}

// TestLocalLister_ParentJunctionIsNotFollowed 는 상위 폴더를 가리키는
// junction 을 한 항목으로만 보고, 밖을 나열하지 않는지 본다.
//
// 운영에서 터지는 지점: 이 항목을 폴더로 보면 재귀가 부모로 올라가
// 멈추지 않거나, 날짜 창 밖의 파일을 새로 발견해 대량 재전송한다.
func TestLocalLister_ParentJunctionIsNotFollowed(t *testing.T) {
	root := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(root, "outside.rnx.gz"),
		[]byte("out"),
		0o600,
	); err != nil {
		t.Fatalf("outside 파일 생성 실패: %v", err)
	}

	day := filepath.Join(root, "2026", "265")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatalf("날짜 폴더 생성 실패: %v", err)
	}

	mkJunction(t, filepath.Join(day, "up"), root)

	entries, err := (LocalLister{}).List(context.Background(), day)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 1 || entries[0].Name != "up" {
		t.Fatalf("entries = %+v, want [up]", entries)
	}

	if entries[0].IsDir || entries[0].IsRegular() {
		t.Errorf(
			"parent junction IsDir=%v IsRegular=%v Type=%v, want neither",
			entries[0].IsDir,
			entries[0].IsRegular(),
			entries[0].Type,
		)
	}
}

// TestLocalLister_HardLinkIsRegularFile 는 하드링크가 일반 파일로
// 남는지 본다.
//
// 운영에서 터지는 지점: 링크를 전부 건너뛰면 같은 파일의 두 번째
// 이름(하드링크)이 전송에서 빠진다. 하드링크는 별도 파일이며
// symlink·junction 이 아니다.
func TestLocalLister_HardLinkIsRegularFile(t *testing.T) {
	root := t.TempDir()

	body := []byte("rinex-body")
	orig := filepath.Join(root, "dbon265a.26o")

	if err := os.WriteFile(orig, body, 0o600); err != nil {
		t.Fatalf("원본 생성 실패: %v", err)
	}

	link := filepath.Join(root, "dbon265b.26o")
	if err := os.Link(orig, link); err != nil {
		t.Fatalf("hard link 생성 실패: %v", err)
	}

	entries, err := (LocalLister{}).List(context.Background(), root)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}

	for _, e := range entries {
		if !e.IsRegular() || e.IsDir {
			t.Errorf(
				"%s: IsRegular=%v IsDir=%v Type=%v, want regular file",
				e.Name,
				e.IsRegular(),
				e.IsDir,
				e.Type,
			)
		}

		if e.Size != int64(len(body)) {
			t.Errorf("%s: Size = %d, want %d", e.Name, e.Size, len(body))
		}
	}
}

// TestLocalLister_ReadOnlyFileIsRegular 는 읽기 전용 속성도 일반
// 파일로 남는지 본다.
//
// 운영에서 터지는 지점: 수신기가 파일을 읽기 전용으로 두면 Mode 에
// 0444 가 실린다. 종류 비트가 아닌 권한 비트까지 보면 전송 후보에서
// 빠진다.
func TestLocalLister_ReadOnlyFileIsRegular(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dbon265a.26o")

	if err := os.WriteFile(path, []byte("rinex"), 0o644); err != nil {
		t.Fatalf("파일 생성 실패: %v", err)
	}

	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("chmod 실패: %v", err)
	}

	entries, err := (LocalLister{}).List(context.Background(), root)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}

	if !entries[0].IsRegular() || entries[0].Type&fs.ModeType != 0 {
		t.Errorf(
			"read-only IsRegular=%v Type=%v, want regular file",
			entries[0].IsRegular(),
			entries[0].Type,
		)
	}
}

// TestLocalLister_RootJunctionIsFollowed 는 설정 경로 자체가 junction 인
// 경우 정상적으로 나열되는지 본다 (PATH_DESIGN v3 §5.3 예외).
//
// 운영에서 터지는 지점: 디스크 증설 후 mklink 로 기존 LocalPath 를
// 새 볼륨에 연결한 설치처에서, 링크 규칙이 루트에까지 적용되면
// 모든 파일이 조용히 사라진다. 링크 규칙은 "안에서 발견한 항목"에만
// 적용되고, 호출자가 직접 지정한 경로는 OS 가 따라가야 한다.
func TestLocalLister_RootJunctionIsFollowed(t *testing.T) {
	root := t.TempDir()

	volume := filepath.Join(root, "newvolume")
	if err := os.Mkdir(volume, 0o755); err != nil {
		t.Fatalf("volume 생성 실패: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(volume, "dbon265a.26o"),
		[]byte("rinex"),
		0o600,
	); err != nil {
		t.Fatalf("파일 생성 실패: %v", err)
	}

	legacy := filepath.Join(root, "RINEX-V2-H")
	mkJunction(t, legacy, volume)

	entries, err := (LocalLister{}).List(context.Background(), legacy)
	if err != nil {
		t.Fatalf("List(junction root) unexpected error: %v", err)
	}

	if len(entries) != 1 || entries[0].Name != "dbon265a.26o" {
		t.Fatalf("entries = %+v, want [dbon265a.26o]", entries)
	}

	if !entries[0].IsRegular() {
		t.Errorf(
			"junction 루트 아래 파일 IsRegular() = false, want true (Type=%v)",
			entries[0].Type,
		)
	}
}

// TestEntry_IsRegular 는 Type 과 IsDir 가 어긋난 Entry 에서도 폴더가
// 일반 파일로 판정되지 않는지 본다.
//
// 운영에서 터지는 지점: Type 을 채우지 않는 DirLister 구현(테스트 fake,
// 향후 원격 구현)이 IsDir=true 만 세우면, Type==0 규칙만으로는
// "폴더이면서 일반 파일"이 되어 Scanner 판정 순서에 따라 폴더가
// 전송 후보로 새어 나간다.
func TestEntry_IsRegular(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  bool
	}{
		{"zero value file", Entry{Name: "a.rnx"}, true},
		{"permission bits only", Entry{Name: "a.rnx", Type: 0o644}, true},
		{"dir via IsDir only (fake)", Entry{Name: "00", IsDir: true}, false},
		{"symlink with dir bit", Entry{Name: "l", IsDir: true, Type: fs.ModeDir | fs.ModeSymlink}, false},
		{"dir via both", Entry{Name: "00", IsDir: true, Type: fs.ModeDir}, false},
		{"symlink", Entry{Name: "l", Type: fs.ModeSymlink}, false},
		{"irregular (junction)", Entry{Name: "j", Type: fs.ModeIrregular}, false},
		{"named pipe", Entry{Name: "p", Type: fs.ModeNamedPipe}, false},
		{"device", Entry{Name: "d", Type: fs.ModeDevice}, false},
		{"socket", Entry{Name: "s", Type: fs.ModeSocket}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.entry.IsRegular(); got != tt.want {
				t.Errorf("IsRegular() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocalLister_EmptyDirectory(t *testing.T) {
	root := t.TempDir()

	entries, err := (LocalLister{}).List(
		context.Background(),
		root,
	)
	if err != nil {
		t.Fatalf("List() unexpected error: %v", err)
	}

	if len(entries) != 0 {
		t.Errorf(
			"entries = %d, want 0",
			len(entries),
		)
	}
}

func TestLocalLister_MissingDirectoryPreservesNotExist(t *testing.T) {
	root := t.TempDir()

	missing := filepath.Join(
		root,
		"does-not-exist",
	)

	_, err := (LocalLister{}).List(
		context.Background(),
		missing,
	)

	if err == nil {
		t.Fatal("missing directory: expected error")
	}

	// Scanner 가 이 판정으로 Missing 을 계산하므로
	// LocalLister 가 오류 종류를 잃어버리면 안 된다.
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf(
			"error = %v, want fs.ErrNotExist",
			err,
		)
	}
}

func TestLocalLister_PathIsFile(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(
		root,
		"not-a-directory.txt",
	)

	if err := os.WriteFile(
		path,
		[]byte("data"),
		0o600,
	); err != nil {
		t.Fatalf("test file 생성 실패: %v", err)
	}

	_, err := (LocalLister{}).List(
		context.Background(),
		path,
	)

	if err == nil {
		t.Fatal("file path: expected error")
	}

	// OS별 구체 오류 문자열까지 계약으로 묶지 않는다.
	// 오류가 발생한다는 사실만 LocalLister 계약이다.
}

func TestLocalLister_ContextAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	cancel()

	root := t.TempDir()

	_, err := (LocalLister{}).List(
		ctx,
		root,
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"List() error = %v, want context.Canceled",
			err,
		)
	}
}

func TestLocalLister_NilContext(t *testing.T) {
	root := t.TempDir()

	_, err := (LocalLister{}).List(
		nil,
		root,
	)

	if err == nil {
		t.Fatal("nil context: expected error")
	}

	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf(
			"error = %v, want ErrInvalidInput",
			err,
		)
	}
}
