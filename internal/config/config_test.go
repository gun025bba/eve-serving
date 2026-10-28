package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndEnv(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "config.yaml")
	os.WriteFile(p, []byte("max_conc: 3\ndefaults:\n  max_tokens: 10\ncors_origins: [\"http://a\"]\n"), 0o644)
	env := map[string]string{"GW_MAX_QUEUE": "5.0", "GW_QUEUE_TIMEOUT_S": "1.5", "GW_CORS_ORIGINS": "http://x, http://y", "GW_LOG_LEVEL": "DEBUG"}
	c, err := Load(p, func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxConc != 3 || c.MaxQueue != 5 || c.QueueTimeoutS != 1.5 || c.LogLevel != "DEBUG" {
		t.Fatalf("bad cfg %+v", c)
	}
	if len(c.CORSOrigins) != 2 || c.CORSOrigins[1] != "http://y" {
		t.Fatalf("cors %v", c.CORSOrigins)
	}
	if c.Defaults["max_tokens"] != 10 {
		t.Fatalf("defaults %v", c.Defaults)
	}
	if c.ReadTimeoutS != 300 {
		t.Fatal("default not kept")
	}
}

func TestBadEnv(t *testing.T) {
	_, err := Load("/nonexistent", func(k string) string {
		if k == "GW_MAX_CONC" {
			return "abc"
		}
		return ""
	})
	if err == nil {
		t.Fatal("want error")
	}
}

func TestAllowedUsersEnv(t *testing.T) {
	env := map[string]string{"GW_ALLOWED_USERS": " a@github , ,B@example.com"}
	c, err := Load("/nonexistent.yaml", func(k string) string { return env[k] })
	if err != nil || len(c.AllowedUsers) != 2 || c.AllowedUsers[1] != "B@example.com" {
		t.Fatal(c.AllowedUsers, err)
	}
	c, _ = Load("/nonexistent.yaml", func(string) string { return "" })
	if c.AllowedUsers == nil || len(c.AllowedUsers) != 0 {
		t.Fatal(c.AllowedUsers)
	}
}

func TestAuthProviders(t *testing.T) {
	c, _ := Load("/nonexistent.yaml", func(string) string { return "" })
	if len(c.Auth.Providers) != 2 || c.Auth.Providers[0] != "session" {
		t.Fatal(c.Auth.Providers)
	}
	c, _ = Load("/nonexistent.yaml", func(k string) string {
		if k == "GW_AUTH_PROVIDERS" {
			return "api-key"
		}
		return ""
	})
	if len(c.Auth.Providers) != 1 || c.Auth.Providers[0] != "api-key" {
		t.Fatal(c.Auth.Providers)
	}
}

