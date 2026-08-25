// Package ledger 는 Ledger DB 의 연결과 기록을 담당한다.
//
// 장부는 판정하지 않는다. 파일이 정상인가에 대한 판정은 verify 패키지의
// 책임이고, 이 패키지는 그 결과를 기록만 한다. (CONCEPT 2.1)
//
// 다만 같은 file_name 이 다른 size 또는 mtime 으로 재관측되었을 때
// revision 을 올리는 규칙은 저장된 이전 행과의 비교가 필요하고
// 한 트랜잭션 안에서 처리되어야 하므로 ledger 가 책임진다.
package ledger

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"SFTPClient/internal/domain"
)

//go:embed schema.sql
var schemaSQL string

const driverName = "sqlite"

var (
	ErrIdentityRuleMismatch = errors.New("ledger: identity rule mismatch")
	ErrPragmaNotApplied     = errors.New("ledger: pragma not applied")
	ErrSchemaMetaMissing    = errors.New("ledger: schema_meta key missing")
)

// DB 는 Ledger DB 연결을 감싼다.
type DB struct {
	conn *sql.DB
}

// Open 은 Ledger DB 를 열고 사용 가능한 상태까지 준비한다.
//
//	sql.Open
//	→ SetMaxOpenConns(1)
//	→ Ping
//	→ PRAGMA 실제 값 확인
//	→ schema.sql 실행
//	→ identity_rule 검사
func Open(ctx context.Context, path string) (*DB, error) {
	dataSourceName, err := dsn(path)
	if err != nil {
		return nil, fmt.Errorf("ledger: build dsn %q: %w", path, err)
	}

	sqlDB, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("ledger: open %q: %w", path, err)
	}

	// MVP 1 은 단일 Writer 전제이다.
	//
	// PRAGMA 중 일부는 connection 단위이므로,
	// 하나의 DB handle 이 여러 physical connection 을 열지 않도록 제한한다.
	// 향후 Worker Pool 을 도입하더라도 쓰기는 단일 Ledger Writer 로 직렬화한다.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db := &DB{conn: sqlDB}

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ledger: ping %q: %w", path, err)
	}

	// PRAGMA 확인을 schema.sql 실행보다 먼저 한다.
	// 설정이 적용되지 않았다면 DDL 을 돌리기 전에 멈추는 편이
	// 원인을 찾기 쉽다. FK 선언 자체는 PRAGMA 값과 무관하게 저장되므로
	// 순서가 스키마의 정합성을 좌우하지는 않는다.
	if err := db.verifyPragmas(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	if _, err := sqlDB.ExecContext(ctx, schemaSQL); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ledger: apply schema: %w", err)
	}

	if err := db.verifyIdentityRule(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return db, nil
}

// Close 는 DB 연결을 닫는다.
func (db *DB) Close() error {
	return db.conn.Close()
}

// dsn 은 modernc.org/sqlite 에 전달할 DSN 을 만든다.
//
// 경로와 query string 을 문자열로 직접 이어 붙이지 않고 file URI 로 구성한다.
// 따라서 공백, &, # 같은 문자가 경로에 포함되어도 query parameter 와 섞이지 않는다.
//
// PRAGMA 는 modernc.org/sqlite 의 shorthand DSN 옵션을 사용한다.
// PRAGMA 를 Exec 로 한 번 실행하는 방식은 커넥션 풀이 새 connection 을
// 만들 때 누락되므로 DSN 에 지정한다.
func dsn(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty database path")
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("absolute path: %w", err)
	}

	slashPath := filepath.ToSlash(absPath)

	// Windows drive path:
	//
	//	C:/sqlite/rinex_ledger.db
	//
	// 를 file:///C:/sqlite/rinex_ledger.db 형태로 만든다.
	if filepath.VolumeName(absPath) != "" &&
		!strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}

	q := url.Values{}
	q.Set("_foreign_keys", "1")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "NORMAL")
	q.Set("_busy_timeout", "5000")

	u := url.URL{
		Scheme:   "file",
		Path:     slashPath,
		RawQuery: q.Encode(),
	}

	return u.String(), nil
}

