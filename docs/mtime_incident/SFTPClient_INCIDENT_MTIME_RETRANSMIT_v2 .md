# SFTPClient — RINEX 전송 장애 기록 및 MVP2 반영 결정

> **한 줄 요약:** ① 상류 rinexSend가 mtime을 보존하지 않아(`PreserveTimestamp=$false`) SFTPClient가 동일 파일을 전량 "변경됨"으로 판정·대량 재전송한 문제는 rinexSend 한 줄 수정으로 해소(**RNX2 정상화, changed=0**). ② 그러나 **RNX3는 여전히 지리원 QC error 폴더에 격리**되며, RNX2는 rev 1~2인데 RNX3만 rev≈24로 폭주한다. mtime은 RNX2·RNX3를 동일하게 때리므로 **RNX3 장애는 mtime으로 설명되지 않으며, RNX3 전용의 별도 원인(유력: 전송 완료 미확정)이 존재한다.**

- **발생일:** 2026-09-16
- **환경:** 국립해양측위정보원(측위원) → 국토지리정보원(지리원) RINEX 전송 체인
- **상태:** ① mtime 재전송 = 조치 완료(RNX2) / ② RNX3 장애 = **조사 중(미해결)**
- **문서 표기:** `[확정]`(로그·코드·쿼리 검증) · `[관측-미검증]`(사진·로그 없음, 기억 기반) · `[가설]` · `[제약]`(볼 수 없는 것)

---

## 1. 전송 체인 구조

```
[rinexSend 서버]  E:\RINEX-V2-D / V2-H / V3-D / V3-H
   │  Send_RINEX_SFTP.ps1  (WinSCP, 7일치 매시간 롤링 업로드)
   │  → /RINEX2Outgoing/{RNX2_D, RNX2_H, RNX3_D, RNX3_H}
   ▼
[SFTPClient 서버] E:\RINEX2Outgoing\...\yyyy\doy\
   │  rinexclient.exe  (Go, PUT, ledger 기반 변경/완료 판정)
   │  내부 카테고리: RINEX2_DAILY/HOURLY, RINEX4_DAILY/HOURLY
   ▼
[지리원]  /RNX2, /RNX3  →  QC 처리 프로그램  →  정상: 서비스 / 실패: error 폴더 격리
```

- rinexSend: 대표 작성. 보안 개편(평문 비밀번호 → DPAPI + SSH 키)은 담당자(동현) 수행.
- SFTPClient: 담당자 단독 개발(Go). MVP2(PUT) 단계.
- **버전 매핑 주의 `[가설]`:** 내부 카테고리는 `RINEX2_*`·`RINEX4_*`, 현장/타겟 명칭은 `RNX2`(V2)·`RNX3`(V3). "RINEX4_* ↔ RNX3" 매핑이 코드/config에서 올바른지 **미확인**.

---

# Part 1 — mtime 재전송 (RNX2, 조치 완료)

## 2. 증상

- SFTPClient 스캔 후보 비정상 과다: `total candidates=7438`, 대부분 `changed`.
  - 예) `RINEX2_HOURLY: unchanged=2 new=156 changed=4338`
- `common_ledger`: `CHANGED=112,573`, `READY=6,504` → **94.5%가 CHANGED**.
- `put_ledger` VERIFIED=645,364 → 파일당 평균 약 5.4 revision.
- 스케줄러를 30분 전 중단했는데도 후보 7,000개 → "다운타임 누적"으로 설명 불가.

## 3. 진단 과정 `[확정]`

1. **new vs changed 분리:** 신규 ~249개뿐, 나머지 ~7,185개가 changed → 이미 아는 파일이 재변경으로 잡힘.
2. **판정 코드** — `internal/put/runner.go:533`
   ```go
   mtime := e.MTime.UTC().Unix()                 // 487행, 초 단위 UTC
   if exists && k.Size == e.Size && k.MTime == mtime { // → unchanged
   ```
   **changed = size 또는 mtime이 장부와 다름.** 카테고리 무관 동일 적용. 코드는 설계대로 동작(버그 아님).
3. **결정적 증거:** `E:\RINEX2Outgoing\RNX2_D\2026\253\ANHN2530.260.gz`
   - 경로상 관측일 DOY 253 = **9/10**(6일 전 확정 데일리), `LastWriteTime = 오늘 15:32` → 옛 파일 mtime이 오늘로 갱신됨.
