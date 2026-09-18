package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"SFTPClient/internal/domain"
)

// maxLookupNames 는 IN (...) 조회 한 번에 넣는 최대 이름 수이다.
//
// SQLite 의 바인드 상한(SQLITE_MAX_VARIABLE_NUMBER)보다 훨씬 보수적으로 잡는다.
// 관측소별 디렉터리가 없어 한 디렉터리에 수백 개가 들어오는 것이 정상 규모이므로
// 실무상 대부분 한 번의 조회로 끝나고, 상한 근처의 거대한 SQL 문자열을 만들
// 이유가 없다. (CONCEPT 4.5)
//
// category 바인드 1개가 추가되지만 maxLookupNames=500 이므로 상한과 충분히 멀다.
//
// 이 값을 넘는 입력은 오류가 아니라 여러 번의 조회로 나뉜다.
// SetMaxOpenConns(1) 이므로 분할 조회도 자연히 직렬이다.
const maxLookupNames = 500

// ErrInvalidLookupName 은 정규화되지 않은 이름으로 조회를 시도했을 때
// 반환된다.
//
// 정규화 누락을 조용히 받으면 그 이름은 장부에 "없음"으로 판정되고,
// 호출자는 신규 파일로 오인하여 재전송할 수 있다.
// 오류가 나지 않고 조용히 틀리는 종류이므로 CommonInput.Validate 와
// 같은 방침으로 입구에서 막는다.
var ErrInvalidLookupName = errors.New("ledger: lookup name is not normalized")

// ErrInvalidLookupCategory 는 정의되지 않았거나 정규화되지 않은
// Category 로 조회를 시도했을 때 반환된다.
//
// 잘못된 Category 로 조회하면 모든 파일이 "장부에 없음"으로 보여
// 호출자가 신규로 오인할 수 있다. CommonInput.Validate 와 같은
// 방침으로 입구에서 막는다.
var ErrInvalidLookupCategory = errors.New("ledger: invalid lookup category")

// Known 은 (category, file_name) 하나에 대해 장부가 아는 사실이다.
//
// 판정하지 않는다. 장부는 기록하고, 판정은 밖에서 한다. (CONCEPT 2.1)
// 신규/변경/제외 판정은 호출자(put)가 이 값과 Scan 이 관측한 디스크
// 실측치(size·mtime)를 메모리에서 대조하여 내린다. (CONCEPT 4.5)
//
// LookupCommon 호출 자체가 하나의 Category 로 한정되므로 Known 에
// Category 를 중복 저장하지 않는다. 반환 map 의 key 역시 file_name 만으로
// 충분하다.
//
// 대조가 ledger 안에 들어오지 않는 이유는 verify 와 같다.
// 대조에는 디스크 쪽 사실이 필요하고 그것은 호출자만 들고 있다.
// ledger 가 디스크 실측치를 인자로 받기 시작하면
// "장부는 기록만 한다" 는 경계가 흐려진다.
type Known struct {
	// FileName 은 정규화된 논리 파일명이다.
	// DB 의 실제 식별자는 (category, file_name) 복합키이다.
	FileName string

	// Revision 은 이 Category/FileName 의 현재 개정 번호이다.
	Revision int64

	// Size 는 장부가 기억하는 최근 관측 크기이다.
	// 호출자가 디스크 실측치와 대조하여 변경 여부를 판정한다.
	Size int64

	// MTime 은 변경 판정 기준선이다. Unix 초, UTC.
	// (v9 의미 재정의 — schema.sql mtime 주석 참조)
	MTime int64

	// ContentHash 는 이 revision 으로 최근 안정 관측한 파일 전체
	// 바이트의 SHA-256 지문(hex 소문자 64자)이다.
	// '' 는 지문 없음 — v9 이전 행 / 백필 미도달 / 해시 실패.
	// size 가 같고 mtime 만 다른 관측에서만 대조한다. (UNIT2 설계 v3)
	ContentHash string

	// Origin 은 최초 유입 경로이다.
	// PUT 후보 선정에서 DOWNLOAD 를 기본 제외하는
	// Ping-Pong 방지 판정에 쓰인다. (설계안 9.1)
	Origin domain.Origin

	// State 는 입고 상태(READY/CHANGED)이다.
	// 후보 필터가 아니라 운영 관측용이다. (CONCEPT 4.5)
	State domain.State

	// PutStatus 는 현재 Revision 에 대한 put_ledger 상태이다.
	//
	// 해당 Revision 의 전송 이력이 아직 없으면 빈 값("")이다.
	// "이력 없음" 과 "PENDING" 은 다른 사실이므로 구분해야 하는데,
	// domain.Status 의 유효값에 "" 가 없어 제로값으로 안전하게
	// 표현된다. 포인터를 쓰지 않는 이유다.
	PutStatus domain.Status
}

