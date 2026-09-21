package ledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
)

// 운영 시작일 (resend 설계 v4 §3.5) 테스트.
//
// 고정하는 계약:
//  1. 새(빈) DB — 기록하지 않는다. OperationOrigin 은 now 의 날짜다.
//  2. 빈 Open(dry-run) 뒤 늦게 운영을 시작해도 origin 은 빈 Open 날짜가
//     아니라 첫 행의 first_seen 날짜다 (설치 후 대량 재전송 방지).
//  3. 기존 DB — 값이 없으면 MIN(first_seen) 의 UTC 날짜로 백필된다.
//  4. 한 번 기록하면 재Open 이 바꾸지 않는다.
//  5. 이 기록은 schema_version 을 올리지 않는다.
//  6. OperationOrigin 은 UTC 자정 시각을 돌려준다.

// utcDay 는 시각을 UTC 자정으로 자른다.
func utcDay(t time.Time) time.Time {
	u := t.UTC()

	return time.Date(
		u.Year(), u.Month(), u.Day(),
		0, 0, 0, 0,
		time.UTC,
	)
}

// hasOriginKey 는 schema_meta 에 operation_origin 이 기록됐는지 답한다.
func hasOriginKey(t *testing.T, db *DB) bool {
	t.Helper()

	_, err := db.SchemaMeta(context.Background(), operationOriginKey)
	if err == nil {
		return true
	}

	if !errors.Is(err, ErrSchemaMetaMissing) {
		t.Fatalf("SchemaMeta(%s) 실패: %v", operationOriginKey, err)
	}

	return false
}

// insertSeen 은 행 하나를 넣고 first_seen 을 seen 으로 조작한다.
// UpsertCommon 은 first_seen 을 now 로 채우므로 직접 UPDATE 한다
// (common_test.go 의 first_seen 보존 테스트와 같은 수법).
func insertSeen(t *testing.T, db *DB, raw string, seen time.Time) {
	t.Helper()

	ctx := context.Background()

	in := CommonInput{
		FileName:          domain.NormalizeName(raw),
		BaseName:          domain.BaseName(raw),
		Category:          domain.CategoryRINEX3Daily,
		Size:              1024,
		MTime:             seen.Unix(),
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: seen.Unix(),
	}

	if _, err := db.UpsertCommon(ctx, in); err != nil {
		t.Fatalf("UpsertCommon(%s) 실패: %v", raw, err)
	}

	if _, err := db.conn.ExecContext(
		ctx,
		`UPDATE common_ledger
		    SET first_seen = ?
		  WHERE file_name = ?;`,
		seen.Unix(),
		domain.NormalizeName(raw),
	); err != nil {
		t.Fatalf("first_seen 조작 실패: %v", err)
	}
}

// TestOperationOrigin_NewDB — 빈 DB 는 기록하지 않고, 조회는 now 의
// 날짜다. 오늘이 하한이면 자동 창(To = today − ScanDays)은 항상 빈다.
func TestOperationOrigin_NewDB(t *testing.T) {
	ctx := context.Background()

	db := newTestDB(t)

	if hasOriginKey(t, db) {
		t.Fatal("빈 DB 인데 operation_origin 이 기록됐다")
	}

	now := time.Date(2026, 9, 21, 23, 30, 0, 0, time.UTC)

	got, err := db.OperationOrigin(ctx, now)
	if err != nil {
		t.Fatalf("OperationOrigin() 실패: %v", err)
	}

	if !got.Equal(utcDay(now)) {
		t.Errorf(
			"origin = %s, want %s (now 의 날짜)",
			got.Format(originDateLayout),
			utcDay(now).Format(originDateLayout),
		)
	}

	// UTC 자정 계약 — 시·분·초가 붙어 있으면 창 하한 비교가 어긋난다.
	if !got.Equal(utcDay(got)) || got.Location() != time.UTC {
		t.Errorf("origin 이 UTC 자정이 아니다: %v", got)
	}

	// KST 로 넘긴 now 도 UTC 날짜로 자른다 (KST 09-22 08:30 = UTC 09-21).
	kst := time.FixedZone("KST", 9*60*60)
	got, err = db.OperationOrigin(ctx, time.Date(2026, 9, 22, 8, 30, 0, 0, kst))
	if err != nil {
		t.Fatalf("OperationOrigin(KST) 실패: %v", err)
	}

	if got.Format(originDateLayout) != "2026-09-21" {
		t.Errorf("KST now 의 origin = %s, want 2026-09-21",
			got.Format(originDateLayout))
	}
}

