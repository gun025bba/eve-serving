// Package cli implements the host-side admin commands shared by the gateway
// binary (`gateway pair ...`) and `eve-gw`:
//
//	eve-gw pair --user <login>     print a one-time pairing code (10 min)
//	eve-gw sessions list           list browser sessions (no tokens)
//	eve-gw sessions revoke <id>    revoke one session
//
// They read the same config/env as the gateway (GW_BASE, GW_CONFIG,
// GW_DATABASE_URL, GW_SESSION_PEPPER_FILE) and need the database.
package cli

import (
	"context"
	"flag"
	"fmt"
	"github.com/gun025bba/eve-serving/internal/tools"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
	"github.com/gun025bba/eve-serving/internal/session"
	"github.com/gun025bba/eve-serving/internal/store/postgres"
)

// IsCommand reports whether args[0] is an admin subcommand.
func IsCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "pair" || args[0] == "sessions" || args[0] == "killswitch-check")
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// PepperPath is the session pepper file for cfg (default <base>/.session_pepper).
func PepperPath(cfg config.Config, base string) string {
	if cfg.SessionPepperFile != "" {
		return config.Expand(cfg.SessionPepperFile)
	}
	return filepath.Join(config.Expand(base), ".session_pepper")
}

// Run executes an admin subcommand and returns the exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if args[0] == "killswitch-check" {
		return killswitchCheck(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("eve-gw "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", envOr("GW_BASE", "~/llm-serving"), "base dir holding config.yaml")
	cfgPath := fs.String("config", os.Getenv("GW_CONFIG"), "config file (default <base>/config.yaml)")
	user := fs.String("user", "", "pair: users.login to bind the browser session to (must be in allowed_users)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	die := func(f string, a ...any) int {
		fmt.Fprintf(stderr, "eve-gw: "+f+"\n", a...)
		return 1
	}
	if *cfgPath == "" {
		*cfgPath = filepath.Join(config.Expand(*base), "config.yaml")
	}
	cfg, err := config.Load(config.Expand(*cfgPath), os.Getenv)
	if err != nil {
		return die("config: %v", err)
	}
	if cfg.Database.URL == "" {
		return die("GW_DATABASE_URL / database.url is empty (sessions need the database)")
	}
	pepper, err := session.LoadSecret(PepperPath(cfg, *base))
	if err != nil {
		return die("session pepper: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := postgres.Open(ctx, cfg.Database.URL)
	if err != nil {
		return die("database: %v", err)
	}
	defer pg.Close()
	m, err := session.New(pg, pepper)
	if err != nil {
		return die("%v", err)
	}
	rest := fs.Args()
	switch args[0] {
	case "pair":
		if *user == "" {
			return die("pair: --user <login> is required")
		}
		if !auth.AllowedLogin(cfg.AllowedUsers, *user) {
			return die("pair: %q is not in allowed_users", *user)
		}
		code, err := m.IssueCode(ctx, *user)
		if err != nil {
			return die("pair: %v", err)
		}
		fmt.Fprintf(stdout, "%s\n", code)
		fmt.Fprintf(stderr, "one-time pairing code for %s, valid %s (until %s). Enter it on the Eve page.\n",
			*user, session.CodeTTL, time.Now().Add(session.CodeTTL).Format("15:04:05 MST"))
		return 0
	case "sessions":
		if len(rest) == 0 {
			return die("sessions: list | revoke <id>")
		}
		switch rest[0] {
		case "list":
			ss, err := pg.ListSessions(ctx)
			if err != nil {
				return die("sessions: %v", err)
			}
			tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tUSER\tCREATED\tLAST_SEEN\tSTATE")
			for _, s := range ss {
				st := "active"
				switch {
				case s.RevokedAt != nil:
					st = "revoked"
				case time.Since(s.LastSeen) > session.IdleTTL || time.Since(s.CreatedAt) > session.AbsoluteTTL:
					st = "expired"
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", s.ID, s.Login, s.CreatedAt.Format(time.RFC3339), s.LastSeen.Format(time.RFC3339), st)
			}
			tw.Flush()
			return 0
		case "revoke":
			if len(rest) != 2 {
				return die("sessions revoke <id>")
			}
			id, err := strconv.ParseInt(rest[1], 10, 64)
			if err != nil {
				return die("sessions revoke: bad id %q", rest[1])
			}
			if err := pg.RevokeSession(ctx, id, time.Now()); err != nil {
				return die("sessions revoke: %v", err)
			}
			fmt.Fprintf(stdout, "revoked session %d\n", id)
			return 0
		}
		return die("sessions: unknown subcommand %q", rest[0])
	}
	return die("unknown command %q", args[0])
}

// killswitchCheck is the dry run of a candidate kill-switch file: it parses
// the file with the gateway's own rules, prints how this build and a 5.x build
// read it, applies nothing, and needs no config or database. Exit 0: valid for
// this build and for 5.x; 3: valid only for this build; 1: invalid.
func killswitchCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: eve-gw killswitch-check <file>")
		return 2
	}
	r := tools.CheckKillswitchFile(config.Expand(args[0]), os.Getuid())
	fmt.Fprint(stdout, r.String())
	switch {
	case r.Result != "ok":
		return 1
	case r.OldBuild != "valid":
		return 3
	}
	return 0
}
