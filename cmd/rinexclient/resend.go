// resend 서브커맨드 — 수동 재전송 (resend 설계 v4 §6.3).
//
// 정시 회차(run)와 달리 사람이 앉아서 실행하는 명령이므로, 락이 잡혀
// 있으면 조용히 물러나는 대신 기다리고, 조건이 잘못되면 실행 전에
// 시끄럽게 거부한다.
//
//	Lock(대기, 상한 = LockStaleSeconds)
//	→ Dial(+stall watchdog) → Recover
//	→ resend 1회 (지정 조건, 예산 = MaxFilesPerRun, 소진 재무장)
//	→ Release
//
// 한 번 실행 = 한 배치다. 스스로 반복하지 않는다 — 잘린 파일은 자동
// 창 안이면 다음 정시가 이어 보내고, 운영자는 필요하면 다시 실행한다.
// put 단계(①)는 없다. put 은 정시 회차의 책임이다.
//
// config 로드·transport 해석·watchdog 배선은 run() 의 동일 블록과
// 계약을 공유한다 (특히 stall 발화 순서: cancelRun → Abort). 그쪽을
// 바꾸면 여기도 함께 바꿔라 — 배선 중복은 run() 재수정 위험보다 낫다고
// 판단해 감수한 것이다 (커밋 8, 2026-09-22).
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
	"strconv"
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

// 락 대기 정책 (§6.3 — 간격은 구현 시 확정 사항, 2026-09-22 확정 10초).
//
// 10초: 정시 회차가 풀린 뒤 평균 5초 안에 잡아 사람 체감으로 "바로"에
// 가깝고, 상한(LockStaleSeconds, 기본 3시간)까지 가도 천 회 남짓이라
// 부하·로그 모두 무해하다. 대기 중임은 침묵도 도배도 아니게 알린다 —
// 반복 안내 간격은 5분이다 (메시지 문서 §3-2, 2026-09-22 확정.
// 첫 대기 안내와 획득·시간 초과·취소 안내는 별개다).
const (
	lockWaitInterval = 10 * time.Second
	lockWaitLogEvery = 5 * time.Minute
)

