package put

// transfer.go 의 상태 전이 순서를 심판하는 테스트다.
//
// 우선순위 (테스트 지시서 2026-08-31):
//   - Rename 이후 실패는 최종 파일을 지우지 않는다 (#9, #10)
//   - preflight 실패 1건이 실행 전체를 중단시키지 않는다 (#16)
//   - IsRetry(FAILED 행) + stale 이 회차를 중단시키지 않는다 (#17 —
//     2026-08-31 교차 리뷰가 잡은 버그의 회귀 심판)
//
// Uploader 는 fake, Ledger 는 실제 SQLite 다. 상태 전이의 심판을
// fake ledger 로 하면 WHERE 가드 같은 실제 결함이 테스트와 함께
// 통과해 버린다.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/pathpl"
)

// ---------------------------------------------------------------------------
// fake Uploader
// ---------------------------------------------------------------------------

// call 은 fake 가 기록하는 호출 하나다.
type call struct {
	Method string
	Path   string // 대표 인자 (Rename 은 oldPath, UploadPart 는 partPath)
}

// fakeUploader 는 put.Uploader 의 테스트 구현이다.
//
//   - 호출을 순서대로 기록한다.
//   - 메서드별 n번째 호출에 오류를 주입할 수 있다.
//   - 목적지는 map[string][]byte 다. UploadPart 는 localPath 를 실제로
//     읽는다 — preflight 와 같은 파일을 보게 하기 위해서다.
//   - Join 은 path.Join('/') 이다. 목적지가 원격이라는 전제를 유지한다.
type fakeUploader struct {
	mu    sync.Mutex
	calls []call
	count map[string]int

	// failAt[method][n] 이 있으면 그 메서드의 n번째(1-base) 호출이
	// 그 오류를 반환한다.
	failAt map[string]map[int]error

	// sizeOverride[path] 가 있으면 Size 가 그 값을 반환한다.
	// (.part / 최종 Size 불일치 케이스용)
	sizeOverride map[string]int64

	// onCall 은 각 호출 직전에 불린다. 취소 주입용이다.
	onCall func(method, p string)

	store map[string][]byte
}

func newFakeUploader() *fakeUploader {
	return &fakeUploader{
		count:        map[string]int{},
		failAt:       map[string]map[int]error{},
		sizeOverride: map[string]int64{},
		store:        map[string][]byte{},
	}
}

func (f *fakeUploader) failOn(method string, nth int, err error) {
	if f.failAt[method] == nil {
		f.failAt[method] = map[int]error{}
	}

	f.failAt[method][nth] = err
}

// enter 는 기록·카운트·주입 오류 판정을 한 곳에서 한다.
func (f *fakeUploader) enter(method, p string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call{Method: method, Path: p})
	f.count[method]++
	n := f.count[method]
	inject := f.failAt[method][n]
	hook := f.onCall
	f.mu.Unlock()

	if hook != nil {
		hook(method, p)
	}

	return inject
}

func (f *fakeUploader) methodCalls(method string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []string

	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c.Path)
		}
	}

	return out
}

func (f *fakeUploader) sequence() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.Method)
	}

	return out
}

func (f *fakeUploader) EnsureDir(_ context.Context, dir string) error {
	return f.enter("EnsureDir", dir)
}

func (f *fakeUploader) UploadPart(
	_ context.Context, localPath, partPath string,
) error {
	if err := f.enter("UploadPart", partPath); err != nil {
		return err
	}

	b, err := os.ReadFile(localPath)
	if err != nil {
		return fmt.Errorf("fake upload read local: %w", err)
	}

	f.mu.Lock()
	f.store[partPath] = b
	f.mu.Unlock()

	return nil
}

