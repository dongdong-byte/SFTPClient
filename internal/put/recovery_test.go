package put

// recovery.go 의 회수 계약을 심판한다 (A안 — salvage 없음).
//
// Uploader 는 fake, Ledger 는 실제 SQLite 다 (transfer_test 와 동일 원칙).
// FailPut 의 WHERE 가드나 .part 정리 순서 같은 실제 결함을 fake ledger 로
// 덮지 않기 위해서다.
//
// 심판 목록:
//   #R1 IN_PROGRESS 없음          → Found=0, Remove 미호출, nil
//   #R2 part_path 있는 1건        → Remove(part) 후 FAILED, attempts 불변, 사유 기록
//   #R3 part_path NULL 인 1건     → Remove 미호출, FAILED (nil 가드)
//   #R4 Remove 실제 오류          → 실행 중단, 행은 IN_PROGRESS 유지
//   #R5 여러 건                   → 전부 FAILED, Found=Recovered
//   #R6 두 번 실행                → 두 번째는 Found=0 (멱등)
//   #R7 attempts 상한 상태        → Recovery 가 attempts 를 초기화하지 않음
//   #R8 원격 final 이 멀쩡해 보여도 salvage 하지 않음

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
)

// seedInProgress 는 IN_PROGRESS 로 남은 잔여 전송 행을 만든다.
//
// seedCandidate(PENDING) → BeginPut(IN_PROGRESS, attempts=1, part_path 기록).
// 실제 크래시가 남기는 상태와 같은 정상 API 경로로 만든다.
func seedInProgress(
	t *testing.T,
	db *ledger.DB,
	name, content, remotePath, partPath string,
) ledger.PutKey {
	t.Helper()

	c := seedCandidate(t, db, name, content)

	if err := db.BeginPut(
		context.Background(),
		c.Key,
		remotePath,
		partPath,
		c.Size,
		5, // maxRetries
	); err != nil {
		t.Fatalf("BeginPut(%q) 실패: %v", name, err)
	}

	return c.Key
}

// rawExec 는 검증 준비를 위해 별도 연결로 put_ledger 를 직접 조작한다.
//
// part_path 를 NULL 로 만드는 등 정상 API 로는 만들 수 없는
// 방어 상태를 구성할 때만 사용한다.
func rawExec(t *testing.T, dbPath, query string, args ...any) {
	t.Helper()

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("raw open 실패: %v", err)
	}
	defer raw.Close()

	if _, err := raw.Exec(query, args...); err != nil {
		t.Fatalf("raw exec 실패: %v", err)
	}
}

// #R1 — IN_PROGRESS 가 없으면 아무 일도 하지 않는다.
func TestRecoverNoInProgress(t *testing.T) {
	db, _ := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("Recover() 오류: %v", err)
	}

	if rep.Found != 0 || rep.Recovered != 0 || rep.PartRemoved != 0 {
		t.Errorf("rep = %+v, want 전부 0", rep)
	}

	if got := len(up.methodCalls("Remove")); got != 0 {
		t.Errorf("Remove 호출 = %d, want 0", got)
	}
}

// #R2 — part_path 있는 IN_PROGRESS 는
//
//	원격 .part 삭제 → FAILED → attempts 불변 → 사유 기록
//
// 까지 확정한다. 회수의 핵심 경로다.
func TestRecoverSingleWithPart(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	const partPath = "out/2026/001/03/site0010.26o.gz.part"

	key := seedInProgress(
		t,
		db,
		"SITE0010.26O.gz",
		"payload",
		"out/2026/001/03/site0010.26o.gz",
		partPath,
	)

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("Recover() 오류: %v", err)
	}

	if rep.Found != 1 || rep.Recovered != 1 || rep.PartRemoved != 1 {
		t.Errorf(
			"rep = %+v, want Found/Recovered/PartRemoved=1",
			rep,
		)
	}

	removes := up.methodCalls("Remove")
	if len(removes) != 1 || removes[0] != partPath {
		t.Errorf("Remove 호출 = %v, want [%q]", removes, partPath)
	}

	row := readPutRaw(t, dbPath, key)

	if row.status != string(domain.StatusFailed) {
		t.Errorf("status = %q, want FAILED", row.status)
	}

	// attempts 는 BeginPut 이 올린 1 그대로여야 한다.
	// Recovery 자체는 시도 횟수를 소비하지 않는다.
	if row.attempts != 1 {
		t.Errorf(
			"attempts = %d, want 1 (회수는 attempts 불변)",
			row.attempts,
		)
	}

	if row.errText.String != recoverCause {
		t.Errorf(
			"error = %q, want %q",
			row.errText.String,
			recoverCause,
		)
	}
}

