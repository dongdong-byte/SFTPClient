package put

// Worker Pool(디렉터리 단위 병렬 전송)의 계약을 심판한다.
//
// 확정본(2026-09-01) 필수 케이스:
//   #P1 배치 내 순서 보존 (groupByDirectory — map 순회 함정 회귀 심판)
//   #P2 다중 디렉터리 병렬 전송 + 공통 부모 (happy path, -race sink)
//   #P3 파일 하나 실패 → 같은 디렉터리의 다음 파일 계속
//   #P4 MaxWorkers > 디렉터리 수 (불필요한 유휴 워커 생성 없이 정상)
//   #P5 외부 취소 → 전체 정지 (ctx.Err backstop)
//   #P6 워커 발 fatal → pool 전체 취소 → 진행 중 파일 FAILED 정리
//
// 단일 워커 경로의 기존 계약은 transfer_test.go 가 계속 심판한다.
// MaxWorkers 미지정 시 Transfer 가 1 로 보정하므로 기존 테스트들은
// 단일 워커로 기존과 같은 동작을 유지한다.
//
// ★ -race 주의:
// 병렬 워커가 r.now() 를 동시에 부르므로 여기서는 tick 을 atomic 으로
// 증가시키는 poolRunner 를 쓴다. 기존 xferRunner 의 평범한 tick++ 는
// 단일 워커 테스트에서만 안전하다.

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/pathpl"
)

// poolRunner 는 병렬 안전한 Now 를 가진 Runner 다.
func poolRunner(db *ledger.DB, maxWorkers int) *Runner {
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)

	var tick int64

	return &Runner{
		DB: db,
		Opts: RunOptions{
			MaxRetries: 5,
			MaxWorkers: maxWorkers,
			Logger:     log.New(io.Discard, "", 0),
			Now: func() time.Time {
				n := atomic.AddInt64(&tick, 1)
				return base.Add(time.Duration(n) * time.Second)
			},
		},
	}
}

// seedCandDir 는 seedCandidate 로 만든 후보의 When 을 지정해
// 원격 디렉터리를 통제한다.
//
// RemotePath 는 (DOY) 까지이므로 slot 을 날짜 오프셋으로 쓴다. slot 이
// 다르면 다른 디렉터리, 같으면 같은 디렉터리로 그룹핑된다. ((HH) 토큰
// 제거 전에는 시각으로 나눴다 — PATH_DESIGN v3 §1.)
//
// When 은 ledger 에 저장되지 않으며 전송 시 remote_path 계산에만
// 사용되므로 seed 후 덮어써도 장부 정합성에 영향이 없다.
func seedCandDir(
	t *testing.T,
	db *ledger.DB,
	name string,
	content string,
	slot int,
) Candidate {
	t.Helper()

	c := seedCandidate(t, db, name, content)
	c.When = time.Date(
		2026,
		1,
		1+slot,
		0,
		0,
		0,
		0,
		time.UTC,
	)

	return c
}

// countStatuses 는 여러 키의 put_ledger status 분포를 센다.
func countStatuses(
	t *testing.T,
	dbPath string,
	keys []ledger.PutKey,
) map[string]int {
	t.Helper()

	out := map[string]int{}

	for _, k := range keys {
		out[readPutRaw(t, dbPath, k).status]++
	}

	return out
}

// ---------------------------------------------------------------------------
// #P1
// ---------------------------------------------------------------------------

