package push

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/store"
	"time"
)

func TestPushPayload_NidIsMessageIDUnder256B(t *testing.T) {
	id := store.NewID(time.Now())
	b, err := Payload(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":1,"type":"proactive","nid":"`+id+`"}` || len(b) > MaxPayloadLen {
		t.Fatal(string(b))
	}
	var m map[string]any
	json.Unmarshal(b, &m)
	if len(m) != 3 {
		t.Fatal(m)
	}
	for _, bad := range []string{"", "hello world", strings.Repeat("A", 300), "01J0000000000000000000000U"} {
		if _, err := Payload(bad); err == nil {
			t.Fatal(bad)
		}
	}
}

func TestULID_CryptoRandNonMonotonic(t *testing.T) {
	now := time.Now()
	a, b := store.NewID(now), store.NewID(now)
	if a == b || a[:10] != b[:10] {
		t.Fatal(a, b)
	}
	// monotonic mode would make b = a+1 in the random part
	if strings.TrimRight(a[10:], "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" {
		t.Fatal(a)
	}
}

func TestPushEndpoint_IPLiteralsRejected(t *testing.T) {
	for _, ep := range []string{"https://17.253.0.1/x", "https://[::1]/x", "https://0x11fd0001/x", "https://2130706433/x"} {
		if ValidateEndpoint(ep) == nil {
			t.Fatal(ep)
		}
	}
	if ValidateEndpoint("https://web.push.apple.com/abc") != nil {
		t.Fatal("valid rejected")
	}
}
