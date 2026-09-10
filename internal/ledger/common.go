package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"SFTPClient/internal/domain"
)

// CommonInput 은 Scan 과 Ingress 검증을 통과한 파일 하나의 관측값이다.
//
// 호출자는 관측한 사실만 채운다.
// revision, state, first_seen 같은 Ledger 내부 값은 채우지 않는다.
// 그것들은 저장된 이전 행과 비교하여 ledger 패키지가 결정한다.
//
// 호출자가 revision 을 직접 구성하는 코드 경로를 만들지 않는다.
// (CONCEPT 5④)
//
// 로컬 경로는 담지 않는다. (schema v5)
// common_ledger 는 "Ingress 검증을 통과한 어떤 파일을 관측했는가"를 기록하며,
// 파일의 현재 위치를 기억하는 것이 목적이 아니다.
//
// 전송에 필요한 현재 경로는 Scan 이 디렉터리를 나열한 시점부터
// 메모리로 들고 다니며 DB 를 거치지 않는다.
//
// 경로는 실제 운영에서 변경될 수 있으므로 Ledger 가 이를 전송 경로의
// 기준으로 삼지 않는다. PUT 후보는 DB 단독 조회가 아니라
// 이번 Scan Entry 와 Ledger 상태를 대조하여 결정한다.
// (SCAN DESIGN 6절, CONCEPT 4.1·4.5)
type CommonInput struct {
	// FileName 은 domain.NormalizeName 을 거친 값이어야 한다.
	FileName string

	// BaseName 은 domain.BaseName 을 거친 값이어야 한다.
	BaseName string

	// Category 는 config 의 [PUT.<CATEGORY>] 섹션에서 전달받은 값이다.
	// Scanner 가 파일에서 판정하지 않는다.
	Category domain.Category

	// Size 는 바이트 크기이다.
	// CommonInput 은 Ingress 검증을 통과한 값이므로 반드시 0보다 커야 한다.
	Size int64

	// MTime 은 최종 수정시각이다.
	// Unix epoch 초, UTC 기준이다.
	//
	//	info.ModTime().UTC().Unix()
	MTime int64

	// Origin 은 최초 등록 시에만 사용한다.
	// 동일 file_name 이 재관측되어도 기존 origin 을 유지한다.
	Origin domain.Origin

	// IngressVerifiedAt 은 Ingress 검증을 통과한 시각이다.
	// Unix epoch 초, UTC 기준이다. (설계안 7)
	//
	// Retention Cleanup 의 기준 컬럼이기도 하다. revision 이 증가하면
	// 갱신되므로, 오래전에 처음 발견된 파일이 최근 다시 갱신된 경우에도
	// 최신 검증 시각이 보존된다. (SCAN DESIGN 5절 Retention Cleanup)
	IngressVerifiedAt int64
}

// ErrInvalidInput 은 CommonInput 이 Ledger 에 기록될 수 없는 값일 때 반환된다.
//
// schema.sql 의 CHECK 제약도 잘못된 값을 거부하지만,
// 가능한 오류는 DB 에 도달하기 전에 구체적인 원인과 함께 거부한다.
var ErrInvalidInput = errors.New("ledger: invalid common input")

