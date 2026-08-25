-- =============================================================================
--  rinex_ledger.db  /  Ledger 스키마
-- =============================================================================
--  설계 원칙 (설계안 9)
--    "하나의 사실에는 하나의 주인만 둔다."
--    파일의 정체성은 common_ledger 가, 송신 이력은 put_ledger 가 소유한다.
--    두 테이블은 같은 사실을 중복 저장하지 않고 file_name 으로 상호 검증한다.
--
--  식별자 정책 (설계안 9.1 에서 개정)
--    원안은 SHA-256( Domain │ Category │ NormalizedName ) 해시였으나,
--    RINEX3 파일명이 관측소·시각·기간·샘플링·데이터타입을 모두 포함하는
--    자기서술적 형식이므로 파일명 자체가 유일하다.
--        정규화 전 : SONP00KOR_R_20260010300_01H_01S_MS.rnx.gz
--        정규화 후 : sonp00kor_r_20260010300_01h_01s_ms.rnx.gz   ← 저장되는 값
--    따라서 해시를 거치지 않고 정규화한 파일명을 그대로 키로 사용한다.
--    원안의 목적인 "경로 비의존" 은 그대로 유지된다. 경로는 키가 아니라
--    속성이므로 local_path 컬럼에 별도로 기록한다.
--
--    Domain 은 인스턴스별로 DB 파일이 분리되어 있어(9.3) 한 DB 안에서
--    타 기관 파일과 만날 일이 없으므로 식별자에서 제외한다.
--    config 값과 로그 태그로만 유지한다.
--
--  NormalizedName 규칙
--    - 디렉터리 경로 제외, 파일명만 사용
--    - 대소문자는 소문자로 통일
--    - .part 등 임시 접미사 제거
--    - 압축 확장자(.gz, .Z) 는 유지
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
--
--  개정 이력
--    v1  최초 (해시 file_id)
--    v2  식별자를 정규화 파일명으로 변경, Domain·Category 를 키에서 제외
--    v3  CHECK 제약 전면 적용, part_path·base_name·schema_meta 추가
--    v4  verified_at 을 ingress_/transfer_ 로 분리
--        state 를 필터에서 관측 용도로 재정의 (후보 판정은 revision 매칭)
--        후보 선정 인덱스를 (category, origin, state) 순서로 교정
--
--  적용 범위
--    [현재] common_ledger, put_ledger, schema_meta
--    [예정] download_ledger  (MVP 2 에서 추가. 설계안 9)
-- =============================================================================


-- -----------------------------------------------------------------------------
--  common_ledger
--    파일 자체의 정체성과 입고 상태를 기록한다.
--    "이 파일이 무엇인가" 에 대한 단 하나의 주인이며, 전송 이력은 두지 않는다.
--    파일 하나당 한 행을 유지하며, 재관측 시 UPDATE 로 갱신한다.
--    설계안 9
-- -----------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS common_ledger (

    file_name   TEXT    NOT NULL PRIMARY KEY
                        CHECK (file_name = lower(file_name)),
        -- 정규화된 파일명. 경로를 포함하지 않는 논리적 식별자이다.
        -- DOWNLOAD LocalPath 와 PUT LocalPath 가 달라도 동일 파일은
        -- 동일 값을 가지며, 이 성질이 BOTH 모드의 재전송 방지를 성립시킨다.
        --
        -- CHECK 는 소문자 정규화를 DB 차원에서 강제한다. 규칙을 지키지 않은
        -- 코드 경로가 하나라도 있으면 같은 파일이 두 행으로 등록되어
        -- 중복 전송이 발생하므로, 코드 규율에만 맡기지 않는다.

    base_name   TEXT    NOT NULL,
        -- 압축 확장자(.gz, .Z) 를 제거한 이름.
        -- 같은 관측 데이터가 .rnx 와 .rnx.gz 두 형태로 유입되는지 탐지한다.
        -- 두 형태는 바이트열도 크기도 다르므로 서로 다른 file_name 으로
        -- 등록되며(누락을 막기 위한 의도적 선택), 이 컬럼은 그 상황이
        -- 실제로 발생하는지 운영 중에 확인하기 위한 관측 수단이다.
        --   SELECT base_name, COUNT(*) FROM common_ledger
        --    GROUP BY base_name HAVING COUNT(*) > 1;
        -- 식별자가 아니므로 UNIQUE 를 걸지 않는다.

    category    TEXT    NOT NULL
                        CHECK (category IN ('RINEX2_DAILY',  'RINEX2_HOURLY',
                                            'RINEX3_DAILY',  'RINEX3_HOURLY')),
        -- 식별자에 포함되지 않는 일반 컬럼이다. 값은 Scanner 가 판정하지 않고
        -- config.ini 의 [PUT.<CATEGORY>] 섹션에서 그대로 전달받는다.
        -- RINEX3 파일명에 01D / 01H 가 들어 있으므로, verify 단계에서
        -- config 가 지정한 category 와 파일명이 함의하는 주기를 대조하여
        -- 불일치 시 Ingress 를 거부한다. Config 오기입 탐지 장치이다.
        --
        -- 정수(iota)가 아닌 문자열로 저장하여 순서 변경에 영향받지 않게 한다.

    size        INTEGER NOT NULL CHECK (size >= 0),
        -- 최근 관측된 바이트 크기. Transfer 검증의 기준값이 된다. (8.1)

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
        -- 되돌릴 경우 위 CHECK 에 'RECEIVER' 를 추가하면 되며, 그때 그 값은
        -- config 선언에 의존하는 미검증 메타데이터임을 전제해야 한다.

    revision    INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1),
        -- 동일 파일명이 서로 다른 size 또는 mtime 으로 재관측될 때 1 증가한다.
        --
        -- 실제 발생 사유는 두 가지이다.
        --   1) 관측소에서 결측 구간을 채워 파일을 재생성하는 경우 (9.1)
        --   2) 0바이트로 먼저 생성된 뒤 내용이 나중에 채워지는 경우
        --
        -- 2) 는 대부분 Ingress Verification 의 size>0 / mtime grace 판정에서
        -- 전송 전에 보류되므로 Ledger 에 등록되지 않는다. revision 은 그 판정을
        -- 통과한 뒤에 내용이 변경된 경우를 회수하는 안전망이다.
        --
        -- revision 없이 UPDATE 로 덮어쓰면 과거 전송 이력을 잃고,
        -- 무조건 skip 하면 보정된 데이터가 영영 전송되지 않는다.
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
        -- 0바이트 선생성 후 채워지는 현상이 현장에서 실제로 얼마나
        -- 발생하는지는 아직 관측된 바 없고, 이 값이 그것을 답한다.
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

    local_path  TEXT    NOT NULL,
        -- 파일이 관측된 로컬 경로. 식별자가 아니라 속성이다. (9.1)
        -- 운영 장애 분석 시 "이 파일이 어느 디렉터리에 있었는가" 를 답한다.

    first_seen  INTEGER NOT NULL,
        -- 이 파일을 처음 발견한 시각 (Unix epoch 초, UTC).
        -- revision 이 올라가도 갱신하지 않는다. 최초 입고 시점을 보존한다.

    ingress_verified_at INTEGER NOT NULL
        -- Ingress Verification 을 통과한 시각 (Unix epoch 초, UTC). (7)
        -- 위 state 주석대로 미통과 파일은 행이 생기지 않으므로 NULL 이 없다.
        -- revision 이 올라가면 재검증 시각으로 갱신한다.
        --
        -- put_ledger 에도 transfer_verified_at 이 있다. 둘은 다른 검증이다.
        --   ingress  — 파일이 정상적으로 완성되었는가 (설계안 7)
        --   transfer — 목적지에 제대로 도착했는가   (설계안 8.1)
        -- 양쪽을 같은 이름으로 두면 조인 조회에서 어느 쪽 시각인지 알 수 없어
        -- 접두어로 구분한다.
);

