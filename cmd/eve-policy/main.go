// Command eve-policy maintains the web domain policy files
// (docs/tool-platform.md §13).
//
//	eve-policy verify [-dir deploy/policy]          # CI: policy loads, snapshot signatures verify, age <= 30 days
//	eve-policy import -source NAME -upstream-commit SHA -in FEED -out FILE  # feed -> snapshot (IP entries dropped)
//	eve-policy sign -key KEYFILE -key-id ID FILE       # writes FILE.sig ("ID:base64 ed25519 signature")
//	eve-policy keygen -out KEYFILE                     # new private key (0600) + prints the public key
//
// Private keys never go into the repository; only keys/*.pub is committed.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gun025bba/eve-serving/internal/policy/domains"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "verify":
		err = verify(os.Args[2:])
	case "import":
		err = importFeed(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "keygen":
		err = keygen(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "eve-policy:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: eve-policy verify|import|sign|keygen ...")
	os.Exit(2)
}

func verify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	dir := fs.String("dir", "deploy/policy", "policy directory")
	fs.Parse(args)
	now := time.Now()
	fsys := os.DirFS(*dir)
	if err := domains.CheckSnapshotFiles(fsys, now); err != nil {
		return err
	}
	p, err := domains.Load(fsys, now)
	if err != nil {
		return err
	}
	if len(p.Warnings) > 0 {
		return errors.New(strings.Join(p.Warnings, "; "))
	}
	d, a := p.RuleCount()
	fmt.Printf("ok: deny_rules=%d allow_rules=%d malware_generated=%s\n", d, a, p.SnapshotGenerated.UTC().Format(time.RFC3339))
	return nil
}

func importFeed(args []string) error {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	in := fs.String("in", "", "feed file (hosts format or one domain per line)")
	out := fs.String("out", "", "snapshot file to write")
	source := fs.String("source", "", "feed name/URL recorded in the header")
	upstream := fs.String("upstream-commit", "", "upstream feed commit/version the feed file was taken from")
	fs.Parse(args)
	if *in == "" || *out == "" || *source == "" || *upstream == "" {
		return errors.New("-in, -out, -source and -upstream-commit are required")
	}
	f, err := os.Open(*in)
	if err != nil {
		return err
	}
	defer f.Close()
	set := map[string]bool{}
	updated := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(l, "# Updated:"); ok && updated == "" {
			updated = strings.TrimSpace(v)
		}
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		fields := strings.Fields(l)
		h := fields[len(fields)-1]
		if _, err := domains.ParseIPLiteral(h); err == nil {
			continue // IP literals are blocked by the ip_literal rule anyway
		}
		n, err := domains.NormalizeHost(h)
		if err != nil || !strings.Contains(n, ".") {
			continue
		}
		set[n] = true
	}
	if err := sc.Err(); err != nil {
		return err
	}
	hosts := make([]string, 0, len(set))
	for h := range set {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	var b strings.Builder
	fmt.Fprintf(&b, "# Malware/phishing hosts: offline snapshot, verified by signature (docs/tool-platform.md §13).\n")
	fmt.Fprintf(&b, "# generated: %s\n# source: %s\n# upstream-commit: %s\n", time.Now().UTC().Format(time.RFC3339), *source, *upstream)
	if updated != "" {
		fmt.Fprintf(&b, "# upstream-updated: %s\n", updated)
	}
	fmt.Fprintf(&b, "# entries: %d\n", len(hosts))
	for _, h := range hosts {
		b.WriteString(h + "\n")
	}
	return os.WriteFile(*out, []byte(b.String()), 0o644)
}

func readKey(path string) (ed25519.PrivateKey, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s: permissions must be 0600", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(b) != ed25519.SeedSize {
		return nil, errors.New("bad private key file")
	}
	return ed25519.NewKeyFromSeed(b), nil
}

func sign(args []string) error {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	key := fs.String("key", "", "private key file (base64 seed, 0600)")
	keyID := fs.String("key-id", "", "key id of the matching keys/*.pub (recorded in the signature)")
	fs.Parse(args)
	if *key == "" || *keyID == "" || fs.NArg() != 1 || strings.ContainsAny(*keyID, ": \n") {
		return errors.New("usage: sign -key KEYFILE -key-id ID FILE")
	}
	k, err := readKey(*key)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	sig := *keyID + ":" + base64.StdEncoding.EncodeToString(ed25519.Sign(k, body))
	return os.WriteFile(fs.Arg(0)+".sig", []byte(sig+"\n"), 0o644)
}

func keygen(args []string) error {
	fs := flag.NewFlagSet("keygen", flag.ExitOnError)
	out := fs.String("out", "", "private key file to create (0600, must not exist)")
	fs.Parse(args)
	if *out == "" {
		return errors.New("-out required")
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
		return err
	}
	fmt.Println(base64.StdEncoding.EncodeToString(pub))
	return nil
}
