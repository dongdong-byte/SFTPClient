# SFTPClient Linux 시연 Runbook (2026-09-28 확정)

> 대상: WSL2 Oracle Linux 9.x, `~/SFTPClient` 에 바이너리·config·키·데이터가
> 이미 준비된 상태. 이 문서는 **실행 명령만** 다룬다. 설정 파일 내용과
> 데이터 배치는 이미 끝났으며, 여기 적힌 기대값은 2026-09-28 실측이다.

## 0. 전제 (한 번만 확인)

```bash
wsl -d OracleLinux_9_5            # PowerShell 에서 진입
cd ~/SFTPClient
ls                                 # SFTPClient  config.ini  config.download.ini  data  keys  logs
timeout 3 bash -c 'cat < /dev/null > /dev/tcp/192.168.0.4/9046' && echo "9046 OPEN"
date -u +%Y-%j                     # 오늘 UTC DOY — hot scan 은 오늘 포함 2일(ScanRecentDays=2)
```

- PUT: `config.ini` → 서울시 실데이터 `~/rinex/{v2d,v2h,v3d,v3h}` → SFTPGo `/RINEX2Outgoing`, `/RINEX3Outgoing`
- GET: `config.download.ini` → SFTPGo `/RINEX*_Download_Outgoing` → `~/rinex_dl`
- 두 config 는 같은 서버(192.168.0.4:9046, rinexgo)를 쓰되 원격 폴더가 겹치지 않는다.
- 바이너리 리빌드(PowerShell, 저장소 루트):
  ```powershell
  $env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="amd64"
  go build -mod=vendor -o dist\SFTPClient_linux .\cmd\rinexclient
  Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
  ```
  ```bash
  cp /mnt/c/Users/samsung/Desktop/Coding_Program/Code/SFTPClient/dist/SFTPClient_linux ~/SFTPClient/SFTPClient
  chmod +x ~/SFTPClient/SFTPClient
  ```

## 1. 시연 직전 초기화

```bash
cd ~/SFTPClient
rm -rf ~/rinex_dl
sqlite3 data/rinex_ledger.db "delete from put_ledger; delete from common_ledger;"
ls -la data                        # *.lock 이 남아 있지 않은지
```

옆 창에 로그:

```bash
tail -f ~/SFTPClient/logs/rinexclient_$(date +%Y%m%d).log
```

## 2. PUT — 정시 전송

```bash
./SFTPClient --dry-run             # 미리보기, 장부 안 씀
./SFTPClient                       # 실제 전송
./SFTPClient                       # 재실행 → 중복 방지
```

기대 (오늘=271, 데이터=268~270):

| 카테고리 | dirs | candidates | 비고 |
|---|---|---|---|
| RINEX2_DAILY | 3 | 10 | `v2d/(SITE)/(DOY)/` |
| RINEX2_HOURLY | 9 | 30 | `v2h/(SITE)/(DOY)/(HH)/` |
| RINEX3_DAILY | 3 | 6 | `v3d/(YYYY)/(SITE)/(DOY)/` |
| RINEX3_HOURLY | 9 | 18 | `v3h/(SITE)/(YYYY)/(DOY)/` |
| 합계 | | 64 | `[XFER] attempted=64 verified=64 failed=0` |

- `missing=2` 는 271 폴더가 두 관측소에 없다는 뜻. 정상.
- 재실행은 `candidates=0 excluded={verified:N}`.
- 268·269 는 hot scan 창 밖이라 여기서 안 나간다 (→ 3번).

## 3. PUT — resend (창 밖 과거분 회수)

```bash
./SFTPClient resend --from 2026-09-25 --to 2026-09-26 --dry-run
./SFTPClient resend --from 2026-09-25 --to 2026-09-26
```

기대: Daily 268·269 → RINEX2 20 + RINEX3 12 = 32건. 날짜는 UTC 관측일이며
`2026-268` 처럼 DOY 표기도 된다.

변형:

```bash
./SFTPClient resend --from 2026-09-25 --to 2026-09-26 --site DBON --dry-run
./SFTPClient resend --from 2026-09-25 --to 2026-09-26 --category RINEX3_DAILY --dry-run
./SFTPClient --deep                # ScanDays(4일) 전체를 정시 방식으로 (Deep Scan 설명용)
```

이미 보낸 것을 다시 보내는 시연은 먼저 장부를 비운다 (1번).

