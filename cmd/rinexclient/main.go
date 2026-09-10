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
	"strings"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/lock"
	"SFTPClient/internal/put"
	"SFTPClient/internal/scan"
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
		jobs = append(
			jobs,
			put.CategoryJob{
				Category:   cc.Category,
				LocalPath:  cc.LocalPath,
				RemotePath: cc.RemotePath,

				// Set Completeness Gate 정책 (버전 단위, 확정 §5·§7).
				// nil 이면 게이트 OFF — 기본 배포 상태이며 기존 동작과
				// 완전히 동일하다. 버전의 출처는 닫힌 열거형 하나다
				// (§5 3차 확정) — Category 는 config 가 ParseCategory 로
				// 강제한 값이라 아래 err 는 발생하지 않아야 하며,
				// 발생한다면 열거형/switch 정합이 깨진 코드 결함이므로
				// 조용히 게이트를 끄는 대신 시작을 중단한다.
				RequiredKinds: mustSetKinds(cfg, cc.Category),
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
			DryRun:           *dryRun,
			MaxRetries:       cfg.Put.MaxRetries,
			MaxFilesPerRun:   cfg.Put.MaxFilesPerRun,
			MaxWorkers:       cfg.Put.MaxWorkers,
			RepostDownloaded: cfg.General.RepostDownloaded,
			SeedMode:         *seed,
		},
	}

	// seed 는 초기화 모드다:
	//
	// 후보 계산(Run, SeedMode)
	// → 원격 대조(Seed)
	// → 리포트
	// → 종료
	//
	// 전송과 startup recovery 는 하지 않는다.
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

		kept, report, err := runner.Run(
			ctx,
			jobs,
			seedRng,
		)
		if err != nil {
			return err
		}

		report.Print(nil)

		if _, err := runner.Seed(
			ctx,
			uploader,
			jobs,
			kept,
		); err != nil {
			return err
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
			ctx,
			uploader,
		); err != nil {
			return err
		}
	}

	kept, report, err := runner.Run(
		ctx,
		jobs,
		rng,
	)
	if err != nil {
		return err
	}

	report.Print(nil)

	if live {
		if _, err := runner.Transfer(
			ctx,
			uploader,
			jobs,
			kept,
		); err != nil {
			return err
		}
	}

	// TODO(Retention 단계): Deep 실행일이면 Retention Cleanup.

	return nil
}

// mustSetKinds 는 카테고리의 RINEX 버전에 해당하는 [SET.RINEXx]
// RequiredKinds 를 돌려준다. 게이트 OFF(부재/false)는 nil 이다.
//
// RinexVersion 의 err 는 "열거형에 있는데 버전 switch 가 빠진" 설정
// 공백이며, 정상 config 경로에서는 도달하지 않는다. 도달하면 게이트가
// 조용히 꺼진 채 도는 것을 막기 위해 즉시 중단한다 (SetKeyKind 의
// err/유보 이원 계약과 같은 원칙).
func mustSetKinds(cfg *config.Config, cat domain.Category) []string {
	ver, err := cat.RinexVersion()
	if err != nil {
		fmt.Fprintf(os.Stderr, "set gate: %v\n", err)
		os.Exit(1)
	}

	return cfg.Set.Policy(ver).RequiredKinds
}
