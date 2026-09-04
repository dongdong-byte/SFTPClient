# config 값 DPAPI 암호화 — 코드 설계 개요 v2

> 위치: `docs/` (Scan·Ledger 결정 문서와 같다).
> `internal/security` 에는 구현만 둔다. 패키지 안에 설계 사본을 두지 않는다.

성격: 구현 지시서가 아니라 "어떻게 짤 것인가"의 구조 문서.
v1 → v2: 교차검증 2라운드(Claude 실사 + GPT) 반영.
- 라운드 1: 엔트로피 생략 확정, foldKey 정규화, localfs 경고 조건,
  이중 오류 방지
- 라운드 2(GPT): mapConfig 배선 명시, 이중 오류 해법 교체(GPT 안 채택),
  echo 파이프 예제 삭제, transport 마스킹 전체 유예, 3커밋 + git 안전
  지침, CurrentUser 문구 교정, CGO 불필요 확인

---

## 1. 목표와 위협 모델

측위원 요구: config.ini 의 Host / User / Port 를 평문으로 저장하지 말 것.

막는 것: config.ini 파일이 PC 밖으로 나갔을 때의 자격증명 노출.
막지 않는 것 (요구 범위 밖, 스스로 확장하지 않는다):
- 같은 PC 안의 프로세스·계정 간 접근 (LocalMachine 스코프의 한계,
  관리된 정부기관 서버라는 전제에서 수용)
- 기존 오류 로그에 나타나는 접속 주소 (5절 유예 항목 참조)

## 2. 구조 — 세 조각과 그 경계

```
┌─ cmd/rinexclient ──────────────────────────────┐
│  main: security.New() 를 config.Load 에 배선     │
│  secure-set 서브커맨드: security.Protect 호출     │
└───────────────┬────────────────────────────────┘
                │ config.Protector (인터페이스)
┌─ internal/config ─────────────┐  ┌─ internal/security ──────────┐
│ Protector 인터페이스 선언       │  │ enc:base64(blob) 형식의 소유자 │
│ l.value(): 원시 값 단일 통로    │  │ DPAPI 구현 (windows 빌드 태그) │
│  (부재 + 복호화를 함께 소유)     │  │ 비 Windows: enc: 명시적 거부   │
│ 평문 경고 정책 ([PUT.SFTP]만)   │  └──────────────────────────────┘
└───────────────────────────────┘
```

경계 규칙 (SOLID / one fact, one owner):

- **`enc:` 형식(접두어, base64, DPAPI)의 소유자는 security 하나.**
  config 는 형식을 모른다 — `Resolve(value) (plain, wasEncrypted, err)`
  인터페이스만 안다. 인터페이스는 사용하는 쪽(config)이 선언한다.
- **ini 파서(ini.go)는 암호화를 모른다.** 파서는 문법만 담당.
- **"어떤 키가 자격증명인가" 라는 정책은 [PUT.SFTP] 매핑부만 안다.**
  복호화 자체는 키 무관 범용 — 값이 enc: 이면 어느 섹션 어느 키든 푼다.
  기관이 암호화 대상을 추가 요구해도 코드 수정 0줄.
- **transport 는 이번 작업에서 무변경.** (5절)

## 3. 데이터 흐름

### 3.1 설치 시 (1회, 반드시 실행될 그 PC 에서)

```
운영자 → rinexclient.exe secure-set (대화식 실행)
       → 값 입력 + Enter
       → security.Protect: DPAPI(LocalMachine|UI_FORBIDDEN) → enc:... 출력
       → 운영자가 콘솔에서 복사해 config.ini 값에 붙여넣기
       → Host / User / Port 각각 1회씩, 총 3회
```

- 공식 절차는 대화식 입력뿐이다.
  기각: `echo <값> | secure-set` — 값이 PowerShell 히스토리에 남는다.
  기각: 출력의 파일 리다이렉트 — PowerShell `>` 는 UTF-16 BOM 을 만든다
  (known_hosts 전례). 콘솔 복사가 표준.
  기각: 입력 echo 숨김(터미널 라이브러리) — 값은 패스워드가 아닌
  Host/User/Port 이며, 의존성 추가 대비 이득 없음.

### 3.2 실행 시 (매 회차)

```
main → config.Load(path, security.New())
     → Load/LoadFrom → mapConfig(f, path, p)   ★ Protector 를 관통 배선
       (loader 는 mapConfig 안에서 생성되므로 시그니처에 p 가 필요하다.
        mapConfig 를 직접 부르는 테스트 helper 도 함께 바뀐다)
     → 모든 타입 리더(str/intVal/...)가 l.value() 단일 통로 경유
       · 값이 enc: → Resolve 로 복호화 후 반환 (복호화가 타입 변환보다
         먼저 → Port = enc:... 도 intVal 이 그대로 처리)
       · 복호화 실패 → Load 오류 → main 즉시 종료 + 원인 명시
         (schema_version 불일치와 같은 방침: 조용히 잘못 돌지 않는다)
     → [PUT.SFTP] 매핑 후, Transport=sftp 이면 평문 자격증명을
       cfg.Warnings 에 축적
     → main 이 Warnings 를 [WARN] 으로 기록하고 정상 진행
```

