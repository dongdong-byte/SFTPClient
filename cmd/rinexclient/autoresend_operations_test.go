package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"SFTPClient/internal/domain"
	"SFTPClient/internal/ledger"
	"SFTPClient/internal/lock"
)

// 각 실제 run을 별도 프로세스로 실행한다. flag·시그널·로그 전역 상태를
// 테스트 사이에 공유하지 않고, 프로세스 종료 후 Windows 파일 핸들도 닫힌다.
func TestAutoResendOperations(t *testing.T) {
	cases := []struct {
		name, flag, wantLog                           string
		limit                                         string
		fresh, gate, failPut, held, noHot, exhaustOld bool
		failResend, partialPut                        bool
		wantHot, wantOld, wantError                   bool
	}{
		{name: "put_before_resend", limit: "4000", wantHot: true, wantOld: true, wantLog: "budget=3999"},
		{name: "budget_exhausted", limit: "1", wantHot: true, wantLog: "skipped (budget=0"},
		{name: "remaining_budget", limit: "2", wantHot: true, wantOld: true, wantLog: "budget=1"},
		{name: "first_live", limit: "4000", fresh: true, wantHot: true, wantLog: "before origin="},
		{name: "dry_budget_exhausted", limit: "1", flag: "--dry-run", wantLog: "skipped (budget=0"},
		{name: "dry_remaining", limit: "2", flag: "--dry-run", wantLog: "budget=1"},
		{name: "seed_excludes_resend", limit: "4000", flag: "--seed", wantLog: "mode=seed"},
		{name: "deep_then_resend", limit: "4000", flag: "--deep", wantHot: true, wantOld: true, wantLog: "range=deep"},
		{name: "minimum_kinds_wired", limit: "4000", gate: true, wantOld: true, wantLog: "range=resend"},
		// 파일 단위 FAILED 는 예전 PUT 과 같이 종료 코드 0 이다. ②만 건너뛴다.
		{name: "put_failure_blocks_resend", limit: "4000", failPut: true, wantLog: "skipped (put error"},
		// ①이 일부라도 성공하면 원격은 살아 있다 — 실패 1건으로 ②를 막지 않는다.
		{name: "partial_put_failure_still_resends", limit: "4000", failPut: true, partialPut: true, wantOld: true, wantLog: "[RESEND] auto"},
		{name: "resend_failure_keeps_exit_0", limit: "4000", failResend: true, wantHot: true, wantLog: "failed=2"},
		{name: "held_lock_blocks_both", limit: "4000", held: true, wantLog: "[LOCK]"},
		{name: "empty_put_still_resends", limit: "4000", noHot: true, wantOld: true, wantLog: "budget=4000"},
		{name: "auto_does_not_rearm", limit: "4000", exhaustOld: true, wantHot: true, wantLog: "[RESEND] auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			now := time.Now().UTC()
			today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
			oldDay := today.AddDate(0, 0, -10)
			local, remote := filepath.Join(root, "local"), filepath.Join(root, "remote")
			dbPath, logDir := filepath.Join(root, "ledger.db"), filepath.Join(root, "logs")
			cfgPath := filepath.Join(root, "config.ini")
			hotName := ""
			if !tc.noHot {
				hotName = writeDaily(t, local, "hotx", today)
			}
			// 어제 파일은 막지 않은 원격 디렉터리로 가므로 ①이 일부 성공한다.
			yesterday := today.AddDate(0, 0, -1)
			yesterdayName := ""
			if tc.partialPut {
				yesterdayName = writeDaily(t, local, "hoty", yesterday)
			}
			oldName := writeDaily(t, local, "oldx", oldDay)
			// ②의 남은 예산을 초과하는 후보와 Retention 밖 후보를 준비한다.
			secondName := writeDaily(t, local, "zzzz", oldDay)
			outsideDay := today.AddDate(0, 0, -34)
			outsideName := writeDaily(t, local, "outx", outsideDay)
			if !tc.fresh {
				seedOrigin(t, dbPath, today.AddDate(0, 0, -60))
			}
			if tc.exhaustOld {
				seedExhaustedPut(t, dbPath, oldName, filepath.Join(dayDir(local, oldDay), oldName))
			}
			writeConfig(t, cfgPath, local, remote, dbPath, logDir)
			data, err := os.ReadFile(cfgPath)
			if err != nil {
				t.Fatal(err)
			}
			ini := strings.Replace(string(data), "MaxFilesPerRun = 4000", "MaxFilesPerRun = "+tc.limit, 1)
			if tc.gate {
				ini += "\n[SET.RINEX2]\nRequiredKinds = o,n\nResendMinKinds = o\n"
			}
			if err := os.WriteFile(cfgPath, []byte(ini), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.failPut || tc.failResend {
				// 오늘 원격 디렉터리만 파일로 막는다. 과거 경로는 쓰기 가능하므로
				// ① 실패 후 잘못 ②로 진행하면 실제 과거 파일이 생긴다.
				blocked := dayDir(remote, today)
				if tc.failResend {
					blocked = dayDir(remote, oldDay)
				}
				if err := os.MkdirAll(filepath.Dir(blocked), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.held {
				l, err := lock.Acquire(dbPath+".lock", 3*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := l.Release(); err != nil {
						t.Error(err)
					}
				})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAutoResendOperationProcess$")
			cmd.Env = append(os.Environ(), "RINEX_TEST_CONFIG="+cfgPath, "RINEX_TEST_FLAG="+tc.flag)
			if tc.wantError {
				cmd.Env = append(cmd.Env, "RINEX_TEST_ERROR=1")
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("child: %v\n%s", err, out)
			}
			logs := strings.Join(readLogLines(t, logDir), "\n")
			if !strings.Contains(logs, tc.wantLog) {
				t.Fatalf("missing %q\n%s", tc.wantLog, logs)
			}
			if hotName != "" && remoteExists(remote, today, hotName) != tc.wantHot {
				t.Fatalf("hot transfer mismatch\n%s", logs)
			}
			if remoteExists(remote, oldDay, oldName) != tc.wantOld {
				t.Fatalf("resend transfer mismatch\n%s", logs)
			}
			if remoteExists(remote, outsideDay, outsideName) {
				t.Fatal("Retention 밖 파일 전송")
			}
			if tc.limit == "2" && remoteExists(remote, oldDay, secondName) {
				t.Fatal("합산 예산 초과")
			}
			if tc.wantHot && tc.wantOld {
				xfer := strings.Index(logs, "[XFER]")
				start := strings.Index(logs, "[RESEND] auto")
				if xfer < 0 || start <= xfer {
					t.Fatalf("PUT 완료 전에 resend 시작\n%s", logs)
				}
			}
			if yesterdayName != "" && !remoteExists(remote, yesterday, yesterdayName) {
				t.Fatalf("①의 막히지 않은 파일이 전송되지 않았다\n%s", logs)
			}
			if tc.flag == "--seed" || tc.held || (tc.failPut && !tc.partialPut) {
				if strings.Contains(logs, "[RESEND] auto") {
					t.Fatalf("resend must not start\n%s", logs)
				}
			}
			if tc.exhaustOld && remoteExists(remote, oldDay, oldName) {
				t.Fatalf("자동 resend 가 소진 파일을 재무장해 보냈다\n%s", logs)
			}
			if tc.flag == "--dry-run" {
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var count int
				if err := db.QueryRow("SELECT (SELECT count(*) FROM common_ledger) + (SELECT count(*) FROM put_ledger)").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("dry-run wrote ledger rows: %d", count)
				}
			}
		})
	}
}

func TestAutoResendOperationProcess(t *testing.T) {
	path := os.Getenv("RINEX_TEST_CONFIG")
	if path == "" {
		t.Skip("subprocess helper")
	}
	os.Args = []string{"rinexclient", "--config", path}
	if f := os.Getenv("RINEX_TEST_FLAG"); f != "" {
		os.Args = append(os.Args, f)
	}
	err := run()
	if (err != nil) != (os.Getenv("RINEX_TEST_ERROR") == "1") {
		t.Fatalf("run() = %v", err)
	}
}

// seedExhaustedPut 은 디스크 파일과 size·mtime 이 같은 FAILED 소진 행을
// 심는다. 자동 ②가 RearmExhausted 없이 이 파일을 보내면 안 된다.
func seedExhaustedPut(t *testing.T, ledgerPath, fileName, diskPath string) {
	t.Helper()

	st, err := os.Stat(diskPath)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	db, err := ledger.Open(ctx, ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	name := domain.NormalizeName(fileName)
	base := domain.BaseName(fileName)
	setKey, kind, ok, err := domain.SetKeyKind(domain.CategoryRINEX2Daily, name)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatalf("세트 키를 파생하지 못했다: %s", name)
	}

	raw, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	mtime := st.ModTime().Unix()
	now := time.Now().Unix()
	if _, err := raw.ExecContext(
		ctx,
		`INSERT INTO common_ledger
		   (file_name, base_name, category, size, mtime, origin,
		    revision, state, first_seen, ingress_verified_at,
		    set_key, kind, content_hash)
		 VALUES (?, ?, 'RINEX2_DAILY', ?, ?, 'LOCAL',
		         1, 'READY', ?, ?, ?, ?, '');`,
		name, base, st.Size(), mtime, now, now, setKey, kind,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := raw.ExecContext(
		ctx,
		`INSERT INTO put_ledger
		   (category, file_name, revision, status, attempts)
		 VALUES ('RINEX2_DAILY', ?, 1, 'FAILED', 5);`,
		name,
	); err != nil {
		t.Fatal(err)
	}
}