원격 확인(0.4 원격데스크톱): `D:\RINEX_Incoming\RINEX2Outgoing`, `…\RINEX3Outgoing`.

## 4. GET — 수신

```bash
./SFTPClient --config config.download.ini
find ~/rinex_dl -type f | sort
./SFTPClient --config config.download.ini      # 재실행 → 전부 "기존"
```

**주의: DOWNLOAD 는 `--dry-run` 이 없다.** 장부를 쓰지 않으므로 실제 실행이
곧 미리보기다.

기대:

| 카테고리 | 원격 구조 (0.4) | 로컬 결과 | 건수 |
|---|---|---|---|
| RINEX2_DAILY | `Daily/2026/(SITE)/270/` | `v2d/2026/270/` 평면 (관측소 폴더 제거) | 10 |
| RINEX2_HOURLY | `Hourly/2026/270/` 평면 | `v2h/(SITE)/2026/270/(HH)/` 생성 | 40 |
| RINEX3_DAILY | `Daily/(SITE)/270/` | `v3d/2026/270/(SITE)/` (연도 생성, 관측소 아래로) | 6 |
| RINEX3_HOURLY | `Hourly/2026/270/` 평면 | `v3h/(SITE)/2026/270/(HH)/` 생성 | 24 |
| 합계 | | | 80 |

콘솔 한 줄 형식: `[DOWNLOAD] 종류=… 범위=최근 원격=N 수신=N 소요=…`,
재실행은 `원격=N 기존=N`.

## 5. GET — 재수신 / 필터 (resend 대체)

```bash
rm -rf ~/rinex_dl/v3h/EOCH                       # 일부만 지우고
./SFTPClient --config config.download.ini        # 그 부분만 다시 받음 (수신=12 기존=…)

./SFTPClient --config config.download.ini --deep # ScanDays 범위 회수

sed -i 's/^Sites =.*/Sites = EOCH/' config.download.ini
rm -rf ~/rinex_dl && ./SFTPClient --config config.download.ini     # EOCH 만
find ~/rinex_dl -type f | grep -c GAGE           # 0
sed -i 's/^Sites =.*/Sites =/' config.download.ini                 # 원복
```

0.4 에 관측소 폴더를 하나 더 만든 뒤 재실행하면
`원격=6 수신=3 기존=3` 처럼 "새 것만 받는다"가 한 줄로 나온다.

## 6. 확인·진단 명령

```bash
tail -40 logs/rinexclient_$(date +%Y%m%d).log
sqlite3 data/rinex_ledger.db "select category,file_name,status,attempts from put_ledger order by file_name;"
sqlite3 data/rinex_ledger.db "select 'put',count(*) from put_ledger union all select 'common',count(*) from common_ledger;"
grep 'RINEX2_DAILY' logs/rinexclient_$(date +%Y%m%d).log | tail   # GET 상세(폴더밖파일 등)
```

원격 직접 확인 (같은 키):

```bash
sftp -P 9046 -i keys/id_ed25519 -o UserKnownHostsFile=keys/known_hosts rinexgo@192.168.0.4 << 'EOF'
ls /RINEX3Outgoing
ls /RINEX3_Download_Outgoing/Daily/*/*
EOF
```

## 7. 오늘 겪은 오류와 해석

| 증상 | 원인 | 조치 |
|---|---|---|
| `Mode = download 에서는 --dry-run 을 쓸 수 없다` | v4 범위 밖 | 실제 실행으로 대체 |
| `패턴폴더없음 … 폴더밖파일 … 토큰 위치가 한 단계 어긋났을 수 있음` | RemotePath 에 `(HH)` 를 뒀는데 원격은 평면 | RemotePath 에서 `(HH)` 제거 |
| `대상없음` | 원격에 `(SITE)`/`(DOY)` 폴더 구조가 없거나 DOY 가 창 밖 | 0.4 폴더 구조 수정 또는 `--deep` |
| `(site)` 토큰 거부 | 토큰 이름은 대문자 `(SITE)` (폴더 값은 대소문자 무관) | 대문자로 |

## 8. 후속 항목 (시연 뒤)

- DOWNLOAD `--dry-run`
- `secure-set` 경고 문구의 `rinexclient.exe` 명칭 (바이너리 이름과 불일치)
- 로그 파일명 `rinexclient_YYYYMMDD.log` 명칭
- 시연에 쓴 `keys/id_ed25519` 는 zip 으로 반출된 적이 있음 → SFTPGo 공개키 교체
