package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"SFTPClient/internal/domain"
)

// lookupTestDB 는 임시 디렉터리에 실제 SQLite Ledger 를 연다.
// 다른 테스트 파일의 헬퍼와 이름이 겹치지 않도록 lookup 접두어를 쓴다.
func lookupTestDB(t *testing.T) *DB {
	t.Helper()

	db, err := Open(
		context.Background(),
		filepath.Join(t.TempDir(), "rinex_ledger.db"),
	)
	if err != nil {
		t.Fatalf("Open() 실패: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() 실패: %v", err)
		}
	})

	return db
}

// lookupUpsert 는 정규화된 이름 하나를 common_ledger 에 넣는다.
// 반환값 검증은 common_test.go 의 몫이므로 여기서는 성공만 요구한다.
func lookupUpsert(
	t *testing.T,
	db *DB,
	category domain.Category,
	name string,
	size int64,
	mtime int64,
	origin domain.Origin,
) {
	t.Helper()

	_, err := db.UpsertCommon(context.Background(), CommonInput{
		FileName:          name,
		BaseName:          domain.BaseName(name),
		Category:          category,
		Size:              size,
		MTime:             mtime,
		Origin:            origin,
		IngressVerifiedAt: 1_700_000_000,
	})
	if err != nil {
		t.Fatalf("UpsertCommon(%s, %q) 실패: %v", category, name, err)
	}
}

// insertPutRow 는 put_ledger 에 행을 직접 넣는다.
//
// put_ledger 에 쓰는 정식 함수는 아직 없으므로(다음 단계),
// 같은 패키지의 특권으로 raw INSERT 를 사용한다.
// status 외의 열은 schema 기본값에 맡긴다.
// FK 제약이 있으므로 common_ledger 행을 먼저 만들어야 한다.
func insertPutRow(
	t *testing.T,
	db *DB,
	category domain.Category,
	name string,
	revision int64,
	status domain.Status,
) {
	t.Helper()

	_, err := db.conn.ExecContext(
		context.Background(),
		`INSERT INTO put_ledger (category, file_name, revision, status)
		 VALUES (?, ?, ?, ?);`,
		string(category),
		name,
		revision,
		string(status),
	)
	if err != nil {
		t.Fatalf(
			"put_ledger INSERT (%s, %q, rev=%d, %s) 실패: %v",
			category,
			name,
			revision,
			status,
			err,
		)
	}
}

func TestLookupCommonAbsentName(t *testing.T) {
	db := lookupTestDB(t)

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{"nope0010.26o.gz"},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	// 장부가 모르는 파일은 오류가 아니라 "map 에 없음" 이라는 답이다.
	// 호출자는 이것을 신규로 판정한다.
	if len(got) != 0 {
		t.Errorf("미등록 이름 조회 결과 = %v, want 빈 map", got)
	}
}

func TestLookupCommonNoPutHistory(t *testing.T) {
	db := lookupTestDB(t)

	const name = "a001.rnx.gz"
	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	k, ok := got[name]
	if !ok {
		t.Fatalf("등록된 이름이 결과에 없다: %v", got)
	}

	want := Known{
		FileName:  name,
		Revision:  1,
		Size:      100,
		MTime:     1_700_000_100,
		Origin:    domain.OriginLocal,
		State:     domain.StateReady,
		PutStatus: "",
	}

	if k != want {
		t.Errorf("Known = %+v, want %+v", k, want)
	}

	// "" 는 이 revision 의 전송 이력이 없다는 사실이다.
	if k.PutStatus.Valid() {
		t.Errorf("이력 없음이 유효한 Status 로 위장되었다: %q", k.PutStatus)
	}
}

