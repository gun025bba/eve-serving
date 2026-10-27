package handlers

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/memstore"
	"github.com/gun025bba/eve-serving/internal/tools"
)

const alice, bob = "alice@example", "bob@example"

type penv struct {
	s   *Server
	h   http.Handler
	st  *memstore.Store
	log *strings.Builder
	led *audit.Memory
}

func proactiveEnv(t *testing.T, mut func(*config.Config)) *penv {
	logbuf := &strings.Builder{}
	var mu sync.Mutex
	s := csrfServer(t, "http://127.0.0.1:1", []string{testOrigin}, lockedWriter{&mu, logbuf})
	s.Cfg.AllowedUsers = []string{alice, bob}
	s.Cfg.Push = config.PushConfig{Enabled: true, VAPIDPublicKey: "BPUBLICKEYPLACEHOLDER", VAPIDKeyID: "k1"}
	setKill(t, "")
	if mut != nil {
		mut(&s.Cfg)
	}
	st := memstore.New()
	s.Store = st
	led := &audit.Memory{}
	s.Audit = led
	return &penv{s: s, h: s.Handler(), st: st, log: logbuf, led: led}
}

func (e *penv) do(as, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set(testUserHeader, as)
	browserHeaders(req)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *penv) proposal(t *testing.T, as string, withItems bool) store.Action {
	t.Helper()
	th, _ := e.st.CreateThread(context.Background(), as, "t")
	m, _ := e.st.AddMessage(context.Background(), as, store.Message{ThreadID: th.ID, Role: "assistant", Content: "x"})
	payload := `{"op":"add","items":[{"text":"a"}]}`
	if withItems {
		payload = `{"op":"delete","items":[{"id":"i1","text":"a"},{"id":"i2","text":"b"},{"id":"i3","text":"c"}]}`
	}
	a, err := e.st.CreateAction(context.Background(), as, store.Action{
		ServerFields: store.ServerFields{ThreadID: &th.ID, MessageID: &m.ID, Tool: "memory.propose", ToolVersion: "1.0.0",
			Scopes: []string{"memory.write"}, Provenance: store.Provenance{Actor: "user"}, Tainted: true},
		ModelFields: store.ModelFields{Summary: "remember", PayloadView: json.RawMessage(payload)},
		Payload:     json.RawMessage(payload)})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func walkKeys(v any, fn func(string)) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			fn(k)
			walkKeys(e, fn)
		}
	case []any:
		for _, e := range x {
			walkKeys(e, fn)
		}
	}
}

func TestActionsList_NoTokenRelatedKeys(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	if r := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	r := e.do(alice, "GET", "/v1/actions", "")
	if r.Code != 200 {
		t.Fatal(r.Code)
	}
	var v map[string]any
	json.Unmarshal(r.Body.Bytes(), &v)
	walkKeys(v, func(k string) {
		if strings.Contains(strings.ToLower(k), "token") || k == "root_id" {
			t.Fatalf("forbidden key %q in %s", k, r.Body.String())
		}
	})
	items := v["data"].([]any)
	if len(items) != 1 {
		t.Fatal(r.Body.String())
	}
	it := items[0].(map[string]any)
	for _, k := range []string{"action_id", "thread_id", "message_id", "payload_hash", "payload_view", "provenance", "scopes", "expires_at", "tainted"} {
		if _, ok := it[k]; !ok {
			t.Fatalf("missing %s: %v", k, it)
		}
	}
	if it["thread_id"] != *a.ThreadID || it["message_id"] != *a.MessageID || it["payload_hash"] != a.PayloadHash {
		t.Fatal(it)
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("no-store")
	}
}

func TestActionsList_OnlySessionUserAndPending(t *testing.T) {
	e := proactiveEnv(t, nil)
	e.proposal(t, bob, false)
	a := e.proposal(t, alice, false)
	e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", "") // voids a, creates successor
	var v struct{ Data []map[string]any }
	json.Unmarshal(e.do(alice, "GET", "/v1/actions", "").Body.Bytes(), &v)
	if len(v.Data) != 1 || v.Data[0]["action_id"] == a.ID {
		t.Fatalf("%v", v.Data)
	}
	if r := e.do(alice, "GET", "/v1/actions?status=approved", ""); r.Code != 400 {
		t.Fatal(r.Code)
	}
}