// requiredPragmas 는 DSN 지정이 실제로 적용되었는지 확인할 항목이다.
//
// SQLite 의 PRAGMA 는 반환 타입이 항목마다 다르다.
//
//	foreign_keys   INTEGER  1
//	journal_mode   TEXT     "wal"
//	synchronous    INTEGER  1   (NORMAL)
//	busy_timeout   INTEGER  5000
//
// 전부 string 으로 Scan 하면 database/sql 의 암묵적 변환에 의존하게 되고,
// 드라이버가 돌려주는 driver.Value 타입에 따라 실패할 수 있다.
// any 로 받아 문자열로 정규화한 뒤 비교한다.
var requiredPragmas = []struct {
	name string
	want string
}{
	{"foreign_keys", "1"},
	{"journal_mode", "wal"},
	{"synchronous", "1"}, // NORMAL 은 정수 1 로 보고된다
	{"busy_timeout", "5000"},
}

// verifyPragmas 는 DSN 에 지정한 PRAGMA 가 실제 connection 에
// 적용되었는지 다시 읽어 확인한다.
//
// DSN 파라미터의 오타는 조용히 무시되므로, 되읽지 않으면
// foreign_keys 가 꺼진 채로 운영에 들어가 고아 행이 쌓인다.
func (db *DB) verifyPragmas(ctx context.Context) error {
	for _, p := range requiredPragmas {
		var raw any

		row := db.conn.QueryRowContext(ctx, "PRAGMA "+p.name+";")

		if err := row.Scan(&raw); err != nil {
			return fmt.Errorf(
				"ledger: read pragma %s: %w",
				p.name,
				err,
			)
		}

		got := pragmaValueToString(raw)

		if !strings.EqualFold(got, p.want) {
			return fmt.Errorf(
				"%w: %s = %q, want %q",
				ErrPragmaNotApplied,
				p.name,
				got,
				p.want,
			)
		}
	}

	return nil
}

// pragmaValueToString 은 PRAGMA 가 돌려준 값을 비교 가능한 문자열로 만든다.
//
// 드라이버는 INTEGER 를 int64 로, TEXT 를 string 또는 []byte 로 돌려준다.
// 어느 쪽이든 같은 방식으로 비교할 수 있도록 정규화한다.
func pragmaValueToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(t)
	}
}

// verifyIdentityRule 은 DB 에 기록된 identity_rule 과
// 현재 실행파일의 domain.IdentityRule 이 같은지 확인한다.
//
// 폐쇄망에서는 실행파일과 DB 파일이 서로 다른 시점에 갱신될 수 있다.
// 정규화 규칙이 바뀐 실행파일이 기존 Ledger 를 열면 같은 파일이 다른
// file_name 으로 등록되어 누적 이력이 무효가 되고 전량 재전송이 발생한다.
// 이 검사가 그것을 막는 유일한 지점이므로 호출자는 복구를 시도하지 않고
// 즉시 종료해야 한다. (CONCEPT 4.1, 7)
func (db *DB) verifyIdentityRule(ctx context.Context) error {
	got, err := db.SchemaMeta(ctx, "identity_rule")
	if err != nil {
		return err
	}

	if got != domain.IdentityRule {
		return fmt.Errorf(
			"%w: db has %q, binary expects %q",
			ErrIdentityRuleMismatch,
			got,
			domain.IdentityRule,
		)
	}

	return nil
}

// SchemaMeta 는 schema_meta 의 값을 읽는다.
func (db *DB) SchemaMeta(
	ctx context.Context,
	key string,
) (string, error) {
	var value string

	row := db.conn.QueryRowContext(
		ctx,
		`SELECT value
		   FROM schema_meta
		  WHERE key = ?;`,
		key,
	)

	switch err := row.Scan(&value); {
	case errors.Is(err, sql.ErrNoRows):
		return "", fmt.Errorf(
			"%w: %s",
			ErrSchemaMetaMissing,
			key,
		)

	case err != nil:
		return "", fmt.Errorf(
			"ledger: read schema_meta %q: %w",
			key,
			err,
		)
	}

	return value, nil
}

// SQLiteVersion 은 이 프로그램이 실제로 사용 중인 SQLite 엔진 버전을 반환한다.
//
// 시스템에 설치된 sqlite3.exe 버전이 아니라
// modernc.org/sqlite 에 포함되어 실제 실행 중인 엔진 버전이다.
func (db *DB) SQLiteVersion(ctx context.Context) (string, error) {
	var version string

	if err := db.conn.QueryRowContext(
		ctx,
		`SELECT sqlite_version();`,
	).Scan(&version); err != nil {
		return "", fmt.Errorf(
			"ledger: read sqlite version: %w",
			err,
		)
	}

	return version, nil
}
