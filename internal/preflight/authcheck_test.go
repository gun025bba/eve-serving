package preflight

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func plist(env string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.llmserving.gateway</string>
<key>ProgramArguments</key><array><string>/opt/eve-gw/bin/gateway</string></array>
<key>Sockets</key><dict><key>GW_ALLOWED_USERS</key><string>nested-not-env</string></dict>
` + env + `
<key>RunAtLoad</key><true/>
</dict></plist>`
}

func envDict(kv ...string) string {
	var b strings.Builder
	b.WriteString("<key>EnvironmentVariables</key><dict>")
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString("<key>" + kv[i] + "</key><string>" + kv[i+1] + "</string>")
	}
	b.WriteString("</dict>")
	return b.String()
}

func writeCfg(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParsePlistEnv(t *testing.T) {
	env, err := ParsePlistEnv(strings.NewReader(plist(envDict("GW_ALLOWED_USERS", "a@x", "GW_DB_AUTO_MIGRATE", "false"))))
	if err != nil || env["GW_ALLOWED_USERS"] != "a@x" || len(env) != 2 {
		t.Fatal(env, err)
	}
	env, err = ParsePlistEnv(strings.NewReader(plist("")))
	if err != nil || len(env) != 0 {
		t.Fatal("no env dict:", env, err)
	}
	bad := map[string]string{
		"duplicate env key":  plist(envDict("GW_ALLOWED_USERS", "a@x", "GW_ALLOWED_USERS", "")),
		"duplicate env dict": plist(envDict("A", "1") + envDict("B", "2")),
		"non-string value":   plist("<key>EnvironmentVariables</key><dict><key>GW_ALLOWED_USERS</key><true/></dict>"),
		"key without value":  plist("<key>EnvironmentVariables</key><dict><key>GW_ALLOWED_USERS</key></dict>"),
		"malformed":          "<plist><dict><key>EnvironmentVariables</key><dict>",
		"binary":             "bplist00garbage",
	}
	for name, body := range bad {
		if _, err := ParsePlistEnv(strings.NewReader(body)); err == nil {
			t.Errorf("%s: want error", name)
		} else if strings.Contains(err.Error(), "a@x") {
			t.Errorf("%s: error leaks value: %v", name, err)
		}
	}
}

func TestCheckAuth(t *testing.T) {
	repoCfg, _ := filepath.Abs("../../config.yaml")
	block := writeCfg(t, "allowed_users:\n  - a@x\n  - b@y\nauth:\n  providers:\n    - tailscale-identity\n    - api-key\n")
	blockAPI := writeCfg(t, "allowed_users: []\nauth:\n  providers:\n    - api-key\n")
	blockEmptyUsers := writeCfg(t, "allowed_users:\n  - \" \"\nauth:\n  providers:\n    - tailscale-identity\n")
	dupYAML := writeCfg(t, "allowed_users: [a@x]\nallowed_users: [b@y]\n")
	dupNested := writeCfg(t, "auth:\n  providers: [api-key]\n  providers: [tailscale-identity]\n")
	cases := []struct {
		name   string
		cfg    string
		env    map[string]string
		ok     bool
		logins int
	}{
		{"repo config, no env", repoCfg, nil, false, 0},
		{"repo config, empty env value -> config decides (empty)", repoCfg, map[string]string{"GW_ALLOWED_USERS": ""}, false, 0},
		{"Med B: env ' , ' -> 0 logins", repoCfg, map[string]string{"GW_ALLOWED_USERS": " , "}, false, 0},
		{"env set", repoCfg, map[string]string{"GW_ALLOWED_USERS": "a@x, b@y"}, true, 2},
		{"env value alone decides over config", block, map[string]string{"GW_ALLOWED_USERS": " , "}, false, 0},
		{"Med A: block-style lists", block, nil, true, 2},
		{"block-style api-key only", blockAPI, nil, true, 0},
		{"block-style blank login", blockEmptyUsers, nil, false, 0},
		{"duplicate YAML key", dupYAML, map[string]string{"GW_ALLOWED_USERS": "a@x"}, false, 0},
		{"duplicate nested YAML key", dupNested, nil, false, 0},
	}
	for _, tc := range cases {
		r, err := CheckAuth(tc.cfg, tc.env)
		if (err == nil) != tc.ok {
			t.Errorf("%s: ok=%v want %v (%v)", tc.name, err == nil, tc.ok, err)
			continue
		}
		if err != nil {
			if !errors.Is(err, ErrAbort) {
				t.Errorf("%s: not ErrAbort: %v", tc.name, err)
			}
			for _, v := range []string{"a@x", "b@y"} {
				if strings.Contains(err.Error(), v) {
					t.Errorf("%s: error leaks login: %v", tc.name, err)
				}
			}
			continue
		}
		if r.AllowedLogins != tc.logins {
			t.Errorf("%s: logins %d want %d", tc.name, r.AllowedLogins, tc.logins)
		}
	}
	// The caller's own environment must not leak in.
	t.Setenv("GW_ALLOWED_USERS", "a@x")
	if _, err := CheckAuth(repoCfg, nil); err == nil {
		t.Fatal("process env must be ignored")
	}
}