func (f *fakeUploader) Size(_ context.Context, p string) (int64, error) {
	if err := f.enter("Size", p); err != nil {
		return 0, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if v, ok := f.sizeOverride[p]; ok {
		return v, nil
	}

	b, ok := f.store[p]
	if !ok {
		return 0, fmt.Errorf("fake stat: %w", fs.ErrNotExist)
	}

	return int64(len(b)), nil
}

func (f *fakeUploader) Rename(_ context.Context, oldPath, newPath string) error {
	if err := f.enter("Rename", oldPath); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	b, ok := f.store[oldPath]
	if !ok {
		return fmt.Errorf("fake rename: %w", fs.ErrNotExist)
	}

	f.store[newPath] = b
	delete(f.store, oldPath)

	return nil
}

func (f *fakeUploader) Remove(_ context.Context, p string) error {
	if err := f.enter("Remove", p); err != nil {
		return err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.store, p)

	return nil
}

func (f *fakeUploader) Join(dir, name string) string {
	return path.Join(dir, name)
}

// 컴파일 타임 계약 확인.
var _ Uploader = (*fakeUploader)(nil)

// ---------------------------------------------------------------------------
// 헬퍼
// ---------------------------------------------------------------------------

const xferTestCat = domain.CategoryRINEX3Hourly

func xferTestDB(t *testing.T) (*ledger.DB, string) {
	t.Helper()

	p := filepath.Join(t.TempDir(), "xfer.db")

	db, err := ledger.Open(context.Background(), p)
	if err != nil {
		t.Fatalf("ledger.Open() 실패: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db, p
}

// seedLocal 은 로컬 원본 파일을 만든다.
func seedLocal(t *testing.T, name, content string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	return p
}

// seedCandidate 는 common_ledger 등록 → revision 확정 → PENDING 등록
// → 전송 준비가 끝난 Candidate 를 만든다.
func seedCandidate(
	t *testing.T, db *ledger.DB, name, content string,
) Candidate {
	t.Helper()

	ctx := context.Background()
	localPath := seedLocal(t, name, content)
	norm := domain.NormalizeName(name)

	if _, err := db.UpsertCommon(ctx, ledger.CommonInput{
		FileName:          norm,
		BaseName:          domain.BaseName(norm),
		Category:          xferTestCat,
		Size:              int64(len(content)),
		MTime:             1_700_000_100,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: 1_700_000_200,
	}); err != nil {
		t.Fatalf("UpsertCommon(%q) 실패: %v", name, err)
	}

	known, err := db.LookupCommon(ctx, xferTestCat, []string{norm})
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	key := ledger.PutKey{
		Category: xferTestCat,
		FileName: norm,
		Revision: known[norm].Revision,
	}

	if err := db.InsertPendingBatch(ctx, []ledger.PutKey{key}); err != nil {
		t.Fatalf("InsertPendingBatch() 실패: %v", err)
	}

	return Candidate{
		Key:       key,
		LocalPath: localPath,
		Size:      int64(len(content)),
		When:      time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),
	}
}

// xferRunner 는 시각이 호출마다 1초씩 전진하는 Runner 다.
// sent_at 과 transfer_verified_at 이 반드시 달라지게 만든다 (#4).
func xferRunner(db *ledger.DB) *Runner {
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	tick := 0

	return &Runner{
		DB: db,
		Opts: RunOptions{
			MaxRetries: 5,
			Logger:     log.New(io.Discard, "", 0),
			Now: func() time.Time {
				tick++
				return base.Add(time.Duration(tick) * time.Second)
			},
		},
	}
}

func xferJobs(t *testing.T) []CategoryJob {
	t.Helper()

	// (HH) 는 제거된 토큰이다 (PATH_DESIGN v3 §1). 하위 시각 폴더는
	// 재귀가 흡수하므로 로컬·원격 모두 (DOY) 까지만 적는다.
	remote, err := pathpl.Parse("out/(YYYY)/(DOY)/")
	if err != nil {
		t.Fatalf("pathpl.Parse() 실패: %v", err)
	}

	local, err := pathpl.Parse("in/(YYYY)/(DOY)/")
	if err != nil {
		t.Fatalf("pathpl.Parse() 실패: %v", err)
	}

	return []CategoryJob{{
		Category:   xferTestCat,
		LocalPath:  local,
		RemotePath: remote,
	}}
}

// putRowRaw 는 put_ledger 행을 별도 연결로 직접 읽는다.
//
// LookupPut 을 쓰지 않는 이유는 검증 대상 API 로 검증하면 그 API 의
// 결함이 테스트와 함께 통과하기 때문이고, remote_path·sent_at 같은
// 컬럼은 LookupPut 이 돌려주지 않기 때문이다. (ledger 는 WAL 이라
// 두 번째 읽기 연결이 안전하다.)
type putRowRaw struct {
	status     string
	attempts   int64
	remotePath sql.NullString
	partPath   sql.NullString
	localSize  sql.NullInt64
	errText    sql.NullString
	sentAt     sql.NullInt64
	verifiedAt sql.NullInt64
}

func readPutRaw(t *testing.T, dbPath string, key ledger.PutKey) putRowRaw {
	t.Helper()

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open 실패: %v", err)
	}
	defer raw.Close()

	var row putRowRaw

	err = raw.QueryRow(
		`SELECT status, attempts, remote_path, part_path, local_size,
		        error, sent_at, transfer_verified_at
		   FROM put_ledger
		  WHERE category = ? AND file_name = ? AND revision = ?;`,
		key.Category.String(), key.FileName, key.Revision,
	).Scan(
		&row.status, &row.attempts, &row.remotePath, &row.partPath,
		&row.localSize, &row.errText, &row.sentAt, &row.verifiedAt,
	)
	if err != nil {
		t.Fatalf("raw read(%q rev=%d) 실패: %v", key.FileName, key.Revision, err)
	}

	return row
}

// ---------------------------------------------------------------------------
// 1.2 정상 경로 (#1 ~ #5)
// ---------------------------------------------------------------------------

func TestTransferHappyPath(t *testing.T) {
	db, dbPath := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db)
	c := seedCandidate(t, db, "HAPPY00KOR_R.rnx.gz", "hello-rinex")

	rep, err := r.Transfer(context.Background(), up, xferJobs(t), []Candidate{c})
	if err != nil {
		t.Fatalf("Transfer() 실패: %v", err)
	}

	// #1 리포트
	if rep.Attempted != 1 || rep.Verified != 1 || rep.Failed != 0 {
		t.Fatalf("rep = %+v, want Attempted=1 Verified=1 Failed=0", rep)
	}

	// #2 호출 순서
	wantSeq := []string{"EnsureDir", "UploadPart", "Size", "Rename", "Size"}
	gotSeq := up.sequence()

	if len(gotSeq) != len(wantSeq) {
		t.Fatalf("호출 순서 = %v, want %v", gotSeq, wantSeq)
	}

	for i := range wantSeq {
		if gotSeq[i] != wantSeq[i] {
			t.Fatalf("호출 순서 = %v, want %v", gotSeq, wantSeq)
		}
	}

	// #3 put_ledger 확정값
	row := readPutRaw(t, dbPath, c.Key)

	if row.status != string(domain.StatusVerified) {
		t.Errorf("status = %q, want VERIFIED", row.status)
	}

	if row.attempts != 1 {
		t.Errorf("attempts = %d, want 1", row.attempts)
	}

	if !row.remotePath.Valid || !strings.HasSuffix(
		row.remotePath.String, "happy00kor_r.rnx.gz",
	) {
		// 원격 파일명은 원본 이름 보존 규칙이지만 이 케이스의 원본이
		// 대문자이므로 basename 은 원본 그대로여야 한다.
		if !strings.HasSuffix(row.remotePath.String, "HAPPY00KOR_R.rnx.gz") {
			t.Errorf("remote_path = %v, 원본 이름 보존 위반", row.remotePath)
		}
	}

	if !row.partPath.Valid ||
		!strings.HasSuffix(row.partPath.String, putPartSuffix) {
		t.Errorf("part_path = %v, want *.part", row.partPath)
	}

	if !row.localSize.Valid || row.localSize.Int64 != c.Size {
		t.Errorf("local_size = %v, want %d", row.localSize, c.Size)
	}

	// #4 sent_at ≠ transfer_verified_at — 배선 생존 확인
	if !row.sentAt.Valid || !row.verifiedAt.Valid {
		t.Fatalf("sent_at/transfer_verified_at 이 NULL 이다: %+v", row)
	}

	if row.sentAt.Int64 == row.verifiedAt.Int64 {
		t.Errorf(
			"sent_at == transfer_verified_at (%d) — res.SentAt 배선이 죽었다",
			row.sentAt.Int64,
		)
	}

	// #5 목적지에 최종 파일만 있고 .part 는 없다
	if len(up.store) != 1 {
		t.Fatalf("목적지 파일 수 = %d, want 1 (%v)", len(up.store), up.store)
	}

	for p := range up.store {
		if strings.HasSuffix(p, putPartSuffix) {
			t.Errorf(".part 가 목적지에 남았다: %s", p)
		}
	}
}