func TestWebDefaultsOffAndEnv(t *testing.T) {
	c, err := Load("/nonexistent.yaml", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if c.Web.Enabled || c.Web.DefaultMode != "off" || c.Web.SearxngURL != "http://127.0.0.1:8888" || c.Web.RetentionDays != 90 || c.Web.FetchGlobalConc != 4 || c.Web.FetchPerHostConc != 2 {
		t.Fatalf("%+v", c.Web)
	}
	env := map[string]string{"GW_WEB_ENABLED": "true", "GW_WEB_DEFAULT_MODE": "auto", "GW_SEARXNG_URL": "http://127.0.0.1:9999", "GW_FETCH_ALLOWED_PORTS": "80,443,8443", "GW_SEARCH_TIMEOUT": "4s"}
	c, err = Load("/nonexistent.yaml", func(k string) string { return env[k] })
	if err != nil || !c.Web.Enabled || c.Web.DefaultMode != "auto" || c.Web.SearxngURL != "http://127.0.0.1:9999" || len(c.Web.FetchAllowedPort) != 3 || c.Web.SearchTimeoutS != 4 {
		t.Fatalf("%v %+v", err, c.Web)
	}
	if _, err := Load("/nonexistent.yaml", func(k string) string { return map[string]string{"GW_WEB_DEFAULT_MODE": "always"}[k] }); err == nil {
		t.Fatal("bad mode accepted")
	}
}

func TestLoad_PushRequiresKillswitch(t *testing.T) {
	env := map[string]string{"GW_PUSH_ENABLED": "true"}
	get := func(k string) string { return env[k] }
	if _, err := Load("/nonexistent.yaml", get); err == nil {
		t.Fatal("push without kill switch must refuse startup")
	}
	env["GW_TOOLS_KILLSWITCH_FILE"] = "/tmp/ks.yaml"
	if _, err := Load("/nonexistent.yaml", get); err != nil {
		t.Fatal(err)
	}
}

func TestPimConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	c, err := Load("/nonexistent.yaml", env(nil))
	if err != nil || c.Pim.Enabled {
		t.Fatal("pim must default off", err)
	}
	if _, err := Load("/nonexistent.yaml", env(map[string]string{"GW_PIM_ENABLED": "true"})); err == nil {
		t.Fatal("enabled without socket/owner accepted")
	}
	if _, err := Load("/nonexistent.yaml", env(map[string]string{"GW_PIM_ENABLED": "true", "GW_PIM_SOCKET_PATH": "rel/pim.sock", "GW_PIM_OWNER_USER_ID": "1"})); err == nil {
		t.Fatal("relative socket accepted")
	}
	c, err = Load("/nonexistent.yaml", env(map[string]string{"GW_PIM_ENABLED": "true", "GW_PIM_SOCKET_PATH": "/tmp/x/pim.sock", "GW_PIM_OWNER_USER_ID": "7"}))
	if err != nil || c.Pim.OwnerUserID != 7 {
		t.Fatal(err, c.Pim)
	}
}

func TestMemoryEnabledOffWins(t *testing.T) {
	cases := []struct {
		name, yaml, env string
		want            bool
		src             string
	}{
		{"env true + config false", "memory:\n  enabled: false\n", "true", false, "config"},
		{"env false + config true", "memory:\n  enabled: true\n", "false", false, "env"},
		{"both unset", "max_conc: 1\n", "", true, "default"},
		{"env unset + config true", "memory:\n  enabled: true\n", "", true, "config"},
		{"env true + config unset", "max_conc: 1\n", "true", true, "env"},
		{"both false", "memory:\n  enabled: false\n", "false", false, "env"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.yaml")
			os.WriteFile(p, []byte(tc.yaml), 0o644)
			env := map[string]string{"GW_MEMORY_ENABLED": tc.env}
			c, err := Load(p, func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if c.Memory.Enabled != tc.want || c.Memory.EnabledSource != tc.src {
				t.Fatalf("got %v/%s want %v/%s", c.Memory.Enabled, c.Memory.EnabledSource, tc.want, tc.src)
			}
		})
	}
}

func TestMemoryResolvedLogLine(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	c := Default()
	c.Memory.Enabled, c.Memory.EnabledSource = ResolveMemoryEnabled(nil, boolp(false))
	msg, args := MemoryResolvedLog(c)
	l.Info(msg, args...)
	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, `msg="memory config resolved" memory_enabled=false memory_enabled_source=env`) {
		t.Fatalf("log line: %q", out)
	}
}

func boolp(b bool) *bool { return &b }