// TestOperationOrigin_DryRunThenLateStart — 설치 사고 재현.
//
// 설치일에 --dry-run 으로 설정만 확인했다(빈 Open). 실제 운영은 몇 주 뒤
// 시작했다. 빈 Open 이 그날을 origin 으로 굳히면, 첫 live 회차 뒤 자동
// resend 창이 [빈 Open 날짜, today − ScanDays] 가 되어 장부에 행이 없는
// 그 사이 날짜 전체를 신규로 판정해 대량 재전송한다.
// origin 은 첫 행의 first_seen(= 첫 실제 스캔) 날짜여야 한다.
func TestOperationOrigin_DryRunThenLateStart(t *testing.T) {
	ctx := context.Background()

	// 설치일 dry-run — 빈 Open.
	path, db := newTestDBAt(t)
	if hasOriginKey(t, db) {
		t.Fatal("dry-run(빈 Open) 이 operation_origin 을 기록했다")
	}

	// 몇 주 뒤 첫 live 회차가 행을 만든다.
	firstLive := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	insertSeen(t, db, "DBON00KOR_R_20262880000_01D_MO.rnx.gz", firstLive)

	// 같은 프로세스의 resend 단계 — 아직 기록 전이지만 값은 같다.
	got, err := db.OperationOrigin(ctx, firstLive)
	if err != nil {
		t.Fatalf("OperationOrigin() 실패: %v", err)
	}

	if !got.Equal(utcDay(firstLive)) {
		t.Errorf("기록 전 origin = %s, want %s",
			got.Format(originDateLayout),
			utcDay(firstLive).Format(originDateLayout))
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close() 실패: %v", err)
	}

	// 다음 정시 — Open 이 첫 live 날짜로 기록한다.
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("재Open 실패: %v", err)
	}
	defer closeDB(t, db2)

	if !hasOriginKey(t, db2) {
		t.Fatal("행이 생긴 뒤 Open 인데 operation_origin 이 기록되지 않았다")
	}

	got, err = db2.OperationOrigin(ctx, firstLive.AddDate(0, 0, 30))
	if err != nil {
		t.Fatalf("OperationOrigin() 실패: %v", err)
	}

	if !got.Equal(utcDay(firstLive)) {
		t.Errorf("origin = %s, want 첫 live 날짜 %s",
			got.Format(originDateLayout),
			utcDay(firstLive).Format(originDateLayout))
	}
}

// TestOperationOrigin_BackfillFromFirstSeen — 값이 없는 기존 DB 는
// MIN(first_seen) 의 UTC 날짜로 한 번 채워진다 (§3.5).
//
// 커밋 4 이전에 만들어진 운영 DB 를 재현한다: 행이 쌓인 DB 에서
// operation_origin 키를 지운 뒤 다시 Open 한다.
func TestOperationOrigin_BackfillFromFirstSeen(t *testing.T) {
	ctx := context.Background()

	path, db := newTestDBAt(t)

	// 행 두 개를 넣고 first_seen 을 과거로 조작한다. 빈 Open 은 키를
	// 쓰지 않으므로 이 상태가 곧 커밋 4 이전의 운영 DB 다.
	// 둘 다 UTC 14:30 이후라 KST 로는 다음 날이다 — UTC 날짜를 쓰는지 본다.
	older := time.Date(2026, 3, 5, 16, 30, 0, 0, time.UTC)
	newer := time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC)

	insertSeen(t, db, "DBON00KOR_R_20260640000_01D_MO.rnx.gz", older)
	insertSeen(t, db, "SONP00KOR_R_20260910000_01D_MO.rnx.gz", newer)

	if hasOriginKey(t, db) {
		t.Fatal("재현 전제 위반: 키가 이미 있다")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close() 실패: %v", err)
	}

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("재Open 실패: %v", err)
	}
	defer closeDB(t, db2)

	if !hasOriginKey(t, db2) {
		t.Fatal("백필이 기록되지 않았다")
	}

	got, err := db2.OperationOrigin(ctx, time.Now())
	if err != nil {
		t.Fatalf("OperationOrigin() 실패: %v", err)
	}

	want := utcDay(older)
	if !got.Equal(want) {
		t.Errorf(
			"origin = %s, want %s (MIN(first_seen) 의 날짜)",
			got.Format(originDateLayout),
			want.Format(originDateLayout),
		)
	}

	version, err := db2.SchemaMeta(ctx, "schema_version")
	if err != nil || version != schemaVersion {
		t.Errorf("schema_version = %q, err = %v — origin 백필이 세대를 올리면 안 된다",
			version, err)
	}
}

