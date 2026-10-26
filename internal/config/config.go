// Package config loads config.yaml and applies GW_<KEY> environment overrides.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config mirrors config.yaml. Field names match the YAML keys.
type Config struct {
	Upstream        string         `yaml:"upstream" json:"upstream"`
	Model           string         `yaml:"model" json:"model"`
	Defaults        map[string]any `yaml:"defaults" json:"defaults"`
	MaxConc         int            `yaml:"max_conc" json:"max_conc"`
	MaxQueue        int            `yaml:"max_queue" json:"max_queue"`
	QueueTimeoutS   float64        `yaml:"queue_timeout_s" json:"queue_timeout_s"`
	ConnectTimeoutS float64        `yaml:"connect_timeout_s" json:"connect_timeout_s"`
	ReadTimeoutS    float64        `yaml:"read_timeout_s" json:"read_timeout_s"`
	Retries         int            `yaml:"retries" json:"retries"`
	RetryBackoffS   float64        `yaml:"retry_backoff_s" json:"retry_backoff_s"`
	LogLevel        string         `yaml:"log_level" json:"log_level"`
	LogFile         string         `yaml:"log_file" json:"log_file"`
	StatsWindow     int            `yaml:"stats_window" json:"stats_window"`
	MinFreeMemGB    float64        `yaml:"min_free_mem_gb" json:"min_free_mem_gb"`
	CORSOrigins     []string       `yaml:"cors_origins" json:"cors_origins"`
	StaticDir       string         `yaml:"static_dir" json:"static_dir"`
	// AllowedUsers are Tailscale logins accepted via the Tailscale-User-Login
	// header (loopback only). Empty = identity auth disabled.
	AllowedUsers []string   `yaml:"allowed_users" json:"allowed_users"`
	Auth         AuthConfig `yaml:"auth" json:"auth"`
}

// AuthConfig selects authentication providers, tried in order.
type AuthConfig struct {
	Providers []string `yaml:"providers" json:"providers"`
}

// Default returns built-in defaults (same values as the shipped config.yaml).
func Default() Config {
	return Config{
		Upstream: "http://127.0.0.1:8080", Model: "mlx-community/Qwen3-30B-A3B-4bit",
		Defaults: map[string]any{"max_tokens": 1024, "temperature": 0.7, "top_p": 0.9},
		MaxConc:  2, MaxQueue: 16, QueueTimeoutS: 120, ConnectTimeoutS: 5, ReadTimeoutS: 300,
		Retries: 2, RetryBackoffS: 0.5, LogLevel: "INFO", LogFile: "~/llm-serving/gateway.log",
		StatsWindow: 200, MinFreeMemGB: 4, CORSOrigins: []string{}, StaticDir: "~/llm-serving/web", AllowedUsers: []string{},
		Auth: AuthConfig{Providers: []string{"tailscale-identity", "api-key"}},
	}
}

// Load reads path (if it exists) over the defaults, then applies env overrides via getenv.
func Load(path string, getenv func(string) string) (Config, error) {
	c := Default()
	if b, err := os.ReadFile(path); err == nil {
		if err := yaml.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return c, err
	}
	if err := applyEnv(&c, getenv); err != nil {
		return c, err
	}
	if c.MaxConc < 1 {
		return c, fmt.Errorf("max_conc must be >= 1")
	}
	if c.CORSOrigins == nil {
		c.CORSOrigins = []string{}
	}
	if c.AllowedUsers == nil {
		c.AllowedUsers = []string{}
	}
	return c, nil
}

func applyEnv(c *Config, getenv func(string) string) error {
	str := map[string]*string{"UPSTREAM": &c.Upstream, "MODEL": &c.Model, "LOG_LEVEL": &c.LogLevel, "LOG_FILE": &c.LogFile, "STATIC_DIR": &c.StaticDir}
	for k, p := range str {
		if v := getenv("GW_" + k); v != "" {
			*p = v
		}
	}
	// Integers accept decimals ("3.0") like the Python gateway did.
	ints := map[string]*int{"MAX_CONC": &c.MaxConc, "MAX_QUEUE": &c.MaxQueue, "RETRIES": &c.Retries, "STATS_WINDOW": &c.StatsWindow}
	for k, p := range ints {
		if v := getenv("GW_" + k); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("GW_%s: %w", k, err)
			}
			*p = int(f)
		}
	}
	floats := map[string]*float64{"QUEUE_TIMEOUT_S": &c.QueueTimeoutS, "CONNECT_TIMEOUT_S": &c.ConnectTimeoutS, "READ_TIMEOUT_S": &c.ReadTimeoutS, "RETRY_BACKOFF_S": &c.RetryBackoffS, "MIN_FREE_MEM_GB": &c.MinFreeMemGB}
	for k, p := range floats {
		if v := getenv("GW_" + k); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("GW_%s: %w", k, err)
			}
			*p = f
		}
	}
	if v := getenv("GW_CORS_ORIGINS"); v != "" {
		c.CORSOrigins = splitList(v)
	}
	if v := getenv("GW_ALLOWED_USERS"); v != "" {
		c.AllowedUsers = splitList(v)
	}
	if v := getenv("GW_AUTH_PROVIDERS"); v != "" {
		c.Auth.Providers = splitList(v)
	}
	return nil
}

// Expand resolves a leading "~/" to the user's home directory.
func Expand(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

func splitList(v string) []string {
	out := []string{}
	for _, o := range strings.Split(v, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}