func TestActionsList_SchedulerProposalNullMessage(t *testing.T) {
	e := proactiveEnv(t, nil)
	job, name := "J1", "아침 브리핑"
	e.st.CreateAction(context.Background(), alice, store.Action{
		ServerFields: store.ServerFields{Tool: "proactive.propose_followup", Provenance: store.Provenance{Actor: "scheduler", JobID: &job, JobName: &name}},
		Payload:      json.RawMessage(`{}`)})
	var v struct{ Data []map[string]any }
	json.Unmarshal(e.do(alice, "GET", "/v1/actions", "").Body.Bytes(), &v)
	if v.Data[0]["message_id"] != nil || v.Data[0]["thread_id"] != nil {
		t.Fatal(v.Data)
	}
	p := v.Data[0]["provenance"].(map[string]any)
	if p["actor"] != "scheduler" || p["job_id"] != job || p["job_name"] != name {
		t.Fatal(p)
	}
}

func TestActionReissue_FirstIssueCountsTowardCap3(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	var toks []string
	for i := 0; i < 3; i++ {
		r := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
		if r.Code != 200 {
			t.Fatal(i, r.Code, r.Body.String())
		}
		var c map[string]any
		json.Unmarshal(r.Body.Bytes(), &c)
		tok, _ := c["confirm_token"].(string)
		if len(tok) < 40 || c["token_expires_at"] == nil || c["payload_hash"] != a.PayloadHash {
			t.Fatal(c)
		}
		toks = append(toks, tok)
	}
	if toks[0] == toks[1] || toks[1] == toks[2] {
		t.Fatal("tokens must differ")
	}
	r := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
	if r.Code != 422 || r.Body.String() != `{"error_code":"cap_exceeded"}` {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestActionReissue_InvalidatesPreviousToken(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	r1 := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
	var c1 store.IssuedConfirmation
	json.Unmarshal(r1.Body.Bytes(), &c1)
	e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
	as, _ := e.st.ListPendingActions(context.Background(), alice)
	if *as[0].TokenSHA256 == store.TokenHash(c1.ConfirmToken) {
		t.Fatal("previous token still valid")
	}
}

func TestActionReissue_OtherUser404(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, bob, false)
	other := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
	missing := e.do(alice, "POST", "/v1/actions/01J0000000000000000000000X/reissue", "")
	if other.Code != 404 || other.Body.String() != `{"error_code":"not_found"}` {
		t.Fatal(other.Code, other.Body.String())
	}
	sameNotFound(t, other, missing)
	// bob's proposal untouched
	as, _ := e.st.ListPendingActions(context.Background(), bob)
	if as[0].IssueCount != 0 {
		t.Fatal("foreign reissue counted")
	}
}

func TestActionReissue_NonPending409(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", "")
	r := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
	if r.Code != 409 || r.Body.String() != `{"error_code":"conflict"}` {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestActionReissue_CSRFAndExactOrigin(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	for _, o := range []string{"", "null", testOrigin + ".evil", "https://evil.example.test"} {
		req := httptest.NewRequest("POST", "/v1/actions/"+a.ID+"/reissue", nil)
		req.Header.Set(testUserHeader, alice)
		req.Header.Set(HeaderEveRequest, "1")
		if o != "" {
			req.Header.Set("Origin", o)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Fatalf("origin %q: %d", o, rec.Code)
		}
	}
	as, _ := e.st.ListPendingActions(context.Background(), alice)
	if as[0].IssueCount != 0 {
		t.Fatal("rejected request issued a token")
	}
}

func TestProposalRevise_ResponseHasTokenAndNewHash(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, true)
	r := e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", `{"item_ids":["i1","i3"]}`)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	var c map[string]any
	json.Unmarshal(r.Body.Bytes(), &c)
	if c["confirm_token"] == nil || c["token_expires_at"] == nil || c["action_id"] == a.ID || c["payload_hash"] == a.PayloadHash {
		t.Fatal(c)
	}
	if _, ok := c["root_id"]; ok {
		t.Fatal("root_id exported")
	}
	items := c["payload_view"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatal(items)
	}
	pv := c["provenance"].(map[string]any)
	if pv["actor"] != "user" || c["tainted"] != true || c["scopes"].([]any)[0] != "memory.write" {
		t.Fatal("inheritance", c)
	}
}

func TestProposalRevise_BodyOnlyItemIDsElse400(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, true)
	for _, b := range []string{`{"root_id":"x"}`, `{"item_ids":["i1"],"payload":{}}`, `{"item_ids":null}`, `{"item_ids":[]}`,
		`{"item_ids":["nope"]}`, `{"item_ids":["i1","i1"]}`, `[1]`, `{"item_ids":["i1"]} {}`} {
		r := e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", b)
		if r.Code != 400 || r.Body.String() != `{"error_code":"invalid"}` {
			t.Fatalf("%s: %d %s", b, r.Code, r.Body.String())
		}
	}
	// item_ids on a proposal without item ids
	b := e.proposal(t, alice, false)
	if r := e.do(alice, "POST", "/v1/proposals/"+b.ID+"/revise", `{"item_ids":["i1"]}`); r.Code != 400 {
		t.Fatal(r.Code)
	}
}

func TestProposalRevise_EmptyBodyReissuesSamePayload(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	for _, b := range []string{"", "{}"} {
		r := e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", b)
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var c store.IssuedConfirmation
		json.Unmarshal(r.Body.Bytes(), &c)
		if c.PayloadHash != a.PayloadHash {
			t.Fatal("payload changed")
		}
		a.ID = c.ID
	}
}

func TestProposalRevise_IssueCapAcrossChain(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, false)
	e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "") // 1
	var c store.IssuedConfirmation
	json.Unmarshal(e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", "").Body.Bytes(), &c) // 2
	if r := e.do(alice, "POST", "/v1/actions/"+c.ID+"/reissue", ""); r.Code != 200 {          // 3
		t.Fatal(r.Code)
	}
	if r := e.do(alice, "POST", "/v1/actions/"+c.ID+"/reissue", ""); r.Code != 422 {
		t.Fatal(r.Code)
	}
	if r := e.do(alice, "POST", "/v1/proposals/"+c.ID+"/revise", ""); r.Code != 422 || r.Body.String() != `{"error_code":"cap_exceeded"}` {
		t.Fatal(r.Code, r.Body.String())
	}
	// failed revise leaves the proposal pending
	as, _ := e.st.ListPendingActions(context.Background(), alice)
	if len(as) != 1 || as[0].ID != c.ID {
		t.Fatal(as)
	}
}

