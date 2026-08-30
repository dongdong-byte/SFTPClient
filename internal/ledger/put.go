package ledger

// put_ledger 상태 머신 (2026-08-30 확정 정책 요약)
//
// PUT 실행 정책
//  1. 후보는 Scan/Ledger 대조로 계산한다 (이 파일 밖).
//  2. MaxFilesPerRun 적용 후의 이번 실행 대상만 put_ledger 에
//     PENDING 으로 한 트랜잭션 일괄 등록한다.
//  3. PENDING 등록 트랜잭션 전체가 커밋된 뒤에만 Worker 를 시작한다.
//
// Retry 정책
//   - MaxRetries 는 동일 (category, file_name, revision) 의
//     첫 시도를 포함한 누적 자동 시도 상한이다.
//   - 한 프로세스 실행 안에서는 같은 파일을 재시도하지 않는다.
//     매시 스케줄러가 곧 1시간 간격 retry 다. backoff 장치를 두지 않는다.
//   - 상한 도달 항목은 FAILED + attempts>=MaxRetries 그대로 둔다.
//     EXHAUSTED 상태를 신설하지 않는다.
//   - 회차 내 재시도 없음 ≠ 크래시 복구 없음.
//     시작 시 IN_PROGRESS 회수(잔여 .part 정리 후 FAILED 되돌리기)는
//     확정 정책이며 transport 도입 시 조립한다 (PENDING 되돌리기 아님).
//
// Transaction 정책
//   - common_ledger 와 put_ledger 를 묶는 cross-table 트랜잭션은 없다.
//   - put_ledger 의 각 상태 전이는 단일 statement 로 원자화한다.
//   - 유일한 트랜잭션은 PENDING 일괄 INSERT 이며,
//     그 안에서는 반드시 tx.ExecContext 만 쓴다 (db.go 교착 규칙).
//
// 도메인 전이 준수
//   - domain.Status.CanTransitionTo 는 FAILED → PENDING 을 허용하지 않는다.
//   - 일괄 등록은 신규 행 INSERT 전용이다 (ON CONFLICT DO NOTHING).
//   - 기존 FAILED 행은 BeginPut 이 FAILED → IN_PROGRESS 로 직접 올린다.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"SFTPClient/internal/domain"
)

// maxPutLookupKeys 는 LookupPut 한 번의 조회에 넣는 최대 키 수이다.
//
// maxLookupNames 를 그대로 쓰지 않고 나눠 두는 이유는 바인드 수가 다르기
// 때문이다. LookupCommon 은 이름 1개당 ? 1개지만, 여기는
// (file_name, revision) 튜플이라 키 1개당 ? 2개다.
//
//	LookupCommon   500 + 1(category) =  501 바인드
//	LookupPut      500×2 + 1         = 1001 바인드   ← 같은 값을 쓰면
//
// maxLookupNames 주석의 "상한과 충분히 멀다" 라는 근거가 여기서는
// 성립하지 않는다. 지금 드라이버의 상한으로는 터지지 않더라도,
// 상수의 근거가 조용히 깨진 상태를 남기면 나중에 maxLookupNames 를
// 올리거나 세 번째 조회 함수를 추가할 때 아무도 이 계산을 다시 하지 않는다.
//
// maxLookupNames 에서 유도하여 두 값이 함께 움직이게 한다.
const maxPutLookupKeys = maxLookupNames / 2

// ErrNotCandidate 는 BeginPut 이 전이할 수 없는 행을 만났을 때 반환한다.
//
// 다른 Worker 가 선점했거나(IN_PROGRESS/VERIFIED), 상한에 도달한 경우다.
// 호출자는 오류로 중단하지 않고 그 파일을 건너뛴다.
//
// 이 오류가 "건너뛰기" 로 해석되므로, 건너뛰면 안 되는 사유는 절대
// 여기에 섞지 않는다. BeginPut 의 인자 검증이 별도 오류인 이유다.
var ErrNotCandidate = errors.New("ledger: not a candidate")