// ---------------------------------------------------------------------------
// 1.3 실패 분기 — .part 정리 규칙 (#6 ~ #12)
// ---------------------------------------------------------------------------

func TestTransferFailureCleanup(t *testing.T) {
	tests := []struct {
		name string
		// setup 은 오류 주입. 반환값은 이 케이스의 추가 검증.
		setup      func(up *fakeUploader, c Candidate, finalPath string)
		wantRemove bool // Remove(partPath) 호출 여부 — 이 표의 심장
		wantRename bool // Rename 호출 여부
	}{
		{
			// #6
			name: "UploadPart 실패",
			setup: func(up *fakeUploader, _ Candidate, _ string) {
				up.failOn("UploadPart", 1, fmt.Errorf("boom"))
			},
			wantRemove: true,
			wantRename: false,
		},
		{
			// #7
			name: ".part Size 불일치",
			setup: func(up *fakeUploader, c Candidate, finalPath string) {
				up.sizeOverride[finalPath+putPartSuffix] = c.Size + 999
			},
			wantRemove: true,
			wantRename: false,
		},
		{
			// #8
			name: "Rename 실패",
			setup: func(up *fakeUploader, _ Candidate, _ string) {
				up.failOn("Rename", 1, fmt.Errorf("rename boom"))
			},
			wantRemove: true,
			wantRename: true,
		},
		{
			// #9 — 이 파일에서 제일 중요한 케이스 ①
			name: "Rename 성공 후 final Size 조회 실패",
			setup: func(up *fakeUploader, _ Candidate, _ string) {
				up.failOn("Size", 2, fmt.Errorf("stat boom"))
			},
			wantRemove: false,
			wantRename: true,
		},
		{
			// #10 — 이 파일에서 제일 중요한 케이스 ②
			name: "Rename 성공 후 final Size 불일치",
			setup: func(up *fakeUploader, c Candidate, finalPath string) {
				up.sizeOverride[finalPath] = c.Size + 1
			},
			wantRemove: false,
			wantRename: true,
		},
		{
			// #11
			name: "EnsureDir 실패",
			setup: func(up *fakeUploader, _ Candidate, _ string) {
				up.failOn("EnsureDir", 1, fmt.Errorf("mkdir boom"))
			},
			wantRemove: true, // BeginPut 이후이므로 정리 경로를 탄다
			wantRename: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, dbPath := xferTestDB(t)
			up := newFakeUploader()
			r := xferRunner(db)
			c := seedCandidate(t, db, "fail001.rnx.gz", "payload")

			jobs := xferJobs(t)
			finalPath := up.Join(
				jobs[0].RemotePath.Expand(c.When),
				filepath.Base(c.LocalPath),
			)

			tt.setup(up, c, finalPath)

			rep, err := r.Transfer(
				context.Background(), up, jobs, []Candidate{c},
			)
			if err != nil {
				t.Fatalf("개별 실패가 실행을 중단시켰다: %v", err)
			}

			if rep.Attempted != 1 || rep.Failed != 1 || rep.Verified != 0 {
				t.Fatalf(
					"rep = %+v, want Attempted=1 Failed=1 Verified=0", rep,
				)
			}

			row := readPutRaw(t, dbPath, c.Key)
			if row.status != string(domain.StatusFailed) {
				t.Errorf("status = %q, want FAILED", row.status)
			}

			if row.attempts != 1 {
				t.Errorf("attempts = %d, want 1 (BeginPut 성공 후)", row.attempts)
			}

			gotRemove := len(up.methodCalls("Remove")) > 0
			if gotRemove != tt.wantRemove {
				t.Errorf(
					"Remove 호출 = %v, want %v — Renamed 분기가 죽었다",
					gotRemove, tt.wantRemove,
				)
			}

			gotRename := len(up.methodCalls("Rename")) > 0
			if gotRename != tt.wantRename {
				t.Errorf("Rename 호출 = %v, want %v", gotRename, tt.wantRename)
			}
		})
	}
}