func TestProposalRevise_ExpiryCappedAtRoot24h410(t *testing.T) {
	e := proactiveEnv(t, nil)
	old := time.Now().UTC().Add(-25 * time.Hour)
	a, _ := e.st.CreateAction(context.Background(), alice, store.Action{ServerFields: store.ServerFields{Tool: "x", Provenance: store.Provenance{Actor: "user"},
		CreatedAt: old}, Payload: json.RawMessage(`{}`)})
	if r := e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", ""); r.Code != 410 || r.Body.String() != `{"error_code":"expired"}` {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", ""); r.Code != 410 {
		t.Fatal(r.Code)
	}
	// successor of a 23h-old root expires at root + 24h
	b, _ := e.st.CreateAction(context.Background(), alice, store.Action{ServerFields: store.ServerFields{Tool: "x", Provenance: store.Provenance{Actor: "user"},
		CreatedAt: time.Now().UTC().Add(-23 * time.Hour)}, Payload: json.RawMessage(`{}`)})
	var c store.IssuedConfirmation
	json.Unmarshal(e.do(alice, "POST", "/v1/proposals/"+b.ID+"/revise", "").Body.Bytes(), &c)
	if !c.ExpiresAt.Equal(b.CreatedAt.Add(24*time.Hour)) || c.TokenExpiresAt.After(c.ExpiresAt) {
		t.Fatal(c.ExpiresAt, b.CreatedAt)
	}
}

