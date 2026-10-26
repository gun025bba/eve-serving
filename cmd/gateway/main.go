// Command gateway is the Go implementation of the local LLM gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/handlers"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/proxy"
	"github.com/gun025bba/eve-serving/internal/queue"
	"github.com/gun025bba/eve-serving/internal/store"
	"github.com/gun025bba/eve-serving/internal/store/postgres"
	"github.com/gun025bba/eve-serving/internal/tools"
)

var version = "dev"

func secs(f float64) time.Duration { return time.Duration(f * float64(time.Second)) }

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func main() {
	base := flag.String("base", envOr("GW_BASE", "~/llm-serving"), "base dir holding config.yaml and .api_key")
	cfgPath := flag.String("config", os.Getenv("GW_CONFIG"), "config file (default <base>/config.yaml)")
	keyPath := flag.String("key-file", os.Getenv("GW_KEY_FILE"), "API key file (default <base>/.api_key)")
	host := flag.String("host", envOr("GW_HOST", "127.0.0.1"), "bind address")
	port := flag.Int("port", func() int { p, _ := strconv.Atoi(envOr("GW_PORT", "8000")); return p }(), "bind port")
	showVer := flag.Bool("version", false, "print version")
	migrateOnly := flag.Bool("migrate", false, "apply database migrations (GW_DATABASE_URL) and exit")
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	b := config.Expand(*base)
	if *cfgPath == "" {
		*cfgPath = filepath.Join(b, "config.yaml")
	}
	if *keyPath == "" {
		*keyPath = filepath.Join(b, ".api_key")
	}
	cfg, err := config.Load(config.Expand(*cfgPath), os.Getenv)
	if err != nil {
		fatal("config: %v", err)
	}
	if *migrateOnly {
		if cfg.Database.URL == "" {
			fatal("migrate: GW_DATABASE_URL / database.url is empty")
		}
		ctx, c := context.WithTimeout(context.Background(), time.Minute)
		defer c()
		if err := postgres.Migrate(ctx, cfg.Database.URL); err != nil {
			fatal("%v", err)
		}
		fmt.Fprintln(os.Stderr, "migrations applied")
		return
	}
	key, err := auth.LoadKey(config.Expand(*keyPath))
	if err != nil {
		fatal("api key: %v", err)
	}
	lf, err := os.OpenFile(config.Expand(cfg.LogFile), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fatal("log file: %v", err)
	}
	lv, err := logging.ParseLevel(cfg.LogLevel)
	if err != nil {
		fatal("log_level: %v", err)
	}
	log := logging.New(lf, lv)

	m := metrics.New(cfg.StatsWindow)
	q := queue.New(cfg.MaxConc, cfg.MaxQueue, secs(cfg.QueueTimeoutS))
	q.OnChange = func(w, i int64) { m.QDepth.Set(float64(w)); m.Inflight.Set(float64(i)) }
	up := proxy.New(proxy.Options{BaseURL: cfg.Upstream, ConnectTimeout: secs(cfg.ConnectTimeoutS), ReadTimeout: secs(cfg.ReadTimeoutS),
		Retries: cfg.Retries, Backoff: secs(cfg.RetryBackoffS),
		OnRetry: func(a int, e error) { log.Warn("upstream_connect_retry", "attempt", a, "error", e.Error()) }})
	chain, err := auth.Build(cfg.Auth.Providers, auth.Options{APIKey: key, AllowedUsers: cfg.AllowedUsers})
	if err != nil {
		fatal("%v", err)
	}
	var st store.Store
	if cfg.Database.URL != "" {
		ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		if cfg.Database.AutoMigrate {
			if err := postgres.Migrate(ctx, cfg.Database.URL); err != nil {
				c()
				fatal("database: %v", err)
			}
		}
		pg, err := postgres.Open(ctx, cfg.Database.URL)
		c()
		if err != nil {
			fatal("database: %v", err)
		}
		defer pg.Close()
		st = pg
	}
	srv := &handlers.Server{Store: st, Cfg: cfg, Key: key, Auth: chain, Log: log, M: m, Mem: &metrics.MemSampler{Needle: "mlx_lm.server"}, Q: q, Up: up, Start: time.Now(), Version: version}

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	hs := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
	if cfg.Web.Enabled {
		wt, err := handlers.NewWebTools(cfg.Web)
		if err != nil {
			fmt.Fprintln(os.Stderr, "web tools:", err)
			os.Exit(1)
		}
		srv.Web = wt
		log.Info("web_tools_enabled", "providers", strings.Join(cfg.Web.Providers, ","), "default_mode", cfg.Web.DefaultMode)
	}
	reg := tools.Default() // embedded manifests; invalid manifests abort startup
	log.Info("tool_registry_loaded", "tools", strings.Join(reg.Reg.Names(), ","))
	if cfg.ToolsKillswitchFile != "" {
		ks := tools.NewKillswitch(config.Expand(cfg.ToolsKillswitchFile), os.Getuid())
		ks.OnLoad = func(res string, disabled []string, allOff bool) {
			log.Info("tools_killswitch", "result", res, "all_off", allOff, "disabled", strings.Join(disabled, ","))
		}
		ks.Reload()
		tools.SetKillswitch(ks)
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
				ks.Reload()
			}
		}()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go srv.RunWebRetention(ctx, 24*time.Hour)
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		hs.Shutdown(sctx)
	}()
	log.Info("gateway_start", "addr", addr, "version", version, "upstream", cfg.Upstream, "max_conc", cfg.MaxConc, "max_queue", cfg.MaxQueue,
		"auth_providers", strings.Join(cfg.Auth.Providers, ","), "allowed_users", len(cfg.AllowedUsers), "persistence", st != nil)
	fmt.Fprintf(os.Stderr, "gateway %s listening on %s\n", version, addr)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("gateway_exit", "error", err.Error())
		fatal("listen: %v", err)
	}
	log.Info("gateway_stop")
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "gateway: "+f+"\n", a...)
	os.Exit(1)
}
