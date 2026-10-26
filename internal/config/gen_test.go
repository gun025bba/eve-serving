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