func TestProposalRevise_OtherUser404AndNotFoundIdentical(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, bob, true)
	other := e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", `{"item_ids":["i1"]}`)
	missing := e.do(alice, "POST", "/v1/proposals/01J0000000000000000000000X/revise", `{"item_ids":["i1"]}`)
	sameNotFound(t, other, missing)
	if as, _ := e.st.ListPendingActions(context.Background(), bob); len(as) != 1 || as[0].ID != a.ID {
		t.Fatal("foreign proposal changed")
	}
}

// sameNotFound: TestNotFound_BodyBytesIdenticalMissingVsOtherUser / HeadersIdentical.
func sameNotFound(t *testing.T, a, b *httptest.ResponseRecorder) {
	t.Helper()
	if a.Code != 404 || b.Code != 404 {
		t.Fatal(a.Code, b.Code)
	}
	if a.Body.String() != b.Body.String() || a.Body.String() != `{"error_code":"not_found"}` {
		t.Fatalf("%q vs %q", a.Body.String(), b.Body.String())
	}
	ha, hb := a.Header().Clone(), b.Header().Clone()
	ha.Del("X-Request-ID")
	hb.Del("X-Request-ID")
	if len(ha) != len(hb) {
		t.Fatal(ha, hb)
	}
	for k := range ha {
		if strings.Join(ha[k], ",") != strings.Join(hb[k], ",") {
			t.Fatalf("header %s: %v vs %v", k, ha[k], hb[k])
		}
	}
	if ha.Get("Cache-Control") != "no-store" || ha.Get("Content-Length") == "" {
		t.Fatal(ha)
	}
}

func TestErrorCodes_StatusMapping(t *testing.T) {
	want := map[string]int{"invalid": 400, "scope_denied": 403, "tainted": 403, "kill_switch": 403, "not_found": 404,
		"item_gone": 409, "conflict": 409, "expired": 410, "secret_detected": 422, "cap_exceeded": 422, "payload_mismatch": 422}
	codes := ErrorCodes()
	if len(codes) != len(want) {
		t.Fatal(codes)
	}
	e := proactiveEnv(t, nil)
	for _, c := range codes {
		rec := httptest.NewRecorder()
		e.s.writeCode(rec, httptest.NewRequest("GET", "/", nil), c)
		if rec.Code != want[c] || rec.Body.String() != `{"error_code":"`+c+`"}` {
			t.Fatal(c, rec.Code, rec.Body.String())
		}
	}
}

func TestTokenEndpoints_CacheNoStoreAndBodiesNotLogged(t *testing.T) {
	e := proactiveEnv(t, nil)
	a := e.proposal(t, alice, true)
	r := e.do(alice, "POST", "/v1/actions/"+a.ID+"/reissue", "")
	var c store.IssuedConfirmation
	json.Unmarshal(r.Body.Bytes(), &c)
	r2 := e.do(alice, "POST", "/v1/proposals/"+a.ID+"/revise", `{"item_ids":["i2"]}`)
	var c2 store.IssuedConfirmation
	json.Unmarshal(r2.Body.Bytes(), &c2)
	for _, x := range []*httptest.ResponseRecorder{r, r2, e.do(alice, "GET", "/v1/actions", "")} {
		if x.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("no-store")
		}
	}
	logs := e.log.String()
	for _, secret := range []string{c.ConfirmToken, c2.ConfirmToken, store.TokenHash(c.ConfirmToken), `"i2"`, "payload_view"} {
		if strings.Contains(logs, secret) {
			t.Fatalf("log leaks %q", secret)
		}
	}
	// TestAudit_NoTokenOrTokenHash: ledger has action_id + payload_hash only.
	rows := e.led.Rows()
	if len(rows) != 2 {
		t.Fatal(len(rows))
	}
	for _, row := range rows {
		b, _ := json.Marshal(row)
		for _, secret := range []string{c.ConfirmToken, c2.ConfirmToken, store.TokenHash(c.ConfirmToken), store.TokenHash(c2.ConfirmToken)} {
			if strings.Contains(string(b), secret) {
				t.Fatal("audit leaks token")
			}
		}
	}
	if rows[0].Event.PendingActionID != a.ID || rows[0].Event.PayloadSHA256 != a.PayloadHash {
		t.Fatal(rows[0].Event)
	}
}