// #12 — Remove 까지 실패해도 FAILED 로 확정되고 두 사유가 모두 남는다.
func TestTransferCleanupFailureStillFails(t *testing.T) {
	db, dbPath := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db)
	c := seedCandidate(t, db, "fail002.rnx.gz", "payload")

	up.failOn("UploadPart", 1, fmt.Errorf("upload-cause"))
	up.failOn("Remove", 1, fmt.Errorf("remove-cause"))

	rep, err := r.Transfer(context.Background(), up, xferJobs(t), []Candidate{c})
	if err != nil {
		t.Fatalf("Transfer() 가 중단됐다: %v", err)
	}

	if rep.Failed != 1 {
		t.Fatalf("Failed = %d, want 1", rep.Failed)
	}

	row := readPutRaw(t, dbPath, c.Key)

	if row.status != string(domain.StatusFailed) {
		t.Fatalf("status = %q, want FAILED", row.status)
	}

	for _, want := range []string{"upload-cause", "remove-cause"} {
		if !row.errText.Valid || !strings.Contains(row.errText.String, want) {
			t.Errorf("error 컬럼에 %q 가 없다: %v", want, row.errText)
		}
	}
}

// ---------------------------------------------------------------------------
// 1.4 preflight — 실행이 중단되면 안 된다 (#13, #14, #16, #17)
// ---------------------------------------------------------------------------