// #R3 — part_path 가 NULL 이면 Remove 를 부르지 않고 FAILED 로 되돌린다.
//
// 프로덕션에서 BeginPut 은 항상 part_path 를 채우므로 이 상태는 방어용이다.
// nil 가드가 사라지면 이 테스트가 잡는다.
func TestRecoverNilPartPath(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	key := seedInProgress(
		t,
		db,
		"SITE0020.26O.gz",
		"payload",
		"out/2026/001/03/site0020.26o.gz",
		"out/2026/001/03/site0020.26o.gz.part",
	)

	// BeginPut 이 채운 part_path 를 NULL 로 만들어 방어 경로를 구성한다.
	rawExec(
		t,
		dbPath,
		`UPDATE put_ledger SET part_path = NULL
		  WHERE category = ? AND file_name = ? AND revision = ?;`,
		key.Category.String(),
		key.FileName,
		key.Revision,
	)

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("Recover() 오류: %v", err)
	}

	if rep.Found != 1 || rep.Recovered != 1 {
		t.Errorf(
			"rep = %+v, want Found/Recovered=1",
			rep,
		)
	}

	if rep.PartRemoved != 0 {
		t.Errorf(
			"PartRemoved = %d, want 0 (part_path NULL)",
			rep.PartRemoved,
		)
	}

	if got := len(up.methodCalls("Remove")); got != 0 {
		t.Errorf("Remove 호출 = %d, want 0", got)
	}

	if row := readPutRaw(t, dbPath, key); row.status != string(domain.StatusFailed) {
		t.Errorf("status = %q, want FAILED", row.status)
	}
}

// #R4 — Remove 가 실제 오류를 내면 실행을 중단하고,
// 그 행은 아직 IN_PROGRESS 여야 한다.
//
// FailPut 을 Remove 뒤에 두는 순서의 심판이다.
// 순서가 뒤집히면 이 테스트에서 status 가 FAILED 로 나와 실패한다.
func TestRecoverRemoveErrorAborts(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	wantErr := fmt.Errorf("permission denied")
	up.failOn("Remove", 1, wantErr)

	key := seedInProgress(
		t,
		db,
		"SITE0030.26O.gz",
		"payload",
		"out/2026/001/03/site0030.26o.gz",
		"out/2026/001/03/site0030.26o.gz.part",
	)

	_, err := r.Recover(context.Background(), up)
	if err == nil {
		t.Fatal("Recover() 오류 없음, want Remove 오류로 중단")
	}

	if !errors.Is(err, wantErr) {
		t.Errorf("오류 = %v, want %v 포함", err, wantErr)
	}

	// 정리 실패이므로 FailPut 에 닿지 않았다.
	// 행은 그대로 IN_PROGRESS 여야 한다.
	if row := readPutRaw(t, dbPath, key); row.status != string(domain.StatusInProgress) {
		t.Errorf(
			"status = %q, want IN_PROGRESS (FailPut 미도달)",
			row.status,
		)
	}
}

// #R5 — 여러 IN_PROGRESS 를 모두 회수한다.
func TestRecoverMultiple(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	names := []string{
		"SITE0040.26O.gz",
		"SITE0050.26O.gz",
		"SITE0060.26O.gz",
	}

	keys := make([]ledger.PutKey, 0, len(names))

	for i, name := range names {
		part := fmt.Sprintf("out/p/%d/%s.part", i, name)

		keys = append(
			keys,
			seedInProgress(
				t,
				db,
				name,
				"payload",
				fmt.Sprintf("out/p/%d/%s", i, name),
				part,
			),
		)
	}

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("Recover() 오류: %v", err)
	}

	if rep.Found != len(names) || rep.Recovered != len(names) {
		t.Errorf(
			"rep = %+v, want Found/Recovered=%d",
			rep,
			len(names),
		)
	}

	if rep.PartRemoved != len(names) {
		t.Errorf(
			"PartRemoved = %d, want %d",
			rep.PartRemoved,
			len(names),
		)
	}

	if got := len(up.methodCalls("Remove")); got != len(names) {
		t.Errorf(
			"Remove 호출 = %d, want %d",
			got,
			len(names),
		)
	}

	for _, key := range keys {
		if row := readPutRaw(t, dbPath, key); row.status != string(domain.StatusFailed) {
			t.Errorf(
				"%q status = %q, want FAILED",
				key.FileName,
				row.status,
			)
		}
	}
}

