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
			category,
			file_name,
			revision,
			status,
			attempts
		)
		VALUES (?, ?, ?, ?, ?);
		`,
		string(domain.CategoryRINEX3Hourly),
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

// schema_meta 의 실행파일 계약값을 확인한다.
//
// schema_version 과 identity_rule 은 단순 정보가 아니라
// 실행파일이 기존 DB 를 열 수 있는지를 결정하는 계약이다.
func TestSchemaMeta(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	t.Run("schema_version", func(t *testing.T) {
		got, err := db.SchemaMeta(ctx, "schema_version")
		if err != nil {
			t.Fatalf(
				"SchemaMeta(schema_version) 실패: %v",
				err,
			)
		}

		if got != schemaVersion {
			t.Errorf(
				"schema_version = %q, want %q",
				got,
				schemaVersion,
			)
		}
	})

	t.Run("identity_rule", func(t *testing.T) {
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
	})

	t.Run("missing", func(t *testing.T) {
		_, err := db.SchemaMeta(ctx, "no_such_key")

		if !errors.Is(err, ErrSchemaMetaMissing) {
			t.Errorf(
				"없는 키 조회 오류 = %v, want ErrSchemaMetaMissing",
				err,
			)
		}
	})
}

// schema.sql 과 db.go 는 같은 값을 두 곳에 적어 두고 있다.
//
//	schema.sql   INSERT OR IGNORE ... ('schema_version', '4', ...)
//	db.go        const schemaVersion = "4"
//
// 한쪽만 올리면 새로 만든 DB 조차 열리지 않는다.
// schema.sql 이 넣은 값과 실행파일이 기대하는 값이 달라
// verifySchemaVersion 이 첫 Open 에서 실패하기 때문이다.
//
// 그 상황은 Open 을 거치는 다른 테스트에서도 실패로 나타나지만,
// 메시지가 "schema version mismatch" 라 DB 파일이 오래된 것으로 오인되기 쉽다.
// 여기서는 Open 을 거치지 않고 embed 된 원문을 직접 확인하여
// "두 파일이 어긋났다" 는 원인을 곧바로 알린다.
//
// identity_rule 도 같은 성질이므로 함께 고정한다.
func TestSchemaMetaLiteralsMatchConstants(t *testing.T) {
	tests := []struct {
		name  string
		probe string
	}{
		{
			name:  "schema_version",
			probe: "('schema_version', '" + schemaVersion + "'",
		},
		{
			name:  "identity_rule",
			probe: "('identity_rule',  '" + domain.IdentityRule + "'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if strings.Contains(schemaSQL, tt.probe) {
				return
			}

			t.Errorf(
				"schema.sql 에 %q 가 없다.\n"+
					"schema.sql 의 INSERT 값과 Go 상수가 어긋났다. 두 곳을 함께 고쳐야 한다.\n"+
					"(공백 등 표기만 바뀐 경우에도 여기서 걸리므로 probe 문자열을 함께 맞춘다)",
				tt.probe,
			)
		})
	}
}

// 식별자 규칙이 다른 DB 는 시작 단계에서 반드시 거부해야 한다.
//
// 식별자 규칙이 다른 상태로 실행하면 동일한 물리 파일을 다른 identity 로
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

// 구조 세대가 다른 DB 는 시작 단계에서 반드시 거부해야 한다.
//
// schema.sql 의 CREATE TABLE 은 IF NOT EXISTS 이고 schema_meta 의 INSERT 는
// OR IGNORE 이므로 구조가 다른 기존 DB 에 schema.sql 을 다시 실행해도
// 기존 테이블이 현재 구조로 자동 변경되지 않는다.
//
// 낮은 버전과 높은 버전 어느 쪽이든 실행파일과 DB 의 계약이 다르므로
// Open 단계에서 즉시 중단해야 한다.
func TestOpenRejectsSchemaVersionMismatch(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name   string
		stored string
	}{
		{
			name:   "DB가 실행파일보다 오래됨",
			stored: "1",
		},
		{
			name:   "DB가 실행파일보다 새로움",
			stored: "99",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				 WHERE key = 'schema_version';
				`,
				tt.stored,
			)
			if err != nil {
				_ = db.Close()

				t.Fatalf(
					"schema_version 변경 실패: %v",
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
					"구조 세대가 다른 DB 가 열렸다",
				)
			}

			if !errors.Is(err, ErrSchemaVersionMismatch) {
				t.Fatalf(
					"오류 = %v, want ErrSchemaVersionMismatch",
					err,
				)
			}

			t.Logf(
				"schema_version mismatch message: %v",
				err,
			)
		})
	}
}

