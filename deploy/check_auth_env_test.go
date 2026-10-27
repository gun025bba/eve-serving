package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writePlist writes a minimal launchd plist with the given env (nil = no key).
func writePlist(t *testing.T, dir, name string, env map[string]string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>x</string><key>EnvironmentVariables</key><dict>`)
	for k, v := range env {
		b.WriteString("<key>" + k + "</key><string>" + v + "</string>")
	}
	b.WriteString(`</dict></dict></plist>`)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckAuthEnv(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if _, err := exec.LookPath("plutil"); err != nil {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("neither plutil nor python3 available")
		}
	}
	d := t.TempDir()
	cfg := func(name, body string) string {
		p := filepath.Join(d, name)
		os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	repoCfg, _ := filepath.Abs("../config.yaml")
	cases := []struct {
		name   string
		env    map[string]string
		config string
		ok     bool
	}{
		{"empty users, default providers", map[string]string{"GW_ALLOWED_USERS": ""}, "", false},
		{"blank users", map[string]string{"GW_ALLOWED_USERS": " , "}, "", false},
		{"no env at all", nil, "", false},
		{"users set", map[string]string{"GW_ALLOWED_USERS": "a@x"}, "", true},
		{"api-key only", map[string]string{"GW_AUTH_PROVIDERS": "api-key"}, "", true},
		{"repo config.yaml, empty users", nil, repoCfg, false},
		{"config allowlist", nil, cfg("c2.yaml", "allowed_users: [a@x]\n"), true},
		{"config api-key only", nil, cfg("c3.yaml", "allowed_users: []\nauth:\n  providers: [api-key]\n"), true},
	}
	for i, tc := range cases {
		p := writePlist(t, d, tc.name[:3]+string(rune('a'+i))+".plist", tc.env)
		args := []string{"check-auth-env.sh", p}
		if tc.config != "" {
			args = append(args, tc.config)
		}
		out, err := exec.Command("bash", args...).CombinedOutput()
		if (err == nil) != tc.ok {
			t.Errorf("%s: ok=%v want %v: %s", tc.name, err == nil, tc.ok, out)
		}
		if strings.Contains(string(out), "a@x") {
			t.Errorf("%s: printed a login value: %s", tc.name, out)
		}
	}
	if err := exec.Command("bash", "check-auth-env.sh", filepath.Join(d, "missing.plist")).Run(); err == nil {
		t.Error("missing plist must fail")
	}
}