// #R6 — 회수는 멱등이다.
//
// 한 번 회수한 뒤 다시 호출하면 대상이 없고,
// 이미 정리한 .part 를 다시 지우지도 않는다.
func TestRecoverIdempotent(t *testing.T) {
	db, _ := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	seedInProgress(
		t,
		db,
		"SITE0070.26O.gz",
		"payload",
		"out/2026/001/03/site0070.26o.gz",
		"out/2026/001/03/site0070.26o.gz.part",
	)

	if _, err := r.Recover(context.Background(), up); err != nil {
		t.Fatalf("1차 Recover() 오류: %v", err)
	}

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("2차 Recover() 오류: %v", err)
	}

	if rep.Found != 0 {
		t.Errorf(
			"2차 Found = %d, want 0 (이미 FAILED — 멱등)",
			rep.Found,
		)
	}

	// 첫 회수 때 한 번만 호출되어야 한다.
	if got := len(up.methodCalls("Remove")); got != 1 {
		t.Errorf(
			"Remove 누적 호출 = %d, want 1",
			got,
		)
	}
}

// #R7 — attempts 가 이미 자동 시도 상한에 도달했더라도
// Recovery 는 그 값을 초기화하거나 감소시키지 않는다.
//
// Recovery 의 책임은 IN_PROGRESS → FAILED 회수까지다.
// 이후 exhausted 로 후보에서 제외할지는 Runner 후보 판정의 책임이다.
func TestRecoverPreservesAttemptsAtLimit(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	key := seedInProgress(
		t,
		db,
		"SITE0080.26O.gz",
		"payload",
		"out/2026/001/03/site0080.26o.gz",
		"out/2026/001/03/site0080.26o.gz.part",
	)

	const maxRetries = 5

	// 실제로 여러 회차에서 실패를 반복한 뒤 마지막 시도 중
	// 프로세스가 죽은 상태를 구성한다.
	rawExec(
		t,
		dbPath,
		`UPDATE put_ledger SET attempts = ?
		  WHERE category = ? AND file_name = ? AND revision = ?;`,
		maxRetries,
		key.Category.String(),
		key.FileName,
		key.Revision,
	)

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("Recover() 오류: %v", err)
	}

	if rep.Found != 1 || rep.Recovered != 1 {
		t.Errorf(
			"rep = %+v, want Found/Recovered=1",
			rep,
		)
	}

	row := readPutRaw(t, dbPath, key)

	if row.status != string(domain.StatusFailed) {
		t.Errorf("status = %q, want FAILED", row.status)
	}

	if row.attempts != maxRetries {
		t.Errorf(
			"attempts = %d, want %d (Recovery 는 attempts 불변)",
			row.attempts,
			maxRetries,
		)
	}
}

// #R8 — 원격 최종 파일이 멀쩡해 보여도(존재 + 크기 일치) salvage 하지
// 않고 FAILED 로 되돌린다. A안(2026-09-01)의 명문화다.
//
// 크기 일치는 "이 revision 의 rename 완료" 를 증명하지 못한다 —
// 크기 보존 보정(내용만 다르고 size 동일)이 이전 revision 위에서
// 죽으면 같은 관측이 나온다. 누군가 크기 기반 salvage 를 다시 넣으면
// 이 테스트가 잡는다: status 가 VERIFIED 로 나오거나 Size 가 호출된다.
func TestRecoverDoesNotSalvageIntactFinal(t *testing.T) {
	db, dbPath := xferTestDB(t)
	r := xferRunner(db)
	up := newFakeUploader()

	const (
		content    = "same-size-payload"
		remotePath = "out/2026/001/03/site0090.26o.gz"
	)

	key := seedInProgress(
		t,
		db,
		"SITE0090.26O.gz",
		content,
		remotePath,
		remotePath+".part",
	)

	// rename 직후 사망처럼 보이는 상태: 최종 파일 존재, 크기 일치.
	// (업로드 전 사망 + 크기 보존 보정과 관측상 구분 불가)
	up.store[remotePath] = []byte(content)

	rep, err := r.Recover(context.Background(), up)
	if err != nil {
		t.Fatalf("Recover() 오류: %v", err)
	}

	if rep.Recovered != 1 {
		t.Errorf("Recovered = %d, want 1", rep.Recovered)
	}

	if got := len(up.methodCalls("Size")); got != 0 {
		t.Errorf(
			"Size 호출 = %d, want 0 (회수는 원격 크기를 판정에 쓰지 않는다)",
			got,
		)
	}

	row := readPutRaw(t, dbPath, key)

	if row.status != string(domain.StatusFailed) {
		t.Errorf(
			"status = %q, want FAILED (salvage 금지 — A안)",
			row.status,
		)
	}

	// 최종 파일 자체는 건드리지 않는다. 재전송이 덮어쓴다.
	if _, ok := up.store[remotePath]; !ok {
		t.Error("회수가 원격 최종 파일을 삭제했다")
	}
}
