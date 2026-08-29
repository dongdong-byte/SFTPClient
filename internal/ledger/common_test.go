package ledger

import (
	"context"
	"errors"
	"testing"

	"SFTPClient/internal/domain"
)

const (
	testRawName = "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz"

	// testPathLike 는 "경로가 섞인 file_name" 을 거부하는지 확인할 때만 쓴다.
	// schema v5 에서 local_path 컬럼이 삭제되었으므로 저장되는 값이 아니다.
	testPathLike = `D:\RINEX3\2026\001\03\` + testRawName
)

// newInput 은 Ingress 검증을 통과한 정상적인 CommonInput 하나를 만든다.
// 각 테스트는 필요한 필드만 바꿔 사용한다.
func newInput() CommonInput {
	return CommonInput{
		FileName:          domain.NormalizeName(testRawName),
		BaseName:          domain.BaseName(testRawName),
		Category:          domain.CategoryRINEX3Hourly,
		Size:              1_048_576,
		MTime:             1_767_225_600,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: 1_767_225_900,
	}
}

// commonRow 는 테스트에서 확인할 common_ledger 컬럼을 담는다.
type commonRow struct {
	baseName          string
	category          string
	size              int64
	mtime             int64
	origin            string
	revision          int64
	state             string
	firstSeen         int64
	ingressVerifiedAt int64
}

func readCommon(t *testing.T, db *DB, fileName string) commonRow {
	t.Helper()

	var r commonRow

	err := db.conn.QueryRowContext(
		context.Background(),
		`
		SELECT
			base_name,
			category,
			size,
			mtime,
			origin,
			revision,
			state,
			first_seen,
			ingress_verified_at
		  FROM common_ledger
		 WHERE file_name = ?;
		`,
		fileName,
	).Scan(
		&r.baseName,
		&r.category,
		&r.size,
		&r.mtime,
		&r.origin,
		&r.revision,
		&r.state,
		&r.firstSeen,
		&r.ingressVerifiedAt,
	)

	if err != nil {
		t.Fatalf(
			"common_ledger 조회 실패 (%q): %v",
			fileName,
			err,
		)
	}

	return r
}

func countCommon(t *testing.T, db *DB) int {
	t.Helper()

	var count int

	if err := db.conn.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM common_ledger;`,
	).Scan(&count); err != nil {
		t.Fatalf("common_ledger 행 수 조회 실패: %v", err)
	}

	return count
}

func upsert(t *testing.T, db *DB, in CommonInput) Result {
	t.Helper()

	got, err := db.UpsertCommon(
		context.Background(),
		in,
	)
	if err != nil {
		t.Fatalf(
			"UpsertCommon() 실패: %v",
			err,
		)
	}

	return got
}

// schema v5 는 common_ledger 에서 local_path 를 삭제했다.
//
// 컬럼이 되살아나면 "무엇을 어디서 보낼지" 를 DB 가 지시하는 v4 구조로
// 되돌아갈 여지가 생긴다. 그 회귀는 컴파일 오류를 내지 않고,
// UpsertCommon 이 그 컬럼을 쓰지 않는 한 다른 테스트도 모두 통과한다.
// 스키마 파일이 v4 로 되돌려지는 사고를 여기서 잡는다.
//

func TestUpsertCommonInserts(t *testing.T) {
	db := newTestDB(t)
	in := newInput()

	if got := upsert(t, db, in); got != ResultInserted {
		t.Fatalf(
			"Result = %v, want Inserted",
			got,
		)
	}

	row := readCommon(t, db, in.FileName)

	if row.baseName != in.BaseName {
		t.Errorf(
			"base_name = %q, want %q",
			row.baseName,
			in.BaseName,
		)
	}

	if row.category != string(in.Category) {
		t.Errorf(
			"category = %q, want %q",
			row.category,
			in.Category,
		)
	}

	if row.revision != 1 {
		t.Errorf(
			"revision = %d, want 1",
			row.revision,
		)
	}

	if row.state != string(domain.StateReady) {
		t.Errorf(
			"state = %q, want %q",
			row.state,
			domain.StateReady,
		)
	}

	if row.size != in.Size {
		t.Errorf(
			"size = %d, want %d",
			row.size,
			in.Size,
		)
	}

	if row.mtime != in.MTime {
		t.Errorf(
			"mtime = %d, want %d",
			row.mtime,
			in.MTime,
		)
	}

	if row.origin != string(in.Origin) {
		t.Errorf(
			"origin = %q, want %q",
			row.origin,
			in.Origin,
		)
	}

	if row.firstSeen <= 0 {
		t.Errorf(
			"first_seen = %d, 채워지지 않았다",
			row.firstSeen,
		)
	}

	if row.ingressVerifiedAt != in.IngressVerifiedAt {
		t.Errorf(
			"ingress_verified_at = %d, want %d",
			row.ingressVerifiedAt,
			in.IngressVerifiedAt,
		)
	}
}

