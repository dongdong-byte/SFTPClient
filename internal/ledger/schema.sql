-- =============================================================================
--  rinex_ledger.db  /  Ledger 스키마
-- =============================================================================
--  설계 원칙 (설계안 9)
--    "하나의 사실에는 하나의 주인만 둔다."
--    파일의 정체성은 common_ledger 가, 송신 이력은 put_ledger 가 소유한다.
--    두 테이블은 같은 사실을 중복 저장하지 않고
--    (category, file_name) 으로 상호 검증한다.
--
--  식별자 정책 (설계안 9.1 에서 개정)
--    원안은 SHA-256( Domain │ Category │ NormalizedName ) 해시였으나,
--    RINEX3 파일명이 관측소·시각·기간·샘플링·데이터타입을 모두 포함하는
--    자기서술적 형식이므로 파일명 자체가 유일하다.
--        정규화 전 : SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz
--        정규화 후 : sonp00kor_r_20260010300_01h_01s_ms.rnx.gz   ← 저장되는 값
--    따라서 해시를 거치지 않고 정규화한 파일명을 그대로 키로 사용한다.
--
--    원안의 목적인 "경로 비의존" 은 그대로 유지된다.
--    v5 에서 local_path 컬럼을 삭제하였으므로 경로는 이제 키도 속성도 아니다.
--    삭제 근거는 아래 common_ledger 말미의 [v5 개정 주석] 을 참조한다.
--
--    Domain 은 사용하지 않는다.
--    인스턴스별로 DB 파일이 분리되어 있어(9.3) 한 DB 안에서
--    타 기관 파일과 만날 일이 없으므로 식별자 구성요소로 필요하지 않으며,
--    최신 설계에서는 config 에서도 제거하였다.
--
--  NormalizedName 규칙
--    - 디렉터리 경로 제외, 파일명만 사용
--    - 대소문자는 소문자로 통일
--    - .part 등 임시 접미사 제거
--    - 압축 확장자는 제거하지 않고 그대로 보존
--        규칙이 "보존" 이므로 알려진 확장자 목록을 유지할 필요가 없다.
--        현장에서 .Z / .gz / .zip 이 관측소마다 혼재하는 것이 확인되었고,
--        새로운 압축 형식이 등장해도 코드를 수정하지 않는다.
--        단, .Z 는 소문자화되어 .z 가 되므로 같은 디렉터리에 .Z 와 .z 가
--        공존하면 식별자가 충돌한다. 발생 가능성은 없다고 판단하나
--        Ingress 단계에서 충돌 시 경고 로그를 남긴다.
--    이 규칙을 변경하면 누적된 Ledger 이력 전체가 무효화된다.
--    변경 여부를 프로그램이 감지할 수 있도록 schema_meta.identity_rule 에 기록한다.
--
--  접속 시 필수 PRAGMA
--    PRAGMA foreign_keys = ON;      -- 기본값이 OFF 이다. 켜지 않으면 아래
--                                   -- FOREIGN KEY 선언과 ON DELETE CASCADE 가
--                                   -- 아무 일도 하지 않고 고아 행이 남는다.
--    PRAGMA journal_mode = WAL;     -- 조회가 전송 중 쓰기를 막지 않게 한다. (9.3)
--    PRAGMA synchronous  = NORMAL;  -- (9.3)
--    PRAGMA busy_timeout = 5000;    -- (9.3)
--
--  본 파일은 스키마의 원본이다. DB 파일은 이 파일에서 파생된 산출물로 취급한다.
--  ALTER TABLE 수행 시 SQLite 가 저장된 CREATE 문을 재작성하면서 주석이
--  손실될 수 있으므로, 스키마 변경은 반드시 이 파일을 먼저 수정한다.
--
--  주석은 sqlite_master.sql 에 원문 그대로 보존되며 다음으로 확인한다.
--    SELECT sql FROM sqlite_master WHERE type='table';
--
--  개념·논리 모델의 근거는 SFTPClient_LEDGER_CONCEPT.md 에 별도로 남긴다.
--  본 파일은 그 결론의 물리 구현이다. 두 문서는 함께 갱신한다.
--  v5 결정 경위는 SFTPClient_SCAN_DESIGN_DECISIONS.md 6절에 있다.
--
--  개정 이력
--    v1  최초 (해시 file_id)
--    v2  식별자를 정규화 파일명으로 변경, Domain·Category 를 키에서 제외
--    v3  CHECK 제약 전면 적용, part_path·base_name·schema_meta 추가
--    v4  verified_at 을 ingress_/transfer_ 로 분리
--        state 를 필터에서 관측 용도로 재정의 (후보 판정은 revision 매칭)
--        후보 선정 인덱스를 (category, origin, state) 순서로 교정
--    v5  local_path 컬럼 삭제
--        후보 선정을 DB 주도에서 Scan 주도로 변경
--        (파괴적 변경이다. 아래 마이그레이션 주석 참조)
--    v6  common_ledger.category CHECK 에
--        RINEX4_DAILY / RINEX4_HOURLY 추가
--        schema_meta.schema_version 을 '2' → '3' 으로 올린다
--        (CREATE TABLE IF NOT EXISTS 는 기존 CHECK 를 바꾸지 않으므로
--         개발 DB 는 삭제 후 재생성한다)
--    v7  2026-08-30  식별자를 (category, file_name) 복합키로 변경.
--        RINEX3/RINEX4 는 동일 long filename 을 가질 수 있어
--        file_name 단독 PK 에서는 서로 revision 을 올리고 category 를
--        덮어쓰는 영구 재전송 루프가 된다. put_ledger 도
--        (category, file_name, revision) 으로 맞추고 schema_version 3 → 4.
--    v8  2026-09-10  common_ledger 에 set_key·kind 추가 (MVP2 세트 원자성).
--        schema_version 4 → 5. 최초의 운영 DB 보존 전환 —
--        기존 DB 는 ledger.Open 이 자동 마이그레이션한다 (아래 절 참조).
--
--  적용 범위
--    [현재] common_ledger, put_ledger, schema_meta
--    [예정] download_ledger  (MVP3에서 추가. 설계안 9)
-- =============================================================================