func TestCORS_TokenEndpointsAndActionsNoAllowHeaders(t *testing.T) {
	e := proactiveEnv(t, func(c *config.Config) { c.CORSOrigins = []string{testOrigin} })
	a := e.proposal(t, alice, false)
	for _, p := range []string{"/v1/actions", "/v1/actions/" + a.ID + "/reissue", "/v1/proposals/" + a.ID + "/revise"} {
		for _, m := range []string{"OPTIONS", "GET", "POST"} {
			req := httptest.NewRequest(m, p, nil)
			req.Header.Set(testUserHeader, alice)
			browserHeaders(req)
			if m == "OPTIONS" {
				req.Header.Set("Access-Control-Request-Method", "POST")
			}
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			for k := range rec.Header() {
				if strings.HasPrefix(k, "Access-Control-Allow") {
					t.Fatalf("%s %s: %s", m, p, k)
				}
			}
		}
	}
}

func TestConfirmationRedaction_ByStructOrigin(t *testing.T) {
	const secret = "sk-canary-0123456789"
	tools.Default().Redact.Add(secret)
	e := proactiveEnv(t, nil)
	job := "J1"
	name := "job " + secret // server field: passes through untouched (never model data)
	pv := `{"job_name":"` + secret + `","provenance":{"job_name":"` + secret + `"},"template_kind":"` + secret + `"}`
	e.st.CreateAction(context.Background(), alice, store.Action{
		ServerFields: store.ServerFields{Tool: "x", Provenance: store.Provenance{Actor: "scheduler", JobID: &job, JobName: &name}},
		ModelFields:  store.ModelFields{Summary: "s " + secret, PayloadView: json.RawMessage(pv)},
		Payload:      json.RawMessage(`{}`)})
	var v struct{ Data []map[string]any }
	body := e.do(alice, "GET", "/v1/actions", "").Body.String()
	json.Unmarshal([]byte(body), &v)
	d := v.Data[0]
	pvb, _ := json.Marshal(d["payload_view"])
	if strings.Contains(string(pvb), secret) || strings.Contains(d["summary"].(string), secret) {
		t.Fatal("model fields not redacted (server-looking key names must not exempt them)", string(pvb))
	}
	if d["provenance"].(map[string]any)["job_name"] != name {
		t.Fatal("server field altered")
	}
}

