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
// GenConfig bounds detached generation (ui-api-contract.md §3.8).
type GenConfig struct {
	UserMaxActive   int     `yaml:"user_max_active" json:"user_max_active"`     // concurrent generations per user (over -> 429)
	TimeoutS        float64 `yaml:"timeout_s" json:"timeout_s"`                 // one generation, all rounds
	IdleCancelS     float64 `yaml:"idle_cancel_s" json:"idle_cancel_s"`         // auto-cancel with no subscriber for this long
	RetainS         float64 `yaml:"retain_s" json:"retain_s"`                   // finished run replayable from memory
	RunMaxBytes     int     `yaml:"run_max_bytes" json:"run_max_bytes"`         // event log per run (oldest dropped)
	RunMaxEvents    int     `yaml:"run_max_events" json:"run_max_events"`       // event log per run (oldest dropped)
	PartialMaxBytes int     `yaml:"partial_max_bytes" json:"partial_max_bytes"` // content+reasoning; over -> failed too_large
	HubMaxRuns      int     `yaml:"hub_max_runs" json:"hub_max_runs"`           // runs in memory (finished evicted first)
	HubMaxBytes     int     `yaml:"hub_max_bytes" json:"hub_max_bytes"`         // event bytes in memory (finished evicted first)
	MaxSubscribers  int     `yaml:"max_subscribers" json:"max_subscribers"`     // per message (over -> 429)
	WriteTimeoutS   float64 `yaml:"write_timeout_s" json:"write_timeout_s"`     // per SSE write
}

func DefaultGen() GenConfig {
	return GenConfig{UserMaxActive: 2, TimeoutS: 900, IdleCancelS: 300, RetainS: 600, RunMaxBytes: 2 << 20, RunMaxEvents: 10000,
		PartialMaxBytes: 64 << 10, HubMaxRuns: 64, HubMaxBytes: 64 << 20, MaxSubscribers: 4, WriteTimeoutS: 30}
}

type Config struct {
	Generation      GenConfig      `yaml:"generation" json:"generation"`
	Upstream        string         `yaml:"upstream" json:"upstream"`
	Model           string         `yaml:"model" json:"model"`
	Defaults        map[string]any `yaml:"defaults" json:"defaults"`
	MaxConc         int            `yaml:"max_conc" json:"max_conc"`
	MaxTokensCap    int            `yaml:"max_tokens_cap" json:"max_tokens_cap"`
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
	// AllowedOrigins is the exact-match Origin allowlist for browser writes (CSRF).
	// Must be the gateway's own public origin. Empty = all browser writes rejected.
	AllowedOrigins []string `yaml:"allowed_origins" json:"allowed_origins"`
	StaticDir      string   `yaml:"static_dir" json:"static_dir"`
	// ToolsKillswitchFile is the optional 0600 off-only kill switch file
	// (docs/tool-platform.md §1.3). Empty = no kill switch (nothing disabled).
	ToolsKillswitchFile string `yaml:"tools_killswitch_file" json:"tools_killswitch_file"`
	// AllowedUsers are Tailscale logins accepted via the Tailscale-User-Login
	// header (loopback only). Empty = identity auth disabled.
	AllowedUsers []string `yaml:"allowed_users" json:"allowed_users"`
	// Env is "prod" (default) or "dev". Only dev may run the web tools with
	// the in-memory audit ledger (no database).
	Env      string     `yaml:"env" json:"env"`
	Auth     AuthConfig `yaml:"auth" json:"auth"`
	Database DBConfig   `yaml:"database" json:"database"`
	Web      WebConfig  `yaml:"web" json:"web"`
	Push     PushConfig `yaml:"push" json:"push"`
}

// PushConfig enables the Web Push subscription endpoints
// (docs/architecture/ui-api-contract.md §3.5.3). Off by default; when off the
// push endpoints answer 404. The VAPID public key is not a secret.
type PushConfig struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	VAPIDPublicKey string `yaml:"vapid_public_key" json:"vapid_public_key"`
	VAPIDKeyID     string `yaml:"vapid_key_id" json:"vapid_key_id"`
}