## 4. 핵심 메커니즘의 결정 사항

### 4.1 `l.value()` — 원시 값 접근의 단일 통로 (부재 처리까지 소유)

```go
func (l *loader) value(s *iniSection, key string) (string, bool) {
    v, ok := s.get(key)
    if !ok {
        l.absent(s, key)          // 부재 오류의 유일한 생산처
        return "", false
    }

    plain, wasEnc, err := l.prot.Resolve(v)
    if err != nil {
        l.addf(...)               // 복호화 오류의 유일한 생산처
        return "", false          // 리더는 기본값 반환만 한다
    }

    if wasEnc {
        l.encrypted[foldKey(s.name)+"/"+foldKey(key)] = true
    }

    return plain, true
}
```

- **부재와 복호화 실패를 value() 가 모두 소유한다.** 타입 리더는
  `!ok → 기본값 반환` 만 남는다 (기존 `l.absent` 호출 줄 삭제).
  → 복호화 실패 시 오류가 정확히 1건만 남는다
  ("" 를 intVal 이 재파싱해 "not an integer" 가 겹치는 문제 차단).
  기각: decryptFailed 셋 + absent 억제 — value 와 absent 사이에
  셋이라는 숨은 상태 결합이 생긴다. 관심사를 한 함수에 모으는 쪽 채택.
- **foldKey 정규화 필수** — s.name 은 파일의 원문 표기를 보존하므로
  `[put.sftp]` 표기 시 정규화 없이는 저장·조회 키가 어긋나
  enc: 인데도 평문 경고가 오발된다. 두 AI 가 독립적으로 발견한
  결함 — 회귀 테스트 필수 지점.

### 4.2 평문 경고 — 정책의 위치와 발동 조건

- 위치: [PUT.SFTP] 매핑부. Host/User/Port 가 `l.encrypted`
  (foldKey 정규화 키)에 없으면 cfg.Warnings 에 추가.
- 발동 조건: **`cfg.General.Transport == "sftp"` 일 때만.**
  mapConfig 는 [PUT.SFTP] 를 Transport 분기 없이 항상 읽으므로,
  조건 없이는 localfs 검증 실행마다 경고 3건이 소음으로 뜬다.
- 경고이지 거부가 아닌 이유: 개발·localfs 테스트에서 평문이 정상 경로
  (설계안 15.1 그대로).

### 4.3 DPAPI 호출 세부

- 플래그: `CRYPTPROTECT_LOCAL_MACHINE | CRYPTPROTECT_UI_FORBIDDEN`
  (무인 스케줄 실행이므로 UI 절대 금지).
- **optionalEntropy = nil 확정.**
  기각 사유: entropy 상수는 결국 배포 폴더의 바이너리 안에 박히므로
  "같은 PC 다른 프로세스" 방어가 성립하지 않는다(난독화 수준).
  secure-set 과 로더가 상수를 공유하는 관리 포인트만 증가. 과설계.