// ErrInvalidLookupRevision 은 LookupPut 이 1 미만의 revision 을 받았을 때
// 반환한다.
//
// ErrInvalidLookupCategory / ErrInvalidLookupName (lookup.go) 과 같은 계열이다.
// 셋 중 하나만 sentinel 이 아니면 호출자가 errors.Is 로 입력 오류를
// 한 갈래로 다루지 못하고, 그 하나만 문자열 대조로 떨어진다.
var ErrInvalidLookupRevision = errors.New("ledger: invalid lookup revision")

// PutKey 는 put_ledger 한 행의 식별자다.
//
// revision 은 반드시 common_ledger 에서 읽은 값을 그대로 쓴다.
// 호출자가 직접 구성하지 않는다. (schema.sql FK 한계 주석)
type PutKey struct {
	Category domain.Category
	FileName string
	Revision int64
}

// NameRev 는 LookupPut 입력용 (file_name, revision) 쌍이다.
//
// Category 는 LookupPut 인자로 따로 받으므로 여기에 두지 않는다.
type NameRev struct {
	FileName string
	Revision int64
}

// PutState 는 후보 필터가 필요로 하는 최소 사실이다.
//
// LookupCommon 의 Known.PutStatus 와는 별개다.
// LookupCommon 은 common_ledger(+현재 revision put status) 조회이고
// attempts 가 없다. 후보 필터가 필요로 하는 status/attempts 는
// 이 값이 put_ledger 에서 직접 읽은 결과다.
type PutState struct {
	Status   domain.Status
	Attempts int64
}

// InProgressItem 은 시작 시 IN_PROGRESS 회수 절차의 재료다.
//
// 원격 .part 삭제 후 FailPut 으로 되돌리는 조립은 transport 도입 시이다.
type InProgressItem struct {
	PutKey

	// PartPath 는 IN_PROGRESS 전환 시 기록한 원격 .part 경로다.
	// 비정상 종료로 NULL 일 수 있다.
	PartPath *string

	// RemotePath 는 IN_PROGRESS 전환 시 기록한 최종 목적지 경로다.
	RemotePath *string
}

