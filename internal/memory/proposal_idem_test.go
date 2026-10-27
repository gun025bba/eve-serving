package memory

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestExecute_IdempotentByAction(t *testing.T) {
	fs, err := Open(filepath.Join(t.TempDir(), "m"))
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"op":"add","items":[{"kind":"fact","text":"고양이를 키운다"},{"kind":"fact","text":"커피는 디카페인"}]}`)
	c := ProposalCtx{Actor: "user", ActionID: "01JBACTION", ActionCreated: time.Unix(1700000000, 0)}
	a, err := Execute(fs, 7, raw, c, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Execute(fs, 7, raw, c, nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	d, _ := fs.Load(7)
	if len(d.Items) != 2 || a[0] != b[0] || a[1] != b[1] {
		t.Fatalf("not idempotent: %d items %v %v", len(d.Items), a, b)
	}
}

func TestHasBlockMarker_CfAndCompat(t *testing.T) {
	for _, s := range []string{"<user\u200d_memory>", "<\u2066user_memory>", "＜ｕｓｅｒ＿ｍｅｍｏｒｙ＞", "</user\u00admemory>"} {
		if !HasBlockMarker(s) {
			t.Errorf("not caught: %q", s)
		}
	}
}
