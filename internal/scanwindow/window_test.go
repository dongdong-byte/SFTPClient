package scanwindow

import (
	"errors"
	"testing"
	"time"

	"SFTPClient/internal/scan"
)

// 고정하는 계약 (v4 §3.3·§3.5, 검토 v1 §1.3·§3-5·§3-7):
//   - limit = today − (RetentionDays − 2), 자동·수동이 같은 함수를 쓴다.
//   - 자동 창 From = max(limit, origin), To = today − ScanDays, 양끝 포함.
//   - R = S+2 는 빈 창이 아니라 1일짜리 유효 창이다 (경계 정정 —
//     v4 §3.3 의 "이하" 서술은 "미만"으로 정정 대상).
//   - 빈 창 사유: 설정(R < S+2)이 운영 초기(origin)보다 먼저 판정된다.
//   - 첫 설치 후 D+0 ~ D+S−1 은 매일 EmptyBeforeOrigin, D+S 에 origin
//     당일 1일 창이 처음 열린다.
//   - 수동은 origin 무관, 세 위반은 각자의 센티널 오류.
//   - 모든 입력은 UTC 자정으로 내려 계산한다.

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func mustAuto(
	t *testing.T,
	today time.Time,
	origin time.Time,
	retentionDays int,
	scanDays int,
) AutoResult {
	t.Helper()

	got, err := Auto(today, origin, retentionDays, scanDays)
	if err != nil {
		t.Fatalf("Auto() 실패: %v", err)
	}

	return got
}

func TestRetentionLimit(t *testing.T) {
	today := day(2026, 9, 21)

	tests := []struct {
		name          string
		retentionDays int
		want          time.Time
	}{
		{"운영 권장 35 → 33일 전 (월 경계 포함)", 35, day(2026, 8, 19)},
		{"종전 기본 30 → 28일 전", 30, day(2026, 8, 24)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RetentionLimit(today, tt.retentionDays)
			if !got.Equal(tt.want) {
				t.Errorf(
					"limit = %s, want %s",
					got.Format(time.DateOnly),
					tt.want.Format(time.DateOnly),
				)
			}
		})
	}

	// 연도 전환: 2026-01-02 에서 35 일이면 2025-11-30.
	got := RetentionLimit(day(2026, 1, 2), 35)
	if want := day(2025, 11, 30); !got.Equal(want) {
		t.Errorf(
			"연도 전환 limit = %s, want %s",
			got.Format(time.DateOnly),
			want.Format(time.DateOnly),
		)
	}
}

func TestAuto(t *testing.T) {
	today := day(2026, 9, 21)
	oldOrigin := day(2020, 1, 1) // limit 보다 훨씬 과거 — 하한에 관여하지 않음

	tests := []struct {
		name          string
		origin        time.Time
		retentionDays int
		scanDays      int
		wantEmpty     bool
		wantWhy       EmptyReason
		wantFrom      time.Time
		wantTo        time.Time
	}{
		{
			name:   "정상 창 (R=35, S=7) — From=limit",
			origin: oldOrigin, retentionDays: 35, scanDays: 7,
			wantFrom: day(2026, 8, 19), wantTo: day(2026, 9, 14),
		},
		{
			name:   "origin 이 limit 과 To 사이 — From=origin (§3.5 하한)",
			origin: day(2026, 9, 1), retentionDays: 35, scanDays: 7,
			wantFrom: day(2026, 9, 1), wantTo: day(2026, 9, 14),
		},
		{
			name:   "origin == To — 당일 포함, 1일 창",
			origin: day(2026, 9, 14), retentionDays: 35, scanDays: 7,
			wantFrom: day(2026, 9, 14), wantTo: day(2026, 9, 14),
		},
		{
			name:   "origin > To — 운영 초기의 빈 창은 INFO 사유",
			origin: day(2026, 9, 15), retentionDays: 35, scanDays: 7,
			wantEmpty: true, wantWhy: EmptyBeforeOrigin,
		},
		{
			name:   "R = S+2 — 빈 창이 아니라 1일 유효 창 (경계 정정)",
			origin: oldOrigin, retentionDays: 9, scanDays: 7,
			wantFrom: day(2026, 9, 14), wantTo: day(2026, 9, 14),
		},
		{
			name:   "R = S+3 — 2일 창",
			origin: oldOrigin, retentionDays: 10, scanDays: 7,
			wantFrom: day(2026, 9, 13), wantTo: day(2026, 9, 14),
		},
		{
			name:   "R = S+1 — 설정이 자동 창을 허용하지 않음, WARN 사유",
			origin: oldOrigin, retentionDays: 8, scanDays: 7,
			wantEmpty: true, wantWhy: EmptyRetentionTooShort,
		},
		{
			name:   "두 원인 동시 — 설정 원인이 이긴다",
			origin: day(2026, 9, 20), retentionDays: 8, scanDays: 7,
			wantEmpty: true, wantWhy: EmptyRetentionTooShort,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mustAuto(t, today, tt.origin, tt.retentionDays, tt.scanDays)

			if got.Empty != tt.wantEmpty {
				t.Fatalf("Empty = %t, want %t (why=%s)", got.Empty, tt.wantEmpty, got.Why)
			}

			if got.Empty != (got.Why != EmptyNone) {
				t.Fatalf("Empty(%t) 와 Why(%s) 불일치", got.Empty, got.Why)
			}

			if tt.wantEmpty {
				if got.Why != tt.wantWhy {
					t.Errorf("Why = %s, want %s", got.Why, tt.wantWhy)
				}

				if !got.Range.From.IsZero() || !got.Range.To.IsZero() {
					t.Errorf("빈 창의 Range 가 zero 가 아니다: %+v", got.Range)
				}

				return
			}

			if !got.Range.From.Equal(tt.wantFrom) || !got.Range.To.Equal(tt.wantTo) {
				t.Errorf(
					"창 = [%s, %s], want [%s, %s]",
					got.Range.From.Format(time.DateOnly),
					got.Range.To.Format(time.DateOnly),
					tt.wantFrom.Format(time.DateOnly),
					tt.wantTo.Format(time.DateOnly),
				)
			}
		})
	}
}