// schemaVersionHint 는 오류 메시지용 안내일 뿐 판정에는 관여하지 않는다.
//
// 그래도 방향을 거꾸로 안내하면 현장 대응을 잘못할 수 있으므로
// 낮은 버전 / 높은 버전 / 잘못된 값 세 경우를 고정한다.
func TestSchemaVersionHint(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
		text string
	}{
		{
			name: "DB가 오래됨",
			got:  "1",
			want: "2",
			text: "database is older than the binary",
		},
		{
			name: "실행파일이 오래됨",
			got:  "3",
			want: "2",
			text: "binary is older than the database",
		},
		{
			name: "숫자가 아님",
			got:  "broken",
			want: "2",
			text: "version value is not numeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := schemaVersionHint(
				tt.got,
				tt.want,
			)

			if !strings.Contains(got, tt.text) {
				t.Errorf(
					"schemaVersionHint(%q, %q) = %q, want containing %q",
					tt.got,
					tt.want,
					got,
					tt.text,
				)
			}
		})
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
		t.Fatalf(
			"검증용 Open() 실패: %v",
			err,
		)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf(
				"검증용 Close() 실패: %v",
				err,
			)
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

// schema v5 에서 common_ledger.local_path 를 삭제했다.
//
// 경로는 Scanner 가 현재 관측한 사실이며 common_ledger 의 책임이 아니다.
// 이 테스트는 local_path 가 나중에 실수로 다시 스키마에 들어오는 것을 막는다.
//
// 이 회귀는 컴파일 오류를 내지 않는다. schema.sql 만 v4 로 되돌아가면
// UpsertCommon 이 그 컬럼을 쓰지 않을 뿐 다른 테스트는 모두 통과한다.
//
// 컬럼 수까지 고정하는 이유는, 컬럼 추가가 반드시 의식적인 결정이어야
// 하기 때문이다. 정당한 스키마 변경이라면 이 숫자와 아래 목록을 함께 고친다.
func TestCommonLedgerHasNoLocalPath(t *testing.T) {
	db := newTestDB(t)

	rows, err := db.conn.QueryContext(
		context.Background(),
		`SELECT name
		   FROM pragma_table_info('common_ledger');`,
	)
	if err != nil {
		t.Fatalf(
			"컬럼 목록 조회 실패: %v",
			err,
		)
	}

	defer func() {
		if err := rows.Close(); err != nil {
			t.Errorf(
				"rows.Close() 실패: %v",
				err,
			)
		}
	}()

	var columns []string

	for rows.Next() {
		var name string

		if err := rows.Scan(&name); err != nil {
			t.Fatalf(
				"컬럼 이름 Scan 실패: %v",
				err,
			)
		}

		if name == "local_path" {
			t.Error(
				"common_ledger 에 local_path 컬럼이 존재한다 (schema v5 에서 삭제됨)",
			)
		}

		columns = append(columns, name)
	}

	if err := rows.Err(); err != nil {
		t.Fatalf(
			"컬럼 순회 실패: %v",
			err,
		)
	}

	// schema v5 common_ledger:
	//
	//	file_name
	//	base_name
	//	category
	//	size
	//	mtime
	//	origin
	//	revision
	//	state
	//	first_seen
	//	ingress_verified_at
	if len(columns) != 10 {
		t.Errorf(
			"common_ledger 컬럼 수 = %d, want 10: %v\n"+
				"의도한 스키마 변경이라면 이 숫자와 위 주석의 목록을 함께 고친다",
			len(columns),
			columns,
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
	path := filepath.Join(
		t.TempDir(),
		"rinex_ledger.db",
	)

	got, err := dsn(path)
	if err != nil {
		t.Fatalf(
			"dsn(%q) 실패: %v",
			path,
			err,
		)
	}

	t.Logf(
		"path = %s",
		path,
	)
	t.Logf(
		"dsn  = %s",
		got,
	)

	if !strings.HasPrefix(got, "file://") {
		t.Errorf(
			"dsn 이 file URI 가 아니다: %q",
			got,
		)
	}

	for _, want := range []string{
		"_foreign_keys=1",
		"_journal_mode=WAL",
		"_synchronous=NORMAL",
		"_busy_timeout=5000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf(
				"dsn 에 %q 가 없다: %q",
				want,
				got,
			)
		}
	}
}

func TestDSNRejectsEmptyPath(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
	} {
		if _, err := dsn(in); err == nil {
			t.Errorf(
				"dsn(%q) 오류를 기대했으나 성공했다",
				in,
			)
		}
	}
}

// DSN 에 경로와 query string 을 문자열로 이어 붙이지 않고 file URI 로
// 구성하는 이유를 실제 동작으로 검증한다.
//
// 공백이 포함된 경로가 query parameter 와 섞이면 Open 이 실패한다.
func TestOpenWithPathContainingSpaces(t *testing.T) {
	ctx := context.Background()

	dir := filepath.Join(
		t.TempDir(),
		"directory with spaces",
	)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf(
			"테스트 디렉터리 생성 실패: %v",
			err,
		)
	}

	path := filepath.Join(
		dir,
		"rinex ledger.db",
	)

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
