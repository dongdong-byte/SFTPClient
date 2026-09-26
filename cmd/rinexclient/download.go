package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"SFTPClient/internal/config"
	"SFTPClient/internal/download"
	"SFTPClient/internal/lock"
	"SFTPClient/internal/scan"
	"SFTPClient/internal/transport"
)

// DOWNLOAD 커밋 7 — main 배선 (DOWNLOAD_COMMIT_PLAN v1 §8).
//
// 배선만 둔다. 나열·판정·수신은 download 가, 접속은 transport 가 한다.
// PUT 경로(main.go)는 바꾸지 않는다 — run() 이 Mode=download 일 때만 이
// 파일로 들어온다.

// downloadConn 은 DOWNLOAD 회차가 원격 연결에 요구하는 능력이다.
// 운영에서는 *transport.SFTPFS 가 만족한다.
type downloadConn interface {
	download.RemoteFS
	transport.ProgressSource
	Abort()
	Close() error
}

// dialDownloadFunc 는 DOWNLOAD 접속 함수다. 테스트가 in-process 대역을
// 넣기 위해 주입점으로 둔다 — run() 은 한 테스트 프로세스에서 한 번만
// 부를 수 있어(flag.CommandLine) 배선 검증을 run() 에 맡길 수 없다.
type dialDownloadFunc func(transport.SFTPDialOptions) (downloadConn, error)

// errDownloadPartial은 회차 자체는 끝까지 수행했지만 파일 수신 또는 원격
// 나열 실패가 하나 이상 있었음을 뜻한다. main이 비정상 종료 코드로 바꾸되,
// 개별 원인은 앞서 출력한 DOWNLOAD 보고에 남는다.
var errDownloadPartial = errors.New("download: partial failure")

// dialDownloadSFTP 는 운영 접속이다. posix-rename 검사가 없는 읽기 전용
// 접속이다 (DialSFTPReadOnly — DOWNLOAD 는 원격 Rename 을 쓰지 않는다).
func dialDownloadSFTP(o transport.SFTPDialOptions) (downloadConn, error) {
	sf, err := transport.DialSFTPReadOnly(o)
	if err != nil {
		// nil *SFTPFS 를 인터페이스에 담아 돌려주면 호출자의 nil 비교가
		// 거짓이 된다. 오류일 때는 명시적으로 nil 을 돌려준다.
		return nil, err
	}

	return sf, nil
}

// checkDownloadFlags 는 Mode=download 에서 쓸 수 없는 실행 인자를 거부한다
// (커밋 계획 §1.3).
//
//	--seed       PUT 장부 초기화 기능이다. DOWNLOAD 에는 장부가 없다
//	--dry-run    이번 범위 밖이다. 필요하면 별도 기능으로 설계한다
//	--transport  sftp 외에는 원격 구현이 없다
//
// --deep 은 허용한다 (ScanDays 범위를 강제한다).
func checkDownloadFlags(dryRun, seed bool, transportName string) error {
	if seed {
		return errors.New(
			"Mode = download 에서는 --seed 를 쓸 수 없다 " +
				"(seed 는 PUT 장부 초기화 기능이다)",
		)
	}

	if dryRun {
		return errors.New(
			"Mode = download 에서는 --dry-run 을 쓸 수 없다 " +
				"(DOWNLOAD dry-run 은 아직 없다)",
		)
	}

	switch t := strings.ToLower(strings.TrimSpace(transportName)); t {
	case "", "sftp":
		return nil
	default:
		return fmt.Errorf(
			"Mode = download 에서는 --transport=%s 를 쓸 수 없다 (sftp 만 지원)",
			t,
		)
	}
}

// downloadRange 는 이번 회차의 날짜 범위와 이름이다.
//
// PUT 의 Hot/Deep 판정(main.go)과 같은 규칙·같은 로그 문구다. PUT 코드를
// 이번 커밋에서 건드리지 않기 위해 여기 따로 둔다 — 규칙을 바꿀 때는 두
// 곳을 함께 고친다.
func downloadRange(
	cfg *config.Config,
	deepFlag bool,
	now time.Time,
	l *log.Logger,
) (scan.Range, download.Range) {
	autoDeep := now.Hour() == cfg.Scan.DeepScanHour
	isDeep := deepFlag || autoDeep

	days := cfg.Scan.RecentDays
	name := download.RangeHot

	if isDeep {
		days = cfg.Scan.Days
		name = download.RangeDeep

		switch {
		case deepFlag && autoDeep:
			l.Printf(
				"[SCAN] deep run (--deep, DeepScanHour=%d 도 일치)",
				cfg.Scan.DeepScanHour,
			)

		case autoDeep:
			l.Printf(
				"[SCAN] deep run (DeepScanHour=%d matched)",
				cfg.Scan.DeepScanHour,
			)

		default:
			l.Printf("[SCAN] deep run (--deep 수동 강제)")
		}
	}

	return scan.Range{
		From: now.AddDate(0, 0, -(days - 1)),
		To:   now,
	}, name
}

