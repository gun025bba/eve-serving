package handlers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFeedbackReasons(t *testing.T) {
	e := newEnv(t, true)
	r, b := e.call(t, "alice@example", "POST", "/v1/chat/completions", `{"messages":[{"role":"user","content":"질문"}],"thread_id":"new"}`)
	if r.StatusCode != 200 {
		t.Fatal(string(b))
	}
	mid, tid := js(t, b)["message_id"].(string), js(t, b)["thread_id"].(string)
	put := func(body string) (int, map[string]any) {
		r, b := e.call(t, "alice@example", "PUT", "/v1/messages/"+mid+"/feedback", body)
		var m map[string]any
		json.Unmarshal(b, &m)
		return r.StatusCode, m
	}
	errCode := func(m map[string]any) any { return m["error"].(map[string]any)["code"] }

	// old client: no reasons/comment
	if c, m := put(`{"rating":"up"}`); c != 200 || len(m["reasons"].([]any)) != 0 || m["comment"] != nil {
		t.Fatal(c, m)
	}
	// dedupe + trim
	c, m := put(`{"rating":"down","reasons":["wrong_info","too_long","wrong_info"],"comment":"  틀렸어요  "}`)
	if c != 200 || len(m["reasons"].([]any)) != 2 || m["comment"] != "틀렸어요" {
		t.Fatal(c, m)
	}
	// polarity mismatch / unknown code
	for _, body := range []string{`{"rating":"up","reasons":["wrong_info"]}`, `{"rating":"down","reasons":["accurate"]}`, `{"rating":"down","reasons":["nope"]}`} {
		if c, m := put(body); c != 400 || errCode(m) != "invalid_feedback_reason" {
			t.Fatal(body, c, m)
		}
	}
	// comment length is in characters (140 ok, 141 not)
	if c, _ := put(`{"rating":"down","comment":"` + strings.Repeat("가", 140) + `"}`); c != 200 {
		t.Fatal(c)
	}
	if c, m := put(`{"rating":"down","comment":"` + strings.Repeat("가", 141) + `"}`); c != 400 || errCode(m) != "feedback_comment_too_long" {
		t.Fatal(c, m)
	}
	// full replace: omitting reasons/comment clears them
	put(`{"rating":"down","reasons":["bad_source"],"comment":"x"}`)
	if c, m := put(`{"rating":"down"}`); c != 200 || len(m["reasons"].([]any)) != 0 || m["comment"] != nil {
		t.Fatal(c, m)
	}
	put(`{"rating":"up","reasons":["accurate","concise"],"comment":"좋아요"}`)
	_, b = e.call(t, "alice@example", "GET", "/v1/threads/"+tid+"/messages", "")
	fb := js(t, b)["data"].([]any)[1].(map[string]any)["feedback"].(map[string]any)
	if fb["rating"] != "up" || len(fb["reasons"].([]any)) != 2 || fb["comment"] != "좋아요" {
		t.Fatal(fb)
	}

	// export: API key and allowlisted identity; field order fixed
	for _, who := range []string{"", "bob@example"} {
		r, b = e.call(t, who, "GET", "/v1/feedback/export?since=2000-01-01&format=jsonl", "")
		if r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-ndjson") {
			t.Fatal(r.StatusCode, string(b))
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		n := 0
		for sc.Scan() {
			n++
			line := sc.Text()
			if !strings.HasPrefix(line, `{"message":"질문","response":"답변","rating":"up","reasons":["accurate","concise"],"comment":"좋아요","created_at":`) ||
				!strings.Contains(line, `"thread_id":"`+tid+`","message_id":"`+mid+`"}`) {
				t.Fatal(line)
			}
		}
		if n != 1 {
			t.Fatal(n)
		}
	}
	if r, _ := e.call(t, "", "GET", "/v1/feedback/export?since=2999-01-01", ""); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := e.call(t, "", "GET", "/v1/feedback/export?format=csv", ""); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := e.call(t, "", "GET", "/v1/feedback/export?since=yesterday", ""); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
}
