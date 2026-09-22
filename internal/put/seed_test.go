package put

// seed.go 와 Run(SeedMode) 의 계약을 심판한다.
//
// 심판 목록:
//   #S1 원격 일치 → VERIFIED 등록 (sent_at NULL, attempts 0)
//   #S2 원격 없음 → 미seed (RemoteMissing) — 이후 live 가 전송
//   #S3 Size 불일치 → 미seed (SizeMismatch) — 이후 live 가 덮어씀
//   #S4 재실행 멱등 — VERIFIED 는 Run 이 후보에서 제외 (candidates=0)
//   #S5 SeedMode 는 MaxFilesPerRun 절단을 무시
//   #S6 SeedMode 는 PENDING 을 등록하지 않는다 (장부 행 = seed 행뿐)
//   #S7 원격 예상 밖 오류 → 실행 중단 (멱등이므로 재실행 무비용)
//   #S8 seed 후 첫 live 실행: 일치분 candidates 제외, 불일치분만 전송
//       — 설치 시나리오의 최종 증명

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"io"
	"log"

	"SFTPClient/internal/pathpl"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

// seedEnv 는 로컬 스캔 디렉터리 + 원격 store + DB 를 갖춘 seed 실행
// 환경이다. Run 이 실제 스캔을 타야 하므로 LocalLister 를 쓴다.
type seedEnv struct {
	db     *ledger.DB
	dbPath string
	up     *fakeUploader
	runner *Runner
	jobs   []CategoryJob

	localRoot string
	when      time.Time
}

func newSeedEnv(t *testing.T) *seedEnv {
	t.Helper()

	db, dbPath := xferTestDB(t)

	e := &seedEnv{
		db:        db,
		dbPath:    dbPath,
		up:        newFakeUploader(),
		localRoot: t.TempDir(),
		when:      time.Now().UTC(),
	}

	// 로컬은 (DOY) 까지만 적는다. putLocal 은 파일을 시각 하위 폴더에
	// 두므로, seed 테스트 전체가 실제 LocalLister 로 서울시형 재귀
	// 수집을 거친다 (PATH_DESIGN v3 §1).
	local, err := pathpl.Parse(e.localRoot + "/(YYYY)/(DOY)/")
	if err != nil {
		t.Fatalf("local template: %v", err)
	}

	remote, err := pathpl.Parse("rem/(YYYY)/(DOY)/")
	if err != nil {
		t.Fatalf("remote template: %v", err)
	}

	e.jobs = []CategoryJob{{
		Category:   xferTestCat,
		LocalPath:  local,
		RemotePath: remote,
	}}

	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	tick := 0

	e.runner = &Runner{
		Scanner:  scan.New(scan.LocalLister{}),
		DB:       db,
		Verifier: verify.Verifier{Grace: 60 * time.Second},
		Opts: RunOptions{
			MaxRetries:     5,
			MaxFilesPerRun: 2000,
			SeedMode:       true,
			Logger:         log.New(io.Discard, "", 0),
			Now: func() time.Time {
				tick++
				return base.Add(time.Duration(tick) * time.Second)
			},
		},
	}

	return e
}

// putLocal 은 스캔 창(오늘) 디렉터리에 파일을 만들고, mtime 을 Grace
// 밖(5분 전)으로 민다.
func (e *seedEnv) putLocal(t *testing.T, name, content string) {
	t.Helper()

	dir := filepath.Join(
		e.localRoot,
		e.when.Format("2006"),
		fmt.Sprintf("%03d", e.when.YearDay()),
		fmt.Sprintf("%02d", e.when.Hour()),
	)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
}

// remoteFinal 은 이 파일의 예상 finalPath(원격 대소문자 보존)다.
func (e *seedEnv) remoteFinal(name string) string {
	return fmt.Sprintf(
		"rem/%s/%03d/%s",
		e.when.Format("2006"),
		e.when.YearDay(),
		name,
	)
}

// runSeed 는 seed 모드 전체(Run + Seed)를 실행한다.
func (e *seedEnv) runSeed(t *testing.T) (SeedReport, []Candidate) {
	t.Helper()

	rng := scan.Range{From: e.when, To: e.when}

	kept, _, err := e.runner.Run(context.Background(), e.jobs, rng)
	if err != nil {
		t.Fatalf("Run(SeedMode) 오류: %v", err)
	}

	rep, err := e.runner.Seed(context.Background(), e.up, e.jobs, kept)
	if err != nil {
		t.Fatalf("Seed() 오류: %v", err)
	}

	return rep, kept
}

