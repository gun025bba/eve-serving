package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// The OpenAI tools array built from manifests must be byte-identical to the
// v1.1.0 hard-coded toolJSON (docs/tool-platform.md §8-1).
func TestRegistry_DefsByteIdenticalToV110(t *testing.T) {
	want, err := os.ReadFile("testdata/tooljson_v1.1.0.golden")
	if err != nil {
		t.Fatal(err)
	}
	p := Default()
	if got := p.Reg.defsJSON(p.Exposed()); got != string(want) {
		t.Fatalf("tool defs differ from v1.1.0\n got: %s\nwant: %s", got, want)
	}
}

func TestRegistry_BuiltinNames(t *testing.T) {
	if got := strings.Join(Default().Reg.Names(), ","); got != "web_fetch,web_search" {
		t.Fatal(got)
	}
}

func manifestFS(name, manifest, schema string) fstest.MapFS {
	return fstest.MapFS{
		name + "/manifest.yaml": {Data: []byte(manifest)},
		name + "/schema.json":   {Data: []byte(schema)},
	}
}

const okManifest = `name: t_ok
version: 1.0.0
description: "d"
scopes: [web.read]
side_effect: read
taint_output: true
egress: [public_web]
secrets: []
timeout: 8s
max_result_chars: 100
exposure: {modes: [auto], max_per_turn: 1}
`
const okSchema = `{"type":"object","properties":{}}`

func TestManifest_Validation(t *testing.T) {
	if _, err := LoadFS(manifestFS("t_ok", okManifest, okSchema)); err != nil {
		t.Fatal("valid manifest rejected:", err)
	}
	bad := map[string]string{
		"unknown_field": okManifest + "extra: 1\n",
		"trade_effect":  strings.Replace(okManifest, "side_effect: read", "side_effect: trade", 1),
		"trade_scope":   strings.Replace(okManifest, "[web.read]", "[stock.trade]", 1),
		"bad_scope":     strings.Replace(okManifest, "[web.read]", "[web]", 1),
		"no_scope":      strings.Replace(okManifest, "[web.read]", "[]", 1),
		"bad_version":   strings.Replace(okManifest, "1.0.0", "1.0", 1),
		"timeout_high":  strings.Replace(okManifest, "8s", "31s", 1),
		"egress_ip":     strings.Replace(okManifest, "[public_web]", `["10.0.0.1:80"]`, 1),
		"egress_host":   strings.Replace(okManifest, "[public_web]", `["host.example"]`, 1),
		"secrets":       strings.Replace(okManifest, "secrets: []", "secrets: [k]", 1),
		"bad_mode":      strings.Replace(okManifest, "[auto]", "[always]", 1),
		"name_mismatch": strings.Replace(okManifest, "name: t_ok", "name: t_other", 1),
	}
	for k, m := range bad {
		if _, err := LoadFS(manifestFS("t_ok", m, okSchema)); err == nil {
			t.Errorf("%s: accepted", k)
		}
	}
	if _, err := LoadFS(manifestFS("t_ok", okManifest, `[]`)); err == nil {
		t.Error("non-object schema accepted")
	}
	if _, err := LoadFS(fstest.MapFS{"t_ok/manifest.yaml": {Data: []byte(okManifest)}}); err == nil {
		t.Error("missing schema accepted")
	}
}

func writeKill(t *testing.T, dir, body string, perm os.FileMode) string {
	p := filepath.Join(dir, "tools-killswitch.yaml")
	if err := os.WriteFile(p, []byte(body), perm); err != nil {
		t.Fatal(err)
	}
	os.Chmod(p, perm)
	return p
}

func TestKillswitch_MissingAtStartupDisablesNothing(t *testing.T) {
	k := NewKillswitch(filepath.Join(t.TempDir(), "none.yaml"), os.Getuid())
	if r := k.Reload(); r != "missing" || k.Disabled("web_search") {
		t.Fatal(r, k)
	}
}