func TestThreadsList_UnreadFilter(t *testing.T) {
	e := proactiveEnv(t, nil)
	ctx := context.Background()
	t1, _ := e.st.CreateThread(ctx, alice, "plain")
	e.st.AddMessage(ctx, alice, store.Message{ThreadID: t1.ID, Role: "user", Content: "hi"})
	t2, _ := e.st.CreateThread(ctx, alice, "briefing")
	job, kind, name := "J1", "briefing", "아침 브리핑"
	pm, err := e.st.AddMessage(ctx, alice, store.Message{ThreadID: t2.ID, Role: "assistant", Content: "good morning", Origin: store.OriginProactive,
		JobID: &job, TemplateKind: &kind, Provenance: &store.Provenance{JobName: &name}})
	if err != nil {
		t.Fatal(err)
	}
	tb, _ := e.st.CreateThread(ctx, bob, "bob")
	e.st.AddMessage(ctx, bob, store.Message{ThreadID: tb.ID, Role: "assistant", Content: "x", Origin: store.OriginProactive, JobID: &job})
	var v struct{ Data []store.Thread }
	json.Unmarshal(e.do(alice, "GET", "/v1/threads?unread=true", "").Body.Bytes(), &v)
	if len(v.Data) != 1 || v.Data[0].ID != t2.ID || v.Data[0].UnreadCount != 1 {
		t.Fatalf("%+v", v.Data)
	}
	json.Unmarshal(e.do(alice, "GET", "/v1/threads", "").Body.Bytes(), &v)
	if len(v.Data) != 2 {
		t.Fatal(v.Data)
	}
	if r := e.do(alice, "GET", "/v1/threads?unread=maybe", ""); r.Code != 400 {
		t.Fatal(r.Code)
	}
	// message shape: template_kind top-level, provenance {actor, job_id, job_name}
	var ms struct{ Data []map[string]any }
	json.Unmarshal(e.do(alice, "GET", "/v1/threads/"+t2.ID+"/messages", "").Body.Bytes(), &ms)
	m := ms.Data[0]
	p := m["provenance"].(map[string]any)
	if m["origin"] != "proactive" || m["template_kind"] != "briefing" || m["job_id"] != job || p["actor"] != "scheduler" || p["job_id"] != job || p["job_name"] != name {
		t.Fatal(m)
	}
	ms.Data = nil
	json.Unmarshal(e.do(alice, "GET", "/v1/threads/"+t1.ID+"/messages", "").Body.Bytes(), &ms)
	if ms.Data[0]["origin"] != "user" || ms.Data[0]["template_kind"] != nil || ms.Data[0]["provenance"] != nil {
		t.Fatal(ms.Data[0])
	}
	e.st.MarkRead(alice, pm.ID, time.Now())
	v.Data = nil
	json.Unmarshal(e.do(alice, "GET", "/v1/threads?unread=true", "").Body.Bytes(), &v)
	if len(v.Data) != 0 {
		t.Fatal(v.Data)
	}
}

// --- push ---

func p256dh(t *testing.T) string {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes())
}

func subBody(t *testing.T, ep string) string {
	auth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	b, _ := json.Marshal(map[string]any{"endpoint": ep, "keys": map[string]string{"p256dh": p256dh(t), "auth": auth}, "vapid_key_id": "k1"})
	return string(b)
}

const apple = "https://web.push.apple.com/QGuK"

func TestPushVapidKey_Returns(t *testing.T) {
	e := proactiveEnv(t, nil)
	r := e.do(alice, "GET", "/v1/push/vapid-public-key", "")
	if r.Code != 200 || strings.TrimSpace(r.Body.String()) != `{"key":"BPUBLICKEYPLACEHOLDER","key_id":"k1"}` {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestPushSubscribe_201AndUnsubscribe204(t *testing.T) {
	e := proactiveEnv(t, nil)
	if r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, apple+"/a")); r.Code != 201 {
		t.Fatal(r.Code, r.Body.String())
	}
	if e.st.PushSubscriptionOwner(apple+"/a") != alice {
		t.Fatal("not stored")
	}
	for _, ep := range []string{apple + "/a", apple + "/missing"} {
		if r := e.do(alice, "DELETE", "/v1/push/subscriptions", `{"endpoint":"`+ep+`"}`); r.Code != 204 {
			t.Fatal(r.Code)
		}
	}
	if e.st.PushSubscriptionOwner(apple+"/a") != "" {
		t.Fatal("not deleted")
	}
}

func TestPushSubscribe_EndpointValidation(t *testing.T) {
	e := proactiveEnv(t, nil)
	long := apple + "/" + strings.Repeat("a", 1024)
	for _, ep := range []string{"http://web.push.apple.com/x", "https://web.push.apple.com:8443/x", "https://x.web.push.apple.com/x",
		"https://web.push.apple.com.evil.test/x", "https://u:p@web.push.apple.com/x", "https://web.push.apple.com/x?q=1",
		"https://web.push.apple.com/x#f", "https://web.push.apple.com/x?", "https://17.0.0.1/x", "https://fcm.googleapis.com/x", long} {
		if r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, ep)); r.Code != 400 {
			t.Fatalf("%s: %d", ep, r.Code)
		}
	}
	if r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, "https://web.push.apple.com:443/x")); r.Code != 201 {
		t.Fatal(r.Code)
	}
}