// #S1 + #S6 — 원격 일치분은 VERIFIED(sent_at NULL, attempts 0)로
// 등록되고, PENDING 행은 하나도 생기지 않는다.
func TestSeedMatchRegistersVerified(t *testing.T) {
	e := newSeedEnv(t)

	e.putLocal(t, "MTCH00KOR_R_20260010000_01H_01S_MS.rnx.gz", "same-bytes")
	e.up.store[e.remoteFinal("MTCH00KOR_R_20260010000_01H_01S_MS.rnx.gz")] =
		[]byte("same-bytes")

	rep, kept := e.runSeed(t)

	if rep.Seeded != 1 || rep.RemoteMissing != 0 || rep.SizeMismatch != 0 {
		t.Fatalf("rep = %+v, want Seeded=1", rep)
	}

	row := readPutRaw(t, e.dbPath, kept[0].Key)

	if row.status != string(domain.StatusVerified) {
		t.Errorf("status = %q, want VERIFIED", row.status)
	}

	if row.sentAt.Valid {
		t.Errorf("sent_at = %v, want NULL (seed 는 전송 시각을 모른다)",
			row.sentAt.Int64)
	}

	if row.attempts != 0 {
		t.Errorf("attempts = %d, want 0 (PUT 착수 이력 없음)", row.attempts)
	}

	if !row.verifiedAt.Valid {
		t.Error("transfer_verified_at 이 기록되어야 한다 (대조 시각)")
	}

	// #S6: put_ledger 전체 행 수 = seed 행 1개뿐 (PENDING 없음)
	if n := countPutRows(t, e.dbPath); n != 1 {
		t.Errorf("put_ledger 행 수 = %d, want 1 (PENDING 미등록)", n)
	}
}

// #S2 — 원격에 없는 파일은 seed 하지 않는다.
func TestSeedRemoteMissingNotSeeded(t *testing.T) {
	e := newSeedEnv(t)

	e.putLocal(t, "MISS00KOR_R_20260010000_01H_01S_MS.rnx.gz", "local-only")

	rep, _ := e.runSeed(t)

	if rep.Seeded != 0 || rep.RemoteMissing != 1 {
		t.Fatalf("rep = %+v, want RemoteMissing=1 Seeded=0", rep)
	}

	if n := countPutRows(t, e.dbPath); n != 0 {
		t.Errorf("put_ledger 행 수 = %d, want 0", n)
	}
}

// #S3 — 원격 Size 불일치는 seed 하지 않는다 (불완전 전송 잔재 방어).
func TestSeedSizeMismatchNotSeeded(t *testing.T) {
	e := newSeedEnv(t)

	e.putLocal(t, "MSMT00KOR_R_20260010000_01H_01S_MS.rnx.gz", "full-content")
	e.up.store[e.remoteFinal("MSMT00KOR_R_20260010000_01H_01S_MS.rnx.gz")] =
		[]byte("trunc")

	rep, _ := e.runSeed(t)

	if rep.Seeded != 0 || rep.SizeMismatch != 1 {
		t.Fatalf("rep = %+v, want SizeMismatch=1 Seeded=0", rep)
	}
}

// #S4 — seed 재실행은 멱등이다. VERIFIED 행은 Run 이 후보에서 제외한다.
func TestSeedIdempotent(t *testing.T) {
	e := newSeedEnv(t)

	e.putLocal(t, "IDEM00KOR_R_20260010000_01H_01S_MS.rnx.gz", "payload")
	e.up.store[e.remoteFinal("IDEM00KOR_R_20260010000_01H_01S_MS.rnx.gz")] =
		[]byte("payload")

	rep1, _ := e.runSeed(t)
	if rep1.Seeded != 1 {
		t.Fatalf("1차 Seeded = %d, want 1", rep1.Seeded)
	}

	rep2, _ := e.runSeed(t)
	if rep2.Candidates != 0 || rep2.Seeded != 0 {
		t.Fatalf("2차 rep = %+v, want Candidates=0 (VERIFIED 제외)", rep2)
	}
}

// #S5 — SeedMode 는 MaxFilesPerRun 절단을 무시한다.
// 7일 창 113k 파일에서 2000건만 seed 되는 사고의 회귀 심판이다.
func TestSeedIgnoresMaxFilesPerRun(t *testing.T) {
	e := newSeedEnv(t)
	e.runner.Opts.MaxFilesPerRun = 1 // 절단이 적용된다면 1건만 남는다

	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("CUT%d00KOR_R_20260010000_01H_01S_MS.rnx.gz", i)
		e.putLocal(t, name, "x")
		e.up.store[e.remoteFinal(name)] = []byte("x")
	}

	rep, kept := e.runSeed(t)

	if len(kept) != 3 {
		t.Fatalf("kept = %d, want 3 (SeedMode 는 절단 무시)", len(kept))
	}

	if rep.Seeded != 3 {
		t.Fatalf("Seeded = %d, want 3", rep.Seeded)
	}
}

// #S7 — 원격의 예상 밖 오류(권한 등)는 실행 중단이다.
func TestSeedUnexpectedStatErrorAborts(t *testing.T) {
	e := newSeedEnv(t)

	e.putLocal(t, "PERM00KOR_R_20260010000_01H_01S_MS.rnx.gz", "x")

	permErr := errors.New("permission denied")
	e.up.failOn("Size", 1, permErr)

	rng := scan.Range{From: e.when, To: e.when}
	kept, _, err := e.runner.Run(context.Background(), e.jobs, rng)
	if err != nil {
		t.Fatalf("Run 오류: %v", err)
	}

	_, err = e.runner.Seed(context.Background(), e.up, e.jobs, kept)
	if !errors.Is(err, permErr) {
		t.Fatalf("err = %v, want %v 포함 (중단)", err, permErr)
	}
}

