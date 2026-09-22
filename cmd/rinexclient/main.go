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
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/lock"
	"SFTPClient/internal/logging"
	"SFTPClient/internal/put"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/scanwindow"
	"SFTPClient/internal/security"
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
	// secure-set 은 설치 시 1회용 서브커맨드다. flag.Parse 보다 먼저
	// 가로챈다 — 가로채지 않으면 flag 가 "secure-set" 을 위치 인자로
	// 조용히 무시하고 정기 실행(live 전송)에 들어간다. 프로비저닝을
	// 하려던 운영자가 전송을 발동시키는 사고를 여기서 차단한다.
	//
	// 런타임 무인자 원칙과 충돌하지 않는다 — --seed 와 같은
	// "설치 시 1회 도구" 범주다.
	if len(os.Args) > 1 && os.Args[1] == "secure-set" {
		return secureSet()
	}

	// resend 는 수동 재전송 서브커맨드다 (v4 §6.3). secure-set 과 같은
	// 방식으로 flag.Parse 전에 가로챈다 — 가로채지 않으면 위치 인자로
	// 무시되어 정기 실행(live 전송)이 발동한다.
	if len(os.Args) > 1 && os.Args[1] == "resend" {
		return resendCmd()
	}

	var (
		configPath = flag.String(
			"config",
			"",
			"config.ini 경로. 미지정 시 실행파일 옆의 config.ini",
		)

		dryRun = flag.Bool(
			"dry-run",
			false,
			"ledger 에 쓰지 않고 무엇을 할 것인지만 보고한다",
		)

		deep = flag.Bool(
			"deep",
			false,
			"Deep Scan 범위(ScanDays)로 강제 실행한다. "+
				"DeepScanHour 회차에는 플래그 없이도 자동으로 Deep 이 되며, "+
				"이 플래그는 장애 점검 등 수동 강제용이다",
		)

		seed = flag.Bool(
			"seed",
			false,
			"설치 초기화: ScanDays 창의 로컬 파일 중 원격에 이미 있고 "+
				"Size 가 일치하는 것만 VERIFIED 로 선반영하고 종료한다. "+
				"전송하지 않는다",
		)

		transportName = flag.String(
			"transport",
			"",
			"config [GENERAL] Transport 를 이번 실행에서만 덮어쓴다 "+
				"(sftp | localfs). 평소에는 config 값으로 충분하다",
		)
	)

	flag.Parse()

	// resend·secure-set 은 os.Args[1] 에서만 가로챈다. 그 앞에 플래그가
	// 오면 Parse 가 서브커맨드에서 멈추고, 남은 인자를 버린 채 정기
	// 실행이 나간다. `rinexclient --config x resend ...` 가 live 전송이
	// 되는 구멍을 여기서 막는다.
	if err := rejectExtraArgs(flag.Args()); err != nil {
		return err
	}

	// --config 미지정 시 실행파일 옆의 config.ini 를 쓴다.
	//
	// 기본값을 "config.ini" 문자열로 두면 CWD 상대경로가 되어,
	// 작업 스케줄러(schtasks)가 CWD 를 exe 위치와 다르게 잡을 때
	// "설정 파일 없음" 으로 끝날 수 있다.
	//
	// DefaultPath 는 os.Executable 기준이라 CWD 와 무관하게
	// exe 옆 config.ini 를 찾는다.
	if *configPath == "" {
		p, err := config.DefaultPath()
		if err != nil {
			return err
		}

		*configPath = p
	}

	cfg, err := config.Load(*configPath, security.New())
	if err != nil {
		return err
	}

	// File logging is best-effort: an observability failure must not become a
	// data-transfer outage. stderr stays first so every line is still visible
	// when the file writer later degrades.
	logWriter, logErr := logging.NewRotatingWriter(
		cfg.Log.Dir,
		cfg.Log.RetentionDays,
	)
	if logErr != nil {
		log.Printf(
			"[LOG][WARN] file logging disabled; continuing with stderr only: %v",
			logErr,
		)
	} else {
		log.SetOutput(io.MultiWriter(os.Stderr, logWriter))
	}

	// 평문 자격증명 경고 출력.
	//
	// Warnings 는 config.ini 에 적힌 Transport 기준으로 Load 가 만든 것이다.
	// 아래 --transport CLI override 는 반영하지 않는다 — override 는
	// 수동 점검 전용이고, 파일에 적힌 상태가 경고의 대상이기 때문이다.
	for _, w := range cfg.Warnings {
		log.Printf("[WARN] %s", w)
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
				"(seed 는 항상 ScanDays 창으로 돈다 — 좁게 seed 하면 " +
				"다음 Deep 이 나머지를 재전송한다)",
		)
	}

	// 세 모드는 상호 배타다:
	//
	//	dry-run : 관측
	//	seed    : 설치 초기화
	//	live    : 실제 전송
	live := !*dryRun && !*seed

	// 전송 계층은 config [GENERAL] Transport 가 기본이다.
	//
	// --transport 가 있으면 이번 실행에 한해 config 값을 덮어쓴다.
	// 운영에서는 보통 인자 없이 rinexclient.exe 만 실행한다.
	//
	// CLI 값 역시 config 값과 같은 형태로 정규화한다.
	needsTransport := live || *seed

	resolvedTransport := cfg.General.Transport

	if *transportName != "" {
		if !needsTransport {
			return fmt.Errorf(
				"--transport 는 live/seed 전용이다 " +
					"(--dry-run 과 함께 쓸 수 없다)",
			)
		}

		resolvedTransport = strings.ToLower(
			strings.TrimSpace(*transportName),
		)

		cfg.General.Transport = resolvedTransport

		// config.Load 는 config.ini 에 적힌 원래 Transport 를 기준으로
		// Validate + CheckEnvironment 를 이미 수행했다.
		//
		// CLI override 로 최종 Transport 가 바뀌었으므로,
		// 최종 실행 설정을 다시 검증한다.
		if err := cfg.Validate(); err != nil {
			return err
		}

		if err := cfg.CheckEnvironment(); err != nil {
			return err
		}
	}

	// Ctrl+C(Interrupt) 를 ctx 취소로 전파한다.
	//
	// 이 연결이 없으면 프로세스가 즉사하여 전송 경로의 취소 설계
	// (UploadPart 의 청크 단위 검사, FinishPut/FailPut 의 WithoutCancel
	// 격리)가 실전에서 발동할 수 없다.
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
	)
	defer stop()

	// 첫 Ctrl+C 로 ctx 가 취소되면 시그널 가로채기를 해제한다.
	//
	// 첫 취소 이후에는 두 번째 Ctrl+C 가 Go 기본 동작으로
	// 프로세스를 즉시 종료할 수 있게 한다.
	go func() {
		<-ctx.Done()
		stop()
	}()

	var uploader put.Uploader

	// runCtx 는 이번 회차(Recover·Run·Transfer)의 수명이다.
	// 기본은 시그널 ctx 그대로이며, live SFTP 에서만 스톨 watchdog 이
	// 이를 취소할 수 있다 (UNIT3 v3 §3.2 — 신규 착수 차단이 연결
	// 종료보다 먼저다).
	runCtx := ctx

	// stalled 는 이번 회차가 무진행으로 중단되었는지의 표식이다.
	// watchdog goroutine 이 세우고, run 의 종료부가 읽어 회차를
	// 비정상 종료 코드로 마감한다 (v3 §3.7 — 스톨 회차의 가시화).
	var stalled atomic.Bool

	switch {
	case !needsTransport:
		// dry-run 은 전송 계층을 만들지 않는다.

	case resolvedTransport == "localfs":
		uploader = transport.LocalFS{}

	case resolvedTransport == "sftp":
		// 접속 시점에 posix-rename 확장 지원을 확인하고
		// 미지원이면 전송 시작 전에 거부한다.
		sf, err := transport.DialSFTP(
			transport.SFTPDialOptions{
				Host:           cfg.Put.SFTP.Host,
				Port:           cfg.Put.SFTP.Port,
				User:           cfg.Put.SFTP.User,
				PrivateKeyPath: cfg.Put.SFTP.PrivateKey,
				KnownHostsPath: cfg.Put.SFTP.KnownHosts,
			},
		)
		if err != nil {
			return err
		}

		defer func() {
			if err := sf.Close(); err != nil {
				log.Printf("[SFTP][WARN] close: %v", err)
			}
		}()

		uploader = sf

		// 무진행 감시 배선 (UNIT3 v3 §3.1~§3.3).
		//
		// Dial 직후·Recover 이전에 시작한다 — Recover 의 원격
		// .part Remove 도 같은 감시 아래 있어야 한다 (v3 §3.2:
		// "전송 시작 후에만 켜면 그 앞이 구멍이다").
		//
		// 발화 시 순서가 계약이다 (v3 §3.2):
		//   ① cancelRun — 신규 착수(BeginPut) 차단. 이것이 없으면
		//      깨어난 워커가 "파일 실패 후 다음 파일"(transfer 의
		//      기존 동작)로 죽은 세션에 착수를 이어가 attempts 만
		//      깎는다.
		//   ② Abort — ssh 를 닫아 블록된 호출을 에러로 깨운다.
		//      정리(Remove)보다 먼저다. 죽은 연결로 cleanup 을
		//      먼저 돌리면 종료가 cleanupTimeout × 건수로 늘어난다.
		//
		// 이후는 전부 기존 경로다: 깨어난 워커 → failOne/failPending
		// (WithoutCancel 이라 취소된 회차에서도 기록됨) → Transfer
		// 반환 → main defer 의 lock Release → 다음 정시 Recover.
		// 성공 조건은 "이번 회차의 기록 완결"이 아니라 "프로세스가
		// 기존 반환 경로로 끝나는 것"이다 (v3 §3.1 성공 조건).
		var cancelRun context.CancelFunc
		runCtx, cancelRun = context.WithCancel(ctx)
		watchDone := make(chan struct{})
		defer func() {
			cancelRun()
			<-watchDone
		}()

		go func() {
			defer close(watchDone)
			// 일반 취소에서도 감시만 멈추고 SFTP 대기를 남기지 않는다.
			defer sf.Abort()
			transport.WatchStall(
				runCtx,
				sf,
				cfg.Put.SFTP.StallTimeout,
				func(inFlight int64, quiet time.Duration) {
					stalled.Store(true)
					log.Printf(
						"[STALL] 원격 무진행 %s (진행 중 작업 %d개, 문턱 %s) — "+
							"신규 착수를 차단하고 SSH 연결을 닫는다",
						quiet.Truncate(time.Second),
						inFlight,
						cfg.Put.SFTP.StallTimeout,
					)
					cancelRun()
					sf.Abort()
				},
			)
		}()

	default:
		// config 경로는 Validate 가 sftp/localfs 만 허용한다.
		// 따라서 주로 --transport 오타가 이 경로에 도달한다.
		return fmt.Errorf(
			"알 수 없는 transport: %q",
			resolvedTransport,
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
			// 짧다는 운영 신호다.
			log.Printf("[LOCK][WARN] release: %v", err)
		}
	}()

	if l.TookOver {
		log.Printf(
			"[LOCK][WARN] took over stale lock "+
				"(pid=%d started=%s) — previous run did not exit cleanly",
			l.Prev.PID,
			l.Prev.Started.Format(time.RFC3339),
		)
	}

	db, err := ledger.Open(
		ctx,
		cfg.General.LedgerPath,
	)
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
		// Set Completeness Gate 정책 (버전 단위).
		// RequiredKinds nil 은 게이트 OFF — 기본 배포 상태이며 기존
		// 동작과 완전히 동일하다. ResendMinKinds 는 ②(자동 resend)와
		// 수동 resend 의 보류 판정에만 쓰이고(put Opts.Resend), 정시
		// ① 에서는 읽히지 않는다.
		required, resendMin := mustSetPolicy(cfg, cc.Category)

		jobs = append(
			jobs,
			put.CategoryJob{
				Category:   cc.Category,
				LocalPath:  cc.LocalPath,
				RemotePath: cc.RemotePath,

				RequiredKinds:  required,
				ResendMinKinds: resendMin,
			},
		)
	}

	if len(jobs) == 0 {
		return fmt.Errorf(
			"활성화된 PUT Category 가 없다 (config 확인)",
		)
	}

	runner := &put.Runner{
		Scanner:  scan.New(scan.LocalLister{}),
		DB:       db,
		Verifier: verify.Verifier{Grace: cfg.Ingress.Grace},
		Opts: put.RunOptions{
			DryRun:                *dryRun,
			MaxRetries:            cfg.Put.MaxRetries,
			MaxFilesPerRun:        cfg.Put.MaxFilesPerRun,
			MaxHashBackfillPerRun: cfg.Put.MaxHashBackfillPerRun,
			MaxWorkers:            cfg.Put.MaxWorkers,
			RepostDownloaded:      cfg.General.RepostDownloaded,
			SeedMode:              *seed,
		},
	}

	// seed 는 초기화 모드다:
	//
	// 후보 계산(Run, SeedMode)
	// → 원격 대조(Seed)
	// → 리포트
	// → 종료
	//
	// 전송과 시작 시 Recover 는 하지 않는다.
	//
	// 범위는 항상 ScanDays 이다.
	// Hot 범위만 seed 하면 다음 Deep 회차에서 나머지 파일이
	// 신규로 보일 수 있다.
	if *seed {
		now := time.Now()

		seedRng := scan.Range{
			From: now.AddDate(
				0,
				0,
				-(cfg.Scan.Days - 1),
			),
			To: now,
		}

		// seed 도 live SFTP 연결을 쓰므로 watchdog 아래에 있다.
		// runCtx 를 넘겨 발화 시 신규 원격 대조 착수가 끊기게 한다
		// (UNIT3 v3 §3.2·§9 — 취소가 연결 종료보다 먼저다. seed 만
		// ctx 를 쓰면 이 순서 계약이 seed 회차에서만 깨진다).
		kept, report, err := runner.Run(
			runCtx,
			jobs,
			seedRng,
		)
		if err != nil {
			if stalled.Load() {
				return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (seed Run): %w", err)
			}
			return err
		}

		// 창 크기는 Deep 과 동일한 ScanDays 전체다 (위 seedRng).
		// 모집단 구분은 mode=seed 가 담당한다 (UNIT4 §5).
		report.Range = "deep"

		report.Print(nil)

		if _, err := runner.Seed(
			runCtx,
			uploader,
			jobs,
			kept,
		); err != nil {
			if stalled.Load() {
				return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (Seed): %w", err)
			}
			return err
		}

		// 마지막 대조 직후에 발화하면 오류 없이 여기 도달할 수 있다.
		// live 회차와 같은 이유로 성공으로 위장하지 않는다 (UNIT3 v3 §3.7).
		if stalled.Load() {
			return errors.New("stall: 회차가 무진행으로 중단됨 (seed)")
		}

		return nil
	}

	// Deep 여부는 수동 플래그 OR 시각 자동 판정이다.
	//
	// DeepScanHour 는 서버 로컬시간 기준이다.
	// 작업 스케줄러가 매시 한 번 실행하므로 hour 일치로
	// 하루 한 회차만 자동 Deep 이 된다.
	now := time.Now()

	autoDeep := now.Hour() == cfg.Scan.DeepScanHour
	isDeep := *deep || autoDeep

	days := cfg.Scan.RecentDays

	if isDeep {
		days = cfg.Scan.Days

		switch {
		case *deep && autoDeep:
			log.Printf(
				"[SCAN] deep run (--deep, DeepScanHour=%d 도 일치)",
				cfg.Scan.DeepScanHour,
			)

		case autoDeep:
			log.Printf(
				"[SCAN] deep run (DeepScanHour=%d matched)",
				cfg.Scan.DeepScanHour,
			)

		default:
			log.Printf(
				"[SCAN] deep run (--deep 수동 강제)",
			)
		}
	}

	// scan.Range 는 From·To 날짜를 모두 포함하며
	// 내부에서 UTC 자정으로 내린다.
	//
	// 오늘 포함 days 일이므로 From 은 오늘-(days-1) 이다.
	rng := scan.Range{
		From: now.AddDate(
			0,
			0,
			-(days - 1),
		),
		To: now,
	}

	// 시작 시 IN_PROGRESS 회수.
	//
	// 확정 흐름:
	//
	//	Lock
	//	→ Recover
	//	→ Scan
	//	→ 후보
	//	→ Transfer
	//
	// 회수된 IN_PROGRESS 는 FAILED 로 전환되고,
	// attempts < MaxRetries 이면 바로 아래 Run 에서 retry 후보로
	// 다시 선택되어 같은 실행에서 재전송된다.
	//
	// salvage 는 하지 않는다.
	if live {
		if _, err := runner.Recover(
			runCtx,
			uploader,
		); err != nil {
			if stalled.Load() {
				return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (Recover): %w", err)
			}
			return err
		}
	}

	kept, report, err := runner.Run(
		runCtx,
		jobs,
		rng,
	)
	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (Run): %w", err)
		}
		return err
	}

	// UNIT4 §5 — 요약 줄 하나로 스캔 창 모집단을 식별한다. 창의 이름은
	// Run 이 아니라 여기(main)만 알므로 Print 전에 채운다 (§0.1-3).
	report.Range = "hot"
	if isDeep {
		report.Range = "deep"
	}

	report.Print(nil)

	// putFailed·putVerified 는 ①의 파일 단위 결과다. 실패는 fatal 이
	// 아니므로 회차는 계속되고 종료 코드도 0 이지만, ②의 진입 판단이
	// 두 값을 함께 읽는다.
	putFailed, putVerified := 0, 0

	if live {
		putRep, err := runner.Transfer(
			runCtx,
			uploader,
			jobs,
			kept,
		)
		if err != nil {
			if stalled.Load() {
				return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (Transfer): %w", err)
			}
			return err
		}

		putFailed = putRep.Failed
		putVerified = putRep.Verified
	}

	// ── ② 자동 resend (v4 §6.1 — 정시 회차의 2순위) ─────────────────
	//
	// ①(정시 put)의 Transfer 가 끝난 뒤에만 시작한다. put 코드·정렬
	// 규칙은 바꾸지 않는다 — ② 안에서는 기존 SortCandidates 가 관측일
	// 오래된 것부터(Retention 한계에 가까운 날부터) 보낸다 (§6.2).
	//
	// stall 이 발화한 회차는 죽은 연결로 착수만 시도하게 되므로
	// 건너뛴다 — 새 기능이 아니라 기존 stall 종료 경로를 ② 앞에서
	// 한 번 더 지키는 조건문이다 (§6.1). ①이 fatal 오류로 끝났으면
	// 위에서 이미 return 되어 여기 오지 않고, seed 는 조기 반환이라
	// ② 가 없다.
	//
	// ①이 실패만 있고 성공이 하나도 없으면(Failed > 0 && Verified == 0)
	// ② 를 건너뛴다 — 원격이 전반적으로 실패를 내는 상태에서 ② 를
	// 돌리면 같은 원인으로 attempts 만 태울 가능성이 크다.
	//
	// 일부라도 성공했으면 원격은 살아 있다 — 실패는 특정 파일의 사정
	// (원격 권한 등)으로 보고 ② 를 진행한다. 실패 1건으로 막으면 그
	// 파일이 소진될 때까지(MaxRetries 회차) ② 가 매시간 멈추고, 그런
	// 파일이 계속 생기면 ② 가 영영 돌지 않는다 (2026-09-22 확정).
	//
	// 회차 종료 코드는 기존대로 0 이다(파일 단위 실패는 retry 체계의
	// 몫). 실패분과 ② 몫은 다음 정시가 재판정한다. (운영 테스트
	// put_failure_blocks_resend 가 차단 쪽 계약을 고정한다 — 메시지
	// 문서 MSG-XFER-01 반영은 커밋 9.)
	//
	// dry-run 도 ② 를 관측으로 수행한다 — 예산이 len(kept) 기준이라
	// live 와 같은 숫자가 나온다 (§6.2, resendBudget 주석).
	switch {
	case stalled.Load():
		// 아래 기존 stall 반환이 회차를 마감한다.

	case putFailed > 0 && putVerified == 0:
		// 매시간 반복되면 원격 장애다 — 조용한 일반 줄로 두지 않는다.
		log.Printf(
			"[RESEND][WARN] skipped (put error: failed=%d verified=0) — "+
				"①이 한 건도 성공하지 못해 자동 재전송을 보류한다. 다음 정시가 재판정한다",
			putFailed,
		)

	default:
		// now 를 넘겨 resend 창 계산과 같은 "오늘"을 쓴다. 키가 아직
		// 없으면(운영 극초기) 행의 MIN(first_seen) 또는 오늘로 지연
		// 계산된다 — 어느 쪽이든 자동 창이 과거로 열리지 않는다.
		origin, err := db.OperationOrigin(ctx, now)
		if err != nil {
			// origin 없이 자동 창을 계산하면 하한이 사라져 §3.5 가
			// 막으려던 대량 재전송이 그대로 난다. zero 값이나
			// Retention 한계로 대체하지 않는다 (커밋 5 검토 §1.4).
			return fmt.Errorf("resend: operation origin: %w", err)
		}

		auto, err := scanwindow.Auto(
			now,
			origin,
			cfg.Ledger.RetentionDays,
			cfg.Scan.Days,
		)
		if err != nil {
			// ErrInvalidOrigin — origin 이 손상(2000년 이전 등)이다.
			// 오늘이나 Retention 한계로 대체하면 자동 창 범위가
			// 조용히 달라지므로 회차를 중단하고 사람에게 올린다.
			return fmt.Errorf("resend: auto window: %w", err)
		}

		budget, skipBudget := resendBudget(
			cfg.Put.MaxFilesPerRun,
			len(kept),
		)

		switch {
		case auto.Empty && auto.Why == scanwindow.EmptyBeforeOrigin:
			// 운영 시작 후 ScanDays 가 지나기 전의 정상 상태 —
			// WARN 이 아니라 INFO 다 (§3.5).
			log.Printf(
				"[RESEND] skipped (window empty: before origin=%s)",
				origin.Format(time.DateOnly),
			)

		case auto.Empty:
			// RetentionDays < ScanDays+2 — 자동 창이 영구히 없는
			// 설정이다. 조용히 지나가면 안 된다 (§3.3, scanwindow).
			log.Printf(
				"[RESEND][WARN] skipped (window empty: %s, "+
					"RetentionDays=%d ScanDays=%d)",
				auto.Why,
				cfg.Ledger.RetentionDays,
				cfg.Scan.Days,
			)

		case skipBudget:
			log.Printf(
				"[RESEND] skipped (budget=0: MaxFilesPerRun=%d kept=%d)",
				cfg.Put.MaxFilesPerRun,
				len(kept),
			)

		default:
			// 단계 전환 표식 — ① 요약·XFER 뒤, ② 요약 앞의 한 줄이다.
			// 순서 검증과 budget 관측이 이 줄을 읽는다 (§6.1 단계별 요약).
			if cfg.Put.MaxFilesPerRun == 0 {
				log.Printf(
					"[RESEND] auto from=%s to=%s budget=unlimited (MaxFilesPerRun=0)",
					auto.Range.From.Format(time.DateOnly),
					auto.Range.To.Format(time.DateOnly),
				)
			} else {
				log.Printf(
					"[RESEND] auto from=%s to=%s budget=%d",
					auto.Range.From.Format(time.DateOnly),
					auto.Range.To.Format(time.DateOnly),
					budget,
				)
			}

			// ② 는 ① 과 같은 협력자에 Opts 만 다르다: 남은 예산으로
			// 절단하고, 게이트는 ResendMinKinds 로 판정한다(Resend).
			// 자동은 소진 파일을 재무장하지 않는다 (§4.3 — 켜면
			// 영구 실패 파일이 Retention 한계까지 매시간 재시도된다).
			resendOpts := runner.Opts
			resendOpts.MaxFilesPerRun = budget
			resendOpts.Resend = true

			resendRunner := &put.Runner{
				Scanner:  scan.New(scan.LocalLister{}),
				DB:       db,
				Verifier: verify.Verifier{Grace: cfg.Ingress.Grace},
				Opts:     resendOpts,
			}

			resendKept, resendReport, err := resendRunner.Run(
				runCtx,
				jobs,
				auto.Range,
			)
			if err != nil {
				if stalled.Load() {
					return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (resend Run): %w", err)
				}
				return err
			}

			// 요약 모집단을 hot/deep 과 분리한다 (§6.1, UNIT4 §5).
			resendReport.Range = "resend"

			resendReport.Print(nil)

			if live {
				if _, err := resendRunner.Transfer(
					runCtx,
					uploader,
					jobs,
					resendKept,
				); err != nil {
					if stalled.Load() {
						return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (resend Transfer): %w", err)
					}
					return err
				}
			}
		}
	}

	// 스톨 발화 후에도 오류 없이 여기 도달할 수 있다 (발화 시점이
	// 마지막 작업 직후라 취소가 아무것도 끊지 못한 경우 등).
	// 회차를 성공으로 위장하지 않는다. [STALL] WARN 과 비정상 종료
	// 코드가 짝이다 (UNIT3 v3 §3.7). 연속 스톨 경보화는 유닛 4가
	// 닫지 않았다 (UNIT4 v2 §8.1) — 소진 WARN 이 반복 실패의 신호다.
	if stalled.Load() {
		return errors.New("stall: 회차가 무진행으로 중단됨")
	}

	// TODO(MVP2 Retention Cleanup): Deep 실행일이면 Retention Cleanup.

	return nil
}

