package search

import (
	"net/netip"
	"os"
	"regexp"
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
	// Limiter pass_ip: exactly one /32 that is the gateway of the pinned "searxng" network.
	var nw struct {
		Networks map[string]struct {
			Ipam struct {
				Config []struct {
					Subnet  string `yaml:"subnet"`
					Gateway string `yaml:"gateway"`
				} `yaml:"config"`
			} `yaml:"ipam"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal(raw, &nw); err != nil {
		t.Fatal(err)
	}
	ipc := nw.Networks["searxng"].Ipam.Config
	if len(ipc) != 1 {
		t.Fatalf("searxng network must pin exactly one subnet: %+v", ipc)
	}
	subnet, err := netip.ParsePrefix(ipc[0].Subnet)
	if err != nil {
		t.Fatalf("searxng subnet: %v", err)
	}
	gw, err := netip.ParseAddr(ipc[0].Gateway)
	if err != nil || !subnet.Contains(gw) {
		t.Fatalf("searxng gateway %q not in pinned subnet %s", ipc[0].Gateway, subnet)
	}
	lim, err := os.ReadFile("../../deploy/searxng/limiter.toml")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^\s*pass_ip\s*=\s*\[([^\]]*)\]`).FindAllStringSubmatch(string(lim), -1)
	if len(m) != 1 {
		t.Fatalf("limiter.toml must have exactly one pass_ip line, got %d", len(m))
	}
	items := regexp.MustCompile(`"([^"]*)"`).FindAllStringSubmatch(m[0][1], -1)
	if len(items) != 1 {
		t.Fatalf("pass_ip must hold exactly one entry: %q", m[0][1])
	}
	pfx, err := netip.ParsePrefix(items[0][1])
	if err != nil {
		t.Fatalf("pass_ip entry must be CIDR (x.x.x.x/32): %v", err)
	}
	if !pfx.Addr().Is4() || pfx.Bits() != 32 {
		t.Fatalf("pass_ip %s broader than /32", pfx)
	}
	if pfx.Addr() != gw {
		t.Fatalf("pass_ip %s != searxng network gateway %s", pfx, gw)
	}
}