func writeCfg(t *testing.T, body string) string {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func noEnv(string) (string, bool) { return "", false }

func TestStrictYAML(t *testing.T) {
	cases := map[string]string{
		"parse failure":  "memory: [unclosed\n",
		"typo key":       "memory:\n  enable: false\n",
		"top-level typo": "memroy:\n  enabled: false\n",
		"null enabled":   "memory:\n  enabled: null\n",
		"empty enabled":  "memory:\n  enabled:\n",
		"tilde enabled":  "memory:\n  enabled: ~\n",
		"on":             "memory:\n  enabled: on\n",
		"yes":            "memory:\n  enabled: yes\n",
		"Yes":            "memory:\n  enabled: Yes\n",
		"off":            "memory:\n  enabled: off\n",
		"TRUE":           "memory:\n  enabled: TRUE\n",
		"False":          "memory:\n  enabled: False\n",
		"one":            "memory:\n  enabled: 1\n",
		"double quoted":  "memory:\n  enabled: \"false\"\n",
		"single quoted":  "memory:\n  enabled: 'true'\n",
		"str tag":        "memory:\n  enabled: !!str true\n",
		"str tag false":  "memory:\n  enabled: !!str false\n",
		"bool tag yes":   "memory:\n  enabled: !!bool yes\n",
		"bool tag TRUE":  "memory:\n  enabled: !!bool TRUE\n",
		"list":           "memory:\n  enabled: [false]\n",
		"flow map list":  "memory: {enabled: [false]}\n",
	}
	for name, body := range cases {
		if _, err := LoadEnv(writeCfg(t, body), noEnv); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	c, err := LoadEnv(writeCfg(t, ""), noEnv)
	if err != nil || !c.Memory.Enabled || c.Memory.EnabledSource != "default" {
		t.Fatalf("empty file: %v %+v", err, c.Memory)
	}
	c, err = LoadEnv(writeCfg(t, "memory:\n  enabled: !!bool false\n"), noEnv)
	if err != nil || c.Memory.Enabled {
		t.Fatalf("!!bool false: %v %+v", err, c.Memory)
	}
	c, err = LoadEnv(writeCfg(t, "memory: {enabled: false}\n"), noEnv)
	if err != nil || c.Memory.Enabled {
		t.Fatalf("flow style false: %v %+v", err, c.Memory)
	}
	c, err = LoadEnv(writeCfg(t, "web:\n  enabled: false\nmemory:\n  enabled: true\n"), noEnv)
	if err != nil || !c.Memory.Enabled {
		t.Fatalf("web.enabled must not affect memory: %v %+v", err, c.Memory)
	}
	c, err = LoadEnv(writeCfg(t, "memory:\n  enabled: false\n"), noEnv)
	if err != nil || c.Memory.Enabled {
		t.Fatalf("explicit false: %v %+v", err, c.Memory)
	}
}

func TestRepoConfigYAMLStrict(t *testing.T) {
	if _, err := LoadEnv(filepath.Join("..", "..", "config.yaml"), noEnv); err != nil {
		t.Fatalf("repo config.yaml must load with KnownFields: %v", err)
	}
}

func TestMemoryEnvEmptyOrInvalid(t *testing.T) {
	for _, v := range []string{"", "  ", "yes-please", "on", "yes", "1", "0", "TRUE", "False", "t"} {
		env := func(k string) (string, bool) {
			if k == "GW_MEMORY_ENABLED" {
				return v, true
			}
			return "", false
		}
		if _, err := LoadEnv("", env); err == nil {
			t.Errorf("GW_MEMORY_ENABLED=%q: want startup error", v)
		}
	}
	env := func(k string) (string, bool) {
		if k == "GW_MEMORY_ENABLED" {
			return " false ", true
		}
		return "", false
	}
	if c, err := LoadEnv("", env); err != nil || c.Memory.Enabled || c.Memory.EnabledSource != "env" {
		t.Fatalf("trimmed false: %v %+v", err, c.Memory)
	}
}

func TestMemoryResolvedLogNoSecrets(t *testing.T) {
	c := Default()
	c.Database.URL = "postgres://u:hunter2pw@db/x"
	c.Memory.AuditKeyFile = "/tmp/audit.key"
	c.Memory.Enabled, c.Memory.EnabledSource = ResolveMemoryEnabled(boolp(true), nil)
	var buf bytes.Buffer
	msg, args := MemoryResolvedLog(c)
	slog.New(slog.NewTextHandler(&buf, nil)).Info(msg, args...)
	out := buf.String()
	for _, bad := range []string{"SECRETKEY", "hunter2pw", "postgres://", "audit.key", "key=", "password", "token"} {
		if strings.Contains(out, bad) {
			t.Fatalf("log line leaks %q: %s", bad, out)
		}
	}
}
