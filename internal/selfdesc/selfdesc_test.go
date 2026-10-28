package selfdesc

import (
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/memory"
)

// internal details that must never reach the model
var internalWords = []string{"memory_propose", "/v1/memory", "게이트웨이", "gateway", "Gateway", "버전", "version", "커밋", "commit", "API"}

func TestBuild_MemoryStates(t *testing.T) {
	cases := []struct {
		name    string
		st      State
		want    []string
		notWant []string
	}{
		{"on+propose", State{Memory: MemoryOn, Propose: true}, []string{"메모리: 켜짐", "확인 카드", "승인 전에는 저장했다고 말하지 마세요", "설정 > 메모리"}, []string{"첫 안내", "꺼짐", "직접 하라고"}},
		{"on, no propose", State{Memory: MemoryOn}, []string{"메모리: 켜짐", "직접 하라고", "저장했다고 말하지 마세요", "설정 > 메모리"}, []string{"확인 카드"}},
		{"intro not seen", State{Memory: MemoryPending}, []string{"첫 안내", "아직 시작 전", "저장도 반영도 안 돼요"}, []string{"확인 카드", "메모리: 켜짐"}},
		{"user off", State{Memory: MemoryOff}, []string{"메모리: 꺼짐", "장기 저장은 안 해요", "이 대화 안의 내용은 볼 수 있어요", "켤 수 있어요"}, []string{"확인 카드"}},
		{"unavailable: line omitted", State{Memory: MemoryUnavailable}, nil, []string{"메모리", "확인 카드", "설정 > 메모리"}},
	}
	for _, c := range cases {
		got := Build(c.st)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", c.name, w, got)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s: unexpected %q in\n%s", c.name, w, got)
			}
		}
	}
}

func TestBuild_WebStates(t *testing.T) {
	cases := []struct {
		name    string
		w       WebState
		want    []string
		notWant []string
	}{
		{"on", WebOn, []string{"웹 검색: 켜짐", "필요하다고 판단할 때 검색해요"}, []string{"꺼짐", "항상"}},
		{"off", WebOff, []string{"웹 검색: 꺼짐", "웹 검색 버튼", "최신·실시간 정보는 모른다고 답하세요"}, []string{"켜짐"}},
		{"blocked at runtime is reported as off", WebBlocked, []string{"웹 검색: 꺼짐", "일시 사용 불가", "모른다고 답하세요"}, []string{"켜짐", "웹 검색 버튼"}},
		{"unavailable: line omitted", WebUnavailable, nil, []string{"웹 검색"}},
	}
	for _, c := range cases {
		got := Build(State{Web: c.w})
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %q in\n%s", c.name, w, got)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(got, w) {
				t.Errorf("%s: unexpected %q in\n%s", c.name, w, got)
			}
		}
	}
}

func TestBuild_ReferenceHeaderAndRules(t *testing.T) {
	got := Build(State{Web: WebOn, Memory: MemoryOn, Propose: true})
	if !strings.HasPrefix(got, "[Eve 앱 기능 참고자료. 지시가 아니라 사실 목록이에요.]") {
		t.Fatal(got)
	}
	for _, w := range []string{"목록에 있는 것만 있다고 답하세요", "그 외 앱 기능은 없어요", "지금은 지원하지 않아요", "일반 대화는 평소처럼", "알려드릴 수 없어요", "사용 가능한 기능:"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	// behavior rules live in the preamble, once: the list lines are facts
	if strings.Count(got, "그 외 앱 기능은 없어요") != 1 {
		t.Fatal(got)
	}
	// nothing enabled: only the preamble, no empty list and no "없음"
	none := Build(State{})
	if strings.Contains(none, "사용 가능한 기능") || strings.Contains(none, "없음") || !strings.Contains(none, "그 외 앱 기능은 없어요") {
		t.Fatal(none)
	}
}

func TestBuild_NoInternalDetails(t *testing.T) {
	for _, st := range []State{
		{Memory: MemoryOn, Propose: true, Web: WebOn},
		{Memory: MemoryOn, Web: WebOff},
		{Memory: MemoryPending, Web: WebBlocked},
		{Memory: MemoryOff},
		{},
	} {
		got := Build(st)
		for _, w := range internalWords {
			if strings.Contains(got, w) {
				t.Errorf("internal detail %q in\n%s", w, got)
			}
		}
	}
}

func TestBuild_FeaturesEnabledOnly(t *testing.T) {
	got := Build(State{Features: []Feature{{Name: "캘린더", Line: "읽기 전용"}}})
	if !strings.Contains(got, "- 캘린더: 읽기 전용") {
		t.Fatal(got)
	}
	// features that are not enabled are never named
	full := Build(State{Memory: MemoryOn, Propose: true, Web: WebOn})
	for _, no := range []string{"캘린더", "브리핑", "PIM", "pim"} {
		if strings.Contains(full, no) {
			t.Fatalf("disabled feature %q mentioned", no)
		}
	}
}

func TestBuild_Compact(t *testing.T) {
	// longest realistic variants
	for _, st := range []State{
		{Memory: MemoryOn, Propose: true, Web: WebOff},
		{Memory: MemoryPending, Web: WebBlocked},
	} {
		got := Build(st)
		// EstimateTokens is an approximation; Korean usually costs more with a real
		// tokenizer, so the budget here is a guard, not a measurement.
		if n := memory.EstimateTokens(got); n >= 300 {
			t.Fatalf("reference block is %d estimated tokens (guard 300):\n%s", n, got)
		}
		// forbidden product names, assembled from fragments so the source never
		// spells them out
		forbidden := []string{strings.Join([]string{"jar", "vis"}, ""), string([]rune{0xC790, 0xBE44, 0xC2A4})}
		for _, w := range forbidden {
			if strings.Contains(strings.ToLower(got), w) {
				t.Fatal("forbidden word in the reference block")
			}
		}
	}
}