// 정상 운영에서 가장 흔한 경로이다.
//
// 이미 등록된 파일을 다음 Scan 에서 다시 보더라도
// size 와 mtime 이 모두 같으면 아무 것도 변경하지 않는다.
//
// ingress_verified_at 을 의도적으로 다르게 넣어
// SQL 의 WHERE 조건이 false 일 때 UPDATE 자체가 일어나지 않는지도 확인한다.
// v5 에서 local_path 가 사라졌으므로 이 값이 유일한 확인 수단이다.
//
// WHERE 조건을 두는 이유는 쓰기 비용이다.
// 조건 없이 DO UPDATE 를 두면 값이 같아도 페이지가 갱신되어 WAL 이 커진다.
// 한 번의 Scan 이 수만 건을 훑고 그 대부분이 이 경로이므로 차이가 크다.
func TestUpsertCommonUnchanged(t *testing.T) {
	db := newTestDB(t)
	in := newInput()

	upsert(t, db, in)

	before := readCommon(t, db, in.FileName)

	rescanned := in
	rescanned.IngressVerifiedAt = in.IngressVerifiedAt + 3600

	if got := upsert(t, db, rescanned); got != ResultUnchanged {
		t.Fatalf(
			"두 번째 Result = %v, want Unchanged",
			got,
		)
	}

	after := readCommon(t, db, in.FileName)

	if after.revision != before.revision {
		t.Errorf(
			"revision 이 변했다: %d → %d",
			before.revision,
			after.revision,
		)
	}

	if after.state != string(domain.StateReady) {
		t.Errorf(
			"state = %q, want %q",
			after.state,
			domain.StateReady,
		)
	}

	if after.firstSeen != before.firstSeen {
		t.Errorf(
			"first_seen 이 변했다: %d → %d",
			before.firstSeen,
			after.firstSeen,
		)
	}

	if after.ingressVerifiedAt != before.ingressVerifiedAt {
		t.Errorf(
			"ingress_verified_at 이 변경되었다: %d → %d",
			before.ingressVerifiedAt,
			after.ingressVerifiedAt,
		)
	}
}

func TestUpsertCommonSizeChanged(t *testing.T) {
	db := newTestDB(t)
	in := newInput()

	upsert(t, db, in)

	changed := in
	changed.Size = in.Size + 4096
	changed.IngressVerifiedAt = in.IngressVerifiedAt + 3600

	if got := upsert(t, db, changed); got != ResultUpdated {
		t.Fatalf(
			"Result = %v, want Updated",
			got,
		)
	}

	row := readCommon(t, db, in.FileName)

	if row.revision != 2 {
		t.Errorf(
			"revision = %d, want 2",
			row.revision,
		)
	}

	if row.state != string(domain.StateChanged) {
		t.Errorf(
			"state = %q, want %q",
			row.state,
			domain.StateChanged,
		)
	}

	if row.size != changed.Size {
		t.Errorf(
			"size = %d, want %d",
			row.size,
			changed.Size,
		)
	}

	if row.ingressVerifiedAt != changed.IngressVerifiedAt {
		t.Errorf(
			"ingress_verified_at = %d, want %d",
			row.ingressVerifiedAt,
			changed.IngressVerifiedAt,
		)
	}
}

// size 는 그대로인데 mtime 만 바뀌는 경우도 실제 운영에서 존재할 수 있다.
// 같은 크기의 파일이 다시 쓰이거나 복구되는 경우를 놓치지 않기 위해
// mtime 변경만으로도 revision 을 증가시킨다.
//
// 이 테스트는 SQL 의 OR 선택을 고정한다. AND 로 바꾸면 여기서 깨진다.
// 그 대가로 볼륨 이전이나 대량 복사처럼 mtime 이 일괄 갱신되는 상황에서는
// Scan 범위 안의 파일이 전부 재전송된다. 결함이 아니라
// "누락보다 헛전송이 낫다" 는 선택의 결과이다.
// (upsertCommonSQL 주석, CONCEPT 4.9)
func TestUpsertCommonMTimeChanged(t *testing.T) {
	db := newTestDB(t)
	in := newInput()

	upsert(t, db, in)

	changed := in
	changed.MTime = in.MTime + 60

	if got := upsert(t, db, changed); got != ResultUpdated {
		t.Fatalf(
			"Result = %v, want Updated",
			got,
		)
	}

	row := readCommon(t, db, in.FileName)

	if row.revision != 2 {
		t.Errorf(
			"revision = %d, want 2",
			row.revision,
		)
	}

	if row.state != string(domain.StateChanged) {
		t.Errorf(
			"state = %q, want %q",
			row.state,
			domain.StateChanged,
		)
	}

	if row.mtime != changed.MTime {
		t.Errorf(
			"mtime = %d, want %d",
			row.mtime,
			changed.MTime,
		)
	}
}

