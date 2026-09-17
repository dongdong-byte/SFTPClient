package ledger

import (
	"context"
	"errors"
	"testing"

	"SFTPClient/internal/domain"
)

const testHashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
	"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const testHashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" +
	"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// seedKnown 은 in 을 Upsert 하고 LookupCommon 이 돌려준 Known 을 준다 —
// "판정에 사용한 사실 그대로"라는 두 함수의 입력 계약을 테스트도
// 같은 경로로 만족시킨다.
func seedKnown(t *testing.T, db *DB, in CommonInput) Known {
	t.Helper()

	upsert(t, db, in)

	known, err := db.LookupCommon(
		context.Background(),
		in.Category,
		[]string{in.FileName},
	)
	if err != nil {
		t.Fatalf("LookupCommon 실패: %v", err)
	}

	k, ok := known[in.FileName]
	if !ok {
		t.Fatalf("시드 행이 조회되지 않음: %s", in.FileName)
	}

	return k
}

// TestTouchCommonMTime_UpdatesBaselineOnly 는 mtime 만 갱신되고
// revision·state·지문·ingress_verified_at 이 보존됨을 고정한다. (H13)
func TestTouchCommonMTime_UpdatesBaselineOnly(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	in := newInput()
	in.ContentHash = testHashA
	k := seedKnown(t, db, in)

	before := readCommon(t, db, in.FileName)

	newMTime := k.MTime + 3600
	applied, err := db.TouchCommonMTime(ctx, in.Category, k, newMTime)
	if err != nil {
		t.Fatalf("TouchCommonMTime: %v", err)
	}

	if !applied {
		t.Fatal("판정 근거가 그대로인데 applied=false")
	}

	after := readCommon(t, db, in.FileName)

	if after.mtime != newMTime {
		t.Errorf("mtime = %d, want %d", after.mtime, newMTime)
	}

	if after.revision != before.revision ||
		after.state != before.state ||
		after.ingressVerifiedAt != before.ingressVerifiedAt ||
		after.firstSeen != before.firstSeen {
		t.Errorf("mtime 외의 사실이 변함: before=%+v after=%+v", before, after)
	}

	known, _ := db.LookupCommon(ctx, in.Category, []string{in.FileName})
	if h := known[in.FileName].ContentHash; h != testHashA {
		t.Errorf("지문이 변함: %q", h)
	}
}

// TestTouchCommonMTime_GuardRejectsStaleJudgement 는 판정 근거
// (revision·size·mtime·지문) 어느 하나라도 현재 행과 다르면 0행으로
// 끝나고 행이 보존됨을 고정한다. (H9 + §2.2 전체 가드)
func TestTouchCommonMTime_GuardRejectsStaleJudgement(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	in := newInput()
	in.ContentHash = testHashA
	fresh := seedKnown(t, db, in)

	stale := map[string]Known{
		"revision": func() Known { k := fresh; k.Revision++; return k }(),
		"size":     func() Known { k := fresh; k.Size++; return k }(),
		"mtime":    func() Known { k := fresh; k.MTime++; return k }(),
		"hash":     func() Known { k := fresh; k.ContentHash = testHashB; return k }(),
	}

	for label, k := range stale {
		applied, err := db.TouchCommonMTime(ctx, in.Category, k, fresh.MTime+7200)
		if err != nil {
			t.Fatalf("%s: 예기치 못한 오류: %v", label, err)
		}

		if applied {
			t.Errorf("%s 불일치인데 applied=true — 가드 누락", label)
		}
	}

	after := readCommon(t, db, in.FileName)
	if after.mtime != fresh.MTime {
		t.Errorf("가드 불일치 시도 후 mtime 이 변함: %d", after.mtime)
	}
}

// TestTouchCommonMTime_InputContract 는 입력 규약 위반이 DB 도달 전에
// 거부됨을 고정한다 — 특히 "드리프트 없는 Touch"는 판정 자체가 성립하지
// 않으므로 오류다.
func TestTouchCommonMTime_InputContract(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	in := newInput()
	in.ContentHash = testHashA
	k := seedKnown(t, db, in)

	cases := map[string]func() (bool, error){
		"기준 지문 없음": func() (bool, error) {
			bad := k
			bad.ContentHash = ""
			return db.TouchCommonMTime(ctx, in.Category, bad, k.MTime+1)
		},
		"newMTime 미설정": func() (bool, error) {
			return db.TouchCommonMTime(ctx, in.Category, k, 0)
		},
		"드리프트 없음": func() (bool, error) {
			return db.TouchCommonMTime(ctx, in.Category, k, k.MTime)
		},
		"비정규화 이름": func() (bool, error) {
			bad := k
			bad.FileName = "UPPER.GZ"
			return db.TouchCommonMTime(ctx, in.Category, bad, k.MTime+1)
		},
		"잘못된 카테고리": func() (bool, error) {
			return db.TouchCommonMTime(ctx, domain.Category("rinex2_daily"), k, k.MTime+1)
		},
	}

	for label, fn := range cases {
		if _, err := fn(); !errors.Is(err, ErrInvalidRefreshInput) {
			t.Errorf("%s: ErrInvalidRefreshInput 이 아님: %v", label, err)
		}
	}
}