// WebConfig configures the web_search / web_fetch tools (docs/web-search.md
// §2.10). Disabled by default; requests asking for web search then get 400
// web_search_disabled.
type WebConfig struct {
	Enabled          bool     `yaml:"enabled" json:"enabled"`
	DefaultMode      string   `yaml:"default_mode" json:"default_mode"` // off|auto|force
	MaxRounds        int      `yaml:"max_rounds" json:"max_rounds"`
	MaxCalls         int      `yaml:"max_calls" json:"max_calls"`
	MaxSearches      int      `yaml:"max_searches" json:"max_searches"`
	RatePerMin       int      `yaml:"rate_per_min" json:"rate_per_min"`             // searches per user per minute
	FetchRatePerMin  int      `yaml:"fetch_rate_per_min" json:"fetch_rate_per_min"` // fetches per user per minute
	RetentionDays    int      `yaml:"retention_days" json:"retention_days"`
	Providers        []string `yaml:"providers" json:"providers"`
	SearxngURL       string   `yaml:"searxng_url" json:"searxng_url"`
	BraveKeyFile     string   `yaml:"brave_api_key_file" json:"-"`
	SearchTimeoutS   float64  `yaml:"search_timeout_s" json:"search_timeout_s"`
	SearchMaxResults int      `yaml:"search_max_results" json:"search_max_results"`
	FetchTimeoutS    float64  `yaml:"fetch_timeout_s" json:"fetch_timeout_s"`
	FetchMaxBytes    int64    `yaml:"fetch_max_bytes" json:"fetch_max_bytes"`
	FetchMaxChars    int      `yaml:"fetch_max_chars" json:"fetch_max_chars"`
	FetchMaxPages    int      `yaml:"fetch_max_pages" json:"fetch_max_pages"`
	FetchAllowedPort []int    `yaml:"fetch_allowed_ports" json:"fetch_allowed_ports"`
	FetchDenyCIDRs   []string `yaml:"fetch_deny_cidrs" json:"fetch_deny_cidrs"`
	FetchGlobalConc  int      `yaml:"fetch_global_conc" json:"fetch_global_conc"`
	FetchPerHostConc int      `yaml:"fetch_per_host_conc" json:"fetch_per_host_conc"`
}

func defaultWeb() WebConfig {
	return WebConfig{Enabled: false, DefaultMode: "off", MaxRounds: 3, MaxCalls: 5, MaxSearches: 3, RatePerMin: 10, FetchRatePerMin: 20,
		RetentionDays: 90, Providers: []string{"searxng"}, SearxngURL: "http://127.0.0.1:8888", SearchTimeoutS: 6, SearchMaxResults: 5,
		FetchTimeoutS: 10, FetchMaxBytes: 2 << 20, FetchMaxChars: 6000, FetchMaxPages: 3, FetchAllowedPort: []int{80, 443},
		FetchDenyCIDRs: []string{}, FetchGlobalConc: 4, FetchPerHostConc: 2}
}

// DBConfig configures conversation persistence (PostgreSQL). An empty URL
// disables persistence. Keep the URL out of git: set GW_DATABASE_URL.
type DBConfig struct {
	URL         string `yaml:"url" json:"-"` // never serialized (contains a password)
	AutoMigrate bool   `yaml:"auto_migrate" json:"auto_migrate"`
}

// AuthConfig selects authentication providers, tried in order.
type AuthConfig struct {
	Providers []string `yaml:"providers" json:"providers"`
}

// Default returns built-in defaults (same values as the shipped config.yaml).
func Default() Config {
	return Config{
		Upstream: "http://127.0.0.1:8080", Model: "mlx-community/Qwen3-30B-A3B-4bit",
		Defaults: map[string]any{"max_tokens": 8192, "temperature": 0.7, "top_p": 0.9},
		MaxConc:  2, MaxTokensCap: 16384, MaxQueue: 16, QueueTimeoutS: 120, ConnectTimeoutS: 5, ReadTimeoutS: 300,
		Retries: 2, RetryBackoffS: 0.5, LogLevel: "INFO", LogFile: "~/llm-serving/gateway.log",
		StatsWindow: 200, MinFreeMemGB: 4, CORSOrigins: []string{}, StaticDir: "~/llm-serving/web", AllowedUsers: []string{},
		Auth:       AuthConfig{Providers: []string{"tailscale-identity", "api-key"}},
		Database:   DBConfig{AutoMigrate: true},
		Web:        defaultWeb(),
		Generation: DefaultGen(),
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
	if c.Push.Enabled && strings.TrimSpace(c.ToolsKillswitchFile) == "" {
		// Push/proactive is gated by Killswitch.ProactiveAllowed, which needs a file.
		return c, fmt.Errorf("push.enabled requires tools_killswitch_file")
	}
	if c.MaxConc < 1 {
		return c, fmt.Errorf("max_conc must be >= 1")
	}
	if c.CORSOrigins == nil {
		c.CORSOrigins = []string{}
	}
	for _, o := range c.CORSOrigins {
		if strings.TrimSpace(o) == "*" {
			return c, fmt.Errorf("cors_origins: wildcard \"*\" is not allowed")
		}
	}
	if c.AllowedUsers == nil {
		c.AllowedUsers = []string{}
	}
	switch c.Web.DefaultMode {
	case "off", "auto", "force":
	case "":
		c.Web.DefaultMode = "off"
	default:
		return c, fmt.Errorf("web.default_mode must be off|auto|force")
	}
	return c, nil
}

func applyEnv(c *Config, getenv func(string) string) error {
	str := map[string]*string{"UPSTREAM": &c.Upstream, "MODEL": &c.Model, "LOG_LEVEL": &c.LogLevel, "LOG_FILE": &c.LogFile, "STATIC_DIR": &c.StaticDir, "TOOLS_KILLSWITCH_FILE": &c.ToolsKillswitchFile, "ENV": &c.Env}
	for k, p := range str {
		if v := getenv("GW_" + k); v != "" {
			*p = v
		}
	}
	// Integers accept decimals ("3.0") like the Python gateway did.
	ints := map[string]*int{"MAX_CONC": &c.MaxConc, "MAX_TOKENS_CAP": &c.MaxTokensCap, "MAX_QUEUE": &c.MaxQueue, "RETRIES": &c.Retries, "STATS_WINDOW": &c.StatsWindow}
	for k, p := range ints {
		if v := getenv("GW_" + k); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("GW_%s: %w", k, err)
			}
			*p = int(f)
		}
	}
	floats := map[string]*float64{"QUEUE_TIMEOUT_S": &c.QueueTimeoutS, "CONNECT_TIMEOUT_S": &c.ConnectTimeoutS, "READ_TIMEOUT_S": &c.ReadTimeoutS, "RETRY_BACKOFF_S": &c.RetryBackoffS, "MIN_FREE_MEM_GB": &c.MinFreeMemGB, "GEN_IDLE_CANCEL_S": &c.Generation.IdleCancelS, "GEN_TIMEOUT_S": &c.Generation.TimeoutS}
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
	if v := getenv("GW_ALLOWED_ORIGINS"); v != "" {
		c.AllowedOrigins = splitList(v)
	}
	if v := getenv("GW_ALLOWED_USERS"); v != "" {
		c.AllowedUsers = splitList(v)
	}
	if v := getenv("GW_DATABASE_URL"); v != "" {
		c.Database.URL = v
	}
	if v := getenv("GW_DB_AUTO_MIGRATE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GW_DB_AUTO_MIGRATE: %w", err)
		}
		c.Database.AutoMigrate = b
	}
	if v := getenv("GW_PUSH_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GW_PUSH_ENABLED: %w", err)
		}
		c.Push.Enabled = b
	}
	if v := getenv("GW_PUSH_VAPID_PUBLIC_KEY"); v != "" {
		c.Push.VAPIDPublicKey = v
	}
	if v := getenv("GW_PUSH_VAPID_KEY_ID"); v != "" {
		c.Push.VAPIDKeyID = v
	}
	if v := getenv("GW_AUTH_PROVIDERS"); v != "" {
		c.Auth.Providers = splitList(v)
	}
	return applyWebEnv(&c.Web, getenv)
}