func TestLookupCommonJoinsPutStatus(t *testing.T) {
	tests := []struct {
		name   string
		status domain.Status
	}{
		{"VERIFIED 매칭", domain.StatusVerified},
		{"FAILED 매칭", domain.StatusFailed},
		{"PENDING 매칭", domain.StatusPending},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := lookupTestDB(t)

			const name = "b001.rnx.gz"
			lookupUpsert(
				t, db, domain.CategoryRINEX3Hourly,
				name, 100, 1_700_000_100, domain.OriginLocal,
			)
			insertPutRow(
				t, db, domain.CategoryRINEX3Hourly,
				name, 1, tt.status,
			)

			got, err := db.LookupCommon(
				context.Background(),
				domain.CategoryRINEX3Hourly,
				[]string{name},
			)
			if err != nil {
				t.Fatalf("LookupCommon() 실패: %v", err)
			}

			if got[name].PutStatus != tt.status {
				t.Errorf(
					"PutStatus = %q, want %q",
					got[name].PutStatus,
					tt.status,
				)
			}
		})
	}
}

// 이 테스트가 lookup 의 존재 이유다.
//
// 파일이 갱신되어 revision 이 올라가면, 과거 revision 의 VERIFIED 는
// 조인에서 떨어져 PutStatus 가 "" 가 되어야 한다.
// 그래야 호출자가 이 파일을 다시 전송 대상으로 판정한다. (CONCEPT 4.5)
func TestLookupCommonRevisionMismatchDropsOldVerified(t *testing.T) {
	db := lookupTestDB(t)

	const name = "c001.rnx.gz"

	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)
	insertPutRow(
		t, db, domain.CategoryRINEX3Hourly,
		name, 1, domain.StatusVerified,
	)

	// revision 1 인 동안은 VERIFIED 가 보인다.
	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	if got[name].PutStatus != domain.StatusVerified {
		t.Fatalf(
			"rev1 PutStatus = %q, want VERIFIED",
			got[name].PutStatus,
		)
	}

	// 파일이 갱신된다. size 변경 → revision 2.
	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 200, 1_700_000_100, domain.OriginLocal,
	)

	got, err = db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	k := got[name]

	if k.Revision != 2 {
		t.Errorf("Revision = %d, want 2", k.Revision)
	}

	if k.State != domain.StateChanged {
		t.Errorf("State = %q, want CHANGED", k.State)
	}

	// 과거 revision 의 VERIFIED 는 더 이상 이 파일의 전송 상태가 아니다.
	if k.PutStatus != "" {
		t.Errorf(
			"rev2 PutStatus = %q, want \"\" (재전송 대상)",
			k.PutStatus,
		)
	}

	if k.Size != 200 {
		t.Errorf("Size = %d, want 200 (최신 관측값)", k.Size)
	}
}

func TestLookupCommonCarriesOriginAndState(t *testing.T) {
	db := lookupTestDB(t)

	const name = "d001.rnx.gz"
	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginDownload,
	)

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	// Ping-Pong 방지 판정의 재료가 그대로 전달되어야 한다.
	if got[name].Origin != domain.OriginDownload {
		t.Errorf("Origin = %q, want DOWNLOAD", got[name].Origin)
	}
}

func TestLookupCommonRejectsUnnormalizedNames(t *testing.T) {
	db := lookupTestDB(t)

	tests := []struct {
		name string
		in   string
	}{
		{"대문자", "A001.RNX.GZ"},
		{"경로 포함", `D:\RINEX\a001.rnx.gz`},
		{"part 접미사", "a001.rnx.gz.part"},
		{"빈 문자열", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.LookupCommon(
				context.Background(),
				domain.CategoryRINEX3Hourly,
				[]string{tt.in},
			)
			if !errors.Is(err, ErrInvalidLookupName) {
				t.Errorf(
					"LookupCommon(%q) = %v, want ErrInvalidLookupName",
					tt.in,
					err,
				)
			}
		})
	}
}

// TestLookupCommonAcceptsNormalizedFilepart 는 UNIT5 가드 비대칭을 고정한다.
//
// .part 는 NormalizeName 이 접미사를 떼므로 Lookup 가드가 거부한다.
// .filepart 는 정규화 불변이라 가드가 통과한다. 후보 제외는 IsPartFile
// 게이트에만 의존한다.
func TestLookupCommonAcceptsNormalizedFilepart(t *testing.T) {
	db := lookupTestDB(t)

	const name = "a001.rnx.gz.filepart"

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("LookupCommon(%q) = %v, want 빈 결과", name, err)
	}
	if len(got) != 0 {
		t.Fatalf("미등록 .filepart 조회 결과 = %+v, want 없음", got)
	}
}