func TestPushSubscribe_KeysAndKeyID(t *testing.T) {
	e := proactiveEnv(t, nil)
	good := p256dh(t)
	auth := base64.RawURLEncoding.EncodeToString(make([]byte, 16))
	off := make([]byte, 65)
	off[0] = 4
	off[64] = 1 // not on the curve
	bad := []map[string]any{
		{"endpoint": apple, "keys": map[string]string{"p256dh": base64.RawURLEncoding.EncodeToString(off), "auth": auth}, "vapid_key_id": "k1"},
		{"endpoint": apple, "keys": map[string]string{"p256dh": good[:20], "auth": auth}, "vapid_key_id": "k1"},
		{"endpoint": apple, "keys": map[string]string{"p256dh": good, "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 15))}, "vapid_key_id": "k1"},
		{"endpoint": apple, "keys": map[string]string{"p256dh": good, "auth": auth}, "vapid_key_id": "old"},
		{"endpoint": apple, "keys": map[string]string{"p256dh": good, "auth": auth}, "vapid_key_id": "k1", "extra": 1},
	}
	for i, b := range bad {
		js, _ := json.Marshal(b)
		if r := e.do(alice, "POST", "/v1/push/subscriptions", string(js)); r.Code != 400 {
			t.Fatal(i, r.Code)
		}
	}
}

func TestPushSubscribe_Cap5BeforeOwnership(t *testing.T) {
	e := proactiveEnv(t, nil)
	e.do(bob, "POST", "/v1/push/subscriptions", subBody(t, apple+"/bob"))
	for i := 0; i < 5; i++ {
		if r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, apple+"/"+string(rune('a'+i)))); r.Code != 201 {
			t.Fatal(i, r.Code)
		}
	}
	// a 6th, even one owned by bob, is cap_exceeded (cap checked first)
	for _, ep := range []string{apple + "/f", apple + "/bob"} {
		if r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, ep)); r.Code != 422 || r.Body.String() != `{"error_code":"cap_exceeded"}` {
			t.Fatal(ep, r.Code, r.Body.String())
		}
	}
	// re-registering one of my own is fine
	if r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, apple+"/a")); r.Code != 201 {
		t.Fatal(r.Code)
	}
}

func TestPushSubscribe_OtherUserEndpointNoTransferNoLeak(t *testing.T) {
	e := proactiveEnv(t, nil)
	ep := apple + "/shared"
	e.do(bob, "POST", "/v1/push/subscriptions", subBody(t, ep))
	r := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, ep))
	r2 := e.do(alice, "POST", "/v1/push/subscriptions", subBody(t, apple+"/fresh"))
	if r.Code != 201 || r.Body.String() != r2.Body.String() {
		t.Fatal(r.Code, r.Body.String(), r2.Body.String())
	}
	if e.st.PushSubscriptionOwner(ep) != bob {
		t.Fatal("ownership moved")
	}
	// alice's DELETE of bob's endpoint is 204 and a no-op
	if d := e.do(alice, "DELETE", "/v1/push/subscriptions", `{"endpoint":"`+ep+`"}`); d.Code != 204 || e.st.PushSubscriptionOwner(ep) != bob {
		t.Fatal("foreign delete")
	}
	// audit + ops alert carry only the endpoint hash
	found := false
	for _, row := range e.led.Rows() {
		b, _ := json.Marshal(row)
		if strings.Contains(string(b), "shared") {
			t.Fatal("raw endpoint in audit")
		}
		if row.Event.Event == "push_endpoint_conflict" && row.Event.Detail["endpoint_sha256"] == hashEndpoint(ep) {
			found = true
		}
	}
	if !found || !strings.Contains(e.log.String(), `"alert":"push_endpoint_conflict"`) || strings.Contains(e.log.String(), "shared") || strings.Contains(e.log.String(), hashEndpoint(ep)) || strings.Contains(e.log.String(), "endpoint_sha256") {
		t.Fatal("audit/ops alert", e.log.String())
	}
}

