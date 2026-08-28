// Package ledger 는 Ledger DB 의 연결과 기록을 담당한다.
//
// 장부는 판정하지 않는다. 파일이 정상인가에 대한 판정은 verify 패키지의
// 책임이고, 이 패키지는 그 결과를 기록만 한다. (CONCEPT 2.1)
//
// 다만 같은 file_name 이 다른 size 또는 mtime 으로 재관측되었을 때
// revision 을 올리는 규칙은 저장된 이전 행과의 비교가 필요하고
// 원자적으로 처리되어야 하므로 ledger 가 책임진다.
package ledger

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"

	"SFTPClient/internal/domain"
)

//go:embed schema.sql
var schemaSQL string

const (
	driverName = "sqlite"

	// schemaVersion 은 이 실행파일이 기대하는 DB 구조 세대이다.
	//
	// schema.sql 파일명의 v 번호(설계 개정 이력)와는 다른 것을 센다.
	// 이 값은 실제 DB 파일의 구조 호환성이 깨질 때만 올린다.
	//
	// schema v5 에서 common_ledger.local_path 를 삭제하면서
	// 배포 DB 구조 세대를 1 → 2 로 올렸다.
	//
	// domain 이 아니라 ledger 에 두는 이유는 이 값이
	// 파일 식별 규칙이 아니라 DB 스키마의 성질이기 때문이다.
	// 파일 식별 규칙은 domain.IdentityRule 이 소유한다.
	//
	// ★ 이 값은 schema.sql 에도 같은 리터럴로 들어 있다.
	//
	//	INSERT OR IGNORE INTO schema_meta ... ('schema_version', '2', ...)
	//
	// 한쪽만 올리면 새로 만든 DB 가 곧바로 열리지 않는다.
	// 스크립트가 넣은 값과 실행파일이 기대하는 값이 달라
	// verifySchemaVersion 이 첫 Open 에서 실패하기 때문이다.
	// 반드시 두 곳을 함께 올린다.
	// db_test.go 의 TestSchemaVersionMatchesSchemaSQL 이 이를 고정한다.
	schemaVersion = "2"
)