// #S8 — 설치 시나리오의 최종 증명.
//
// 원격에 이미 있는 파일 2개(일치 1 + 불일치 1) + 원격에 없는 파일 1개
// → seed → 첫 live 실행 → 일치분은 후보 제외(excluded verified),
// 불일치분과 미존재분만 전송된다. 중복 전송 0.
func TestSeedThenFirstLiveRun(t *testing.T) {
	e := newSeedEnv(t)

	e.putLocal(t, "OKAY00KOR_R_20260010000_01H_01S_MS.rnx.gz", "already-sent")
	e.up.store[e.remoteFinal("OKAY00KOR_R_20260010000_01H_01S_MS.rnx.gz")] =
		[]byte("already-sent")

	e.putLocal(t, "STAL00KOR_R_20260010000_01H_01S_MS.rnx.gz", "new-content!")
	e.up.store[e.remoteFinal("STAL00KOR_R_20260010000_01H_01S_MS.rnx.gz")] =
		[]byte("old")

	e.putLocal(t, "NEWF00KOR_R_20260010000_01H_01S_MS.rnx.gz", "never-sent")

	rep, _ := e.runSeed(t)
	if rep.Seeded != 1 || rep.SizeMismatch != 1 || rep.RemoteMissing != 1 {
		t.Fatalf("seed rep = %+v, want 1/1/1", rep)
	}

	// ── 첫 live 실행 ──
	e.runner.Opts.SeedMode = false

	rng := scan.Range{From: e.when, To: e.when}
	kept, report, err := e.runner.Run(context.Background(), e.jobs, rng)
	if err != nil {
		t.Fatalf("live Run 오류: %v", err)
	}

	if len(kept) != 2 {
		t.Fatalf("live 후보 = %d, want 2 (seed 일치분 제외)", len(kept))
	}

	if got := report.Categories[0].ExcludedVerified; got != 1 {
		t.Errorf("excluded verified = %d, want 1", got)
	}

	xrep, err := e.runner.Transfer(context.Background(), e.up, e.jobs, kept)
	if err != nil {
		t.Fatalf("Transfer 오류: %v", err)
	}

	if xrep.Verified != 2 {
		t.Fatalf("전송 = %d, want 2 (불일치+미존재만)", xrep.Verified)
	}

	// 불일치분은 새 내용으로 덮였다.
	got := e.up.store[e.remoteFinal("STAL00KOR_R_20260010000_01H_01S_MS.rnx.gz")]
	if string(got) != "new-content!" {
		t.Errorf("원격 내용 = %q, want 새 내용으로 덮어씀", got)
	}
}

// ---------------------------------------------------------------------------
// 헬퍼
// ---------------------------------------------------------------------------

func countPutRows(t *testing.T, dbPath string) int {
	t.Helper()

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM put_ledger;`).Scan(&n); err != nil {
		t.Fatal(err)
	}

	return n
}

//   #S9 seed 는 원격 조회만 수행하고 UploadPart/Rename/Remove 를 절대 호출하지 않음

func TestSeedNeverMutatesRemote(t *testing.T) {
	e := newSeedEnv(t)

	name := "READ00KOR_R_20260010000_01H_01S_MS.rnx.gz"
	content := "payload-seed-readonly"

	// 로컬 파일 준비.
	e.putLocal(t, name, content)

	// 원격에 이미 동일 크기의 최종 파일이 존재하도록 준비한다.
	e.up.store[e.remoteFinal(name)] = []byte(content)

	// Seed 전체 흐름 실행.
	rep, _ := e.runSeed(t)

	if rep.Seeded != 1 {
		t.Errorf(
			"Seeded=%d, want 1",
			rep.Seeded,
		)
	}

	// Seed는 원격을 '조회'만 해야 한다.
	//
	// 허용:
	//   Size / Stat 계열 조회
	//
	// 금지:
	//   UploadPart / Rename / Remove
	//
	// Seed의 목적은 초기 설치 시 이미 원격에 존재하는 파일을
	// VERIFIED 로 장부 초기화하여 중복 전송을 막는 것이지,
	// 원격 파일을 생성·변경·삭제하는 것이 아니다.
	if got := len(e.up.methodCalls("UploadPart")); got != 0 {
		t.Errorf(
			"UploadPart 호출=%d, want 0",
			got,
		)
	}

	if got := len(e.up.methodCalls("Rename")); got != 0 {
		t.Errorf(
			"Rename 호출=%d, want 0",
			got,
		)
	}

	if got := len(e.up.methodCalls("Remove")); got != 0 {
		t.Errorf(
			"Remove 호출=%d, want 0",
			got,
		)
	}
}
