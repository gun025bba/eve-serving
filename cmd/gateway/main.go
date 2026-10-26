// Command gateway is the Go implementation of the local LLM gateway.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/gun025bba/eve-serving/internal/listen"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	policydata "github.com/gun025bba/eve-serving/deploy/policy"
	"github.com/gun025bba/eve-serving/internal/audit"
	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/handlers"
	"github.com/gun025bba/eve-serving/internal/logging"
	"github.com/gun025bba/eve-serving/internal/metrics"
	"github.com/gun025bba/eve-serving/internal/policy/domains"
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
	providers := cfg.Auth.Providers
	if cfg.TestIdentity.Injection {
		if err := auth.CheckInjection(auth.InjectionCheck{Env: cfg.Env, Host: *host, TCPEnabled: !cfg.TCPDisabled, DBURL: cfg.Database.URL,
			ProdUsers: cfg.AllowedUsers, Providers: cfg.Auth.Providers, UnixSocket: cfg.UnixSocket, Users: cfg.TestIdentity.Users}); err != nil {
			fatal("%v", err)
		}
		providers = append([]string{auth.ProviderTestIdentity}, providers...)
		log.Warn("test_identity_injection_enabled", "users", len(cfg.TestIdentity.Users))
	}
	chain, err := auth.Build(providers, auth.Options{APIKey: key, AllowedUsers: cfg.AllowedUsers, TestUsers: cfg.TestIdentity.Users})
	if err != nil {
		fatal("%v", err)
	}
	var st store.Store
	var ledger audit.Ledger
	var pgStore *postgres.Store
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
		// §5 startup gate: the gateway login must not be able to rewrite the
		// audit ledger (owner membership / UPDATE / DELETE / TRUNCATE).
		actx, ac := context.WithTimeout(context.Background(), 10*time.Second)
		err = pg.CheckAuditPrivileges(actx)
		ac()
		if err != nil {
			fatal("refusing to start: %v", err)
		}
		st, ledger, pgStore = pg, pg, pg
		// No generation survives a restart: close out rows left "generating".
		sctx, sc := context.WithTimeout(context.Background(), 10*time.Second)
		if n, err := pg.FailStaleGenerating(sctx); err != nil {
			log.Error("stale_generating_sweep_failed", "error", err.Error())
		} else if n > 0 {
			log.Warn("stale_generating_failed", "count", n)
		}
		sc()
	} else {
		// no database: nothing is persisted; audit rows live in memory only
		ledger = &audit.Memory{}
		log.Warn("audit_memory_only", "reason", "no database configured")
	}
	switch strings.ToLower(cfg.Env) {
	case "", "prod", "dev", "test", "e2e":
	default:
		log.Warn("unknown_gw_env", "value", cfg.Env, "treated_as", "prod")
	}
	breaker := audit.NewBreaker()
	breaker.OnTrip = func() {
		m.AuditBreakerOpen.Set(1)
		m.WebAuditDisabled.WithLabelValues("breaker").Set(1)
		log.Error("audit_breaker_open", "msg", "web tools disabled: repeated audit failures; restart after fixing the audit store")
	}
	srv := &handlers.Server{AuditBreaker: breaker, WebAuditBlocked: handlers.MemoryLedgerBlocksWeb(cfg, pgStore == nil), Audit: ledger, Store: st, Cfg: cfg, Key: key, Auth: chain, Log: log, M: m, Mem: &metrics.MemSampler{Needle: "mlx_lm.server"}, Q: q, Up: up, Start: time.Now(), Version: version}

	if srv.WebAuditBlocked {
		m.WebAuditDisabled.WithLabelValues("memory_ledger").Set(1)
		log.Warn("web_tools_disabled", "reason", "in-memory audit ledger outside GW_ENV=dev")
	}

	addr := net.JoinHostPort(*host, strconv.Itoa(*port))
	handler := srv.Handler()
	var servers []*http.Server
	var listeners []net.Listener
	if !cfg.TCPDisabled {
		tl, err := net.Listen("tcp", addr)
		if err != nil {
			fatal("listen: %v", err)
		}
		servers = append(servers, &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second})
		listeners = append(listeners, tl)
	} else {
		addr = ""
	}
	if cfg.UnixSocket != "" {
		ul, err := listen.Unix(config.Expand(cfg.UnixSocket))
		if err != nil {
			fatal("refusing to start: %v", err)
		}
		// Only connections accepted here may carry Tailscale identity headers.
		servers = append(servers, listen.TrustedServer(handler))
		listeners = append(listeners, ul)
	} else if slices.Contains(cfg.Auth.Providers, auth.ProviderTailscale) && len(cfg.AllowedUsers) > 0 {
		log.Warn("tailscale_identity_inactive", "reason", "no GW_UNIX_SOCKET: identity headers are only trusted on the unix socket; TCP accepts API keys only")
	}
	sysEvent := func(ev, status string, detail map[string]string) {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		if err := ledger.Append(ctx, "", audit.Event{At: time.Now(), Actor: audit.ActorSystem, Event: ev, Status: status, Detail: detail}); err != nil {
			m.AuditWriteFail.Inc()
			breaker.Fail()
			log.Error("audit_write_failed", "event", ev, "err", err.Error())
		}
	}
	// §13 domain policy (embedded, signed malware snapshot). A load failure
	// keeps the web tools fail-closed (every call: policy_unavailable).
	pol, perr := domains.Load(policydata.FS, time.Now())
	if perr != nil {
		pol = nil
		log.Error("web_policy_unavailable", "err", perr.Error())
		sysEvent(audit.EvPolicyLoaded, "error", nil)
	} else {
		for _, w := range pol.Warnings {
			log.Warn("web_policy_warning", "msg", w)
		}
		if !pol.SnapshotGenerated.IsZero() {
			m.PolicySnapshotAge.Set(time.Since(pol.SnapshotGenerated).Seconds())
		}
		if pol.SnapshotFallback {
			m.PolicyFallback.Set(1)
		}
		d, a := pol.RuleCount()
		log.Info("web_policy_loaded", "deny_rules", d, "allow_rules", a, "snapshot_fallback", pol.SnapshotFallback)
		sysEvent(audit.EvPolicyLoaded, "ok", map[string]string{"deny_rules": strconv.Itoa(d), "allow_rules": strconv.Itoa(a)})
	}
	if cfg.Web.Enabled {
		wt, err := handlers.NewWebTools(cfg.Web, pol)
		if err != nil {
			fmt.Fprintln(os.Stderr, "web tools:", err)
			os.Exit(1)
		}
		srv.Web = wt
		log.Info("web_tools_enabled", "providers", strings.Join(cfg.Web.Providers, ","), "default_mode", cfg.Web.DefaultMode)
	}
	reg := tools.Default() // embedded manifests; invalid manifests abort startup
	log.Info("tool_registry_loaded", "tools", strings.Join(reg.Reg.Names(), ","))
	sysEvent(audit.EvRegistryLoaded, "ok", map[string]string{"tools": strings.Join(reg.Reg.Names(), ",")})
	if cfg.ToolsKillswitchFile != "" {
		ks := tools.NewKillswitch(config.Expand(cfg.ToolsKillswitchFile), os.Getuid())
		ks.OnLoad = func(res string, disabled []string, allOff bool) {
			log.Info("tools_killswitch", "result", res, "all_off", allOff, "disabled", strings.Join(disabled, ","))
			sysEvent(audit.EvKillswitch, res, map[string]string{"all_off": strconv.FormatBool(allOff), "disabled": strings.Join(disabled, ",")})
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
	if pgStore != nil {
		go runAuditVerify(ctx, pgStore, log, m, breaker, 24*time.Hour)
	}
	defer stop()
	go func() {
		<-ctx.Done()
		sctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		for _, hs := range servers {
			hs.Shutdown(sctx)
		}
	}()
	log.Info("gateway_start", "addr", addr, "unix_socket", cfg.UnixSocket != "", "tcp", !cfg.TCPDisabled, "version", version, "upstream", cfg.Upstream, "max_conc", cfg.MaxConc, "max_queue", cfg.MaxQueue,
		"auth_providers", strings.Join(cfg.Auth.Providers, ","), "allowed_users", len(cfg.AllowedUsers), "persistence", st != nil)
	if addr != "" {
		fmt.Fprintf(os.Stderr, "gateway %s listening on %s\n", version, addr)
	}
	if cfg.UnixSocket != "" {
		fmt.Fprintf(os.Stderr, "gateway %s listening on unix socket\n", version)
	}
	errc := make(chan error, len(servers))
	for i := range servers {
		go func(hs *http.Server, l net.Listener) { errc <- hs.Serve(l) }(servers[i], listeners[i])
	}
	for range servers {
		if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("gateway_exit", "error", err.Error())
			fatal("serve: %v", err)
		}
	}
	log.Info("gateway_stop")
}

// runAuditVerify recomputes the tool_audit hash chain at startup and daily.
func runAuditVerify(ctx context.Context, pg *postgres.Store, log *logging.Logger, m *metrics.Metrics, br *audit.Breaker, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		vctx, c := context.WithTimeout(ctx, 5*time.Minute)
		res, err := pg.VerifyAudit(vctx)
		c()
		if err != nil {
			m.AuditVerifyFail.Inc()
			br.Fail()
			log.Error("audit_verify_failed", "err", err.Error(), "first_bad_seq", res.FirstBad)
		} else {
			log.Info("audit_verify_ok", "rows", res.Rows, "seq_gaps", res.SeqGaps)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "gateway: "+f+"\n", a...)
	os.Exit(1)
}
