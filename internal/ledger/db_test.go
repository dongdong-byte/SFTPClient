package ledger

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"SFTPClient/internal/domain"
)

// newTestDB 는 임시 디렉터리에 실제 파일 기반 Ledger DB 를 만든다.
//
// 메모리 DB 를 사용하지 않는 이유는 WAL 등 파일 DB 에서만 의미가 있는
// 동작을 실제 운영 조건에 가깝게 검증하기 위해서이다.
//
// t.TempDir 은 테스트 종료 시 자동으로 정리된다.
func newTestDB(t *testing.T) *DB {
	t.Helper()

	path := filepath.Join(
		t.TempDir(),
		"rinex_ledger.db",
	)

	db, err := Open(context.Background(), path)
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

func TestOpenCreatesTables(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	want := []string{
		"common_ledger",
		"put_ledger",
		"schema_meta",
	}

	for _, table := range want {
		t.Run(table, func(t *testing.T) {
			var name string

			err := db.conn.QueryRowContext(
				ctx,
				`SELECT name
				   FROM sqlite_master
				  WHERE type = 'table'
				    AND name = ?;`,
				table,
			).Scan(&name)

			if err != nil {
				t.Fatalf(
					"테이블 %q 가 생성되지 않았다: %v",
					table,
					err,
				)
			}

			if name != table {
				t.Errorf(
					"table name = %q, want %q",
					name,
					table,
				)
			}
		})
	}
}

// PRAGMA 는 connection 단위 설정이므로 DSN 에 넣었다는 사실만으로
// 적용되었다고 간주하지 않고 실제 값을 다시 읽어 확인한다.
//
// PRAGMA 의 반환 타입은 항목마다 다르므로(journal_mode 만 TEXT)
// db.go 와 같은 방식으로 any 로 받아 정규화한 뒤 비교한다.
func TestOpenAppliesPragmas(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, p := range requiredPragmas {
		t.Run(p.name, func(t *testing.T) {
			var raw any

			err := db.conn.QueryRowContext(
				ctx,
				"PRAGMA "+p.name+";",
			).Scan(&raw)

			if err != nil {
				t.Fatalf(
					"PRAGMA %s 조회 실패: %v",
					p.name,
					err,
				)
			}

			got := pragmaValueToString(raw)

			if !strings.EqualFold(got, p.want) {
				t.Errorf(
					"PRAGMA %s = %q, want %q",
					p.name,
					got,
					p.want,
				)
			}
		})
	}
}

// foreign_keys 값이 1 인지만 보는 것으로 끝내지 않고
// 실제 FOREIGN KEY 제약이 동작하는지 확인한다.
//
// foreign_keys 가 꺼진 DB 에서는 이 삽입이 그대로 성공하므로,
// 이 테스트는 PRAGMA 누락을 실제 동작으로 잡아낸다.
func TestForeignKeyIsEnforced(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.conn.ExecContext(
		ctx,
		`
		INSERT INTO put_ledger (
			file_name,
			revision,
			status,
			attempts
		)
		VALUES (?, ?, ?, ?);
		`,
		"orphan.rnx.gz",
		1,
		string(domain.StatusPending),
		0,
	)

	if err == nil {
		t.Fatal(
			"부모 common_ledger 행이 없는 put_ledger 삽입이 허용되었다",
		)
	}
}

func TestSchemaMeta(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	got, err := db.SchemaMeta(ctx, "identity_rule")
	if err != nil {
		t.Fatalf(
			"SchemaMeta(identity_rule) 실패: %v",
			err,
		)
	}

	if got != domain.IdentityRule {
		t.Errorf(
			"identity_rule = %q, want %q",
			got,
			domain.IdentityRule,
		)
	}

	_, err = db.SchemaMeta(ctx, "no_such_key")
	if !errors.Is(err, ErrSchemaMetaMissing) {
		t.Errorf(
			"없는 키 조회 오류 = %v, want ErrSchemaMetaMissing",
			err,
		)
	}
}

// 식별자 규칙이 다른 DB 는 시작 단계에서 반드시 거부해야 한다.
//
// 식별자 규칙이 다른 상태로 실행하면 동일 파일을 다른 identity 로
// 인식할 수 있으므로 경고가 아니라 실행 중단 사유이다.
func TestOpenRejectsIdentityRuleMismatch(t *testing.T) {
	ctx := context.Background()

	path := filepath.Join(
		t.TempDir(),
		"rinex_ledger.db",
	)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf(
			"최초 Open() 실패: %v",
			err,
		)
	}

	_, err = db.conn.ExecContext(
		ctx,
		`
		UPDATE schema_meta
		   SET value = ?
		 WHERE key = 'identity_rule';
		`,
		"FILENAME_V2",
	)
	if err != nil {
		_ = db.Close()
		t.Fatalf(
			"identity_rule 변경 실패: %v",
			err,
		)
	}

	if err := db.Close(); err != nil {
		t.Fatalf(
			"Close() 실패: %v",
			err,
		)
	}

	reopened, err := Open(ctx, path)
	if err == nil {
		_ = reopened.Close()

		t.Fatal(
			"식별자 규칙이 다른 DB 가 열렸다",
		)
	}

	if !errors.Is(err, ErrIdentityRuleMismatch) {
		t.Errorf(
			"오류 = %v, want ErrIdentityRuleMismatch",
			err,
		)
	}
}

