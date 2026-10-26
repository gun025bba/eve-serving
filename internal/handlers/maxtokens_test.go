package handlers

import (
	"testing"

	"github.com/gun025bba/llm-serving/internal/config"
)

func TestMaxTokensCap(t *testing.T) {
	s := &Server{Cfg: config.Config{Model: "m", MaxTokensCap: 100, Defaults: map[string]any{"max_tokens": 50}}}
	b, err := s.prepareBody([]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":999,"max_completion_tokens":5000}`))
	if err != nil || b["max_tokens"] != 100 || b["max_completion_tokens"] != 100 {
		t.Fatalf("clamp failed: %v %v", b, err)
	}
	b, _ = s.prepareBody([]byte(`{"messages":[{"role":"user","content":"x"}]}`))
	if b["max_tokens"] != 50 {
		t.Fatalf("default not applied: %v", b)
	}
	if _, err := s.prepareBody([]byte(`{"messages":[{"role":"user","content":"x"}],"max_tokens":0}`)); err == nil {
		t.Fatal("expected 400 for max_tokens 0")
	}
}
