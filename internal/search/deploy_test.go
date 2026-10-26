package search

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSearxngDeployHardening checks deploy/searxng (review §7.2 items 1-4).
func TestSearxngDeployHardening(t *testing.T) {
	raw, err := os.ReadFile("../../deploy/searxng/docker-compose.yml")
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Ports       []string          `yaml:"ports"`
			ReadOnly    bool              `yaml:"read_only"`
			CapDrop     []string          `yaml:"cap_drop"`
			SecurityOpt []string          `yaml:"security_opt"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Services) != 2 {
		t.Fatal(c.Services)
	}
	for name, s := range c.Services {
		if !strings.Contains(s.Image, "@sha256:") || !s.ReadOnly || len(s.CapDrop) != 1 || s.CapDrop[0] != "ALL" || len(s.SecurityOpt) == 0 || s.SecurityOpt[0] != "no-new-privileges:true" {
			t.Fatalf("%s not hardened: %+v", name, s)
		}
		for _, p := range s.Ports {
			if !strings.HasPrefix(p, "127.0.0.1:") {
				t.Fatalf("%s publishes beyond loopback: %s", name, p)
			}
		}
	}
	if len(c.Services["valkey"].Ports) != 0 {
		t.Fatal("valkey must not be published")
	}
	if sec := c.Services["searxng"].Environment["SEARXNG_SECRET"]; !strings.HasPrefix(sec, "${SEARXNG_SECRET") {
		t.Fatal("secret must come from env")
	}
	set, _ := os.ReadFile("../../deploy/searxng/settings.yml")
	var st struct {
		Search struct{ Formats []string } `yaml:"search"`
		Server map[string]any             `yaml:"server"`
	}
	if err := yaml.Unmarshal(set, &st); err != nil {
		t.Fatal(err)
	}
	if st.Server["limiter"] != true || st.Server["secret_key"] != nil || len(st.Search.Formats) != 1 || st.Search.Formats[0] != "json" {
		t.Fatalf("%+v", st)
	}
	lim, _ := os.ReadFile("../../deploy/searxng/limiter.toml")
	if !strings.Contains(string(lim), `pass_ip = ["127.0.0.1", "::1"]`) {
		t.Fatal("pass_ip must be loopback only")
	}
}
