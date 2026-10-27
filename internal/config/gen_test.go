package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerationHubBytesValidated(t *testing.T) {
	if err := DefaultGen().Validate(); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(p, []byte("generation:\n  hub_max_runs: 10\n  run_max_bytes: 1000\n  hub_max_bytes: 9999\n"), 0o600)
	_, err := Load(p, func(string) string { return "" })
	if err == nil || !strings.Contains(err.Error(), "hub_max_bytes") {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte("generation:\n  hub_max_runs: 10\n  run_max_bytes: 1000\n  hub_max_bytes: 10000\n"), 0o600)
	if _, err := Load(p, func(string) string { return "" }); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyIdentityConfigRefused(t *testing.T) {
	for _, env := range []map[string]string{
		{"GW_TCP_DISABLED": "true"},
		{"GW_UNIX_SOCKET": "/tmp/x/gw.sock"},
		{"GW_AUTH_PROVIDERS": "tailscale-identity,api-key"},
	} {
		if _, err := Load("/nonexistent.yaml", func(k string) string { return env[k] }); err == nil {
			t.Fatalf("%v accepted", env)
		}
	}
	env := map[string]string{"GW_ALLOWED_HOSTS": "gw.example.test, localhost", "GW_SESSION_PEPPER_FILE": "/x/pepper"}
	c, err := Load("/nonexistent.yaml", func(k string) string { return env[k] })
	if err != nil || len(c.AllowedHosts) != 2 || c.SessionPepperFile != "/x/pepper" || c.Auth.Providers[0] != "session" {
		t.Fatal(c.AllowedHosts, c.SessionPepperFile, c.Auth.Providers, err)
	}
}