4. **rev=24의 의미:** DOY 251(8일 전)이 rev=24. 7일 롤링 매시간 반복 → 도착 후 약 24시간 매시간 재전송. 내용 변경 아닌 mtime 갱신에 의한 순수 헛전송(산술 정합).
5. **상류 코드** — `Send_RINEX_SFTP.ps1:358~360`
   ```powershell
   $TransferOptions = New-Object WinSCP.TransferOptions
   # 기존 put -nopreservetime 동작 유지
   $TransferOptions.PreserveTimestamp = $false     # ← 근본 원인
   $TransferOptions.OverwriteMode = [WinSCP.OverwriteMode]::Overwrite
   ```
   `DaysToSend=7`(오늘 포함) 롤링 + `Overwrite` + `PreserveTimestamp=$false` → 매 회차 7일치 전 파일이 업로드 시각(now) mtime으로 덮어써짐.

## 4. 근본 원인 `[확정]`

**rinexSend `Send_RINEX_SFTP.ps1:360` `PreserveTimestamp=$false`.** WinSCP 기본값 `$true`인데 명시적으로 꺼짐 — 레거시 `put -nopreservetime`을 보안 개편 때 그대로 이관(개편 시 신규 실수 아님). SFTPClient가 신뢰하는 전제 "공급자는 mtime을 유지한다"가 매 회차 깨짐. **SFTPClient(Go) 코드 결함 아님.**

## 5. 조치 및 검증 `[확정]`

- **조치:** `Send_RINEX_SFTP.ps1:360` → `$TransferOptions.PreserveTimestamp = $true` (주석도 갱신. 인코딩 UTF-8 with BOM 유지 — PS 5.1 한글 깨짐 방지).
- **검증:** 재실행 → **`changed=0`**. 원본(`E:\RINEX-V2-D`) mtime이 안정적임도 동시 증명(불안정했다면 0 불가). 스케줄러 재가동.
- **RNX2 정상화 확인:** RNX2 시간·일단위 모두 정상 전송·다운로드. rev 1~2.
- **부작용 없음 `[확정]`:** 7일 전송창(`UtcNow.Date.AddDays` → yyyy\doy 폴더)·30일 정리(`Cleanup_Target_RINEX.ps1:9` "파일 수정시각이 아니라 폴더 경로 관측일로 판단")는 날짜/폴더 기준이라 무관.

---

# Part 2 — RNX3 장애 (미해결, 조사 중) 〔갱신〕

## 10. RNX3만 문제 — mtime으로 설명되지 않는다

### 10-1. 관측 `[관측-미검증]` (사진·로그 없음, 사용자 기억 기반)

- **RNX2: 시간·일단위 모두 정상. 문제는 RNX3.**
- **rev 분포: RNX2 = 1~2, RNX3 ≈ 24.** "rev가 이상하게 많은 건 거의 다 3(RNX3)." → **정확한 값은 ledger 쿼리로 재확정 필요.**
- 지리원 QC error 폴더에 **일단위 파일 DOY 239~256(8/27~9/13)** 격리(9/14 기준).
- 어제 mtime 수정 이후 **RNX2 일단위는 정상 전송**됨. RNX3는 여전히 문제.

### 10-2. 왜 mtime이 RNX3 원인이 아닌가 `[확정 논리]`

`runner.go:533` 판정은 카테고리를 가리지 않는다. mtime이 주원인이면 RNX2·RNX3 rev가 비슷해야 한다.

| 카테고리 | rev | 상태 |
|---|---|---|
| RNX2 (RINEX2_*) | 1~2 | 정상 |
| RNX3 (RINEX4_*?) | ≈24 | error 격리 |

**같은 mtime 조건에서 결과가 갈렸다 = RNX3에는 mtime 외 별도 원인이 있다.** 어제의 "mtime 수정으로 전부 해결" 결론은 **RNX2에 한해 옳고 RNX3에는 적용되지 않는다.** rev가 계속 오른다는 것은 SFTPClient가 그 파일을 매 회차 "미완료/재변경"으로 보고 후보로 되살린다는 뜻 — RNX2가 rev 1~2에서 멈추므로 파이프라인 자체는 정상, **RNX3 경로에서만 완료 확정(VERIFIED 전이)이 안 되고 있다.**

### 10-3. 가설 (우선순위)