-- -----------------------------------------------------------------------------
--  common_ledger
--    파일 자체의 정체성과 입고 상태를 기록한다.
--    "이 파일이 무엇인가" 에 대한 단 하나의 주인이며, 전송 이력은 두지 않는다.
--    파일 하나당 한 행을 유지하며, 재관측 시 UPDATE 로 갱신한다.
--    설계안 9
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS common_ledger (

                                             file_name   TEXT    NOT NULL
                                             CHECK (file_name = lower(file_name)),
    -- 정규화된 파일명. 경로를 포함하지 않는다.
    -- 단독 식별자가 아니라 category 와 함께 PRIMARY KEY 를 구성한다.
    -- (2026-08-30 복합키 전환)
    --
    -- DOWNLOAD LocalPath 와 PUT LocalPath 가 달라도 동일 파일명은
    -- 동일 file_name 값을 가지며, 같은 Category 안에서는 그 성질이
    -- BOTH 모드의 재전송 방지를 성립시킨다.
    --
    -- CHECK 는 소문자 정규화를 DB 차원에서 강제한다. 규칙을 지키지 않은
    -- 코드 경로가 하나라도 있으면 같은 파일이 두 행으로 등록되어
    -- 중복 전송이 발생하므로, 코드 규율에만 맡기지 않는다.
    --
    -- Scan 이 디렉터리 단위로 던지는
    --   WHERE category = ? AND file_name IN (?, ?, ...)
    -- 조회의 대상이다. 복합 PK 인덱스를 그대로 타므로
    -- file_name 단독 인덱스는 두지 않는다.

    base_name   TEXT    NOT NULL,
    -- 압축 확장자(.gz, .Z, .zip) 를 제거한 이름.
    -- 같은 관측 데이터가 .rnx 와 .rnx.gz 두 형태로 유입되는지 탐지한다.
    -- 두 형태는 바이트열도 크기도 다르므로 서로 다른 file_name 으로
    -- 등록되며(누락을 막기 위한 의도적 선택), 이 컬럼은 그 상황이
    -- 실제로 발생하는지 운영 중에 확인하기 위한 관측 수단이다.
    --   SELECT base_name, COUNT(*) FROM common_ledger
    --    GROUP BY base_name HAVING COUNT(*) > 1;
    -- 식별자가 아니므로 UNIQUE 를 걸지 않는다.

    category    TEXT    NOT NULL
    CHECK (category IN ('RINEX2_DAILY',  'RINEX2_HOURLY',
           'RINEX3_DAILY',  'RINEX3_HOURLY',
           'RINEX4_DAILY',  'RINEX4_HOURLY')),
    -- file_name 과 함께 논리 식별자(PRIMARY KEY)를 구성한다.
    -- (2026-08-30 복합키 전환)
    --
    -- 값은 Scanner 가 판정하지 않고 config.ini 의 [PUT.<CATEGORY>]
    -- 섹션에서 그대로 전달받는다.
    --
    -- RINEX3 과 RINEX4 는 파일명 규칙이 같아 같은 file_name 이
    -- 두 Category 에 동시에 존재할 수 있다. category 를 키에 넣지
    -- 않으면 두 파일이 서로를 "변경됨"으로 만들어 revision 이
    -- 끝없이 오르는 재전송 루프가 된다.
    --
    -- 한 디렉터리에 두 버전이 섞이는 상황은 상정하지 않는다.
    -- 현장에서 RINEX2 디렉터리에 RINEX3 파일이 관측된 사례가 있으나
    -- 이는 오류이며, 기관 측에서 버전별 디렉터리를 분리할 예정이다.
    --
    -- 그럼에도 verify 단계에서 config 가 지정한 category 와
    -- 파일명이 함의하는 버전·주기를 대조하여 불일치 시 Ingress 를 거부한다.
    --   RINEX3/4  긴 파일명의 _01D_ / _01H_ 필드
    --   RINEX2    SSSSDDDh.YYt 형태. h='0' 이면 Daily, 'a'~'x' 면 Hourly
    -- 혼입을 상정하지 않되 감지는 한다. 비용은 문자열 비교 하나이며,
    -- 원인 불명의 혼입이 실제로 관측된 이상 조용히 지나가게 두지 않는다.
    --
    -- 주의: 항법 파일은 관측 파일과 필드 구성이 다르다.
    --   관측  SONP00KOR_R_20260010200_01H_01S_MO.crx.gz   ← 데이터율 필드 있음
    --   항법  SONP00KOR_R_20260010000_01D_MN.rnx          ← 없음
    -- 고정 오프셋으로 자르면 항법 파일에서 어긋난다.
    -- 반드시 '_' 분리 후 필드 단위로 검사한다.
    --
    -- 정수(iota)가 아닌 문자열로 저장하여 순서 변경에 영향받지 않게 한다.

    size        INTEGER NOT NULL CHECK (size >= 0),
    -- 최근 관측된 바이트 크기. Transfer 검증의 기준값이 된다. (8.1)
    -- v5 부터 Scan 이 이 값을 읽어 디스크 실측치와 비교하여
    -- revision 증가 여부를 판정한다. 아래 후보 선정 주석 참조.

    mtime       INTEGER NOT NULL,
    -- 최종 수정시각 (Unix epoch 초, UTC).
    -- Ingress Verification 의 Grace Time 판정에 사용한다. (7)
    -- TEXT 가 아닌 INTEGER 로 두어 타임존 문제와 비교 비용을 피한다.

    origin      TEXT    NOT NULL
    CHECK (origin IN ('LOCAL', 'DOWNLOAD')),
    -- LOCAL    — 이 서버에 원래 존재하던 파일. 수신기·BNC·타 프로세스가
    --            써 넣은 것을 모두 포함한다.
    -- DOWNLOAD — 이 프로그램이 DOWNLOAD 모드로 수신한 파일.
    --            PUT 후보 선정에서 기본 제외하여 다운로드한 파일이 다시
    --            송신되는 Ping-Pong 을 방지한다. (9.1)
    --            중계 구성에서는 config 의 RepostDownloaded 로만 예외 허용한다.
    --
    -- 설계안 9.1 은 RECEIVER / LOCAL / DOWNLOAD 세 값을 예시하나,
    -- Scanner 는 디렉터리에 놓인 파일만 볼 뿐 누가 썼는지 판별할 수 없어
    -- RECEIVER 와 LOCAL 을 구분할 근거가 없고 동작 차이도 없다.
    -- 검증 불가능한 값을 남기지 않기 위해 LOCAL 로 통합한다.
    --
    -- 현재 알려진 모든 기관에서 DOWNLOAD 는 사용되지 않는다.
    -- 원본 서버와 중간 서버 양쪽에 본 프로그램을 설치할 수 있으므로
    -- 다단 구성도 PUT 두 번으로 성립한다. DOWNLOAD 는 "타 기관 소유라
    -- 설치가 불가한 서버" 가 나타날 때를 위한 예비 값이다.

    revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
    -- 동일 파일명이 서로 다른 size 또는 mtime 으로 재관측될 때 1 증가한다.
    --
    -- 실제 발생 사유는 두 가지이다.
    --   1) 관측소에서 결측 구간을 채워 파일을 재생성하는 경우 (9.1)
    --   2) 전송이 중단되어 절반짜리 파일이 남았다가 재시도로 완성되는 경우
    --
    -- 2) 가 v5 에서 새로 확인된 주된 사유이다.
    -- 우리 서버가 받는 것은 QC 를 마친 완제품이므로 관측 중 append 성장은
    -- 존재하지 않는다. 크기가 변하는 구간은 수신기가 밀어 넣는 전송 중뿐이며
    -- 수 초~수 분이다. 그러나 전송이 중간에 끊기면 그 파일은 더 이상
    -- 커지지 않으므로 Grace Time 을 통과해버린다. 원리적으로 막을 수 없다.
    -- 완성본으로 덮어써질 때 size 가 달라져 revision 이 오르고 회수된다.
    --
    -- revision 없이 UPDATE 로 덮어쓰면 과거 전송 이력을 잃고,
    -- 무조건 skip 하면 보정된 데이터가 영영 전송되지 않는다.
    --
    -- size 와 mtime 을 OR 로 본다. size 가 같고 mtime 만 달라도 올린다.
    -- 내용이 같은데 재전송하는 경우가 생기지만, 반대 방향의 실수
    -- (내용이 바뀌었는데 안 보냄)보다 낫다는 판단이다.
    -- AND 로 두면 크기가 우연히 같은 보정 파일을 놓친다.
    --
    -- 대신 볼륨 이전이나 대량 복사로 mtime 이 일괄 갱신되면
    -- Scan 범위 안의 모든 파일이 revision +1 로 재전송된다.
    -- 이는 규칙의 결함이 아니라 위 선택의 대가이며,
    -- MaxFilesPerRun 이 그때의 방어선이다.
    -- 스토리지 작업이 예정되어 있다면 사전에 그 값을 확인한다.
    --
    -- 주의: 파일명의 시각 필드가 다르면(예: ..._20260040000_ 과
    -- ..._20260041856_) 서로 다른 file_name 이므로 revision 이 아니라
    -- 별개의 신규 파일로 등록된다. revision 은 파일명이 같을 때만 쓰인다.

    state       TEXT    NOT NULL
    CHECK (state IN ('READY', 'CHANGED')),
    -- READY   — Ingress 검증 통과.
    -- CHANGED — 이 파일은 재생성·보정된 이력이 있다.
    --           revision 증가와 동시에 전환되며, 이후 되돌리지 않는다.
    --
    -- 이 컬럼은 전송 후보를 거르는 필터가 아니다.
    -- 두 값 모두 전송 대상이므로 조회 조건으로서는 하는 일이 없다.
    -- 재전송 여부는 아래 put_ledger 와의 revision 매칭이 결정한다.
    --
    -- 그럼에도 컬럼을 두는 이유는 운영 가시성이다. 결측 보정이나
    -- 전송 중단 후 재시도가 현장에서 실제로 얼마나 발생하는지는
    -- 아직 관측된 바 없고, 이 값이 그것을 답한다.
    --   SELECT COUNT(*) FROM common_ledger WHERE state='CHANGED';
    -- base_name 과 같은 성격의 관측 수단이다.
    --
    -- CHANGED 를 한 스캔 주기 대기시키는 방안을 검토했으나 채택하지 않았다.
    -- 실행 주기가 1시간이므로 두 스캔 연속으로 size 가 변한다는 것은
    -- 파일이 1시간 넘게 쓰이는 중이라는 뜻이며 실제로 발생하지 않는다.
    -- 방어 효과는 없고 보정 파일의 송신만 1시간 지연된다.
    -- 작성 중 파일 배제는 Ingress 의 Grace Time 이 담당한다. (7)
    --
    -- Ingress 를 통과하지 못한 파일(size=0, Grace Time 미충족 등)은
    -- 행을 만들지 않는다. 다음 Scan 에서 다시 판정한다. (7, 14)
    -- 따라서 이 테이블에 존재한다는 것 자체가 검증 통과를 뜻한다.

    first_seen  INTEGER NOT NULL,
    -- 이 파일을 처음 발견한 시각 (Unix epoch 초, UTC).
    -- revision 이 올라가도 갱신하지 않는다. 최초 입고 시점을 보존한다.

    ingress_verified_at INTEGER NOT NULL,
    -- Ingress Verification 을 통과한 시각 (Unix epoch 초, UTC). (7)
    -- 위 state 주석대로 미통과 파일은 행이 생기지 않으므로 NULL 이 없다.
    -- revision 이 올라가면 재검증 시각으로 갱신한다.
    --
    -- put_ledger 에도 transfer_verified_at 이 있다. 둘은 다른 검증이다.
    --   ingress  — 파일이 정상적으로 완성되었는가 (설계안 7)
    --   transfer — 목적지에 제대로 도착했는가   (설계안 8.1)
    -- 양쪽을 같은 이름으로 두면 조인 조회에서 어느 쪽 시각인지 알 수 없어
    -- 접두어로 구분한다.

    set_key     TEXT    NOT NULL DEFAULT ''
    CHECK (set_key = lower(set_key)),
    -- 이 파일이 속한 관측 세트의 키. 파일명에서 파생된 사실이다.
    --   RINEX2      dbon2500.26              (세션 문자 포함)
    --   RINEX3/4    dbon00kor_r_20262500000_01d
    -- '' 는 "세트 소속을 확정할 수 없는 이름"(유보)이다.
    -- 값의 산출처는 domain.SetKeyKind 하나이며, v4→v5 마이그레이션
    -- 백필과 Upsert 경로가 같은 파서를 쓴다.
    --
    -- COMPLETE/HELD 같은 판정 결과는 저장하지 않는다. 세트 완성
    -- 여부는 현재 정책(RequiredKinds)과 현재 파일 상태로 매 스캔
    -- 재계산한다. Ledger 는 사실만 기록한다. (MVP2 확정 §13~§14)

    kind        TEXT    NOT NULL DEFAULT ''
    CHECK (kind = lower(kind)),
    -- 세트 안에서의 데이터 종류. 소문자.
    --   RINEX2      g / l / n / o / s / z ...   (파일타입 1글자)
    --   RINEX3/4    mo / mn / ms ...            (2글자)
    -- 표현 형식(.rnx/.crx)과 압축(.gz)은 포함하지 않는다 (§11).
    -- '' 는 set_key 와 같은 유보 표기다.
    -- 두 컬럼 모두 식별자가 아니므로 UNIQUE 를 두지 않는다.
    -- lower CHECK 를 두는 이유는 file_name 과 같다 — 게이트가 이 값으로
    -- 그룹핑하므로 대소문자 누출은 세트 분열이라는 조용한 오류가 된다.
    -- 코드 규율에만 맡기지 않는다.

    PRIMARY KEY (category, file_name)
    -- RINEX3/RINEX4 동일 file_name 공존을 허용하면서
    -- Category 간 교차 revision 루프를 막는다. (2026-08-30)
    );

