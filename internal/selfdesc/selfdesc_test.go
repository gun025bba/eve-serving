package selfdesc

import (
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/memory"
)

func TestBuild_MemoryStates(t *testing.T) {
	cases := []struct {
		name    string
		st      State
		want    []string
		notWant []string
	}{
		{"on+propose", State{Memory: MemoryOn, Propose: true}, []string{"메모리: 켜짐", "확인 카드", "삭제 카드", "설정 > 메모리", "/v1/memory"}, []string{"첫 사용 안내", "꺼짐", "사용할 수 없어요"}},
		{"on, no propose", State{Memory: MemoryOn}, []string{"메모리: 켜짐", "직접 하도록", "설정 > 메모리"}, []string{"확인 카드", "삭제 카드"}},
		{"intro not seen", State{Memory: MemoryPending}, []string{"첫 사용 안내", "메모리 첫 안내", "반영되지 않고", "설정 > 메모리"}, []string{"확인 카드", "메모리: 켜짐."}},
		{"user off", State{Memory: MemoryOff}, []string{"메모리: 꺼짐", "아무것도 기억·저장하지 않아요", "켤 수 있어요"}, []string{"확인 카드", "/v1/memory"}},
		{"unavailable", State{Memory: MemoryUnavailable}, []string{"메모리: 이 연결에서는 사용할 수 없어요"}, []string{"확인 카드", "/v1/memory", "설정 > 메모리"}},
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

func TestBuild_ToolsListEnabledOnly(t *testing.T) {
	if got := Build(State{}); !strings.Contains(got, "도구: 없음") {
		t.Fatal(got)
	}
	got := Build(State{Tools: []Tool{{Name: "웹 검색", Detail: "켜짐"}, {Name: "기억 제안", Detail: "memory_propose"}}})
	if !strings.Contains(got, "도구: 웹 검색(켜짐), 기억 제안(memory_propose)") {
		t.Fatal(got)
	}
	// a future tool appears without touching the template
	got = Build(State{Tools: []Tool{{Name: "캘린더"}}})
	if !strings.Contains(got, "도구: 캘린더") {
		t.Fatal(got)
	}
	// features that are not enabled are never named
	for _, no := range []string{"캘린더", "브리핑", "PIM", "pim"} {
		if strings.Contains(Build(State{Memory: MemoryOn, Propose: true, Tools: []Tool{{Name: "웹 검색", Detail: "켜짐"}}}), no) {
			t.Fatalf("disabled feature %q mentioned", no)
		}
	}
}

func TestBuild_Version(t *testing.T) {
	if got := Build(State{Version: "5.0.2+abc1234"}); !strings.Contains(got, "- 버전: 5.0.2+abc1234\n") {
		t.Fatal(got)
	}
	for _, v := range []string{"", "  ", "a b", "x\ny", strings.Repeat("9", 41), "5.0.2\n- 도구: 캘린더", "<x>"} {
		if got := Build(State{Version: v}); strings.Contains(got, "버전") {
			t.Fatalf("unsafe version %q accepted:\n%s", v, got)
		}
	}
}

func TestBuild_Compact(t *testing.T) {
	// longest realistic variant: memory on with propose, one tool with a long detail
	got := Build(State{Version: "5.0.2+abcdef123456", Memory: MemoryOn, Propose: true,
		Tools: []Tool{{Name: "웹 검색", Detail: "꺼짐, 입력창 웹 검색 버튼으로 켬"}, {Name: "기억 제안", Detail: "memory_propose"}}})
	if n := memory.EstimateTokens(got); n >= 200 {
		t.Fatalf("self-description is %d tokens (budget 200):\n%s", n, got)
	}
	// forbidden product names, assembled from fragments so the source never
	// spells them out
	forbidden := []string{strings.Join([]string{"jar", "vis"}, ""), string([]rune{0xC790, 0xBE44, 0xC2A4})}
	for _, w := range forbidden {
		if strings.Contains(strings.ToLower(got), w) {
			t.Fatal("forbidden word in the self-description")
		}
	}
}
