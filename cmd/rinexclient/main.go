// rinexclient 는 RINEX 파일 전송 자동화의 진입점이다.
//
// 이 파일에는 배선(wiring)만 둔다. 후보 판정은 put 이, 나열은 scan 이,
// 판정은 verify 가, 기록은 ledger 가, 전송은 transport 가 한다.
// 여기 로직이 생기기 시작하면 테스트 불가능한 계층이 하나 생기는 것이다.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/lock"
	"SFTPClient/internal/put"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/transport"
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
		configPath = flag.String("config", "config.ini", "config.ini 경로")
		dryRun     = flag.Bool("dry-run", false,
			"ledger 에 쓰지 않고 무엇을 할 것인지만 보고한다")
		deep = flag.Bool("deep", false,
			"Deep Scan 범위(ScanDays)로 실행한다. "+
				"DeepScanHour 자동 판정은 스케줄러 연동과 함께 붙는다")

		// TODO(sftpfs 도입 시): live 가 기본이 되면 이 플래그를 제거한다.
		seedCommon = flag.Bool("seed-common", false,
			"전송하지 않고 common_ledger 만 실제로 기록한다. "+
				"Unchanged 경로 검증용이며 seed 자체는 아니다")

		transportName = flag.String("transport", "",
			"live 전송 계층. 현재 localfs 만 구현되어 있다. "+
				"live(--dry-run/--seed-common 없이)는 이 값이 필수다")
	)

	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if *dryRun && *seedCommon {
		return fmt.Errorf(
			"--dry-run 과 --seed-common 은 함께 쓸 수 없다 " +
				"(전자는 쓰지 않고 후자는 쓴다)",
		)
	}

	// 세 모드는 상호 배타다: dry-run(관측) / seed-common(장부만) / live(전송).
	live := !*dryRun && !*seedCommon

	// live 는 전송 계층을 명시해야 한다. 기본값으로 무언가를 보내기
	// 시작하는 실행 경로는 두지 않는다 — "오류 없이 잘못 도는" 부류다.
	var uploader put.Uploader

	switch {
	case !live && *transportName != "":
		return fmt.Errorf("--transport 는 live 전용이다 " +
			"(--dry-run/--seed-common 과 함께 쓸 수 없다)")

	case live && *transportName == "":
		return fmt.Errorf(
			"live 전송은 --transport 지정이 필수다. " +
				"현재 localfs 만 구현되어 있다 (sftp 는 후속)",
		)

	case live && *transportName == "localfs":
		uploader = transport.LocalFS{}

	case live && *transportName == "sftp":
		return fmt.Errorf("transport sftp 는 미구현이다 (후속 단계)")

	case live:
		return fmt.Errorf("알 수 없는 transport: %q", *transportName)
	}

	if *seedCommon {
		log.Printf(
			"[SEED][WARN] common_ledger 를 실제로 기록한다. " +
				"전송하지 않으므로 put_ledger 에 PENDING 고아가 남는다. " +
				"이것은 원격 존재 여부를 반영하는 seed 가 아니다",
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

	// TODO(sftpfs 도입 시): 시작 시 IN_PROGRESS 회수.
	//   ledger.ListInProgress → 원격 .part 삭제(Uploader.Remove) →
	//   ledger.FailPut. 정리 순서(회수 → Scan/전송 → Cleanup)는
	//   schema.sql 방침이다. localfs 단일 경로에서는 크래시 잔여가
	//   다음 실행의 BeginPut 재시도로 자연 회수되므로 뒤로 미룬다.

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

	kept, report, err := runner.Run(ctx, jobs, rng)
	if err != nil {
		return err
	}

	report.Print(nil)

	if live {
		// PENDING 등록이 실패했다면 Run 이 오류를 반환하여 여기 오지
		// 않는다 — kept 가 있다는 것 자체가 등록 완료의 증거다.
		if _, err := runner.Transfer(ctx, uploader, jobs, kept); err != nil {
			return err
		}
	}

	// TODO(sftpfs 도입 시): Worker Pool 전송 (MaxWorkers).
	// TODO(sftpfs 도입 시): Deep 실행일이면 Retention Cleanup.

	return nil
}