var (
	ErrIdentityRuleMismatch  = errors.New("ledger: identity rule mismatch")
	ErrSchemaVersionMismatch = errors.New("ledger: schema version mismatch")
	ErrPragmaNotApplied      = errors.New("ledger: pragma not applied")
	ErrSchemaMetaMissing     = errors.New("ledger: schema_meta key missing")
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
//	→ schema_version 검사
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

	// Ledger DB 는 하나의 physical connection 만 사용한다.
	//
	// PRAGMA 중 일부가 connection 단위 설정이고,
	// SQLite 쓰기를 하나의 connection 으로 직렬화하기 위해
	// MaxOpenConns / MaxIdleConns 를 1로 제한한다.
	//
	// MVP 1 의 전송 Worker 는 기본 4개지만 Ledger 접근은
	// database/sql 을 통해 직렬화된다.
	//
	// 별도의 Ledger Writer goroutine + batch commit 은
	// 정합성 요건이 아니라 처리량 최적화이며,
	// 실제 병목이 확인될 경우 MVP 4 에서 검토한다.
	//
	// ★ 교착 주의 — 트랜잭션을 도입할 때 반드시 지킬 것
	//
	// 커넥션이 하나뿐이므로, db.Begin 으로 연 트랜잭션이 살아 있는 동안
	// 같은 *DB 로 QueryContext / ExecContext 를 호출하면 영원히 대기한다.
	// 트랜잭션이 유일한 커넥션을 쥐고 있고, 그 호출은 커넥션이 반납되기를
	// 기다리기 때문이다. busy_timeout 은 SQLite 잠금 대기 설정이라
	// 이 상황에는 관여하지 않으며, context 취소 외에는 풀리지 않는다.
	//
	// 트랜잭션 안에서는 반드시 tx.QueryContext / tx.ExecContext 를 쓴다.
	// put_ledger 의 PENDING → IN_PROGRESS 전이처럼 두 테이블을 한
	// 트랜잭션에서 갱신하는 지점(MVP 1 후반)이 이 규칙의 첫 적용 대상이다.
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	db := &DB{conn: sqlDB}

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ledger: ping %q: %w", path, err)
	}

	// PRAGMA 확인을 schema.sql 실행보다 먼저 한다.
	//
	// 설정이 적용되지 않았다면 DDL 을 실행하기 전에 멈추는 편이
	// 원인을 찾기 쉽다.
	// FK 선언 자체는 PRAGMA 값과 무관하게 저장되므로
	// 이 순서가 스키마 정의 자체의 정합성을 좌우하지는 않는다.
	if err := db.verifyPragmas(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	// schema.sql 은 CREATE TABLE / CREATE INDEX 가 IF NOT EXISTS 이고
	// schema_meta 의 INSERT 는 OR IGNORE 이므로 반복 실행이 안전하다.
	//
	// 구조가 다른 옛 DB 를 열면 여기서 실패한다.
	// 예를 들어 category 컬럼이 없던 세대의 DB 에서는
	// idx_common_category_origin 생성이 "no such column" 으로 멈춘다.
	// 그 결과 schema_meta 도 만들어지지 않으므로,
	// 구버전 DB 가 최신 schema_version 으로 위장되는 경로는 없다.
	if _, err := sqlDB.ExecContext(ctx, schemaSQL); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ledger: apply schema: %w", err)
	}

	// DB 구조 세대를 먼저 확인하고,
	// 그 구조 안에서 사용하는 파일 식별 규칙을 다음으로 확인한다.
	//
	// 위 실행으로 기존 DB 가 현재 구조로 변환되지는 않는다.
	// v4 DB 는 모든 문장이 no-op 으로 통과한 뒤
	// schema_version 이 '1' 로 남아 여기서 잡힌다.
	//
	// 실행파일과 DB 의 구조 세대가 다르면 즉시 중단한다.
	// 암묵적 migration 이나 자동 보정을 시도하지 않는다.
	if err := db.verifySchemaVersion(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
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
// 만들 때 누락될 수 있으므로 DSN 에 지정한다.
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
// DSN 파라미터의 오타는 조용히 무시될 수 있으므로,
// 되읽지 않으면 foreign_keys 가 꺼진 채 운영되어
// FK 제약이 실제로 동작하지 않을 수 있다.
func (db *DB) verifyPragmas(ctx context.Context) error {
	for _, p := range requiredPragmas {
		var raw any

		row := db.conn.QueryRowContext(
			ctx,
			"PRAGMA "+p.name+";",
		)

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
// 드라이버는 INTEGER 를 int64 로, TEXT 를 string 또는 []byte 로 돌려줄 수 있다.
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

// verifySchemaVersion 은 DB 의 구조 세대가 실행파일이 기대하는 값과
// 같은지 확인한다.
//
//	DB < 실행파일   현재 실행파일이 기대하는 migration 이 적용되지 않은 상태
//	DB > 실행파일   현재 실행파일보다 새로운 DB 를 연 상태
//
// 어느 방향이든 자동 보정하지 않고 즉시 중단한다.
//
// 구버전 실행파일과 신버전 DB 또는 신버전 실행파일과 구버전 DB를
// 섞어 사용하면 SQL 오류 또는 구조에 대한 잘못된 가정으로 이어질 수 있다.
// 폐쇄망에서 실행파일과 DB 가 서로 다른 시점에 배포되는 경우를 방어한다.
func (db *DB) verifySchemaVersion(ctx context.Context) error {
	got, err := db.SchemaMeta(ctx, "schema_version")
	if err != nil {
		return err
	}

	if got == schemaVersion {
		return nil
	}

	return fmt.Errorf(
		"%w: db has %q, binary expects %q (%s)",
		ErrSchemaVersionMismatch,
		got,
		schemaVersion,
		schemaVersionHint(got, schemaVersion),
	)
}

// schemaVersionHint 는 불일치 방향에 따라
// 운영자가 확인할 내용을 한 줄로 알려준다.
//
// 판정 자체에는 영향을 주지 않는다.
// schema_version 이 다르면 어느 방향이든 Open 은 실패한다.
func schemaVersionHint(got, want string) string {
	gotN, gotErr := strconv.Atoi(got)
	wantN, wantErr := strconv.Atoi(want)

	switch {
	case gotErr != nil || wantErr != nil:
		return "version value is not numeric"

	case gotN < wantN:
		return "database is older than the binary; recreate the development DB or apply a verified migration"

	default:
		return "binary is older than the database; check the deployed executable"
	}
}

// verifyIdentityRule 은 DB 에 기록된 identity_rule 과
// 현재 실행파일의 domain.IdentityRule 이 같은지 확인한다.
//
// 정규화 규칙이 바뀐 실행파일이 기존 Ledger 를 열면
// 같은 물리 파일을 다른 file_name 으로 인식할 수 있고,
// 누적 이력과 중복 방지 규칙이 무효가 될 수 있다.
//
// 이 검사는 경고가 아니라 시작 중단 조건이다.
// 호출자는 여기서 자동 복구나 규칙 변경을 시도하지 않는다.
// (CONCEPT 4.1, 7)
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
