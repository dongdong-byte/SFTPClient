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
			"Deep Scan 범위(ScanDays)로 강제 실행한다. "+
				"DeepScanHour 회차에는 플래그 없이도 자동으로 Deep 이 되며, "+
				"이 플래그는 장애 점검 등 수동 강제용이다")

		seed = flag.Bool("seed", false,
			"설치 초기화: ScanDays 창의 로컬 파일 중 원격에 이미 있고 "+
				"Size 가 일치하는 것만 VERIFIED 로 선반영하고 종료한다. "+
				"전송하지 않는다. --transport 필수")

		transportName = flag.String("transport", "",
			"원격 접근 계층 (localfs | sftp). "+
				"live 전송과 --seed 실행에는 이 값이 필수다")
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

	if *dryRun && *seed {
		return fmt.Errorf(
			"--dry-run 과 --seed 는 함께 쓸 수 없다 " +
				"(전자는 쓰지 않고 후자는 쓴다)",
		)
	}

	if *deep && *seed {
		return fmt.Errorf(
			"--deep 과 --seed 는 함께 쓸 수 없다 " +
				"(seed 는 항상 ScanDays 창으로 돈다 — 2026-09-01 확정: " +
				"좁게 seed 하면 다음 Deep 이 나머지를 재전송한다)",
		)
	}

	// 세 모드는 상호 배타다: dry-run(관측) / seed(초기화) / live(전송).
	// (--seed-common 은 real seed 완성으로 역할이 끝나 제거했다 —
	// common 만 기록해 PENDING 고아를 만들고 원격 검증이 없어
	// real seed 와 이름·역할이 충돌했다.)
	live := !*dryRun && !*seed

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

	// live 전송과 seed(원격 Stat 대조)는 전송 계층을 명시해야 한다.
	// 기본값으로 무언가를 보내기 시작하는 실행 경로는 두지 않는다 —
	// "오류 없이 잘못 도는" 부류다.
	needsTransport := live || *seed

	var uploader put.Uploader

	switch {
	case !needsTransport && *transportName != "":
		return fmt.Errorf("--transport 는 live/seed 전용이다 " +
			"(--dry-run 과 함께 쓸 수 없다)")

	case needsTransport && *transportName == "":
		return fmt.Errorf(
			"live 전송과 seed 는 --transport 지정이 필수다 (localfs | sftp)",
		)

	case needsTransport && *transportName == "localfs":
		uploader = transport.LocalFS{}

	case needsTransport && *transportName == "sftp":
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

	case needsTransport:
		return fmt.Errorf("알 수 없는 transport: %q", *transportName)
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
			MaxWorkers:       cfg.Put.MaxWorkers,
			RepostDownloaded: cfg.General.RepostDownloaded,
			SeedMode:         *seed,
		},
	}

	// seed 는 초기화 모드다: 후보 계산(Run, SeedMode) → 원격 대조
	// (Seed) → 리포트 → 종료. 전송·recovery 를 하지 않는다 —
	// recovery 는 live 시작 절차이고, seed 시점의 IN_PROGRESS 잔재는
	// 후보 계산이 제외하므로(ExcludedInProgress) 건드리지 않는다.
	//
	// 창은 항상 ScanDays(7일) 고정이다. Hot(2일)만 seed 하면 다음
	// Deep 회차가 3~7일째 파일을 "신규" 로 보아 재전송한다.
	if *seed {
		seedNow := time.Now()

		seedRng := scan.Range{
			From: seedNow.AddDate(0, 0, -(cfg.Scan.Days - 1)),
			To:   seedNow,
		}

		kept, report, err := runner.Run(ctx, jobs, seedRng)
		if err != nil {
			return err
		}

		report.Print(nil)

		if _, err := runner.Seed(ctx, uploader, jobs, kept); err != nil {
			return err
		}

		return nil
	}

	// Deep 여부는 수동 플래그 OR 시각 자동 판정이다.
	//
	// DeepScanHour 는 서버 로컬 시간이고(확정 — "조용한 시간대" 는 로컬
	// 개념), 스케줄러는 매시 HH:10 에 실행하므로 시(hour) 일치 비교로
	// 충분하다. 분 단위 창 계산은 기각 — HH:10 은 시 경계에서 50분
	// 떨어져 있어 정각 실행의 경계 문제가 없고, 매시 1회 실행이라
	// 하루 중 정확히 한 회차만 참이 된다.
	//
	// 놓친 Deep 회차(크래시·재부팅으로 4:10 회차 누락)는 보충하지
	// 않는다 (2026-09-01 조건부 기각). Hot 2일 창이 매시 돌고 있어
	// 유실이 아니라 최대 24시간 지연이며, 유실은 Deep 이 6일 연속
	// 빠져야 가능한데 그 정도면 사람이 개입할 서버 장애다.
	// last_deep_run 류 meta 상태 추가는 초기화·시계 역행 처리가
	// 따라와 이득 대비 비싸다. 누락 관측은 아래 deep run 로그로 한다.
	// 재검토 트리거: 운영 중 Deep 누락이 실제 관측되고 지연이 문제 되면.
	now := time.Now()

	autoDeep := now.Hour() == cfg.Scan.DeepScanHour
	isDeep := *deep || autoDeep

	days := cfg.Scan.RecentDays
	if isDeep {
		days = cfg.Scan.Days

		// 자동 발동 사유를 남긴다. 이 줄이 없으면 운영자가 "왜 이
		// 회차만 7일 창인가" 를 로그로 구분할 수 없고, 하루치 로그에
		// 이 줄이 없는 것이 곧 Deep 누락의 관측 신호다.
		switch {
		case *deep && autoDeep:
			log.Printf("[SCAN] deep run (--deep, DeepScanHour=%d 도 일치)",
				cfg.Scan.DeepScanHour)
		case autoDeep:
			log.Printf("[SCAN] deep run (DeepScanHour=%d matched)",
				cfg.Scan.DeepScanHour)
		default:
			log.Printf("[SCAN] deep run (--deep 수동 강제)")
		}
	}

	// scan.Range 는 From·To 날짜를 모두 포함하며 내부에서 UTC 자정으로
	// 내린다 (scan.Range 정의). 오늘 포함 days 일이므로 From 은
	// 오늘−(days−1) 이다.
	rng := scan.Range{
		From: now.AddDate(0, 0, -(days - 1)),
		To:   now,
	}

	// 시작 시 IN_PROGRESS 회수 — 확정 흐름의 첫 단계
	// (Lock → Recover → Scan → 후보 → Transfer).
	//
	// live 전용이다. 회수는 원격 .part 삭제(uploader)와 put_ledger 쓰기를
	// 하므로 dry-run/seed-common 에는 uploader 도 없고 쓰기도 하지 않는다.
	//
	// 회수된 IN_PROGRESS 는 FAILED 가 되어 바로 아래 Run 의 후보 판정에서
	// 재시도(IsRetry)로 다시 잡혀 같은 실행에서 재전송된다 —
	// 다음 회차를 기다리지 않는다. salvage 는 하지 않는다 (A안,
	// recovery.go 주석).
	if live {
		if _, err := runner.Recover(ctx, uploader); err != nil {
			return err
		}
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
