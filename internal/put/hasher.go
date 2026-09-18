package put

// 이 파일은 내용 지문(SHA-256) 계산기를 담는다. (UNIT2 설계 v3 §1.3, §4)
//
// Hasher 는 사실만 돌려준다 — 계산 전후의 동일 핸들 stat 과 실제 읽은
// 바이트. "이 관측이 stale 인가"의 판정은 Runner 가 한다 (hashStable).
// 장부가 기록만 하고 판정하지 않는 것과 같은 계열의 분리다.
// Hasher 가 스캔 관측치를 입력받아 스스로 판정하는 안은 기각되었다 —
// 관측기가 판정을 소유하면 통계의 주인이 둘이 되고, 판정 규칙이 바뀔
// 때마다 인터페이스가 흔들린다 (설계 v3 §1.3 기각 기록).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// HashResult 는 해시 계산의 관측 사실이다. 판정하지 않는다.
type HashResult struct {
	// Hash 는 파일 전체 바이트의 SHA-256, hex 소문자 64자다.
	Hash string

	// Pre*/Post* 는 계산 직전·직후 동일 핸들 stat 이다. Unix 초, UTC.
	PreSize   int64
	PreMTime  int64
	PostSize  int64
	PostMTime int64

	// Bytes 는 실제 읽은 바이트 수다 (계측용).
	Bytes int64
}

// Hasher 는 파일 하나의 내용 지문을 계산한다.
//
// ctx 는 현재 취소원이 시그널뿐이지만, 순서 3(watchdog)이 취소를
// 도입할 때 시그니처 변경 없이 연결되도록 지금 받는다 (설계 v3 §1.3).
type Hasher interface {
	HashFile(ctx context.Context, path string) (HashResult, error)
}

// defaultHasher 는 스트리밍 SHA-256 구현이다. Runner.Hasher 가 nil 이면
// 이것이 쓰인다 (Logger 의 nil → log.Default() 관행과 동일).
//
// 별도 패키지(internal/hash)는 기각 — 소비자가 put 하나뿐이며, 두 번째
// 소비자가 생기는 날 옮기면 된다 (설계 v3 §4).
type defaultHasher struct{}

func (defaultHasher) HashFile(
	ctx context.Context,
	path string,
) (HashResult, error) {
	var hr HashResult

	f, err := os.Open(path)
	if err != nil {
		return hr, fmt.Errorf("hash open: %w", err)
	}

	defer func() {
		// 읽기 전용이므로 Close 실패가 데이터 상태를 바꾸지 않는다.
		_ = f.Close()
	}()

	pre, err := f.Stat()
	if err != nil {
		return hr, fmt.Errorf("hash stat(pre): %w", err)
	}

	hr.PreSize = pre.Size()
	hr.PreMTime = pre.ModTime().UTC().Unix()

	h := sha256.New()

	n, err := io.Copy(h, &ctxReader{ctx: ctx, r: f})
	hr.Bytes = n
	if err != nil {
		return hr, fmt.Errorf("hash read: %w", err)
	}

	post, err := f.Stat()
	if err != nil {
		return hr, fmt.Errorf("hash stat(post): %w", err)
	}

	hr.PostSize = post.Size()
	hr.PostMTime = post.ModTime().UTC().Unix()

	hr.Hash = hex.EncodeToString(h.Sum(nil))

	return hr, nil
}

// ctxReader 는 Read 마다 ctx 취소를 확인한다. 대파일 해시 도중 종료
// 신호가 와도 한 청크(io.Copy 기본 32KB) 안에서 멈춘다.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}

	return c.r.Read(p)
}

// hashStable 은 해시가 판정에 쓸 수 있는 안정 관측인지 Runner 관점에서
// 판정한다 (설계 v3 §1.3·§1.4).
//
//	Pre != Post          — 계산 도중 파일이 변했다.
//	Pre != 스캔 관측치   — 스캔과 해시 사이에 이미 변했다 (stale).
//
// 어느 쪽이든 "파일이 지금 변하고 있다"이며, Ingress 의 작성 중 보류와
// 동일 의미론으로 이번 회차를 보류한다. 판정 함수를 Runner 쪽 파일에
// 두는 이유가 이 분리다 — Hasher 는 사실, Runner 는 판정.
func hashStable(hr HashResult, scanSize, scanMTime int64) bool {
	return hr.PreSize == hr.PostSize &&
		hr.PreMTime == hr.PostMTime &&
		hr.PreSize == scanSize &&
		hr.PreMTime == scanMTime
}

// hashFile 은 Hasher 호출을 계측(건수·바이트·소요시간)과 함께 감싼다.
// stats 는 해시 원인 4분류(신규/변경/드리프트/백필) 중 호출자가 지정한
// 하나다 (설계 v3 §6).
func (r *Runner) hashFile(
	ctx context.Context,
	path string,
	stats *HashStats,
) (HashResult, error) {
	h := r.Hasher
	if h == nil {
		h = defaultHasher{}
	}

	started := r.now()
	hr, err := h.HashFile(ctx, path)

	stats.Files++
	stats.Bytes += hr.Bytes
	stats.Millis += r.now().Sub(started).Milliseconds()

	return hr, err
}
