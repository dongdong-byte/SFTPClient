package ledger

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// v4CommonLedgerDDL 은 마이그레이션 이전 세대(v4)의 common_ledger 정의다.
//
// 현행 schema.sql 은 v5 정의이므로, v4 DB 픽스처는 여기서 직접 만든다.
// 제약은 마이그레이션이 관여하는 최소한만 재현한다 — 전환 대상은
// 컬럼 존재 여부이지 제약 세부가 아니다.
const v4CommonLedgerDDL = `
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
    PRIMARY KEY (category, file_name)
);

CREATE TABLE schema_meta (
    key         TEXT    NOT NULL PRIMARY KEY,
    value       TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL
);

INSERT INTO schema_meta (key, value, updated_at) VALUES
    ('schema_version', '4',           strftime('%s', 'now')),
    ('identity_rule',  'FILENAME_V1', strftime('%s', 'now')),
    ('mvp_stage',      'MVP1_PUT',    strftime('%s', 'now'));
`

// newV4Fixture 는 실데이터 형태의 행을 담은 v4 DB 파일을 만든다.
func newV4Fixture(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "v4_ledger.db")

	dataSourceName, err := dsn(path)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	if _, err := raw.Exec(v4CommonLedgerDDL); err != nil {
		t.Fatalf("v4 픽스처 생성 실패: %v", err)
	}

	// 실파일 기반 표본:
	//   RINEX2 완전 세트 일부(g/o) + 유보 이름(yons060.20m)
	//   RINEX4 장기명 MO(레이트 있음) / MN(레이트 생략)
	rows := []struct {
		name, base, cat string
	}{
		{"dbon2500.26g.gz", "dbon2500.26g", "RINEX2_DAILY"},
		{"dbon2500.26o.gz", "dbon2500.26o", "RINEX2_DAILY"},
		{"yons060.20m", "yons060.20m", "RINEX2_DAILY"},
		{
			"dbon00kor_r_20262500000_01d_30s_mo.crx.gz",
			"dbon00kor_r_20262500000_01d_30s_mo.crx",
			"RINEX4_DAILY",
		},
		{
			"dbon00kor_r_20262500000_01d_mn.rnx.gz",
			"dbon00kor_r_20262500000_01d_mn.rnx",
			"RINEX4_DAILY",
		},
	}

	for _, r := range rows {
		if _, err := raw.Exec(
			`INSERT INTO common_ledger
			   (file_name, base_name, category, size, mtime, origin,
			    revision, state, first_seen, ingress_verified_at)
			 VALUES (?, ?, ?, 100, 1700000000, 'LOCAL',
			         1, 'READY', 1700000000, 1700000000);`,
			r.name, r.base, r.cat,
		); err != nil {
			t.Fatalf("v4 표본 행 삽입 실패 (%s): %v", r.name, err)
		}
	}

	return path
}

// TestMigrateV4ToV5 는 v4 DB 가 Open 만으로 v5 로 전환되고
// 기존 행이 domain 파서로 백필됨을 고정한다. (2026-09-10 계약)
func TestMigrateV4ToV5(t *testing.T) {
	ctx := context.Background()
	path := newV4Fixture(t)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("v4 DB Open 실패 — 자동 마이그레이션이 안 됨: %v", err)
	}
	defer db.Close()

	// 세대가 올라갔다.
	got, err := db.SchemaMeta(ctx, "schema_version")
	if err != nil {
		t.Fatal(err)
	}

	if got != "5" {
		t.Fatalf("schema_version = %q, want \"5\"", got)
	}

	// identity_rule 은 건드리지 않는다.
	if id, _ := db.SchemaMeta(ctx, "identity_rule"); id != "FILENAME_V1" {
		t.Errorf("identity_rule 이 변조됨: %q", id)
	}

	// 백필 결과 — '' 는 유보(세트 소속 미확정)다.
	want := map[string][2]string{
		"dbon2500.26g.gz": {"dbon2500.26", "g"},
		"dbon2500.26o.gz": {"dbon2500.26", "o"},
		"yons060.20m":     {"", ""},
		"dbon00kor_r_20262500000_01d_30s_mo.crx.gz": {
			"dbon00kor_r_20262500000_01d", "mo",
		},
		// 항법 파일(레이트 필드 생략)이 관측 파일과 같은 세트 키를
		// 받는 것이 백필의 핵심 성질이다.
		"dbon00kor_r_20262500000_01d_mn.rnx.gz": {
			"dbon00kor_r_20262500000_01d", "mn",
		},
	}

	for name, w := range want {
		var setKey, kind string

		if err := db.conn.QueryRowContext(
			ctx,
			`SELECT set_key, kind FROM common_ledger
			  WHERE file_name = ?;`,
			name,
		).Scan(&setKey, &kind); err != nil {
			t.Fatalf("%s 조회 실패: %v", name, err)
		}

		if setKey != w[0] || kind != w[1] {
			t.Errorf("%s: (set_key, kind) = (%q, %q), want (%q, %q)",
				name, setKey, kind, w[0], w[1])
		}
	}
}

// TestMigrateV4ToV5_Idempotent 는 전환된 DB 를 다시 열어도
// 마이그레이션이 재실행되지 않고 정상 동작함을 고정한다.
func TestMigrateV4ToV5_Idempotent(t *testing.T) {
	ctx := context.Background()
	path := newV4Fixture(t)

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// 두 번째 Open — v5 상태에서 migrateIfNeeded 는 아무것도 안 한다.
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("전환된 DB 재Open 실패: %v", err)
	}
	defer db2.Close()

	var setKey string
	if err := db2.conn.QueryRowContext(
		ctx,
		`SELECT set_key FROM common_ledger WHERE file_name = ?;`,
		"dbon2500.26g.gz",
	).Scan(&setKey); err != nil || setKey != "dbon2500.26" {
		t.Errorf("재Open 후 백필 값 유지 실패: %q, %v", setKey, err)
	}
}

// TestMigrate_UnsupportedGenerationStillRejected 는 v4→v5 밖의
// 세대(예: '3')가 종전대로 시작 중단됨을 고정한다.
// 자동 마이그레이션은 알려진 단일 스텝만 수행한다.
func TestMigrate_UnsupportedGenerationStillRejected(t *testing.T) {
	ctx := context.Background()
	path := newV4Fixture(t)

	// 픽스처의 세대를 지원 밖 값으로 낮춘다.
	dataSourceName, err := dsn(path)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open(driverName, dataSourceName)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := raw.Exec(
		`UPDATE schema_meta SET value = '3'
		  WHERE key = 'schema_version';`,
	); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	if _, err := Open(ctx, path); err == nil {
		t.Fatal("지원 밖 세대('3')가 Open 을 통과했다 — " +
			"단일 스텝 원칙 위반")
	}
}
