package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"
)

// 운영 시작일 (resend 설계 v4 §3.5)
//
// 이 장부가 처음 운영을 시작한 UTC 날짜를 schema_meta 에 키 하나로
// 기록한다. 자동 resend 창의 하한이 이 날짜다 — 운영 시작 전의 날짜는
// 한 번도 스캔된 적이 없고 기존 송신 수단이 이미 보냈을 수 있으므로,
// 자동 창이 그 구간을 훑으면 전부 신규로 판정되어 대량 재전송된다.
//
// 규칙 (§3.5 커밋 4 구현 보완):
//   - 새 DB·기존 DB: 값이 없고 행이 있으면 MIN(common_ledger.first_seen)의
//     UTC 날짜로 한 번 채운다.
//   - 행이 하나도 없으면 기록하지 않고 조회 시 회차의 오늘 날짜를 반환한다.
//   - 한 번 기록하면 바꾸지 않는다. schema_version 은 올리지 않는다.
//
// 구현 해석 — "장부를 처음 만들 때"는 DB 파일 생성이 아니라 **첫 행이
// 생길 때**다. 두 경우 모두 MIN(first_seen) 하나로 표현된다:
//
//	행 있음 → MIN(first_seen) 의 날짜를 기록한다 (새 DB 도 첫 회차 뒤
//	          다음 Open 에서 그 회차 날짜로 기록된다).
//	행 없음 → 기록하지 않는다. OperationOrigin 은 "오늘"을 계산해
//	          돌려준다 — 오늘이 하한이면 자동 창은 항상 비므로 안전하다.
//
// 빈 Open 에서 오늘을 기록하지 않는 이유: ledger.Open 은 dry-run 에서도
// 불린다(main.go). 설치 당일 --dry-run 으로 설정만 확인하고 실제 운영은
// 몇 주 뒤에 시작하면, 빈 Open 날짜가 origin 으로 굳는다. 첫 live 회차는
// ScanDays 창만 장부에 올리므로 그 사이 날짜는 행이 없고, 자동 resend 가
// 첫 정시에 그 구간 전체를 신규로 판정해 대량 재전송한다 — §3.5 가 막으려던
// 바로 그 사고다. first_seen 은 실제 스캔이 행을 만들 때만 생기므로
// dry-run·빈 DB 가 origin 을 오염시킬 수 없다.
//
// first_seen 은 관측일이 아니라 "이 장부가 그 파일을 처음 발견한 시각"
// 이므로, 그 최솟값은 곧 장부의 최초 스캔 시각이다. §3.5 가 원하는
// "운영 시작"의 실측치다.
const operationOriginKey = "operation_origin"

// originDateLayout — 저장 형식은 UTC 날짜 문자열 하나다 (예: 2026-09-21).
// §3.5 의 단위가 날짜이고, 사람이 sqlite3 로 열어봐도 읽히는 표기를 둔다.
const originDateLayout = "2006-01-02"

// ensureOperationOrigin 은 operation_origin 키가 없고 장부에 행이 있으면
// MIN(first_seen) 의 날짜로 채운다. 키가 있으면 형식만 검증하고 값을
// 바꾸지 않는다. 행이 없으면 아무것도 하지 않는다.
//
// Open 절차의 마지막(verifySchemaVersion 통과 후)에 호출한다.
// schema.sql 의 INSERT 목록에 넣지 않는 이유:
//   - 백필 값은 MIN(first_seen) 인데, schema.sql 의 정적
//     INSERT OR IGNORE 로는 "없을 때만 오늘"만 표현되어 기존 DB 가
//     오늘 날짜로 잘못 채워진다. INSERT ... SELECT 로 표현할 수는
//     있으나(기각), schema.sql 은 검증 전에 실행되므로 지원하지 않는
//     세대의 DB 에까지 값을 써 넣게 된다 — "그 외 불일치는 손대지
//     않는다"(migrateIfNeeded 주석)와 충돌한다. 검증을 모두 통과한
//     DB 에만 쓰는 지금 위치가 그 원칙과 일치한다.
//   - schema_version 을 올리지 않는 단발 백필이므로 migrateIfNeeded
//     의 스텝도 아니다.
//
// dry-run 도 이 기록을 한다. 값은 행에서 결정적으로 도출되므로 live 가
// 기록했을 값과 같고, Open 의 스키마 마이그레이션도 dry-run 에서 이미
// 쓰기를 한다 — 장부(common/put) 쓰기 금지와는 별개다.
func (db *DB) ensureOperationOrigin(ctx context.Context) error {
	value, err := db.SchemaMeta(ctx, operationOriginKey)
	if err == nil {
		_, err = parseOperationOrigin(value)
		return err
	}

	if !errors.Is(err, ErrSchemaMetaMissing) {
		return err
	}

	day, found, err := db.minFirstSeenDay(ctx)
	if err != nil {
		return err
	}

	if !found {
		// 운영 전(빈 장부). 기록하지 않는다 — 위 「빈 Open」 참조.
		return nil
	}

	value = day.Format(originDateLayout)

	// OR IGNORE — 잠금 파일이 동시 실행을 막고 있으나,
	// 읽기와 쓰기 사이의 창을 코드 규율에만 맡기지 않는다.
	// 먼저 쓴 값이 이기고, 이 실행의 값은 조용히 버려진다.
	result, err := db.conn.ExecContext(
		ctx,
		`INSERT OR IGNORE INTO schema_meta (key, value, updated_at)
		 VALUES (?, ?, strftime('%s', 'now'));`,
		operationOriginKey,
		value,
	)
	if err != nil {
		return fmt.Errorf(
			"ledger: record %s: %w",
			operationOriginKey,
			err,
		)
	}

	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ledger: check %s insert: %w", operationOriginKey, err)
	}
	if inserted == 0 {
		// 다른 Open이 먼저 기록했으면 실제 저장된 값을 검증한다.
		stored, err := db.SchemaMeta(ctx, operationOriginKey)
		if err != nil {
			return err
		}
		_, err = parseOperationOrigin(stored)
		return err
	}

	// 단발 사건이므로 남긴다. 매 실행 나오는 로그가 아니다.
	// 배포 직후 기관별 값을 이 줄로 확인한다 — 운영 시작보다 과거라면
	// 자동 resend 가 그 사이 구간을 보낸다.
	log.Printf(
		"[LEDGER] %s=%s recorded (from MIN(first_seen))",
		operationOriginKey,
		value,
	)

	return nil
}

