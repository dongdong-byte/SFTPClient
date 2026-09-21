package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// exampleINIPath 는 저장소 루트의 config.example.ini 경로를 돌려준다.
//
// 테스트 작업 디렉터리에 의존하지 않는다.
// go test 가 패키지 디렉터리에서 돌든 모듈 루트에서 돌든 같다.
func exampleINIPath(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}

	path := filepath.Join(
		filepath.Dir(file),
		"..",
		"..",
		"config.example.ini",
	)

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.example.ini 을 찾을 수 없다: %v", err)
	}

	return path
}

// TestExampleINI_MatchesKnownKeys 는 config.example.ini 와 knownKeys() 를
// 양방향으로 대조한다.
//
//	코드에만 키 추가  → 새 서버 설치 때 템플릿에서 빠진다
//	템플릿에만 키 추가 → 실행이 unknown key 로 거부된다
//
// GUIDELINES 8절, load.go knownKeys 주석.
//
// 예외 — 조건부 키(conditionalTemplateKeys): 다른 키의 "값"이 전제일 때만
// 합법인 키는 기본 상태 템플릿에 실물로 둘 수 없다. ResendMinKinds 는
// 게이트 ON(RequiredKinds = 목록)에서만 합법인데(RESEND v4 §5.3 3행 —
// 게이트 OFF + 키 존재는 시작 오류), 템플릿의 SET 섹션은 배포 기본인
// 게이트 OFF(false)다. 키를 실물로 넣으면 LoadFromValidates 가 깨지고,
// 게이트를 켜면 템플릿을 복사한 새 설치처의 동작이 바뀐다. 따라서
// 순방향 검사(코드→템플릿)만 면제하고 템플릿에는 주석으로 문서화한다.
// 역방향 검사(템플릿→코드)는 그대로 적용된다.
func TestExampleINI_MatchesKnownKeys(t *testing.T) {
	// section(foldKey) → key(foldKey) → true.
	// 버전 목록은 knownKeys 와 같은 setVersions() 에서 온다.
	// RINEX5 를 추가하면 여기 표를 손대지 않아도 SET.RINEX5 가 면제된다.
	conditionalTemplateKeys := resendMinKindsTemplateException(t)

	f, err := parseINIFile(exampleINIPath(t))
	if err != nil {
		t.Fatalf("config.example.ini 파싱 실패: %v", err)
	}

	known, err := knownKeys()
	if err != nil {
		t.Fatalf("knownKeys(): %v", err)
	}

	exampleSections := make(map[string]map[string]bool, len(f.sectionNames()))

	for _, name := range f.sectionNames() {
		folded := foldKey(name)
		sec, ok := f.section(name)
		if !ok {
			t.Fatalf("section %q disappeared after sectionNames()", name)
		}

		keys := make(map[string]bool, len(sec.keys()))
		for _, key := range sec.keys() {
			keys[foldKey(key)] = true
		}

		exampleSections[folded] = keys
	}

	// 예외 목록 자체의 부패를 막는다.
	//	knownKeys 에서 빠진 키 → 예외가 죽은 항목으로 남는다
	//	템플릿 주석에서 빠진 키 → 새 설치처가 키의 존재를 알 수 없다
	commented := commentedTemplateKeys(t)

	for section, keys := range conditionalTemplateKeys {
		for key := range keys {
			found := false
			for _, k := range known[section] {
				if foldKey(k) == key {
					found = true
					break
				}
			}

			if !found {
				t.Errorf(
					"조건부 예외 [%s] %s 가 knownKeys 에 없다 — 예외 목록에서 지운다",
					section,
					key,
				)
			}

			if !commented[key] {
				t.Errorf(
					"조건부 키 %s 가 config.example.ini 주석에 문서화되지 않았다",
					key,
				)
			}
		}
	}

	for section, keys := range known {
		foldedSection := foldKey(section)
		got, ok := exampleSections[foldedSection]
		if !ok {
			t.Errorf(
				"knownKeys 섹션 [%s] 가 config.example.ini 에 없다",
				section,
			)
			continue
		}

		for _, key := range keys {
			if conditionalTemplateKeys[foldedSection][foldKey(key)] {
				continue
			}

			if !got[foldKey(key)] {
				t.Errorf(
					"knownKeys [%s] %s 가 config.example.ini 에 없다",
					section,
					key,
				)
			}
		}
	}

	for section, keys := range exampleSections {
		want, ok := known[section]
		if !ok {
			t.Errorf(
				"config.example.ini 섹션 [%s] 가 knownKeys 에 없다",
				section,
			)
			continue
		}

		allowed := make(map[string]bool, len(want))
		for _, key := range want {
			allowed[foldKey(key)] = true
		}

		for key := range keys {
			if !allowed[key] {
				t.Errorf(
					"config.example.ini [%s] 키 %q 가 knownKeys 에 없다",
					section,
					key,
				)
			}
		}
	}
}

