#!/bin/bash
# Offline tests for deploy/nopasswd/mac-oneshot.sh. Everything it calls is a fake on PATH (gh with an in-memory GitHub, curl, sudo, ssh, ssh-keygen); nothing
# touches the network, GitHub, sudo or the real home directory. Needs bash, python3, jq (skips with a note if python3 or jq is missing).
#   bash deploy/nopasswd/test/mac-oneshot-test.sh          (also run by test/run.sh, so CI runs it on Ubuntu and on macOS with bash 3.2)
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
D=$(cd "$HERE/.." && pwd)
SCRIPT=$D/mac-oneshot.sh
T=$(cd "$(mktemp -d)" && pwd -P); trap 'rm -rf "$T"' EXIT
PASS=0; FAILN=0
ok()  { PASS=$((PASS + 1)); }
bad() { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()  { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
has() { case "$2" in *"$3"*) ok;; *) bad "$1: missing [$3]";; esac; }
hasnt() { case "$2" in *"$3"*) bad "$1: unexpected [$3]";; *) ok;; esac; }
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
if ! command -v python3 >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then echo "SKIP mac-oneshot tests: python3 and jq are needed"; echo "mac-oneshot tests passed: 0 failed: 0 (skipped)"; exit 0; fi

# ---------------- static checks ----------------
const() { sed -n "s/^$1=\([^ ]*\).*/\1/p" "$SCRIPT" | head -1; }
[ -x "$SCRIPT" ] && ok || bad "mac-oneshot.sh must be executable"
eq "pin: installer sha256 = the installer file" "$(const INSTALL_SHA256)" "$(sha256 "$D/setup-deploy-nopasswd.sh")"
eq "pin: entrypoint sha256 = eve-deploy" "$(const ENTRY_SHA256)" "$(sha256 "$D/eve-deploy")"
eq "pin: entrypoint sha256 = SHA_DEPLOY of the installer" "$(const ENTRY_SHA256)" "$(sed -n 's/^SHA_DEPLOY=//p' "$D/setup-deploy-nopasswd.sh")"
eq "pin: known_hosts sha256" "$(const KNOWN_HOSTS_SHA256)" "$(sha256 "$D/github_known_hosts")"
grep -q "$(const INSTALL_COMMIT)/deploy/nopasswd/setup-deploy-nopasswd.sh" "$D/README.md" && ok || bad "README install line must use the same installer commit as mac-oneshot.sh"
grep -q "= $(const INSTALL_SHA256) " "$D/README.md" && ok || bad "README install line must use the same installer sha256 as mac-oneshot.sh"
eq "pin: installer SRC_COMMIT is a full sha" "$(sed -n 's/^SRC_COMMIT=//p' "$D/setup-deploy-nopasswd.sh" | grep -cE '^[0-9a-f]{40}$')" 1
eq "eve-deploy keeps web disabled" "$(sed -n 's/^WEB_ENABLED=\([a-z]*\).*/\1/p' "$D/eve-deploy")" no
case "$(head -1 "$SCRIPT")" in "#!/bin/bash") ok;; *) bad "shebang";; esac
grep -q '^set -euo pipefail' "$SCRIPT" && ok || bad "set -euo pipefail"
grep -nE '(^|[^a-z_])(set -x|set -o xtrace)' "$SCRIPT" >/dev/null && bad "xtrace must never be enabled" || ok
grep -nE 'declare -A|mapfile|readarray|\$\{[a-zA-Z_]+(,,|\^\^)\}|\|&' "$SCRIPT" >/dev/null && bad "bash 3.2: no associative arrays, mapfile, case-conversion expansions or |&" || ok
if command -v shellcheck >/dev/null 2>&1; then shellcheck -s bash "$SCRIPT" >"$T/sc.out" 2>&1 && ok || { bad "shellcheck"; cat "$T/sc.out"; }; else echo "note: shellcheck not installed, skipped"; fi

# ---------------- fakes ----------------
mkdir -p "$T/bin" "$T/state"
cat >"$T/bin/gh" <<'FAKE'
#!/usr/bin/env python3
# in-memory GitHub for the repo gun025bba/eve-web; state in $FAKE_STATE/gh.json, every call logged to $FAKE_STATE/gh.log
import json, os, sys, subprocess
S = os.environ["FAKE_STATE"]; R = "repos/gun025bba/eve-web"
argv = sys.argv[1:]
def load():
    with open(S + "/gh.json") as f: return json.load(f)
def save(st):
    with open(S + "/gh.json", "w") as f: json.dump(st, f)
def log(o):
    with open(S + "/gh.log", "a") as f: f.write(json.dumps(o) + "\n")
if argv[:2] == ["auth", "login"]:
    cfg = os.environ.get("GH_CONFIG_DIR", "")
    log({"cmd": "login", "args": argv, "cfg": cfg, "GH_TOKEN": os.environ.get("GH_TOKEN"), "GITHUB_TOKEN": os.environ.get("GITHUB_TOKEN"), "gitcfg": os.environ.get("GIT_CONFIG_GLOBAL")})
    if os.environ.get("FAKE_LOGIN_FAIL"): sys.exit(1)
    open(cfg + "/hosts.yml", "w").write("github.com: fake-token-in-throwaway-dir\n"); sys.exit(0)
if argv[:2] == ["auth", "token"]:
    cfg = os.environ.get("GH_CONFIG_DIR", ""); log({"cmd": "token", "cfg": cfg})
    if os.environ.get("FAKE_TOKEN_UNREADABLE"): sys.exit(1)
    if not os.path.exists(cfg + "/hosts.yml"): sys.exit(1)
    print(os.environ.get("FAKE_TOKEN") or "gho_FAKETOKEN0123456789"); sys.exit(0)
if argv[:2] == ["auth", "logout"]:
    cfg = os.environ.get("GH_CONFIG_DIR", ""); log({"cmd": "logout", "cfg": cfg, "had_login": os.path.exists(cfg + "/hosts.yml")})
    try: os.remove(cfg + "/hosts.yml")
    except OSError: pass
    sys.exit(0)
if not argv or argv[0] != "api": log({"cmd": "BAD", "args": argv}); sys.exit(2)
method, ep, jq, inp, i, args, ver = "GET", None, None, False, 1, argv, ""
while i < len(args):
    a = args[i]
    if a == "-H": ver = args[i + 1].split(": ")[-1]; i += 2; continue
    if a == "-i": i += 1; continue
    if a == "-X": method = args[i + 1]; i += 2; continue
    if a == "--input": inp = True; i += 2; continue
    if a == "--jq": jq = args[i + 1]; i += 2; continue
    ep = a; i += 1
body = json.loads(sys.stdin.read()) if inp else None
path = ep.split("?")[0]
log({"cmd": "api", "method": method, "ep": path, "ver": ver, "body": body, "GH_TOKEN": os.environ.get("GH_TOKEN"), "cfg": os.environ.get("GH_CONFIG_DIR"), "GH_DEBUG": os.environ.get("GH_DEBUG"), "DEBUG": os.environ.get("DEBUG"), "SHELLOPTS": os.environ.get("SHELLOPTS")})
if not os.path.exists(os.environ.get("GH_CONFIG_DIR", "/nonexistent") + "/hosts.yml"): print("gh: not logged in", file=sys.stderr); sys.exit(4)
st = load()
def out(obj):
    if jq is None:
        print(json.dumps(obj)); sys.exit(0)
    p = subprocess.run(["jq", "-r", jq], input=json.dumps(obj), capture_output=True, text=True)
    if p.returncode: print(p.stderr, file=sys.stderr); sys.exit(3)
    sys.stdout.write(p.stdout); sys.exit(0)
def nf(): print("gh: Not Found (HTTP 404)", file=sys.stderr); sys.exit(1)
def done(): save(st); sys.exit(0)
if path == "user": out({"login": st["user"]})
if path == R: out({"permissions": {"admin": st["admin"]}})
if path == R + "/rulesets":
    if method == "GET": out([{"id": r["id"], "name": r["name"], "target": r["target"]} for r in st["rulesets"]])
    if method == "POST":
        if st.get("reject_deploykey") and any(b.get("actor_type") == "DeployKey" for b in body.get("bypass_actors", [])): print("gh: Validation Failed (HTTP 422)", file=sys.stderr); sys.exit(1)
        st["next"] += 1; body["id"] = st["next"]; st["rulesets"].append(body); done()
if path.startswith(R + "/rulesets/"):
    rid = int(path.rsplit("/", 1)[1]); idx = [k for k, r in enumerate(st["rulesets"]) if r["id"] == rid]
    if not idx: nf()
    if method == "GET": out(st["rulesets"][idx[0]])
    if method == "PUT": body["id"] = rid; st["rulesets"][idx[0]] = body; done()
