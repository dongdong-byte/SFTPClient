package verify

import (
	"testing"
	"time"

	"SFTPClient/internal/domain"
)

// now 는 테스트의 고정 현재 시각이다. 실제 시계에 의존하지 않는다.
var now = time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

// verifier 는 Grace 60초, 고정 시계의 판정기를 만든다.
func verifier(grace time.Duration) Verifier {
	return Verifier{
		Grace: grace,
		Now:   func() time.Time { return now },
	}
}

// okInput 은 모든 판정을 통과하는 기준 입력이다.
// 각 테스트는 여기서 한 가지씩만 어긋나게 만들어, 실패 원인이
// 의도한 판정 하나임을 보장한다.
func okInput() Input {
	return Input{
		Name:     "SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz",
		Size:     123456,
		MTime:    now.Add(-10 * time.Minute),
		IsDir:    false,
		Category: domain.CategoryRINEX3Hourly,
	}
}

func TestVerify(t *testing.T) {
	const grace = 60 * time.Second

	tests := []struct {
		name   string
		mutate func(in *Input)
		want   Reason
	}{
		{
			name:   "모든 판정 통과",
			mutate: func(in *Input) {},
			want:   ReasonNone,
		},
		{
			name:   "디렉터리는 거부",
			mutate: func(in *Input) { in.IsDir = true },
			want:   ReasonIsDir,
		},
		{
			name: "part 임시 파일은 거부",
			mutate: func(in *Input) {
				in.Name = in.Name + ".part"
			},
			want: ReasonPartFile,
		},
		{
			name: "대문자 PART 도 거부",
			mutate: func(in *Input) {
				in.Name = in.Name + ".PART"
			},
			want: ReasonPartFile,
		},
		{
			name: "Grace 를 넘는 미래 mtime 은 시계 이상",
			mutate: func(in *Input) {
				in.MTime = now.Add(grace + time.Second)
			},
			want: ReasonFutureMTime,
		},
		{
			name: "Grace 이내의 미래는 방금 쓰임으로 흡수한다",
			// NAS 와 서버의 수 초 어긋남. future= 를 오염시키지 않는다.
			mutate: func(in *Input) {
				in.MTime = now.Add(2 * time.Second)
			},
			want: ReasonTooRecent,
		},
		{
			name: "방금 쓰인 파일은 대기",
			mutate: func(in *Input) {
				in.MTime = now.Add(-30 * time.Second)
			},
			want: ReasonTooRecent,
		},
		{
			name: "나이가 정확히 Grace 면 아직 대기",
			mutate: func(in *Input) {
				in.MTime = now.Add(-grace)
			},
			want: ReasonTooRecent,
		},
		{
			name: "나이가 Grace 를 1초라도 넘으면 통과",
			mutate: func(in *Input) {
				in.MTime = now.Add(-grace - time.Second)
			},
			want: ReasonNone,
		},
		{
			name:   "Grace 를 지난 0바이트만 zero 로 남는다",
			mutate: func(in *Input) { in.Size = 0 },
			want:   ReasonZeroSize,
		},
		{
			name:   "음수 크기는 0바이트와 같이 다룬다",
			mutate: func(in *Input) { in.Size = -1 },
			want:   ReasonZeroSize,
		},
		{
			name: "Category 모순은 거부 (Hourly 폴더의 Daily 파일)",
			mutate: func(in *Input) {
				in.Name = "gumc00kor_r_20260040000_01d_mn.rnx.gz"
			},
			want: ReasonCategoryMismatch,
		},
		{
			name: "혼입 실사례: RINEX2 폴더의 RINEX3 파일",
			mutate: func(in *Input) {
				in.Category = domain.CategoryRINEX2Daily
			},
			want: ReasonCategoryMismatch,
		},
		{
			name: "판정 근거가 없는 이름은 통과 (Unknown)",
			mutate: func(in *Input) {
				in.Name = "yons060.20m"
			},
			want: ReasonNone,
		},
		{
			name: "RINEX2 짧은 이름 일치는 통과",
			mutate: func(in *Input) {
				in.Name = "SONP001A.26O.gz"
				in.Category = domain.CategoryRINEX2Hourly
			},
			want: ReasonNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := okInput()
			tt.mutate(&in)

			if got := verifier(grace).Verify(in); got != tt.want {
				t.Errorf("Verify(%+v) = %v, want %v", in, got, tt.want)
			}
		})
	}
}

