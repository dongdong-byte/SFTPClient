package ledger

// 이 파일은 "revision 을 올리지 않는 쓰기" 두 개를 담는다.
// (UNIT2 설계 v3 §2.2)
//
// UpsertCommon 이 소유한 사실은 "파일의 재관측"이며, size 또는 mtime 이
// 다르면 revision 이 오른다. 아래 두 함수가 소유한 사실은 그와 다르다:
//
//	TouchCommonMTime — 내용 동일이 확인된 mtime 드리프트에서 변경 판정
//	                   기준선(mtime)만 현재 관측치로 옮긴다.
//	SetContentHash   — 지문 없는 행의 자연 백필. 내용은 그대로라는
//	                   판정 위에서 지문만 채운다.
//
// 둘을 upsertCommonSQL 에 욱여넣지 않는 이유(§2.2 기각 기록): upsert 의
// WHERE 는 "size≠ OR mtime≠ → revision+1" 이라는 재관측 계약이고, 이
// 함수들은 정확히 그 계약을 우회해야 하는 쓰기다. 한 문에 합치면 WHERE
// 가 3중 분기되고 Unchanged 의 WAL 비대 방지 조건이 깨진다.
//
// 두 함수 모두 "판정에 사용한 기존 사실 전부"를 WHERE 가드로 요구한다.
// 단일 실행 lock 아래에서 그 사이 행을 바꿀 주체는 자기 자신뿐이므로,
// 가드 불일치(applied=false)는 경합이 아니라 코드 불변식 이상의 신호다.
// 호출자(put)의 0행 정책: WARN 기록 + 해당 파일만 이번 회차 보류.
// 조용한 무시(신호 은폐)와 실행 중단(파일 하나로 회차 전체 중단)은
// 기각되었다 — §2.2.

import (
	"context"
	"errors"
	"fmt"

	"SFTPClient/internal/domain"
)

// ErrInvalidRefreshInput 은 TouchCommonMTime / SetContentHash 의 입력이
// 저장 규약을 만족하지 않을 때 반환된다.
//
// CommonInput 의 ErrInvalidInput 과 같은 원칙이다: schema.sql 의 CHECK 가
// 최후 방어선이지만, 가능한 오류는 DB 에 도달하기 전에 구체적인 원인과
// 함께 거부한다.
var ErrInvalidRefreshInput = errors.New("ledger: invalid refresh input")

// touchCommonMTimeSQL 은 변경 판정 기준선(mtime)만 갱신한다.
//
// SET 에 mtime 하나뿐인 것이 계약이다 — revision·state·content_hash·
// ingress_verified_at 은 건드리지 않는다. MetadataChangedOnly 는 재검증도
// 내용 변경도 아니므로 그 사실들의 주인이 아니다. (설계 v3 §1 판정표)
//
// WHERE 는 판정에 사용한 기존 사실 전부다. revision 만 거는 안은
// 기각되었다(§2.2) — 판정 근거(size·mtime·content_hash)가 그대로일 때만
// 갱신되어야 "판정 당시의 행"에 쓴다는 계약이 완전해진다.
const touchCommonMTimeSQL = `
UPDATE common_ledger
   SET mtime = ?
 WHERE category     = ?
   AND file_name    = ?
   AND revision     = ?
   AND size         = ?
   AND mtime        = ?
   AND content_hash = ?;
`

// TouchCommonMTime 은 MetadataChangedOnly 판정(size 같음 · mtime 다름 ·
// 지문 일치) 이후 장부의 mtime 기준선을 현재 관측치로 옮긴다.
//
// k 는 판정에 사용한 Known 그대로여야 한다 — LookupCommon 이 돌려준
// 사실을 호출자가 가공 없이 되넘기는 구조라서, 부분 가드를 조립하는
// 경로가 애초에 없다.
//
// 반환 applied:
//
//	true  — 정확히 그 행에 썼다.
//	false — 판정 근거와 현재 행이 다르다(0행). 오류가 아니라 사실이며,
//	        처리 정책(WARN + 해당 파일 회차 보류)은 호출자 몫이다.
//
// newMTime 이 k.MTime 과 같으면 판정 자체가 성립하지 않은 것이므로
// 입력 오류로 거부한다 — MetadataChangedOnly 는 mtime 이 다를 때만
// 존재하는 판정이다.
func (db *DB) TouchCommonMTime(
	ctx context.Context,
	category domain.Category,
	k Known,
	newMTime int64,
) (applied bool, err error) {
	if err := validateRefreshKey(category, k); err != nil {
		return false, err
	}

	// MetadataChangedOnly 는 기존 지문과 새 안정 해시가 같다는 증명 위에서만
	// 성립한다. 지문 없는(v5 이관·백필 미도달) 행의 mtime 기준선을 먼저
	// 옮기면, 같은 크기의 실제 내용 변경도 다음 관측에서 Unchanged 로 보일
	// 수 있으므로 Touch 로 우회시키지 않는다.
	if k.ContentHash == "" {
		return false, fmt.Errorf(
			"%w: touch mtime requires an existing content hash",
			ErrInvalidRefreshInput,
		)
	}

	if newMTime <= 0 {
		return false, fmt.Errorf(
			"%w: new mtime %d is not set",
			ErrInvalidRefreshInput,
			newMTime,
		)
	}

	if newMTime == k.MTime {
		return false, fmt.Errorf(
			"%w: new mtime equals judged mtime %d "+
				"(MetadataChangedOnly requires a drift)",
			ErrInvalidRefreshInput,
			k.MTime,
		)
	}

	res, err := db.conn.ExecContext(
		ctx,
		touchCommonMTimeSQL,
		newMTime,
		string(category),
		k.FileName,
		k.Revision,
		k.Size,
		k.MTime,
		k.ContentHash,
	)
	if err != nil {
		return false, fmt.Errorf(
			"ledger: touch mtime %q: %w",
			k.FileName,
			err,
		)
	}

	return oneRowApplied(res, "touch mtime", k.FileName)
}