// #13 — 로컬 파일 없음: PENDING 유지, error 없음.
func TestTransferPreflightMissingKeepsPending(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	c := seedCandidate(t, db, "gone001.rnx.gz", "payload")

	if err := os.Remove(c.LocalPath); err != nil {
		t.Fatal(err)
	}

	rep, err := r.Transfer(
		context.Background(), newFakeUploader(), xferJobs(t), []Candidate{c},
	)
	if err != nil {
		t.Fatalf("파일 없음이 실행을 중단시켰다: %v", err)
	}

	if rep.SkippedPreflight != 1 || rep.Attempted != 0 {
		t.Fatalf("rep = %+v, want SkippedPreflight=1 Attempted=0", rep)
	}

	row := readPutRaw(t, dbPath, c.Key)

	if row.status != string(domain.StatusPending) {
		t.Errorf("status = %q, want PENDING (고아 재개 대상 유지)", row.status)
	}

	if row.attempts != 0 {
		t.Errorf("attempts = %d, want 0 (preflight 는 예산 미소모)", row.attempts)
	}
}

// #14 — Size 변경: PENDING → FAILED, attempts 미소모.
func TestTransferPreflightStaleFailsPending(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	c := seedCandidate(t, db, "stale001.rnx.gz", "payload")

	// Scan 이후 파일이 커졌다.
	if err := os.WriteFile(
		c.LocalPath, []byte("payload-grown"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	rep, err := r.Transfer(
		context.Background(), newFakeUploader(), xferJobs(t), []Candidate{c},
	)
	if err != nil {
		t.Fatalf("stale 이 실행을 중단시켰다: %v", err)
	}

	if rep.SkippedPreflight != 1 || rep.Attempted != 0 {
		t.Fatalf("rep = %+v, want SkippedPreflight=1 Attempted=0", rep)
	}

	row := readPutRaw(t, dbPath, c.Key)

	if row.status != string(domain.StatusFailed) {
		t.Errorf("status = %q, want FAILED (옛 revision PENDING 정리)", row.status)
	}

	if row.attempts != 0 {
		t.Errorf("attempts = %d, want 0", row.attempts)
	}

	// FailPut 의 사유에 preflight 판정 문구가 남는다.
	// (errPreflightStale 의 메시지. 정확 일치가 아니라 포함 검사 —
	// 문구 다듬기가 테스트를 깨지 않게 한다.)
	if !row.errText.Valid ||
		!strings.Contains(row.errText.String, "no longer matches") {
		t.Errorf("error 컬럼 = %v, preflight 사유 누락", row.errText)
	}
}

// #16 — 회귀의 핵심: 3건 중 1번째 preflight 실패, 나머지는 전송된다.
func TestTransferPreflightFailureDoesNotStall(t *testing.T) {
	db, _ := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db)

	c1 := seedCandidate(t, db, "aaa001.rnx.gz", "one")
	c2 := seedCandidate(t, db, "bbb002.rnx.gz", "two")
	c3 := seedCandidate(t, db, "ccc003.rnx.gz", "three")

	// 1번째만 stale.
	if err := os.WriteFile(c1.LocalPath, []byte("one-grown"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := r.Transfer(
		context.Background(), up, xferJobs(t), []Candidate{c1, c2, c3},
	)
	if err != nil {
		t.Fatalf("Transfer() 중단: %v", err)
	}

	if rep.Verified != 2 {
		t.Fatalf(
			"Verified = %d, want 2 — 영구 정체 버그가 되돌아왔다 (rep=%+v)",
			rep.Verified, rep,
		)
	}

	if rep.SkippedPreflight != 1 {
		t.Errorf("SkippedPreflight = %d, want 1", rep.SkippedPreflight)
	}
}

// #17 — 2026-08-31 교차 리뷰가 잡은 버그의 회귀 심판.
//
// IsRetry 후보(put_ledger 가 이미 FAILED)가 preflight stale 이 되어도
// FailPut 을 부르지 않고(전이 위반 → 회차 중단이 되므로) 건너뛴다.
// 뒤 후보는 계속 전송된다.
func TestTransferRetryStaleSkipsWithoutFailPut(t *testing.T) {
	db, dbPath := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db)

	retry := seedCandidate(t, db, "retry01.rnx.gz", "payload")

	// FAILED attempts=1 상태를 실제 전이로 만든다.
	ctx := context.Background()
	if err := db.BeginPut(
		ctx, retry.Key, "/out/x", "/out/x.part", retry.Size, 5,
	); err != nil {
		t.Fatalf("BeginPut() 실패: %v", err)
	}

	if err := db.FailPut(ctx, retry.Key, "first failure"); err != nil {
		t.Fatalf("FailPut() 실패: %v", err)
	}

	retry.IsRetry = true

	// stale 로 만든다.
	if err := os.WriteFile(
		retry.LocalPath, []byte("payload-grown"), 0o644,
	); err != nil {
		t.Fatal(err)
	}

	healthy := seedCandidate(t, db, "healthy1.rnx.gz", "fine")

	rep, err := r.Transfer(
		ctx, up, xferJobs(t), []Candidate{retry, healthy},
	)
	if err != nil {
		t.Fatalf("IsRetry+stale 이 회차를 중단시켰다 (버그 회귀): %v", err)
	}

	if rep.SkippedPreflight != 1 || rep.Verified != 1 {
		t.Fatalf("rep = %+v, want SkippedPreflight=1 Verified=1", rep)
	}

	row := readPutRaw(t, dbPath, retry.Key)

	if row.status != string(domain.StatusFailed) {
		t.Errorf("retry row status = %q, want FAILED 유지", row.status)
	}

	if row.attempts != 1 {
		t.Errorf("retry row attempts = %d, want 1 유지", row.attempts)
	}

	if !row.errText.Valid || row.errText.String != "first failure" {
		t.Errorf(
			"error 컬럼이 덮였다: %v (최초 실패 사유가 보존되어야 한다)",
			row.errText,
		)
	}
}

// ---------------------------------------------------------------------------
// 1.5 EnsureDir 캐시 (#18, #19, #20)
// ---------------------------------------------------------------------------

func TestTransferEnsureDirCache(t *testing.T) {
	t.Run("같은 dir 5건 → 1회", func(t *testing.T) {
		db, _ := xferTestDB(t)
		up := newFakeUploader()
		r := xferRunner(db)

		cands := make([]Candidate, 0, 5)
		for i := 0; i < 5; i++ {
			cands = append(cands, seedCandidate(
				t, db, fmt.Sprintf("same%03d.rnx.gz", i), "x",
			))
			// 같은 When → 같은 remoteDir.
		}

		if _, err := r.Transfer(
			context.Background(), up, xferJobs(t), cands,
		); err != nil {
			t.Fatal(err)
		}

		if got := len(up.methodCalls("EnsureDir")); got != 1 {
			t.Fatalf("EnsureDir 호출 = %d, want 1", got)
		}
	})

	t.Run("다른 dir 3건 → 3회", func(t *testing.T) {
		db, _ := xferTestDB(t)
		up := newFakeUploader()
		r := xferRunner(db)

		var cands []Candidate
		for i := 0; i < 3; i++ {
			c := seedCandidate(t, db, fmt.Sprintf("hour%03d.rnx.gz", i), "x")
			c.When = c.When.AddDate(0, 0, i) // (DOY) 가 달라진다
			cands = append(cands, c)
		}

		if _, err := r.Transfer(
			context.Background(), up, xferJobs(t), cands,
		); err != nil {
			t.Fatal(err)
		}

		if got := len(up.methodCalls("EnsureDir")); got != 3 {
			t.Fatalf("EnsureDir 호출 = %d, want 3", got)
		}
	})

	t.Run("실패한 dir 는 캐시에 안 남아 재시도된다", func(t *testing.T) {
		db, _ := xferTestDB(t)
		up := newFakeUploader()
		r := xferRunner(db)

		c1 := seedCandidate(t, db, "cache01.rnx.gz", "x")
		c2 := seedCandidate(t, db, "cache02.rnx.gz", "x")

		up.failOn("EnsureDir", 1, fmt.Errorf("transient"))

		rep, err := r.Transfer(
			context.Background(), up, xferJobs(t), []Candidate{c1, c2},
		)
		if err != nil {
			t.Fatal(err)
		}

		if got := len(up.methodCalls("EnsureDir")); got != 2 {
			t.Fatalf("EnsureDir 호출 = %d, want 2 (실패는 캐시 안 됨)", got)
		}

		if rep.Failed != 1 || rep.Verified != 1 {
			t.Fatalf("rep = %+v, want Failed=1 Verified=1", rep)
		}
	})
}

// ---------------------------------------------------------------------------
// 1.6 취소와 조립 오류 (#21 ~ #26)
// ---------------------------------------------------------------------------

// #21 — 파일 사이 취소: 뒤 후보는 BeginPut 조차 안 된다.
func TestTransferCancelBetweenCandidates(t *testing.T) {
	db, dbPath := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db)

	c1 := seedCandidate(t, db, "cxl001.rnx.gz", "x")
	c2 := seedCandidate(t, db, "cxl002.rnx.gz", "x")
	c3 := seedCandidate(t, db, "cxl003.rnx.gz", "x")

	ctx, cancel := context.WithCancel(context.Background())

	// 첫 파일의 마지막 원격 작업(final Size) 직후 취소.
	up.onCall = func(method, _ string) {
		if method == "Size" && up.count["Size"] == 2 {
			cancel()
		}
	}

	rep, err := r.Transfer(ctx, up, xferJobs(t), []Candidate{c1, c2, c3})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	if rep.Verified != 1 {
		t.Fatalf("Verified = %d, want 1 (첫 파일은 완주)", rep.Verified)
	}

	// c2, c3 은 손대지 않았다 — PENDING attempts=0.
	for _, c := range []Candidate{c2, c3} {
		row := readPutRaw(t, dbPath, c.Key)
		if row.status != string(domain.StatusPending) || row.attempts != 0 {
			t.Errorf(
				"%s: status=%q attempts=%d, want PENDING/0 (BeginPut 미호출)",
				c.Key.FileName, row.status, row.attempts,
			)
		}
	}
}

// #22 — 전송 도중 취소: WithoutCancel 덕에 FAILED 확정은 된다.
func TestTransferCancelDuringUploadStillFailsLedger(t *testing.T) {
	db, dbPath := xferTestDB(t)
	up := newFakeUploader()
	r := xferRunner(db)
	c := seedCandidate(t, db, "cxl004.rnx.gz", "x")

	ctx, cancel := context.WithCancel(context.Background())

	up.onCall = func(method, _ string) {
		if method == "UploadPart" {
			cancel()
		}
	}
	up.failOn("UploadPart", 1, context.Canceled)

	_, err := r.Transfer(ctx, up, xferJobs(t), []Candidate{c})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (실행 중단)", err)
	}

	row := readPutRaw(t, dbPath, c.Key)
	if row.status != string(domain.StatusFailed) {
		t.Errorf(
			"status = %q, want FAILED — WithoutCancel 이 동작하지 않는다",
			row.status,
		)
	}
}

