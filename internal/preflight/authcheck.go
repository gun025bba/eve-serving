// Package preflight holds pre-bootstrap checks that reuse the gateway's own
// config loader, so deploy scripts never re-parse YAML or plists by hand.
package preflight

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/gun025bba/eve-serving/internal/auth"
	"github.com/gun025bba/eve-serving/internal/config"
)

// Result is what the check reports. It never contains login values.
type Result struct {
	Providers     []string // provider names (not secret)
	AllowedLogins int      // count only
	EnvKeys       int      // number of plist EnvironmentVariables keys
}

// ErrAbort marks a configuration that must not be bootstrapped.
var ErrAbort = errors.New("abort")

// ParsePlistEnv reads the EnvironmentVariables dict of an XML launchd plist.
// Duplicate keys, non-string values and binary plists are errors.
func ParsePlistEnv(r io.Reader) (map[string]string, error) {
	br := bufio.NewReader(r)
	if head, _ := br.Peek(8); bytes.HasPrefix(head, []byte("bplist")) {
		return nil, fmt.Errorf("binary plist: convert first (plutil -convert xml1 -o - <plist>)")
	}
	d := xml.NewDecoder(br)
	d.Strict = true
	// Walk to the top-level <dict>, then find key EnvironmentVariables at depth 1.
	depth := 0
	var lastKey string
	var found map[string]string
	readText := func() (string, error) {
		var sb strings.Builder
		for {
			t, err := d.Token()
			if err != nil {
				return "", err
			}
			switch v := t.(type) {
			case xml.CharData:
				sb.Write(v)
			case xml.EndElement:
				return sb.String(), nil
			case xml.StartElement:
				return "", fmt.Errorf("unexpected <%s> inside text element", v.Name.Local)
			}
		}
	}
	for {
		t, err := d.Token()
		if err == io.EOF {
			if found == nil {
				found = map[string]string{} // no EnvironmentVariables: empty env
			}
			return found, nil
		}
		if err != nil {
			return nil, fmt.Errorf("plist: %w", err)
		}
		switch v := t.(type) {
		case xml.StartElement:
			if v.Name.Local == "dict" {
				depth++
				if depth == 2 && lastKey == "EnvironmentVariables" {
					if found != nil {
						return nil, fmt.Errorf("plist: duplicate EnvironmentVariables")
					}
					if found, err = readEnvDict(d, readText); err != nil {
						return nil, err
					}
					depth--
					lastKey = ""
				}
				continue
			}
			if v.Name.Local == "key" && depth == 1 {
				if lastKey, err = readText(); err != nil {
					return nil, err
				}
				continue
			}
			if depth == 1 && v.Name.Local != "plist" {
				lastKey = "" // value of some other key
			}
		case xml.EndElement:
			if v.Name.Local == "dict" {
				depth--
			}
		}
	}
}

func readEnvDict(d *xml.Decoder, readText func() (string, error)) (map[string]string, error) {
	env := map[string]string{}
	var key string
	haveKey := false
	for {
		t, err := d.Token()
		if err != nil {
			return nil, fmt.Errorf("plist env: %w", err)
		}
		switch v := t.(type) {
		case xml.StartElement:
			switch v.Name.Local {
			case "key":
				if haveKey {
					return nil, fmt.Errorf("plist env: key without value")
				}
				if key, err = readText(); err != nil {
					return nil, err
				}
				if _, dup := env[key]; dup {
					return nil, fmt.Errorf("plist env: duplicate key %s", key)
				}
				haveKey = true
			case "string":
				if !haveKey {
					return nil, fmt.Errorf("plist env: value without key")
				}
				val, err := readText()
				if err != nil {
					return nil, err
				}
				env[key], haveKey = val, false
			default:
				return nil, fmt.Errorf("plist env: %s must be a <string>", key)
			}
		case xml.EndElement:
			if v.Name.Local == "dict" {
				if haveKey {
					return nil, fmt.Errorf("plist env: key without value")
				}
				return env, nil
			}
		}
	}
}

// CheckAuth loads cfgPath with exactly the gateway's loader, overlaying only
// env (the plist's EnvironmentVariables, never the caller's environment), and
// builds the auth chain. Any error is ErrAbort-wrapped; messages never contain
// login values or config contents.
func CheckAuth(cfgPath string, env map[string]string) (Result, error) {
	r := Result{EnvKeys: len(env)}
	getenv := func(k string) string { return env[k] }
	cfg, err := config.Load(cfgPath, getenv)
	if err != nil {
		// The loader's message can quote YAML snippets; do not echo it.
		return r, fmt.Errorf("%w: config does not load with the gateway loader (start the gateway in a terminal to see why)", ErrAbort)
	}
	r.Providers = cfg.Auth.Providers
	for _, u := range cfg.AllowedUsers {
		if strings.TrimSpace(u) != "" {
			r.AllowedLogins++
		}
	}
	if _, err := auth.Build(cfg.Auth.Providers, auth.Options{APIKey: "preflight-placeholder", AllowedUsers: cfg.AllowedUsers}); err != nil {
		msg := "auth providers do not build"
		for _, p := range cfg.Auth.Providers {
			if p == auth.ProviderTailscale && r.AllowedLogins == 0 {
				msg = "tailscale-identity is enabled but there are 0 allowed logins (GW_ALLOWED_USERS in the plist, or allowed_users in config.yaml); the gateway would crash-loop. Fix: set a non-empty GW_ALLOWED_USERS in the plist, or set auth.providers: [api-key] in config.yaml for API key only"
			}
		}
		return r, fmt.Errorf("%w: %s", ErrAbort, msg)
	}
	return r, nil
}