// resendCmd 는 rinexclient resend ... 의 진입점이다.
// run() 과 같은 원칙: 배선만 두고, 판정은 내부 패키지가 한다.
func resendCmd() error {
	fs := flag.NewFlagSet("resend", flag.ContinueOnError)

	var (
		siteArg = fs.String(
			"site",
			"",
			"관측소 코드(4자리 영숫자), 쉼표로 복수. 생략 시 모든 관측소",
		)

		categoryArg = fs.String(
			"category",
			"",
			"카테고리(쉼표로 복수). 생략 시 활성 PUT 카테고리 전부. "+
				"지정 시 활성 카테고리만 허용",
		)

		fromArg = fs.String(
			"from",
			"",
			"시작 관측일 (UTC, YYYY-MM-DD 또는 YYYY-DDD). 필수",
		)

		toArg = fs.String(
			"to",
			"",
			"끝 관측일 (UTC, 양끝 포함). 필수",
		)

		dryRun = fs.Bool(
			"dry-run",
			false,
			"장부에 쓰지 않고 후보 수·분포만 관측한다",
		)

		configPath = fs.String(
			"config",
			"",
			"config.ini 경로. 미지정 시 실행파일 옆의 config.ini",
		)

		transportName = fs.String(
			"transport",
			"",
			"config [GENERAL] Transport 를 이번 실행에서만 덮어쓴다 "+
				"(sftp | localfs)",
		)
	)

	// --seed / --deep 은 정의하지 않는다 — resend 와 함께 쓸 수 없다는
	// v4 §6.3 규칙이 "미지의 플래그" 오류로 자동 강제된다.
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	if fs.NArg() > 0 {
		return fmt.Errorf(
			"알 수 없는 인자: %q",
			fs.Args(),
		)
	}

	// ── 실행 전 거부 — 여기서 통과하면 0건도 정상이다 ──────────────
	//
	// 판정 가능한 잘못(형식·범위·존재하지 않는 값)은 전부 락·접속
	// 이전에 거부한다. 실행이 시작된 뒤의 "선택 0건"은 오류가 아니라
	// 관측 결과다 (SITE §7-5, 2026-09-22 확정: exit 0 + 사유 표시).

	if *fromArg == "" || *toArg == "" {
		return fmt.Errorf(
			"--from 과 --to 는 필수다 (UTC 관측일, YYYY-MM-DD 또는 YYYY-DDD)",
		)
	}

	fromDay, err := parseObsDate(*fromArg)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}

	toDay, err := parseObsDate(*toArg)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}

	// site·category 문법은 값만으로 생략과 명시적 빈 값을 구분할 수
	// 없다. 생략은 전체 선택이고, --site="" / --category="" 는 실행 전
	// 오류다. 빈 값을 전체 선택으로 확대하면 운영자가 거른 줄 알고
	// 활성 카테고리·전 관측소를 보낸다 (MSG-INPUT-01).
	siteExplicit := false
	categoryExplicit := false

	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "site":
			siteExplicit = true
		case "category":
			categoryExplicit = true
		}
	})

	sites, err := parseSiteArg(siteExplicit, *siteArg)
	if err != nil {
		return err
	}

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

	for _, w := range cfg.Warnings {
		log.Printf("[WARN] %s", w)
	}

	// 기간 검증 (§3.3): From >= Retention 한계, To <= 오늘, From <= To.
	// origin 은 수동에 적용하지 않는다 — 운영 시작 전 누락분을 보내는
	// 정규 경로가 수동 resend 다 (§3.5). Retention 밖을 여는 옵션은 없다.
	now := time.Now()

	if err := scanwindow.ValidateManual(
		fromDay,
		toDay,
		now,
		cfg.Ledger.RetentionDays,
	); err != nil {
		// 기간은 UTC 관측일이다(경로 토큰·파일명 DOY 가 UTC). KST 오전
		// 0~9시에 한국 날짜로 "오늘"을 넣으면 UTC 로는 아직 내일이라
		// 거부된다 — 매일 아침 겪을 수 있으므로 현재 UTC 날짜를 알려 준다.
		if errors.Is(err, scanwindow.ErrToInFuture) {
			return fmt.Errorf(
				"resend 기간: %w — 기간은 UTC 관측일 기준이다. "+
					"현재 UTC 날짜는 %s 이다 (현지 시각 %s)",
				err,
				now.UTC().Format(time.DateOnly),
				now.Format("2006-01-02 15:04 MST"),
			)
		}

		return fmt.Errorf("resend 기간: %w", err)
	}

	// 카테고리: 생략 = 활성 전부, 지정 = 활성 카테고리만 허용 (§6.3).
	selected, err := selectCategories(
		cfg.Put.EnabledCategories(),
		*categoryArg,
		categoryExplicit,
	)
	if err != nil {
		return err
	}

	// ── transport (run() 의 동일 블록과 계약 공유) ─────────────────
	live := !*dryRun

	resolvedTransport := cfg.General.Transport

	if *transportName != "" {
		if !live {
			return fmt.Errorf(
				"--transport 는 live 전용이다 (--dry-run 과 함께 쓸 수 없다)",
			)
		}

		resolvedTransport = strings.ToLower(
			strings.TrimSpace(*transportName),
		)

		cfg.General.Transport = resolvedTransport

		if err := cfg.Validate(); err != nil {
			return err
		}

		if err := cfg.CheckEnvironment(); err != nil {
			return err
		}
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
	)
	defer stop()

	go func() {
		<-ctx.Done()
		stop()
	}()

	// ── 락 — 스케줄러와 달리 기다린다 (§6.3) ───────────────────────
	//
	// 락을 **접속보다 먼저** 잡는다. run() 은 Dial → Lock 순서지만 ErrHeld
	// 면 즉시 끝나므로 접속이 놀 틈이 없다. 수동은 최대 LockStaleSeconds
	// (기본 3시간)까지 기다리므로, 접속을 먼저 열면 그동안 SFTP 세션이
	// 유휴로 방치된다. 서버 idle timeout 이나 방화벽 NAT 유휴 절단으로
	// 세션이 죽은 뒤 락을 얻으면, Recover·Transfer 가 죽은 연결로 착수해
	// 파일마다 attempts 를 태우거나(FAILED 대량 기록) stall 로 끝난다.
	l, err := acquireLockWithWait(
		ctx,
		cfg.General.LedgerPath+".lock",
		cfg.General.LockStale,
	)
	if err != nil {
		return err
	}

	defer func() {
		if err := l.Release(); err != nil {
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

	var uploader put.Uploader

	runCtx := ctx

	var stalled atomic.Bool

	switch {
	case !live:
		// dry-run 은 전송 계층을 만들지 않는다.

	case resolvedTransport == "localfs":
		uploader = transport.LocalFS{}

	case resolvedTransport == "sftp":
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

		// 무진행 감시 — 발화 순서(cancelRun → Abort)는 run() 과 같은
		// 계약이다 (UNIT3 v3 §3.2). Recover 의 원격 정리도 감시 아래
		// 있어야 하므로 Dial 직후에 시작한다.
		var cancelRun context.CancelFunc
		runCtx, cancelRun = context.WithCancel(ctx)
		watchDone := make(chan struct{})
		defer func() {
			cancelRun()
			<-watchDone
		}()

		go func() {
			defer close(watchDone)
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
		return fmt.Errorf(
			"알 수 없는 transport: %q",
			resolvedTransport,
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

	// origin 이전 기간 관측 (§3.5 — 로그만, 거부 없음). 운영 시작 전
	// 날짜는 장부가 비어 전부 신규로 판정된다 — 기존 수단이 이미 보낸
	// 파일이면 중복 전송이 된다는 사실을 운영자가 알고 실행해야 한다.
	origin, err := db.OperationOrigin(ctx, now)
	if err != nil {
		return fmt.Errorf("resend: operation origin: %w", err)
	}

	if fromDay.Before(origin) {
		days := int(origin.Sub(fromDay).Hours() / 24)

		log.Printf(
			"[RESEND] before_origin=%d days (from=%s origin=%s) — "+
				"운영 시작 전 구간은 장부에 없어 전부 신규로 전송된다",
			days,
			obsDateLabel(fromDay),
			origin.Format(time.DateOnly),
		)
	}

	// 시작 로그 (SITE §5 — sites= 는 정규화 대문자, 생략은 all).
	sitesLabel := "all"
	if len(sites) > 0 {
		sitesLabel = strings.ToUpper(strings.Join(sites, ","))
	}

	catNames := make([]string, 0, len(selected))
	for _, cc := range selected {
		catNames = append(catNames, string(cc.Category))
	}

	log.Printf(
		"[RESEND] manual sites=%s categories=%s from=%s to=%s dry_run=%t budget=%d",
		sitesLabel,
		strings.Join(catNames, ","),
		obsDateLabel(fromDay),
		obsDateLabel(toDay),
		*dryRun,
		cfg.Put.MaxFilesPerRun,
	)

	jobs := make([]put.CategoryJob, 0, len(selected))

	for _, cc := range selected {
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

			// 수동 resend 의 정체성 (§4.3·§5·§6.3):
			// 게이트는 ResendMinKinds, 소진 파일은 재무장, site 는
			// 등록 전 필터. RearmExhausted 는 dry-run 에도 켠다 —
			// 미리보기와 실제가 같은 후보를 계산해야 한다.
			Resend:         true,
			RearmExhausted: true,
			Sites:          sites,
		},
	}

	if live {
		if _, err := runner.Recover(runCtx, uploader); err != nil {
			if stalled.Load() {
				return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (Recover): %w", err)
			}
			return err
		}
	}

	kept, report, err := runner.Run(
		runCtx,
		jobs,
		scan.Range{From: fromDay, To: toDay},
	)
	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (resend Run): %w", err)
		}
		return err
	}

	report.Range = "resend"

	report.Print(nil)

	// 선택 0건 — 오류가 아니라 관측 결과다 (exit 0, 메시지 문서 §3-4).
	// 잘못된 입력은 위에서 이미 거부되었다.
	//
	// site 미발견 exit 1 예외(§3-6)는 겹침 우선순위(§6 미결 1·2·3)를
	// 닫기 전에는 구현하지 않는다 — 문서 §3 의 잠금 그대로다. 여기서는
	// 판정에 필요한 관측(요청 site별 일치 수)만 전부 남긴다.
	if len(kept) == 0 {
		var entries, dirs, missing, errs, mismatch, unknown, held int

		matched := map[string]int{}

		for _, c := range report.Categories {
			entries += c.Scan.Files
			dirs += c.Scan.Dirs
			missing += c.Scan.Missing
			errs += c.Scan.Errs
			mismatch += c.SiteMismatch
			unknown += c.SiteUnknown
			held += c.SetHeld

			for s, n := range c.SiteMatched {
				matched[s] += n
			}
		}

		// MSG-SCAN-01 [확정 문구]: site 미지정 + 탐색 오류 0 + 관측
		// 엔트리 0 일 때만 쓴다. 엔트리가 하나라도 있으면(임시 파일
		// 포함) "데이터 없음"으로 단정하지 않는다 — §6-4 는 미결이다.
		//
		// 디렉터리가 하나도 안 열리고 missing 만 있으면 자료 없음과
		// 드라이브·마운트 부재가 같은 숫자다. 확정 문구는 유지하고,
		// 그 경우를 정상 자료 없음으로만 읽지 않게 경고를 덧붙인다.
		if len(sites) == 0 && errs == 0 && entries == 0 {
			log.Printf(
				"[RESEND] 해당 기간에 데이터가 없습니다. — 정상 종료 "+
					"(dirs=%d missing=%d errs=0)",
				dirs,
				missing,
			)

			if dirs == 0 && missing > 0 {
				log.Printf(
					"[RESEND][WARN] 대상 디렉터리가 모두 없습니다. " +
						"설정 경로·드라이브·마운트 상태를 확인하세요. " +
						"파일 목록만으로 저장소 연결을 정상으로 단정하지 않습니다",
				)
			}
		} else {
			// 요청 site별 발견 수를 함께 남긴다 — 오타 site 는
			// 그 site 의 0 으로 드러난다 (합계로 대신하지 않는다,
			// 문서 §5 MSG-SITE-01 선행 계약).
			//
			// errs>0 은 요청 범위를 다 보지 못한 것이다. 종료 코드는
			// 아직 미결(메시지 문서 §5 MSG-SCAN-02)이라 0 을 유지하되,
			// "정상 종료"라고 적으면 운영자가 재실행하지 않는다.
			siteInfo := ""
			if len(sites) > 0 {
				parts := make([]string, 0, len(sites))
				for _, s := range sites {
					parts = append(parts, fmt.Sprintf(
						"%s:%d",
						strings.ToUpper(s),
						matched[s],
					))
				}

				siteInfo = " site_matched=" + strings.Join(parts, ",")
			}

			outcome := "정상 종료"
			if errs > 0 {
				outcome = "탐색이 불완전하다 — 종료 코드는 0 이지만 범위를 모두 확인하지 못했다"
			}

			log.Printf(
				"[RESEND] no files to send — %s "+
					"(scanned=%d errs=%d site_mismatch=%d site_unknown=%d set_held=%d%s)",
				outcome,
				entries,
				errs,
				mismatch,
				unknown,
				held,
				siteInfo,
			)
		}

		if stalled.Load() {
			return errors.New("stall: 회차가 무진행으로 중단됨 (resend)")
		}

		return nil
	}

	if live {
		if _, err := runner.Transfer(
			runCtx,
			uploader,
			jobs,
			kept,
		); err != nil {
			if stalled.Load() {
				return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (resend Transfer): %w", err)
			}
			return err
		}
	}

	if stalled.Load() {
		return errors.New("stall: 회차가 무진행으로 중단됨 (resend)")
	}

	return nil
}

// acquireLockWithWait 는 수동 명령의 락 획득이다 (§6.3).
//
// 스케줄러(run)는 ErrHeld 에 조용히 물러나지만, 수동은 사람이 기다리고
// 있으므로 10초 간격으로 재시도한다. 상한은 설정 LockStaleSeconds 다 —
// 그보다 오래 잡혀 있는 락은 stale 탈취가 이미 가능한 상태이므로 더
// 기다리는 것은 의미가 없고, 오류로 끝내 운영자가 상황을 본다.
func acquireLockWithWait(
	ctx context.Context,
	path string,
	stale time.Duration,
) (*lock.Lock, error) {
	deadline := time.Now().Add(stale)

	var lastLog time.Time

	for {
		l, err := lock.Acquire(path, stale)
		if err == nil {
			return l, nil
		}

		if !errors.Is(err, lock.ErrHeld) {
			return nil, err
		}

		if time.Now().After(deadline) {
			return nil, fmt.Errorf(
				"resend: lock 대기가 상한(LockStaleSeconds=%s)을 넘었다 — "+
					"정시 회차가 비정상적으로 길다. 확인 후 다시 실행하라: %w",
				stale,
				err,
			)
		}

		if lastLog.IsZero() || time.Since(lastLog) >= lockWaitLogEvery {
			log.Printf(
				"[RESEND] lock held — waiting (retry=%s, cap=%s)",
				lockWaitInterval,
				stale,
			)

			lastLog = time.Now()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-time.After(lockWaitInterval):
		}
	}
}

// parseSiteArg 는 --site 의 지정 여부와 값을 해석한다 (SITE §3.1).
//
//	생략(explicit=false)        → nil (전체 관측소).
//	명시적 빈 값·공백만          → 실행 전 오류. 문법 오류를 전체 선택으로
//	                              확대하지 않는다 (MSG-INPUT-01).
//	그 외                        → ParseSiteList 가 문법을 판정한다.
func parseSiteArg(explicit bool, raw string) ([]string, error) {
	if !explicit {
		return nil, nil
	}

	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf(
			"--site: 명시적인 빈 값은 허용하지 않는다 — " +
				"전체 관측소를 원하면 --site 를 생략하라 (SITE §3.1)",
		)
	}

	sites, err := domain.ParseSiteList(raw)
	if err != nil {
		return nil, fmt.Errorf("--site: %w", err)
	}

	return sites, nil
}

