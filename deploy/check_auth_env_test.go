package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func plist(env string) string {
	return `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>x</string>` +
		env + `</dict></plist>`
}

// TestCheckAuthEnv runs deploy/check-auth-env.sh against a freshly built
// gateway, so the guard is exercised end to end with the real loader.
func TestCheckAuthEnv(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	d := t.TempDir()
	gw := filepath.Join(d, "gateway")
	if out, err := exec.Command("go", "build", "-o", gw, "../cmd/gateway").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repoCfg, _ := filepath.Abs("../config.yaml")
	blockCfg := writeFile(t, d, "block.yaml", "allowed_users:\n  - a@x\nauth:\n  providers:\n    - tailscale-identity\n    - api-key\n")
	apiCfg := writeFile(t, d, "api.yaml", "auth:\n  providers:\n    - api-key\n")
	env := func(kv string) string { return "<key>EnvironmentVariables</key><dict>" + kv + "</dict>" }
	cases := []struct {
		name, plist, cfg string
		ok               bool
	}{
		{"empty users, default providers", plist(env("<key>GW_ALLOWED_USERS</key><string></string>")), repoCfg, false},
		{"Med B: ' , '", plist(env("<key>GW_ALLOWED_USERS</key><string> , </string>")), repoCfg, false},
		{"no env", plist(""), repoCfg, false},
		{"users set", plist(env("<key>GW_ALLOWED_USERS</key><string>a@x</string>")), repoCfg, true},
		{"env ' , ' overrides config list", plist(env("<key>GW_ALLOWED_USERS</key><string> , </string>")), blockCfg, false},
		{"Med A: block-style config", plist(""), blockCfg, true},
		{"api-key only via config", plist(""), apiCfg, true},
		{"duplicate plist env key", plist(env("<key>GW_ALLOWED_USERS</key><string>a@x</string><key>GW_ALLOWED_USERS</key><string>a@x</string>")), repoCfg, false},
		{"duplicate YAML key", plist(env("<key>GW_ALLOWED_USERS</key><string>a@x</string>")), writeFile(t, d, "dup.yaml", "allowed_users: [a@x]\nallowed_users: [a@x]\n"), false},
	}
	for i, tc := range cases {
		p := writeFile(t, d, "p"+string(rune('a'+i))+".plist", tc.plist)
		cmd := exec.Command("bash", "-x", "check-auth-env.sh", gw, p, tc.cfg) // -x: prove nothing sensitive is traced
		cmd.Env = append(os.Environ(), "GW_ALLOWED_USERS=a@x")                // caller env must be ignored
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.ok {
			t.Errorf("%s: ok=%v want %v: %s", tc.name, err == nil, tc.ok, out)
		}
		// the plist path appears in the trace; login values must not
		trace := strings.ReplaceAll(string(out), p, "")
		if strings.Contains(trace, "a@x") {
			t.Errorf("%s: output leaks a login value: %s", tc.name, out)
		}
	}
	for _, args := range [][]string{
		{gw, filepath.Join(d, "missing.plist"), repoCfg},
		{gw, writeFile(t, d, "ok.plist", plist("")), filepath.Join(d, "missing.yaml")},
		{filepath.Join(d, "nogw"), writeFile(t, d, "ok2.plist", plist("")), repoCfg},
	} {
		if err := exec.Command("bash", append([]string{"check-auth-env.sh"}, args...)...).Run(); err == nil {
			t.Errorf("%v: must fail", args)
		}
	}
}