-- -----------------------------------------------------------------------------
--  [v5 개정 주석]  local_path 컬럼 삭제와 후보 선정 방식 변경
--
--  v4 는 아래와 같이 DB 가 "무엇을 어디서 보낼지" 를 모두 지시했다.
--
--    SELECT c.file_name, c.revision, c.local_path, c.size
--      FROM common_ledger c
--      LEFT JOIN put_ledger p
--        ON  p.file_name = c.file_name AND p.revision = c.revision
--     WHERE c.category = ? AND c.origin = 'LOCAL'
--       AND (p.status IS NULL OR p.status = 'FAILED');
--
--  삭제 근거
--    장부의 존재 목적은 "완제품이 잘 받아졌는가 / 잘 보내졌는가" 에 답하는
--    것이다. "어디에 있었는가" 는 그 목적에 기여하지 않는다.
--    경로는 통보와 함께 실제로 자주 바뀌며, DB 에 적어두면 코드가 그 값에
--    의존하게 되어 경로 변경이 중복 전송으로 이어질 여지가 생긴다.
--    로컬 디스크에 10년치가 보관되는 환경이므로 이 위험은 작지 않다.
--
--  v5 의 후보 선정 (Scan 주도)
--    Scan 은 디렉터리를 나열한 시점에 이미 (경로, 파일명, size, mtime) 을
--    모두 알고 있다. 장부에는 "이 이름들 중 무엇을 아직 안 보냈는가" 만 묻는다.
--
--    1) Scan 이 디렉터리 하나를 나열한다
--    2) 그 안의 파일명들로 장부를 한 번에 조회한다
--
--         SELECT c.file_name, c.revision, c.size, c.mtime, p.status
--           FROM common_ledger c
--           LEFT JOIN put_ledger p
--             ON  p.file_name = c.file_name AND p.revision = c.revision
--          WHERE c.file_name IN (?, ?, ?, ...);
--
--    3) 메모리에서 대조한다
--         장부에 없음                         → 신규.        Ingress 후 전송
--         있고 size/mtime 동일, VERIFIED      → 제외
--         있고 size/mtime 동일, NULL / FAILED → 전송 (또는 재시도)
--         있고 size/mtime 상이                → revision +1 후 전송
--
--    PK 인덱스를 그대로 타므로 별도 인덱스가 필요 없다.
--    IN 절의 항목 수는 SQLITE_MAX_VARIABLE_NUMBER(기본 32766) 이내로 나눈다.
--    관측소별 디렉터리가 없어 한 디렉터리에 수백 개가 들어오므로
--    실무상 한두 번의 조회로 끝난다.
--
--  파생 효과
--    v4 에서는 FAILED 행이 Scan 범위 밖으로 밀려나도 영구히 후보로 남았으나,
--    v5 에서는 자연히 만료된다. Deep Scan 범위(기본 7일)를 벗어나면 후보에서
--    빠진다. 기간·대상을 명시하는 향후 resend 가 수동 재전송을 담당한다.
--    자동 재시도와 운영자 지정 재전송의 경계가 명확해진다.
-- -----------------------------------------------------------------------------

