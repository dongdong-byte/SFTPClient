package domain

import "strings"

// IdentityRule 은 이 실행파일이 사용하는 파일 식별자 규칙의 이름이다.
//
// 시작 시 schema_meta.identity_rule 값과 대조하여 다르면 실행을 중단한다.
// 폐쇄망에서 실행파일과 DB 파일이 서로 다른 버전으로 배포되는 상황을
// 방어하기 위한 값이다.
//
// NormalizeName 의 규칙을 변경하면 기존 Ledger 의 file_name 의미가 달라질 수 있으므로
// 반드시 IdentityRule 도 함께 변경한다.
//
// 식별자 규칙 변경은 누적 Ledger 전체에 영향을 줄 수 있어
// 프로젝트에서 변경 비용이 가장 큰 항목이다. (CONCEPT 4.1, 7)
const IdentityRule = "FILENAME_V1"

// partSuffix 는 전송·수신 중인 파일에 사용하는 임시 접미사이다.
//
// 이 프로그램이 업로드 시 사용하는 접미사이며, 다른 프로세스가
// 다른 접미사(.tmp 등)를 사용한다면 이 값만으로는 걸러지지 않는다.
// 그 경우 tempSuffixes 형태의 목록으로 확장하고 IdentityRule 을 올린다.
const partSuffix = ".part"

// compressExts 는 BaseName 이 제거할 압축 확장자이다.
// NormalizeName 은 이 확장자를 유지한다.
//
// 소문자로 적는다. BaseName 이 NormalizeName 을 거친 값을 다루기 때문이다.
// 현장에서 .gz / .Z / .zip 이 관측소마다 혼재하므로 세 형태를 모두 포함한다.
// (schema.sql base_name 주석, SCAN DESIGN 10.2)
var compressExts = []string{".gz", ".z", ".zip"}

// NormalizeName 은 경로 또는 파일명을 Ledger 의 논리적 파일 식별자로 변환한다.
// 반환값은 common_ledger.file_name 에 저장된다.
//
// 규칙 (CONCEPT 4.2):
//   - 디렉터리 경로를 제외하고 파일명만 사용한다.
//   - 대소문자를 소문자로 통일한다.
//   - .part 임시 접미사를 제거한다.
//   - 압축 확장자(.gz, .Z, .zip)는 유지한다.
//
// 파일명 정규화 규칙은 반드시 이 함수 하나에서만 구현한다.
// schema.sql 의 CHECK (file_name = lower(file_name)) 는 최후 방어선이며,
// 정규화 자체를 DB 에 의존하지 않는다. (CONCEPT 5②)
//
// 경로를 식별자에서 제외하므로 DOWNLOAD LocalPath 와 PUT LocalPath 가 달라도
// 동일 파일명은 동일한 file_name 을 갖는다.
// 이 성질은 BOTH 모드에서 동일 파일을 식별하는 기반이 된다.
//
// 주의: 이 함수는 .part 를 제거하므로 작성 중인 파일도 최종 파일명으로 보인다.
// scan 은 .part 를 거르지 않는다. 호출 전 IsPartFile 로 제외하는 것은
// verify(Ingress) 의 책임이다.
func NormalizeName(pathOrName string) string {
	name := trimDir(pathOrName)
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, partSuffix)

	return name
}

// trimDir 은 마지막 경로 구분자 이후의 파일명만 반환한다.
//
// filepath.Base 를 사용하지 않는 이유는 실행 OS 에 따라 인식하는
// 경로 구분자가 달라질 수 있기 때문이다.
//
// Windows 형식:
//
//	D:\RINEX\AAAA.rnx.gz
//
// Linux 형식:
//
//	/opt/rinex/AAAA.rnx.gz
//
// 어느 형식이 입력되더라도 동일한 파일명이 반환되어야 하므로
// '/' 와 '\' 를 모두 경로 구분자로 처리한다.
// MVP 5 의 Linux 빌드에서 Windows 경로가 그대로 들어오는 경우를 대비한다.
func trimDir(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}

	return p
}

// BaseName 은 정규화된 파일명에서 압축 확장자를 제거한 값을 반환한다.
// 반환값은 common_ledger.base_name 에 저장된다.
//
// base_name 은 식별자가 아니라 운영 관측을 위한 값이다.
// 같은 관측 데이터가 압축/비압축 형태로 함께 유입되는지를 확인하는 데 사용하며
// UNIQUE 제약을 두지 않는다. (CONCEPT 4.7)
//
// 예:
//
//	aaaa.rnx.gz  -> aaaa.rnx
//	aaaa.rnx.Z   -> aaaa.rnx
//	aaaa.rnx.zip -> aaaa.rnx
//	aaaa.rnx     -> aaaa.rnx
//
// 호출자가 별도로 정규화할 필요가 없도록 내부에서 NormalizeName 을 호출한다.
func BaseName(pathOrName string) string {
	name := NormalizeName(pathOrName)

	for _, ext := range compressExts {
		if after, ok := strings.CutSuffix(name, ext); ok {
			return after
		}
	}

	return name
}

// IsPartFile 은 파일명이 .part 임시 접미사로 끝나는지 답한다.
//
// scan 은 관측 사실만 올리고 .part 를 거르지 않는다.
// 후보에서 제외하는 것은 verify(Ingress) 의 책임이다.
//
// .part 를 먼저 제외하지 않고 NormalizeName 하면 임시 접미사가 제거되어
// 작성 중인 파일이 최종 파일명처럼 보일 수 있다.
//
// 대부분의 경우 Grace Time 이 작성 중 파일을 막지만,
// .part 자체를 Ingress 단계에서 제외하여 불필요한 후보가
// Ledger 로 전달되지 않도록 한다. (설계안 7, PROJECT_GUIDELINES)
func IsPartFile(pathOrName string) bool {
	name := trimDir(pathOrName)

	return strings.HasSuffix(strings.ToLower(name), partSuffix)
}