// InsertPendingBatch 는 이번 실행이 집행할 대상을 PENDING 으로
// 일괄 등록한다. MaxFilesPerRun 적용 "후"의 목록만 받는다.
//
// 신규 (category, file_name, revision) 행만 만들고 기존 행은 건드리지
// 않는다 (ON CONFLICT DO NOTHING). FAILED 재후보를 PENDING 으로
// 되돌리는 것은 domain 전이표가 금지한다. 그 행은 BeginPut 이
// FAILED → IN_PROGRESS 로 직접 올린다.
//
// 전체가 한 트랜잭션이다. 부분 성공을 남기지 않는다 — 700건만
// 등록된 채 Worker 가 출발하면 장부와 실행 목록이 어긋난다.
// 오류 시 호출자는 Worker 를 시작해서는 안 된다.
func (db *DB) InsertPendingBatch(ctx context.Context, items []PutKey) error {
	if len(items) == 0 {
		return nil
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ledger: begin pending batch: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	const q = `
INSERT INTO put_ledger (category, file_name, revision, status, attempts)
VALUES (?, ?, ?, ?, 0)
ON CONFLICT(category, file_name, revision) DO NOTHING;`

	for _, item := range items {
		if _, err := tx.ExecContext(
			ctx,
			q,
			item.Category.String(),
			item.FileName,
			item.Revision,
			string(domain.StatusPending),
		); err != nil {
			return fmt.Errorf(
				"ledger: insert pending %s %q rev=%d: %w",
				item.Category,
				item.FileName,
				item.Revision,
				err,
			)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ledger: commit pending batch: %w", err)
	}

	return nil
}

// LookupPut 은 category 범위에서 (file_name, revision) 들의 현재
// put_ledger 상태를 일괄 조회한다. 행이 없는 항목은 결과 맵에 없다.
//
// LookupCommon 과는 별개의 신설 함수다.
// LookupCommon 은 common_ledger(revision·size·mtime) 조회라 attempts 가
// 없는 것이 당연하며, 수정하지 않는다. 후보 필터가 필요로 하는
// status/attempts 는 이 함수가 put_ledger 에서 읽는다.
//
// 후보 판정 규칙 (조회 결과를 쓰는 쪽 — put 조립):
//
//	행 없음                          → 후보 (신규. InsertPendingBatch 대상)
//	PENDING                          → 후보 (직전 실행이 등록 후 죽은 고아. 재개)
//	IN_PROGRESS                      → 제외 (시작 시 회수 절차 몫 — transport)
//	FAILED && attempts <  MaxRetries → 후보 (INSERT 불필요. BeginPut 이 직접 올림)
//	FAILED && attempts >= MaxRetries → 제외 (자동 재시도 소진)
//	VERIFIED                         → 제외 (해당 revision 종료 상태)
//
// 소진 제외는 조용히 하면 안 된다. 이 필터를 적용하는 쪽(put 조립)이
// 제외 건수와 예시 파일명 몇 개를 WARN 으로 남긴다.
// ledger 는 순수 계층이므로 여기서 로깅하지 않는다 —
// attempts 를 반환하는 것까지가 이 계층의 책임이다.
func (db *DB) LookupPut(
	ctx context.Context,
	cat domain.Category,
	keys []NameRev,
) (map[NameRev]PutState, error) {
	parsed, err := domain.ParseCategory(string(cat))
	if err != nil || parsed != cat {
		return nil, fmt.Errorf(
			"%w: %q",
			ErrInvalidLookupCategory,
			cat,
		)
	}

	unique := make([]NameRev, 0, len(keys))
	seen := make(map[NameRev]struct{}, len(keys))

	for _, key := range keys {
		if key.FileName == "" ||
			domain.NormalizeName(key.FileName) != key.FileName {
			return nil, fmt.Errorf(
				"%w: %q",
				ErrInvalidLookupName,
				key.FileName,
			)
		}

		if key.Revision < 1 {
			return nil, fmt.Errorf(
				"%w: %d for %q",
				ErrInvalidLookupRevision,
				key.Revision,
				key.FileName,
			)
		}

		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}
		unique = append(unique, key)
	}

	out := make(map[NameRev]PutState, len(unique))

	for start := 0; start < len(unique); start += maxPutLookupKeys {
		end := start + maxPutLookupKeys
		if end > len(unique) {
			end = len(unique)
		}

		if err := db.lookupPutChunk(
			ctx,
			cat,
			unique[start:end],
			out,
		); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (db *DB) lookupPutChunk(
	ctx context.Context,
	cat domain.Category,
	keys []NameRev,
	out map[NameRev]PutState,
) error {
	if len(keys) == 0 {
		return nil
	}

	// (file_name, revision) IN ((?,?), ...) — LookupCommon 의
	// file_name IN (...) 청크 방식과 같은 계열이다.
	// 키 1개당 바인드가 2개인 것이 maxPutLookupKeys 를 따로 둔 이유다.
	tuples := make([]string, 0, len(keys))
	args := make([]any, 0, 1+2*len(keys))
	args = append(args, cat.String())

	for _, key := range keys {
		tuples = append(tuples, "(?, ?)")
		args = append(args, key.FileName, key.Revision)
	}

	q := fmt.Sprintf(`
SELECT file_name, revision, status, attempts
  FROM put_ledger
 WHERE category = ?
   AND (file_name, revision) IN (%s);`,
		strings.Join(tuples, ", "),
	)

	rows, err := db.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf(
			"ledger: lookup put category=%s keys=%d: %w",
			cat,
			len(keys),
			err,
		)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			name     string
			revision int64
			status   string
			attempts int64
		)

		if err := rows.Scan(
			&name,
			&revision,
			&status,
			&attempts,
		); err != nil {
			return fmt.Errorf(
				"ledger: scan put lookup row: %w",
				err,
			)
		}

		st, err := domain.ParseStatus(status)
		if err != nil {
			return fmt.Errorf(
				"ledger: put row %q rev=%d: %w",
				name,
				revision,
				err,
			)
		}

		out[NameRev{FileName: name, Revision: revision}] = PutState{
			Status:   st,
			Attempts: attempts,
		}
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"ledger: iterate put lookup rows: %w",
			err,
		)
	}

	return nil
}