// TestGroupByDirectoryPreservesOrder 는 groupByDirectory 가 정렬된 후보의
// 배치 내 상대 순서를 보존하는지 심판한다.
//
// 같은 디렉터리 파일이 입력에서 다른 디렉터리 후보와 교차되어 있어도
// 각 배치는 최초 등장 순서를 그대로 유지해야 한다.
//
// dirKey map 은 조회에만 사용하고 map 순회로 결과를 만들면 안 된다.
// Go map 순회 순서는 보장되지 않기 때문이다.
func TestGroupByDirectoryPreservesOrder(t *testing.T) {
	r := poolRunner(nil, 4) // groupByDirectory 는 DB 를 쓰지 않는다.

	mk := func(name string, slot int) Candidate {
		return Candidate{
			Key: ledger.PutKey{
				Category: xferTestCat,
				FileName: name,
				Revision: 1,
			},
			LocalPath: "/local/" + name,
			Size:      10,
			// slot 은 날짜 오프셋이다 (seedCandDir 주석).
			When: time.Date(
				2026,
				1,
				1+slot,
				0,
				0,
				0,
				0,
				time.UTC,
			),
		}
	}

	// dirA(slot=3)와 dirB(slot=4)를 의도적으로 교차한다.
	cands := []Candidate{
		mk("a1", 3),
		mk("b1", 4),
		mk("a2", 3),
		mk("b2", 4),
	}

	batches, err := r.groupByDirectory(
		xferJobs(t),
		cands,
	)
	if err != nil {
		t.Fatalf(
			"groupByDirectory() 오류: %v",
			err,
		)
	}

	if len(batches) != 2 {
		t.Fatalf(
			"배치 수 = %d, want 2",
			len(batches),
		)
	}

	// 배치 자체의 순서도 first-seen 이다.
	// dirA(a1)가 먼저, dirB(b1)가 다음이다.
	got0 := fileNames(batches[0].Candidates)
	got1 := fileNames(batches[1].Candidates)

	if !equalStrings(
		got0,
		[]string{"a1", "a2"},
	) {
		t.Errorf(
			"batch[0] = %v, want [a1 a2]",
			got0,
		)
	}

	if !equalStrings(
		got1,
		[]string{"b1", "b2"},
	) {
		t.Errorf(
			"batch[1] = %v, want [b1 b2]",
			got1,
		)
	}
}

