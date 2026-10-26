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