// rejectExtraArgs 는 정기 실행의 위치 인자를 거부한다.
//
// 스케줄러 호출은 플래그만 쓰거나 인자가 없다. 위치 인자가 있으면
// 서브커맨드를 정기 실행으로 오인한 것이므로 전송을 시작하지 않는다.
func rejectExtraArgs(args []string) error {
	if len(args) == 0 {
		return nil
	}

	return fmt.Errorf(
		"알 수 없는 인자: %q — 정기 실행은 위치 인자를 받지 않는다. "+
			"수동 재전송은 resend 가 첫 인자여야 한다 (rinexclient resend ...)",
		args,
	)
}

// mustSetPolicy 는 카테고리의 RINEX 버전에 해당하는 [SET.RINEXx] 의
// RequiredKinds(평소 게이트)와 ResendMinKinds(resend 게이트, v4 §5)를
// 돌려준다. 게이트 OFF(부재/false)는 둘 다 nil 이다.
//
// 버전의 출처는 닫힌 열거형 하나다 (§5 3차 확정). RinexVersion 의 err 는
// "열거형에 있는데 버전 switch 가 빠진" 설정 공백이며, 정상 config
// 경로에서는 도달하지 않는다. 도달하면 게이트가 조용히 꺼진 채 도는
// 것을 막기 위해 즉시 중단한다 (SetKeyKind 의 err/유보 이원 계약과
// 같은 원칙).
func mustSetPolicy(
	cfg *config.Config,
	cat domain.Category,
) (required, resendMin []string) {
	ver, err := cat.RinexVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "set gate: %v\n", err)
		os.Exit(1)
	}

	p := cfg.Set.Policy(ver)

	return p.RequiredKinds, p.ResendMinKinds
}
