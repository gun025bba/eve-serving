package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gun025bba/eve-serving/internal/listen"
)

// TestIdentityTrustedOnlyOnUnixSocket runs the real listeners: plain TCP
// (like 127.0.0.1:8000) and the unix socket that tailscale serve proxies to.
func TestIdentityTrustedOnlyOnUnixSocket(t *testing.T) {
	up := fakeUpstream(t, 0, nil, nil)
	defer up.Close()
	s := csrfServer(t, up.URL, []string{testOrigin}, io.Discard)
	h := s.Handler()

	tcp := httptest.NewServer(h) // no trusted ConnContext: the TCP listener
	defer tcp.Close()

	dir, _ := os.MkdirTemp("/tmp", "evs")
	defer os.RemoveAll(dir)
	os.Chmod(dir, 0o700)
	sock := filepath.Join(dir, "gw.sock")
	ul, err := listen.Unix(sock)
	if err != nil {
		t.Fatal(err)
	}
	us := listen.TrustedServer(h)
	go us.Serve(ul)
	defer us.Close()
	uc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	// tailscale serve keeps the public Host; Origin is the ts.net origin.
	const sockBase = "http://gateway.example.test"

	do := func(c *http.Client, base, method, path, body string, hdr map[string]string) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		b, _ := io.ReadAll(resp.Body)
		json.Unmarshal(b, &m)
		return resp.StatusCode, m
	}
	ident := map[string]string{"Tailscale-User-Login": "owner@example", "Tailscale-User-Name": "Owner"}
	withKey := map[string]string{"Tailscale-User-Login": "owner@example", "Authorization": "Bearer " + key}
	ui := map[string]string{"Tailscale-User-Login": "owner@example", "X-Eve-Request": "1", "Origin": testOrigin, "Content-Type": "application/json"}
	const chat = `{"messages":[{"role":"user","content":"hi"}]}`

	// TCP: identity header ignored
	if c, _ := do(tcp.Client(), tcp.URL, "GET", "/whoami", "", ident); c != 401 {
		t.Fatal("tcp identity accepted", c)
	}
	if c, _ := do(tcp.Client(), tcp.URL, "POST", "/v1/chat/completions", chat, ui); c != 401 {
		t.Fatal("tcp ui chat accepted", c)
	}
	// TCP: header + key -> key user only
	if c, m := do(tcp.Client(), tcp.URL, "GET", "/whoami", "", withKey); c != 200 || m["user"] != "api-key" || m["provider"] != "api-key" {
		t.Fatal("tcp key", c, m)
	}
	// socket: identity accepted
	if c, m := do(uc, sockBase, "GET", "/whoami", "", ident); c != 200 || m["user"] != "owner@example" || m["provider"] != "tailscale-identity" {
		t.Fatal("socket identity", c, m)
	}
	// socket: allowlist still applies
	if c, _ := do(uc, sockBase, "GET", "/whoami", "", map[string]string{"Tailscale-User-Login": "mallory@example"}); c != 403 && c != 401 {
		t.Fatal("socket non-allowlisted", c)
	}
	if c, _ := do(uc, sockBase, "GET", "/whoami", "", nil); c != 401 {
		t.Fatal("socket no identity", c)
	}
	// socket: CSRF/Origin still enforced and working
	if c, _ := do(uc, sockBase, "POST", "/v1/chat/completions", chat, ui); c != 200 {
		t.Fatal("socket ui chat", c)
	}
	bad := map[string]string{}
	for k, v := range ui {
		bad[k] = v
	}
	bad["Origin"] = "https://evil.example.test"
	if c, _ := do(uc, sockBase, "POST", "/v1/chat/completions", chat, bad); c != 403 {
		t.Fatal("socket cross-origin chat", c)
	}
}