// lookupCommonSQL 은 하나의 Category 안에서 이름 목록에 대한
// 장부의 사실을 한 번에 읽는다.
//
//	ON  p.category  = c.category
//	AND p.file_name = c.file_name
//	AND p.revision  = c.revision
//
// 이 세 조건이 현재 논리 파일의 현재 Revision 에 대한 PUT 상태를
// 정확히 연결한다.
//
// RINEX3 과 RINEX4 는 같은 long filename 을 가질 수 있으므로
// file_name 만으로 조인하거나 조회하면 안 된다.
// category 는 반드시 조회 조건과 JOIN 조건에 함께 들어간다.
//
// put_ledger 의 PK 는 (category, file_name, revision)이므로
// common 한 행당 현재 revision 의 put 행은 최대 하나다.
// 따라서 결과를 map[string]Known 으로 표현할 수 있다.
//
// COALESCE 로 현재 revision 의 전송 이력 없음을 빈 문자열로 접는다.
// NULL 을 *string 으로 받는 것보다 Known.PutStatus 의 제로값 규약과
// 곧바로 맞는다.
//
// %s 자리에는 이름 개수만큼의 ? 가 들어간다.
// category 와 file_name 값은 모두 바인드 파라미터로 전달하며
// SQL 문자열에 값을 직접 잇지 않는다.
const lookupCommonSQL = `
SELECT c.file_name,
       c.revision,
       c.size,
       c.mtime,
       c.origin,
       c.state,
       c.content_hash,
       COALESCE(p.status, '')
  FROM common_ledger c
  LEFT JOIN put_ledger p
    ON  p.category  = c.category
    AND p.file_name = c.file_name
    AND p.revision  = c.revision
 WHERE c.category = ?
   AND c.file_name IN (%s);
`

// LookupCommon 은 하나의 category 안에서 names 의 각 파일에 대해
// 장부가 아는 사실을 돌려준다.
//
// DB 의 논리 식별자는 (category, file_name)이지만, 이 함수 호출 하나가
// 이미 Category 하나로 한정되므로 반환 map 의 key 는 file_name 만 사용한다.
//
// 반환 map 에 키가 없으면 해당 Category 에서 장부가 모르는 파일,
// 즉 신규이다. 이것은 오류가 아니라 정상적인 답이므로 부재를 오류로
// 보고하지 않는다.
//
// names 는 domain.NormalizeName 을 거친 값이어야 한다.
// 아니면 ErrInvalidLookupName 을 반환한다.
//
// .part 파일은 NormalizeName 과정에서 최종 파일명과 같은 키가 될 수 있으므로
// 정상 PUT 흐름에서는 LookupCommon 호출 전에 제외한다.
// .filepart 는 정규화 불변이라 이 가드가 거부하지 않는다. 미완성 입력
// 제외는 IsPartFile 게이트의 책임이다.
// 이 함수는 전달된 이름이 이미 정규화되었는지만 확인한다.
//
// 중복 이름은 허용하되 한 번만 조회하고,
// 빈 입력은 빈 map 을 돌려준다.
//
// Scan 의 Batch 하나(디렉터리 하나)가 이 함수 호출 한 번에 대응한다.
// Batch 는 Category 하나만 가지므로 이 API 와 자연스럽게 맞는다.
// (SCAN DESIGN 6절 v5 후보 선정)
func (db *DB) LookupCommon(
	ctx context.Context,
	category domain.Category,
	names []string,
) (map[string]Known, error) {
	// Category 는 domain 에 정의된 정확한 값이어야 한다.
	//
	// ParseCategory 는 config 입력을 위해 공백·대소문자를 허용하므로,
	// 파싱 결과가 원래 값과 같은지까지 확인하여
	// "rinex3_hourly" 같은 느슨한 값이 조회 키로 쓰이는 것을 막는다.
	// CommonInput.Validate 와 같은 패턴·같은 근거이다.
	parsed, err := domain.ParseCategory(string(category))
	if err != nil || parsed != category {
		return nil, fmt.Errorf(
			"%w: %q",
			ErrInvalidLookupCategory,
			category,
		)
	}

	unique := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))

	for _, name := range names {
		if name == "" || domain.NormalizeName(name) != name {
			return nil, fmt.Errorf(
				"%w: %q",
				ErrInvalidLookupName,
				name,
			)
		}

		if _, exists := seen[name]; exists {
			continue
		}

		seen[name] = struct{}{}
		unique = append(unique, name)
	}

	out := make(map[string]Known, len(unique))

	for start := 0; start < len(unique); start += maxLookupNames {
		end := start + maxLookupNames
		if end > len(unique) {
			end = len(unique)
		}

		if err := db.lookupChunk(
			ctx,
			category,
			unique[start:end],
			out,
		); err != nil {
			return nil, err
		}
	}

	return out, nil
}