// TestOperationOrigin_ImmutableAcrossReopen — 한 번 기록하면 재Open 이
// 바꾸지 않는다 (§3.5). 값을 표식으로 바꿔 두고 재Open 후 그대로인지
// 본다. 행이 쌓여 MIN(first_seen) 이 무엇이든 기존 값이 이긴다.
func TestOperationOrigin_ImmutableAcrossReopen(t *testing.T) {
	ctx := context.Background()

	path, db := newTestDBAt(t)

	// 표식은 MIN(first_seen) 과 다른 날이어야 "재계산이 없다"를 증명한다.
	insertSeen(t, db, "DBON00KOR_R_20262500000_01D_MO.rnx.gz",
		time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC))

	const marker = "2000-01-01"

	if _, err := db.conn.ExecContext(
		ctx,
		`INSERT INTO schema_meta (key, value, updated_at)
		 VALUES (?, ?, 0);`,
		operationOriginKey,
		marker,
	); err != nil {
		t.Fatalf("표식 설정 실패: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("Close() 실패: %v", err)
	}

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("재Open 실패: %v", err)
	}
	defer closeDB(t, db2)

	got, err := db2.OperationOrigin(ctx, time.Now())
	if err != nil {
		t.Fatalf("OperationOrigin() 실패: %v", err)
	}

	if got.Format(originDateLayout) != marker {
		t.Errorf(
			"origin = %s, want %s — 재Open 이 값을 바꿨다",
			got.Format(originDateLayout),
			marker,
		)
	}
}

// TestOperationOrigin_DoesNotBumpSchemaVersion — 이 기록은 구조 세대가
// 아니다 (§3.5 "schema_version 은 올리지 않는다").
func TestOperationOrigin_DoesNotBumpSchemaVersion(t *testing.T) {
	ctx := context.Background()

	db := newTestDB(t)

	got, err := db.SchemaMeta(ctx, "schema_version")
	if err != nil {
		t.Fatalf("SchemaMeta(schema_version) 실패: %v", err)
	}

	if got != schemaVersion {
		t.Errorf(
			"schema_version = %q, want %q",
			got,
			schemaVersion,
		)
	}
}

// TestOperationOrigin_CorruptValue — 기록값이 날짜 형식이 아니면 조용한
// 기본값 없이 오류다. 잘못된 하한으로 자동 창을 여는 것보다 시작 중단이 낫다.
func TestOperationOrigin_CorruptValue(t *testing.T) {
	ctx := context.Background()

	db := newTestDB(t)

	if _, err := db.conn.ExecContext(
		ctx,
		`INSERT INTO schema_meta (key, value, updated_at)
		 VALUES (?, '2026/09/21', 0);`,
		operationOriginKey,
	); err != nil {
		t.Fatalf("값 설정 실패: %v", err)
	}

	if _, err := db.OperationOrigin(ctx, time.Now()); err == nil {
		t.Error("형식이 깨진 origin 인데 오류가 없다")
	}
}

