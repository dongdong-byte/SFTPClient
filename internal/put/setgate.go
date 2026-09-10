package put

import (
	"fmt"
	"sort"

	"SFTPClient/internal/domain"
)

// setGate는 한 카테고리의 세트 완성도 게이트다.
//
// 후보 타입이나 Ledger 접근에 의존하지 않는 순수 계산이다.
// 호출자는 검증을 통과한 파일명으로 게이트를 구성하고,
// holds와 setKeyOf의 결과로 후보를 필터링하고 묶는다.
//
// 동일 세트의 검증된 형제 파일을 names에 빠짐없이 포함해야 한다.
// 이번 스캔이나 전송 후보의 일부만 전달하면 완성 세트를
// 미완성으로 판정할 수 있다. "검증을 통과한 파일" 이란 후보 여부와
// 무관하게 Ingress 를 통과한 실재 파일 전체다 — 이미 VERIFIED 로
// 전송이 끝난 멤버, DOWNLOAD origin 으로 후보에서 빠지는 멤버도
// 디스크의 정상 데이터이므로 완성도에는 존재로 센다. 그래야
// "G 는 지난 회차에 보냈고 L·N·O 가 오늘 도착" 한 세트가 영구
// 보류되지 않는다.
type setGate struct {
	category    domain.Category
	required    []string
	requiredSet map[string]bool

	// set_key → 검증된 파일에서 관측한 kind 집합.
	present map[string]map[string]bool
}

// newSetGate는 카테고리와 필수 kind 목록으로 게이트를 구성한다.
//
// required는 config에서 소문자로 정규화한 목록이다.
// 빈 목록이면 게이트 OFF다.
//
// names는 검증을 통과한 파일명이어야 한다.
// 파싱 불가 파일은 완성도 계산에 포함하지 않는다.
// 카테고리 라우팅 오류는 호출자에게 반환한다.
func newSetGate(
	cat domain.Category,
	required []string,
	names []string,
) (*setGate, error) {
	// names가 비어 있어도 지원하지 않는 카테고리는 검출한다.
	if _, err := cat.RinexVersion(); err != nil {
		return nil, fmt.Errorf(
			"put: create set gate for category %q: %w",
			cat,
			err,
		)
	}

	g := &setGate{
		category:    cat,
		required:    make([]string, 0, len(required)),
		requiredSet: make(map[string]bool, len(required)),
		present:     make(map[string]map[string]bool),
	}

	// 중복은 제거하고 설정에 적힌 순서는 유지한다.
	for _, k := range required {
		if g.requiredSet[k] {
			continue
		}

		g.requiredSet[k] = true
		g.required = append(g.required, k)
	}

	if !g.on() {
		return g, nil
	}

	for _, name := range names {
		setKey, kind, ok, err := g.parse(name)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		kinds := g.present[setKey]
		if kinds == nil {
			kinds = make(map[string]bool)
			g.present[setKey] = kinds
		}

		kinds[kind] = true
	}

	return g, nil
}

// on은 세트 게이트가 켜져 있는지 반환한다.
func (g *setGate) on() bool {
	return len(g.required) > 0
}

// parse는 domain 파서의 오류에 호출 맥락을 추가한다.
// err와 ok=false를 서로 다른 결과로 유지한다.
func (g *setGate) parse(
	name string,
) (setKey, kind string, ok bool, err error) {
	setKey, kind, ok, err = domain.SetKeyKind(g.category, name)
	if err != nil {
		return "", "", false, fmt.Errorf(
			"put: set gate category=%q file=%q: %w",
			g.category,
			name,
			err,
		)
	}

	return setKey, kind, ok, nil
}

// holds는 파일을 전송 보류해야 하는지 반환한다.
//
//	게이트 OFF                → 보류하지 않음
//	파싱 불가(세트 소속 유보)  → 보류하지 않음 (개별 파일로 통과)
//	완성 세트의 멤버           → 보류하지 않음 (필수 아닌 종 포함)
//	미완성 세트의 멤버         → 보류 (필수 아닌 종 포함 — 세트 전체 보류)
//
// 두 의미론 결정 (2026-09-10 확정):
//
// ① 파싱 불가 = 개별 통과. YONS060.20M 같은 표준 밖 실데이터가
//
//	실재하므로, 게이트를 켠 순간 그런 파일이 영구 미전송되는 안을
//	기각했다. MatchesName 의 "판별 근거 부족만으로 거부하지 않는다"
//	와 같은 유보 철학이다. 대가인 "파서가 못 읽는 세트 멤버의 개별
//	누출"은 domain 파서의 필드 형태 검사가 닫는다. 통과 건수의
//	관측(SetUnparsed 카운터·경고 로그)은 호출자(runner)가 observed
//	순회로 수행한다 — 이 값이 늘면 파일명 규약이 가정과 다르다는
//	신호다.
//
// ② 미완성 세트는 선택 종 포함 전체 보류. "필수 종이 모두 존재할
//
//	때만 해당 세트의 파일을 후보로 올린다"(확정 §2)가 우선한다.
//	§7 의 "S 는 개별 파일로 정상 전송된다"는 완성 세트에서의
//	동작이며, §7 의 본뜻은 S 의 존재가 완성 판정에 불참한다는 것
//	(OptionalKinds 불필요의 근거)이다. 미완성 세트에서 S 만 새어
//	나가면 목적지에 부분 세트가 생긴다 — 게이트가 막으려는 바로
//	그 상태다.
//
// 오류가 반환되면 호출자는 오류를 전파해야 한다.
func (g *setGate) holds(name string) (bool, error) {
	if !g.on() {
		return false, nil
	}

	setKey, _, ok, err := g.parse(name)
	if err != nil {
		return true, err
	}
	if !ok {
		// ① 세트 소속 유보 — 개별 파일로 통과.
		return false, nil
	}

	// ② 세트 소속이 확정된 파일은 종과 무관하게 세트 운명을 따른다.
	return !g.complete(setKey), nil
}

