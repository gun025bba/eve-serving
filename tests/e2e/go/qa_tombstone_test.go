package memory

// QA (#52 b25a1e7): a retried Execute of the same approved action must not re-add an
// item the user deleted in between (tombstone-aware), while undeleted items stay.
import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestQA_TombstoneNotReAdded(t *testing.T) {
	fs, err := Open(filepath.Join(t.TempDir(), "m"))
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"op":"add","items":[{"kind":"fact","text":"고양이를 키운다"},{"kind":"fact","text":"아침에 커피를 마신다"}]}`)
	c := ProposalCtx{Actor: "user", ActionID: "01QATOMBSTONE1", ActionCreated: time.Unix(1700000000, 0)}
	ids, err := Execute(fs, 9, raw, c, nil, time.Now())
	if err != nil || len(ids) != 2 {
		t.Fatalf("first execute: %v %v", ids, err)
	}
	if err := Delete(fs, 9, ids[:1], time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := Execute(fs, 9, raw, c, nil, time.Now()); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	d, _ := fs.Load(9)
	if len(d.Items) != 1 || d.Items[0].ID != ids[1] {
		t.Fatalf("after retries want only %s, got %+v", ids[1], d.Items)
	}
	for _, it := range d.Items {
		if it.ID == ids[0] {
			t.Fatal("tombstoned ID re-added")
		}
	}
}
