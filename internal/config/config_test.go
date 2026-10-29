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

func TestWebDefaultsOnAndEnv(t *testing.T) {
	c, err := Load("/nonexistent.yaml", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if !c.Web.Enabled || c.Web.MaxSearches != 2 || c.Web.ArgsRetentionDays != 90 || c.Tools.V4Mode != V4Off || c.Web.DefaultMode != "auto" || c.Web.AutoSelectOff || c.Web.SearxngURL != "http://127.0.0.1:8888" || c.Web.RetentionDays != 90 || c.Web.FetchGlobalConc != 4 || c.Web.FetchPerHostConc != 2 {
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

// auto_select_off is the 6.0.0 rollback switch: it fails closed (an unreadable
// env value turns auto selection off) and off wins over the file.
func TestWebAutoSelectOff(t *testing.T) {
	cases := []struct {
		name, env, yaml string
		want            bool
	}{
		{"unset", "", "", false},
		{"env true", "true", "", true},
		{"env 1", "1", "", true},
		{"env false", "false", "", false},
		{"env garbage fails closed", "maybe", "", true},
		{"yaml true, env false: off wins", "false", "web:\n  auto_select_off: true\n", true},
		{"yaml true", "", "web:\n  auto_select_off: true\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := "/nonexistent.yaml"
			if c.yaml != "" {
				path = filepath.Join(t.TempDir(), "c.yaml")
				if err := os.WriteFile(path, []byte(c.yaml), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cfg, err := Load(path, func(k string) string {
				if k == "GW_WEB_AUTO_SELECT_OFF" {
					return c.env
				}
				return ""
			})
			if err != nil || cfg.Web.AutoSelectOff != c.want {
				t.Fatalf("%v %+v", err, cfg.Web)
			}
		})
	}
}

func TestWebBudgetEnv(t *testing.T) {
	env := map[string]string{"GW_WEB_USER_DAILY_SEARCH": "7", "GW_WEB_GLOBAL_DAILY_FETCH": "9"}
	c, err := Load("/nonexistent.yaml", func(k string) string { return env[k] })
	if err != nil || c.Web.UserDailySearch != 7 || c.Web.GlobalDailyFetch != 9 {
		t.Fatalf("%v %+v", err, c.Web)
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

func TestToolsConfig_PIMWindow(t *testing.T) {
	c, err := Load("", func(k string) string {
		return map[string]string{"GW_TOOLS_PIM_WINDOW_START": "2026-11-01T09:00:00+09:00", "GW_TOOLS_PIM_WINDOW_END": "2026-11-15T09:00:00+09:00"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	st, en := c.Tools.PIMWindow()
	if st.IsZero() || en.IsZero() || !en.After(st) {
		t.Fatalf("window %v %v", st, en)
	}
	c, err = Load("", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if st, en := c.Tools.PIMWindow(); !st.IsZero() || !en.IsZero() {
		t.Fatalf("unset must mean earliest / no end: %v %v", st, en)
	}
	if _, err = Load("", func(k string) string {
		if k == "GW_TOOLS_PIM_WINDOW_START" {
			return "yesterday"
		}
		return ""
	}); err == nil {
		t.Fatal("a window time that does not parse was accepted")
	}
}

// N-3: web.default_mode "off" is honoured as auto_select_off (an old deployment
// that had the web tools off keeps them off), "force" and unknown values are
// load errors, "auto" and absent change nothing. Same through the env.
func TestWebDefaultMode_OffHonouredForceRefused(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	noEnv := func(string) string { return "" }
	c, err := Load(write("web:\n  default_mode: off\n"), noEnv)
	if err != nil || !c.Web.AutoSelectOff || !c.Web.DefaultModeOff {
		t.Fatalf("default_mode off ignored: %v %+v", err, c.Web)
	}
	c, err = Load("/nonexistent.yaml", func(k string) string { return map[string]string{"GW_WEB_DEFAULT_MODE": "off"}[k] })
	if err != nil || !c.Web.AutoSelectOff {
		t.Fatalf("GW_WEB_DEFAULT_MODE=off ignored: %v %+v", err, c.Web)
	}
	for _, mode := range []string{"force", "always", "ON"} {
		if _, err := Load(write("web:\n  default_mode: "+mode+"\n"), noEnv); err == nil {
			t.Errorf("default_mode %q accepted", mode)
		}
	}
	for _, body := range []string{"web:\n  default_mode: auto\n", "web:\n  enabled: true\n"} {
		c, err := Load(write(body), noEnv)
		if err != nil || c.Web.AutoSelectOff || c.Web.DefaultModeOff {
			t.Errorf("%q: %v %+v", body, err, c.Web)
		}
	}
}

// N-2: web enabled and no kill switch file configured: automatic selection is
// switched off at start-up; with a file it stays as configured.
func TestRequireKillswitch(t *testing.T) {
	c := Default()
	if !c.RequireKillswitch() || !c.Web.AutoSelectOff {
		t.Fatal("web on without kill switch file must turn automatic selection off")
	}
	c = Default()
	c.ToolsKillswitchFile = "/etc/eve/tools-killswitch.yaml"
	if c.RequireKillswitch() || c.Web.AutoSelectOff {
		t.Fatal("a configured file must not change the setting")
	}
	c = Default()
	c.Web.Enabled = false
	if c.RequireKillswitch() {
		t.Fatal("web disabled: nothing to require")
	}
}