// BeginPut 은 전송 착수를 기록한다. 다음을 하나의 UPDATE 로 원자화한다.
//
//	status      → IN_PROGRESS
//	attempts    → attempts + 1   (누적 시도. 첫 시도 포함)
//	remote_path → 인자
//	part_path   → 인자
//	local_size  → 인자
//	error       → NULL
//
// remote_path/part_path 를 완료 시점이 아니라 여기서 기록하는 이유는
// schema.sql part_path 주석 참조 — 중단 시 잔여 .part 를 찾기 위해서다.
//
// WHERE 가 전이 가드다: status IN (PENDING, FAILED) AND attempts < ?
// 영향 행이 0 이면 ErrNotCandidate 를 반환한다. 다른 Worker 가
// 선점했거나(IN_PROGRESS/VERIFIED) 상한에 도달한 것이므로
// 호출자는 그 파일을 건너뛴다. 오류로 중단하지 않는다.
//
// maxRetries 는 반드시 1 이상이어야 한다. 아래 검증 주석 참조.
func (db *DB) BeginPut(
	ctx context.Context,
	key PutKey,
	remotePath, partPath string,
	localSize int64,
	maxRetries int,
) error {
	// maxRetries < 1 을 여기서 막는다.
	//
	// config.Validate 가 이미 거부하지만, 그 경로를 거치지 않고
	// DB 를 직접 여는 호출자(테스트, 운영 도구, 향후 진입점)가 있다.
	// lock.Acquire 가 staleAfter <= 0 을 입구에서 막는 것과 같은 이유다.
	//
	// 막지 않으면 attempts < 0 이 항상 거짓이 되어 모든 BeginPut 이
	// ErrNotCandidate 를 반환한다. 호출자는 그것을 "건너뛰기" 로 처리하므로
	// 프로그램은 오류 없이 정상 종료하고 전송만 0건이 된다.
	// 다음 회차도, 그다음도 마찬가지다. 로그에 원인이 남지 않는다.
	//
	// 반환 오류를 ErrNotCandidate 로 감싸지 않는 것이 이 검증의 핵심이다.
	// 감싸면 호출자가 그대로 건너뛰어 위 상황이 되돌아온다.
	if maxRetries < 1 {
		return fmt.Errorf(
			"ledger: begin put %s %q rev=%d: maxRetries must be at least 1, got %d",
			key.Category,
			key.FileName,
			key.Revision,
			maxRetries,
		)
	}

	res, err := db.conn.ExecContext(
		ctx,
		`
UPDATE put_ledger
   SET status      = ?,
       attempts    = attempts + 1,
       remote_path = ?,
       part_path   = ?,
       local_size  = ?,
       error       = NULL
 WHERE category = ?
   AND file_name = ?
   AND revision  = ?
   AND status IN (?, ?)
   AND attempts < ?;`,
		string(domain.StatusInProgress),
		remotePath,
		partPath,
		localSize,
		key.Category.String(),
		key.FileName,
		key.Revision,
		string(domain.StatusPending),
		string(domain.StatusFailed),
		maxRetries,
	)
	if err != nil {
		return fmt.Errorf(
			"ledger: begin put %s %q rev=%d: %w",
			key.Category,
			key.FileName,
			key.Revision,
			err,
		)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"ledger: begin put rows affected: %w",
			err,
		)
	}

	if n == 0 {
		return ErrNotCandidate
	}

	return nil
}