func TestKillswitch_PermOrParseErrorAllOff(t *testing.T) {
	for name, c := range map[string]struct {
		body string
		perm os.FileMode
	}{"perm": {"disabled: [web_fetch]\n", 0o644}, "parse": {"disabled: [\n", 0o600}, "bad_name": {"disabled: [\"A B\"]\n", 0o600}} {
		k := NewKillswitch(writeKill(t, t.TempDir(), c.body, c.perm), os.Getuid())
		k.Reload()
		if !k.Disabled("web_search") || !k.Disabled("web_fetch") {
			t.Errorf("%s: not all off: %v", name, k)
		}
	}
}

func TestKillswitch_UnknownKeyAllOff(t *testing.T) {
	k := NewKillswitch(writeKill(t, t.TempDir(), "enable: [web_search]\n", 0o600), os.Getuid())
	if r := k.Reload(); r != "parse_error" || !k.Disabled("web_search") {
		t.Fatal(r)
	}
}

func TestKillswitch_DisablesListedOnly(t *testing.T) {
	k := NewKillswitch(writeKill(t, t.TempDir(), "disabled: [web_fetch]\n", 0o600), os.Getuid())
	if r := k.Reload(); r != "ok" || !k.Disabled("web_fetch") || k.Disabled("web_search") {
		t.Fatal(r, k)
	}
}

func TestKillswitch_FileRemovedKeepsOff(t *testing.T) {
	p := writeKill(t, t.TempDir(), "all_off: true\n", 0o600)
	k := NewKillswitch(p, os.Getuid())
	k.Reload()
	os.Remove(p)
	if r := k.Reload(); r != "missing" || !k.Disabled("web_search") {
		t.Fatal("deleting the file re-enabled tools", r)
	}
}

func TestKillswitch_RejectsSymlinkWrongOwner(t *testing.T) {
	dir := t.TempDir()
	real := writeKill(t, dir, "disabled: []\n", 0o600)
	link := filepath.Join(dir, "link.yaml")
	os.Symlink(real, link)
	k := NewKillswitch(link, os.Getuid())
	if k.Reload(); !k.Disabled("web_search") {
		t.Fatal("symlink accepted")
	}
	k2 := NewKillswitch(real, os.Getuid()+12345)
	if r := k2.Reload(); r != "bad_owner" || !k2.Disabled("web_search") {
		t.Fatal("wrong owner accepted", r)
	}
}

func TestKillswitch_ReloadAudited(t *testing.T) {
	var got []string
	k := NewKillswitch(writeKill(t, t.TempDir(), "disabled: [web_fetch]\n", 0o600), os.Getuid())
	k.OnLoad = func(res string, d []string, all bool) { got = append(got, res) }
	k.Reload()
	if len(got) != 1 || got[0] != "ok" {
		t.Fatal(got)
	}
}

func TestKillswitch_ExecAndExposure(t *testing.T) {
	p := Default()
	old := p.Kill
	defer func() { p.Kill = old }()
	k := NewKillswitch(writeKill(t, t.TempDir(), "disabled: [web_fetch]\n", 0o600), os.Getuid())
	k.Reload()
	p.Kill = k
	if ex := p.Exposed(); len(ex) != 1 || ex[0] != "web_search" {
		t.Fatal(ex)
	}
	if len(ToolDefs()) != 1 {
		t.Fatal("disabled tool still offered to model")
	}
	calls, _, fails := ParseTextToolCalls(`<tool_call>{"name":"web_fetch","arguments":{"url":"https://a.example/"}}</tool_call>`, 5)
	if len(calls) != 0 || fails != 1 {
		t.Fatal("disabled tool parsed", calls, fails)
	}
	s := NewSession(Deps{Rate: NewRateLimiter(map[string]int{"search": 9, "fetch": 9})}, "u", []string{"https://a.example/"})
	_, c := s.Exec(context.Background(), 1, "c1", "web_fetch", `{"url":"https://a.example/"}`)
	if c.Status != "blocked" || c.Error != "tool disabled" {
		t.Fatal(c)
	}
}

func TestRedactor(t *testing.T) {
	var nilR *Redactor
	if nilR.Redact("abc") != "abc" {
		t.Fatal()
	}
	r := &Redactor{}
	r.Add("short")
	r.Add("sk-SECRET-123456")
	if got := r.Redact("x sk-SECRET-123456 y short"); got != "x [REDACTED] y short" {
		t.Fatal(got)
	}
}
