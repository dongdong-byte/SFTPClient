// rinexclient 는 RINEX 파일 전송 자동화의 진입점이다.
//
// 이 파일에는 배선(wiring)만 둔다. 후보 판정은 put 이, 나열은 scan 이,
// 판정은 verify 가, 기록은 ledger 가 한다. 여기 로직이 생기기 시작하면
// 테스트 불가능한 계층이 하나 생기는 것이다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/lock"
	"SFTPClient/internal/put"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/verify"
)

func main() {
	if err := run(); err != nil {
		log.Printf("[FATAL] %v", err)
		os.Exit(1)
	}
}

// run 은 오류를 반환값으로 모아 main 에서 exit code 를 한 곳에서 정한다.
//
// lock.ErrHeld 는 오류가 아니라 정상 종료(0)다 — 스케줄러 회차 겹침은
// 장애가 아니며, 스케줄러가 이를 실패로 집계하면 안 된다.
func run() error {
	var (
		configPath = flag.String(
			"config",
			"",
			"config.ini 경로 (미지정 시 실행 파일과 같은 디렉터리의 config.ini)",
		)
		dryRun = flag.Bool(
			"dry-run",
			false,
			"ledger 업무 데이터를 변경하지 않고 무엇을 할 것인지만 보고한다",
		)
		deep = flag.Bool(
			"deep",
			false,
			"Deep Scan 범위(ScanDays)로 실행한다. "+
				"DeepScanHour 자동 판정은 스케줄러 연동과 함께 붙는다",
		)
	)

	flag.Parse()

	resolvedConfigPath, err := resolveConfigPath(*configPath)
	if err != nil {
		return err
	}

	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	// live 전송은 transport 도입 전까지 명시적으로 거부한다.
	// PENDING 만 쌓고 전송하지 않는 실행은 재개 가능한 고아라 무해하지만,
	// "오류 없이 잘못 도는" 부류이므로 시작 자체를 막는다.
	if !*dryRun {
		return fmt.Errorf(
			"live 전송은 미구현이다 (transport 도입 전). " +
				"--dry-run 으로 실행하라",
		)
	}

	l, err := lock.Acquire(
		cfg.General.LedgerPath+".lock",
		cfg.General.LockStale,
	)
	if errors.Is(err, lock.ErrHeld) {
		log.Printf("[LOCK] %v — exiting", err)
		return nil
	}
	if err != nil {
		return err
	}

	defer func() {
		if err := l.Release(); err != nil {
			// ErrLost 는 LockStaleSeconds 가 실제 실행 시간보다
			// 짧다는 운영 신호다. (lock.ErrLost 주석)
			log.Printf("[LOCK][WARN] release: %v", err)
		}
	}()

	if l.TookOver {
		log.Printf(
			"[LOCK][WARN] took over stale lock (pid=%d started=%s) — "+
				"previous run did not exit cleanly",
			l.Prev.PID,
			l.Prev.Started.Format(time.RFC3339),
		)
	}

	ctx := context.Background()

	db, err := ledger.Open(ctx, cfg.General.LedgerPath)
	if err != nil {
		return err
	}

	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("[LEDGER][WARN] close: %v", err)
		}
	}()

	// TODO(transport): 시작 시 IN_PROGRESS 회수.
	//   ledger.ListInProgress → 원격 .part 삭제 → ledger.FailPut (→ FAILED).
	//   PENDING 으로 되돌리지 않는다. 정리 순서(회수 → Scan/전송 → Cleanup)는
	//   schema.sql · GUIDELINES 5절 확정 방침이다.

	days := cfg.Scan.RecentDays
	if *deep {
		days = cfg.Scan.Days
	}

	// scan.Range 는 From·To 날짜를 모두 포함하며 내부에서 UTC 자정으로
	// 내린다 (scan.Range 정의). 오늘 포함 days 일이므로 From 은
	// 오늘−(days−1) 이다.
	now := time.Now()
	rng := scan.Range{
		From: now.AddDate(0, 0, -(days - 1)),
		To:   now,
	}

	jobs := make([]put.CategoryJob, 0)
	for _, cc := range cfg.Put.EnabledCategories() {
		jobs = append(jobs, put.CategoryJob{
			Category:   cc.Category,
			LocalPath:  cc.LocalPath,
			RemotePath: cc.RemotePath,
		})
	}

	if len(jobs) == 0 {
		return fmt.Errorf("활성화된 PUT Category 가 없다 (config 확인)")
	}

	runner := &put.Runner{
		Scanner:  scan.New(scan.LocalLister{}),
		DB:       db,
		Verifier: verify.Verifier{Grace: cfg.Ingress.Grace},
		Opts: put.RunOptions{
			DryRun:           *dryRun,
			MaxRetries:       cfg.Put.MaxRetries,
			MaxFilesPerRun:   cfg.Put.MaxFilesPerRun,
			RepostDownloaded: cfg.General.RepostDownloaded,
		},
	}

	_, report, err := runner.Run(ctx, jobs, rng)
	if err != nil {
		return err
	}

	report.Print(nil)

	// TODO(transport): Worker Pool 전송 (MaxWorkers).
	// TODO(transport): Deep 실행일이면 Retention Cleanup.

	return nil
}

// resolveConfigPath 는 --config 가 생략되었을 때 실행 파일과 같은
// 디렉터리의 config.ini 를 기본값으로 사용한다.
//
// Windows 작업 스케줄러는 작업 디렉터리를 실행 파일 위치와 다르게
// 잡을 수 있으므로 cwd/config.ini 에 의존하지 않는다.
// 사용자가 --config 를 명시했다면 그 값을 그대로 사용한다.
func resolveConfigPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}

	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("실행 파일 경로 확인 실패: %w", err)
	}

	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("실행 파일 절대 경로 확인 실패: %w", err)
	}

	return filepath.Join(filepath.Dir(exe), "config.ini"), nil
}
