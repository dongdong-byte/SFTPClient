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
	"os/signal"
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
		configPath = flag.String("config", "",
			"config.ini 경로. 미지정 시 실행파일 옆의 config.ini")
		dryRun = flag.Bool("dry-run", false,
			"ledger 에 쓰지 않고 무엇을 할 것인지만 보고한다")
		deep = flag.Bool("deep", false,
			"Deep Scan 범위(ScanDays)로 실행한다. "+
				"DeepScanHour 자동 판정은 스케줄러 연동과 함께 붙는다")

		// TODO(seed 구현 시): live 가 기본이 되면 이 플래그를 제거한다.
		seedCommon = flag.Bool("seed-common", false,
			"전송하지 않고 common_ledger 만 실제로 기록한다. "+
				"Unchanged 경로 검증용이며 seed 자체는 아니다")

		transportName = flag.String("transport", "",
			"live 전송 계층 (localfs | sftp). "+
				"live(--dry-run/--seed-common 없이)는 이 값이 필수다")
	)

	flag.Parse()

	// --config 미지정 시 실행파일 옆의 config.ini 를 쓴다.
	//
	// 기본값을 "config.ini" 문자열로 두면 CWD 상대경로가 되어,
	// 작업 스케줄러(schtasks)가 CWD 를 exe 위치와 다르게 잡을 때
	// "설정 파일 없음" 으로 끝난다. DefaultPath 는 os.Executable 기준이라
	// CWD 와 무관하게 exe 옆 config.ini 를 찾는다.
	// --config 를 명시하면 그 값이 우선이다.
	if *configPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}
		*configPath = p
	}

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

	// Ctrl+C(Interrupt) 를 ctx 취소로 전파한다.
	//
	// 이 연결이 없으면 프로세스가 즉사하여, 전송 경로의 취소 설계
	// (UploadPart 의 청크 단위 검사, FinishPut/FailPut 의 WithoutCancel
	// 격리)가 실전에서 발동할 수 없다. IN_PROGRESS recovery 검증의
	// "실행 중 Ctrl+C" 시나리오도 이 전파를 전제한다.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 첫 Ctrl+C 로 ctx 가 취소되면 시그널 가로채기를 해제한다.
	//
	// signal.NotifyContext 는 stop() 이 불릴 때까지 SIGINT 를 계속
	// 가로챈다. defer stop() 뿐이면 실행 중 두 번째 Ctrl+C 가 삼켜져,
	// 취소 처리가 걸려 있을 때 빠져나갈 탈출구가 없다. 첫 취소 직후
	// stop() 을 호출해 Go 기본 동작(즉시 종료)을 복원한다.
	//
	// 정상 종료 시에는 defer stop() 이 ctx 를 취소하여 이 goroutine 을
	// 깨우므로 누수되지 않는다. stop() 이 두 번 불려도 멱등이라 무해하다.
	go func() {
		<-ctx.Done()
		stop()
	}()

	// live 는 전송 계층을 명시해야 한다. 기본값으로 무언가를 보내기
	// 시작하는 실행 경로는 두지 않는다 — "오류 없이 잘못 도는" 부류다.
	var uploader put.Uploader

	switch {
	case !live && *transportName != "":
		return fmt.Errorf("--transport 는 live 전용이다 " +
			"(--dry-run/--seed-common 과 함께 쓸 수 없다)")

	case live && *transportName == "":
		return fmt.Errorf(
			"live 전송은 --transport 지정이 필수다 (localfs | sftp)",
		)

	case live && *transportName == "localfs":
		uploader = transport.LocalFS{}

	case live && *transportName == "sftp":
		// 접속 시점에 posix-rename 확장 지원을 확인하고 미지원이면
		// 전송 시작 전에 거부한다 (DialSFTP 주석 — 2026-08-31 확정).
		//
		// lock 획득 전에 접속하므로, 회차가 겹친 실행도 접속 한 번은
		// 수행한 뒤 ErrHeld 로 물러난다. 접속 한 번의 비용은 작고,
		// transport 준비를 다른 준비 단계(config/validate)와 같은
		// 자리에 두는 쪽을 택한다.
		sf, err := transport.DialSFTP(transport.SFTPDialOptions{
			Host:           cfg.Put.SFTP.Host,
			Port:           cfg.Put.SFTP.Port,
			User:           cfg.Put.SFTP.User,
			PrivateKeyPath: cfg.Put.SFTP.PrivateKey,
			KnownHostsPath: cfg.Put.SFTP.KnownHosts,
		})
		if err != nil {
			return err
		}
		defer func() {
			if err := sf.Close(); err != nil {
				log.Printf("[SFTP][WARN] close: %v", err)
			}
		}()
		uploader = sf

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

	db, err := ledger.Open(ctx, cfg.General.LedgerPath)
	if err != nil {
		return err
	}

	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("[LEDGER][WARN] close: %v", err)
		}
	}()

	// TODO(recovery 단계): 시작 시 IN_PROGRESS 회수.
	//   ledger.ListInProgress → 원격 .part 삭제(Uploader.Remove) →
	//   ledger.FailPut (단, Rename 후 사망분은 원격 Size == local_size
	//   면 재전송 없이 VERIFIED 승격 — salvage 분기, 설계서에서 확정).
	//   정리 순서(회수 → Scan/전송 → Cleanup)는 schema.sql 방침이다.

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

	// TODO(Worker Pool 단계): Worker Pool 전송 (MaxWorkers).
	// TODO(Retention 단계): Deep 실행일이면 Retention Cleanup.

	return nil
}