// setKeyOf는 전송 개수 제한에서 세트를 나누지 않기 위한 키를 반환한다.
//
// 게이트 OFF 와 파싱 불가(개별 통과 파일)는 빈 키를 반환한다.
// 세트 소속이 확정된 파일은 필수 여부와 무관하게 키를 갖는다 —
// 전체 보류 의미론(위 ②)에서 선택 종도 세트와 함께 움직여야
// 경계 절단이 세트를 쪼개지 않는다.
//
// 이 함수는 세트 완성 여부를 검사하지 않는다.
// 호출자는 holds를 통과한 후보에 대해서만 사용한다.
func (g *setGate) setKeyOf(name string) (string, error) {
	if !g.on() {
		return "", nil
	}

	setKey, _, ok, err := g.parse(name)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", nil
	}

	return setKey, nil
}

// complete는 해당 세트에 모든 필수 kind가 존재하는지 반환한다.
func (g *setGate) complete(setKey string) bool {
	present := g.present[setKey]
	if present == nil {
		return false
	}

	for _, k := range g.required {
		if !present[k] {
			return false
		}
	}

	return true
}

// heldSetInfo는 미완성 세트 하나의 로그용 요약이다.
type heldSetInfo struct {
	SetKey  string
	Have    []string
	Missing []string
}

// heldSets는 미완성 세트를 반환한다.
//
// 전체 보류 의미론에 따라, 선택 종만 도착한 세트(예: S 단독)도
// 보류 상태이므로 요약에 포함한다 — 그 세트의 파일도 실제로
// 보류되는데 요약에서 빠지면 "전송이 안 되는데 로그에 없음" 이 된다.
// 완성 세트는 제외한다.
//
// 세트 키 순서로 정렬한다. Have 는 관측한 종의 사전순,
// Missing 은 설정에 적힌 순서를 따른다.
// 세트 키를 도출하지 못한 파일(개별 통과)은 이 요약과 무관하다.
func (g *setGate) heldSets() []heldSetInfo {
	if !g.on() {
		return nil
	}

	out := make([]heldSetInfo, 0)

	for setKey, present := range g.present {
		missing := make([]string, 0)

		for _, k := range g.required {
			if !present[k] {
				missing = append(missing, k)
			}
		}

		if len(missing) == 0 {
			continue
		}

		have := make([]string, 0, len(present))
		for k := range present {
			have = append(have, k)
		}

		sort.Strings(have)

		out = append(out, heldSetInfo{
			SetKey:  setKey,
			Have:    have,
			Missing: missing,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].SetKey < out[j].SetKey
	})

	return out
}

// setID는 세트 경계 절단에서 세트를 식별하는 키다.
//
// SetKey 는 카테고리 안에서만 유일하다 — RINEX3 과 RINEX4 는 같은
// 긴 파일명 규칙을 공유하므로 SetKey 만으로는 두 카테고리의 세트가
// 합쳐질 수 있다. SortCandidates 가 category 를 보조 키로 두는 것과
// 같은 이유다.
func setID(c Candidate) string {
	return string(c.Key.Category) + "\x00" + c.SetKey
}

// dropSplitSets는 MaxFilesPerRun 절단이 세트를 반으로 자르지 않도록
// 절단선 양쪽에 걸친 세트의 kept 쪽 멤버를 제거해 돌려준다.
// (세트 경계 절단, MVP2 확정 §13.1)
//
// 정렬 후에도 같은 세트의 멤버가 반드시 인접한다는 보장은 없다 —
// 다른 카테고리의 파일명이 사전순으로 사이에 끼어들 수 있다. 따라서
// "절단점에서 뒤로 물러나기" 가 아니라 잘린 쪽과 남은 쪽 양쪽에
// 걸친 세트를 집합으로 찾아 제거한다.
//
// SetKey 가 빈 후보(게이트 OFF / 소속 유보)는 참여하지 않는다.
func dropSplitSets(kept, cut []Candidate) (out []Candidate, moved int) {
	split := map[string]struct{}{}

	for _, c := range cut {
		if c.SetKey != "" {
			split[setID(c)] = struct{}{}
		}
	}

	if len(split) == 0 {
		return kept, 0
	}

	out = kept[:0]

	for _, c := range kept {
		if c.SetKey != "" {
			if _, ok := split[setID(c)]; ok {
				moved++
				continue
			}
		}

		out = append(out, c)
	}

	return out, moved
}