// TestAuto_FirstInstallScenario — 첫 설치일 D 에 origin=D 로 기록된 뒤
// (커밋 4: 빈 DB = 오늘), today 만 진행시키며 창의 개폐를 본다.
// 몇 주 뒤 처음 운영을 시작한 빈 DB 도 origin=그날 이므로 같은 곡선이다.
func TestAuto_FirstInstallScenario(t *testing.T) {
	const retentionDays, scanDays = 35, 7

	origin := day(2026, 9, 1) // D

	// D+0 ~ D+S−1: 자동 창은 매일 비고, 사유는 항상 운영 초기다.
	for i := 0; i < scanDays; i++ {
		today := origin.AddDate(0, 0, i)
		got := mustAuto(t, today, origin, retentionDays, scanDays)

		if !got.Empty || got.Why != EmptyBeforeOrigin {
			t.Fatalf(
				"D+%d: Empty=%t Why=%s, want Empty=true Why=before_origin",
				i, got.Empty, got.Why,
			)
		}
	}

	// D+S: origin 당일 1일짜리 창이 처음 열린다.
	got := mustAuto(t, origin.AddDate(0, 0, scanDays), origin, retentionDays, scanDays)
	if got.Empty {
		t.Fatalf("D+%d 에 창이 열리지 않았다: why=%s", scanDays, got.Why)
	}

	want := scan.Range{From: origin, To: origin}
	if !got.Range.From.Equal(want.From) || !got.Range.To.Equal(want.To) {
		t.Fatalf(
			"D+%d 창 = [%s, %s], want [%s, %s]",
			scanDays,
			got.Range.From.Format(time.DateOnly),
			got.Range.To.Format(time.DateOnly),
			want.From.Format(time.DateOnly),
			want.To.Format(time.DateOnly),
		)
	}

	// 운영 기간이 Retention 한계를 넘으면 하한은 자연히 limit 이 된다 (§3.5).
	later := origin.AddDate(0, 0, retentionDays+30)
	got = mustAuto(t, later, origin, retentionDays, scanDays)

	if got.Empty || !got.Range.From.Equal(RetentionLimit(later, retentionDays)) {
		t.Fatalf(
			"장기 운영 하한이 limit 이 아니다: %+v",
			got,
		)
	}
}