func TestLookupCommonEmptyInput(t *testing.T) {
	db := lookupTestDB(t)

	for _, names := range [][]string{nil, {}} {
		got, err := db.LookupCommon(
			context.Background(),
			domain.CategoryRINEX3Hourly,
			names,
		)
		if err != nil {
			t.Fatalf("LookupCommon(%v) 실패: %v", names, err)
		}

		if len(got) != 0 {
			t.Errorf("빈 입력 결과 = %v, want 빈 map", got)
		}
	}
}

func TestLookupCommonDeduplicatesNames(t *testing.T) {
	db := lookupTestDB(t)

	const name = "e001.rnx.gz"
	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name, name, name},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	if len(got) != 1 {
		t.Errorf("중복 이름 결과 크기 = %d, want 1", len(got))
	}
}

func TestLookupCommonMixedPresentAndAbsent(t *testing.T) {
	db := lookupTestDB(t)

	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		"f001.rnx.gz", 100, 1_700_000_100, domain.OriginLocal,
	)
	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		"f002.rnx.gz", 200, 1_700_000_200, domain.OriginLocal,
	)

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{
			"f001.rnx.gz",
			"ghost.rnx.gz",
			"f002.rnx.gz",
		},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("결과 크기 = %d, want 2: %v", len(got), got)
	}

	if _, ok := got["ghost.rnx.gz"]; ok {
		t.Error("미등록 이름이 결과에 있다")
	}

	if got["f001.rnx.gz"].Size != 100 || got["f002.rnx.gz"].Size != 200 {
		t.Errorf("각 이름의 값이 섞였다: %v", got)
	}
}

// maxLookupNames 를 넘는 입력은 여러 조회로 나뉘어도 결과가 완전해야 한다.
func TestLookupCommonChunksLargeInput(t *testing.T) {
	db := lookupTestDB(t)

	const total = maxLookupNames + 1 // 501. 분할 경계 +1

	names := make([]string, 0, total)

	for i := 0; i < total; i++ {
		name := fmt.Sprintf("site%04d.rnx.gz", i)
		names = append(names, name)
		lookupUpsert(
			t, db, domain.CategoryRINEX3Hourly,
			name, int64(i+1), 1_700_000_100, domain.OriginLocal,
		)
	}

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		names,
	)
	if err != nil {
		t.Fatalf("LookupCommon(%d개) 실패: %v", total, err)
	}

	if len(got) != total {
		t.Fatalf("결과 크기 = %d, want %d", len(got), total)
	}

	// 분할 경계 양쪽의 값이 올바른 행과 짝지어졌는지 표본 확인.
	for _, i := range []int{0, maxLookupNames - 1, maxLookupNames} {
		name := fmt.Sprintf("site%04d.rnx.gz", i)

		if got[name].Size != int64(i+1) {
			t.Errorf(
				"%s Size = %d, want %d",
				name,
				got[name].Size,
				i+1,
			)
		}
	}
}

// --- 복합키(A~E) ---
//
// RINEX3/RINEX4 동일 file_name 공존이 단독 PK 재전송 루프를 만들지 않는지,
// Lookup 이 Category 경계를 지키는지 고정한다. (2026-08-30)

// A: 같은 file_name 이 서로 다른 Category 에 독립 행으로 존재한다.
func TestLookupCommonA_SameNameDifferentCategoriesAreIndependent(t *testing.T) {
	db := lookupTestDB(t)

	const name = "shared00kor_r_20260010000_01h_30s_mo.rnx.gz"

	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)
	lookupUpsert(
		t, db, domain.CategoryRINEX4Hourly,
		name, 200, 1_700_000_200, domain.OriginLocal,
	)

	if countCommon(t, db) != 2 {
		t.Fatalf("common_ledger 행 수 = %d, want 2", countCommon(t, db))
	}

	got3, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("Lookup RINEX3: %v", err)
	}

	got4, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX4Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("Lookup RINEX4: %v", err)
	}

	if got3[name].Size != 100 || got3[name].Revision != 1 {
		t.Errorf("RINEX3 Known = %+v, want size=100 rev=1", got3[name])
	}

	if got4[name].Size != 200 || got4[name].Revision != 1 {
		t.Errorf("RINEX4 Known = %+v, want size=200 rev=1", got4[name])
	}
}