// runDownload 는 DOWNLOAD 한 회차다 (커밋 계획 §8.2).
//
//	lock 획득 → 읽기 전용 접속 → stall 감시 → download.Runner.Run
//	→ 보고 → 접속 종료 → lock 해제
//
// lock 을 접속보다 먼저 잡는다. 앞 회차가 아직 돌고 있으면 SSH 접속을 만들지
// 않고 끝난다. (PUT 은 접속 → lock 순서이며 그대로 둔다.)
//
// PUT 장부(ledger)는 열지도 만들지도 않는다 (D12 역방향).
//
// 종료 코드 (DOWNLOAD v3 §10.3, 커밋 계획 §8.2):
//
//	0  정상 또는 lock 양보
//	1  파일 수신·원격 나열의 부분 실패, 설정·접속·lock 오류, stall, 취소
//
// console 은 화면+파일, detail 은 파일 전용 로거다 (로그 분리 합의
// 2026-09-26). detail 이 nil 이면 console 로 보낸다.
func runDownload(
	ctx context.Context,
	cfg *config.Config,
	deepFlag bool,
	now time.Time,
	dial dialDownloadFunc,
	console *log.Logger,
	detail *log.Logger,
) error {
	if console == nil {
		console = log.Default()
	}

	if detail == nil {
		detail = console
	}

	l, err := lock.Acquire(cfg.LockFile(), cfg.General.LockStale)
	if errors.Is(err, lock.ErrHeld) {
		console.Printf("[LOCK] %v — exiting", err)
		return nil
	}

	if err != nil {
		return err
	}

	defer func() {
		if err := l.Release(); err != nil {
			console.Printf("[LOCK][WARN] release: %v", err)
		}
	}()

	if l.TookOver {
		console.Printf(
			"[LOCK][WARN] took over stale lock "+
				"(pid=%d started=%s) — previous run did not exit cleanly",
			l.Prev.PID,
			l.Prev.Started.Format(time.RFC3339),
		)
	}

	s := cfg.Download.SFTP

	sf, err := dial(transport.SFTPDialOptions{
		Host:           s.Host,
		Port:           s.Port,
		User:           s.User,
		PrivateKeyPath: s.PrivateKey,
		KnownHostsPath: s.KnownHosts,
	})
	if err != nil {
		return err
	}

	defer func() {
		if err := sf.Close(); err != nil {
			console.Printf("[SFTP][WARN] close: %v", err)
		}
	}()

	// 무진행 감시 — PUT 과 같은 배선이다 (main.go, UNIT3 v3 §3.1~§3.3).
	// 발화 순서: cancelRun(새 수신 차단) → Abort(블록된 원격 호출 해제).
	var stalled atomic.Bool

	runCtx, cancelRun := context.WithCancel(ctx)
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
			s.StallTimeout,
			func(inFlight int64, quiet time.Duration) {
				stalled.Store(true)
				console.Printf(
					"[STALL] 원격 무진행 %s (진행 중 작업 %d개, 문턱 %s) — "+
						"신규 착수를 차단하고 SSH 연결을 닫는다",
					quiet.Truncate(time.Second),
					inFlight,
					s.StallTimeout,
				)
				cancelRun()
				sf.Abort()
			},
		)
	}()

	rng, rangeName := downloadRange(cfg, deepFlag, now, console)

	enabled := cfg.Download.EnabledCategories()
	jobs := make([]download.Job, 0, len(enabled))

	for _, cc := range enabled {
		jobs = append(jobs, download.Job{
			Category:   cc.Category,
			RemotePath: cc.RemotePath,
			LocalPath:  cc.LocalPath,
		})
	}

	if len(jobs) == 0 {
		return errors.New("활성화된 DOWNLOAD Category 가 없다 (config 확인)")
	}

	runner := &download.Runner{
		Remote: sf,
		Local:  download.OSLocalStore{},
		Opts: download.Options{
			MaxWorkers:   cfg.Download.MaxWorkers,
			Grace:        cfg.Download.Grace,
			Sites:        cfg.Download.Sites,
			Logger:       console,
			DetailLogger: detail,
		},
	}

	rep, err := runner.Run(runCtx, jobs, rng)

	// 취소·stall 로 끝나도 그때까지의 집계는 남긴다.
	rep.Range = rangeName
	rep.Print(console, detail)

	if err != nil {
		if stalled.Load() {
			return fmt.Errorf("stall: 회차가 무진행으로 중단됨 (download Run): %w", err)
		}

		return err
	}

	// 마지막 수신 직후에 발화하면 오류 없이 여기 도달할 수 있다.
	// PUT 과 같은 이유로 성공으로 위장하지 않는다 (UNIT3 v3 §3.7).
	if stalled.Load() {
		return errors.New("stall: 회차가 무진행으로 중단됨 (download)")
	}

	if failed := rep.Failed(); failed > 0 {
		return fmt.Errorf("%w: 실패=%d", errDownloadPartial, failed)
	}

	return nil
}