func TestValidateManual(t *testing.T) {
	today := day(2026, 9, 21)

	const retentionDays = 35

	limit := RetentionLimit(today, retentionDays) // 2026-08-19

	tests := []struct {
		name    string
		from    time.Time
		to      time.Time
		wantErr error
	}{
		{"경계 전체 — From=limit, To=today", limit, today, nil},
		{"하루 범위", day(2026, 9, 3), day(2026, 9, 3), nil},
		{"origin 이전이라도 Retention 안이면 허용 (§3.5 수동 미적용)", limit, day(2026, 8, 25), nil},
		{"From > To", day(2026, 9, 5), day(2026, 9, 3), ErrFromAfterTo},
		{"From < limit", limit.AddDate(0, 0, -1), today, ErrBeforeRetentionLimit},
		{"To > today", day(2026, 9, 3), today.AddDate(0, 0, 1), ErrToInFuture},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateManual(tt.from, tt.to, today, retentionDays)

			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("err = %v, want nil", err)
				}

				return
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestUTCDayTruncation — 자정이 아닌 시각(예: KST 오전 = UTC 전날 밤)이
// 들어와도 UTC 날짜로 내려 같은 결과를 낸다.
func TestUTCDayTruncation(t *testing.T) {
	kst := time.FixedZone("KST", 9*3600)

	// KST 2026-09-22 08:00 = UTC 2026-09-21 23:00 → UTC 날짜는 09-21.
	todayKST := time.Date(2026, 9, 22, 8, 0, 0, 0, kst)
	todayUTC := day(2026, 9, 21)

	origin := day(2020, 1, 1)

	a := mustAuto(t, todayKST, origin, 35, 7)
	b := mustAuto(t, todayUTC, origin, 35, 7)

	if !a.Range.From.Equal(b.Range.From) || !a.Range.To.Equal(b.Range.To) {
		t.Errorf(
			"KST 입력 창 [%s, %s] != UTC 입력 창 [%s, %s]",
			a.Range.From.Format(time.DateOnly),
			a.Range.To.Format(time.DateOnly),
			b.Range.From.Format(time.DateOnly),
			b.Range.To.Format(time.DateOnly),
		)
	}

	if err := ValidateManual(
		time.Date(2026, 9, 3, 15, 30, 0, 0, kst),
		time.Date(2026, 9, 3, 23, 59, 0, 0, kst),
		todayKST,
		35,
	); err != nil {
		t.Errorf("자정 아닌 입력 검증 실패: %v", err)
	}
}

// TestAuto_InvalidOriginDoesNotOpenRetentionWindow — 커밋 7 이
// OperationOrigin 오류를 zero Time 으로 바꾸거나, unix 0 을 origin 으로
// 넘기면 max(limit, 0001/1970) = limit 이 되어 설치 당일 자동 창이
// Retention−ScanDays 일치(35/7 이면 약 26일)를 연다. 빈 창(INFO)으로
// 숨기면 배선 결함이 정상 운영 초기와 구분되지 않으므로 거부한다.
func TestAuto_InvalidOriginDoesNotOpenRetentionWindow(t *testing.T) {
	today := day(2026, 9, 21)
	limit := RetentionLimit(today, 35)
	to := today.AddDate(0, 0, -7)

	// 거부하지 않으면 실제로 열리는 창 — 테스트가 "치명 경로"를 숫자로 고정한다.
	if limit.After(to) {
		t.Fatalf("전제 붕괴: limit=%s to=%s", limit.Format(time.DateOnly), to.Format(time.DateOnly))
	}

	days := int(to.Sub(limit)/(24*time.Hour)) + 1
	if days < 20 {
		t.Fatalf("전제 붕괴: 잘못된 origin 이 열 창이 %d 일뿐", days)
	}

	for _, origin := range []time.Time{
		{},
		time.Unix(0, 0).UTC(),
		time.Unix(1, 0).UTC(),
		day(1970, 1, 1),
		day(1999, 12, 31),
	} {
		got, err := Auto(today, origin, 35, 7)
		if !errors.Is(err, ErrInvalidOrigin) {
			t.Fatalf("origin=%v: err=%v got=%+v, want ErrInvalidOrigin (창 %d 일이 열렸을 경로)",
				origin, err, got, days)
		}

		if !got.Range.From.IsZero() || !got.Range.To.IsZero() {
			t.Fatalf("origin=%v: 오류인데 창이 실렸다: %+v", origin, got)
		}
	}
}

// TestAuto_DiscoveryOriginDoesNotReplayObservationSpan — 설치 당일
// 디스크에 관측일 30일치가 있어도 origin 은 발견일(오늘)이다. Auto 가
// 그 값을 쓰면 창은 비고, 가장 옛 관측일을 origin 으로 오인하면
// From=limit 인 26일 창이 열린다.
func TestAuto_DiscoveryOriginDoesNotReplayObservationSpan(t *testing.T) {
	install := day(2026, 9, 21)
	oldestObs := install.AddDate(0, 0, -29)

	got := mustAuto(t, install, install, 35, 7)
	if !got.Empty || got.Why != EmptyBeforeOrigin {
		t.Fatalf("발견일 origin: Empty=%t Why=%s, want before_origin", got.Empty, got.Why)
	}

	wrong := mustAuto(t, install, oldestObs, 35, 7)
	if wrong.Empty {
		t.Fatal("관측일을 origin 으로 넣었는데 창이 비었다 — 오인 경로가 사라졌다")
	}

	from := oldestObs
	if lim := RetentionLimit(install, 35); lim.After(from) {
		from = lim
	}

	if !wrong.Range.From.Equal(from) || wrong.Range.Days() < 20 {
		t.Fatalf("오인 origin 창 [%s, %s] days=%d — 설치 직후 대량 재전송 경로",
			wrong.Range.From.Format(time.DateOnly),
			wrong.Range.To.Format(time.DateOnly),
			wrong.Range.Days())
	}
}
