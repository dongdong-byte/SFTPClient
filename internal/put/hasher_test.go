package put

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestDefaultHasher_FactsMatchDisk 는 defaultHasher 가 돌려주는 사실
// (지문·바이트·전후 stat)이 디스크 실물과 일치함을 고정한다.
func TestDefaultHasher_FactsMatchDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dbon2500.26o.gz")
	payload := []byte("rinex observation payload for hashing\n")

	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	hr, err := defaultHasher{}.HashFile(context.Background(), path)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}

	sum := sha256.Sum256(payload)
	if want := hex.EncodeToString(sum[:]); hr.Hash != want {
		t.Errorf("Hash = %s, want %s", hr.Hash, want)
	}

	if hr.Bytes != int64(len(payload)) {
		t.Errorf("Bytes = %d, want %d", hr.Bytes, len(payload))
	}

	wantMTime := info.ModTime().UTC().Unix()
	if hr.PreSize != info.Size() || hr.PostSize != info.Size() ||
		hr.PreMTime != wantMTime || hr.PostMTime != wantMTime {
		t.Errorf("stat 사실 불일치: %+v (disk size=%d mtime=%d)",
			hr, info.Size(), wantMTime)
	}
}

// TestDefaultHasher_ContextCancelStopsRead 는 취소된 ctx 가 읽기를
// 중단시킴을 고정한다 — 순서 3(watchdog) 연결 지점의 계약이다.
func TestDefaultHasher_ContextCancelStopsRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.gz")

	if err := os.WriteFile(path, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := (defaultHasher{}).HashFile(ctx, path); err == nil {
		t.Fatal("취소된 ctx 로 해시가 완주했다")
	}
}

// TestHashStable_JudgesRunnerSide 는 안정성 판정표를 고정한다 —
// Pre≠Post(계산 중 변경)와 Pre≠스캔 관측치(stale) 각각이 불안정이다.
// (설계 v3 §1.3: Hasher 는 사실, Runner 는 판정)
func TestHashStable_JudgesRunnerSide(t *testing.T) {
	base := HashResult{
		PreSize: 100, PreMTime: 1000,
		PostSize: 100, PostMTime: 1000,
	}

	cases := []struct {
		name      string
		mutate    func(*HashResult)
		scanSize  int64
		scanMTime int64
		want      bool
	}{
		{"안정", func(*HashResult) {}, 100, 1000, true},
		{"계산 중 size 변경", func(h *HashResult) { h.PostSize = 101 }, 100, 1000, false},
		{"계산 중 mtime 변경", func(h *HashResult) { h.PostMTime = 1001 }, 100, 1000, false},
		{"스캔 대비 size stale", func(*HashResult) {}, 99, 1000, false},
		{"스캔 대비 mtime stale", func(*HashResult) {}, 100, 999, false},
	}

	for _, c := range cases {
		hr := base
		c.mutate(&hr)

		if got := hashStable(hr, c.scanSize, c.scanMTime); got != c.want {
			t.Errorf("%s: hashStable = %v, want %v", c.name, got, c.want)
		}
	}
}

// stubHasher 는 판정 경로 테스트용 스텁이다 — 호출 횟수를 세어
// "MetadataChangedOnly 처리 후 반복 해시 없음"(H4) 류의 계약을
// runner 통합 테스트에서 검증할 수 있게 한다.
type stubHasher struct {
	calls  int
	result HashResult
	err    error
}

func (s *stubHasher) HashFile(
	_ context.Context,
	_ string,
) (HashResult, error) {
	s.calls++

	return s.result, s.err
}