// TestSetContentHash_BackfillFillsEmptyOnce 는 지문 없는 행이 채워지고,
// 판정과 저장 사이에 행이 변한 경우(같은 stale Known 재사용) 0행으로
// 무해하게 끝남을 고정한다. (H6)
func TestSetContentHash_BackfillFillsEmptyOnce(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	in := newInput() // ContentHash 미지정 = '' (지문 없는 v5 행과 동형)
	k := seedKnown(t, db, in)

	if k.ContentHash != "" {
		t.Fatalf("시드 지문이 비어 있지 않음: %q", k.ContentHash)
	}

	applied, err := db.SetContentHash(ctx, in.Category, k, testHashA)
	if err != nil {
		t.Fatalf("SetContentHash: %v", err)
	}

	if !applied {
		t.Fatal("지문 없는 행 백필인데 applied=false")
	}

	known, _ := db.LookupCommon(ctx, in.Category, []string{in.FileName})
	if h := known[in.FileName].ContentHash; h != testHashA {
		t.Fatalf("백필 지문 = %q, want %q", h, testHashA)
	}

	// 같은 판정(k.ContentHash='')을 다시 저장 — 그 사이 행이 변했으므로
	// WHERE content_hash='' 가드가 0행으로 접는다. 오류가 아니다.
	applied, err = db.SetContentHash(ctx, in.Category, k, testHashB)
	if err != nil {
		t.Fatalf("stale 백필: 예기치 못한 오류: %v", err)
	}

	if applied {
		t.Fatal("이미 지문 있는 행에 stale 백필이 적용됨 — 가드 누락")
	}

	known, _ = db.LookupCommon(ctx, in.Category, []string{in.FileName})
	if h := known[in.FileName].ContentHash; h != testHashA {
		t.Errorf("stale 백필이 지문을 덮음: %q", h)
	}
}

// TestSetContentHash_GuardRejectsStaleSizeMTime 는 size·mtime 가드를
// 고정한다 — "size=·mtime= 이면 내용 동일"의 안전 근거가 SQL 에 있다.
func TestSetContentHash_GuardRejectsStaleSizeMTime(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	in := newInput()
	fresh := seedKnown(t, db, in)

	for label, k := range map[string]Known{
		"size":  func() Known { k := fresh; k.Size++; return k }(),
		"mtime": func() Known { k := fresh; k.MTime++; return k }(),
	} {
		applied, err := db.SetContentHash(ctx, in.Category, k, testHashA)
		if err != nil {
			t.Fatalf("%s: 예기치 못한 오류: %v", label, err)
		}

		if applied {
			t.Errorf("%s 불일치인데 백필 적용 — 안전 근거 붕괴", label)
		}
	}
}

// TestSetContentHash_MisuseIsLoud 는 오용(지문 있는 행 대상, 무효 지문)이
// 0행이 아니라 오류로 거부됨을 고정한다.
func TestSetContentHash_MisuseIsLoud(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	in := newInput()
	in.ContentHash = testHashA
	k := seedKnown(t, db, in) // k.ContentHash = testHashA

	if _, err := db.SetContentHash(ctx, in.Category, k, testHashB); !errors.Is(err, ErrInvalidRefreshInput) {
		t.Errorf("지문 있는 행 백필이 오류가 아님: %v", err)
	}

	empty := k
	empty.ContentHash = ""

	for label, h := range map[string]string{
		"빈 지문": "",
		"63자":  testHashA[:63],
		"대문자":  "A" + testHashA[1:],
	} {
		if _, err := db.SetContentHash(ctx, in.Category, empty, h); !errors.Is(err, ErrInvalidRefreshInput) {
			t.Errorf("%s 이 오류가 아님: %v", label, err)
		}
	}
}