// TestGroupByDirectoryFlatRemoteCollapsesHours 는 RemotePath 에 (HH) 가
// 없으면 서로 다른 시각의 Hourly 파일이 한 원격 디렉터리로 묶이는지 본다.
//
// 서울시 Hourly 소스(dir) → /RNX2/ (flat) 조합이다.
// 파일명 세션 문자(f=5시, g=6시)가 달라 같은 폴더에서도 충돌하지 않는다.
func TestGroupByDirectoryFlatRemoteCollapsesHours(t *testing.T) {
	r := poolRunner(nil, 4)

	remote, err := pathpl.Parse("/RNX2/")
	if err != nil {
		t.Fatalf("pathpl.Parse(remote): %v", err)
	}

	local, err := pathpl.Parse(`Z:\RINEX-V2-H\(YYYY)\(DOY)\(HH)\`)
	if err != nil {
		t.Fatalf("pathpl.Parse(local): %v", err)
	}

	jobs := []CategoryJob{{
		Category:   xferTestCat,
		LocalPath:  local,
		RemotePath: remote,
	}}

	mk := func(name string, hour int) Candidate {
		return Candidate{
			Key: ledger.PutKey{
				Category: xferTestCat,
				FileName: name,
				Revision: 1,
			},
			LocalPath: "/local/" + name,
			Size:      10,
			When: time.Date(
				2026,
				9,
				1,
				hour,
				0,
				0,
				0,
				time.UTC,
			),
		}
	}

	cands := []Candidate{
		mk("dbon244f.26o", 5),
		mk("dbon244g.26o", 6),
	}

	batches, err := r.groupByDirectory(jobs, cands)
	if err != nil {
		t.Fatalf("groupByDirectory() 오류: %v", err)
	}

	if len(batches) != 1 {
		t.Fatalf("배치 수 = %d, want 1 (flat RemotePath)", len(batches))
	}

	if batches[0].RemoteDir != "/RNX2/" {
		t.Errorf("RemoteDir = %q, want /RNX2/", batches[0].RemoteDir)
	}

	got := fileNames(batches[0].Candidates)
	if !equalStrings(got, []string{"dbon244f.26o", "dbon244g.26o"}) {
		t.Errorf("candidates = %v, want [dbon244f.26o dbon244g.26o]", got)
	}
}

// ---------------------------------------------------------------------------
// #P2
// ---------------------------------------------------------------------------

// TestTransferParallelAllVerified 는 여러 원격 디렉터리를 병렬 전송했을 때
// 모든 파일이 VERIFIED 로 끝나는지 심판한다.
//
// hour 3/4/5 는 서로 다른 최종 디렉터리지만 공통 부모를 가진다.
// 서로 다른 워커가 같은 부모 계층을 준비할 수 있으므로 EnsureDir 의
// "이미 존재함 = 성공" 계약과 공유 Uploader 의 병렬 안전성을 함께 본다.
//
// 실제 mkdir 충돌 타이밍 자체를 강제하는 테스트는 아니며,
// 병렬 happy path 및 -race sink 역할을 한다.
func TestTransferParallelAllVerified(t *testing.T) {
	db, dbPath := xferTestDB(t)

	r := poolRunner(db, 4)
	up := newFakeUploader()

	var keys []ledger.PutKey
	var cands []Candidate

	for i, hour := range []int{3, 4, 5} {
		for j := 0; j < 3; j++ {
			n := fileName(i, j)

			c := seedCandDir(
				t,
				db,
				n,
				"payload-"+n,
				hour,
			)

			cands = append(cands, c)
			keys = append(keys, c.Key)
		}
	}

	rep, err := r.Transfer(
		context.Background(),
		up,
		xferJobs(t),
		cands,
	)
	if err != nil {
		t.Fatalf(
			"Transfer() 오류: %v",
			err,
		)
	}

	if rep.Verified != len(cands) ||
		rep.Failed != 0 {
		t.Errorf(
			"rep verified=%d failed=%d, want verified=%d failed=0",
			rep.Verified,
			rep.Failed,
			len(cands),
		)
	}

	dist := countStatuses(
		t,
		dbPath,
		keys,
	)

	if dist[string(domain.StatusVerified)] != len(cands) {
		t.Errorf(
			"VERIFIED = %d, want %d (dist=%v)",
			dist[string(domain.StatusVerified)],
			len(cands),
			dist,
		)
	}
}

// ---------------------------------------------------------------------------
// #P3
// ---------------------------------------------------------------------------

// TestTransferParallelOneFileFailsRestContinue 는 파일 하나가 실패해도
// 같은 디렉터리를 담당한 워커가 다음 파일을 계속 처리하는지 심판한다.
//
// dirA:
//
//	A1 → UploadPart 실패 → FAILED
//	A2 → 같은 DirectoryBatch 의 다음 파일 → VERIFIED
//
// dirB:
//
//	B1 → 다른 워커 → VERIFIED
//
// 이 테스트에서 중요한 것은 단순히 "다른 Worker가 계속 돈다"가 아니다.
// A1 실패 뒤 같은 디렉터리의 A2가 VERIFIED 되어야
//
//	파일 실패 → FailPut → 다음 파일 계속
//
// 계약을 실제로 증명한다.
//
// 병렬 scheduler 순서에 결과가 의존하지 않도록 nth 호출 실패가 아니라
// 정확한 LocalPath 하나만 실패시키는 failLocalUploader 를 사용한다.
func TestTransferParallelOneFileFailsRestContinue(t *testing.T) {
	db, dbPath := xferTestDB(t)

	r := poolRunner(db, 2)
	base := newFakeUploader()

	// dirA — 같은 Worker가 순차 처리해야 하는 두 파일.
	a1 := seedCandDir(
		t,
		db,
		fileName(0, 0),
		"payload-a1",
		3,
	)

	a2 := seedCandDir(
		t,
		db,
		fileName(0, 1),
		"payload-a2",
		3,
	)

	// dirB — 다른 Worker가 처리할 파일.
	b1 := seedCandDir(
		t,
		db,
		fileName(1, 0),
		"payload-b1",
		4,
	)

	up := &failLocalUploader{
		Uploader:  base,
		localPath: a1.LocalPath,
		err:       errors.New("boom"),
	}

	cands := []Candidate{
		a1,
		a2,
		b1,
	}

	keys := []ledger.PutKey{
		a1.Key,
		a2.Key,
		b1.Key,
	}

	rep, err := r.Transfer(
		context.Background(),
		up,
		xferJobs(t),
		cands,
	)
	if err != nil {
		t.Fatalf(
			"Transfer() 는 파일 실패로 중단하면 안 된다: %v",
			err,
		)
	}

	if rep.Failed != 1 {
		t.Errorf(
			"failed=%d, want 1",
			rep.Failed,
		)
	}

	if rep.Verified != 2 {
		t.Errorf(
			"verified=%d, want 2",
			rep.Verified,
		)
	}

	dist := countStatuses(
		t,
		dbPath,
		keys,
	)

	if dist[string(domain.StatusFailed)] != 1 {
		t.Errorf(
			"FAILED=%d, want 1 (dist=%v)",
			dist[string(domain.StatusFailed)],
			dist,
		)
	}

	if dist[string(domain.StatusVerified)] != 2 {
		t.Errorf(
			"VERIFIED=%d, want 2 (dist=%v)",
			dist[string(domain.StatusVerified)],
			dist,
		)
	}

	// 핵심 심판:
	// A1 실패 뒤 같은 DirectoryBatch 의 A2가 실제 VERIFIED 여야 한다.
	a1Row := readPutRaw(
		t,
		dbPath,
		a1.Key,
	)

	a2Row := readPutRaw(
		t,
		dbPath,
		a2.Key,
	)

	if a1Row.status != string(domain.StatusFailed) {
		t.Errorf(
			"A1 status=%q, want FAILED",
			a1Row.status,
		)
	}

	if a2Row.status != string(domain.StatusVerified) {
		t.Errorf(
			"A2 status=%q, want VERIFIED — 같은 워커가 다음 파일을 계속하지 않음",
			a2Row.status,
		)
	}
}

// ---------------------------------------------------------------------------
// #P4
// ---------------------------------------------------------------------------

// TestTransferParallelFewerDirsThanWorkers 는 MaxWorkers 가 실제 디렉터리
// 수보다 커도 정상 동작하는지 심판한다.
//
// 디렉터리 2개에 MaxWorkers=8 이면 실제 워커 수는 2 로 캡된다.
// 불필요한 유휴 워커를 생성하지 않고 두 디렉터리를 정상 처리해야 한다.
func TestTransferParallelFewerDirsThanWorkers(t *testing.T) {
	db, dbPath := xferTestDB(t)

	r := poolRunner(db, 8)
	up := newFakeUploader()

	var keys []ledger.PutKey
	var cands []Candidate

	for i, hour := range []int{3, 4} {
		n := fileName(i, 0)

		c := seedCandDir(
			t,
			db,
			n,
			"payload-"+n,
			hour,
		)

		cands = append(cands, c)
		keys = append(keys, c.Key)
	}

	rep, err := r.Transfer(
		context.Background(),
		up,
		xferJobs(t),
		cands,
	)
	if err != nil {
		t.Fatalf(
			"Transfer() 오류: %v",
			err,
		)
	}

	if rep.Verified != 2 {
		t.Errorf(
			"verified=%d, want 2",
			rep.Verified,
		)
	}

	dist := countStatuses(
		t,
		dbPath,
		keys,
	)

	if dist[string(domain.StatusVerified)] != 2 {
		t.Errorf(
			"VERIFIED = %d, want 2 (dist=%v)",
			dist[string(domain.StatusVerified)],
			dist,
		)
	}
}

// ---------------------------------------------------------------------------
// #P5
// ---------------------------------------------------------------------------

// TestTransferPreCancelledStopsAll 은 이미 취소된 ctx 로 Transfer 를
// 호출했을 때 아무 파일도 실제 착수하지 않고 context.Canceled 를
// 반환하는지 심판한다.
func TestTransferPreCancelledStopsAll(t *testing.T) {
	db, _ := xferTestDB(t)

	r := poolRunner(db, 4)
	up := newFakeUploader()

	var cands []Candidate

	for i, hour := range []int{3, 4, 5} {
		n := fileName(i, 0)

		cands = append(
			cands,
			seedCandDir(
				t,
				db,
				n,
				"payload-"+n,
				hour,
			),
		)
	}

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	// 실행 시작 전 취소.
	cancel()

	rep, err := r.Transfer(
		ctx,
		up,
		xferJobs(t),
		cands,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"err = %v, want context.Canceled",
			err,
		)
	}

	if rep.Attempted != 0 ||
		rep.Verified != 0 {
		t.Errorf(
			"rep attempted=%d verified=%d, want 0/0 (착수 없음)",
			rep.Attempted,
			rep.Verified,
		)
	}

	if got := len(
		up.methodCalls("UploadPart"),
	); got != 0 {
		t.Errorf(
			"UploadPart 호출 = %d, want 0",
			got,
		)
	}
}

// ---------------------------------------------------------------------------
// #P6
// ---------------------------------------------------------------------------

// TestTransferWorkerFatalCancelsPool 은 한 워커에서 발생한 fatal 이
// 실제 다른 진행 중 워커까지 취소하는지 심판한다.
//
// 시나리오:
//
//	Worker A:
//	  fast UploadPart
//
//	Worker B:
//	  blocked UploadPart 에 진입 후 ctx 취소를 기다림
//
// blocked가 실제 UploadPart 에 진입한 뒤에만 fast를 진행시킨다.
//
//	fast VERIFIED
//	    ↓
//	Worker A가 fatalCandidate 배치 획득
//	    ↓
//	fatalCandidate preflight stale
//	    ↓
//	이미 VERIFIED 인 put row 에 FailPut 시도
//	    ↓
//	Ledger 상태 전이 오류 = fatal
//	    ↓
//	pool cancel
//	    ↓
//	Worker B의 blocked UploadPart 가 ctx 취소 감지
//	    ↓
//	failOne → FAILED
//
// 최종적으로 정상 cancel 경로에서는 IN_PROGRESS 가 하나도 남지 않아야 한다.
// Startup Recover 는 이 정상 취소 경로를 대신하는 수단이 아니라
// 비정상 종료를 위한 최후 방어선이다.
func TestTransferWorkerFatalCancelsPool(t *testing.T) {
	db, dbPath := xferTestDB(t)

	r := poolRunner(db, 2)
	base := newFakeUploader()

	// 첫 번째 배치:
	// 정상 완료 후 fatalCandidate 를 집어갈 Worker.
	fast := seedCandDir(
		t,
		db,
		fileName(0, 0),
		"payload-fast",
		3,
	)

	// 두 번째 배치:
	// UploadPart 안에서 실제 pool cancel 을 기다릴 Worker.
	blocked := seedCandDir(
		t,
		db,
		fileName(1, 0),
		"payload-blocked",
		4,
	)

	// 세 번째 배치:
	// Worker A에서 fatal 을 유발할 후보.
	fatalCandidate := seedCandDir(
		t,
		db,
		fileName(2, 0),
		"payload-fatal",
		5,
	)

	// fatalCandidate 를 미리 VERIFIED 상태로 만든다.
	//
	// 이후 로컬 Size 를 바꾸면 preflight 에서 errPreflightStale 이 난다.
	// IsRetry=false 이므로 transferOne 은 failPending 을 시도한다.
	//
	// 하지만 put row 는 이미 VERIFIED 이므로 VERIFIED → FAILED 전이는
	// 허용되지 않아 Ledger 오류가 발생하며 이것을 Worker fatal 로 사용한다.
	now := time.Date(
		2026,
		1,
		2,
		1,
		0,
		0,
		0,
		time.UTC,
	)

	if err := db.BeginPut(
		context.Background(),
		fatalCandidate.Key,
		"/already/final",
		"/already/final.part",
		fatalCandidate.Size,
		5,
	); err != nil {
		t.Fatalf(
			"fatal candidate BeginPut 준비 실패: %v",
			err,
		)
	}

	if err := db.FinishPut(
		context.Background(),
		fatalCandidate.Key,
		fatalCandidate.Size,
		now,
		now.Add(time.Second),
	); err != nil {
		t.Fatalf(
			"fatal candidate FinishPut 준비 실패: %v",
			err,
		)
	}

	// Scan 당시 Candidate.Size 와 실제 파일 Size 를 다르게 만들어
	// errPreflightStale 을 유발한다.
	if err := os.WriteFile(
		fatalCandidate.LocalPath,
		[]byte("payload-fatal-size-changed"),
		0o600,
	); err != nil {
		t.Fatalf(
			"fatal candidate 파일 변경 실패: %v",
			err,
		)
	}

	up := newCancelProbeUploader(
		base,
		fast.LocalPath,
		blocked.LocalPath,
	)

	cands := []Candidate{
		fast,
		blocked,
		fatalCandidate,
	}

	rep, err := r.Transfer(
		context.Background(),
		up,
		xferJobs(t),
		cands,
	)
	if err == nil {
		t.Fatal(
			"Transfer() 오류 없음, want worker fatal",
		)
	}

	// fast 는 blocked 가 실제 UploadPart 에 들어간 뒤에만 진행하도록
	// probe 가 제어하므로 fatal 발생 전에 정상 VERIFIED 되어야 한다.
	fastRow := readPutRaw(
		t,
		dbPath,
		fast.Key,
	)

	if fastRow.status != string(domain.StatusVerified) {
		t.Errorf(
			"fast status=%q, want VERIFIED",
			fastRow.status,
		)
	}

	// blocked 는 pool cancel 을 ctx 로 받아 UploadPart 에서 빠져나온 뒤
	// failOne → FailPut 을 거쳐 FAILED 로 접혀야 한다.
	blockedRow := readPutRaw(
		t,
		dbPath,
		blocked.Key,
	)

	if blockedRow.status != string(domain.StatusFailed) {
		t.Errorf(
			"blocked status=%q, want FAILED — pool cancel 정리가 안 됨",
			blockedRow.status,
		)
	}

	// fatalCandidate 는 fatal 을 만들기 위해 미리 VERIFIED 로 만든 fixture다.
	// 잘못된 상태 전이가 적용되지 않고 VERIFIED 를 유지해야 한다.
	fatalRow := readPutRaw(
		t,
		dbPath,
		fatalCandidate.Key,
	)

	if fatalRow.status != string(domain.StatusVerified) {
		t.Errorf(
			"fatal candidate status=%q, want VERIFIED",
			fatalRow.status,
		)
	}

	keys := []ledger.PutKey{
		fast.Key,
		blocked.Key,
		fatalCandidate.Key,
	}

	dist := countStatuses(
		t,
		dbPath,
		keys,
	)

	// 가장 중요한 불변식:
	// 정상 cancel 전파 뒤 IN_PROGRESS 를 남기지 않는다.
	if dist[string(domain.StatusInProgress)] != 0 {
		t.Errorf(
			"IN_PROGRESS=%d, want 0 (dist=%v)",
			dist[string(domain.StatusInProgress)],
			dist,
		)
	}

	if rep.Verified != 1 {
		t.Errorf(
			"verified=%d, want 1",
			rep.Verified,
		)
	}

	if rep.Failed != 1 {
		t.Errorf(
			"failed=%d, want 1",
			rep.Failed,
		)
	}
}

// ---------------------------------------------------------------------------
// 테스트용 Uploader wrappers
// ---------------------------------------------------------------------------

// failLocalUploader 는 특정 localPath 의 UploadPart 만 실패시킨다.
//
// 병렬 테스트에서 "n번째 호출 실패" 방식을 쓰면 어느 파일이 실패할지가
// scheduler 순서에 따라 달라진다.
//
// P3는 같은 DirectoryBatch 의 첫 파일 실패 뒤 두 번째 파일 계속이라는
// 정확한 경로를 심판해야 하므로 localPath 로 실패 대상을 고정한다.
type failLocalUploader struct {
	Uploader

	localPath string
	err       error
}

func (u *failLocalUploader) UploadPart(
	ctx context.Context,
	localPath string,
	partPath string,
) error {
	if localPath == u.localPath {
		return u.err
	}

	return u.Uploader.UploadPart(
		ctx,
		localPath,
		partPath,
	)
}

// cancelProbeUploader 는 P6의 실행 순서를 scheduler 운에 맡기지 않고
// 결정적으로 만드는 테스트 전용 Uploader 다.
//
// blockedLocal:
//
//	UploadPart 진입 사실을 blockedStarted 로 알린다.
//	그 뒤 ctx 취소가 올 때까지 대기한다.
//
// fastLocal:
//
//	blockedLocal 이 실제 UploadPart 에 진입한 뒤에만
//	기본 Uploader 의 UploadPart 를 호출한다.
//
// 따라서 순서는:
//
//	Worker B blocked 진입
//	       ↓
//	Worker A fast 완료
//	       ↓
//	Worker A fatal batch 획득
//	       ↓
//	pool cancel
//	       ↓
//	Worker B ctx 취소 감지
//
// 로 고정된다.
type cancelProbeUploader struct {
	Uploader

	fastLocal    string
	blockedLocal string

	blockedStarted chan struct{}
	startOnce      sync.Once
}

// newCancelProbeUploader 는 P6 전용 Uploader wrapper 를 만든다.
func newCancelProbeUploader(
	base Uploader,
	fastLocal string,
	blockedLocal string,
) *cancelProbeUploader {
	return &cancelProbeUploader{
		Uploader:       base,
		fastLocal:      fastLocal,
		blockedLocal:   blockedLocal,
		blockedStarted: make(chan struct{}),
	}
}

// UploadPart 는 localPath 에 따라 P6의 동기화 동작을 수행한다.
func (u *cancelProbeUploader) UploadPart(
	ctx context.Context,
	localPath string,
	partPath string,
) error {
	switch localPath {
	case u.blockedLocal:
		u.startOnce.Do(func() {
			close(u.blockedStarted)
		})

		// 실제 Worker Pool cancel 이 와야 빠져나온다.
		<-ctx.Done()

		return ctx.Err()

	case u.fastLocal:
		// blocked Worker가 UploadPart 에 실제 진입한 뒤에만
		// fast 파일의 업로드를 진행한다.
		select {
		case <-u.blockedStarted:
		case <-ctx.Done():
			return ctx.Err()
		}

		return u.Uploader.UploadPart(
			ctx,
			localPath,
			partPath,
		)

	default:
		return u.Uploader.UploadPart(
			ctx,
			localPath,
			partPath,
		)
	}
}

// ---------------------------------------------------------------------------
// 소소한 테스트 헬퍼
// ---------------------------------------------------------------------------

// fileName 은 디렉터리 i, 순번 j 에 대해 유일한 파일명을 만든다.
//
// seedCandidate 는 UpsertCommon / InsertPendingBatch 를 직접 수행하고
// Verify 를 태우지 않으므로 여기서는 파일명 유일성만 필요하다.
func fileName(i, j int) string {
	return "s" +
		strconv.Itoa(i) +
		strconv.Itoa(j) +
		"0010.26o.gz"
}

// fileNames 는 Candidate slice 의 ledger file_name 만 추출한다.
func fileNames(
	cands []Candidate,
) []string {
	out := make(
		[]string,
		0,
		len(cands),
	)

	for _, c := range cands {
		out = append(
			out,
			c.Key.FileName,
		)
	}

	return out
}

// equalStrings 는 두 문자열 slice 의 길이와 순서를 비교한다.
func equalStrings(
	a []string,
	b []string,
) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}

	return true
}
