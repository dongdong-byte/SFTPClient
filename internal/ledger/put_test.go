package ledger

// ---------------------------------------------------------------------------
// 아래 두 테스트는 put_test.go 끝에 붙인다.
// ---------------------------------------------------------------------------

// seedPutCommons 는 common_ledger 에 n 개의 행을 만들고 PutKey 를 돌려준다.
//
// putTestCommon 은 파일 하나당 LookupCommon 을 한 번씩 부르므로
// 대량 케이스에서는 왕복이 지나치게 많다. 여기서는 Upsert 를 모두 마친 뒤
// LookupCommon 한 번으로 revision 을 읽는다.
func seedPutCommons(
	t *testing.T,
	db *DB,
	category domain.Category,
	names []string,
) []PutKey {
	t.Helper()

	ctx := context.Background()

	for _, name := range names {
		if _, err := db.UpsertCommon(ctx, CommonInput{
			FileName:          name,
			BaseName:          domain.BaseName(name),
			Category:          category,
			Size:              1_024,
			MTime:             1_700_000_100,
			Origin:            domain.OriginLocal,
			IngressVerifiedAt: 1_700_000_200,
		}); err != nil {
			t.Fatalf("UpsertCommon(%q) 실패: %v", name, err)
		}
	}

	known, err := db.LookupCommon(ctx, category, names)
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	keys := make([]PutKey, 0, len(names))

	for _, name := range names {
		row, ok := known[name]
		if !ok {
			t.Fatalf("common_ledger 에 등록한 파일이 조회되지 않았다: %q", name)
		}

		keys = append(keys, PutKey{
			Category: category,
			FileName: name,
			Revision: row.Revision,
		})
	}

	return keys
}

// LookupPut 은 입력이 청크 상한을 넘으면 여러 번의 조회로 나눈다.
//
// 이 분할 경로는 청크 상한 이하의 입력에서는 한 번도 실행되지 않으므로,
// 다른 테스트가 모두 통과해도 다음 결함이 그대로 남는다.
//
//	슬라이스 경계 오류      마지막 청크에서 범위 초과 또는 1건 누락
//	out 을 청크마다 재생성  마지막 청크 결과만 남음
//
// 결과에서 빠진 키는 호출자에게 "장부에 이력이 없다 = 신규" 로 보인다.
// 이미 VERIFIED 인 파일이 후보로 되살아나 재전송되며, 오류도 로그도 남지 않는다.
//
// 실제 LookupPut 의 청크 상한인 maxPutLookupKeys + 1 을 사용하여
// 둘째 청크에 정확히 1건만 들어가는 가장 얇은 마지막 청크를 만든다.
func TestLookupPutSplitsIntoChunks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cat := domain.CategoryRINEX3Hourly

	const total = maxPutLookupKeys + 1

	names := make([]string, 0, total)
	for i := range total {
		names = append(names, fmt.Sprintf("chunk%04d.rnx.gz", i))
	}

	keys := seedPutCommons(t, db, cat, names)

	if err := db.InsertPendingBatch(ctx, keys); err != nil {
		t.Fatalf("InsertPendingBatch() 실패: %v", err)
	}

	// 둘째 청크의 유일한 항목을 FAILED 로 바꾼다.
	// 첫 청크 결과와 둘째 청크 결과가 실제로 합쳐지는지 확인하기 위함이다.
	last := keys[total-1]

	if err := db.BeginPut(
		ctx,
		last,
		"/remote/last",
		"/remote/last.part",
		1_024,
		5,
	); err != nil {
		t.Fatalf("BeginPut(last) 실패: %v", err)
	}

	if err := db.FailPut(ctx, last, "boundary"); err != nil {
		t.Fatalf("FailPut(last) 실패: %v", err)
	}

	lookup := make([]NameRev, 0, total)
	for _, key := range keys {
		lookup = append(lookup, NameRev{
			FileName: key.FileName,
			Revision: key.Revision,
		})
	}

	got, err := db.LookupPut(ctx, cat, lookup)
	if err != nil {
		t.Fatalf("LookupPut() 실패: %v", err)
	}

	if len(got) != total {
		t.Fatalf("LookupPut 결과 수 = %d, want %d", len(got), total)
	}

	// 첫 청크 시작 / 첫 청크 끝 / 둘째 청크 시작.
	// off-by-one 결함은 이 세 경계를 보면 드러난다.
	for _, i := range []int{
		0,
		maxPutLookupKeys - 1,
		maxPutLookupKeys,
	} {
		key := keys[i]
		nr := NameRev{
			FileName: key.FileName,
			Revision: key.Revision,
		}

		state, ok := got[nr]
		if !ok {
			t.Fatalf("index %d (%q) 가 결과에서 누락되었다", i, key.FileName)
		}

		want := PutState{
			Status:   domain.StatusPending,
			Attempts: 0,
		}

		if i == total-1 {
			want = PutState{
				Status:   domain.StatusFailed,
				Attempts: 1,
			}
		}

		if state != want {
			t.Errorf(
				"index %d (%q) state = %+v, want %+v",
				i,
				key.FileName,
				state,
				want,
			)
		}
	}
}

