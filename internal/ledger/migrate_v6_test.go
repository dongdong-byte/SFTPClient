package ledger

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"SFTPClient/internal/domain"
)

// v5CommonLedgerDDL 은 v5→v6 마이그레이션 이전 세대의 정의다.
//
// v4 픽스처(migrate_test.go)와 같은 원칙: 제약은 전환이 관여하는
// 최소한만 재현한다 — 전환 대상은 content_hash 컬럼의 존재 여부다.
const v5CommonLedgerDDL = `
CREATE TABLE common_ledger (
    file_name   TEXT    NOT NULL CHECK (file_name = lower(file_name)),
    base_name   TEXT    NOT NULL,
    category    TEXT    NOT NULL
            CHECK (category IN ('RINEX2_DAILY',  'RINEX2_HOURLY',
                                'RINEX3_DAILY',  'RINEX3_HOURLY',
                                'RINEX4_DAILY',  'RINEX4_HOURLY')),
    size        INTEGER NOT NULL CHECK (size >= 0),
    mtime       INTEGER NOT NULL,
    origin      TEXT    NOT NULL CHECK (origin IN ('LOCAL', 'DOWNLOAD')),
    revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    state       TEXT    NOT NULL CHECK (state IN ('READY', 'CHANGED')),
    first_seen  INTEGER NOT NULL,
    ingress_verified_at INTEGER NOT NULL,
    set_key     TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (category, file_name)
);

CREATE TABLE schema_meta (
    key         TEXT    NOT NULL PRIMARY KEY,
    value       TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL
);

INSERT INTO schema_meta (key, value, updated_at) VALUES
    ('schema_version', '5',           strftime('%s', 'now')),
    ('identity_rule',  'FILENAME_V1', strftime('%s', 'now')),
    ('mvp_stage',      'MVP1_PUT',    strftime('%s', 'now'));
`

// newV5Fixture 는 운영 3기관과 같은 세대(v5)의 DB 파일을 만든다.
func newV5Fixture(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "v5_ledger.db")

	dataSourceName, err := dsn(path)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	if _, err := raw.Exec(v5CommonLedgerDDL); err != nil {
		t.Fatalf("v5 픽스처 생성 실패: %v", err)
	}

	if _, err := raw.Exec(
		`INSERT INTO common_ledger
		   (file_name, base_name, category, size, mtime, origin,
		    revision, state, first_seen, ingress_verified_at,
		    set_key, kind)
		 VALUES ('dbon2500.26o.gz', 'dbon2500.26o', 'RINEX2_DAILY',
		         100, 1700000000, 'LOCAL',
		         3, 'CHANGED', 1700000000, 1700000000,
		         'dbon2500.26', 'o');`,
	); err != nil {
		t.Fatalf("v5 표본 행 삽입 실패: %v", err)
	}

	return path
}

// TestMigrateV5ToV6 는 운영 DB 와 같은 v5 가 Open 만으로 v6 이 되고
// 기존 행의 데이터가 보존되며 content_hash 가 ”(지문 없음)임을
// 고정한다. 이번 배포의 필수 전환 경로다. (UNIT2 설계 v3 §2.3)
//
// 이 테스트는 modernc.org/sqlite 실물 위에서 ALTER + CHECK 마이그레이션
// 경로를 돌므로, 드라이버의 해당 기능 검증을 겸한다 (§2.1).
func TestMigrateV5ToV6(t *testing.T) {
	ctx := context.Background()
	path := newV5Fixture(t)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("v5 DB Open 실패 — v5→v6 자동 마이그레이션이 안 됨: %v", err)
	}
	defer db.Close()

	got, err := db.SchemaMeta(ctx, "schema_version")
	if err != nil {
		t.Fatal(err)
	}

	if got != "6" {
		t.Fatalf("schema_version = %q, want \"6\"", got)
	}

	// 데이터 보존: v5 시절의 사실이 그대로다.
	var (
		revision int64
		state    string
		setKey   string
		hash     string
	)
	if err := db.conn.QueryRowContext(
		ctx,
		`SELECT revision, state, set_key, content_hash
		   FROM common_ledger WHERE file_name = 'dbon2500.26o.gz';`,
	).Scan(&revision, &state, &setKey, &hash); err != nil {
		t.Fatal(err)
	}

	if revision != 3 || state != "CHANGED" || setKey != "dbon2500.26" {
		t.Errorf("v5 데이터 훼손: rev=%d state=%s set_key=%s",
			revision, state, setKey)
	}

	if hash != "" {
		t.Errorf("기존 행 content_hash = %q, want '' (백필은 put 몫)", hash)
	}
}