// Validate 는 CommonInput 이 common_ledger 에 기록 가능한 값인지 검사한다.
//
// 이 함수는 파일이 실제로 정상인지 판정하는 Ingress Verification 이 아니다.
// verify 패키지에서 이미 판정된 결과가 Ledger 의 저장 규약을 만족하는지만 확인한다.
func (in CommonInput) Validate() error {
	switch {
	case in.FileName == "":
		return fmt.Errorf(
			"%w: file_name is empty",
			ErrInvalidInput,
		)

	case domain.NormalizeName(in.FileName) != in.FileName:
		// 정규화를 거치지 않은 값이 들어오면 같은 파일이 서로 다른
		// file_name 으로 등록되어 중복 전송의 원인이 될 수 있다.
		return fmt.Errorf(
			"%w: file_name %q is not normalized",
			ErrInvalidInput,
			in.FileName,
		)

	case domain.BaseName(in.FileName) != in.BaseName:
		return fmt.Errorf(
			"%w: base_name %q does not match file_name %q",
			ErrInvalidInput,
			in.BaseName,
			in.FileName,
		)

	case !in.Origin.Valid():
		return fmt.Errorf(
			"%w: unknown origin %q",
			ErrInvalidInput,
			in.Origin,
		)

	case in.Size <= 0:
		// common_ledger 에 존재한다는 것 자체가 Ingress 검증 통과를 뜻한다.
		// 따라서 0바이트 파일은 이 계층까지 들어오면 안 된다.
		return fmt.Errorf(
			"%w: size %d must be greater than zero",
			ErrInvalidInput,
			in.Size,
		)

	case in.MTime <= 0:
		return fmt.Errorf(
			"%w: mtime %d is not set",
			ErrInvalidInput,
			in.MTime,
		)

	case in.IngressVerifiedAt <= 0:
		return fmt.Errorf(
			"%w: ingress_verified_at %d is not set",
			ErrInvalidInput,
			in.IngressVerifiedAt,
		)
	}

	// Category 는 domain 에 정의된 정확한 값이어야 한다.
	//
	// ParseCategory 는 config 입력을 위해 공백·대소문자를 허용하므로,
	// 파싱 결과가 원래 값과 같은지까지 확인하여
	// "rinex3_hourly" 같은 느슨한 값이 DB 에 저장되는 것을 막는다.
	parsed, err := domain.ParseCategory(string(in.Category))
	if err != nil || parsed != in.Category {
		return fmt.Errorf(
			"%w: unknown category %q",
			ErrInvalidInput,
			in.Category,
		)
	}

	return nil
}

// Result 는 UpsertCommon 이 common_ledger 에 실제로 수행한 작업을 나타낸다.
//
// 이 값은 관측 수단이지 후보 선정 수단이 아니다.
// 무엇을 전송할지는 put_ledger 와의 revision 매칭이 결정하며,
// Result 는 Scan 요약 로그의 new= / changed= 집계에 쓴다.
// (CONCEPT 4.5)
type Result int

const (
	// ResultUnchanged 는 기존 행과 size·mtime 이 같아
	// 아무 변경도 하지 않았음을 뜻한다.
	//
	// 동일 파일을 반복 Scan 하는 정상 운영에서 가장 흔한 결과이다.
	//
	// 제로값이므로 오류와 함께 반환되는 값이기도 하다.
	// 호출자는 Result 보다 error 를 먼저 확인한다.
	ResultUnchanged Result = iota

	// ResultInserted 는 처음 보는 파일을 등록했음을 뜻한다.
	// revision=1, state=READY 로 시작한다.
	ResultInserted

	// ResultUpdated 는 기존 파일의 size 또는 mtime 이 달라져
	// revision 이 증가했음을 뜻한다.
	// state 는 CHANGED 가 된다.
	ResultUpdated
)

// String 은 로그와 테스트에서 사용할 Result 표기를 반환한다.
func (r Result) String() string {
	switch r {
	case ResultUnchanged:
		return "Unchanged"

	case ResultInserted:
		return "Inserted"

	case ResultUpdated:
		return "Updated"

	default:
		return fmt.Sprintf("Result(%d)", int(r))
	}
}

