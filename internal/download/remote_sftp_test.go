package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/scan"
)

// DOWNLOAD 커밋 3 리뷰 — 실제 SFTP 프로토콜 위의 어댑터.
//
// remote_test.go 의 fake 는 FileInfo 를 손으로 만든다. 그러면 "정상 서버가
// 보내는 속성이 어댑터의 두 가드(이름·Mode 0)에 걸리지 않는가"는 시험되지
// 않는다. 가드가 정상 응답에서 발동하면 시각 폴더 전체가 Irregular 로 빠져
// 매 회차 수신 0건이 된다 — 가드가 막으려던 사고와 같은 결과다.
//
// vendor 의 pkg/sftp 서버를 테스트 프로세스 안에서 띄워 READDIR 응답을
// 실제 패킷으로 받는다. SFTPFS 의 비공개 필드는 이 패키지에서 채울 수 없어
// *sftp.Client 위에 얇은 RemoteFS 를 둔다. 없음 판별은 transport 의
// wrapNotExist 와 같은 규칙이다 (그 규칙 자체는 transport 테스트가 고정한다).

type pipeRemote struct {
	client *sftp.Client
}

func (p pipeRemote) ReadDir(ctx context.Context, dir string) ([]fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	infos, err := p.client.ReadDir(dir)
	if err == nil {
		return infos, nil
	}

	var se *sftp.StatusError
	if errors.Is(err, fs.ErrNotExist) ||
		(errors.As(err, &se) && se.FxCode() == sftp.ErrSSHFxNoSuchFile) {
		return nil, fmt.Errorf("read dir: %w: %w", fs.ErrNotExist, err)
	}

	return nil, fmt.Errorf("read dir: %w", err)
}

func (pipeRemote) DownloadPart(context.Context, string, string) error {
	return errors.New("not used by the lister")
}

type rwc struct {
	io.Reader
	io.WriteCloser
}

// newPipeRemote 는 in-process SFTP 서버에 연결된 RemoteFS 를 만든다.
func newPipeRemote(t *testing.T) pipeRemote {
	t.Helper()

	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()

	srv, err := sftp.NewServer(rwc{Reader: c2sR, WriteCloser: s2cW})
	if err != nil {
		t.Fatal(err)
	}

	served := make(chan struct{})

	go func() {
		defer close(served)
		_ = srv.Serve()
		_ = s2cW.Close() // 운영의 SSH 채널 종료 역할 (transport 테스트와 같음)
	}()

	client, err := sftp.NewClientPipe(s2cR, c2sW)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		_ = client.Close()
		_ = c2sW.Close()
		_ = s2cR.Close()

		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("in-process sftp server did not stop")
		}
	})

	return pipeRemote{client: client}
}

// sftpPathOf 는 로컬 절대경로를 in-process 서버의 SFTP 경로로 바꾼다.
// Windows 는 "/C:/..." 형태다 (transport 의 TestPipeSFTP_PathMapping).
func sftpPathOf(t *testing.T, local string) string {
	t.Helper()

	abs, err := filepath.Abs(local)
	if err != nil {
		t.Fatal(err)
	}

	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}

	return p
}