func TestPush_DisabledAll404(t *testing.T) {
	cases := map[string]func(*config.Config){
		"disabled": func(c *config.Config) { c.Push.Enabled = false },
		"no key":   func(c *config.Config) { c.Push.VAPIDPublicKey = "" },
	}
	for name, mut := range cases {
		e := proactiveEnv(t, mut)
		check404All(t, e, name)
	}
}

func check404All(t *testing.T, e *penv, name string) {
	t.Helper()
	ref := e.do(alice, "POST", "/v1/actions/01J0000000000000000000000X/reissue", "")
	for _, c := range []struct{ m, p, b string }{{"GET", "/v1/push/vapid-public-key", ""},
		{"POST", "/v1/push/subscriptions", subBody(t, apple)}, {"DELETE", "/v1/push/subscriptions", `{"endpoint":"` + apple + `"}`}} {
		r := e.do(alice, c.m, c.p, c.b)
		if r.Code != 404 {
			t.Fatalf("%s %s %s: %d", name, c.m, c.p, r.Code)
		}
		sameNotFound(t, r, ref)
	}
}

// setKill installs a kill switch backed by a 0600 file with body (healthy
// unless body says otherwise) and restores the previous switch afterwards.
func setKill(t *testing.T, body string) *tools.Killswitch {
	t.Helper()
	prev := tools.Default().Kill
	t.Cleanup(func() { tools.SetKillswitch(prev) })
	f := filepath.Join(t.TempDir(), "ks.yaml")
	if err := os.WriteFile(f, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ks := tools.NewKillswitch(f, os.Getuid())
	ks.Reload()
	tools.SetKillswitch(ks)
	return ks
}

func TestPush_KillSwitchAndReadFailure404(t *testing.T) {
	e := proactiveEnv(t, nil)
	if r := e.do(alice, "GET", "/v1/push/vapid-public-key", ""); r.Code != 200 {
		t.Fatal("healthy file without proactive_off should allow", r.Code)
	}
	setKill(t, "proactive_off: true\n")
	check404All(t, e, "proactive_off")
	setKill(t, "all_off: true\n")
	check404All(t, e, "all_off")
	// bad permissions = fail closed
	f := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(f, []byte("proactive_off: false\n"), 0o644)
	os.Chmod(f, 0o644)
	ks2 := tools.NewKillswitch(f, os.Getuid())
	ks2.Reload()
	tools.SetKillswitch(ks2)
	check404All(t, e, "bad perms")
	// nil kill switch (no path configured)
	tools.SetKillswitch(nil)
	check404All(t, e, "nil killswitch")
	// missing file at startup
	ks3 := tools.NewKillswitch(filepath.Join(t.TempDir(), "absent.yaml"), os.Getuid())
	ks3.Reload()
	tools.SetKillswitch(ks3)
	check404All(t, e, "missing file")
	// healthy again
	setKill(t, "")
	if r := e.do(alice, "GET", "/v1/push/vapid-public-key", ""); r.Code != 200 {
		t.Fatal(r.Code)
	}
}

func TestKillswitch_FileRemovedDisallowsProactive(t *testing.T) {
	f := filepath.Join(t.TempDir(), "ks.yaml")
	os.WriteFile(f, nil, 0o600)
	ks := tools.NewKillswitch(f, os.Getuid())
	ks.Reload()
	if !ks.ProactiveAllowed() {
		t.Fatal("healthy empty file should allow")
	}
	os.Remove(f)
	ks.Reload()
	if ks.ProactiveAllowed() {
		t.Fatal("removed file must disallow")
	}
}