// 누적 시도 상한을 받아들인 근거는 "파일이 바뀌면 예산이 새로 생긴다" 였다.
//
// put_ledger 의 PK 가 (category, file_name, revision) 이므로 revision 이
// 오르면 attempts 0 인 새 행이 생긴다. 이 성질이 깨지면 한 번 소진된 파일은
// 내용이 바뀌어도 영구히 전송되지 않는다.
//
// 함께 확인하는 것:
//
//	UpsertCommon 이 size 변경으로 revision 을 올리는가
//	새 revision 의 attempts 가 0에서 다시 시작하는가
//	이전 revision 의 전송 이력이 그대로 보존되는가
func TestBeginPutBudgetResetsOnNewRevision(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cat := domain.CategoryRINEX3Hourly

	const (
		name       = "reset001.rnx.gz"
		maxRetries = 3
	)

	rev1 := putTestCommon(t, db, cat, name)

	if err := db.InsertPendingBatch(ctx, []PutKey{rev1}); err != nil {
		t.Fatalf("InsertPendingBatch(rev1) 실패: %v", err)
	}

	// rev1 의 자동 시도 예산을 전부 소진시킨다.
	for attempt := int64(1); attempt <= maxRetries; attempt++ {
		if err := db.BeginPut(
			ctx,
			rev1,
			"/remote/reset001.rnx.gz",
			"/remote/reset001.rnx.gz.part",
			1_024,
			maxRetries,
		); err != nil {
			t.Fatalf("BeginPut(rev1) attempt=%d 실패: %v", attempt, err)
		}

		if err := db.FailPut(ctx, rev1, "temporary failure"); err != nil {
			t.Fatalf("FailPut(rev1) attempt=%d 실패: %v", attempt, err)
		}
	}

	if err := db.BeginPut(
		ctx,
		rev1,
		"/remote/reset001.rnx.gz",
		"/remote/reset001.rnx.gz.part",
		1_024,
		maxRetries,
	); !errors.Is(err, ErrNotCandidate) {
		t.Fatalf("소진 후 BeginPut(rev1) = %v, want ErrNotCandidate", err)
	}

	// 파일 내용이 바뀌었다. size 변경만으로 revision 이 올라야 한다.
	if _, err := db.UpsertCommon(ctx, CommonInput{
		FileName:          name,
		BaseName:          domain.BaseName(name),
		Category:          cat,
		Size:              2_048,
		MTime:             1_700_000_100,
		Origin:            domain.OriginLocal,
		IngressVerifiedAt: 1_700_000_300,
	}); err != nil {
		t.Fatalf("UpsertCommon(size 변경) 실패: %v", err)
	}

	known, err := db.LookupCommon(ctx, cat, []string{name})
	if err != nil {
		t.Fatalf("LookupCommon() 실패: %v", err)
	}

	row, ok := known[name]
	if !ok {
		t.Fatalf("common_ledger 에서 %q 가 조회되지 않았다", name)
	}

	if row.Revision <= rev1.Revision {
		t.Fatalf(
			"size 변경 후 revision = %d, want > %d",
			row.Revision,
			rev1.Revision,
		)
	}

	rev2 := PutKey{
		Category: cat,
		FileName: name,
		Revision: row.Revision,
	}

	if err := db.InsertPendingBatch(ctx, []PutKey{rev2}); err != nil {
		t.Fatalf("InsertPendingBatch(rev2) 실패: %v", err)
	}

	// 이전 revision 과 새 revision 이 동시에 남아 있어야 한다.
	if got := countPut(t, db); got != 2 {
		t.Fatalf("put_ledger 행 수 = %d, want 2", got)
	}

	if err := db.BeginPut(
		ctx,
		rev2,
		"/remote/reset001.rnx.gz",
		"/remote/reset001.rnx.gz.part",
		2_048,
		maxRetries,
	); err != nil {
		t.Fatalf("BeginPut(rev2) 실패: %v", err)
	}

	newRow := readPut(t, db, rev2)

	if newRow.status != string(domain.StatusInProgress) {
		t.Errorf("rev2 status = %q, want IN_PROGRESS", newRow.status)
	}
	if newRow.attempts != 1 {
		t.Errorf("rev2 attempts = %d, want 1 (예산이 리셋되지 않았다)", newRow.attempts)
	}

	// put_ledger 는 과거 사실의 기록이므로 rev1 이 보존되어야 한다.
	oldRow := readPut(t, db, rev1)

	if oldRow.status != string(domain.StatusFailed) {
		t.Errorf("rev1 status = %q, want FAILED", oldRow.status)
	}
	if oldRow.attempts != maxRetries {
		t.Errorf(
			"rev1 attempts = %d, want %d",
			oldRow.attempts,
			maxRetries,
		)
	}
}