func writeLocal(t *testing.T, p string, data []byte, mtime time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// 정상 서버 응답의 폴더·파일은 어떤 가드에도 걸리지 않고, 시각 폴더 아래
// 파일이 전부 수집되며, 크기·mtime 이 보존된다 (Grace 판정의 입력).
func TestSFTPLister_RealProtocol_NormalEntriesAreNotIrregular(t *testing.T) {
	remote := newPipeRemote(t)
	root := t.TempDir()

	mt := time.Date(2026, 9, 25, 13, 5, 7, 0, time.UTC)

	files := map[string]int{
		"RINEX2/2026/268/00/soch268a.26o.gz": 1234,
		"RINEX2/2026/268/13/soch268n.26o.gz": 99,
		"RINEX2/2026/268/23/palm268x.26d.gz": 0, // 0바이트도 파일이다 (판정은 verify)
		"RINEX2/2026/268/soch2680.26o.gz":    7,
	}

	for rel, size := range files {
		writeLocal(t, filepath.Join(root, filepath.FromSlash(rel)), make([]byte, size), mt)
	}

	tpl := mustTpl(t, sftpPathOf(t, root)+"/RINEX2/(YYYY)/(DOY)/")

	type seen struct {
		size  int64
		mtime time.Time
	}

	got := map[string]seen{}

	res, err := scan.New(newSFTPLister(remote)).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		tpl,
		scan.Range{From: day268, To: day268},
		func(b scan.Batch) error {
			prefix := sftpPathOf(t, root) + "/"

			for _, e := range b.Entries {
				rel := strings.TrimPrefix(strings.TrimRight(b.Dir, "/")+"/"+e.Name, prefix)
				got[rel] = seen{size: e.Size, mtime: e.MTime}
			}

			return nil
		},
	)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if res.Irregular != 0 {
		t.Fatalf("Irregular = %d %v — a guard fired on a normal server response",
			res.Irregular, res.IrregularDetails)
	}

	if res.Errs != 0 || res.Missing != 0 {
		t.Errorf("Errs = %d Missing = %d, want 0 (%v)", res.Errs, res.Missing, res.Failures)
	}

	var gotNames, wantNames []string
	for k := range got {
		gotNames = append(gotNames, k)
	}
	for k := range files {
		wantNames = append(wantNames, k)
	}
	sort.Strings(gotNames)
	sort.Strings(wantNames)

	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("collected =\n%v\nwant\n%v", gotNames, wantNames)
	}

	for rel, size := range files {
		s := got[rel]

		if s.size != int64(size) {
			t.Errorf("%s size = %d, want %d", rel, s.size, size)
		}

		// SFTP v3 의 mtime 은 초 단위다.
		if !s.mtime.Equal(mt) {
			t.Errorf("%s mtime = %v, want %v", rel, s.mtime, mt)
		}
	}
}

// Hot Scan 은 아직 생기지 않은 오늘 날짜 폴더를 매 회차 본다. 실제 서버의
// "없음" 응답이 Errs 로 분류되면 정상 운영에서 매시 오류가 쌓여 진짜 장애가
// 묻힌다. 실제 패킷의 없음 응답이 Missing 으로 끝나야 한다.
func TestSFTPLister_RealProtocol_MissingDateIsMissingNotError(t *testing.T) {
	remote := newPipeRemote(t)
	root := t.TempDir()

	writeLocal(t, filepath.Join(root, "RNX", "2026", "267", "soch2670.26o.gz"), []byte("x"), time.Now())

	res, err := scan.New(newSFTPLister(remote)).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustTpl(t, sftpPathOf(t, root)+"/RNX/(YYYY)/(DOY)/"),
		scan.Range{From: day268.AddDate(0, 0, -1), To: day268},
		func(scan.Batch) error { return nil },
	)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if res.Missing != 1 || res.Errs != 0 || res.Files != 1 {
		t.Fatalf("Missing = %d Errs = %d Files = %d, want 1 0 1 (%v)",
			res.Missing, res.Errs, res.Files, res.Failures)
	}
}

// (SITE) 원본 패턴이 실제 프로토콜의 부모 나열로 동작한다.
func TestSFTPLister_RealProtocol_SitePattern(t *testing.T) {
	remote := newPipeRemote(t)
	root := t.TempDir()
	now := time.Now()

	writeLocal(t, filepath.Join(root, "RNX", "2026", "268", "SOCH", "soch2680.26o.gz"), []byte("x"), now)
	writeLocal(t, filepath.Join(root, "RNX", "2026", "268", "PALM", "palm2680.26o.gz"), []byte("x"), now)
	writeLocal(t, filepath.Join(root, "RNX", "2026", "268", "backup", "old.26o.gz"), []byte("x"), now)

	res, err := scan.New(newSFTPLister(remote)).Scan(
		context.Background(),
		domain.CategoryRINEX2Hourly,
		mustTpl(t, sftpPathOf(t, root)+"/RNX/(YYYY)/(DOY)/(SITE)/"),
		scan.Range{From: day268, To: day268},
		func(scan.Batch) error { return nil },
	)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if res.Files != 2 || res.PatternRoots != 2 || res.Unmatched != 1 || res.Irregular != 0 {
		t.Fatalf("result = %+v", res)
	}
}