if path == R + "/environments/deploy":
    if method == "GET":
        if not st["env"]: nf()
        out(st["env"])
    if method == "PUT":
        if st.get("env_put_fail"): print("gh: Validation Failed (HTTP 422)", file=sys.stderr); sys.exit(1)
        st["env"] = body; done()
if path == R + "/environments/deploy/deployment-branch-policies":
    if not st["env"]: nf()
    if method == "GET": out({"total_count": len(st["policies"]), "branch_policies": st["policies"]})
    if method == "POST": st["next"] += 1; st["policies"].append({"id": st["next"], "name": body["name"], "type": body["type"]}); done()
if path.startswith(R + "/environments/deploy/deployment-branch-policies/") and method == "DELETE":
    pid = int(path.rsplit("/", 1)[1]); st["policies"] = [p for p in st["policies"] if p["id"] != pid]; done()
if path == R + "/immutable-releases":
    if method == "GET":
        if not st["immutable"]: nf()
        out({"enabled": True, "enforced_by_owner": False})
    if method == "PUT": st["immutable"] = True; done()
if path.startswith(R + "/actions/permissions"):
    if st.get("v1_actions_fail") and ver == "2022-11-28": print("gh: Not Found (HTTP 404)", file=sys.stderr); sys.exit(1)
    if st.get("actions_put_fail") and method == "PUT": print("gh: Forbidden (HTTP 403) token gho_FAKETOKEN0123456789 ghp_SECRETSECRET1 " + os.environ.get("FAKE_TOKEN", ""), file=sys.stderr); sys.exit(1)
if path == R + "/actions/permissions":
    if method == "GET": out(st["actions"])
    if method == "PUT":
        if body.get("allowed_actions") not in ("all", "local_only", "selected"): print("gh: bad allowed_actions (HTTP 422)", file=sys.stderr); sys.exit(1)
        st["actions"].update(body); done()
if path == R + "/actions/permissions/workflow":
    if method == "GET": out(st["workflow"])
    if method == "PUT": st["workflow"].update(body); done()
if path == R + "/actions/permissions/fork-pr-contributor-approval":
    if method == "GET": out(st["fork"])
    if method == "PUT": st["fork"].update(body); done()
if path == R + "/keys":
    if method == "GET": out(st["keys"])
    if method == "POST":
        if any(k["key"] == body["key"] for k in st["keys"]): print("gh: key is already in use (HTTP 422)", file=sys.stderr); sys.exit(1)
        st["next"] += 1; st["keys"].append({"id": st["next"], "key": body["key"], "title": body["title"], "read_only": body["read_only"]}); done()
if path.startswith(R + "/keys/") and method == "DELETE":
    kid = int(path.rsplit("/", 1)[1]); st["keys"] = [k for k in st["keys"] if k["id"] != kid]; done()
print("gh: unhandled fake call %s %s" % (method, path), file=sys.stderr); sys.exit(5)
FAKE
cat >"$T/bin/ssh-keygen" <<'FAKE'
#!/bin/bash
echo "ssh-keygen $*" >>"$FAKE_STATE/other.log"
case " $* " in
  *" -lf "*) echo "256 SHA256:FAKEFINGERPRINT0001 eve-web-release-tagger (ED25519)"; exit 0;;
  *" -y "*) f=${@: -1}; grep -q ENCRYPTED "$f" && exit 1; n=$(head -1 "$f" | cut -d: -f2); echo "ssh-ed25519 AAAAFAKEKEY$n"; exit 0;;