// 순서가 곧 진단이다. 한 입력이 여러 판정에 걸릴 때 어느 Reason 으로
// 집계되는지를 고정한다. 순서가 바뀌면 요약 로그의 의미가 바뀐다.
func TestVerifyOrder(t *testing.T) {
	const grace = 60 * time.Second

	tests := []struct {
		name   string
		mutate func(in *Input)
		want   Reason
	}{
		{
			name: "디렉터리 + 0바이트 → dir 이 먼저",
			mutate: func(in *Input) {
				in.IsDir = true
				in.Size = 0
			},
			want: ReasonIsDir,
		},
		{
			name: "part + 0바이트 + 방금 쓰임 → part 가 먼저",
			mutate: func(in *Input) {
				in.Name = in.Name + ".part"
				in.Size = 0
				in.MTime = now
			},
			want: ReasonPartFile,
		},
		{
			name: "미래 mtime + 0바이트 → future 가 먼저 (grace 에 섞이지 않는다)",
			mutate: func(in *Input) {
				in.MTime = now.Add(time.Hour)
				in.Size = 0
			},
			want: ReasonFutureMTime,
		},
		{
			// 0바이트의 실원인은 "채워지는 중"이므로 zero= 가 아니라
			// grace= 로 집계되어야 진단이 맞다. (VERIFY DESIGN 2절)
			name: "방금 쓰인 0바이트 → grace 로 집계",
			mutate: func(in *Input) {
				in.MTime = now
				in.Size = 0
			},
			want: ReasonTooRecent,
		},
		{
			name: "방금 쓰인 Mismatch → grace 가 먼저",
			mutate: func(in *Input) {
				in.MTime = now
				in.Name = "gumc00kor_r_20260040000_01d_mn.rnx.gz"
			},
			want: ReasonTooRecent,
		},
		{
			name: "0바이트 Mismatch → zero 가 먼저",
			mutate: func(in *Input) {
				in.Size = 0
				in.Name = "gumc00kor_r_20260040000_01d_mn.rnx.gz"
			},
			want: ReasonZeroSize,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := okInput()
			tt.mutate(&in)

			if got := verifier(grace).Verify(in); got != tt.want {
				t.Errorf("Verify(%+v) = %v, want %v", in, got, tt.want)
			}
		})
	}
}

// Grace = 0 은 mtime 나이 검사(Future 포함)를 끄는 계약이다.
// config.IngressConfig.Grace 주석과 같다.
func TestVerifyGraceDisabled(t *testing.T) {
	v := verifier(0)

	t.Run("방금 쓰인 파일도 통과한다", func(t *testing.T) {
		in := okInput()
		in.MTime = now

		if got := v.Verify(in); got != ReasonNone {
			t.Errorf("Verify() = %v, want None", got)
		}
	})

	t.Run("미래 mtime 도 검사하지 않는다", func(t *testing.T) {
		in := okInput()
		in.MTime = now.Add(24 * time.Hour)

		if got := v.Verify(in); got != ReasonNone {
			t.Errorf("Verify() = %v, want None", got)
		}
	})

	t.Run("0바이트는 여전히 거부한다", func(t *testing.T) {
		in := okInput()
		in.Size = 0

		if got := v.Verify(in); got != ReasonZeroSize {
			t.Errorf("Verify() = %v, want ZeroSize", got)
		}
	})

	t.Run("음수 Grace 는 0 과 같이 다룬다", func(t *testing.T) {
		in := okInput()
		in.MTime = now

		if got := verifier(-time.Minute).Verify(in); got != ReasonNone {
			t.Errorf("Verify() = %v, want None", got)
		}
	})
}

// Now 가 nil 이면 time.Now 를 쓴다. 실제 시계로도 판정이 성립하는지만 본다.
func TestVerifyNilNow(t *testing.T) {
	v := Verifier{Grace: 60 * time.Second}

	in := okInput()
	in.MTime = time.Now().Add(-time.Hour)

	if got := v.Verify(in); got != ReasonNone {
		t.Errorf("Verify() = %v, want None", got)
	}

	in.MTime = time.Now()

	if got := v.Verify(in); got != ReasonTooRecent {
		t.Errorf("Verify() = %v, want TooRecent", got)
	}
}

func TestReasonOK(t *testing.T) {
	if !ReasonNone.OK() {
		t.Error("ReasonNone.OK() = false")
	}

	for _, r := range []Reason{
		ReasonIsDir,
		ReasonPartFile,
		ReasonFutureMTime,
		ReasonTooRecent,
		ReasonZeroSize,
		ReasonCategoryMismatch,
	} {
		if r.OK() {
			t.Errorf("%v.OK() = true", r)
		}
	}
}

// String 은 요약 로그의 key=count 집계 키다. 값이 바뀌면 로그 형식이 바뀐다.
func TestReasonString(t *testing.T) {
	tests := []struct {
		in   Reason
		want string
	}{
		{ReasonNone, "none"},
		{ReasonIsDir, "dir"},
		{ReasonPartFile, "part"},
		{ReasonFutureMTime, "future"},
		{ReasonTooRecent, "grace"},
		{ReasonZeroSize, "zero"},
		{ReasonCategoryMismatch, "mismatch"},
		{Reason(99), "reason(99)"},
	}

	for _, tt := range tests {
		if got := tt.in.String(); got != tt.want {
			t.Errorf("Reason(%d).String() = %q, want %q", int(tt.in), got, tt.want)
		}
	}
}

// 제로값 Verifier{} 는 Grace=0(나이 검사 꺼짐), Now=nil(실제 시계)로
// 그대로 유효하다. 값 리시버 선택의 근거이므로 고정한다.
func TestVerifierZeroValue(t *testing.T) {
	var v Verifier

	in := okInput()
	in.MTime = time.Now() // 방금 쓰였어도 Grace=0 이면 통과

	if got := v.Verify(in); got != ReasonNone {
		t.Errorf("Verifier{}.Verify() = %v, want None", got)
	}
}