-- category 별 집계·조회 경로.
--   RINEXClient.exe db status   (9.2)
--
-- v4 에서는 이 인덱스가 후보 선정의 주 경로였으나, v5 의 후보 선정은
-- PK 를 타므로 더 이상 그렇지 않다. 그럼에도 유지하는 이유는
-- category 별 집계와 origin 필터(Ping-Pong 방지 확인)에 계속 쓰이기 때문이다.
--
-- v7(복합키) 이후 PK 가 (category, file_name) 이라 category 접두가 겹치지만,
-- origin·state 필터용으로 존치한다. PK 만으로는 origin 등가 비교를 좁히지 못한다.
--
-- 컬럼 순서 주의: category 와 origin 은 등가 비교이므로 앞에 두고,
-- 선택도가 낮은 state 를 뒤에 둔다. state 를 중간에 두면 그 뒤 컬럼이
-- 인덱스로 좁혀지지 않는다.
CREATE INDEX IF NOT EXISTS idx_common_category_origin
    ON common_ledger (category, origin, state);

-- .rnx 와 .rnx.gz 동시 유입 탐지용. 위 base_name 주석의 조회에 사용한다.
CREATE INDEX IF NOT EXISTS idx_common_base_name
    ON common_ledger (base_name);


-- -----------------------------------------------------------------------------
--  put_ledger
--    송신 시도·성공·실패·검증 이력을 기록한다.
--    "이 파일을 보냈는가" 에 대한 단 하나의 주인이며, 파일의 정체성은 두지 않는다.
--    설계안 9
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS put_ledger (

                                          category    TEXT    NOT NULL
                                          CHECK (category IN ('RINEX2_DAILY',  'RINEX2_HOURLY',
                                          'RINEX3_DAILY',  'RINEX3_HOURLY',
                                          'RINEX4_DAILY',  'RINEX4_HOURLY')),
    -- common_ledger.category 와 동일한 값 집합이다.
    -- (category, file_name) 복합 FK 의 일부이므로 생략할 수 없다.
    -- (2026-08-30 복합키 전환)

    file_name   TEXT    NOT NULL,
    -- common_ledger.file_name 을 참조한다.
    -- 부모가 소문자로 강제되므로 여기에는 별도 CHECK 를 두지 않는다.

    revision    INTEGER NOT NULL CHECK (revision >= 1),
    -- 전송을 시도한 시점의 common_ledger.revision 값이다.
    -- (category, file_name, revision) 을 키로 두어 이력을 누적하므로
    -- 파일이 갱신되어 재전송되어도 과거 기록이 덮어써지지 않는다. (9.1)

    status      TEXT    NOT NULL
    CHECK (status IN ('PENDING', 'IN_PROGRESS',
           'VERIFIED', 'FAILED')),
    -- PENDING → IN_PROGRESS → VERIFIED 또는 FAILED (9.3)
    --
    -- CHECK 가 없으면 오타 한 글자('VERIFED')가 들어간 행이 성공 집계에도
    -- 실패 재시도 대상에도 잡히지 않아 파일이 조용히 누락된다.
    -- 설계안 13.2 의 "누락 0건" 기준을 깨는 가장 흔한 경로이므로 막는다.
    --
    -- 설계안 8.1 과 13.2 는 SUCCESS, 9.3 은 VERIFIED 로 표기가 엇갈린다.
    -- 상태 전이를 명시적으로 정의한 9.3 을 따라 VERIFIED 로 통일한다.
    -- 두 이름이 코드에 섞이지 않도록 domain 패키지 상수로만 참조한다.
    --
    -- 비정상 종료 시 IN_PROGRESS 로 남은 항목이 발생한다.
    -- 프로그램 시작 시 이를 조회하여 아래 part_path 의 잔여 .part 파일을
    -- 삭제하고 상태를 FAILED 로 되돌린다. 이 절차가 없으면 .part 가 누적된다.
    --
    -- v5 주의: 회수 시 로컬 경로가 필요하지 않다. 로컬 파일은 그대로 있고
    -- Scan 범위 안이라면 다음 Scan 이 다시 발견하므로, 회수 절차는
    -- 원격 .part 정리와 상태 되돌리기까지만 한다.
    --
    -- 범위 밖이면(중단 후 ScanDays 를 넘겨 방치된 경우) 자동으로는
    -- 회수되지 않는다. 이는 결함이 아니라 v5 의 의도된 성질이다.
    -- FAILED 가 영구히 후보로 남지 않고 자연히 만료되도록 한 것이며,
    -- 그런 항목의 수동 재전송은 향후 resend 가 담당한다.
    -- (SCAN_DESIGN_DECISIONS 6절 파생 효과)
    --
    -- 전송 함수가 오류 없이 끝났다는 사실만으로 VERIFIED 로 두지 않는다.
    -- 목적지 파일의 존재와 Size 대조를 마친 뒤에만 확정한다. (8.1)

    attempts    INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    -- 누적 시도 횟수. Retry 상한 판정과 장애 분석에 사용한다. (14)

    local_size  INTEGER,
    -- 전송 직전 확인한 로컬 원본 크기.
    remote_size INTEGER,
    -- 업로드 후 확인한 원격 파일 크기.
    --
    -- 두 값의 일치가 Transfer Verification 의 판정 기준이다. (8.1)
    -- "아직 측정하지 않음" 과 "0바이트" 를 구분하기 위해 NULL 을 허용한다.

    remote_path TEXT,
    -- 최종 목적지 경로. Path Template 확장 결과이다.
    --
    -- common_ledger 의 local_path 는 v5 에서 삭제했으나 이 컬럼은 남긴다.
    -- 성격이 다르기 때문이다. local_path 는 "파일이 어디 있는가" 라는
    -- 현재 상태의 사본이라 원본과 어긋날 수 있었지만,
    -- remote_path 는 "어디로 보냈는가" 라는 과거 사실의 기록이며
    -- 나중에 설정이 바뀌어도 그때 그 경로로 보냈다는 사실은 변하지 않는다.

    part_path   TEXT,
    -- 업로드에 사용한 원격 .part 경로.
    --
    -- 두 값 모두 전송 완료 시점이 아니라 IN_PROGRESS 로 전환하는 시점에,
    -- 상태 갱신과 같은 트랜잭션 안에서 기록한다.
    -- 완료 후에 기록하면 중단된 항목은 NULL 로 남아 위 상태 주석의
    -- 잔여 .part 정리 절차가 삭제 대상을 찾지 못한다. (9.3, 13.2)
    --
    -- .part 규칙으로 유도하지 않고 실제 사용한 경로를 저장하는 이유는,
    -- Path Template 설정이 중간에 바뀌어도 과거에 남은 .part 를
    -- 찾아 지울 수 있어야 하기 때문이다.

    sent_at     INTEGER,
    -- 업로드가 완료된 시각 (Unix epoch 초, UTC).
    transfer_verified_at INTEGER,
    -- Transfer Verification 을 통과한 시각 (Unix epoch 초, UTC). (8.1)
    -- sent_at 과 분리하여 "보냈지만 아직 검증 안 됨" 구간을 표현한다.
    -- common_ledger.ingress_verified_at 과 구분하기 위한 접두어이다.

    error       TEXT,
    -- 최근 실패 원인. 성공 시 NULL.
    -- 성공은 집계 중심으로 기록하고 실패는 상세 원인을 남긴다. (14)

    PRIMARY KEY (category, file_name, revision),

    FOREIGN KEY (category, file_name)
    REFERENCES common_ledger (category, file_name)
    ON DELETE CASCADE
    -- Ledger 보존기간 경과로 common_ledger 행을 정리하면
    -- 대응하는 전송 이력도 함께 정리된다. (14)
    -- 헤더의 PRAGMA foreign_keys = ON 이 켜져 있어야 동작한다.
    --
    -- ★ 보존기간 주의 (v5)
    --   LedgerRetentionDays 는 반드시 ScanDays 보다 길어야 한다.
    --   짧으면 디스크에는 있는데 장부에서만 지워진 파일이 생기고,
    --   그 파일은 다음 Scan 에서 신규로 판정되어 재전송된다.
    --   로컬 보존이 10년이므로 이 조건이 깨지면 피해가 크다.
    --   config 검증에서 LedgerRetentionDays > ScanDays 를 강제한다.
    --   Retention 으로 지운 기간까지 자동·재귀 탐색하면 로컬 파일이 신규로
    --   판정될 수 있으므로 탐색 날짜 경계와 함께 검증해야 한다.
    --
    -- 한계: FK 는 (category, file_name) 만 참조하므로 revision 정합성은
    -- 강제되지 않는다. 부모가 revision=1 인데 자식에 revision=99 를
    -- 넣어도 DB 는 막지 못한다. 후보 선정 쿼리가 common 에서 읽은
    -- revision 을 그대로 쓰는 한 발생하지 않지만,
    -- 값을 직접 구성하는 코드 경로를 만들지 않는다.
    );