-- PUT 후보 선정 경로. 스캔마다 타는 가장 빈번한 조회이다.
--
--   SELECT c.file_name, c.revision, c.local_path, c.size
--     FROM common_ledger c
--     LEFT JOIN put_ledger p
--       ON  p.file_name = c.file_name
--       AND p.revision  = c.revision
--    WHERE c.category = ?
--      AND c.origin   = 'LOCAL'
--      AND (p.status IS NULL OR p.status = 'FAILED');
--
-- 후보 판정의 주체는 state 가 아니라 revision 매칭이다.
--   - 한 번도 보낸 적 없으면          put 행이 없음        → NULL  → 후보
--   - 보냈고 검증까지 끝났으면        VERIFIED             → 제외
--   - 파일이 갱신되어 revision 이 올랐으면 그 revision 의 행이 없음 → 후보
--   - 실패했으면                      FAILED               → 재시도 후보
-- state 는 조건에 넣지 않는다. READY / CHANGED 둘 다 대상이기 때문이다.
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

    file_name   TEXT    NOT NULL,
        -- common_ledger.file_name 을 참조한다.
        -- 부모가 소문자로 강제되므로 여기에는 별도 CHECK 를 두지 않는다.

    revision    INTEGER NOT NULL CHECK (revision >= 1),
        -- 전송을 시도한 시점의 common_ledger.revision 값이다.
        -- (file_name, revision) 을 키로 두어 이력을 누적하므로
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
        -- 삭제하고 상태를 PENDING 으로 되돌린다. 이 절차가 없으면 .part 가 누적된다.
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

    PRIMARY KEY (file_name, revision),

    FOREIGN KEY (file_name)
        REFERENCES common_ledger (file_name)
        ON DELETE CASCADE
        -- Ledger 보존기간 경과로 common_ledger 행을 정리하면
        -- 대응하는 전송 이력도 함께 정리된다. (14)
        -- 헤더의 PRAGMA foreign_keys = ON 이 켜져 있어야 동작한다.
        --
        -- 한계: FK 는 file_name 만 참조하므로 revision 정합성은 강제되지 않는다.
        -- 부모가 revision=1 인데 자식에 revision=99 를 넣어도 DB 는 막지 못한다.
        -- 후보 선정 쿼리가 common 에서 읽은 revision 을 그대로 쓰는 한
        -- 발생하지 않지만, 값을 직접 구성하는 코드 경로를 만들지 않는다.
);

-- 실패 항목 조회와 Retry 대상 선정에 사용한다.
--   RINEXClient.exe db failed put   (9.2)
-- 재시작 시 IN_PROGRESS 잔여 항목 회수에도 같은 인덱스를 탄다. (9.3)
CREATE INDEX IF NOT EXISTS idx_put_status
    ON put_ledger (status);


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

INSERT OR IGNORE INTO schema_meta (key, value, updated_at) VALUES
    ('schema_version', '1',           strftime('%s', 'now')),
    ('identity_rule',  'FILENAME_V1', strftime('%s', 'now')),
    ('mvp_stage',      'MVP1_PUT',    strftime('%s', 'now'));