func TestOperationOrigin_OpenRejectsCorruptValue(t *testing.T) {
	for _, value := range []string{"", "2026/09/21", "2026-02-30", "2026-09-1", "0000-01-01", "1970-01-01"} {
		t.Run(value, func(t *testing.T) {
			ctx := context.Background()
			path, db := newTestDBAt(t)
			_, err := db.conn.ExecContext(ctx,
				`INSERT INTO schema_meta (key, value, updated_at) VALUES (?, ?, 0)`,
				operationOriginKey, value)
			if err != nil {
				t.Fatal(err)
			}
			closeDB(t, db)
			reopened, err := Open(ctx, path)
			if reopened != nil {
				closeDB(t, reopened)
			}
			if err == nil || !strings.Contains(err.Error(), operationOriginKey) {
				t.Fatalf("Open error = %v, want corrupt origin rejection", err)
			}
		})
	}
}

// 과거 관측 파일이라도 처음 발견한 날짜를 운영 시작일로 사용한다.
func TestOperationOrigin_FirstInstallUsesDiscoveryNotObservation(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	firstLive := time.Date(2026, 9, 21, 1, 0, 0, 0, time.UTC)
	insertSeen(t, db, "DBON00KOR_R_20262340000_01D_MO.rnx.gz", firstLive)
	old := firstLive.AddDate(0, 0, -30)
	if _, err := db.conn.ExecContext(ctx,
		`UPDATE common_ledger SET mtime = ?, ingress_verified_at = ?`, old.Unix(), old.Unix()); err != nil {
		t.Fatal(err)
	}
	got, err := db.OperationOrigin(ctx, firstLive)
	if err != nil || !got.Equal(utcDay(firstLive)) {
		t.Fatalf("origin = %v, err = %v; want first discovery day", got, err)
	}
	// §3.3의 자동 To(today-ScanDays)보다 origin이 뒤에 있어 첫 창은 비어야 한다.
	// 실제 창 함수 및 자동 실행 연결은 커밋 5·7에서 별도로 검증한다.
	if !got.After(utcDay(firstLive).AddDate(0, 0, -7)) {
		t.Fatal("first installation could open the old automatic window")
	}
	if hasOriginKey(t, db) {
		t.Fatal("read-only origin lookup wrote metadata")
	}
}

// TestOperationOrigin_FirstSeedDoesNotReplayThirtyObservationDays
// 는 설치 당일 seed/스캔이 관측일 30일치 파일을 UpsertCommon 으로
// 올려도 origin 이 mtime·ingress 가 아니라 실제 발견 시각(now)의
// 날짜임을 고정한다. insertSeen 은 first_seen 을 덮어쓰므로 이
// 경로를 증명하지 못한다 — 운영 코드가 first_seen 에 mtime 을
// 넣으면 그 테스트는 통과하고 첫 정시만 30일을 신규로 보낸다.
func TestOperationOrigin_FirstSeedDoesNotReplayThirtyObservationDays(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	now := time.Now().UTC()
	oldestObs := now.AddDate(0, 0, -29)

	for d := 0; d < 30; d++ {
		obs := now.AddDate(0, 0, -d)
		name := fmt.Sprintf("DBON00KOR_R_2026%03d0000_01D_MO.rnx.gz", 264-d)
		in := CommonInput{
			FileName:          domain.NormalizeName(name),
			BaseName:          domain.BaseName(name),
			Category:          domain.CategoryRINEX3Daily,
			Size:              1024,
			MTime:             obs.Unix(),
			Origin:            domain.OriginLocal,
			IngressVerifiedAt: obs.Unix(),
		}
		if _, err := db.UpsertCommon(ctx, in); err != nil {
			t.Fatalf("UpsertCommon(%s) 실패: %v", name, err)
		}
	}

	got, err := db.OperationOrigin(ctx, now)
	if err != nil {
		t.Fatal(err)
	}

	if got.Equal(utcDay(oldestObs)) {
		t.Fatalf("origin = %s — MIN(mtime) 를 쓴 것과 같다; 첫 자동 창이 30일을 연다",
			got.Format(originDateLayout))
	}

	want := utcDay(now)
	if !got.Equal(want) && !got.Equal(want.AddDate(0, 0, -1)) {
		t.Fatalf("origin = %s, want discovery day %s (자정 넘김 허용 1일)",
			got.Format(originDateLayout), want.Format(originDateLayout))
	}

	to := utcDay(now).AddDate(0, 0, -7)
	if !got.After(to) {
		t.Fatalf("origin %s is not after To=%s — first auto resend would scan history",
			got.Format(originDateLayout), to.Format(originDateLayout))
	}
}

