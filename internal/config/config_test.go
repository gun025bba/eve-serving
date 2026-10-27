package config

import (
	"os"
	"path/filepath"
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

func TestPimWritesConfig(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	base := map[string]string{"GW_PIM_ENABLED": "true", "GW_PIM_SOCKET_PATH": "/tmp/x/pim.sock", "GW_PIM_OWNER_USER_ID": "7"}
	c, err := Load("/nonexistent.yaml", env(base))
	if err != nil || c.Pim.WritesEnabled || c.Pim.WritesOn() {
		t.Fatal("pim writes must default off", err)
	}
	on := map[string]string{"GW_PIM_WRITES_ENABLED": "true"}
	for k, v := range base {
		on[k] = v
	}
	if c, err = Load("/nonexistent.yaml", env(on)); err != nil || !c.Pim.WritesOn() {
		t.Fatal(err, c.Pim)
	}
	// the writes flag never outlives the pim switch
	if c, err = Load("/nonexistent.yaml", env(map[string]string{"GW_PIM_WRITES_ENABLED": "true"})); err != nil || c.Pim.WritesOn() {
		t.Fatal("writes on without pim.enabled", err)
	}
	if _, err = Load("/nonexistent.yaml", env(map[string]string{"GW_PIM_WRITES_ENABLED": "maybe"})); err == nil {
		t.Fatal("bad bool accepted")
	}
}
