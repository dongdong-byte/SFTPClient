package main

// resendBudget 은 ② 자동 resend 의 절단 예산이다 (resend 설계 v4 §6.2).
//
//	MaxFilesPerRun == 0 → "절단 없음"이 ② 에도 그대로다.
//	                      budget 0(= put.RunOptions.MaxFilesPerRun 의
//	                      "절단하지 않음")에 skip=false 로 통과시킨다.
//	그 외               → MaxFilesPerRun − keptLen.
//	                      0 이하면 ② 를 건너뛴다 (skip=true).
//
// 차감 기준은 Registered 가 아니라 len(kept) 다. live 에서는 둘이
// 같지만 dry-run 은 Registered=0 이라, Registered 로 차감하면 ② 의
// 미리보기 예산이 실제보다 커진다. live 와 dry-run 이 같은 숫자를
// 내도록 kept 로 통일한다 (§6.2 확정).
//
// 이 함수가 cmd 계층에 있는 이유: 예산은 ①과 ②를 한 회차로 묶는
// 규칙이라 put(단계 하나의 실행기)의 소유가 아니다. scanwindow 는
// 날짜 정책만 소유한다.
func resendBudget(maxFilesPerRun, keptLen int) (budget int, skip bool) {
	if maxFilesPerRun == 0 {
		return 0, false
	}

	b := maxFilesPerRun - keptLen
	if b <= 0 {
		return 0, true
	}

	return b, false
}