esac
f=""; while [ $# -gt 0 ]; do [ "$1" = -f ] && f=$2; shift; done
n=$(od -An -N6 -tx1 /dev/urandom 2>/dev/null | tr -d ' \n'); n=${n:-$RANDOM$RANDOM}
printf 'PRIVATE:%s\nFAKE-PRIVATE-KEY-MARKER-9c1e\n' "$n" >"$f"
printf 'ssh-ed25519 AAAAFAKEKEY%s eve-web-release-tagger\n' "$n" >"$f.pub"
FAKE
cat >"$T/bin/ssh" <<'FAKE'
#!/bin/bash
echo "ssh $*" >>"$FAKE_STATE/other.log"
key=""; while [ $# -gt 0 ]; do [ "$1" = -i ] && key=$2; shift; done
pub=$(cut -d' ' -f1,2 <"$key.pub")
if [ "${FAKE_SSH_OK:-1}" = 1 ] && python3 -c 'import json,sys; st=json.load(open(sys.argv[1]+"/gh.json")); sys.exit(0 if any(k["key"]==sys.argv[2] for k in st["keys"]) else 1)' "$FAKE_STATE" "$pub"; then
  echo "Hi gun025bba/eve-web! You've successfully authenticated, but GitHub does not provide shell access."; exit 1
fi
echo "git@github.com: Permission denied (publickey)."; exit 255
FAKE
cat >"$T/bin/curl" <<'FAKE'
#!/bin/bash
echo "curl $*" >>"$FAKE_STATE/other.log"
out=""; url=""; hdr=""; while [ $# -gt 0 ]; do case "$1" in -o) out=$2; shift;; -H) if [ "$2" = "@-" ]; then hdr=$(cat); fi; shift;; http*) url=$1;; esac; shift; done
case "$url" in
  https://api.github.com/user)   # the revocation proof: the token arrives ONLY on stdin (-H @-); the answer is 401 once revoked
    ok=no; [ "$hdr" = "Authorization: Bearer ${FAKE_TOKEN:-gho_FAKETOKEN0123456789}" ] && ok=yes
    python3 - "$FAKE_STATE/gh.log" "$ok" "${FAKE_TOKEN_DEAD:-1}" <<'PY'
import json, sys
open(sys.argv[1], "a").write(json.dumps({"cmd": "token-check", "ep": "user", "stdin_token_ok": sys.argv[2], "alive": sys.argv[3] == "0"}) + "\n")
PY
    if [ "${FAKE_TOKEN_DEAD:-1}" = 0 ]; then printf 200; else printf 401; fi; exit 0;;
esac
case "$url" in *github_known_hosts) src=$FAKE_KH;; *) src=$FAKE_INSTALLER;; esac
if [ -n "${FAKE_CURL_TAMPER:-}" ]; then echo "echo pwned" >"${out:-/dev/stdout}"; exit 0; fi
if [ -n "$out" ]; then cat "$src" >"$out"; else cat "$src"; fi
FAKE
cat >"$T/bin/pbcopy" <<'FAKE'
#!/bin/bash
cat >"$FAKE_STATE/clipboard"
FAKE
cat >"$T/bin/open" <<'FAKE'
#!/bin/bash
# the owner pasting the clipboard on the "new deploy key" page and clicking Add key (a key added in the browser belongs to the repository, not to a token)
echo "open $*" >>"$FAKE_STATE/other.log"
n=$(cat "$FAKE_STATE/open.count" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_STATE/open.count"
[ -n "${FAKE_OPEN_NOADD:-}" ] && exit 0
[ "$n" -lt "${FAKE_OPEN_ADD_ON:-1}" ] && exit 0
python3 - "$FAKE_STATE" "${FAKE_OPEN_READONLY:-}" "${FAKE_OPEN_FOREIGN:-}" <<'PY'
import json, sys
S = sys.argv[1]; st = json.load(open(S + "/gh.json")); pub = " ".join(open(S + "/clipboard").read().split()[:2])
st["next"] += 1; st["keys"].append({"id": st["next"], "key": pub, "title": "eve-web-release-tagger", "read_only": bool(sys.argv[2])})
if sys.argv[3]: st["next"] += 1; st["keys"].append({"id": st["next"], "key": "ssh-ed25519 AAAAFOREIGNBLOB", "title": "added-meanwhile", "read_only": False})
json.dump(st, open(S + "/gh.json", "w"))
PY
FAKE
cat >"$T/bin/id" <<'FAKE'
#!/bin/bash
[ -n "${FAKE_UID:-}" ] && { echo "$FAKE_UID"; exit 0; }
exec /usr/bin/id "$@"
FAKE
cat >"$T/bin/sudo" <<'FAKE'
#!/bin/bash
echo "sudo $*" >>"$FAKE_STATE/other.log"
cat >"$FAKE_STATE/sudo.stdin"
FAKE
chmod +x "$T/bin/"*
cp "$D/setup-deploy-nopasswd.sh" "$T/installer.sh"; cp "$D/github_known_hosts" "$T/kh.pinned"; printf 'added\nrevoked\n' >"$T/tty.yes"

# ---------------- harness ----------------
init_state() { # fresh fake GitHub: the existing eve-web main-protect (no linear history, not strict), no env, no deploy keys
  rm -rf "$T/state"; mkdir -p "$T/state"; rm -f "$T/tty.cur"
  python3 - "$T/state/gh.json" <<'PY'
import json, sys
json.dump({"user": "gun025bba", "admin": True, "next": 1000,
  "rulesets": [{"id": 26085409, "name": "main-protect", "target": "branch", "enforcement": "active", "conditions": {"ref_name": {"include": ["~DEFAULT_BRANCH"], "exclude": []}}, "bypass_actors": [],
    "rules": [{"type": "deletion"}, {"type": "non_fast_forward"}, {"type": "required_status_checks", "parameters": {"required_status_checks": [{"context": "test", "integration_id": 15368}], "strict_required_status_checks_policy": False}}]}],
  "env": None, "policies": [], "immutable": False,
  "actions": {"enabled": True, "allowed_actions": "all", "sha_pinning_required": False},
  "workflow": {"default_workflow_permissions": "write", "can_approve_pull_request_reviews": True},
  "fork": {"approval_policy": "first_time_contributors"}, "keys": []}, open(sys.argv[1], "w"))
PY
}
mutate() { python3 - "$T/state/gh.json" "$1" <<'PY'
import json, sys
st = json.load(open(sys.argv[1])); exec(sys.argv[2]); json.dump(st, open(sys.argv[1], "w"))
PY
}
state() { python3 -c 'import json,sys; st=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$T/state/gh.json" "$1"; }
HOME_T=$T/home; LIBX=$T/libexec
reset_home() { rm -rf "$HOME_T"; mkdir -p "$HOME_T"; rm -rf "$LIBX"; mkdir -p "$LIBX"; }
# run the script in a copy next to a copy of the known_hosts file; PATH = fakes first. Exports a poisoned GH_TOKEN to prove it is not passed on.
SD=$T/sd; mkdir -p "$SD"; cp "$SCRIPT" "$SD/mac-oneshot.sh"; cp "$D/github_known_hosts" "$SD/github_known_hosts"
run() { ( cd "$T" && env HOME="$HOME_T" PATH="$T/bin:$PATH" FAKE_STATE="$T/state" FAKE_INSTALLER="$T/installer.sh" FAKE_KH="$T/kh.pinned" ONESHOT_TTY="$T/tty.yes" ONESHOT_LIBEXEC="$LIBX" GH_TOKEN=LEAKED-TOKEN GITHUB_TOKEN=LEAKED-TOKEN2 TMPDIR="$T/tmp" "$@" ) >"$T/out" 2>"$T/err"; RC=$?; OUT=$(cat "$T/out" "$T/err"); }
mkdir -p "$T/tmp"
glog() { cat "$T/state/gh.log" 2>/dev/null; }
nwrites() { glog | python3 -c 'import sys,json; print(sum(1 for l in sys.stdin if json.loads(l).get("method") in ("POST","PUT","DELETE")))'; }
nothing_left() { [ -z "$(ls "$T/tmp" 2>/dev/null)" ]; }

# ---------------- 1. dry run: no tool is called, nothing is created ----------------
init_state; reset_home; rm -f "$T/state/gh.log" "$T/state/other.log"
run bash "$SD/mac-oneshot.sh" --dry-run
eq "dry-run rc" "$RC" 0
[ ! -e "$T/state/gh.log" ] && [ ! -e "$T/state/other.log" ] && ok || bad "dry-run must not call gh, curl, sudo, ssh or ssh-keygen"
[ ! -e "$HOME_T/.eve-web-release-tagger" ] && ok || bad "dry-run must not create the key dir"
eq "dry-run changed no GitHub state" "$(state 'len(st["rulesets"])')" 1
nothing_left && ok || bad "dry-run leaves no temp dir"
for w in "sudo /bin/bash -s" "gh auth login --web --hostname github.com" "release-tags" "release-tags-lock" "refs/tags/v*" '"actor_type":"DeployKey"' "main-protect" "required_linear_history" "environments/deploy" "immutable-releases" "sha_pinning_required" "default_workflow_permissions" "fork-pr-contributor-approval" "ssh-keygen -q -t ed25519" "settings/keys/new"; do has "dry-run mentions [$w]" "$OUT" "$w"; done
run bash "$SD/mac-oneshot.sh" --bogus; eq "unknown argument refused" "$RC" 1

# ---------------- 2. full run ----------------
init_state; reset_home
run bash "$SD/mac-oneshot.sh"
eq "full run rc" "$RC" 0
has "full run: all checks passed" "$OUT" "ALL CHECKS PASSED"
hasnt "full run: no FAIL line" "$OUT" "FAIL "
eq "sudo got exactly the checked installer text" "$(sha256 "$T/state/sudo.stdin")" "$(sed -n 's/^INSTALL_SHA256=\([0-9a-f]*\).*/\1/p' "$SCRIPT")"
has "sudo invoked as sudo /bin/bash -s" "$(cat "$T/state/other.log")" "sudo /bin/bash -s"
has "curl pinned https+tls1.2 and the pinned commit" "$(cat "$T/state/other.log")" "--proto =https --tlsv1.2 https://raw.githubusercontent.com/gun025bba/eve-serving/$(sed -n 's/^INSTALL_COMMIT=\([0-9a-f]*\).*/\1/p' "$SCRIPT")/deploy/nopasswd/setup-deploy-nopasswd.sh"
eq "login args" "$(glog | python3 -c 'import sys,json; [print(" ".join(d["args"])) for d in map(json.loads, sys.stdin) if d["cmd"]=="login"]')" "auth login --web --hostname github.com --git-protocol https --scopes repo --insecure-storage"
eq "no GH_TOKEN/GITHUB_TOKEN (inherited or the login's own) ever reached gh" "$(glog | python3 -c 'import sys,json; print(sum(1 for d in map(json.loads, sys.stdin) if d.get("GH_TOKEN") or d.get("GITHUB_TOKEN")))')" 0
eq "gh used a throwaway config dir under TMPDIR" "$(glog | python3 -c 'import sys,json; print(all(d.get("cfg","").startswith((sys.argv[1]+"/eve-oneshot-gh.", sys.argv[1]+"/eve-oneshot-chk.")) for d in map(json.loads, sys.stdin) if d.get("cfg")))' "$T/tmp")" True
eq "git config redirected into the throwaway dir" "$(glog | python3 -c 'import sys,json; d=[d for d in map(json.loads, sys.stdin) if d["cmd"]=="login"][0]; print(d["gitcfg"]==d["cfg"]+"/gitconfig")')" True
eq "logged out with a live login" "$(glog | python3 -c 'import sys,json; print([d["had_login"] for d in map(json.loads, sys.stdin) if d["cmd"]=="logout"])')" "[True]"
nothing_left && ok || bad "throwaway dir must be deleted on exit"
has "revoke: link printed" "$OUT" "https://github.com/settings/applications"
has "revoke: prompt printed" "$OUT" 'Type "revoked"'
has "revoke: token proven dead" "$OUT" "token is dead (GitHub answered 401)"
eq "revoke: the token was tested exactly once, via stdin (curl -H @-), and it was the real token" "$(glog | python3 -c 'import sys,json; print([(d["ep"], d["stdin_token_ok"]) for d in map(json.loads, sys.stdin) if d["cmd"]=="token-check"])')" "[('user', 'yes')]"
hasnt "revoke: the token is not on a curl command line" "$(cat "$T/state/other.log")" "gho_FAKETOKEN"
has "revoke: curl pinned https+tls1.2 for the proof" "$(grep 'api.github.com' "$T/state/other.log")" "--proto =https --tlsv1.2"
eq "revoke: token check came after the logout" "$(glog | python3 -c 'import sys,json; c=[d["cmd"] for d in map(json.loads, sys.stdin)]; print(c.index("logout") < c.index("token-check"))')" True
hasnt "the OAuth token is never printed" "$OUT" "gho_FAKETOKEN"
hasnt "the OAuth token is never in the temp files left" "$(ls "$T/tmp")" "gho_"
eq "rulesets: 3 (release-tags and release-tags-lock created, main-protect updated in place)" "$(state 'sorted((r["name"], r["id"]==26085409) for r in st["rulesets"])')" "[('main-protect', True), ('release-tags', False), ('release-tags-lock', False)]"
eq "release-tags rules: only creation" "$(state '",".join(sorted(r["type"] for r in [x for x in st["rulesets"] if x["name"]=="release-tags"][0]["rules"]))')" "creation"
eq "release-tags-lock rules" "$(state '",".join(sorted(r["type"] for r in [x for x in st["rulesets"] if x["name"]=="release-tags-lock"][0]["rules"]))')" "deletion,non_fast_forward,update"
eq "release-tags-lock bypass = admin role ONLY (no deploy key)" "$(state 'sorted((b["actor_type"], b["actor_id"], b["bypass_mode"]) for b in [x for x in st["rulesets"] if x["name"]=="release-tags-lock"][0]["bypass_actors"])')" "[('RepositoryRole', 5, 'always')]"
eq "release-tags-lock ref pattern" "$(state '[x for x in st["rulesets"] if x["name"]=="release-tags-lock"][0]["conditions"]["ref_name"]["include"]')" "['refs/tags/v*']"
eq "release-tags bypass = admin role + DeployKey" "$(state 'sorted((b["actor_type"], b["actor_id"], b["bypass_mode"]) for b in [x for x in st["rulesets"] if x["name"]=="release-tags"][0]["bypass_actors"])')" "[('DeployKey', None, 'always'), ('RepositoryRole', 5, 'always')]"
eq "release-tags ref pattern" "$(state '[x for x in st["rulesets"] if x["name"]=="release-tags"][0]["conditions"]["ref_name"]["include"]')" "['refs/tags/v*']"
eq "main-protect: linear history added" "$(state '",".join(sorted(r["type"] for r in [x for x in st["rulesets"] if x["name"]=="main-protect"][0]["rules"]))')" "deletion,non_fast_forward,required_linear_history,required_status_checks"
eq "main-protect: strict" "$(state '[r for r in [x for x in st["rulesets"] if x["name"]=="main-protect"][0]["rules"] if r["type"]=="required_status_checks"][0]["parameters"]["strict_required_status_checks_policy"]')" True
eq "environment: admins cannot bypass, custom policies" "$(state 'st["env"]')" "{'can_admins_bypass': False, 'deployment_branch_policy': {'protected_branches': False, 'custom_branch_policies': True}}"
eq "environment: only the v* tag policy" "$(state '[(p["type"], p["name"]) for p in st["policies"]]')" "[('tag', 'v*')]"
eq "immutable releases on" "$(state 'st["immutable"]')" True
eq "SHA pinning on, allowed_actions kept" "$(state '(st["actions"]["sha_pinning_required"], st["actions"]["allowed_actions"], st["actions"]["enabled"])')" "(True, 'all', True)"
eq "workflow perms read" "$(state '(st["workflow"]["default_workflow_permissions"], st["workflow"]["can_approve_pull_request_reviews"])')" "('read', False)"
eq "fork PR approval" "$(state 'st["fork"]["approval_policy"]')" all_external_contributors
eq "one deploy key, write, titled" "$(state '[(k["title"], k["read_only"]) for k in st["keys"]]')" "[('eve-web-release-tagger', False)]"
K=$HOME_T/.eve-web-release-tagger
eq "key dir mode" "$(stat -c %a "$K" 2>/dev/null || stat -f %Lp "$K")" 700
eq "private key mode" "$(stat -c %a "$K/id_ed25519" 2>/dev/null || stat -f %Lp "$K/id_ed25519")" 600
hasnt "private key never printed" "$OUT" "FAKE-PRIVATE-KEY-MARKER"
hasnt "private key never sent to GitHub" "$(glog)" "FAKE-PRIVATE-KEY-MARKER"
hasnt "private key never sent to GitHub (content)" "$(glog)" "$(head -1 "$K/id_ed25519")"
hasnt "no token printed" "$OUT" "LEAKED-TOKEN"
has "ssh -T options: ssh config off" "$(cat "$T/state/other.log")" "ssh -F /dev/null -i $K/id_ed25519"
for o in IdentitiesOnly=yes IdentityAgent=none BatchMode=yes StrictHostKeyChecking=yes "UserKnownHostsFile=$SD/github_known_hosts" GlobalKnownHostsFile=/dev/null UpdateHostKeys=no "-T git@github.com"; do has "ssh option $o" "$(grep '^ssh ' "$T/state/other.log")" "$o"; done
PUB1=$(cat "$K/id_ed25519.pub")

# ---------------- 3. re-run: idempotent, no duplicates, key kept, no second install ----------------
cp "$D/eve-deploy" "$LIBX/deploy"; : >"$T/state/other.log"; : >"$T/state/gh.log"; rm -f "$T/state/sudo.stdin"
run bash "$SD/mac-oneshot.sh"
eq "re-run rc" "$RC" 0
has "re-run: all checks passed" "$OUT" "ALL CHECKS PASSED"
eq "re-run: still 3 rulesets" "$(state 'len(st["rulesets"])')" 3
eq "re-run: still one key" "$(state 'len(st["keys"])')" 1
eq "re-run: same key" "$(cat "$K/id_ed25519.pub")" "$PUB1"
eq "re-run: still one environment policy" "$(state 'len(st["policies"])')" 1
[ ! -e "$T/state/sudo.stdin" ] && ok || bad "re-run: the installer must be skipped when the pinned entrypoint is installed"
has "re-run says skipped" "$OUT" "already installed"
eq "re-run: no immutable PUT when already on" "$(glog | grep -c '"ep": "repos/gun025bba/eve-web/immutable-releases", "method": "PUT"' || true)" 0
nothing_left && ok || bad "re-run leaves no temp dir"

# ---------------- 4. refusals: nothing is changed, the login is removed ----------------
init_state; reset_home; mutate 'st["user"]="someone-else"'
run bash "$SD/mac-oneshot.sh" --skip-install
eq "wrong GitHub user rc" "$RC" 1; has "wrong user message" "$OUT" "not gun025bba"
eq "wrong user: no write at all" "$(nwrites)" 0
nothing_left && ok || bad "wrong user: temp dir deleted"
eq "wrong user: logged out" "$(glog | grep -c '"cmd": "logout"' || true)" 1
eq "wrong user: nothing registered on GitHub" "$(state 'len(st["keys"])')" 0

init_state; reset_home; mutate 'st["admin"]=False'
run bash "$SD/mac-oneshot.sh" --skip-install
eq "not admin rc" "$RC" 1; has "not admin message" "$OUT" "not an admin"; eq "not admin: no write" "$(nwrites)" 0

init_state; reset_home
run env FAKE_LOGIN_FAIL=1 bash "$SD/mac-oneshot.sh" --skip-install
eq "login failure rc" "$RC" 1; eq "login failure: no write" "$(nwrites)" 0; nothing_left && ok || bad "login failure: temp dir deleted"

init_state; reset_home
run env FAKE_CURL_TAMPER=1 bash "$SD/mac-oneshot.sh"
eq "tampered installer rc" "$RC" 1; has "tampered installer message" "$OUT" "sha256 mismatch"
[ ! -e "$T/state/sudo.stdin" ] && ok || bad "tampered installer must NOT reach sudo"
[ ! -e "$T/state/gh.log" ] && ok || bad "tampered installer: nothing else runs"

init_state; reset_home; cp "$SD/github_known_hosts" "$T/kh.bak"; echo "x" >>"$SD/github_known_hosts"
run env FAKE_CURL_TAMPER=1 bash "$SD/mac-oneshot.sh"
eq "tampered local AND tampered downloaded known_hosts: rc" "$RC" 1; has "tampered known_hosts message" "$OUT" "pinned sha256"; [ ! -e "$T/state/gh.log" ] && ok || bad "tampered known_hosts: nothing runs"
[ ! -e "$T/state/sudo.stdin" ] && ok || bad "tampered known_hosts: no sudo"
nothing_left && ok || bad "tampered known_hosts: no temp dir left"
init_state; reset_home; rm -f "$T/state/other.log"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "tampered LOCAL known_hosts + good pinned download: rc" "$RC" 0
has "pinned known_hosts downloaded from the pinned commit" "$(cat "$T/state/other.log")" "raw.githubusercontent.com/gun025bba/eve-serving/$(sed -n 's/^INSTALL_COMMIT=\([0-9a-f]*\).*/\1/p' "$SCRIPT")/deploy/nopasswd/github_known_hosts"
hasnt "ssh used the downloaded copy, not the tampered local one" "$(grep '^ssh ' "$T/state/other.log")" "UserKnownHostsFile=$SD/"
nothing_left && ok || bad "downloaded known_hosts dir is deleted on exit"
cp "$T/kh.bak" "$SD/github_known_hosts"

# clone-free: the script text passed to `bash -c` from a directory without the repository files (how the README line runs it)
init_state; reset_home; mkdir -p "$T/empty"; rm -f "$T/state/other.log"
( cd "$T/empty" && env HOME="$HOME_T" PATH="$T/bin:$PATH" FAKE_STATE="$T/state" FAKE_INSTALLER="$T/installer.sh" FAKE_KH="$T/kh.pinned" ONESHOT_TTY="$T/tty.yes" ONESHOT_LIBEXEC="$LIBX" TMPDIR="$T/tmp" bash -c "$(cat "$SCRIPT")" mac-oneshot ) >"$T/out" 2>&1; RC=$?; OUT=$(cat "$T/out")
eq "clone-free run (bash -c) rc" "$RC" 0; has "clone-free: all checks passed" "$OUT" "ALL CHECKS PASSED"
nothing_left && ok || bad "clone-free: no temp dir left"

init_state; reset_home; mutate 'st["rulesets"].append(dict(st["rulesets"][0], id=77))'
run bash "$SD/mac-oneshot.sh" --skip-install
eq "duplicate ruleset names rc" "$RC" 1; has "duplicate ruleset message" "$OUT" "exists 2 times"

init_state; reset_home; mutate 'st["reject_deploykey"]=True'
run bash "$SD/mac-oneshot.sh" --skip-install
eq "DeployKey bypass rejected by the API: fails loudly" "$([ "$RC" != 0 ] && echo failed)" failed
nothing_left && ok || bad "API error: temp dir deleted"

# ---------------- 5. repairs of a wrong state ----------------
init_state; reset_home
mutate 'st["env"]={"can_admins_bypass": True, "deployment_branch_policy": {"protected_branches": True, "custom_branch_policies": False}}; st["policies"]=[{"id":1,"name":"main","type":"branch"},{"id":2,"name":"v*","type":"tag"},{"id":3,"name":"v*","type":"tag"},{"id":4,"name":"rel*","type":"tag"}]'
mutate 'st["immutable"]=False; st["actions"]["allowed_actions"]="selected"'
run bash "$SD/mac-oneshot.sh" --skip-install
eq "repair rc" "$RC" 0
eq "repair: exactly one tag v* policy remains" "$(state '[(p["type"], p["name"]) for p in st["policies"]]')" "[('tag', 'v*')]"
eq "repair: admins bypass off" "$(state 'st["env"]["can_admins_bypass"]')" False
eq "repair: allowed_actions=selected is preserved" "$(state 'st["actions"]["allowed_actions"]')" selected

init_state; reset_home
mkdir -p "$HOME_T/.eve-web-release-tagger"; chmod 755 "$HOME_T/.eve-web-release-tagger"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "existing wide key dir is tightened" "$(stat -c %a "$HOME_T/.eve-web-release-tagger" 2>/dev/null || stat -f %Lp "$HOME_T/.eve-web-release-tagger")" 700

init_state; reset_home; mkdir -p "$HOME_T/.eve-web-release-tagger"; printf 'PRIVATE:1\nENCRYPTED\n' >"$HOME_T/.eve-web-release-tagger/id_ed25519"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "key with passphrase refused" "$RC" 1; has "passphrase message" "$OUT" "refusing to touch it"; eq "passphrase key untouched" "$(sed -n 2p "$HOME_T/.eve-web-release-tagger/id_ed25519")" ENCRYPTED
eq "passphrase key: no GitHub write" "$(nwrites)" 0

# stale write key with the same title, a read-only copy of the right key, an unrelated key
init_state; reset_home
run bash "$SD/mac-oneshot.sh" --skip-install; KEYPUB=$(cut -d' ' -f1,2 <"$HOME_T/.eve-web-release-tagger/id_ed25519.pub")
mutate "st['keys']=[{'id':5,'key':'ssh-ed25519 AAAAOLD','title':'eve-web-release-tagger','read_only':False},{'id':6,'key':'$KEYPUB','title':'readonly-copy','read_only':True}]"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "stale same-title key and read-only copy are replaced by one write key" "$(state '[(k["title"], k["read_only"]) for k in st["keys"]]')" "[('eve-web-release-tagger', False)]"
mutate "st['keys'].append({'id':9,'key':'ssh-ed25519 AAAAOTHER','title':'someone','read_only':False})"
: >"$T/state/gh.log"; mutate "st['rulesets']=[r for r in st['rulesets'] if r['name']=='main-protect']"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "M-1: a foreign WRITE deploy key aborts the run" "$RC" 1
has "M-1: says no change" "$OUT" "NO change"
has "M-1: names the foreign key" "$OUT" "FOREIGN WRITE deploy key: id 9, title 'someone'"
eq "M-1: NO write call at all (no key change, no ruleset, no setting)" "$(nwrites)" 0
eq "M-1: the foreign key is not deleted, nothing registered" "$(state 'sorted(k["id"] for k in st["keys"] if k["id"] == 9) + [len(st["keys"])]')" "[9, 2]"
eq "M-1: the key list was read before anything else on the repo" "$(glog | python3 -c 'import sys,json; print([d["ep"] for d in map(json.loads, sys.stdin) if d["cmd"]=="api"][:3])')" "['user', 'repos/gun025bba/eve-web', 'repos/gun025bba/eve-web/keys']"
eq "M-1: rulesets untouched" "$(state '[r["name"] for r in st["rulesets"]]')" "['main-protect']"
eq "M-1: the login is still revoked (prompt, 401)" "$(glog | grep -c token-check || true)" 1
# read-only foreign keys do not matter (they do not inherit the bypass)
init_state; reset_home; mutate "st['keys']=[{'id':9,'key':'ssh-ed25519 AAAAOTHER','title':'ci-readonly','read_only':True}]"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "M-1: a read-only key of someone else is fine" "$RC" 0
eq "M-1: and it is left alone" "$(state 'sorted(k["title"] for k in st["keys"])')" "['ci-readonly', 'eve-web-release-tagger']"

# ssh test fails -> FAIL and non-zero
init_state; reset_home
run env FAKE_SSH_OK=0 bash "$SD/mac-oneshot.sh" --skip-install
eq "ssh -T failure rc" "$RC" 1; has "ssh -T failure line" "$OUT" "FAIL  ssh -T"

# a setting that did not stick shows as FAIL
init_state; reset_home
cat >"$T/bin/gh.real" <<<"" ; rm -f "$T/bin/gh.real"
mutate 'st["fork_frozen"]=True'
python3 - "$T/bin/gh" <<'PY'
import sys
p = sys.argv[1]; s = open(p).read()
s = s.replace('if method == "PUT": st["fork"].update(body); done()', 'if method == "PUT" and not st.get("fork_frozen"): st["fork"].update(body); done()\n    if method == "PUT": sys.exit(0)')
open(p, "w").write(s)
PY
run bash "$SD/mac-oneshot.sh" --skip-install
eq "a setting that did not apply: rc 1" "$RC" 1; has "FAIL line for fork approval" "$OUT" "FAIL  fork PR approval"

# ---------------- M-2: the OAuth token must be proven dead ----------------
init_state; reset_home; run env FAKE_TOKEN_DEAD=0 bash "$SD/mac-oneshot.sh" --skip-install
eq "M-2: token still valid after the prompt: rc 1" "$RC" 1
has "M-2: FAIL row" "$OUT" "FAIL  GitHub CLI OAuth token revoked: got [confirmed=yes http=200]"
has "M-2: loud warning" "$OUT" "may STILL BE VALID"
init_state; reset_home; run env ONESHOT_TTY=/dev/null bash "$SD/mac-oneshot.sh" --skip-install
eq "M-2: no confirmation possible (no tty / EOF): rc 1" "$RC" 1
has "M-2: unconfirmed FAIL row" "$OUT" "got [confirmed=no http=401]"
printf 'added\nno\nnope\nrevoked\n' >"$T/tty.late"; init_state; reset_home; run env ONESHOT_TTY="$T/tty.late" bash "$SD/mac-oneshot.sh" --skip-install
eq "M-2: the prompt repeats until the owner types revoked" "$RC" 0
printf 'added\na\nb\nc\nrevoked\n' >"$T/tty.never"; init_state; reset_home; run env ONESHOT_TTY="$T/tty.never" bash "$SD/mac-oneshot.sh" --skip-install
eq "M-2: three wrong answers = unconfirmed" "$RC" 1
# an early stop after the login still goes through the revoke flow (wrong user)
init_state; reset_home; mutate 'st["user"]="someone-else"'; run bash "$SD/mac-oneshot.sh" --skip-install
eq "M-2: wrong-user stop still prompts for the revocation and proves it (one token check)" "$(glog | grep -c token-check || true)" 1
has "M-2: wrong-user stop prints the link" "$OUT" "https://github.com/settings/applications"
eq "M-2: wrong-user stop stays rc 1" "$RC" 1

# ---------------- Lows ----------------
init_state; reset_home; mkdir -p "$HOME_T/.eve-web-release-tagger"
run env FAKE_UID=4242 bash "$SD/mac-oneshot.sh" --skip-install
eq "owner check: a key dir not owned by you is refused" "$RC" 1; has "owner check message" "$OUT" "not owned by you"; eq "owner check: no GitHub call" "$([ -e "$T/state/gh.log" ] && echo yes || echo no)" no
init_state; reset_home; run bash "$SD/mac-oneshot.sh" --skip-install
run env FAKE_UID=4242 bash "$SD/mac-oneshot.sh" --skip-install
eq "owner check: an existing key file not owned by you is refused" "$RC" 1
init_state; reset_home; mutate 'st["env_put_fail"]=True'
run bash "$SD/mac-oneshot.sh" --skip-install
eq "environment PUT failing does not abort: the verify table decides (rc 1)" "$RC" 1
has "environment failure is reported by the table" "$OUT" "FAIL  environment deploy"
has "the other settings were still applied and verified" "$OUT" "PASS  immutable releases"
has "the revoke flow still ran" "$OUT" "token is dead"

# ---------------- 7. the deploy key is added in the browser (a key created with the OAuth token dies when the token is revoked) ----------------
init_state; reset_home; run bash "$SD/mac-oneshot.sh" --skip-install
eq "key: browser flow, rc 0" "$RC" 0
eq "key: NO deploy key is created or changed through the API token (it would be deleted by GitHub at the revocation)" "$(glog | python3 -c 'import sys,json; print(sum(1 for d in map(json.loads, sys.stdin) if d.get("method") in ("POST","PUT","DELETE") and d.get("ep","").endswith("/keys")))')" 0
has "key: the new-deploy-key page was opened" "$(cat "$T/state/other.log")" "open https://github.com/gun025bba/eve-web/settings/keys/new"
eq "key: the clipboard got the PUBLIC key with the title" "$(cat "$T/state/clipboard")" "$(cut -d' ' -f1,2 <"$HOME_T/.eve-web-release-tagger/id_ed25519.pub") eve-web-release-tagger"
hasnt "key: the private key is not in the clipboard" "$(cat "$T/state/clipboard")" "PRIVATE"
has "key: the instructions say what to tick" "$OUT" "Allow write access"
has "key: the public key is shown" "$OUT" "$(cut -d' ' -f2 <"$HOME_T/.eve-web-release-tagger/id_ed25519.pub")"
hasnt "key: the private key is not shown" "$OUT" "FAKE-PRIVATE-KEY-MARKER"
# the owner needs two tries
init_state; reset_home; printf 'added\nadded\nrevoked\n' >"$T/tty.two"
run env FAKE_OPEN_ADD_ON=2 ONESHOT_TTY="$T/tty.two" bash "$SD/mac-oneshot.sh" --skip-install
eq "key: not found after the first 'added' -> asked again, then fine: rc" "$RC" 0
eq "key: the page was opened twice" "$(cat "$T/state/open.count")" 2
# the owner never adds it
init_state; reset_home; printf 'added\nadded\nadded\nrevoked\n' >"$T/tty.three"
run env FAKE_OPEN_NOADD=1 ONESHOT_TTY="$T/tty.three" bash "$SD/mac-oneshot.sh" --skip-install
eq "key: never added: rc 1" "$RC" 1
has "key: FAIL row" "$OUT" "FAIL  only write deploy key"
has "key: FAIL row for ssh" "$OUT" "FAIL  ssh -T"
has "key: by-hand instructions" "$OUT" "settings/keys/new  title eve-web-release-tagger"
eq "key: asked at most 3 times" "$(cat "$T/state/open.count")" 3
has "key: the revocation still ran" "$OUT" "token is dead"
# a key that is already registered is not asked for again
init_state; reset_home; run bash "$SD/mac-oneshot.sh" --skip-install; rm -f "$T/state/open.count"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "key: already registered: rc 0" "$RC" 0; [ ! -e "$T/state/open.count" ] && ok || bad "key: a registered key must not open the browser again"

# ---------------- 8. --only-fix ----------------
keysum() { HOME_T=$HOME_T FS=$T/state/gh.json python3 -c 'import json,os; st=json.load(open(os.environ["FS"])); pub=" ".join(open(os.environ["HOME_T"]+"/.eve-web-release-tagger/id_ed25519.pub").read().split()[:2]); print([(k["title"],k["read_only"],k["key"]==pub) for k in st["keys"]])'; }
KFILES() { cksum "$HOME_T/.eve-web-release-tagger/id_ed25519" "$HOME_T/.eve-web-release-tagger/id_ed25519.pub"; stat -c %Y "$HOME_T/.eve-web-release-tagger/id_ed25519" "$HOME_T/.eve-web-release-tagger/id_ed25519.pub" 2>/dev/null || stat -f %m "$HOME_T/.eve-web-release-tagger/id_ed25519" "$HOME_T/.eve-web-release-tagger/id_ed25519.pub"; }
writes_outside_actions() { glog | python3 -c 'import sys,json; print(sum(1 for d in map(json.loads, sys.stdin) if d.get("method") in ("POST","PUT","DELETE") and not d["ep"].startswith("repos/gun025bba/eve-web/actions/permissions") and not d["ep"].startswith("repos/gun025bba/eve-web/rulesets")))'; }
others_snap() { state 'sorted((r["name"], json.dumps(r, sort_keys=True)) for r in st["rulesets"] if r["name"] != "release-tags")'; }
tags_bypass() { state 'sorted((b["actor_type"], b["actor_id"]) for b in [x for x in st["rulesets"] if x["name"]=="release-tags"][0]["bypass_actors"])'; }
# state after a first run whose Actions settings failed and whose API-created key was deleted by the revocation
first_run_aftermath() {
  init_state; reset_home; run bash "$SD/mac-oneshot.sh" --skip-install
  mutate 'st["keys"]=[]; st["actions"]["sha_pinning_required"]=False; st["workflow"]={"default_workflow_permissions":"write","can_approve_pull_request_reviews":True}; st["fork"]={"approval_policy":"first_time_contributors"}'
  : >"$T/state/gh.log"; : >"$T/state/other.log"; rm -f "$T/state/open.count" "$T/state/sudo.stdin"
}
first_run_aftermath; KB=$(KFILES); OTHERS0=$(others_snap)
run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix: rc 0" "$RC" 0
has "only-fix: all checks passed (the whole table is printed)" "$OUT" "ALL CHECKS PASSED"
has "only-fix: says what it does" "$OUT" "ONLY-FIX"
eq "only-fix: the key files are untouched (content and mtime)" "$(KFILES)" "$KB"
hasnt "only-fix: no sudo" "$(cat "$T/state/other.log")" "sudo"
[ ! -e "$T/state/sudo.stdin" ] && ok || bad "only-fix: no installer"
eq "only-fix: no key is generated" "$(grep -c 'ssh-keygen -q' "$T/state/other.log" || true)" 0
eq "only-fix: no write outside the Actions endpoints and the release-tags ruleset" "$(writes_outside_actions)" 0
eq "only-fix: the three Actions settings and release-tags were written" "$(glog | python3 -c 'import sys,json,re; print(sorted(re.sub(r"rulesets/[0-9]+", "rulesets/N", d["ep"].rsplit("eve-web/",1)[1]) for d in map(json.loads, sys.stdin) if d.get("method")=="PUT"))')" "['actions/permissions', 'actions/permissions/fork-pr-contributor-approval', 'actions/permissions/workflow', 'rulesets/N']"
eq "only-fix: release-tags keeps the DeployKey bypass (our key is registered)" "$(tags_bypass)" "[('DeployKey', None), ('RepositoryRole', 5)]"
eq "only-fix: main-protect and release-tags-lock are not rewritten" "$(others_snap)" "$OTHERS0"
eq "only-fix: SHA pinning" "$(state '(st["actions"]["sha_pinning_required"], st["actions"]["allowed_actions"])')" "(True, 'all')"
eq "only-fix: workflow perms" "$(state '(st["workflow"]["default_workflow_permissions"], st["workflow"]["can_approve_pull_request_reviews"])')" "('read', False)"
eq "only-fix: fork approval" "$(state 'st["fork"]["approval_policy"]')" all_external_contributors
eq "only-fix: the existing public key is registered as the write key, added in the browser" "$(keysum)" "[('eve-web-release-tagger', False, True)]"
eq "only-fix: rulesets, environment, immutable releases were read back, not rewritten" "$(glog | python3 -c 'import sys,json; print(sum(1 for d in map(json.loads, sys.stdin) if d.get("method")=="GET" and ("rulesets" in d["ep"] or "environments" in d["ep"] or "immutable" in d["ep"])) > 0)')" True
eq "only-fix: logged out with a live login, then the token was proven dead once" "$(glog | python3 -c 'import sys,json; d=[x["cmd"] for x in map(json.loads, sys.stdin)]; print((d.count("logout"), d.count("token-check")))')" "(1, 1)"
has "only-fix: revoke prompt" "$OUT" 'Type "revoked"'
nothing_left && ok || bad "only-fix: no temp dir left"
# it can be run again: nothing to do for the key, the settings are idempotent
: >"$T/state/gh.log"; rm -f "$T/state/open.count"
run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix twice: rc 0" "$RC" 0; [ ! -e "$T/state/open.count" ] && ok || bad "only-fix: a registered key must not open the browser again"
# the first API version fails (404) for the Actions endpoints: the other version is tried
first_run_aftermath; mutate 'st["v1_actions_fail"]=True'
run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix: API version fallback: rc 0" "$RC" 0
eq "only-fix: the first version was refused, the PUTs succeeded with the second" "$(glog | python3 -c 'import sys,json; print(sorted(set(d["ver"] for d in map(json.loads, sys.stdin) if d.get("method")=="PUT")))')" "['2022-11-28', '2026-03-10']"
eq "only-fix: fallback result: SHA pinning on" "$(state 'st["actions"]["sha_pinning_required"]')" True
# every attempt fails: the error is printed (no token in it), the rest runs, the table decides, by-hand instructions
first_run_aftermath; mutate 'st["actions_put_fail"]=True; st["v1_actions_fail"]=False'
run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix: Actions PUTs refused: rc 1" "$RC" 1
has "only-fix: the API error is printed" "$OUT" "API ERROR: PUT repos/gun025bba/eve-web/actions/permissions -> gh: Forbidden (HTTP 403)"
hasnt "only-fix: no token in the error text" "$OUT" "gho_FAKETOKEN"
hasnt "only-fix: no PAT in the error text" "$OUT" "ghp_SECRETSECRET1"
has "only-fix: FAIL row SHA pinning" "$OUT" "FAIL  Actions SHA pinning required"
has "only-fix: FAIL row workflow permissions" "$OUT" "FAIL  default workflow permissions"
has "only-fix: FAIL row fork" "$OUT" "FAIL  fork PR approval"
has "only-fix: the key and ssh rows still pass" "$OUT" "PASS  only write deploy key"
has "only-fix: by-hand Actions instructions" "$OUT" "https://github.com/gun025bba/eve-web/settings/actions"
has "only-fix: token still revoked" "$OUT" "token is dead"
# a read failure shows why (it used to be a bare ERR)
first_run_aftermath; mutate 'st["v1_actions_fail"]=True'; python3 - "$T/bin/gh" <<'PY'
import sys
p = sys.argv[1]; s = open(p).read()
s = s.replace('if st.get("v1_actions_fail") and ver == "2022-11-28":', 'if st.get("v1_actions_fail") and ver in ("2022-11-28", "2026-03-10"):')
open(p, "w").write(s)
PY
run bash "$SD/mac-oneshot.sh" --only-fix
has "read failure: GET error text is printed" "$OUT" "API ERROR: GET repos/gun025bba/eve-web/actions/permissions -> gh: Not Found (HTTP 404)"
has "read failure: SHA pinning not changed, says why" "$OUT" "cannot read the current allowed_actions"
has "read failure: the row still prints ERR" "$OUT" "got [ERR]"
eq "read failure: rc 1" "$RC" 1
# refusals: nothing is changed
init_state; reset_home; run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix without a key: rc 1" "$RC" 1; has "only-fix without a key: says run the full line" "$OUT" "run the full line first"
[ ! -e "$HOME_T/.eve-web-release-tagger" ] && ok || bad "only-fix must not create the key dir"
[ ! -e "$T/state/gh.log" ] && ok || bad "only-fix without a key: no GitHub call"
first_run_aftermath; chmod 755 "$HOME_T/.eve-web-release-tagger"; run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix: a key dir with the wrong mode is refused (not chmod-ed)" "$RC" 1; eq "only-fix: mode unchanged" "$(stat -c %a "$HOME_T/.eve-web-release-tagger" 2>/dev/null || stat -f %Lp "$HOME_T/.eve-web-release-tagger")" 755
[ ! -e "$T/state/gh.log" ] || [ ! -s "$T/state/gh.log" ] && ok || bad "only-fix: wrong key dir mode: no GitHub call"
first_run_aftermath; echo "ssh-ed25519 AAAAOTHERKEYBLOB x" >"$HOME_T/.eve-web-release-tagger/id_ed25519.pub"; run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix: a .pub that is not the public key of the private key is refused" "$RC" 1; has "only-fix: mismatch message" "$OUT" "is not the public key of"
first_run_aftermath; mutate "st['keys']=[{'id':9,'key':'ssh-ed25519 AAAAOTHER','title':'someone','read_only':False}]"
run bash "$SD/mac-oneshot.sh" --only-fix
eq "only-fix keeps M-1: a foreign write key aborts with no change" "$RC" 1
has "only-fix M-1: message" "$OUT" "NO change"
eq "only-fix M-1: no write call" "$(nwrites)" 0
run bash "$SD/mac-oneshot.sh" --only-fix --skip-github; eq "only-fix + skip-github refused" "$RC" 1; has "only-fix + skip-github message" "$OUT" "exclude each other"
init_state; reset_home; rm -f "$T/state/gh.log" "$T/state/other.log"
run bash "$SD/mac-oneshot.sh" --only-fix --dry-run
eq "only-fix dry-run rc" "$RC" 0; has "only-fix dry-run mentions the mode" "$OUT" "ONLY-FIX"; has "only-fix dry-run lists the Actions calls" "$OUT" "DRY-RUN PUT repos/gun025bba/eve-web/actions/permissions/fork-pr-contributor-approval"
has "only-fix dry-run: only release-tags among the rulesets" "$(printf '%s\n' "$OUT" | grep -c "ruleset '")" "1"
has "only-fix dry-run: it is release-tags" "$OUT" "ruleset 'release-tags'"
has "only-fix dry-run: M-1b re-check is announced" "$OUT" "M-1b"
[ ! -e "$T/state/gh.log" ] && [ ! -e "$T/state/other.log" ] && [ ! -e "$HOME_T/.eve-web-release-tagger" ] && ok || bad "only-fix dry-run: no tool call, no key dir"

# ---------------- 9. security review of #110 ----------------
# M-1b: a foreign write key that shows up while the owner adds ours (after the first preflight) aborts BEFORE any ruleset is touched
init_state; reset_home; printf 'added\nrevoked\n' >"$T/tty.m1b"
run env FAKE_OPEN_FOREIGN=1 ONESHOT_TTY="$T/tty.m1b" bash "$SD/mac-oneshot.sh" --skip-install
eq "M-1b: a foreign write key that appears during 3b aborts: rc 1" "$RC" 1
has "M-1b: names the foreign key" "$OUT" "FOREIGN WRITE deploy key: id 1002, title 'added-meanwhile'"
has "M-1b: says no ruleset was changed" "$OUT" "NO ruleset change"
eq "M-1b: no ruleset was created or changed" "$(state '[r["name"] for r in st["rulesets"]]')" "['main-protect']"
eq "M-1b: no write call at all" "$(nwrites)" 0
eq "M-1b: the re-check came after the key was registered (key listings: preflight, two in 3b, the strict re-check)" "$(glog | python3 -c 'import sys,json; print(sum(1 for d in map(json.loads, sys.stdin) if d.get("cmd")=="api" and d["ep"].endswith("/keys")))')" 4
has "M-1b: the login is still revoked" "$OUT" "token is dead"
nothing_left && ok || bad "M-1b: no temp dir left"
first_run_aftermath; printf 'added\nrevoked\n' >"$T/tty.m1b"; : >"$T/state/gh.log"
run env FAKE_OPEN_FOREIGN=1 ONESHOT_TTY="$T/tty.m1b" bash "$SD/mac-oneshot.sh" --only-fix
eq "M-1b in --only-fix: rc 1" "$RC" 1; has "M-1b in --only-fix: message" "$OUT" "NO ruleset change"
eq "M-1b in --only-fix: no write call (no ruleset, no Actions setting)" "$(nwrites)" 0
# a write key with OUR title but another public key is also foreign in the strict re-check (it is deleted in 3b first, so the run goes on)
# our key not registered at all (never added): release-tags is applied WITHOUT the DeployKey bypass
init_state; reset_home; printf 'added\nadded\nadded\nrevoked\n' >"$T/tty.three"
run env FAKE_OPEN_NOADD=1 ONESHOT_TTY="$T/tty.three" bash "$SD/mac-oneshot.sh" --skip-install
eq "MANUAL_KEY: release-tags has the admin bypass ONLY" "$(tags_bypass)" "[('RepositoryRole', 5)]"
has "MANUAL_KEY: says so" "$OUT" "WITHOUT the DeployKey bypass"
has "MANUAL_KEY: the table checks release-tags against the no-key shape" "$OUT" "PASS  ruleset release-tags: tag active refs/tags/v* creation bypass=RepositoryRole:5:always"
eq "MANUAL_KEY: the other rulesets are as always" "$(state 'sorted(r["name"] for r in st["rulesets"])')" "['main-protect', 'release-tags', 'release-tags-lock']"
# --only-fix with no key: tightens release-tags; a later successful --only-fix puts the DeployKey bypass back
first_run_aftermath; printf 'added\nadded\nadded\nrevoked\n' >"$T/tty.three"
run env FAKE_OPEN_NOADD=1 ONESHOT_TTY="$T/tty.three" bash "$SD/mac-oneshot.sh" --only-fix
eq "MANUAL_KEY in --only-fix: release-tags tightened to admin only" "$(tags_bypass)" "[('RepositoryRole', 5)]"
run bash "$SD/mac-oneshot.sh" --only-fix
eq "then a successful --only-fix: rc 0" "$RC" 0
eq "then a successful --only-fix: the DeployKey bypass is back" "$(tags_bypass)" "[('DeployKey', None), ('RepositoryRole', 5)]"

# N-1: no xtrace / debug leak; the exact token value is masked in error text
init_state; reset_home
run env SHELLOPTS=xtrace GH_DEBUG=api DEBUG=1 FAKE_TOKEN=opaque-OAUTHTOKEN-9f8e7d bash "$SD/mac-oneshot.sh" --skip-install
eq "N-1: SHELLOPTS=xtrace GH_DEBUG DEBUG in the environment: rc 0" "$RC" 0
hasnt "N-1: the token never reaches stderr/stdout under SHELLOPTS=xtrace" "$OUT" "opaque-OAUTHTOKEN"
hasnt "N-1: ... nor the temp files" "$(ls "$T/tmp" 2>/dev/null; cat "$T/state/other.log")" "opaque-OAUTHTOKEN"
eq "N-1: gh never saw GH_DEBUG, DEBUG or SHELLOPTS" "$(glog | python3 -c 'import sys,json; print(sum(1 for d in map(json.loads, sys.stdin) if d.get("GH_DEBUG") or d.get("DEBUG") or d.get("SHELLOPTS")))')" 0
eq "N-1: the revocation proof used the real token" "$(glog | python3 -c 'import sys,json; print([d["stdin_token_ok"] for d in map(json.loads, sys.stdin) if d["cmd"]=="token-check"])')" "['yes']"
first_run_aftermath; mutate 'st["actions_put_fail"]=True'
run env FAKE_TOKEN=opaque-OAUTHTOKEN-9f8e7d bash "$SD/mac-oneshot.sh" --only-fix
has "N-1: an API error that contains the token prints [redacted]" "$OUT" "API ERROR: PUT repos/gun025bba/eve-web/actions/permissions -> gh: Forbidden (HTTP 403) token [redacted] [redacted] [redacted]"
hasnt "N-1: the literal token value (any format) is masked" "$OUT" "opaque-OAUTHTOKEN"
grep -q 'set +x' "$SCRIPT" && [ "$(grep -n 'set +x' "$SCRIPT" | head -1 | cut -d: -f1)" -lt "$(grep -n '^set -euo pipefail' "$SCRIPT" | head -1 | cut -d: -f1)" ] && ok || bad "N-1: set +x comes before set -euo pipefail"

# N-2: the token cannot be read: the revoke instructions are still shown
init_state; reset_home
run env FAKE_TOKEN_UNREADABLE=1 bash "$SD/mac-oneshot.sh" --skip-install
eq "N-2: unreadable token: rc 1" "$RC" 1; has "N-2: says why" "$OUT" "cannot read the token"
has "N-2: the revoke link is shown" "$OUT" "https://github.com/settings/applications"
has "N-2: the revoke prompt is shown" "$OUT" 'Type "revoked"'
has "N-2: the revocation is reported as unproven" "$OUT" "http=unproven"
eq "N-2: no token check was attempted" "$(glog | grep -c token-check || true)" 0
eq "N-2: nothing was changed on GitHub" "$(nwrites)" 0
nothing_left && ok || bad "N-2: no temp dir left"
# a login that never happened does NOT ask to revoke anything
init_state; reset_home; run env FAKE_LOGIN_FAIL=1 bash "$SD/mac-oneshot.sh" --skip-install
hasnt "N-2: failed login: no revoke prompt" "$OUT" 'Type "revoked"'

# N-3: .pub must match the private key (full run, existing key)
init_state; reset_home; run bash "$SD/mac-oneshot.sh" --skip-install
echo "ssh-ed25519 AAAAOTHERKEYBLOB eve-web-release-tagger" >"$HOME_T/.eve-web-release-tagger/id_ed25519.pub"; : >"$T/state/gh.log"
run bash "$SD/mac-oneshot.sh" --skip-install
eq "N-3: a .pub that is not the public key of the private key: rc 1" "$RC" 1; has "N-3: message" "$OUT" "is not the public key of"
eq "N-3: refused before any GitHub call" "$(glog | grep -c . || true)" 0
eq "N-3: the .pub was not rewritten" "$(cut -d' ' -f2 <"$HOME_T/.eve-web-release-tagger/id_ed25519.pub")" AAAAOTHERKEYBLOB

# N-4: main-protect's bypass_actors and integration_id are verified
first_run_aftermath; mutate "[r for r in st['rulesets'] if r['name']=='main-protect'][0]['bypass_actors']=[{'actor_id':5,'actor_type':'RepositoryRole','bypass_mode':'always'}]"
run bash "$SD/mac-oneshot.sh" --only-fix
eq "N-4: a bypass actor on main-protect: rc 1" "$RC" 1; has "N-4: FAIL row names the bypass" "$OUT" "FAIL  ruleset main-protect"; has "N-4: shows RepositoryRole:5:always" "$OUT" "bypass=RepositoryRole:5:always"
first_run_aftermath; mutate "[r for r in st['rulesets'] if r['name']=='main-protect'][0]['rules'][-1]['parameters']['required_status_checks'][0]['integration_id']=1"
run bash "$SD/mac-oneshot.sh" --only-fix
eq "N-4: a wrong integration_id on main-protect: rc 1" "$RC" 1; has "N-4: FAIL row shows the id" "$OUT" "integration=1 "
first_run_aftermath; mutate "[r for r in st['rulesets'] if r['name']=='main-protect'][0]['rules'][-1]['parameters']['required_status_checks'][0].pop('integration_id')"
run bash "$SD/mac-oneshot.sh" --only-fix
eq "N-4: a missing integration_id (any app may satisfy the check): rc 1" "$RC" 1; has "N-4: FAIL row" "$OUT" "FAIL  ruleset main-protect"

# ---------------- 6. bash 3.2 (when the box has one) ----------------
# README: the clone-free one-liner pins the sha256 of THIS script and a full commit
grep -q "raw.githubusercontent.com/gun025bba/eve-serving/[0-9a-f]\{40\}/deploy/nopasswd/mac-oneshot.sh" "$D/README.md" && ok || bad "README must carry the clone-free mac-oneshot line with a full commit sha"
grep -q "= $(sha256 "$SCRIPT") " "$D/README.md" && ok || bad "README mac-oneshot line must pin the sha256 of the current mac-oneshot.sh"
grep -q 'bash -c "\$s" mac-oneshot' "$D/README.md" && ok || bad "README mac-oneshot line runs the checked text with bash -c"
B32=${EVE_BASH32:-}
if [ -n "$B32" ] && [ -x "$B32" ]; then
  init_state; reset_home; run "$B32" "$SD/mac-oneshot.sh"; eq "bash 3.2 full run rc" "$RC" 0; has "bash 3.2 all checks passed" "$OUT" "ALL CHECKS PASSED"
fi

echo "mac-oneshot tests passed: $PASS  failed: $FAILN"
[ "$FAILN" = 0 ]