-- 실패 항목 조회와 Retry 대상 선정에 사용한다.
--   RINEXClient.exe db failed put   (9.2)
-- 재시작 시 IN_PROGRESS 잔여 항목 회수에도 같은 인덱스를 탄다. (9.3)
CREATE INDEX IF NOT EXISTS idx_put_status
    ON put_ledger (status);


-- -----------------------------------------------------------------------------
--  Ledger Retention Cleanup 정책 (v5 설계 확정, 2026-09-15 현재 미구현)
--
--  RetentionDays 는 애플리케이션 설정값이며 SQLite 가 자동으로 행을 지우지 않는다.
--  현재 구현은 설정 로드와 RetentionDays > ScanDays 검증까지다.
--  MVP2에서 Deep Scan 성공 후 다음 기준으로 정리하도록 구현·테스트한다.
--
--      DELETE FROM common_ledger
--       WHERE ingress_verified_at < ?;   -- now - RetentionDays
--
--  put_ledger 는 ON DELETE CASCADE 로 함께 삭제된다.
--
--  first_seen 이 아니라 ingress_verified_at 을 기준으로 하는 이유:
--    오래전에 처음 발견된 파일이 최근 다시 갱신되어 revision 이 증가했다면
--    최신 Ingress 검증 시각을 보존해야 하기 때문이다.
--
--  구현할 정리 순서:
--    시작 시 IN_PROGRESS 복구 → Scan/전송 → Deep Scan 실행일이면 Retention Cleanup.
--
--  VACUUM 은 정기 실행하지 않는다.
--  DELETE 로 생긴 free page 는 이후 INSERT 에 재사용하며,
--  실제 DB 파일 축소가 필요한 경우에만 별도 유지보수 절차로 수행한다.
-- -----------------------------------------------------------------------------