// upsertCommonSQL 은 common_ledger 한 행을 원자적으로 등록하거나 갱신한다.
//
// 동작:
//
//	행 없음
//	  → INSERT
//	  → revision=1
//	  → state=READY
//
//	같은 (category, file_name) + 같은 size·mtime
//	  → ON CONFLICT 의 WHERE 가 false
//	  → 아무 행도 변경하지 않음
//	  → RETURNING 결과 없음
//
//	같은 (category, file_name) + 다른 size 또는 mtime
//	  → UPDATE
//	  → revision + 1
//	  → state=CHANGED
//
// UPDATE 하지 않는 값:
//
//	file_name / category
//	  논리 식별자(복합키)이므로 변경하지 않는다.
//	  (2026-08-30) 단독 file_name PK 시절에는 category 를 UPDATE 대상에
//	  넣어 config 오기입 시 덮어쓸 수 있었고, RINEX3/RINEX4 동일
//	  file_name 이 서로를 "변경됨"으로 만드는 재전송 루프가 열렸다.
//	  복합키 ON CONFLICT (category, file_name) 로 그 경로를 닫았다.
//	  category 는 키의 일부이므로 DO UPDATE SET 에 두지 않는다.
//
//	first_seen
//	  최초 발견 시각이다. revision 이 증가해도 유지한다.
//
//	origin
//	  최초 유입 경로를 유지한다.
//	  DOWNLOAD 로 생성된 파일이 이후 Local Scanner 에서 재관측되더라도
//	  LOCAL 로 덮어쓰지 않는다.
//	  그래야 PUT 후보 기본 제외 규칙과 Ping-Pong 방지가 유지된다.
//
// v5 변경: local_path 컬럼이 삭제되어 INSERT·UPDATE 양쪽에서 사라졌다.
// v8 변경: set_key·kind 컬럼이 추가되었다. 이 두 값은 CommonInput 이 아니라
// (category, file_name) 에서 domain.SetKeyKind 로 도출한다(파일명 파생의 단일
// 주인 — 호출자가 값을 구성하는 경로를 만들지 않는다).
// 파라미터는 총 12개이다 — VALUES 바인딩 11개 + DO UPDATE 의 state 1개.
//
// set_key·kind 는 DO UPDATE 에도 넣는다. 같은 (category, file_name) 이면 값이
// 동일하므로 사실상 불변 재기록이지만, 파서가 나중에 확장되어 과거 유보(”)
// 이름을 읽게 되는 경우 size·mtime 변경 시점에 자연히 채워지는 이점이 있다.
// 파라미터는 총 10개이다.
// VALUES 절의 바인딩 9개 + DO UPDATE 의 state 1개이다.
//
// WHERE 조건은 size 와 mtime 을 OR 로 본다.
// size 가 같고 mtime 만 달라도 revision 을 올린다.
// AND 로 두면 크기가 우연히 같은 보정 파일을 놓칠 수 있다.
//
// 그 방향의 실수(내용이 바뀌었는데 안 보냄)가
// 반대 방향의 실수(불필요한 재전송)보다 위험하다.
// 누락은 조용히 사라질 수 있지만 헛전송은 Ledger/Log 에 남는다.
//
// 대가로 볼륨 이전이나 대량 복사 등으로 mtime 이 일괄 변경되면
// Scan 범위 안의 파일이 변경 파일로 판정되어 재전송될 수 있다.
// (CONCEPT 4.9)
//
// WHERE 절을 없애면 Unchanged 도 RETURNING 을 돌려주게 되어 편해 보이지만,
// 값이 같아도 페이지가 갱신되어 WAL 이 커진다. 한 번의 Scan 이 수만 건을
// 훑고 그 대부분이 Unchanged 이므로 이 조건은 유지한다.
//
// RETURNING revision 으로 INSERT / UPDATE 를 구분한다.
// WHERE 가 false 여서 아무 행도 변경되지 않으면 QueryRow.Scan 은
// sql.ErrNoRows 를 반환한다.
// 이 동작은 실제 SQLite 로 검증했으며 common_test.go 가 회귀를 막는다.
const upsertCommonSQL = `
INSERT INTO common_ledger (
    file_name,
    base_name,
    category,
    size,
    mtime,
    origin,
    revision,
    state,
    first_seen,
    ingress_verified_at,
    set_key,
    kind
) VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
ON CONFLICT (category, file_name) DO UPDATE SET
    base_name           = excluded.base_name,
    size                = excluded.size,
    mtime               = excluded.mtime,
    revision            = common_ledger.revision + 1,
    state               = ?,
    ingress_verified_at = excluded.ingress_verified_at,
    set_key             = excluded.set_key,
    kind                = excluded.kind
WHERE common_ledger.size  <> excluded.size
   OR common_ledger.mtime <> excluded.mtime
RETURNING revision;
`