// B: 한쪽 Category 조회는 다른 Category 의 동일 file_name 을 보지 않는다.
func TestLookupCommonB_LookupIsCategoryScoped(t *testing.T) {
	db := lookupTestDB(t)

	const name = "only4.rnx.gz"
	lookupUpsert(
		t, db, domain.CategoryRINEX4Hourly,
		name, 50, 1_700_000_100, domain.OriginLocal,
	)

	got, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	if len(got) != 0 {
		t.Errorf("다른 Category 행이 조회되었다: %v", got)
	}
}

// C: 한 Category 의 size 변경은 다른 Category 동일 file_name 의 revision 을 올리지 않는다.
func TestLookupCommonC_UpsertDoesNotCrossCategoryRevision(t *testing.T) {
	db := lookupTestDB(t)

	const name = "cross.rnx.gz"

	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)
	lookupUpsert(
		t, db, domain.CategoryRINEX4Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)

	// RINEX4 만 갱신 → revision 2.
	lookupUpsert(
		t, db, domain.CategoryRINEX4Hourly,
		name, 999, 1_700_000_100, domain.OriginLocal,
	)

	got3, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("Lookup RINEX3: %v", err)
	}

	got4, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX4Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("Lookup RINEX4: %v", err)
	}

	if got3[name].Revision != 1 || got3[name].Size != 100 {
		t.Errorf(
			"RINEX3 이 교차 갱신됨: %+v",
			got3[name],
		)
	}

	if got4[name].Revision != 2 || got4[name].Size != 999 {
		t.Errorf(
			"RINEX4 갱신 실패: %+v",
			got4[name],
		)
	}
}

// D: put_ledger JOIN 도 category 를 포함하므로 다른 Category 의 VERIFIED 를 가져오지 않는다.
func TestLookupCommonD_PutJoinIsCategoryScoped(t *testing.T) {
	db := lookupTestDB(t)

	const name = "putcross.rnx.gz"

	lookupUpsert(
		t, db, domain.CategoryRINEX3Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)
	lookupUpsert(
		t, db, domain.CategoryRINEX4Hourly,
		name, 100, 1_700_000_100, domain.OriginLocal,
	)
	insertPutRow(
		t, db, domain.CategoryRINEX3Hourly,
		name, 1, domain.StatusVerified,
	)

	got4, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX4Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("Lookup RINEX4: %v", err)
	}

	if got4[name].PutStatus != "" {
		t.Errorf(
			"다른 Category 의 VERIFIED 가 조인됨: %q",
			got4[name].PutStatus,
		)
	}

	got3, err := db.LookupCommon(
		context.Background(),
		domain.CategoryRINEX3Hourly,
		[]string{name},
	)
	if err != nil {
		t.Fatalf("Lookup RINEX3: %v", err)
	}

	if got3[name].PutStatus != domain.StatusVerified {
		t.Errorf(
			"RINEX3 PutStatus = %q, want VERIFIED",
			got3[name].PutStatus,
		)
	}
}

// E: 잘못된 Category 는 names 가 비어 있어도 ErrInvalidLookupCategory 이다.
func TestLookupCommonE_RejectsInvalidCategory(t *testing.T) {
	db := lookupTestDB(t)

	tests := []struct {
		name     string
		category domain.Category
		names    []string
	}{
		{"알 수 없는 값 + 이름", domain.Category("RINEX5_HOURLY"), []string{"a.rnx.gz"}},
		{"느슨한 표기 + 이름", domain.Category("rinex3_hourly"), []string{"a.rnx.gz"}},
		{"빈 Category + nil names", domain.Category(""), nil},
		{"알 수 없는 값 + 빈 slice", domain.Category("NOPE"), []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := db.LookupCommon(
				context.Background(),
				tt.category,
				tt.names,
			)
			if !errors.Is(err, ErrInvalidLookupCategory) {
				t.Errorf(
					"error = %v, want ErrInvalidLookupCategory",
					err,
				)
			}
		})
	}
}
