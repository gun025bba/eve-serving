package handlers

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/cite"
)

func TestFilterChunkStreamSplit(t *testing.T) {
	cases := []struct {
		name   string
		chunks []string
		valid  []int
		want   string
	}{
		{"split marker removed", []string{"답이에요 [", "3", "]입니다"}, []int{1}, "답이에요입니다"},
		{"split marker kept", []string{"답 [", "1]"}, []int{1}, "답 [1]"},
		{"split run", []string{"끝.[1", "][2", "]"}, nil, "끝."},
		{"split fence", []string{"``", "`\n[1]\n``", "`\n [1]"}, nil, "```\n[1]\n```\n"},
		{"unterminated tail flushed", []string{"x [1"}, nil, "x [1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cf := cite.New(c.valid)
			got := ""
			for _, ch := range c.chunks {
				line, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": ch}}}})
				out := filterChunk(append([]byte("data: "), line...), cf)
				var v struct {
					Choices []struct {
						Delta struct{ Content string } `json:"delta"`
					} `json:"choices"`
				}
				if err := json.Unmarshal(out[6:], &v); err != nil {
					t.Fatal(err)
				}
				got += v.Choices[0].Delta.Content
			}
			got += cf.Flush()
			if got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
	// Non-content lines pass through byte-identical.
	raw := []byte(`data: {"z":1,"choices":[],"usage":{"prompt_tokens":3}}`)
	if string(filterChunk(raw, cite.New(nil))) != string(raw) {
		t.Fatal("non-content line modified")
	}
}

// The model engineer's 15 reference cases through the SSE (streaming) path
// with one rune per chunk and a final finish_reason chunk, and through the
// non-streaming message path.
func TestCiteReferenceCasesHandlerPaths(t *testing.T) {
	raw, err := os.ReadFile("../cite/testdata/citation_cases.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var c struct {
			ID, Text, Expected string
			Valid              []int
			Removed            int
		}
		if err := json.Unmarshal([]byte(ln), &c); err != nil {
			t.Fatal(err)
		}
		n++
		cf := cite.New(c.Valid)
		got := ""
		rs := []rune(c.Text)
		for i := 0; i <= len(rs); i++ {
			ch := map[string]any{"delta": map[string]any{}}
			if i < len(rs) {
				ch["delta"] = map[string]any{"content": string(rs[i])}
			} else {
				ch["finish_reason"] = "stop" // tail must arrive in this chunk
			}
			line, _ := json.Marshal(map[string]any{"choices": []any{ch}})
			out := filterChunk(append([]byte("data: "), line...), cf)
			var v struct {
				Choices []struct {
					Delta struct{ Content string } `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal(out[6:], &v); err != nil {
				t.Fatal(err)
			}
			got += v.Choices[0].Delta.Content
		}
		if rest := cf.Flush(); got != c.Expected || cf.Removed != c.Removed || rest != "" {
			t.Errorf("%s stream: %q,%d (rest %q) want %q,%d", c.ID, got, cf.Removed, rest, c.Expected, c.Removed)
		}
		msg := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": c.Text}}}}
		_, rm := filterMessageContent(msg, c.Valid)
		gotNS := msg["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)["content"]
		if gotNS != c.Expected || rm != c.Removed {
			t.Errorf("%s non-stream: %q,%d want %q,%d", c.ID, gotNS, rm, c.Expected, c.Removed)
		}
	}
	if n != 15 {
		t.Fatalf("want 15 cases, got %d", n)
	}
}