// commentedTemplateKeys 는 config.example.ini 의 주석 줄에서
// "Key =" 꼴로 적힌 키 이름을 foldKey 로 모은다.
//
// 조건부 키는 템플릿에 실물로 둘 수 없으므로 주석 예시가 유일한 문서다.
// 섹션 단위가 아니라 파일 단위로 본다 — 조건부 키 설명은 여러 섹션을
// 묶은 공통 주석 블록에 한 번만 적는다.
func commentedTemplateKeys(t *testing.T) map[string]bool {
	t.Helper()

	data, err := os.ReadFile(exampleINIPath(t))
	if err != nil {
		t.Fatalf("config.example.ini 읽기 실패: %v", err)
	}

	out := make(map[string]bool)

	for _, line := range strings.Split(string(data), "\n") {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(s, ";") {
			continue
		}

		// "; 예)  ResendMinKinds = O" 처럼 앞에 설명이 붙어도 잡는다.
		body, _, ok := strings.Cut(strings.TrimLeft(s, "; \t"), "=")
		if !ok {
			continue
		}

		fields := strings.Fields(body)
		if len(fields) == 0 {
			continue
		}

		out[foldKey(fields[len(fields)-1])] = true
	}

	return out
}

// resendMinKindsTemplateException 은 템플릿 실물 키 대조에서
// ResendMinKinds 를 빼는 면제 표다. SET 섹션은 버전마다 knownKeys 에
// 등록되지만, 템플릿 기본은 게이트 OFF 라 실물 키를 둘 수 없다.
func resendMinKindsTemplateException(t *testing.T) map[string]map[string]bool {
	t.Helper()

	versions, err := setVersions()
	if err != nil {
		t.Fatalf("setVersions(): %v", err)
	}

	out := make(map[string]map[string]bool, len(versions))
	key := foldKey("ResendMinKinds")

	for _, version := range versions {
		out[foldKey(setSectionName(version))] = map[string]bool{key: true}
	}

	return out
}

// TestExampleINI_LoadFromValidates 는 템플릿 값이 Validate 까지 통과하는지 본다.
//
// CheckEnvironment 는 개인키·known_hosts 실재를 요구하므로 LoadFrom 만 쓴다.
// 템플릿의 Mode·경로·숫자 조합이 깨지면 여기서 드러난다.
func TestExampleINI_LoadFromValidates(t *testing.T) {
	file, err := os.Open(exampleINIPath(t))
	if err != nil {
		t.Fatalf("config.example.ini 열기 실패: %v", err)
	}
	defer file.Close()

	// 상대 경로 해석 기준만 필요하다. 실제 파일이 아니어도 된다.
	cfg, err := LoadFrom(file, filepath.Join(t.TempDir(), "config.ini"), fakeProtector{})
	if err != nil {
		t.Fatalf("LoadFrom(config.example.ini) 실패: %v", err)
	}

	if cfg == nil {
		t.Fatal("LoadFrom 이 nil Config 를 돌려줬다")
	}
}

// TestLoadFrom_SeoulSetAtomicityOnValidates 는 템플릿을 서울시처럼
// RINEX2 게이트 ON 으로 바꾼 뒤에도 Validate 까지 통과하는지 본다.
// 커밋 2 가 켠 기관의 시작 경로를 깨면 여기서 터진다.
func TestLoadFrom_SeoulSetAtomicityOnValidates(t *testing.T) {
	data, err := os.ReadFile(exampleINIPath(t))
	if err != nil {
		t.Fatalf("config.example.ini 읽기 실패: %v", err)
	}

	const from = "[SET.RINEX2]\nRequiredKinds = false"
	const to = "[SET.RINEX2]\nRequiredKinds = G,L,N,O"

	rewritten := strings.Replace(string(data), from, to, 1)
	if rewritten == string(data) {
		t.Fatalf("서울시 게이트 ON 치환 실패: %q 를 찾지 못했다", from)
	}

	cfg, err := LoadFrom(
		strings.NewReader(rewritten),
		filepath.Join(t.TempDir(), "config.ini"),
		fakeProtector{},
	)
	if err != nil {
		t.Fatalf("서울시 게이트 ON 템플릿 LoadFrom 실패: %v", err)
	}

	p2 := cfg.Set.Policy(2)
	if !p2.Enabled {
		t.Fatal("치환 후 RINEX2 게이트가 꺼져 있다")
	}

	if !reflect.DeepEqual(p2.RequiredKinds, []string{"g", "l", "n", "o"}) {
		t.Errorf("RequiredKinds = %v, want [g l n o]", p2.RequiredKinds)
	}

	if p2.ResendMinKinds != nil {
		t.Errorf("ResendMinKinds = %v, want nil", p2.ResendMinKinds)
	}
}
