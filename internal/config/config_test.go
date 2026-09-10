package config

import (
	"os"
	"path/filepath"
	"runtime"
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
func TestExampleINI_MatchesKnownKeys(t *testing.T) {
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

	for section, keys := range known {
		got, ok := exampleSections[section]
		if !ok {
			t.Errorf(
				"knownKeys 섹션 [%s] 가 config.example.ini 에 없다",
				section,
			)
			continue
		}

		for _, key := range keys {
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