// lookupChunk 는 하나의 Category 안에서 maxLookupNames 이하의
// 이름 묶음 하나를 조회하여 out 에 합친다.
func (db *DB) lookupChunk(
	ctx context.Context,
	category domain.Category,
	names []string,
	out map[string]Known,
) error {
	placeholders := strings.TrimSuffix(
		strings.Repeat("?, ", len(names)),
		", ",
	)

	// 첫 번째 바인드 값은 WHERE c.category = ? 에 들어간다.
	// 그 뒤로 IN (...) 의 file_name 들이 순서대로 들어간다.
	args := make([]any, 0, len(names)+1)
	args = append(args, category.String())

	for _, name := range names {
		args = append(args, name)
	}

	rows, err := db.conn.QueryContext(
		ctx,
		fmt.Sprintf(lookupCommonSQL, placeholders),
		args...,
	)
	if err != nil {
		return fmt.Errorf(
			"ledger: lookup category=%s names=%d: %w",
			category,
			len(names),
			err,
		)
	}

	defer func() {
		// 읽기 전용 조회이므로 Close 실패가 데이터 상태를 변경하지 않는다.
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			k         Known
			origin    string
			state     string
			putStatus string
		)

		if err := rows.Scan(
			&k.FileName,
			&k.Revision,
			&k.Size,
			&k.MTime,
			&origin,
			&state,
			&k.ContentHash,
			&putStatus,
		); err != nil {
			return fmt.Errorf(
				"ledger: scan lookup row: %w",
				err,
			)
		}

		// DB 의 값은 CHECK 제약 및 domain 상수와 정확히 일치해야 한다.
		// 잘못된 값을 자동 보정하지 않는다.
		//
		// 여기서 어긋난다는 것은 스키마 세대 불일치나 외부 조작이므로
		// 조용히 받아들이지 않는다.
		k.Origin, err = domain.ParseOrigin(origin)
		if err != nil {
			return fmt.Errorf(
				"ledger: row %q: %w",
				k.FileName,
				err,
			)
		}

		k.State, err = domain.ParseState(state)
		if err != nil {
			return fmt.Errorf(
				"ledger: row %q: %w",
				k.FileName,
				err,
			)
		}

		// 빈 문자열은 이 Category/FileName 의 현재 revision 에
		// 전송 이력이 아직 없다는 사실이다.
		//
		// put_ledger 에 실제 상태값이 존재한다면 domain.Status 의
		// 유효값이어야 한다.
		if putStatus != "" {
			k.PutStatus, err = domain.ParseStatus(putStatus)
			if err != nil {
				return fmt.Errorf(
					"ledger: row %q: %w",
					k.FileName,
					err,
				)
			}
		}

		out[k.FileName] = k
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf(
			"ledger: iterate lookup rows: %w",
			err,
		)
	}

	return nil
}