// 여러 번 갱신되면 revision 이 누적되어야 한다.
//
// put_ledger 가 (file_name, revision) 단위로 이력을 쌓기 때문에
// revision 이 겹치면 과거 전송 이력과 현재 전송 이력을 구분할 수 없다.
func TestUpsertCommonRevisionAccumulates(t *testing.T) {
	db := newTestDB(t)
	in := newInput()

	upsert(t, db, in)

	for i := 1; i <= 3; i++ {
		next := in
		next.Size = in.Size + int64(i*1024)

		if got := upsert(t, db, next); got != ResultUpdated {
			t.Fatalf(
				"%d 번째 갱신 Result = %v, want Updated",
				i,
				got,
			)
		}
	}

	row := readCommon(t, db, in.FileName)

	if row.revision != 4 {
		t.Errorf(
			"revision = %d, want 4",
			row.revision,
		)
	}
}

// first_seen 은 최초 입고 시점이므로 revision 이 올라가도 보존되어야 한다.
//
// 실제 시간으로만 비교하면 두 호출이 같은 초에 실행되어
// 잘못된 코드가 있어도 우연히 테스트가 통과할 수 있으므로 marker 를 사용한다.
func TestUpsertCommonPreservesFirstSeen(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	in := newInput()

	upsert(t, db, in)

	const marker = int64(1_000_000)

	_, err := db.conn.ExecContext(
		ctx,
		`
		UPDATE common_ledger
		   SET first_seen = ?
		 WHERE file_name = ?;
		`,
		marker,
		in.FileName,
	)

	if err != nil {
		t.Fatalf(
			"first_seen 표식 설정 실패: %v",
			err,
		)
	}

	changed := in
	changed.Size = in.Size + 1

	upsert(t, db, changed)

	row := readCommon(t, db, in.FileName)

	if row.firstSeen != marker {
		t.Errorf(
			"first_seen = %d, want %d",
			row.firstSeen,
			marker,
		)
	}
}

// DOWNLOAD 로 받은 파일을 Scanner 가 나중에 다시 발견해 LOCAL 을 넘기더라도
// origin 은 DOWNLOAD 로 유지되어야 한다.
//
// 이 값이 LOCAL 로 덮어써지면 PUT 후보에서 제외되지 않아
// 중계 구성에서 Ping-Pong 이 발생할 수 있다.
func TestUpsertCommonPreservesOrigin(t *testing.T) {
	db := newTestDB(t)

	downloaded := newInput()
	downloaded.Origin = domain.OriginDownload

	upsert(t, db, downloaded)

	rescanned := downloaded
	rescanned.Origin = domain.OriginLocal
	rescanned.Size = downloaded.Size + 1

	if got := upsert(t, db, rescanned); got != ResultUpdated {
		t.Fatalf(
			"Result = %v, want Updated",
			got,
		)
	}

	row := readCommon(t, db, downloaded.FileName)

	if row.origin != string(domain.OriginDownload) {
		t.Errorf(
			"origin = %q, want %q",
			row.origin,
			domain.OriginDownload,
		)
	}
}

// state 는 한 번 CHANGED 가 된 뒤에는
// 이후 revision 이 증가해도 READY 로 돌아가지 않는다.
func TestUpsertCommonStateDoesNotRevert(t *testing.T) {
	db := newTestDB(t)
	in := newInput()

	upsert(t, db, in)

	changed := in
	changed.Size = in.Size + 1
	upsert(t, db, changed)

	again := changed
	again.Size = changed.Size + 1
	upsert(t, db, again)

	row := readCommon(t, db, in.FileName)

	if row.state != string(domain.StateChanged) {
		t.Errorf(
			"state = %q, want %q",
			row.state,
			domain.StateChanged,
		)
	}

	if row.revision != 3 {
		t.Errorf(
			"revision = %d, want 3",
			row.revision,
		)
	}
}

// 압축 파일과 비압축 파일은 서로 다른 file_name 이므로
// 별도의 Ledger 행으로 저장되어야 한다.
//
// 다만 base_name 은 같아 압축·비압축 동시 유입 여부를 운영에서 확인할 수 있다.
func TestUpsertCommonKeepsCompressedAndPlainSeparate(t *testing.T) {
	db := newTestDB(t)

	const plainRaw = "SONP00KOR_R_20260010300_01H_01S_MS.rnx"

	gz := newInput()

	plain := newInput()
	plain.FileName = domain.NormalizeName(plainRaw)
	plain.BaseName = domain.BaseName(plainRaw)
	plain.Size = gz.Size * 3

	upsert(t, db, gz)
	upsert(t, db, plain)

	if got := countCommon(t, db); got != 2 {
		t.Errorf(
			"행 수 = %d, want 2",
			got,
		)
	}

	if gz.BaseName != plain.BaseName {
		t.Errorf(
			"base_name 이 다르다: %q vs %q",
			gz.BaseName,
			plain.BaseName,
		)
	}
}