// parseObsDate 는 UTC 관측일을 해석한다 (§6.3 — YYYY-MM-DD 또는
// YYYY-DDD). 두 형식 외에는 거부한다. 자정 UTC 를 돌려준다.
func parseObsDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)

	if t, err := time.ParseInLocation(time.DateOnly, s, time.UTC); err == nil {
		return t, nil
	}

	// 연-DOY: 정확히 YYYY-DDD (DDD 는 세 자리, 001~366).
	if len(s) == 8 && s[4] == '-' {
		year, errY := strconv.Atoi(s[:4])
		doy, errD := strconv.Atoi(s[5:])

		if errY == nil && errD == nil && doy >= 1 && doy <= 366 {
			t := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).
				AddDate(0, 0, doy-1)

			// 평년의 366 은 이듬해 1월 1일로 넘어간다 — 거부.
			if t.Year() == year && t.YearDay() == doy {
				return t, nil
			}
		}
	}

	return time.Time{}, fmt.Errorf(
		"잘못된 날짜 %q — YYYY-MM-DD 또는 YYYY-DDD (UTC 관측일)",
		s,
	)
}

// obsDateLabel 은 로그용 이중 표기다 (§6.3): 2026-09-01(244).
func obsDateLabel(t time.Time) string {
	return fmt.Sprintf(
		"%s(%03d)",
		t.Format(time.DateOnly),
		t.YearDay(),
	)
}

