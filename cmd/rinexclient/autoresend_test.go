package main

import (
	"testing"
)

// resendBudget (v4 §6.2) 테스트.
//
// 고정하는 계약:
//   - MaxFilesPerRun=0 은 절단 없음이 ② 에도 그대로 — 무제한(0)으로
//     통과하고 건너뛰지 않는다.
//   - 예산이 0 이하(①이 다 썼거나 초과)면 ② 를 건너뛴다.
//   - 차감 기준은 len(kept) 하나다 — live 와 dry-run 이 같은 kept 를
//     넣으면 같은 예산이 나온다 (Registered 를 쓰면 dry-run 에서
//     어긋난다).
func TestResendBudget(t *testing.T) {
	tests := []struct {
		name       string
		maxFiles   int
		kept       int
		wantBudget int
		wantSkip   bool
	}{
		{"평시 — 남은 예산", 4000, 200, 3800, false},
		{"① 이 전부 소진 — 예산 0", 4000, 4000, 0, true},
		{"① 이 초과(절단 후에도 kept=max) — 음수 방지", 4000, 5000, 0, true},
		{"MaxFilesPerRun=0 — ② 도 무제한, 건너뛰지 않음", 0, 99999, 0, false},
		{"kept=0 — 예산 전체", 4000, 0, 4000, false},
		{"경계 — 딱 1 남음", 4000, 3999, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, skip := resendBudget(tt.maxFiles, tt.kept)

			if budget != tt.wantBudget || skip != tt.wantSkip {
				t.Errorf(
					"resendBudget(%d, %d) = (%d, %t), want (%d, %t)",
					tt.maxFiles, tt.kept,
					budget, skip,
					tt.wantBudget, tt.wantSkip,
				)
			}
		})
	}
}