func TestTransferAssemblyErrors(t *testing.T) {
	t.Run("nil uploader", func(t *testing.T) {
		db, _ := xferTestDB(t)
		r := xferRunner(db)

		rep, err := r.Transfer(context.Background(), nil, xferJobs(t), nil)
		if err == nil {
			t.Fatal("nil uploader 가 통과했다")
		}

		if rep.Elapsed == 0 {
			t.Error("Elapsed 가 0 — defer 채움이 죽었다") // #23, #26
		}
	})

	t.Run("후보 category 가 jobs 에 없음", func(t *testing.T) {
		db, _ := xferTestDB(t)
		r := xferRunner(db)
		c := seedCandidate(t, db, "orphan1.rnx.gz", "x")
		c.Key.Category = domain.CategoryRINEX2Daily // jobs 에 없는 카테고리

		_, err := r.Transfer(
			context.Background(), newFakeUploader(), xferJobs(t),
			[]Candidate{c},
		)
		if err == nil {
			t.Fatal("category 불일치가 통과했다") // #24
		}
	})

	t.Run("중복 category job", func(t *testing.T) {
		db, _ := xferTestDB(t)
		r := xferRunner(db)
		jobs := append(xferJobs(t), xferJobs(t)...)

		_, err := r.Transfer(
			context.Background(), newFakeUploader(), jobs, nil,
		)
		if err == nil {
			t.Fatal("중복 category 가 통과했다") // #25
		}
	})

	t.Run("nil RemotePath", func(t *testing.T) {
		db, _ := xferTestDB(t)
		r := xferRunner(db)
		jobs := xferJobs(t)
		jobs[0].RemotePath = nil

		_, err := r.Transfer(
			context.Background(), newFakeUploader(), jobs, nil,
		)
		if err == nil {
			t.Fatal("nil RemotePath 가 통과했다")
		}
	})
}