// selectCategories 는 --category 인자를 활성 카테고리로 해석한다 (§6.3).
//
//	생략(explicit=false, 빈 값) → 활성 전부.
//	명시적 빈 값                 → 실행 전 오류. 전체 선택으로 확대하지
//	                              않는다.
//	지정                         → 각 값은 유효한 카테고리이자 활성이어야
//	                              한다. 아니면 실행 전 오류다 — 비활성
//	                              카테고리를 조용히 건너뛰면 "보냈다고
//	                              생각한" 파일이 그대로 남는다.
//	중복                         → 조용히 제거 (ParseSiteList 와 같은 관용).
func selectCategories(
	enabled []config.CategoryConfig,
	raw string,
	explicit bool,
) ([]config.CategoryConfig, error) {
	if strings.TrimSpace(raw) == "" {
		if explicit {
			return nil, fmt.Errorf(
				"--category: 명시적인 빈 값은 허용하지 않는다 — " +
					"활성 전부를 원하면 --category 를 생략하라",
			)
		}

		if len(enabled) == 0 {
			return nil, fmt.Errorf(
				"활성화된 PUT Category 가 없다 (config 확인)",
			)
		}

		return enabled, nil
	}

	byCat := make(map[domain.Category]config.CategoryConfig, len(enabled))
	for _, cc := range enabled {
		byCat[cc.Category] = cc
	}

	seen := map[domain.Category]bool{}

	out := make([]config.CategoryConfig, 0)

	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf(
				"--category: 빈 항목이 있다 (%q)",
				raw,
			)
		}

		cat, err := domain.ParseCategory(name)
		if err != nil {
			return nil, fmt.Errorf("--category: %w", err)
		}

		cc, active := byCat[cat]
		if !active {
			return nil, fmt.Errorf(
				"--category: %s 는 활성 PUT 카테고리가 아니다 (config 확인)",
				cat,
			)
		}

		if seen[cat] {
			continue
		}

		seen[cat] = true

		out = append(out, cc)
	}

	return out, nil
}
