package tools

import (
	"strings"
	"testing"
	"time"
)

func TestEvePrompt(t *testing.T) {
	seoul := time.FixedZone("KST", 9*3600)
	now := time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC) // 2026-10-27 01:00 KST
	for _, c := range []struct {
		st        WebPromptState
		has, nots []string
	}{
		{WebUnavailable, []string{"Eve", "2026년 10월 27일 (화)"}, []string{"웹 검색", "web_search"}},
		{WebOff, []string{"Eve", "웹 검색 버튼을 켜고"}, []string{"web_search"}},
		{WebOn, []string{"Eve", "web_search", "[1]", "검색을 할 수 없다고 말하지 마세요"}, []string{"버튼을 켜고"}},
	} {
		p := EvePrompt(now, seoul, c.st)
		for _, s := range c.has {
			if !strings.Contains(p, s) {
				t.Errorf("state %d: missing %q", c.st, s)
			}
		}
		for _, s := range c.nots {
			if strings.Contains(p, s) {
				t.Errorf("state %d: unexpected %q", c.st, s)
			}
		}
		if strings.Contains(p, "2024") || strings.Contains(strings.ToLower(p), "google") {
			t.Errorf("state %d: cutoff/Google wording", c.st)
		}
	}
}

func TestWithSystemPrompt(t *testing.T) {
	msgs := []any{
		map[string]any{"role": "system", "content": "반말로 답해"},
		map[string]any{"role": "system", "content": "  "},
		map[string]any{"role": "user", "content": "hi"},
		map[string]any{"role": "system", "content": "mid"},
	}
	out := WithSystemPrompt("GW", msgs)
	if len(out) != 3 {
		t.Fatal(out)
	}
	sys := out[0].(map[string]any)["content"].(string)
	if !strings.HasPrefix(sys, "GW") || !strings.HasSuffix(sys, "반말로 답해") {
		t.Fatal(sys)
	}
	if out[1].(map[string]any)["role"] != "user" || out[2].(map[string]any)["content"] != "mid" {
		t.Fatal(out)
	}
	if out := WithSystemPrompt("GW", nil); len(out) != 1 {
		t.Fatal(out)
	}
}