// ---------------------------------------------------------------------------
// 순수 함수와 규칙 드리프트 (#27, #28, partSuffix)
// ---------------------------------------------------------------------------

func TestFlattenErr(t *testing.T) {
	joined := errors.Join(fmt.Errorf("first"), fmt.Errorf("second"))

	got := flattenErr(joined)
	if strings.Contains(got, "\n") {
		t.Errorf("flattenErr() 에 개행이 남았다: %q", got)
	}

	for _, want := range []string{"first", "second"} {
		if !strings.Contains(got, want) {
			t.Errorf("flattenErr() = %q, %q 누락", got, want)
		}
	}
}

func TestPreflightLocal(t *testing.T) {
	t.Run("파일 없음은 stale 이 아니다", func(t *testing.T) {
		err := preflightLocal(Candidate{
			LocalPath: filepath.Join(t.TempDir(), "no-such"),
			Size:      1,
		})
		if err == nil {
			t.Fatal("없는 파일이 통과했다")
		}

		if errors.Is(err, errPreflightStale) {
			t.Errorf("파일 없음이 stale 로 분류됐다: %v", err) // #28
		}
	})

	t.Run("Size 변경은 stale", func(t *testing.T) {
		p := seedLocal(t, "sz.rnx.gz", "grown-content")

		err := preflightLocal(Candidate{LocalPath: p, Size: 3})
		if !errors.Is(err, errPreflightStale) {
			t.Errorf("Size 변경 = %v, want errPreflightStale", err)
		}
	})

	t.Run("디렉터리는 stale", func(t *testing.T) {
		err := preflightLocal(Candidate{LocalPath: t.TempDir(), Size: 0})
		if !errors.Is(err, errPreflightStale) {
			t.Errorf("디렉터리 = %v, want errPreflightStale", err) // #15
		}
	})
}

// domain 과 put 의 .part 규칙이 갈리면 .part 가 후보로 새어 들어간다.
// 값을 export 하지 않고 드리프트만 심판한다 (2026-08-31 판정).
func TestPartSuffixMatchesDomain(t *testing.T) {
	if !domain.IsPartFile("x" + putPartSuffix) {
		t.Fatal("domain.IsPartFile 과 putPartSuffix 가 어긋났다")
	}
}