// TestMigrateV5ToV6_Idempotent 는 전환된 DB 재Open 이 무해함을 고정한다.
func TestMigrateV5ToV6_Idempotent(t *testing.T) {
	ctx := context.Background()
	path := newV5Fixture(t)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("전환된 DB 재Open 실패: %v", err)
	}
	defer db2.Close()

	if got, _ := db2.SchemaMeta(ctx, "schema_version"); got != "6" {
		t.Errorf("재Open 후 schema_version = %q", got)
	}
}

// TestContentHashCheck_MigratedPath 는 마이그레이션으로 추가된 CHECK 가
// 실제로 잘못된 지문을 차단함을 고정한다 (H17). 63자·대문자·비hex 는
// 거부되고 소문자 64자 hex 는 통과한다.
func TestContentHashCheck_MigratedPath(t *testing.T) {
	ctx := context.Background()
	path := newV5Fixture(t)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ok64 := "0123456789abcdef0123456789abcdef" +
		"0123456789abcdef0123456789abcdef"

	bad := map[string]string{
		"63자":  ok64[:63],
		"대문자":  "A" + ok64[1:],
		"비hex": "z" + ok64[1:],
	}

	for label, v := range bad {
		if _, err := db.conn.ExecContext(
			ctx,
			`UPDATE common_ledger SET content_hash = ?
			  WHERE file_name = 'dbon2500.26o.gz';`,
			v,
		); err == nil {
			t.Errorf("%s 지문이 CHECK 를 통과했다", label)
		}
	}

	if _, err := db.conn.ExecContext(
		ctx,
		`UPDATE common_ledger SET content_hash = ?
		  WHERE file_name = 'dbon2500.26o.gz';`,
		ok64,
	); err != nil {
		t.Errorf("정상 64자 hex 지문이 거부됨: %v", err)
	}
}

// TestUpsertCommon_ContentHashRoundTrip 은 Upsert 가 지문을 저장하고
// LookupCommon 이 그대로 돌려줌을 고정한다. 변경(Upsert 재호출) 시
// 지문이 새 값으로 갈리는 것, 해시 실패 표기(”)로 덮이는 것 포함.
func TestUpsertCommon_ContentHashRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := Open(
		ctx,
		filepath.Join(t.TempDir(), "roundtrip.db"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const name = "dbon2500.26o.gz"
	hashA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	hashB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" +
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	in := CommonInput{
		FileName:          name,
		BaseName:          "dbon2500.26o",
		Category:          domain.CategoryRINEX2Daily,
		Size:              100,
		MTime:             1700000000,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: 1700000000,
		ContentHash:       hashA,
	}

	if _, err := db.UpsertCommon(ctx, in); err != nil {
		t.Fatal(err)
	}

	known, err := db.LookupCommon(ctx, domain.CategoryRINEX2Daily, []string{name})
	if err != nil {
		t.Fatal(err)
	}

	if k := known[name]; k.ContentHash != hashA {
		t.Fatalf("최초 지문 = %q, want %q", k.ContentHash, hashA)
	}

	// 내용 변경 관측 — 새 지문으로 갈린다.
	in.Size = 200
	in.ContentHash = hashB
	if _, err := db.UpsertCommon(ctx, in); err != nil {
		t.Fatal(err)
	}

	known, _ = db.LookupCommon(ctx, domain.CategoryRINEX2Daily, []string{name})
	if k := known[name]; k.ContentHash != hashB || k.Revision != 2 {
		t.Fatalf("변경 후 (hash, rev) = (%q, %d), want (%q, 2)",
			k.ContentHash, k.Revision, hashB)
	}

	// 다음 변경에서 해시 실패('') — 옛 지문이 남지 않는다.
	in.Size = 300
	in.ContentHash = ""
	if _, err := db.UpsertCommon(ctx, in); err != nil {
		t.Fatal(err)
	}

	known, _ = db.LookupCommon(ctx, domain.CategoryRINEX2Daily, []string{name})
	if k := known[name]; k.ContentHash != "" {
		t.Fatalf("해시 실패 후 지문 = %q, want '' (옛 지문 잔존 금지)", k.ContentHash)
	}
}

// TestCommonInput_ContentHashValidate 는 저장 규약 위반 지문이
// DB 도달 전에 거부됨을 고정한다.
func TestCommonInput_ContentHashValidate(t *testing.T) {
	in := CommonInput{
		FileName:          "dbon2500.26o.gz",
		BaseName:          "dbon2500.26o",
		Category:          domain.CategoryRINEX2Daily,
		Size:              100,
		MTime:             1700000000,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: 1700000000,
		ContentHash:       "not-a-hash",
	}

	if err := in.Validate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("잘못된 지문이 Validate 를 통과: %v", err)
	}
}