func TestCommonInputValidate(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*CommonInput)
	}{
		{
			name: "빈 file_name",
			modify: func(in *CommonInput) {
				in.FileName = ""
			},
		},
		{
			name: "정규화되지 않은 file_name",
			modify: func(in *CommonInput) {
				in.FileName = testRawName
			},
		},
		{
			name: "경로가 섞인 file_name",
			modify: func(in *CommonInput) {
				in.FileName = testPathLike
			},
		},
		{
			name: "file_name 과 맞지 않는 base_name",
			modify: func(in *CommonInput) {
				in.BaseName = "something_else.rnx"
			},
		},
		{
			name: "알 수 없는 category",
			modify: func(in *CommonInput) {
				in.Category = domain.Category("RINEX5_HOURLY")
			},
		},
		{
			name: "소문자 category",
			modify: func(in *CommonInput) {
				in.Category = domain.Category("rinex3_hourly")
			},
		},
		{
			name: "지원하지 않는 RECEIVER origin",
			modify: func(in *CommonInput) {
				in.Origin = domain.Origin("RECEIVER")
			},
		},
		{
			name: "0 byte",
			modify: func(in *CommonInput) {
				in.Size = 0
			},
		},
		{
			name: "음수 size",
			modify: func(in *CommonInput) {
				in.Size = -1
			},
		},
		{
			name: "채워지지 않은 mtime",
			modify: func(in *CommonInput) {
				in.MTime = 0
			},
		},
		{
			name: "채워지지 않은 ingress_verified_at",
			modify: func(in *CommonInput) {
				in.IngressVerifiedAt = 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := newInput()
			tt.modify(&in)

			err := in.Validate()

			if !errors.Is(err, ErrInvalidInput) {
				t.Errorf(
					"Validate() = %v, want ErrInvalidInput",
					err,
				)
			}
		})
	}

	t.Run("정상 입력", func(t *testing.T) {
		if err := newInput().Validate(); err != nil {
			t.Errorf(
				"Validate() = %v, want nil",
				err,
			)
		}
	})
}

// Scanner 는 0 byte 파일도 사실로서 발견할 수 있다.
//
// 본 프로그램이 대상으로 삼는 최종 DOY/Hourly 경로에는
// QC·배분이 끝난 완제품이 놓이는 것이 정상이다.
// 다만 파일이 서버로 전달되는 짧은 순간에 Scan 이 겹치거나,
// 비정상 전송이 남긴 파일을 관측하면 0 byte 또는 부분 파일이
// 보일 가능성을 완전히 배제할 수 없다.
//
// 이런 파일을 거르는 책임은 Scanner 가 아니라 verify 에 있다.
// CommonInput 은 "Ingress 검증을 이미 통과한 파일"이라는 의미이므로
// size=0 상태에서는 Ledger 에 기록해서는 안 된다.
//
// 이후 정상 파일이 관측되면 다음 Scan 에서 다시 검증하고 등록한다.
func TestUpsertCommonDoesNotRecordZeroSizeYet(t *testing.T) {
	db := newTestDB(t)

	in := newInput()
	in.Size = 0

	got, err := db.UpsertCommon(
		context.Background(),
		in,
	)

	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf(
			"오류 = %v, want ErrInvalidInput",
			err,
		)
	}

	if got != ResultUnchanged {
		t.Errorf(
			"Result = %v, want Unchanged",
			got,
		)
	}

	if count := countCommon(t, db); count != 0 {
		t.Errorf(
			"아직 Ingress 미통과인 0 byte 파일이 저장되었다: count=%d",
			count,
		)
	}
}

func TestUpsertCommonRejectsInvalidInput(t *testing.T) {
	db := newTestDB(t)

	in := newInput()
	in.FileName = testRawName

	got, err := db.UpsertCommon(
		context.Background(),
		in,
	)

	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf(
			"오류 = %v, want ErrInvalidInput",
			err,
		)
	}

	if got != ResultUnchanged {
		t.Errorf(
			"Result = %v, want Unchanged",
			got,
		)
	}
}

func TestResultString(t *testing.T) {
	tests := []struct {
		in   Result
		want string
	}{
		{ResultUnchanged, "Unchanged"},
		{ResultInserted, "Inserted"},
		{ResultUpdated, "Updated"},
		{Result(99), "Result(99)"},
	}

	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf(
				"Result(%d).String() = %q, want %q",
				int(tt.in),
				got,
				tt.want,
			)
		}
	}
}