// setContentHashSQL 은 지문 없는 행에만 지문을 채운다.
//
// WHERE 의 content_hash = ” 가 백필의 안전 근거를 SQL 로 강제한다:
// "size=·mtime= 이면 내용 동일"이라는 전제 위에서만 쓰며, 해시 계산과
// 저장 사이에 행이 변했다면(다른 경로가 revision 을 올렸거나 지문을
// 채웠다면) 0행으로 무해하게 끝난다. (설계 v3 §2.2)
const setContentHashSQL = `
UPDATE common_ledger
   SET content_hash = ?
 WHERE category     = ?
   AND file_name    = ?
   AND revision     = ?
   AND size         = ?
   AND mtime        = ?
   AND content_hash = '';
`

// SetContentHash 는 Unchanged 판정(size 같음 · mtime 같음)이면서 지문이
// 없는 행에 지문을 채운다 — 자연 백필과 후보 필수 해시의 저장 경로다.
// (설계 v3 §3)
//
// k 는 판정에 사용한 Known 그대로여야 하며, k.ContentHash 는 ” 여야
// 한다. 지문이 이미 있는 행에 이 함수를 부르는 것은 백필이 아니라
// 덮어쓰기이며, 그 경로는 UpsertCommon(재관측)만 가진다 — 오용은
// 조용히 0행으로 접지 않고 시끄럽게 거부한다.
//
// hash 는 유효한 지문(hex 소문자 64자)이어야 한다. ” 백필은 무의미
// 하므로 입력 오류다.
//
// 반환 applied 의 의미는 TouchCommonMTime 과 같다.
func (db *DB) SetContentHash(
	ctx context.Context,
	category domain.Category,
	k Known,
	hash string,
) (applied bool, err error) {
	if err := validateRefreshKey(category, k); err != nil {
		return false, err
	}

	if k.ContentHash != "" {
		return false, fmt.Errorf(
			"%w: row already has a content hash "+
				"(backfill only fills empty fingerprints; "+
				"content changes go through UpsertCommon)",
			ErrInvalidRefreshInput,
		)
	}

	if hash == "" || !validContentHash(hash) {
		return false, fmt.Errorf(
			"%w: content_hash %q must be 64 lowercase hex chars",
			ErrInvalidRefreshInput,
			hash,
		)
	}

	res, err := db.conn.ExecContext(
		ctx,
		setContentHashSQL,
		hash,
		string(category),
		k.FileName,
		k.Revision,
		k.Size,
		k.MTime,
	)
	if err != nil {
		return false, fmt.Errorf(
			"ledger: set content hash %q: %w",
			k.FileName,
			err,
		)
	}

	return oneRowApplied(res, "set content hash", k.FileName)
}

// validateRefreshKey 는 두 함수 공통의 키·판정 사실 규약을 검사한다.
// LookupCommon 과 같은 패턴·같은 근거다.
func validateRefreshKey(category domain.Category, k Known) error {
	parsed, err := domain.ParseCategory(string(category))
	if err != nil || parsed != category {
		return fmt.Errorf(
			"%w: unknown category %q",
			ErrInvalidRefreshInput,
			category,
		)
	}

	switch {
	case k.FileName == "" || domain.NormalizeName(k.FileName) != k.FileName:
		return fmt.Errorf(
			"%w: file_name %q is not normalized",
			ErrInvalidRefreshInput,
			k.FileName,
		)

	case k.Revision < 1:
		return fmt.Errorf(
			"%w: revision %d must be at least 1",
			ErrInvalidRefreshInput,
			k.Revision,
		)

	case k.Size <= 0:
		return fmt.Errorf(
			"%w: size %d must be greater than zero",
			ErrInvalidRefreshInput,
			k.Size,
		)

	case k.MTime <= 0:
		return fmt.Errorf(
			"%w: mtime %d is not set",
			ErrInvalidRefreshInput,
			k.MTime,
		)

	case !validContentHash(k.ContentHash):
		return fmt.Errorf(
			"%w: judged content_hash %q must be empty or 64 lowercase hex chars",
			ErrInvalidRefreshInput,
			k.ContentHash,
		)
	}

	return nil
}

// oneRowApplied 는 UPDATE 결과를 applied bool 로 접는다.
//
// 이 두 SQL 의 WHERE 는 PK(category, file_name)를 포함하므로 대상 행은
// 최대 하나다. 2행 이상은 스키마 붕괴이므로 조용히 true 로 접지 않고
// 오류로 올린다.
func oneRowApplied(res interface{ RowsAffected() (int64, error) },
	op, fileName string,
) (bool, error) {
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf(
			"ledger: %s %q: rows affected: %w",
			op,
			fileName,
			err,
		)
	}

	switch n {
	case 0:
		return false, nil

	case 1:
		return true, nil

	default:
		return false, fmt.Errorf(
			"ledger: %s %q affected %d rows (PK broken?)",
			op,
			fileName,
			n,
		)
	}
}