func TestOperationOrigin_RejectsUnixEpochFirstSeen(t *testing.T) {
	ctx := context.Background()
	path, db := newTestDBAt(t)
	insertSeen(t, db, "DBON00KOR_R_20262640000_01D_MO.rnx.gz",
		time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	if _, err := db.conn.ExecContext(ctx,
		`UPDATE common_ledger SET first_seen = 1`); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)

	reopened, err := Open(ctx, path)
	if reopened != nil {
		closeDB(t, reopened)
	}
	if err == nil {
		t.Fatal("first_seen=1 (1970-01-01) 을 origin 으로 받으면 Retention 창이 열린다")
	}
}

func TestOperationOrigin_RejectsZeroFirstSeen(t *testing.T) {
	ctx := context.Background()
	path, db := newTestDBAt(t)
	insertSeen(t, db, "DBON00KOR_R_20262640000_01D_MO.rnx.gz",
		time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC))
	if _, err := db.conn.ExecContext(ctx,
		`UPDATE common_ledger SET first_seen = 0`); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)

	reopened, err := Open(ctx, path)
	if reopened != nil {
		closeDB(t, reopened)
	}
	if err == nil || !strings.Contains(err.Error(), "first_seen") {
		t.Fatalf("Open error = %v, want invalid min first_seen", err)
	}
}

func TestOperationOrigin_PreservedAfterAllRowsRemoved(t *testing.T) {
	ctx := context.Background()
	path, db := newTestDBAt(t)
	seen := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	insertSeen(t, db, "DBON00KOR_R_20262640000_01D_MO.rnx.gz", seen)
	if err := db.ensureOperationOrigin(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.ExecContext(ctx, `DELETE FROM common_ledger`); err != nil {
		t.Fatal(err)
	}
	closeDB(t, db)
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB(t, db)
	got, err := db.OperationOrigin(ctx, seen.AddDate(0, 0, 60))
	if err != nil || !got.Equal(utcDay(seen)) {
		t.Fatalf("origin = %v, err = %v; expected preserved start date", got, err)
	}
}

func TestOperationOrigin_BackfillThroughMigrations(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		create func(*testing.T) string
	}{{"v4", newV4Fixture}, {"v5", newV5Fixture}} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := Open(ctx, fixture.create(t))
			if err != nil {
				t.Fatal(err)
			}
			defer closeDB(t, db)
			got, err := db.OperationOrigin(ctx, time.Now())
			if err != nil || !got.Equal(utcDay(time.Unix(1700000000, 0))) {
				t.Fatalf("origin = %v, err = %v; want historical first_seen", got, err)
			}
			version, err := db.SchemaMeta(ctx, "schema_version")
			if err != nil || version != schemaVersion {
				t.Fatalf("schema version = %q, err = %v", version, err)
			}
		})
	}
}

// newTestDBAt 은 newTestDB 와 같되 재Open 을 위해 경로를 함께 돌려주고,
// Close 를 호출자에게 맡긴다. newTestDB 는 경로를 감추고 Cleanup 에서
// Close 를 걸어두므로 "닫고 다시 여는" 테스트에 쓸 수 없다.
func newTestDBAt(t *testing.T) (string, *DB) {
	t.Helper()

	path := filepath.Join(
		t.TempDir(),
		"rinex_ledger.db",
	)

	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() 실패: %v", err)
	}

	return path, db
}

// closeDB 는 defer 자리에서 Close 오류를 테스트 실패로 승격한다.
func closeDB(t *testing.T, db *DB) {
	t.Helper()

	if err := db.Close(); err != nil {
		t.Errorf("Close() 실패: %v", err)
	}
}