// minFirstSeenDay 는 MIN(first_seen) 의 UTC 날짜를 돌려준다.
// 행이 없으면 found=false 다.
func (db *DB) minFirstSeenDay(
	ctx context.Context,
) (day time.Time, found bool, err error) {
	// MIN 집계는 빈 테이블에서도 NULL 한 행을 돌려준다.
	var minSeen sql.NullInt64

	if err := db.conn.QueryRowContext(
		ctx,
		`SELECT MIN(first_seen)
		   FROM common_ledger;`,
	).Scan(&minSeen); err != nil {
		return time.Time{}, false, fmt.Errorf(
			"ledger: read min first_seen: %w",
			err,
		)
	}

	if !minSeen.Valid {
		return time.Time{}, false, nil
	}

	// 0·음수·epoch 근처는 미설정·손상이다. UTC 날짜로 접으면
	// 1970-01-01 이 되고, 자동 창 하한이 Retention 한계로 밀려
	// 설치 직후 수십 일을 신규로 보낸다 — §3.5 가 막으려던 사고와
	// 같다. 오늘로 대체하지 않는다. 연도 하한은 parseOperationOrigin.
	if minSeen.Int64 <= 0 {
		return time.Time{}, false, fmt.Errorf(
			"ledger: min first_seen %d is not a valid unix time",
			minSeen.Int64,
		)
	}

	day = utcMidnight(time.Unix(minSeen.Int64, 0).UTC())
	if _, err := parseOperationOrigin(day.Format(originDateLayout)); err != nil {
		return time.Time{}, false, fmt.Errorf("ledger: invalid min first_seen: %w", err)
	}
	return day, true, nil
}

// OperationOrigin 은 운영 시작일을 UTC 자정 시각으로 반환한다.
//
//	키 있음        → 기록된 값 (불변).
//	키 없음, 행 있음 → MIN(first_seen) 의 날짜. 첫 회차의 put 이 행을
//	                  만든 직후(같은 프로세스의 resend 단계)가 이 경우다.
//	                  다음 Open 이 같은 값을 기록한다.
//	키 없음, 행 없음 → now 의 날짜. 운영 전이며, 기록하지 않는다.
//
// now 는 호출자의 회차 시각이다 — resend 창 계산과 같은 "오늘"을 쓰게
// 하고, 테스트가 시계를 고정할 수 있게 한다.
func (db *DB) OperationOrigin(
	ctx context.Context,
	now time.Time,
) (time.Time, error) {
	value, err := db.SchemaMeta(ctx, operationOriginKey)
	if errors.Is(err, ErrSchemaMetaMissing) {
		day, found, err := db.minFirstSeenDay(ctx)
		if err != nil {
			return time.Time{}, err
		}

		if !found {
			return utcMidnight(now), nil
		}

		return day, nil
	}

	if err != nil {
		return time.Time{}, err
	}

	return parseOperationOrigin(value)
}

// 시작 시와 조회 시 동일하게 검증한다. 잘못된 값을 오늘이나 Retention
// 하한으로 대체하면 자동 resend 범위가 달라지므로 오류를 전파한다.
func parseOperationOrigin(value string) (time.Time, error) {
	day, err := time.ParseInLocation(
		originDateLayout,
		value,
		time.UTC,
	)
	if err != nil {
		return time.Time{}, fmt.Errorf(
			"ledger: parse %s %q: %w",
			operationOriginKey,
			value,
			err,
		)
	}
	// 2000년 이전은 unix epoch 잔재(1970-01-01)와 구분되지 않는다.
	// 그 값을 하한으로 두면 첫 자동 창이 Retention 전체로 열린다.
	if day.Year() < 2000 || day.Format(originDateLayout) != value {
		return time.Time{}, fmt.Errorf("ledger: invalid %s date %q", operationOriginKey, value)
	}

	return day, nil
}

// utcMidnight 는 t 를 UTC 날짜의 자정으로 자른다.
func utcMidnight(t time.Time) time.Time {
	u := t.UTC()

	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
