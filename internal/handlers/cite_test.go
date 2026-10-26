package handlers

import (
	"encoding/json"
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
		{"split marker removed", []string{"답이에요 [", "3", "]입니다"}, []int{1}, "답이에요 입니다"},
		{"split marker kept", []string{"답 [", "1]"}, []int{1}, "답 [1]"},
		{"split run", []string{"끝.[1", "][2", "]"}, nil, "끝."},
		{"split fence", []string{"``", "`\n[1]\n``", "`\n [1]"}, nil, "```\n[1]\n```\n "},
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