-- -----------------------------------------------------------------------------
--  schema_meta
--    스키마와 식별자 규칙의 버전을 기록한다. 설계안에는 없는 추가 테이블이다.
--
--    헤더에 적었듯 NormalizedName 규칙이 바뀌면 누적된 Ledger 전체가 무효가
--    된다. 그 사고를 사람이 기억해서 막는 대신, 실행파일이 시작 시 이 값을
--    읽어 자신이 기대하는 규칙과 다르면 중단하도록 한다.
--    폐쇄망에서 실행파일과 DB 파일이 따로 갱신되는 상황을 방어한다.
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS schema_meta (
                                           key         TEXT    NOT NULL PRIMARY KEY,
                                           value       TEXT    NOT NULL,
                                           updated_at  INTEGER NOT NULL
);

-- schema_version 은 파일명의 v 번호와 별개로 증가시켜 온 값이다.
--   v4 파일 '1' → v5 '2' (local_path 삭제) → v6 '3' (category CHECK 확장)
--   → v7 '4' (식별자 복합키 전환) → v8 '5' (set_key/kind 추가 — MVP2 세트 게이트)
--   ※ 두 계열이 헷갈릴 소지가 있다. 파일명과 일치시키려면 설계 개정 번호와
--     같게 두어야 하나, 그러면 기존 DB 와 건너뛰는 구간이 생긴다.
--     현 단계에서는 증분을 택했다. 다르게 가려면 여기만 고치면 된다.
--
-- ★ 시작 시 검증 규칙 (v5 에서 명시)
--
--   identity_rule   기대값과 다르면 즉시 중단.
--                   누적 이력 전체가 무효가 되므로 진행 여지가 없다.
--
--   schema_version  기대값과 다르면 즉시 중단. 방향에 따라 안내만 달리한다.
--                     DB < 실행파일 → 마이그레이션 미수행. 절차 안내 후 종료
--                     DB > 실행파일 → 구버전 실행파일. 배포 오류. 종료
--                   구버전 실행파일이 신버전 DB 에 쓰면 컬럼 불일치로
--                   조용히 잘못된 행이 생길 수 있으므로 양방향 모두 막는다.
--
--   mvp_stage       검증하지 않는다. 로그 태그 용도이다.
--                   현재 값 MVP1_PUT 은 DB 최초 생성 단계의 역사적 표기이며
--                   현재 로드맵이나 기능 완료 상태를 판정하는 값이 아니다.
--
-- 아래 INSERT 는 OR IGNORE 이므로 기존 DB 의 값을 덮어쓰지 않는다.
-- 즉 기존 DB 에 이 스크립트를 적용해도 schema_version 은 원래 값으로 남는다.
-- 스크립트가 조용히 버전만 올려놓고 데이터는 옛 구조로 두는 사고를 막는다.
-- 버전 갱신은 Open 의 자동 마이그레이션(db.go migrateV4toV5)의 UPDATE 로만
-- 수행하며, 그 값('4')이 곧 마이그레이션 발동 조건이다.
INSERT OR IGNORE INTO schema_meta (key, value, updated_at) VALUES
    ('schema_version', '5',           strftime('%s', 'now')),
    ('identity_rule',  'FILENAME_V1', strftime('%s', 'now')),
    ('mvp_stage',      'MVP1_PUT',    strftime('%s', 'now'));