// UpsertCommon 은 Ingress 검증을 통과한 파일 하나를 common_ledger 에 반영한다.
//
// common_ledger 에 행이 존재한다는 것 자체가
// Ingress Verification 을 통과했다는 의미이다.
// 미통과 파일은 이 함수를 호출하지 않고 다음 Scan 에서 다시 판정한다.
// (CONCEPT 4.4)
//
// revision 을 반환하지 않는 이유:
//
//	Unchanged 는 갱신된 행이 없어 RETURNING 이 비므로 revision 을 알 수 없고,
//	정상 운영에서는 그것이 대부분이다. 그러나 호출자는 어차피 디렉터리
//	단위로 put_ledger 상태를 일괄 조회하며, 그 조회는 이 Upsert 들이 모두
//	끝난 뒤에 돌므로 common_ledger.revision 이 이미 최신이다.
//
//	  SELECT c.file_name, c.revision, p.status
//	    FROM common_ledger c
//	    LEFT JOIN put_ledger p
//	      ON  p.category  = c.category
//	      AND p.file_name = c.file_name
//	      AND p.revision  = c.revision
//	   WHERE c.category = ?
//	     AND c.file_name IN (?, ?, ...);
//
//	즉 revision 의 주인은 이 조회이고, 여기서 중복해서 돌려주지 않는다.
//	시그니처에 revision 을 추가하면 같은 사실에 주인이 둘이 된다.
//	(SCAN DESIGN 6절 v5 후보 선정)
func (db *DB) UpsertCommon(
	ctx context.Context,
	in CommonInput,
) (Result, error) {
	if err := in.Validate(); err != nil {
		return ResultUnchanged, err
	}

	// first_seen 은 신규 INSERT 에서만 사용한다.
	// 충돌 후 UPDATE 절에서는 이 값을 사용하지 않으므로
	// 기존 최초 발견 시각은 보존된다.
	firstSeen := time.Now().UTC().Unix()

	// set_key·kind 는 (category, file_name) 에서 도출한다. CommonInput 에
	// 담지 않는 이유는 파일명 파생의 주인을 domain 하나로 두기 위함이다 —
	// 게이트 ON/OFF 와 무관하게 모든 기록 경로가 이 파생을 지나므로
	// "'' = 세트 소속을 확정할 수 없는 이름" 이라는 불변식이 구조적으로
	// 보장된다 (마이그레이션 백필과 함께 그 불변식의 나머지 절반이다).
	//
	//	err != nil  — category 가 SetKeyKind 에 라우팅되지 않음(enum 공백).
	//	              Validate 를 통과한 정상 category 에서는 발생하지 않지만,
	//	              발생하면 조용히 넘기지 않고 이 파일의 기록을 막는다.
	//	ok == false — 세트 소속 유보. SetKeyKind 가 돌려준 "" 를 그대로
	//	              저장한다. 스키마가 NOT NULL DEFAULT '' 이므로 NULL
	//	              바인딩은 제약 위반이다 — nullable 안은 기각되었다
	//	              (2026-09-10, schema.sql v8 주석 참조).
	setKey, kind, _, err := domain.SetKeyKind(in.Category, in.FileName)
	if err != nil {
		return ResultUnchanged, fmt.Errorf(
			"ledger: set_key/kind for %q: %w",
			in.FileName,
			err,
		)
	}

	var revision int64

	err = db.conn.QueryRowContext(
		ctx,
		upsertCommonSQL,
		in.FileName,
		in.BaseName,
		string(in.Category),
		in.Size,
		in.MTime,
		string(in.Origin),
		string(domain.StateReady),
		firstSeen,
		in.IngressVerifiedAt,
		setKey,
		kind,
		string(domain.StateChanged),
	).Scan(&revision)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// 동일 (category, file_name) 이 이미 존재하고 size·mtime 도 같아
		// DO UPDATE WHERE 조건이 false 였다.
		return ResultUnchanged, nil

	case err != nil:
		return ResultUnchanged, fmt.Errorf(
			"ledger: upsert %q: %w",
			in.FileName,
			err,
		)
	}

	switch {
	case revision == 1:
		return ResultInserted, nil

	case revision > 1:
		return ResultUpdated, nil

	default:
		// schema.sql 은 revision >= 1 을 강제하므로 정상적으로 발생할 수 없다.
		// DB/스키마 불일치를 조용히 받아들이지 않는다.
		return ResultUnchanged, fmt.Errorf(
			"ledger: upsert %q returned invalid revision %d",
			in.FileName,
			revision,
		)
	}
}