// schema.sql 은 매 프로그램 시작 시 실행되므로
// 같은 DB 에 반복 적용해도 결과가 달라지지 않아야 한다.
//
// 특히 schema_meta 의 INSERT OR IGNORE 가 행을 중복 생성하지 않는지 본다.
func TestOpenIsIdempotent(t *testing.T) {
	ctx := context.Background()

	path := filepath.Join(
		t.TempDir(),
		"rinex_ledger.db",
	)

	for i := range 3 {
		db, err := Open(ctx, path)
		if err != nil {
			t.Fatalf(
				"%d 번째 Open() 실패: %v",
				i+1,
				err,
			)
		}

		if err := db.Close(); err != nil {
			t.Fatalf(
				"%d 번째 Close() 실패: %v",
				i+1,
				err,
			)
		}
	}

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("검증용 Open() 실패: %v", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("검증용 Close() 실패: %v", err)
		}
	}()

	var count int

	err = db.conn.QueryRowContext(
		ctx,
		`
		SELECT COUNT(*)
		  FROM schema_meta;
		`,
	).Scan(&count)

	if err != nil {
		t.Fatalf(
			"schema_meta 조회 실패: %v",
			err,
		)
	}

	// schema.sql 의 INSERT OR IGNORE 는 3개 키를 넣는다.
	// 반복 실행으로 늘어나면 안 된다.
	if count != 3 {
		t.Errorf(
			"schema_meta 행 수 = %d, want 3",
			count,
		)
	}
}

// 이 테스트에서 출력되는 버전이 실제 SFTPClient 가 사용하는 SQLite 버전이다.
//
// Windows 에 별도로 설치한 sqlite3.exe 의 버전과
// modernc.org/sqlite 에 포함된 SQLite 엔진 버전은 서로 독립적이다.
//
// RETURNING 구문은 SQLite 3.35 이상이 필요하므로
// 이 값이 그보다 낮으면 UpsertCommon 이 구문 오류로 실패한다.
func TestSQLiteVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	version, err := db.SQLiteVersion(ctx)
	if err != nil {
		t.Fatalf(
			"SQLiteVersion() 실패: %v",
			err,
		)
	}

	if strings.TrimSpace(version) == "" {
		t.Fatal(
			"SQLiteVersion() 이 빈 문자열을 반환했다",
		)
	}

	t.Logf(
		"modernc.org/sqlite embedded SQLite version: %s",
		version,
	)
}

// DSN 생성 결과를 로그로 남긴다.
//
// Windows 드라이브 문자와 query parameter 가 어떻게 조합되는지는
// 실행해 보기 전에는 알 수 없으므로, 실패 시 원인을 바로 볼 수 있도록
// 문자열 자체를 출력한다.
func TestDSN(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rinex_ledger.db")

	got, err := dsn(path)
	if err != nil {
		t.Fatalf("dsn(%q) 실패: %v", path, err)
	}

	t.Logf("path = %s", path)
	t.Logf("dsn  = %s", got)

	if !strings.HasPrefix(got, "file://") {
		t.Errorf("dsn 이 file URI 가 아니다: %q", got)
	}

	for _, want := range []string{
		"_foreign_keys=1",
		"_journal_mode=WAL",
		"_synchronous=NORMAL",
		"_busy_timeout=5000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("dsn 에 %q 가 없다: %q", want, got)
		}
	}
}

func TestDSNRejectsEmptyPath(t *testing.T) {
	for _, in := range []string{"", "   "} {
		if _, err := dsn(in); err == nil {
			t.Errorf("dsn(%q) 오류를 기대했으나 성공했다", in)
		}
	}
}

// DSN 에 경로와 query string 을 문자열로 이어 붙이지 않고 file URI 로
// 구성하는 이유를 실제 동작으로 검증한다.
//
// 공백이 포함된 경로가 query parameter 와 섞이면 Open 이 실패한다.
func TestOpenWithPathContainingSpaces(t *testing.T) {
	ctx := context.Background()

	dir := filepath.Join(t.TempDir(), "directory with spaces")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("테스트 디렉터리 생성 실패: %v", err)
	}

	path := filepath.Join(dir, "rinex ledger.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf(
			"공백 포함 경로 Open() 실패: %v",
			err,
		)
	}

	if err := db.Close(); err != nil {
		t.Fatalf(
			"Close() 실패: %v",
			err,
		)
	}
}