- DPAPI 출력 blob 은 LocalAlloc 할당 → 복사 후 LocalFree 필수.
- 복호화 실패 메시지에 대표 원인("다른 PC 에서 생성된 암호문일 가능성,
  이 PC 에서 secure-set 재실행")을 함께 적는다.
- 신규 코드는 복호화된 평문을 어떤 로그에도 새로 찍지 않는다.

## 5. 범위 밖 확정 — transport 로그 마스킹은 이번에 하지 않는다

기존 오류 경로(known_hosts 힌트, dial/handshake, posix-rename 프로브)에는
접속 주소가 문자열로 들어간다. 이를 지우는 작업은 이번 범위에서 제외한다.

- 요구는 "config.ini 평문 금지"다. 로그 마스킹은 요구에 없는 보안
  요건을 스스로 추가하는 것이다.
- 마스킹 대상이 한 곳이 아니라 dial/handshake 경로 전반이며(실사 확인),
  net 오류 문자열 치환은 오류 체인 평탄화를 수반한다. 잘 설계된
  체인을 요구도 없이 파괴하지 않는다.
- 이미 실전 전송이 검증된 transport 를 배포 전날 건드리는 리스크가
  마스킹 이득보다 크다.

유예 조건 기록 (known_hosts `-H` 와 같은 부류):
"기관이 로그 내 접속 주소 노출을 실제로 지적하면 별도 작업으로 수행."

## 6. 플랫폼 전략 — Linux 확장은 Protector 가 확장점

- Windows: DPAPI 구현 (`//go:build windows`)
- 비 Windows(현재): 평문 통과 + **enc: 는 명시적 거부**.
  기각: 조용히 리터럴 통과 — `enc:AQAA...` 라는 호스트명으로 접속을
  시도하는, 오류 없이 잘못 도는 부류가 된다.
- 비 Windows(추후, MVP 5): Linux 자격증명 보호를 별도 결정
  (후보: systemd-creds / kernel keyring / 파일권한 등 — 미결).
  이때 바뀌는 것은 internal/security 의 linux 빌드 태그 파일 하나뿐.
  config / main / 테스트 무변경 — 이 구조를 택한 이유가 이것이다.

## 7. 신규 의존성

없음. `golang.org/x/sys/windows` v0.47.0 이 vendor 에 이미 존재
(CryptProtectData / CryptUnprotectData / DataBlob / CRYPTPROTECT_* /
LocalFree 확인 완료). go.mod 무변경.
DPAPI 는 순수 syscall 이므로 **CGO/MinGW 불필요** — 기존 mingw 요구는
`-race` 전용이었고 이번 작업은 동시성 변경이 없어 해당 없음.
현장 Windows 에서 `go test ./... -count=1` 로 충분하다.

## 8. 커밋 계획과 Git 안전 지침

**현재 working tree 는 깨끗하지 않다** (서울시 HourLayout 수정으로
config.example.ini, internal/config/config.go 포함 13개 파일 변경 상태 —
이번 작업이 그중 2개를 다시 건드린다).

순서:
0. **보안 작업 착수 전에 서울시 수정분을 먼저 커밋**하여 tree 를
   깨끗하게 만든다. (섞임 원천 차단 — 가장 확실한 방법)
1. `security: add DPAPI config value protection`
   — internal/security 전체 + 테스트
2. `config: resolve encrypted values during load`
   — Protector 선언, Load/LoadFrom/mapConfig 배선, value() 단일 통로,
   리더 치환, 평문 경고, config 테스트
3. `cli: add secure-set provisioning command`
   — main 배선 + secure-set, config.example.ini 주석, 문서 최소 반영

실행 AI(Cursor) 에게 명시할 안전 지침:
"기존 working tree 변경사항을 되돌리거나 덮어쓰지 말 것.
보안 관련 변경만 추가할 것. `git restore` / `git checkout` /
`git reset` 을 임의로 실행하지 말 것."

## 9. 테스트 전략

- **security**: 빌드 태그로 분리.
  Windows 회차 — DPAPI 왕복, 손상 base64 거부, 평문 통과.
  Linux 회차 — enc: 거부, 평문 통과. 공통 — isEncrypted 판정.
- **config**: 테스트 전용 fakeProtector (`fake:` 성공 / `bad:` 실패 /
  그 외 통과). 프로덕션 API 로 export 하지 않는다.
  - `Port = fake:2222` → 정수 2222. **복호화→타입변환 순서의
    회귀 가드, 이번 작업의 #P1.**
  - `[put.sftp]` 소문자 섹션 + enc 값 → 평문 경고 없음 (foldKey 가드)
  - `bad:` → Load 오류 정확히 1건 (이중 오류 가드)
  - Transport=localfs + 평문 → 경고 0건
  - Transport=sftp + 평문 3키 → 경고 3건
  - nil Protector → 배선 누락 오류
  - 부재 키 → absent 오류 1건 (value 로 이관 후에도 기존 동작 유지 가드)

## 10. 수동 검증 (배포 전, Windows 실기)

1. secure-set 3회로 Host/User/Port 암호화 → config.ini 반영 →
   SFTPGo 전송 정상
2. Warnings 미출력 확인 (3키 모두 enc:)
3. enc: 값 1글자 훼손 → 즉시 종료 + 복호화 원인 로그 1건
4. config.ini 를 타 PC 복사 → 즉시 종료 + "다른 PC" 안내
5. 신규 로그 출력에 복호화된 평문이 나타나지 않음
   (기존 오류 로그의 주소 노출은 5절에 따라 검증 대상 아님)

## 11. 확정·기각 대장 (누적)

| 결정 | 기각된 대안 | 사유 |
|---|---|---|
| enc: 인라인 (config.ini 유지) | secure.dat 분리 | 설정 소유자 분열, 요구는 "평문 금지"일 뿐 |
| DPAPI | 자체 키 대칭 암호화 | 키가 바이너리에 노출되는 난독화 |
| LocalMachine 스코프 | CurrentUser | 암호화 계정과 실행 계정이 달라지면 복호화 실패로 재프로비저닝이 필요 — 운영 계정 결합도 증가 (fail-fast 라 무증상은 아님) |
| entropy 생략 | entropy 상수 내장 | 바이너리 동봉 배포에서 방어 불성립, 관리 포인트 증가 |
| 키 무관 범용 복호화 | 키별 복호화 정책 | 키 추가마다 로더 수정 발생 |
| enc: 거부 (비 Windows) | 조용히 리터럴 통과 | 오류 없이 잘못 도는 부류 |
| value() 가 부재+복호화 소유 | decryptFailed 셋 + absent 억제 | 함수 간 숨은 상태 결합 제거, 오류 1건 보장 |
| 대화식 secure-set 만 공식 | echo 파이프 | 값이 셸 히스토리에 잔류 |
| transport 무변경 | 오류 로그 주소 마스킹 | 요구 범위 밖, 체인 평탄화 수반, 검증된 코드 변경 리스크. 기관 지적 시 별도 작업 |
| 선커밋으로 tree 정리 | dirty tree 위에서 작업 | 서울시 수정과 보안 커밋 섞임 위험 |