1. **`[가설, 유력]` RNX3 전송 완료 미확정.** 첫 진단 시 지리원 `/RNX3`에 **0바이트 `.part`가 ~25분 잔류**(전송 매달림)했음이 근거. `.part → rename` 원자 완료에 도달 못 하면 ledger가 VERIFIED로 못 넘어가 매 회차 재후보 → rev 누적. 지리원 QC는 미완성/재도착을 error 격리.
2. **`[가설]` RNX3 형식/명명.** RINEX 3.x 롱네임 `*_R_*_01D_*_MO.crx.gz`(Hatanaka). 명명·헤더·압축이 QC 검사 또는 SFTPClient 완성 판정과 어긋남.
3. **`[가설]` 카테고리↔타겟 매핑 경계 버그.** `RINEX4_*` → `RNX3` 매핑 지점에서 완료 기록이 잘못 남으면 RNX3만 재전송.
4. **`[가설]` QC 일단위 재도착 거부(H1).** RNX3 일단위 재전송을 QC가 "하루 확정 위반"으로 격리. mtime 수정으로 재도착이 멎었다면 신규는 정상화되어야 함.

**판별 질문:** *어제 `$true` 수정 이후 새로 도착한 RNX3가 아직도 error를 타는가?*
- 멎음 → 4(H1) 성격, 오늘 수정으로 닫힘.
- 계속 → 1/2/3, RNX3 전송/형식/매핑을 별도로 손봐야 함.

### 10-4. 제약 `[제약]`

- **지리원 QC 처리 프로그램:** 코드·로그·**에러 사유 접근 불가.** RNX3 error 최종 원인 확정 불가.
- **개발주임 bat:** 내용 미확인. 지리원 전송 주체가 rinexclient인지, **bat 자체 전송**인지 불명.
- **현장 접근:** 지리원에 대표 상주 → 정치적으로 현장 방문 = 프로그램 오류 시인으로 비침. **원격/사무실 진단만 가능.**
- rev 수치는 사진 없이 기억 기반 → **ledger 쿼리로 재확정 필요.**

> 정치적 프레이밍: "프로그램 전체 고장"이 아니라 "RNX2 정상 / RNX3 특정 경로 이슈" = **범위를 좁혀 관리 중**이라는 방어 논리. 데이터(카테고리별 rev)로 말하는 편이 유리.

### 10-5. 사무실에서 확인 가능한 것 (현장 안 감)

```powershell
# (1) 카테고리별 rev 분포 — RNX3(RINEX4_*)만 높은지 (핵심)
.\sqlite3 -header -column .\data\rinex_ledger.db `
  "SELECT category, MAX(revs) max_rev, ROUND(AVG(revs),2) avg_rev, COUNT(*) files
   FROM (SELECT category, file_name, COUNT(*) revs FROM put_ledger GROUP BY category, file_name)
   GROUP BY category;"

# (2) 수정 이후 RNX3가 여전히 재전송되는지 (신규 rev가 1~2로 멎었는지)
.\sqlite3 -header -column .\data\rinex_ledger.db `
  "SELECT file_name, COUNT(*) revs FROM put_ledger WHERE category LIKE 'RINEX4%'
   GROUP BY file_name ORDER BY revs DESC LIMIT 10;"

# (3) RNX3 파일의 ledger 상태 — VERIFIED로 넘어가는지, IN_PROGRESS/FAILED에 걸리는지
.\sqlite3 -header -column .\data\rinex_ledger.db `
  "SELECT status, COUNT(*) FROM put_ledger WHERE category LIKE 'RINEX4%' GROUP BY status;"