-- =============================================================================
--  마이그레이션 (schema_version 값 기준)
--
--  구조 세대 전환은 ledger.Open 절차 안에서 자동으로 수행한다.
--  운영 DB 는 세 기관에 전송 이력이 쌓여 있으므로 삭제·재생성하지 않는다.
--  삭제하면 스캔 윈도우 안 모든 파일이 신규로 판정되어 전량 재전송된다.
--  ("DB 삭제 후 재생성" 을 기본 절차로 하던 종전 안내는 폐기한다 — 그 절차는
--   운영 이력이 없던 개발 단계의 것이었다.)
--
--  자동 마이그레이션의 범위 (db.go migrateIfNeeded):
--    - DB 가 실행파일보다 정확히 한 세대 낮은 '4' → '5' 전환만 자동 수행한다.
--    - 그 외 방향·간격의 불일치(DB > 실행파일 등)는 자동 보정하지 않고
--      verifySchemaVersion 이 시작을 중단시킨다. (폐쇄망 배포 오류 방어)
--
--  v4 → v5 (set_key·kind 추가, db.go migrateV4toV5):
--    단일 트랜잭션으로 원자적으로 수행한다. 중단 시 전부 롤백된다.
--    1) ALTER TABLE common_ledger ADD COLUMN set_key / kind
--       (NOT NULL DEFAULT '' + lower CHECK — 위 CREATE 정의와 같은 모양.
--        ADD COLUMN 은 기존 행을 재작성하지 않는 메타데이터 연산이다.
--        전송 이력·기존 행이 보존된다.)
--    2) 기존 행 백필 — (category, file_name) 을 전량 읽어 커서를 닫은 뒤
--       domain.SetKeyKind 로 도출해 UPDATE 한다. 단일 커넥션 교착을 피하려
--       SELECT 커서를 연 채 UPDATE 하지 않는다. 유보(파싱 불가) 행은
--       DEFAULT '' 그대로 둔다 — '' 가 곧 유보 표기다.
--       재스캔 자연충전은 size·mtime 이 안 바뀐 행을 갱신하지 않으므로
--       백필로 한 번에 채운다.
--    3) UPDATE schema_meta SET value='5' WHERE key='schema_version'
--    완료 시 [LEDGER] 로그로 행 수·백필 수·유보 수를 남긴다.
--
--  set_key·kind 조회 인덱스(예: (category, set_key))는 지금 두지 않는다.
--  현재 세트 게이트는 메모리에서 판정하므로 필요하지 않다. 구체적인 조회
--  요구와 측정된 병목이 생길 때만 추가한다. (선제 구현 금지)
--
--  주의:
--    CREATE TABLE IF NOT EXISTS 는 기존 테이블에 컬럼을 추가하지 않는다.
--    따라서 이 스크립트 재실행만으로는 v4 DB 에 set_key·kind 가 생기지 않으며,
--    실제 컬럼 추가는 위 migrateV4toV5 가 수행한다. v4 에서 전환된 DB 의
--    sqlite_master 원문에는 위 CREATE 의 컬럼 주석이 없지만 구조는 동일하다.
-- =============================================================================