func applyWebEnv(w *WebConfig, getenv func(string) string) error {
	if v := getenv("GW_WEB_ENABLED"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GW_WEB_ENABLED: %w", err)
		}
		w.Enabled = b
	}
	for k, p := range map[string]*string{"WEB_DEFAULT_MODE": &w.DefaultMode, "SEARXNG_URL": &w.SearxngURL, "BRAVE_API_KEY_FILE": &w.BraveKeyFile} {
		if v := getenv("GW_" + k); v != "" {
			*p = v
		}
	}
	ints := map[string]*int{"WEB_MAX_ROUNDS": &w.MaxRounds, "WEB_MAX_CALLS": &w.MaxCalls, "WEB_MAX_SEARCHES": &w.MaxSearches, "WEB_RATE_PER_MIN": &w.RatePerMin,
		"WEB_FETCH_RATE_PER_MIN": &w.FetchRatePerMin, "WEB_RETENTION_DAYS": &w.RetentionDays, "SEARCH_MAX_RESULTS": &w.SearchMaxResults,
		"FETCH_MAX_CHARS": &w.FetchMaxChars, "FETCH_MAX_PAGES": &w.FetchMaxPages, "FETCH_GLOBAL_CONC": &w.FetchGlobalConc, "FETCH_PER_HOST_CONC": &w.FetchPerHostConc}
	for k, p := range ints {
		if v := getenv("GW_" + k); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("GW_%s: %w", k, err)
			}
			*p = n
		}
	}
	for k, p := range map[string]*float64{"SEARCH_TIMEOUT": &w.SearchTimeoutS, "FETCH_TIMEOUT": &w.FetchTimeoutS} {
		if v := getenv("GW_" + k); v != "" {
			f, err := strconv.ParseFloat(strings.TrimSuffix(v, "s"), 64)
			if err != nil {
				return fmt.Errorf("GW_%s: %w", k, err)
			}
			*p = f
		}
	}
	if v := getenv("GW_FETCH_MAX_BYTES"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return fmt.Errorf("GW_FETCH_MAX_BYTES: %w", err)
		}
		w.FetchMaxBytes = n
	}
	if v := getenv("GW_SEARCH_PROVIDERS"); v != "" {
		w.Providers = splitList(v)
	}
	if v := getenv("GW_FETCH_DENY_CIDRS"); v != "" {
		w.FetchDenyCIDRs = splitList(v)
	}
	if v := getenv("GW_FETCH_ALLOWED_PORTS"); v != "" {
		w.FetchAllowedPort = nil
		for _, p := range splitList(v) {
			n, err := strconv.Atoi(p)
			if err != nil {
				return fmt.Errorf("GW_FETCH_ALLOWED_PORTS: %w", err)
			}
			w.FetchAllowedPort = append(w.FetchAllowedPort, n)
		}
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