# (4) config.ini에서 RINEX4_* 의 원본/타겟 경로 매핑 확인
```

- 개발주임 **bat 내용** 확보 시: `rinexclient.exe` 런처인지 자체 WinSCP 전송인지 → 전송 주체 확정.
- 지리원 error 폴더 RNX3 파일 하나: 명명·헤더를 정상 RNX2와 대조.

---

## 6. 남은 위험 (mtime, MVP2 대상)

`$true`는 **정상 상황**만 해결한다. 다음엔 여전히 취약:
- 2~3일치 **백필** 과정에서 이미 보낸 날짜까지 재복사 → mtime 갱신.
- 원본 **백업 복원 / 볼륨 이전** → mtime 일괄 갱신.

**핵심 난점:** mtime만으로 두 경우 구별 불가.
- (A) 늦게 도착한 **진짜 새 데이터** → 전송해야 함.
- (B) 이미 보낸 것 **재복사로 mtime만 갱신** → 헛전송.

리눅스(원격) 측 발생 시 접근이 4개월 2회 수준 → **SFTPClient가 사람 개입 없이 스스로 방어해야 함.**

---

## 7. MVP2 반영 항목 (우선순위 갱신)

1. **`internal/logging` 구현 (최우선).** 현재 부재, 표준 `log` → stderr 전용. 스케줄러 실행 시 로그 미보존(이번 hung·RNX3 완료실패 추적 불가의 직접 원인). 요건(설계안 14): `log/slog` 파일 핸들러 + 일자별 + 30일 보존 + 콘솔 겸용(`io.MultiWriter`) + 라인 flush. SOLID.
2. **RNX3 완료 확정 조사·수정 (신규 최우선급).** `.part → rename` 원자 완료가 RNX3에서 왜 VERIFIED로 안 넘어가는지. MVP2 세트 원자성/전송 완료 판정과 직결.
3. **내용 기반(해시) 변경 판정.** size=·mtime≠ 인 경우만 해시로 A(진짜 변경)와 B(재복사)를 구분.
   ```
   size ≠ 장부              → 변경 확정, 전송           (해시 불필요)
   size = 장부, mtime ≠ 장부 → 해시 비교
         해시 같음 → 내용 동일 → 재전송 안 함           (B 차단)
         해시 다름 → 내용 변경 → 전송                   (A·백필 통과)
   size = 장부, mtime = 장부 → unchanged
   ```
   선결: `common_ledger`에 `content_hash` 컬럼(스키마 마이그레이션) / 해시 범위(.gz 전체 권장) / IdentityRule 영향 / `verify` 단계 조건부 계산.
4. **CHANGED 폭주 경보.** 회차 후보 중 CHANGED 비율 임계(예 80%) 초과 시 WARN. 이번엔 사람이 rev=24를 눈으로 발견 → 자동 감지로 대체.
5. **`.filepart` 인식 보강.** `IsPartFile`이 `.part`만 인식 → WinSCP 임시파일 `.filepart` 미차단(Grace가 최근 것은 거르나 고아 `.filepart` 누수). `partSuffix` 목록화(`.part`,`.filepart`) + IdentityRule 상향.

---

## 8. 설계 문서 연계 (mtime 파트)

설계 문서가 이미 예측·명시한 시나리오의 실현. (`SFTPClient_LEDGER_CONCEPT.md` 4.9)
- **대가 명시:** "대량 복사로 mtime 일괄 갱신 시 Scan 범위 전 파일 재전송 … `MaxFilesPerRun`이 방어선." → cut 방어선 예언대로 작동.
- **OR 채택 이유:** "누락은 아무도 모르게 사라지고, 헛전송은 로그에 남는다." → 헛전송이 rev로 남아 추적 가능.
- **판단 지표:** "`state='CHANGED'` 누적량이 답한다." → 94.5%가 비용 지배를 즉시 확인.

7-3(해시)는 아래 표에 세 번째 행 추가:

| 규칙 | 놓치는 것 | 대가 |
|---|---|---|
| OR (현행) | 없음 | 헛전송 있음 |
| AND | 크기 우연 일치 보정 파일 | — |
| **OR + 애매할 때 해시 (MVP2)** | **없음** | **거의 없음** |

---

## 9. 부록 — 확인용 명령/쿼리

```powershell
# 파일별 재전송 횟수 상위 (헛전송 규모)
.\sqlite3 -header -column .\data\rinex_ledger.db `
  "SELECT file_name, COUNT(*) AS revs FROM put_ledger GROUP BY file_name ORDER BY revs DESC LIMIT 10;"

# 오늘 이후 신규 DOY의 rev (수정 검증 — 1~2면 정상)
.\sqlite3 -header -column .\data\rinex_ledger.db `
  "SELECT file_name, COUNT(*) AS revs FROM put_ledger WHERE file_name LIKE '%259%' OR file_name LIKE '%258%' GROUP BY file_name ORDER BY revs DESC LIMIT 10;"

# common_ledger 상태 분포
.\sqlite3 -header -column .\data\rinex_ledger.db "SELECT state, COUNT(*) FROM common_ledger GROUP BY state;"

# 원본/타겟 mtime 확인
Get-Item "E:\RINEX-V2-D\2026\251\*.gz"            | Select-Object Name, LastWriteTime | Select-Object -First 3
Get-Item "E:\RINEX2Outgoing\RNX2_D\2026\251\*.gz" | Select-Object Name, LastWriteTime | Select-Object -First 3
```

**임시 로그 캡처(로깅 구현 전):** stderr까지 잡아야 하므로 `2>&1` 필수.
```powershell
.\rinexclient.exe 2>&1 | Tee-Object -FilePath "D:\SW\SFTPClient\logs\manual_$(Get-Date -Format yyyyMMdd_HHmmss).log"
```