// FinishPut 은 IN_PROGRESS → VERIFIED 전이다.
//
// remote_size, sent_at, transfer_verified_at 을 기록하고 error 를 NULL 로 둔다.
// WHERE status = IN_PROGRESS. 영향 행 0 이면 오류 (전이 위반 관측).
//
// 전송 함수가 안 죽었다는 사실이 아니라 Transfer Verification
// (목적지 존재 + Size 대조) 통과 후에만 호출한다. (schema.sql 8.1)
func (db *DB) FinishPut(
	ctx context.Context,
	key PutKey,
	remoteSize int64,
	sentAt, verifiedAt time.Time,
) error {
	res, err := db.conn.ExecContext(
		ctx,
		`
UPDATE put_ledger
   SET status                = ?,
       remote_size           = ?,
       sent_at               = ?,
       transfer_verified_at  = ?,
       error                 = NULL
 WHERE category = ?
   AND file_name = ?
   AND revision  = ?
   AND status    = ?;`,
		string(domain.StatusVerified),
		remoteSize,
		sentAt.UTC().Unix(),
		verifiedAt.UTC().Unix(),
		key.Category.String(),
		key.FileName,
		key.Revision,
		string(domain.StatusInProgress),
	)
	if err != nil {
		return fmt.Errorf(
			"ledger: finish put %s %q rev=%d: %w",
			key.Category,
			key.FileName,
			key.Revision,
			err,
		)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"ledger: finish put rows affected: %w",
			err,
		)
	}

	if n == 0 {
		return fmt.Errorf(
			"ledger: finish put %s %q rev=%d: not IN_PROGRESS",
			key.Category,
			key.FileName,
			key.Revision,
		)
	}

	return nil
}

// FailPut 은 {PENDING, IN_PROGRESS} → FAILED 전이다. error 를 기록한다.
//
// PENDING → FAILED 는 업로드 시작 전 실패 경로다 (Template 확장 실패,
// 로컬 파일 소실). .part 가 없으므로 IN_PROGRESS 를 거치지 않는다.
// (domain/status.go 전이표 주석과 동일한 근거)
func (db *DB) FailPut(
	ctx context.Context,
	key PutKey,
	cause string,
) error {
	res, err := db.conn.ExecContext(
		ctx,
		`
UPDATE put_ledger
   SET status = ?,
       error  = ?
 WHERE category = ?
   AND file_name = ?
   AND revision  = ?
   AND status IN (?, ?);`,
		string(domain.StatusFailed),
		cause,
		key.Category.String(),
		key.FileName,
		key.Revision,
		string(domain.StatusPending),
		string(domain.StatusInProgress),
	)
	if err != nil {
		return fmt.Errorf(
			"ledger: fail put %s %q rev=%d: %w",
			key.Category,
			key.FileName,
			key.Revision,
			err,
		)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf(
			"ledger: fail put rows affected: %w",
			err,
		)
	}

	if n == 0 {
		return fmt.Errorf(
			"ledger: fail put %s %q rev=%d: not PENDING or IN_PROGRESS",
			key.Category,
			key.FileName,
			key.Revision,
		)
	}

	return nil
}

// ListInProgress 는 IN_PROGRESS 로 남은 잔여 항목을 반환한다.
//
// 비정상 종료의 흔적이며, 원격 .part 삭제 후 FAILED 로 되돌리는
// 회수 절차는 transport 와 함께 transport 도입 시 조립한다.
// 여기서는 재료 조회와 FailPut 재사용으로 충분하다.
//
// idx_put_status 인덱스를 탄다 (schema.sql 주석 참조).
func (db *DB) ListInProgress(
	ctx context.Context,
) ([]InProgressItem, error) {
	rows, err := db.conn.QueryContext(
		ctx,
		`
SELECT category, file_name, revision, part_path, remote_path
  FROM put_ledger
 WHERE status = ?
 ORDER BY category, file_name, revision;`,
		string(domain.StatusInProgress),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"ledger: list in_progress: %w",
			err,
		)
	}
	defer func() {
		_ = rows.Close()
	}()

	var out []InProgressItem

	for rows.Next() {
		var (
			item       InProgressItem
			category   string
			partPath   sql.NullString
			remotePath sql.NullString
		)

		if err := rows.Scan(
			&category,
			&item.FileName,
			&item.Revision,
			&partPath,
			&remotePath,
		); err != nil {
			return nil, fmt.Errorf(
				"ledger: scan in_progress row: %w",
				err,
			)
		}

		cat, err := domain.ParseCategory(category)
		if err != nil {
			return nil, fmt.Errorf(
				"ledger: in_progress row %q: %w",
				item.FileName,
				err,
			)
		}

		item.Category = cat

		if partPath.Valid {
			p := partPath.String
			item.PartPath = &p
		}

		if remotePath.Valid {
			r := remotePath.String
			item.RemotePath = &r
		}

		out = append(out, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"ledger: iterate in_progress rows: %w",
			err,
		)
	}

	return out, nil
}
