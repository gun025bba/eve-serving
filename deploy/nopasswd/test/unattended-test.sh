#!/bin/bash
# Offline tests of the unattended-deploy pieces: static checks, eve-oneshot.sh (fake gh/curl/sudo), setup-unattended.sh (stub dscl/launchctl/sudo, sandbox dirs) and eve-poll (local HTTP fixture).
# Nothing touches the network, GitHub, sudo, the real home directory or any account. Needs bash, python3, jq (skips with a note otherwise). The tests run as an unprivileged user.
#   bash deploy/nopasswd/test/unattended-test.sh        (also run by test/run.sh)
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
D=$(cd "$HERE/.." && pwd)
REPO_ROOT=$(cd "$D/../.." && pwd)
T=$(cd "$(mktemp -d)" && pwd -P); SRV_PID=""; trap 'rm -rf "$T"; [ -z "$SRV_PID" ] || kill "$SRV_PID" 2>/dev/null' EXIT
PASS=0; FAILN=0
ok()  { PASS=$((PASS + 1)); }
bad() { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()  { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
has() { case "$2" in *"$3"*) ok;; *) bad "$1: missing [$3]";; esac; }
hasnt() { case "$2" in *"$3"*) bad "$1: unexpected [$3]";; *) ok;; esac; }
sha_in() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1; else sha256sum | cut -d' ' -f1; fi; }
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
if ! command -v python3 >/dev/null 2>&1 || ! command -v jq >/dev/null 2>&1; then echo "SKIP unattended tests: python3 and jq are needed"; echo "unattended tests passed: 0 failed: 0 (skipped)"; exit 0; fi
[ "$(id -u)" != 0 ] || { echo "SKIP unattended tests: run as an unprivileged user (the scripts ignore EVE_* knobs as root)"; echo "unattended tests passed: 0 failed: 0 (skipped)"; exit 0; }

OS=$D/eve-oneshot.sh; SU=$D/setup-unattended.sh; POLL=$D/eve-poll; SETT=$D/settings
M=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
const() { sed -n "s/^$1=\([^ ]*\).*/\1/p" "$2" | head -1; }

# ---------------- static ----------------
for f in eve-oneshot.sh setup-unattended.sh eve-poll pin-unattended.sh; do
  [ -x "$D/$f" ] && ok || bad "$f must be executable"
  bash -n "$D/$f" && ok || bad "$f: bash -n"
  case "$(head -1 "$D/$f")" in "#!/bin/bash") ok;; *) bad "$f shebang";; esac
  grep -nE '(^|[^a-z_])(set -x|set -o xtrace)' "$D/$f" | grep -v 'set +x' >/dev/null && bad "$f: xtrace must never be enabled" || ok
  grep -nE 'declare -A|mapfile|readarray|\$\{[a-zA-Z_]+(,,|\^\^)\}|\|&' "$D/$f" >/dev/null && bad "$f: bash 3.2 incompatible construct" || ok
done
if command -v shellcheck >/dev/null 2>&1; then for f in eve-oneshot.sh setup-unattended.sh eve-poll pin-unattended.sh; do shellcheck -s bash "$D/$f" >"$T/sc.out" 2>&1 && ok || { bad "shellcheck $f"; cat "$T/sc.out"; }; done; else echo "note: shellcheck not installed, skipped"; fi
for f in "$SETT"/*.json; do jq -e . "$f" >/dev/null 2>&1 && ok || bad "$(basename "$f") must be valid JSON"; done
# pins
eq "pin: oneshot installer sha256" "$(const PIN_INSTALLER_SHA256 "$OS")" "$(sha256 "$D/setup-deploy-nopasswd.sh")"
eq "pin: oneshot setup-unattended sha256" "$(const PIN_UNATTENDED_SHA256 "$OS")" "$(sha256 "$SU")"
eq "pin: oneshot settings manifest sha256" "$(const PIN_SETTINGS_SHA256 "$OS")" "$(sha256 "$SETT/SHA256SUMS")"
for n in $(sed -n 's/^SETTINGS_FILES="\(.*\)"/\1/p' "$OS"); do eq "SHA256SUMS: $n" "$(awk -v f="$n" '$2==f {print $1}' "$SETT/SHA256SUMS")" "$(sha256 "$SETT/$n")"; done
eq "SHA256SUMS lists exactly the settings files" "$(wc -l <"$SETT/SHA256SUMS" | tr -d ' ')" "$(ls "$SETT"/*.json | wc -l | tr -d ' ')"
eq "pin: setup-unattended SHA_POLL" "$(const SHA_POLL "$SU")" "$(sha256 "$POLL")"
eq "pin: setup-unattended SHA_PLIST" "$(const SHA_PLIST "$SU")" "$(sha256 "$D/launchd/com.llmserving.evepoll.plist.template")"
# no commit sha is pinned anywhere (PR commits do not survive a squash/rebase merge): files are trusted by sha256, the main commit is an argument
eq "pin: setup-unattended pins no commit (--src-commit is an argument)" "$(grep -cE '^SRC_COMMIT=[0-9a-f]{40}' "$SU")" 0
eq "pin: no 40-hex commit in a URL or pin in the shipped scripts and the README" "$(grep -lE '(eve-serving/|COMMIT=)[0-9a-f]{40}' "$OS" "$SU" "$POLL" "$D/mac-oneshot.sh" "$D/setup-deploy-nopasswd.sh" "$D/pin-unattended.sh" "$D/pin-installer.sh" "$D/README.md" | wc -l | tr -d ' ')" 0
eq "pin: installer SHA_DEPLOY = eve-deploy" "$(const SHA_DEPLOY "$D/setup-deploy-nopasswd.sh")" "$(sha256 "$D/eve-deploy")"
eq "pin: installer SHA_FETCH = eve-fetch" "$(const SHA_FETCH "$D/setup-deploy-nopasswd.sh")" "$(sha256 "$D/eve-fetch")"
# nothing that must never appear in the repository's new files
for f in eve-oneshot.sh setup-unattended.sh eve-poll pin-unattended.sh launchd/com.llmserving.evepoll.plist.template settings/* ../../.github/CODEOWNERS ../../docs/runbooks/unattended-deploy.md; do
  [ -f "$D/$f" ] || continue
  grep -nE 'Mac Studio|\.ts\.net|[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}|gh[pousr]_[A-Za-z0-9]{20}|github_pat_[A-Za-z0-9]|BEGIN [A-Z ]*PRIVATE KEY' "$D/$f" | grep -v '127\.0\.0\.1' >/dev/null && bad "$f contains a forbidden string (product name, address, hostname or secret)" || ok
done
# the settings JSON contain the decisions of the design
eq "release-tags: creation only, bypass admin + DeployKey" "$(jq -r '"\([.rules[].type]|join(",")) \([.bypass_actors[].actor_type]|sort|join(","))"' "$SETT/release-tags.json")" "creation DeployKey,RepositoryRole"
eq "release-tags.nokey: no DeployKey" "$(jq -r '[.bypass_actors[].actor_type]|join(",")' "$SETT/release-tags.nokey.json")" "RepositoryRole"
eq "release-tags-lock: update/delete/non_ff, admin only" "$(jq -r '"\([.rules[].type]|sort|join(",")) \([.bypass_actors[].actor_type]|join(","))"' "$SETT/release-tags-lock.json")" "deletion,non_fast_forward,update RepositoryRole"
RBJ=$SETT/release-branches.eve-serving.json
eq "release-branches: targets branch refs/heads/release/* only, active" "$(jq -r '"\(.target) \(.conditions.ref_name.include|join(",")) \(.conditions.ref_name.exclude|length) \(.enforcement)"' "$RBJ")" "branch refs/heads/release/* 0 active"
eq "release-branches: creation, deletion and force-push blocked" "$(jq -r '[.rules[].type]|map(select(.=="creation" or .=="deletion" or .=="non_fast_forward"))|sort|join(",")' "$RBJ")" "creation,deletion,non_fast_forward"
eq "release-branches: only the repository admin role may bypass (always, like release-tags-lock)" "$(jq -r '.bypass_actors|map("\(.actor_type):\(.actor_id):\(.bypass_mode)")|join(",")' "$RBJ")" "RepositoryRole:5:always"
eq "release-branches: PR required, 0 approvals, squash+rebase, code owner off" "$(jq -r '.rules[]|select(.type=="pull_request")|.parameters|"\(.required_approving_review_count) \(.require_code_owner_review) \(.allowed_merge_methods|sort|join(","))"' "$RBJ")" "0 false rebase,squash"
eq "release-branches: required checks = those of main-protect (test, deploy-nopasswd-macos @15368)" "$(jq -r '.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks|map("\(.context)@\(.integration_id)")|sort|join(",")' "$RBJ")" "deploy-nopasswd-macos@15368,test@15368"
eq "release-branches: same checks as main-protect serving" "$(jq -c '[.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks]' "$RBJ")" "$(jq -c '[.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks]' "$SETT/main-protect.eve-serving.json")"
IOSJ=$SETT/main-protect.eve-ios.json
eq "main-protect ios: required check is compile from the Actions app (15368)" "$(jq -r '.rules[]|select(.type=="required_status_checks")|.parameters.required_status_checks|map("\(.context)@\(.integration_id)")|join(",")' "$IOSJ")" "compile@15368"
eq "main-protect ios: no force-push, no deletion, linear history" "$(jq -r '[.rules[].type]|map(select(.=="non_fast_forward" or .=="deletion" or .=="required_linear_history"))|sort|join(",")' "$IOSJ")" "deletion,non_fast_forward,required_linear_history"
eq "main-protect ios: PR required, 0 approvals, squash+rebase, code owner off" "$(jq -r '.rules[]|select(.type=="pull_request")|.parameters|"\(.required_approving_review_count) \(.require_code_owner_review) \(.allowed_merge_methods|sort|join(","))"' "$IOSJ")" "0 false rebase,squash"
eq "main-protect ios: no bypass actors, default branch, active" "$(jq -r '"\(.bypass_actors|length) \(.conditions.ref_name.include|join(",")) \(.enforcement)"' "$IOSJ")" "0 ~DEFAULT_BRANCH active"
eq "main-protect: no bypass actors (serving)" "$(jq -r '.bypass_actors|length' "$SETT/main-protect.eve-serving.json")" 0
# H-1/H-2: at least as strict as the live ruleset; code owner review is NOT enforced (documented), the gate is CI + the security review
eq "main-protect serving (H-2): code owner review not enforced, 0 approvals" "$(jq -r '.rules[]|select(.type=="pull_request")|.parameters|"\(.require_code_owner_review) \(.required_approving_review_count)"' "$SETT/main-protect.eve-serving.json")" "false 0"
eq "main-protect serving (H-1): dismiss stale reviews, resolve threads" "$(jq -r '.rules[]|select(.type=="pull_request")|.parameters|"\(.dismiss_stale_reviews_on_push) \(.required_review_thread_resolution)"' "$SETT/main-protect.eve-serving.json")" "true true"
eq "main-protect serving (H-1): only squash and rebase merges" "$(jq -r '.rules[]|select(.type=="pull_request")|.parameters.allowed_merge_methods|sort|join(",")' "$SETT/main-protect.eve-serving.json")" "rebase,squash"
eq "main-protect serving: linear history is required" "$(jq -r '[.rules[].type]|index("required_linear_history")!=null' "$SETT/main-protect.eve-serving.json")" true
grep -q 'NOT ENFORCED' "$REPO_ROOT/.github/CODEOWNERS" && ok || bad "H-2: CODEOWNERS says it is not enforced"
eq "env deploy: wait timer, admins cannot bypass" "$(jq -r '"\(.wait_timer) \(.can_admins_bypass)"' "$SETT/env-deploy.json")" "20 false"
eq "env deploy-major: a reviewer, admins cannot bypass" "$(jq -r '"\(.reviewers|length) \(.can_admins_bypass)"' "$SETT/env-deploy-major.json")" "1 false"
eq "secret scanning + push protection" "$(jq -r '.security_and_analysis|"\(.secret_scanning.status) \(.secret_scanning_push_protection.status)"' "$SETT/security-and-analysis.json")" "enabled enabled"

# ---------------- eve-oneshot.sh with fakes ----------------
mkdir -p "$T/bin" "$T/fk"
cat >"$T/bin/gh" <<'FAKE'
#!/usr/bin/env python3
# in-memory GitHub for gun025bba/eve-serving and gun025bba/eve-web; state in $FAKE/gh.json, every call logged to $FAKE/gh.log
import json, os, sys, subprocess
S = os.environ["FAKE"]; argv = sys.argv[1:]
def load():
    with open(S + "/gh.json") as f: return json.load(f)
def save(st):
    with open(S + "/gh.json", "w") as f: json.dump(st, f)
def log(o):
    with open(S + "/gh.log", "a") as f: f.write(json.dumps(o) + "\n")
cfg = os.environ.get("GH_CONFIG_DIR", "")
if argv[:2] == ["auth", "login"]:
    log({"cmd": "login", "cfg": cfg, "GH_TOKEN": os.environ.get("GH_TOKEN"), "GITHUB_TOKEN": os.environ.get("GITHUB_TOKEN"), "EVE_TEMP_TOKEN": os.environ.get("EVE_TEMP_TOKEN") and "set"})
    open(cfg + "/hosts.yml", "w").write("github.com: fake\n"); sys.exit(0)
if argv[:2] == ["auth", "token"]: print("gho_FAKEOAUTHTOKEN0123456789"); sys.exit(0)
if argv[:2] == ["auth", "logout"]: log({"cmd": "logout", "cfg": cfg}); sys.exit(0)
if not argv or argv[0] != "api": log({"cmd": "BAD", "args": argv}); sys.exit(2)
method, ep, jq, inp, i, ver = "GET", None, None, False, 1, ""
while i < len(argv):
    a = argv[i]
    if a == "-H": ver = argv[i + 1].split(": ")[-1]; i += 2; continue
    if a == "-X": method = argv[i + 1]; i += 2; continue
    if a == "--input": inp = True; i += 2; continue
    if a == "--jq": jq = argv[i + 1]; i += 2; continue
    ep = a; i += 1
body = json.loads(sys.stdin.read()) if inp else None
path = ep.split("?")[0]
log({"cmd": "api", "method": method, "ep": path, "body": body, "GH_TOKEN": os.environ.get("GH_TOKEN"), "cfg": cfg})
if not os.path.exists(cfg + "/hosts.yml"): print("gh: not logged in", file=sys.stderr); sys.exit(4)
st = load()
def out(obj):
    if jq is None: print(json.dumps(obj)); sys.exit(0)
    p = subprocess.run(["jq", "-r", jq], input=json.dumps(obj), capture_output=True, text=True)
    if p.returncode: print(p.stderr, file=sys.stderr); sys.exit(3)
    sys.stdout.write(p.stdout); sys.exit(0)
def nf(): print("gh: Not Found (HTTP 404)", file=sys.stderr); sys.exit(1)
def done(): save(st); sys.exit(0)
if path == "user": out({"login": st["user"], "id": 4242})
parts = path.split("/")
if parts[0] != "repos" or len(parts) < 3: nf()
R = "/".join(parts[1:3]); rest = parts[3:]; rp = st["repos"][R]
if not rest:
    if method == "GET": out({"permissions": {"admin": st["admin"]}, "security_and_analysis": rp["sa"]})
    if method == "PATCH":
        for k, v in body["security_and_analysis"].items(): rp["sa"][k] = v
        done()
if rest == ["keys"]: out(rp["keys"])
if rest[0] == "rulesets":
    if len(rest) == 1:
        if method == "GET": out([{"id": r["id"], "name": r["name"], "target": r["target"]} for r in rp["rulesets"]])
        if method == "POST" and os.environ.get("FAKE_RB_NOAPPLY") and body["name"] == "release-branches": done()
        if method == "POST" and os.environ.get("FAKE_IOS_NOAPPLY") and R.endswith("eve-ios"): done()
        if method == "POST": st["next"] += 1; body["id"] = st["next"]; rp["rulesets"].append(body); done()
    else:
        rid = int(rest[1]); idx = [k for k, r in enumerate(rp["rulesets"]) if r["id"] == rid]
        if not idx: nf()
        if method == "GET": out(rp["rulesets"][idx[0]])
        if method == "PUT": body["id"] = rid; rp["rulesets"][idx[0]] = body; done()
if rest[0] == "environments":
    e = rest[1]
    if len(rest) == 2:
        if method == "GET":
            b = rp["envs"].get(e)
            if not b: nf()
            pr = []
            if b.get("wait_timer"): pr.append({"type": "wait_timer", "wait_timer": b["wait_timer"]})
            if b.get("reviewers"): pr.append({"type": "required_reviewers", "reviewers": [{"type": r["type"], "reviewer": {"id": r["id"]}} for r in b["reviewers"]]})
            out({"can_admins_bypass": b["can_admins_bypass"], "deployment_branch_policy": b["deployment_branch_policy"], "protection_rules": pr})
        if method == "PUT":
            if os.environ.get("FAKE_ENV_IGNORE"): done()
            rp["envs"][e] = body; rp["pol"].setdefault(e, []); done()
    elif rest[2] == "deployment-branch-policies":
        pol = rp["pol"].setdefault(e, [])
        if len(rest) == 3:
            if method == "GET": out({"total_count": len(pol), "branch_policies": pol})
            if method == "POST": st["next"] += 1; pol.append({"id": st["next"], "name": body["name"], "type": body["type"]}); done()
        elif method == "DELETE":
            rp["pol"][e] = [p for p in pol if p["id"] != int(rest[3])]; done()
if rest == ["immutable-releases"]:
    if method == "GET": out({"enabled": True}) if rp["immutable"] else nf()
    if method == "PUT": rp["immutable"] = True; done()
if rest[0] == "actions" and rest[1] == "permissions":
    sub = "/".join(rest[2:]) or "perm"
    if method == "GET": out(rp["actions"][sub])
    if method == "PUT":
        rp["actions"][sub].update(body); done()
nf()
FAKE
cat >"$T/bin/curl" <<'FAKE'
#!/bin/bash
# fake curl: GitHub public API + raw files + the revocation probe. argv is logged; the Authorization header arrives on stdin (-H @-) and is NEVER logged.
printf '%s\n' "$*" >>"$FAKE/curl.log"
url=""; fmt=""; hdr=""; tok=""
while [ $# -gt 0 ]; do
  case "$1" in
    -H) if [ "$2" = '@-' ]; then hdr=$(cat); tok=${hdr#*Bearer }; tok=${tok%%$'\n'*}; fi; shift 2;;
    -w) fmt=$2; shift 2;; -m|--proto|-o|--max-redirs|--retry|-D) shift 2;;
    -*) shift;; *) url=$1; shift;;
  esac
done
code() { if [ -n "$fmt" ]; then printf '%s' "$1"; fi; }
M=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
case "$url" in
  https://api.github.com/user) if grep -qxF "$tok" "$FAKE/revoked" 2>/dev/null; then code 401; else code 200; fi; exit 0;;
  https://api.github.com/repos/gun025bba/eve-serving/actions/permissions) code "${FAKE_PAT_CODE:-403}"; exit 0;;
  https://api.github.com/repos/gun025bba/eve-serving/commits/$M) echo '{"sha":"'$M'"}';;
  https://api.github.com/repos/gun025bba/eve-serving/compare/$M...main) echo '{"status":"'"${FAKE_COMPARE:-ahead}"'"}';;
  https://api.github.com/repos/gun025bba/eve-serving/commits/$M/pulls) echo '[{"merged_at":"2026-10-29T00:00:00Z","merge_commit_sha":"'"${FAKE_PR_MERGE:-$M}"'","base":{"ref":"main"}}]';;
  https://api.github.com/repos/gun025bba/eve-serving|https://api.github.com/repos/gun025bba/eve-web|https://api.github.com/repos/gun025bba/eve-ios) echo '{"private":'"${FAKE_PRIVATE:-false}"'}';;
  https://raw.githubusercontent.com/gun025bba/eve-serving/$M/deploy/nopasswd/*)
    f=${url#*/deploy/nopasswd/}; cat "$FAKE_WT/$f" || exit 22; [ "${FAKE_TAMPER:-}" != "$f" ] || echo "# tampered";;
  *) echo "fake curl: unexpected url $url" >&2; exit 22;;
esac
FAKE
cat >"$T/bin/sudo" <<'FAKE'
#!/bin/bash
# fake sudo: records its arguments (length and sha of every argument, not the texts) and does NOT run anything
# the hash tool is looked up like the real scripts do (shasum first; macOS has no sha256sum in /usr/bin, and this runs under env -i PATH=<fakes>:/usr/bin:/bin)
shin() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 | cut -d' ' -f1; else sha256sum | cut -d' ' -f1; fi; }
{ echo "ARGC $#"; n=0; for a in "$@"; do n=$((n + 1)); echo "ARG$n $(printf '%s' "$a" | shin) $(printf '%s' "${a:0:40}" | tr '\n' ' ')"; done; } >>"$FAKE/sudo.log"
printf '%s' "$3" >"$FAKE/sudo.session.txt" 2>/dev/null
exit "${FAKE_SUDO_RC:-0}"
FAKE
chmod +x "$T/bin/"*
new_world() { # fresh fake GitHub; $1 = keys mode: ok | foreign | none
  rm -rf "$T/fk"; mkdir -p "$T/fk"; : >"$T/fk/gh.log"; : >"$T/fk/curl.log"; : >"$T/fk/sudo.log"; printf 'gho_FAKEOAUTHTOKEN0123456789\nPAT_FAKE_TEMP_TOKEN_0123456789\n' >"$T/fk/revoked"
  python3 - "$T/fk/gh.json" "${1:-ok}" <<'PY'
import json, sys
mode = sys.argv[2]
def repo(title):
    keys = {"ok": [{"id": 1, "read_only": False, "title": title, "key": "ssh-ed25519 AAAA"}],
            "foreign": [{"id": 1, "read_only": False, "title": title, "key": "k"}, {"id": 2, "read_only": False, "title": "someone-else", "key": "k2"}],
            "none": []}[mode]
    return {"sa": {}, "keys": keys, "rulesets": [], "envs": {}, "pol": {}, "immutable": False,
            "actions": {"perm": {"enabled": True, "allowed_actions": "selected", "sha_pinning_required": False}, "workflow": {"default_workflow_permissions": "write", "can_approve_pull_request_reviews": True}, "fork-pr-contributor-approval": {"approval_policy": "first_time_contributors"}}}
st = {"user": "gun025bba", "admin": True, "next": 100, "repos": {"gun025bba/eve-serving": repo("release-tagger"), "gun025bba/eve-web": repo("eve-web-release-tagger"), "gun025bba/eve-ios": repo("none")}}
json.dump(st, open(sys.argv[1], "w"))
PY
}
run_os() { # args... ; env FAKE_*, TTYIN (lines typed), TEMP (the temporary token)
  printf '%s\n' "${TTYIN:-revoked}" >"$T/tty.in"
  ( cd "$T" && env -i PATH="$T/bin:/usr/bin:/bin" HOME="$T/home" TMPDIR="$T" FAKE="$T/fk" FAKE_WT="$D" ONESHOT_TTY="$T/tty.in" ${TEMP:+EVE_TEMP_TOKEN=$TEMP} \
      ${FAKE_ENV_IGNORE:+FAKE_ENV_IGNORE=1} ${FAKE_IOS_NOAPPLY:+FAKE_IOS_NOAPPLY=1} ${FAKE_RB_NOAPPLY:+FAKE_RB_NOAPPLY=1} ${FAKE_TAMPER:+FAKE_TAMPER=$FAKE_TAMPER} ${FAKE_PAT_CODE:+FAKE_PAT_CODE=$FAKE_PAT_CODE} ${FAKE_COMPARE:+FAKE_COMPARE=$FAKE_COMPARE} ${FAKE_PRIVATE:+FAKE_PRIVATE=$FAKE_PRIVATE} \
      ${FAKE_SUDO_RC:+FAKE_SUDO_RC=$FAKE_SUDO_RC} bash "$OS" "$@" ) >"$T/out" 2>&1
  OSRC=$?
}
mkdir -p "$T/home"
writes() { jq -rs '[.[]|select(.cmd=="api" and .method!="GET")]|length' "$T/fk/gh.log"; }
glog() { jq -rs "$1" "$T/fk/gh.log"; }
sudo_calls() { grep -c '^ARGC' "$T/fk/sudo.log"; }
out=$(cat "$T/out" 2>/dev/null)
export FAKE_UNUSED=1; unset FAKE_UNUSED

# S1: dry-run changes nothing, uses no tool
new_world ok; run_os --dry-run
eq "dry-run: rc" "$OSRC" 0
eq "dry-run: no gh call" "$(wc -l <"$T/fk/gh.log" | tr -d ' ')" 0
eq "dry-run: no curl call" "$(wc -l <"$T/fk/curl.log" | tr -d ' ')" 0
eq "dry-run: no sudo call" "$(sudo_calls)" 0
has "dry-run: shows the sudo session" "$(cat "$T/out")" "su_ rehearse"
has "dry-run: shows the settings" "$(cat "$T/out")" "release-tags-lock"
hasnt "dry-run: no revoke prompt" "$(cat "$T/out")" 'Type "revoked"'
run_os --dry-run --web-deploy 1.10.3 --rehearse web 1.10.4 --unpause-after-pass; eq "dry-run with options: rc" "$OSRC" 0; has "dry-run options reach the sudo line" "$(cat "$T/out")" "1.10.3 web 1.10.4 1"
# argument validation
run_os --merge-commit nothex; eq "bad merge commit: refused" "$OSRC" 1
run_os --dry-run --web-deploy 1.2; eq "bad web version: refused" "$OSRC" 1
run_os --dry-run --unpause-after-pass; eq "--unpause-after-pass without --rehearse: refused" "$OSRC" 1
run_os --dry-run --rehearse mobile 1.2.3; eq "bad rehearse kind: refused" "$OSRC" 1
run_os --dry-run --bogus; eq "unknown argument: refused" "$OSRC" 1

# S2: happy path with both tokens revoked
new_world ok; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M --web-deploy 1.10.3
cat "$T/out" >"$T/out.happy"; [ -z "${UT_DEBUG:-}" ] || cat "$T/out"
eq "happy: rc" "$OSRC" 0
has "happy: all settings passed" "$(cat "$T/out")" "ALL SETTINGS CHECKS PASSED"
has "happy: temporary token refused on an admin endpoint" "$(cat "$T/out")" "no Administration"
has "happy: both tokens proven dead" "$(cat "$T/out")" "both tokens are dead"
hasnt "happy: no FAIL row" "$(cat "$T/out")" "  FAIL  "
eq "happy: exactly one sudo call" "$(sudo_calls)" 1
eq "happy: sudo ARGC (-c session _ SU INST web rk rv up MERGE)" "$(sed -n 's/^ARGC //p' "$T/fk/sudo.log")" 11
eq "happy: the session text passed to sudo -c" "$(cat "$T/fk/sudo.session.txt")" "$(sed -n "/^ROOT_SESSION='/,/su_ verify'/p" "$OS" | sed "1s/^ROOT_SESSION='//; \$s/'\$//")"
eq "happy: arg 4 (setup-unattended text) sha = the file" "$(sed -n 's/^ARG5 \([0-9a-f]*\) .*/\1/p' "$T/fk/sudo.log")" "$(printf '%s' "$(cat "$SU")" | sha_in)"
eq "happy: arg 5 (installer text) sha = the file" "$(sed -n 's/^ARG6 \([0-9a-f]*\) .*/\1/p' "$T/fk/sudo.log")" "$(printf '%s' "$(cat "$D/setup-deploy-nopasswd.sh")" | sha_in)"
eq "happy: the verified merge commit is the last session argument (M-2)" "$(grep '^ARG11 ' "$T/fk/sudo.log" | awk '{print $3}')" "$M"
eq "happy: web version reaches the session" "$(grep -c '^ARG7 .* 1.10.3' "$T/fk/sudo.log")" 1
eq "happy: 4 rulesets on eve-serving, 3 on eve-web, 1 on eve-ios" "$(jq -r '.repos|map(.rulesets|length)|join(",")' "$T/fk/gh.json")" "4,3,1"
eq "happy: env deploy wait timer 20" "$(jq -r '.repos["gun025bba/eve-web"].envs.deploy.wait_timer' "$T/fk/gh.json")" 20
eq "happy: deploy-major reviewer id from the login" "$(jq -r '.repos["gun025bba/eve-serving"].envs["deploy-major"].reviewers[0].id' "$T/fk/gh.json")" 4242
eq "happy: tag policy v* on both envs" "$(jq -r '.repos["gun025bba/eve-web"].pol|map(.[0].name)|join(",")' "$T/fk/gh.json")" "v*,v*"
eq "happy: actions settings applied" "$(jq -r '.repos["gun025bba/eve-serving"].actions|"\(.perm.sha_pinning_required) \(.perm.allowed_actions) \(.workflow.default_workflow_permissions) \(.["fork-pr-contributor-approval"].approval_policy)"' "$T/fk/gh.json")" "true selected read all_external_contributors"
eq "release-branches: the ruleset applied is the pinned file (eve-serving only)" "$(jq -S 'del(.id)' <(jq -c '.repos["gun025bba/eve-serving"].rulesets[]|select(.name=="release-branches")' "$T/fk/gh.json"))" "$(jq -S . "$SETT/release-branches.eve-serving.json")"
eq "release-branches: not created on eve-web or eve-ios" "$(jq -r '[.repos["gun025bba/eve-web"].rulesets[],.repos["gun025bba/eve-ios"].rulesets[]|select(.name=="release-branches")]|length' "$T/fk/gh.json")" 0
has "release-branches: PASS row" "$(cat "$T/out")" "PASS  gun025bba/eve-serving ruleset release-branches"
# eve-ios: ONLY the main-protect ruleset, required check compile (Actions app), verified in the PASS table
eq "ios: the ruleset applied is the pinned file" "$(jq -S 'del(.id)' <(jq -c '.repos["gun025bba/eve-ios"].rulesets[0]' "$T/fk/gh.json"))" "$(jq -S . "$SETT/main-protect.eve-ios.json")"
eq "ios: the only writes on eve-ios are the ruleset" "$(glog '[.[]|select(.cmd=="api" and .method!="GET" and (.ep|startswith("repos/gun025bba/eve-ios")))|.method+" "+.ep]|unique|join(",")')" "POST repos/gun025bba/eve-ios/rulesets"
eq "ios: no environment, key, Actions or security write on eve-ios" "$(glog '[.[]|select(.cmd=="api" and .method!="GET" and (.ep|startswith("repos/gun025bba/eve-ios/") and (.|test("rulesets$")|not)))]|length')" 0
has "ios: PASS row for the ruleset" "$(cat "$T/out")" "PASS  gun025bba/eve-ios ruleset main-protect"
has "ios: PASS row shows the required check" "$(cat "$T/out")" "compile@15368"
# secrets: never in the output, never in an argv, the throwaway dir is gone, login ran with a clean token env
for s in gho_FAKEOAUTHTOKEN0123456789 PAT_FAKE_TEMP_TOKEN_0123456789; do
  hasnt "happy: $s not in the output" "$(cat "$T/out")" "$s"
  hasnt "happy: $s not in any curl argv" "$(cat "$T/fk/curl.log")" "$s"
  hasnt "happy: $s not in the gh log" "$(cat "$T/fk/gh.log")" "$s"
done
eq "happy: login without GH_TOKEN/GITHUB_TOKEN" "$(glog '[.[]|select(.cmd=="login")|(.GH_TOKEN,.GITHUB_TOKEN)]|map(. == null)|all')" true
eq "happy: the temporary token is not in gh's environment" "$(glog '[.[]|select(.cmd=="login")|.EVE_TEMP_TOKEN]|map(. == null)|all')" true
eq "happy: the OAuth login is logged out before sudo and the dir is gone" "$(glog '[.[]|select(.cmd=="logout")]|length')" 1
eq "happy: no gh temp dir left" "$(ls -d "$T"/eve-oneshot-gh.* 2>/dev/null | wc -l | tr -d ' ')" 0
eq "happy: no work dir left" "$(ls -d "$T"/eve-oneshot.* 2>/dev/null | wc -l | tr -d ' ')" 0
eq "happy: the temporary token was never used for a gh call" "$(glog '[.[]|select(.GH_TOKEN != null)]|length')" 0
# every API write targets one of the two repositories only, never with the temp token, and not the PR / contents endpoints
eq "happy: writes only to settings endpoints" "$(glog '[.[]|select(.cmd=="api" and .method!="GET")|.ep]|map(select(test("/(contents|pulls|git|issues)")))|length')" 0

# S14: idempotent (run again on the same fake GitHub)
before=$(jq -S . "$T/fk/gh.json"); : >"$T/fk/gh.log"; : >"$T/fk/sudo.log"
TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "rerun: rc" "$OSRC" 0
eq "rerun: still 4/3/1 rulesets" "$(jq -r '.repos|map(.rulesets|length)|join(",")' "$T/fk/gh.json")" "4,3,1"
eq "rerun: still one policy per environment (none on eve-ios)" "$(jq -r '.repos|map(.pol|map(length)|join(","))|join(";")' "$T/fk/gh.json")" "1,1;1,1;"
eq "rerun: state identical apart from nothing" "$(jq -S 'del(.next)' "$T/fk/gh.json")" "$(printf '%s' "$before" | jq -S 'del(.next)')"
eq "rerun: one more sudo call" "$(sudo_calls)" 1
# --settings-only: no sudo
new_world ok; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M --settings-only; eq "settings-only: rc" "$OSRC" 0; eq "settings-only: no sudo" "$(sudo_calls)" 0
# rehearsal options reach the session
new_world ok; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M --rehearse gateway 5.0.9 --unpause-after-pass
eq "rehearse: rc" "$OSRC" 0; eq "rehearse: args" "$(grep -E '^ARG(7|8|9) ' "$T/fk/sudo.log" | awk '{print $3}' | tr '\n' ' ')" "- gateway 5.0.9 " 2>/dev/null || true
eq "rehearse: unpause flag is the last argument" "$(grep '^ARG10 ' "$T/fk/sudo.log" | awk '{print $3}')" 1

# S3-S6: revocation must be PROVEN, otherwise FAIL
new_world ok; printf 'gho_FAKEOAUTHTOKEN0123456789\n' >"$T/fk/revoked"; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "temp token still valid: rc non-zero" "$((OSRC != 0))" 1; has "temp token still valid: loud warning" "$(cat "$T/out")" "STILL BE VALID"; has "temp token: http=200 reported" "$(cat "$T/out")" "temporary: http=200"
new_world ok; printf 'PAT_FAKE_TEMP_TOKEN_0123456789\n' >"$T/fk/revoked"; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "oauth token still valid: rc non-zero" "$((OSRC != 0))" 1; has "oauth token still valid: loud warning" "$(cat "$T/out")" "STILL BE VALID"
new_world ok; TTYIN=nope TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "'revoked' not typed: rc non-zero" "$((OSRC != 0))" 1; has "'revoked' not typed: warning" "$(cat "$T/out")" "confirmed=no"
new_world ok; run_os --merge-commit $M
eq "no temporary token given (cannot prove): rc non-zero" "$((OSRC != 0))" 1; has "unproven temp token: http=unproven" "$(cat "$T/out")" "temporary: http=unproven"
new_world ok; TTYIN=$(printf 'revoked\nPAT_FAKE_TEMP_TOKEN_0123456789') run_os --merge-commit $M
eq "temporary token pasted at the end (hidden prompt): rc" "$OSRC" 0; hasnt "pasted token is not echoed" "$(cat "$T/out")" PAT_FAKE_TEMP

# S7: a settings FAIL row stops before sudo; the revoke flow still runs
new_world ok; FAKE_ENV_IGNORE=1 TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "settings FAIL: rc non-zero" "$((OSRC != 0))" 1; has "settings FAIL: a FAIL row" "$(cat "$T/out")" "  FAIL  "; eq "settings FAIL: NO sudo" "$(sudo_calls)" 0; has "settings FAIL: says why" "$(cat "$T/out")" "sudo step is NOT run"; has "settings FAIL: tokens still handled" "$(cat "$T/out")" "both tokens are dead"
# S15: no tag key registered -> the table fails (tagging with the key would break)
new_world none; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "no key: rc non-zero" "$((OSRC != 0))" 1; eq "no key: NO sudo" "$(sudo_calls)" 0; has "no key: DeployKey row" "$(cat "$T/out")" "DeployKey bypass present"
eq "no key: release-tags applied without the DeployKey bypass" "$(jq -r '.repos["gun025bba/eve-serving"].rulesets[]|select(.name=="release-tags")|[.bypass_actors[].actor_type]|join(",")' "$T/fk/gh.json")" "RepositoryRole"
# S8: a foreign write deploy key: abort with NO change at all
new_world foreign; TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "foreign key: rc non-zero" "$((OSRC != 0))" 1; eq "foreign key: not a single write call" "$(writes)" 0; eq "foreign key: NO sudo" "$(sudo_calls)" 0; has "foreign key: says so" "$(cat "$T/out")" "NO change"
# S8b: the eve-ios ruleset did not stick -> FAIL row, no sudo
new_world ok; FAKE_IOS_NOAPPLY=1 TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "ios not applied: rc non-zero" "$((OSRC != 0))" 1; has "ios not applied: FAIL row names eve-ios" "$(cat "$T/out")" "FAIL  gun025bba/eve-ios ruleset main-protect"; eq "ios not applied: NO sudo" "$(sudo_calls)" 0
# S8c: not admin of eve-ios: abort with no change at all (the admin check covers it)
new_world ok; python3 - "$T/fk/gh.json" <<'PY'
import json, sys
p = sys.argv[1]; st = json.load(open(p)); st["admin"] = False; json.dump(st, open(p, "w"))
PY
TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "no admin: rc non-zero, nothing written" "$(( (OSRC != 0) && ($(writes) == 0) ))" 1
new_world ok; run_os --dry-run; has "dry-run: names the release-branches ruleset" "$(cat "$T/out")" "create-or-update ruleset 'release-branches' on gun025bba/eve-serving"; has "dry-run: names the eve-ios ruleset" "$(cat "$T/out")" "gun025bba/eve-ios (main-protect only)"; has "dry-run: names the required check" "$(cat "$T/out")" '"context":"compile"'
# S8d: release-branches did not stick on eve-serving -> FAIL row, no sudo
new_world ok; FAKE_RB_NOAPPLY=1 TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "release-branches not applied: rc non-zero" "$((OSRC != 0))" 1; has "release-branches not applied: FAIL row" "$(cat "$T/out")" "FAIL  gun025bba/eve-serving ruleset release-branches"; eq "release-branches not applied: NO sudo" "$(sudo_calls)" 0
# S9: a tampered file at the merge commit
for f in setup-unattended.sh setup-deploy-nopasswd.sh settings/SHA256SUMS settings/release-tags.json settings/main-protect.eve-ios.json settings/release-branches.eve-serving.json; do
  new_world ok; FAKE_TAMPER=$f TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
  eq "tampered $f: rc non-zero" "$((OSRC != 0))" 1; eq "tampered $f: no gh call (no login)" "$(wc -l <"$T/fk/gh.log" | tr -d ' ')" 0; eq "tampered $f: no sudo" "$(sudo_calls)" 0
done
# S10: a temporary token that can read admin settings violates the conditions
new_world ok; FAKE_PAT_CODE=200 TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "admin-capable temp token: rc non-zero" "$((OSRC != 0))" 1; eq "admin-capable temp token: no gh call" "$(wc -l <"$T/fk/gh.log" | tr -d ' ')" 0; has "admin-capable temp token: says why" "$(cat "$T/out")" "violates the conditions"
# S11: the wrong login
new_world ok; python3 - "$T/fk/gh.json" <<'PY'
import json, sys
p = sys.argv[1]; st = json.load(open(p)); st["user"] = "someone-else"; json.dump(st, open(p, "w"))
PY
TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "wrong login: rc non-zero" "$((OSRC != 0))" 1; eq "wrong login: not a single write call" "$(writes)" 0; eq "wrong login: no sudo" "$(sudo_calls)" 0
# S12: merge commit not in main / not a merged PR / private repo
new_world ok; FAKE_COMPARE=diverged TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M; eq "commit not in main: rc non-zero" "$((OSRC != 0))" 1; eq "commit not in main: no gh call" "$(wc -l <"$T/fk/gh.log" | tr -d ' ')" 0
new_world ok; FAKE_PRIVATE=true TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M; eq "private repo: rc non-zero" "$((OSRC != 0))" 1; eq "private repo: no gh call" "$(wc -l <"$T/fk/gh.log" | tr -d ' ')" 0
# S13: a failing sudo session fails the run (tokens still proven)
new_world ok; FAKE_SUDO_RC=7 TEMP=PAT_FAKE_TEMP_TOKEN_0123456789 run_os --merge-commit $M
eq "sudo session failure: rc non-zero" "$((OSRC != 0))" 1; has "sudo session failure: says exit code" "$(cat "$T/out")" "exit 7"; has "sudo session failure: tokens still handled" "$(cat "$T/out")" "both tokens are dead"

# ---------------- setup-unattended.sh in a sandbox (stub dscl / launchctl / sudo / fetch / deploy) ----------------
S=$T/su; mkdir -p "$S/bin" "$S/libexec" "$S/state" "$S/home" "$S/launchd" "$S/sudoers"
cat >"$S/dscl" <<'FAKE'
#!/bin/bash
# fake dscl: files under $SUDB. usage: dscl . -read /Users/n Attr | -create /Users/n [Attr val] | -list /Users UniqueID | -list /Groups PrimaryGroupID
shift; op=$1; shift
case "$op" in
  -read) p=$SUDB$1; [ -d "$p" ] || exit 1; [ -f "$p/$2" ] || exit 1; echo "$2: $(cat "$p/$2")";;
  -create) p=$SUDB$1; mkdir -p "$p"; [ $# -lt 3 ] || echo "$3" >"$p/$2";;
  -list) d=$SUDB$1; a=$2; for n in "$d"/*; do [ -f "$n/$a" ] && echo "$(basename "$n") $(cat "$n/$a")"; done; true;;
esac
FAKE
cat >"$S/launchctl" <<'FAKE'
#!/bin/bash
case "$1" in bootstrap) touch "$SUSTATE_LOADED";; bootout) rm -f "$SUSTATE_LOADED"; exit 0;; print) [ -f "$SUSTATE_LOADED" ];; esac
FAKE
cat >"$S/sudo" <<'FAKE'
#!/bin/bash
# fake sudo for setup-unattended: "sudo -u USER [/usr/bin/sudo -n] CMD args" just runs CMD
[ "$1" = -u ] && shift 2
if [ "$1" = /usr/bin/sudo ]; then shift; [ "$1" = -n ] && shift
  # "sudo -n -l CMD VERB": allowed only if the (fake) sudoers rule names that exact verb
  if [ "$1" = -l ]; then shift; grep -qE "$1 $2(,|\$)" "$SU_SUDOERS_FILE" 2>/dev/null; exit $?; fi
fi
exec "$@"
FAKE
cat >"$S/libexec/fetch" <<'FAKE'
#!/bin/bash
echo "fetch $*" >>"$SULOG"; echo "$2" >"$SUSTATE_STAGED"
FAKE
cat >"$S/libexec/deploy" <<'FAKE'
#!/bin/bash
# stub entrypoint with the brake semantics (the real one is tested in run.sh): PAUSE refuses; version ending .99 = failing release (restore, blocklist, PAUSE)
echo "deploy $*" >>"$SULOG"; st=$SUSTATE; v=$(cat "$SUSTATE_STAGED"); kind=gateway; cf=current_version; [ "$1" != deploy-web ] || { kind=web; cf=current_web_version; }
[ ! -e "$st/PAUSE" ] || { echo "eve-deploy: deploys are PAUSED ($st/PAUSE exists)" >&2; exit 1; }
case "$v" in *.99) echo "kind=$kind version=$v" >"$st/PAUSE"; echo "$kind $v" >>"$st/blocklist"; [ "${SU_STUB_NOLOG:-}" = 1 ] || echo "automatic rollback OK" >>"$SULOG_LOGF"; exit 1;; esac
[ "${SU_STUB_NOHOLD:-}" = 1 ] || true
echo "$v" >"$st/$cf"
FAKE
chmod +x "$S/dscl" "$S/launchctl" "$S/sudo" "$S/libexec/fetch" "$S/libexec/deploy"; cp "$S/libexec/deploy" "$S/libexec/poll"; chmod 755 "$S/libexec/poll"
echo 5.0.2 >"$S/state/current_version"; echo 1.10.2 >"$S/state/current_web_version"; echo evedeploy >"$S/state/allowed_user"
printf 'Defaults:evedeploy env_reset\nevedeploy ALL=(root) NOPASSWD: %s/deploy deploy, %s/deploy rollback, %s/deploy rollback-web\n' "$S/libexec" "$S/libexec" "$S/libexec" >"$S/sudoers/eve-deploy"; : >"$S/logf"
mkdir "$S/udb"; SUDB=$S/udb
mkdir -p "$S/udb/Users/root" && echo 0 >"$S/udb/Users/root/UniqueID" && mkdir -p "$S/udb/Users/_evegw" && echo 451 >"$S/udb/Users/_evegw/UniqueID"
cp "$POLL" "$S/src_poll"; mkdir -p "$S/src/launchd"; cp "$POLL" "$S/src/eve-poll"; cp "$D/launchd/com.llmserving.evepoll.plist.template" "$S/src/com.llmserving.evepoll.plist.template"
ME=$(id -un); MEG=$(id -gn)
# a copy of the script whose pins match the sandbox sources
cp "$SU" "$S/su.sh"; sed -i.bak -e "s/^SHA_POLL=.*/SHA_POLL=$(sha256 "$S/src/eve-poll")/" -e "s/^SHA_PLIST=.*/SHA_PLIST=$(sha256 "$S/src/com.llmserving.evepoll.plist.template")/" "$S/su.sh" && rm -f "$S/su.sh.bak"
su_dbg() { [ -z "${UT_DEBUG:-}" ] || { echo "--- su out (rc $SURC)"; cat "$S/out"; }; }
su_run() { ( env -i SU_STUB_NOLOG="${SU_STUB_NOLOG:-}" SU_SUDOERS_FILE="$S/sudoers/eve-deploy" PATH="$S/bin:/usr/bin:/bin" SUDB="$S/udb" SULOG="$S/log" SULOG_LOGF="$S/logf" SUSTATE="$S/state" SUSTATE_STAGED="$S/staged" SUSTATE_LOADED="$S/loaded" HOME="$S/home" TMPDIR="$T" \
    EVE_SKIP_ROOT=1 EVE_SKIP_OS=1 EVE_LIBEXEC="$S/libexec" EVE_STATE="$S/state" EVE_HOME_BASE="$S/home" EVE_LAUNCHD_DIR="$S/launchd" EVE_LOGF="$S/logf" EVE_OWNER="$ME:$MEG" \
    EVE_DSCL="$S/dscl" EVE_LAUNCHCTL="$S/launchctl" EVE_SUDO="$S/sudo" EVE_SRC_DIR="${SU_SRC_DIR-$S/src}" EVE_SUDOERS_D="$S/sudoers" bash "$S/su.sh" "$@" ) >"$S/out" 2>&1; SURC=$?; }
: >"$S/log"
su_run state; eq "state before accounts: refused" "$SURC" 1; su_run accounts; su_run state; su_dbg; eq "state before the installer's allowed_user matches: ok" "$SURC" 0
rm -f "$S/state/unattended_installed" "$S/state/PAUSE" "$S/launchd"/*.plist "$S/state/floor" "$S/state/floor_web" "$S/state/blocklist"
su_run accounts; eq "accounts: rc" "$SURC" 0
eq "accounts: evedeploy uid in 460-499" "$(awk '$1>=460 && $1<=499' "$S/udb/Users/evedeploy/UniqueID" | wc -l | tr -d ' ')" 1
eq "accounts: evedeploy shell" "$(cat "$S/udb/Users/evedeploy/UserShell")" /usr/bin/false
eq "accounts: evedeploy hidden, password locked" "$(cat "$S/udb/Users/evedeploy/IsHidden")$(cat "$S/udb/Users/evedeploy/Password")" "1*"
eq "accounts: evebackup exists" "$([ -d "$S/udb/Users/evebackup" ] && echo y)" y
eq "accounts: the two uids differ" "$(cat "$S/udb/Users/evedeploy/UniqueID" "$S/udb/Users/evebackup/UniqueID" | sort -u | wc -l | tr -d ' ')" 2
eq "accounts: never 451 or 0" "$(cat "$S/udb/Users/evedeploy/UniqueID" "$S/udb/Users/evebackup/UniqueID" | grep -cE '^(0|451)$')" 0
eq "accounts: home 0700" "$(stat -c %a "$S/home/evedeploy" 2>/dev/null || stat -f %Lp "$S/home/evedeploy")" 700
eq "accounts: poller work dir 0700" "$(stat -c %a "$S/home/evedeploy/poll" 2>/dev/null || stat -f %Lp "$S/home/evedeploy/poll")" 700
u1=$(cat "$S/udb/Users/evedeploy/UniqueID"); su_run accounts; eq "accounts: idempotent rc" "$SURC" 0; eq "accounts: idempotent uid" "$(cat "$S/udb/Users/evedeploy/UniqueID")" "$u1"
mkdir -p "$S/udb/Users/loginuser"; echo 470 >"$S/udb/Users/loginuser/UniqueID"; echo /bin/zsh >"$S/udb/Users/loginuser/UserShell"
# an existing account with a login shell is refused
mv "$S/udb/Users/evedeploy" "$S/evedeploy.keep"; mkdir "$S/udb/Users/evedeploy"; echo 480 >"$S/udb/Users/evedeploy/UniqueID"; echo /bin/zsh >"$S/udb/Users/evedeploy/UserShell"
su_run accounts; eq "accounts: existing account with a login shell refused" "$SURC" 1; rm -rf "$S/udb/Users/evedeploy"; mv "$S/evedeploy.keep" "$S/udb/Users/evedeploy"
# state
su_run state; eq "state: rc" "$SURC" 0
eq "state: PAUSE set" "$([ -f "$S/state/PAUSE" ] && echo y)" y
eq "state: floor = live" "$(cat "$S/state/floor")/$(cat "$S/state/floor_web")" "5.0.2/1.10.2"
eq "state: poller marks seeded with the live versions" "$(cat "$S/home/evedeploy/poll/done_gateway")/$(cat "$S/home/evedeploy/poll/done_web")" "5.0.2/1.10.2"
eq "state: plist rendered (no placeholder left)" "$(grep -c '__' "$S/launchd/com.llmserving.evepoll.plist")" 0
has "state: plist runs the poller as the deploy user" "$(cat "$S/launchd/com.llmserving.evepoll.plist")" "<string>evedeploy</string>"
eq "state: poller installed 0755" "$(stat -c %a "$S/libexec/poll" 2>/dev/null || stat -f %Lp "$S/libexec/poll")" 755
[ ! -f "$S/loaded" ] && ok || bad "state must NOT load the poller"
echo "reason=owner" >"$S/state/PAUSE"; echo 9.9.9 >"$S/state/floor"; su_run state; eq "state: re-run rc" "$SURC" 0; eq "state: re-run keeps PAUSE and the floor" "$(cat "$S/state/PAUSE")/$(cat "$S/state/floor")" "reason=owner/9.9.9"
echo "initial" >"$S/state/PAUSE"; echo 5.0.2 >"$S/state/floor"
# M-1: one private temp dir, nothing derived under /tmp, nothing left; installed files re-verified; the rendered plist's sha is recorded and checked
eq "M-1: setup-unattended makes exactly one temp path (a single mktemp -d call)" "$(grep -v '^ *#' "$SU" | grep -c 'mktemp')" 1
grep -q 'mktemp -d' "$SU" && ok || bad "M-1: mktemp -d"; no_derived=$(grep -cE '\$t\.(poll|plist|out)|\$\{?t\}?\.' "$SU"); eq "M-1: no temp name derived from another (\$t.poll, \$t.out)" "$no_derived" 0
eq "M-1: no temp dir is left behind by the state step" "$(ls "$T" | grep -c '^eve-unatt')" 0
eq "M-1: the rendered plist's sha256 is recorded" "$(cat "$S/state/plist_sha256" 2>/dev/null)" "$(sha256 "$S/launchd/com.llmserving.evepoll.plist")"
su_run verify; has "M-1: verify checks the installed poller" "$(cat "$S/out")" "installed poller matches its sha256 pin"; has "M-1: verify checks the installed plist" "$(cat "$S/out")" "installed plist matches the sha256 recorded"
cp "$S/libexec/poll" "$S/poll.keep"; echo "# tampered" >>"$S/libexec/poll"; su_run verify; eq "M-1: verify fails when the installed poller was changed" "$SURC" 1; has "M-1: says so" "$(cat "$S/out")" "FAIL  installed poller"; cp "$S/poll.keep" "$S/libexec/poll"; rm -f "$S/poll.keep"
cp "$S/launchd/com.llmserving.evepoll.plist" "$S/plist.keep"; echo "<!-- x -->" >>"$S/launchd/com.llmserving.evepoll.plist"; su_run verify; eq "M-1: verify fails when the installed plist was changed" "$SURC" 1; cp "$S/plist.keep" "$S/launchd/com.llmserving.evepoll.plist"; rm -f "$S/plist.keep"
# M-2: the main commit is an argument; required (40-hex) when files are downloaded
SU_SRC_DIR= su_run state; eq "M-2: no --src-commit and no local source: state refused" "$SURC" 1; has "M-2: says what is needed" "$(cat "$S/out")" "--src-commit"
rm -f "$S/state/unattended_installed"; SU_SRC_DIR= su_run --src-commit abc state; eq "M-2: a short --src-commit refused" "$SURC" 1
rm -f "$S/state/unattended_installed"; su_run --src-commit "$M" state; eq "M-2: --src-commit before the subcommand is accepted" "$SURC" 0
# M-3: raise-floor is the only manual floor change, and it only raises
echo 5.0.0 >"$S/state/floor"; echo 5.0.2 >"$S/state/current_version"
su_run raise-floor gateway 5.0.1; eq "M-3: raise-floor raises" "$SURC/$(cat "$S/state/floor")" "0/5.0.1"
su_run raise-floor gateway 5.0.0; eq "M-3: raise-floor refuses a LOWER floor" "$SURC/$(cat "$S/state/floor")" "1/5.0.1"
su_run raise-floor gateway 5.0.1; eq "M-3: raise-floor to the same value is a no-op" "$SURC/$(cat "$S/state/floor")" "0/5.0.1"
su_run raise-floor gateway 5.0.3; eq "M-3: raise-floor refuses a floor above the live version" "$SURC/$(cat "$S/state/floor")" "1/5.0.1"
su_run raise-floor gateway 5.0.x; eq "M-3: raise-floor refuses a malformed version" "$SURC" 1
rm -f "$S/state/floor"; su_run raise-floor gateway 5.0.1; eq "M-3: raise-floor creates a missing floor" "$SURC/$(cat "$S/state/floor")" "0/5.0.1"
echo 1.10.0 >"$S/state/floor_web"; su_run raise-floor web 1.10.1; eq "M-3: raise-floor web" "$SURC/$(cat "$S/state/floor_web")" "0/1.10.1"
su_run raise-floor mobile 1.0.0; eq "M-3: raise-floor refuses an unknown kind" "$SURC" 1
echo 5.0.2 >"$S/state/floor"; echo 1.10.1 >"$S/state/floor_web"
# pins: a wrong sha is refused
cp "$S/src/eve-poll" "$S/src/eve-poll.ok"; echo "# evil" >>"$S/src/eve-poll"; rm -f "$S/state/unattended_installed"; su_run state; eq "state: a poller that does not match its pin is refused" "$SURC" 1; cp "$S/src/eve-poll.ok" "$S/src/eve-poll"; rm -f "$S/src/eve-poll.ok"
rm -f "$S/state/unattended_installed"; su_run state; eq "state: ok again with the right poller" "$SURC" 0
# supervised deploy refuses while PAUSE exists, works without it
rm -f "$S/state/PAUSE" "$S/state/unattended_installed"; : >"$S/log"; su_run deploy web 1.10.3; eq "deploy web: rc" "$SURC" 0; eq "deploy web: live version" "$(cat "$S/state/current_web_version")" 1.10.3
eq "deploy web: fetch then the web verb" "$(tr '\n' '|' <"$S/log")" "fetch web 1.10.3|deploy deploy-web|"
su_run state; echo "reason=owner (by hand)" >"$S/state/PAUSE"; su_run deploy gateway 5.0.3; eq "deploy while PAUSED by the owner: refused" "$SURC" 1
eq "deploy refused: the owner's PAUSE is untouched" "$(cat "$S/state/PAUSE")" "reason=owner (by hand)"
# the install/initial PAUSE is lifted for the supervised deploy only and put back on every exit path
echo "reason=install (x)" >"$S/state/PAUSE"; su_run deploy gateway 5.0.3; eq "deploy under the install PAUSE: rc" "$SURC" 0; eq "install PAUSE: the deploy went through" "$(cat "$S/state/current_version")" 5.0.3
eq "install PAUSE: put back afterwards" "$(head -c 14 "$S/state/PAUSE" 2>/dev/null)" "reason=install"
echo "reason=install (x)" >"$S/state/PAUSE"; su_run deploy gateway 5.0.99; eq "failed supervised deploy: rc 1" "$SURC" 1; [ -e "$S/state/PAUSE" ] && ok || bad "a failed supervised deploy leaves PAUSE in place"
rm -f "$S/state/blocklist"; touch "$S/state/blocklist"; echo "reason=initial" >"$S/state/PAUSE"
su_run deploy gateway 5.0; eq "deploy: bad version refused" "$SURC" 1; su_run deploy mobile 5.0.3; eq "deploy: bad kind refused" "$SURC" 1
# unpause needs a passed rehearsal
su_run unpause; eq "unpause without rehearsal_ok: refused" "$SURC" 1; [ -f "$S/state/PAUSE" ] && ok || bad "PAUSE must stay"
# rehearsal
printf '' >"$S/log"; su_run rehearse gateway 5.0.99; has "rehearse: output" "$(cat "$S/out")" "REHEARSAL"
# the stub restores nothing on failure (live stays), so every check can pass only if the log line exists
rm -f "$S/state/blocklist"; touch "$S/state/blocklist"; echo "automatic rollback OK" >>"$S/logf"; su_run rehearse gateway 5.0.99; eq "rehearse: rc 0 with a failing test release" "$SURC" 0; has "rehearse: PASS" "$(cat "$S/out")" "REHEARSAL PASS"
eq "rehearse: rehearsal_ok written" "$([ -f "$S/state/rehearsal_ok" ] && echo y)" y; eq "rehearse: PAUSE back" "$([ -f "$S/state/PAUSE" ] && echo y)" y
eq "rehearse: blocklisted" "$(grep -cxF 'gateway 5.0.99' "$S/state/blocklist")" 1
su_run unpause; eq "unpause after PASS: rc" "$SURC" 0; eq "unpause: PAUSE gone" "$([ -f "$S/state/PAUSE" ] && echo y || echo n)" n
eq "low: unpause consumes rehearsal_ok" "$([ -f "$S/state/rehearsal_ok" ] && echo y || echo n)" n
# low: the drill also checks both rollback verbs and the poller; a missing verb fails it BEFORE the test release
echo "reason=initial" >"$S/state/PAUSE"; cp "$S/sudoers/eve-deploy" "$S/sudoers.keep"; sed -i.bak 's#, [^ ]*/deploy rollback-web##' "$S/sudoers/eve-deploy"; rm -f "$S/sudoers/eve-deploy.bak"; : >"$S/log"
su_run rehearse gateway 5.0.99; eq "drill: no rollback-web verb for the deploy user: FAIL" "$SURC" 1; has "drill: says which" "$(cat "$S/out")" "rollback-web"
eq "drill FAIL: the test release was never run" "$(grep -c '^deploy' "$S/log")" 0; [ -e "$S/state/PAUSE" ] && ok || bad "drill FAIL: PAUSE stays"
cp "$S/sudoers.keep" "$S/sudoers/eve-deploy"; rm -f "$S/sudoers.keep"
echo "reason=initial" >"$S/state/PAUSE"; rm -f "$S/state/rehearsal_ok"; : >"$S/logf"
SU_STUB_NOLOG=1 su_run rehearse gateway 5.0.99; eq "rehearse: FAIL when the log shows no automatic restore" "$SURC" 1; eq "rehearse FAIL: no rehearsal_ok" "$([ -f "$S/state/rehearsal_ok" ] && echo y || echo n)" n; eq "rehearse FAIL: PAUSE stays" "$([ -f "$S/state/PAUSE" ] && echo y)" y
rm -f "$S/state/blocklist"; touch "$S/state/blocklist"
echo "automatic rollback OK" >>"$S/logf"; su_run rehearse gateway 5.0.5; eq "rehearse: a release that DEPLOYS fine is a FAIL (it must fail its health check)" "$SURC" 1; eq "rehearse FAIL: PAUSE recreated by the safety trap" "$([ -f "$S/state/PAUSE" ] && echo y)" y
rm -f "$S/state/rehearsal_ok"
su_run load; eq "load: rc" "$SURC" 0; eq "load: bootstrapped" "$([ -f "$S/loaded" ] && echo y)" y; su_run load; eq "load: idempotent" "$SURC" 0
su_run verify; eq "verify: rc" "$SURC" 0; has "verify: INFO about PAUSE" "$(cat "$S/out")" "PAUSED"; hasnt "verify: no FAIL row" "$(cat "$S/out")" "  FAIL  "
rm -f "$S/loaded"; su_run verify; eq "verify: not loaded is a FAIL" "$SURC" 1; has "verify: says poller loaded" "$(cat "$S/out")" "poller loaded"
su_run bogus; eq "unknown subcommand refused" "$SURC" 1
# EVE_* knobs are ignored for root: checked statically (the script unsets them when uid is 0)
grep -q 'unset "\$_v"' "$SU" && ok || bad "setup-unattended must unset EVE_* when root"

# ---------------- eve-poll against a local HTTP fixture ----------------
P=$T/poll; mkdir -p "$P/www" "$P/work" "$P/libexec"
cat >"$P/srv.py" <<'PY'
import http.server, os, sys
W = sys.argv[1]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        f = os.path.join(W, self.path.strip("/").replace("/", "_"))
        if self.path.endswith("/health"): body = open(os.path.join(W, "health")).read() if os.path.exists(os.path.join(W, "health")) else '{"status":"ok"}'; self.send_response(200); self.end_headers(); self.wfile.write(body.encode()); return
        if os.path.exists(f):
            code, loc = open(f).read().strip().split(" ", 1)
            self.send_response(int(code)); self.send_header("Location", loc); self.end_headers(); return
        self.send_response(404); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", 0), H).serve_forever() if False else None
s = http.server.HTTPServer(("127.0.0.1", 0), H); open(os.path.join(W, "port"), "w").write(str(s.server_port)); s.serve_forever()
PY
python3 "$P/srv.py" "$P/www" & SRV_PID=$!
for i in 1 2 3 4 5 6 7 8 9 10; do [ -f "$P/www/port" ] && break; sleep 0.3; done
PORT=$(cat "$P/www/port"); BASE=http://127.0.0.1:$PORT
cat >"$P/libexec/fetch" <<'FAKE'
#!/bin/bash
echo "fetch $*" >>"$PLOG"; [ "${PFETCH_FAIL:-}" != 1 ] || exit 1
FAKE
cat >"$P/libexec/deploy" <<'FAKE'
#!/bin/bash
echo "deploy $*" >>"$PLOG"
if [ "${1:-}" = rollback ] && [ "${PROLL_FAIL:-}" = 1 ]; then echo "eve-deploy: rollback refused" >&2; exit 1; fi
if [ -n "${PDEPLOY_MSG:-}" ]; then echo "$PDEPLOY_MSG" >&2; exit 1; fi
FAKE
cat >"$P/sudo" <<'FAKE'
#!/bin/bash
[ "$1" = -n ] && shift; exec "$@"
FAKE
chmod +x "$P/libexec/"* "$P/sudo"
loc() { printf '%s %s\n' "$2" "$3" >"$P/www/gun025bba_$1_releases_latest"; }   # loc serving 302 URL
poll_run() { ( env -i PATH=/usr/bin:/bin EVE_POLL_TESTING=1 EVE_POLL_LIBEXEC="$P/libexec" EVE_POLL_WORK="$P/work" EVE_POLL_BASE="$BASE" EVE_POLL_PROTO==http EVE_POLL_ALLOW_HOSTS="127.0.0.1:$PORT" EVE_POLL_SUDO="$P/sudo" \
    EVE_POLL_HEALTH_URL="$BASE/health" EVE_POLL_UI_URL="$BASE/health" EVE_POLL_OBSERVE_N=3 EVE_POLL_OBSERVE_GAP=0 EVE_POLL_BACKOFF=${PB:-300} PLOG="$P/log" ${PFETCH_FAIL:+PFETCH_FAIL=1} ${PDEPLOY_MSG:+PDEPLOY_MSG="$PDEPLOY_MSG"} ${PROLL_FAIL:+PROLL_FAIL=1} HOME="$P" TMPDIR="$T" bash "$POLL" ) >"$P/out" 2>&1; PRC=$?; }
reset_poll() { rm -rf "$P/work" "$P/log" "$P/www/gun025bba_"*; mkdir -p "$P/work"; : >"$P/log"; rm -f "$P/www/health"; }
SERV_PREFIX=$BASE/gun025bba/eve-serving/releases/tag/v
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"
poll_run; eq "poll: rc" "$PRC" 0
eq "poll: fetch then deploy (gateway)" "$(tr '\n' '|' <"$P/log")" "fetch gateway 5.0.3|deploy deploy|"
eq "poll: done mark" "$(cat "$P/work/done_gateway")" 5.0.3
eq "poll: lock released" "$([ -d "$P/work/lock" ] && echo held || echo free)" free
: >"$P/log"; poll_run; eq "poll: nothing new -> nothing happens" "$(wc -l <"$P/log" | tr -d ' ')" 0
# the web release is looked at too
loc eve-web 302 "$BASE/gun025bba/eve-web/releases/tag/v1.10.3"; : >"$P/log"; poll_run; eq "poll: web release deployed with the web verb" "$(tr '\n' '|' <"$P/log")" "fetch web 1.10.3|deploy deploy-web|"
# hostile or odd Location headers are all ignored
for l in "https://evil.example/gun025bba/eve-serving/releases/tag/v5.0.4" "${SERV_PREFIX}5.0.4-rc1" "${SERV_PREFIX}05.0.4" "${SERV_PREFIX}5.0.4/../x" "${SERV_PREFIX}5.0" "${SERV_PREFIX}5.0.4%0d%0a" "$BASE/gun025bba/eve-web/releases/tag/v5.0.4" "${SERV_PREFIX}9999999999.0.0" "/gun025bba/eve-serving/releases/tag/v5.0.4" "${SERV_PREFIX}v5.0.4" "${SERV_PREFIX}5.0.4?x=1"; do
  reset_poll; loc eve-serving 302 "$l"; poll_run; eq "poll: ignores [$l]" "$(wc -l <"$P/log" | tr -d ' ')" 0
done
reset_poll; loc eve-serving 200 "${SERV_PREFIX}5.0.4"; poll_run; eq "poll: a 200 (not a redirect) is ignored" "$(wc -l <"$P/log" | tr -d ' ')" 0
reset_poll; loc eve-serving 307 "${SERV_PREFIX}5.0.4"; poll_run; eq "poll: a 307 is ignored" "$(wc -l <"$P/log" | tr -d ' ')" 0
reset_poll; poll_run; eq "poll: no release at all (404) is fine" "$PRC" 0
# the allow-list: the same Location with another host is ignored even though the path is right
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.4"; ( env -i PATH=/usr/bin:/bin EVE_POLL_TESTING=1 EVE_POLL_LIBEXEC="$P/libexec" EVE_POLL_WORK="$P/work" EVE_POLL_BASE="$BASE" EVE_POLL_PROTO==http EVE_POLL_ALLOW_HOSTS="github.com" EVE_POLL_SUDO="$P/sudo" PLOG="$P/log" bash "$POLL" ) >/dev/null 2>&1
eq "poll: host not on the allow list -> nothing" "$(wc -l <"$P/log" | tr -d ' ')" 0
# protocol: with the production default only https is allowed, so a plain-http base is refused by curl itself
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.4"; ( env -i PATH=/usr/bin:/bin EVE_POLL_TESTING=1 EVE_POLL_LIBEXEC="$P/libexec" EVE_POLL_WORK="$P/work" EVE_POLL_BASE="$BASE" EVE_POLL_ALLOW_HOSTS="127.0.0.1:$PORT" EVE_POLL_SUDO="$P/sudo" PLOG="$P/log" bash "$POLL" ) >/dev/null 2>&1
eq "poll: --proto =https refuses an http URL" "$(wc -l <"$P/log" | tr -d ' ')" 0
# PAUSED is not counted and does not re-fetch
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; PDEPLOY_MSG="eve-deploy: deploys are PAUSED (/x/PAUSE exists)" poll_run
eq "poll PAUSED: fetched and tried once" "$(tr '\n' '|' <"$P/log")" "fetch gateway 5.0.3|deploy deploy|"
eq "poll PAUSED: not counted as a failure" "$(ls "$P/work" | grep -c '^try_')" 0; eq "poll PAUSED: staged" "$(cat "$P/work/staged_gateway")" 5.0.3
: >"$P/log"; poll_run; eq "poll PAUSE lifted: no second fetch, deploys" "$(tr '\n' '|' <"$P/log")" "deploy deploy|"; eq "poll PAUSE lifted: done" "$(cat "$P/work/done_gateway")" 5.0.3
# other refusals are counted, at most 3 tries (backoff 0 here)
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"
for i in 1 2 3 4 5; do PB=0 PDEPLOY_MSG="eve-deploy: signature verification FAILED" poll_run; done
eq "poll: max 3 attempts per version" "$(grep -c '^deploy' "$P/log")" 3; eq "poll: attempt counter" "$(cut -d' ' -f1 "$P/work/try_gateway_5.0.3")" 3
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; PDEPLOY_MSG="eve-deploy: refused" poll_run; PDEPLOY_MSG="eve-deploy: refused" poll_run
eq "poll: backoff (300s) stops an immediate retry" "$(grep -c '^deploy' "$P/log")" 1
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; PFETCH_FAIL=1 PB=0 poll_run; eq "poll: a failed fetch is counted and does not deploy" "$(grep -c '^deploy' "$P/log")/$(cut -d' ' -f1 "$P/work/try_gateway_5.0.3")" "0/1"
# lock
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; mkdir "$P/work/lock"; poll_run; eq "poll: a held lock stops the run" "$(wc -l <"$P/log" | tr -d ' ')" 0; eq "poll: the other run's lock is not removed" "$([ -d "$P/work/lock" ] && echo held)" held
touch -t 202001010000 "$P/work/lock"; poll_run; eq "poll: a stale lock (>30 min) is replaced" "$(grep -c '^deploy' "$P/log")" 1
# observation: 3 failed probes in a row call rollback
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; echo bad >"$P/www/health"; poll_run
eq "poll observe: unhealthy after deploy -> rollback verb" "$(tr '\n' '|' <"$P/log")" "fetch gateway 5.0.3|deploy deploy|deploy rollback|"
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; poll_run; eq "poll observe: healthy -> no rollback" "$(grep -c 'rollback' "$P/log")" 0
# M-4: if root could not even set PAUSE after a failed release, or the rollback itself fails, the poller halts itself and does not deploy again
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; PB=0 PDEPLOY_MSG="eve-deploy: HOLD-LATCH-FAILED could not pause" poll_run
[ -e "$P/work/halted" ] && ok || bad "M-4: HOLD-LATCH-FAILED in the deploy output halts the poller"
: >"$P/log"; loc eve-serving 302 "${SERV_PREFIX}5.0.4"; PB=0 poll_run; eq "M-4: halted poller neither fetches nor deploys" "$(wc -l <"$P/log" | tr -d ' ')" 0
has "M-4: halted poller says why" "$(cat "$P/work/poll.log")" "halted"
rm -f "$P/work/halted"; PB=0 poll_run; eq "M-4: after the owner removes halted the poller works again" "$(grep -c '^deploy' "$P/log")" 1
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; echo bad >"$P/www/health"; PROLL_FAIL=1 poll_run
[ -e "$P/work/halted" ] && ok || bad "M-4: a failed rollback halts the poller"
: >"$P/log"; poll_run; eq "M-4: halted after a failed rollback: nothing runs" "$(wc -l <"$P/log" | tr -d ' ')" 0
# low: the EVE_POLL_* knobs are ignored unless EVE_POLL_TESTING=1 (the installed poller always uses the fixed production values)
reset_poll; loc eve-serving 302 "${SERV_PREFIX}5.0.3"; ( env -i PATH=/usr/bin:/bin EVE_POLL_LIBEXEC="$P/libexec" EVE_POLL_WORK="$P/work" EVE_POLL_BASE="$BASE" EVE_POLL_PROTO==http EVE_POLL_ALLOW_HOSTS="127.0.0.1:$PORT" EVE_POLL_SUDO="$P/sudo" PLOG="$P/log" HOME="$P" bash "$POLL" ) >/dev/null 2>&1
eq "knobs: EVE_POLL_* without EVE_POLL_TESTING=1 are ignored (nothing fetched, nothing deployed)" "$(wc -l <"$P/log" | tr -d ' ')" 0
eq "knobs: the plist never sets EVE_POLL_TESTING" "$(grep -c 'EVE_POLL' "$D/launchd/com.llmserving.evepoll.plist.template")" 0
# files: the work dir only; nothing outside
[ -z "${UT_DEBUG:-}" ] || find "$P" -newer "$P/srv.py" -type f -not -path "$P/work/*" -not -path "$P/www/*" -not -path "$P/libexec/*" -not -name sudo -not -name log -not -name out
eq "poll: no file outside the work dir" "$(find "$P" -newer "$P/srv.py" -type f -not -path "$P/work/*" -not -path "$P/www/*" -not -path "$P/libexec/*" -not -name sudo -not -name log -not -name out | wc -l | tr -d ' ')" 0
# the poller never runs as root, never touches a secret
grep -q 'run as the deploy user, not root' "$POLL" && ok || bad "poller must refuse root"
grep -nEi 'token|password|\.ssh|id_ed25519' "$POLL" | grep -v '^[0-9]*:#' >/dev/null && bad "poller must not mention tokens, passwords or keys in code" || ok
grep -q -- "--max-redirs 0" "$POLL" && ok || bad "poller: --max-redirs 0"; grep -q -- '--proto "\$PROTO"' "$POLL" && ok || bad "poller: --proto"
eq "poller default protocol is =https" "$(sed -n 's/^PROTO=\${EVE_POLL_PROTO:-\([^}]*\)}.*/\1/p' "$POLL")" "=https"
# the plist: valid XML (plutil on macOS), no secrets
if command -v python3 >/dev/null 2>&1; then python3 -c "import plistlib,sys; plistlib.loads(open(sys.argv[1],'rb').read().replace(b'__USER__',b'u').replace(b'__LIBEXEC__',b'/l').replace(b'__WORK__',b'/w'))" "$D/launchd/com.llmserving.evepoll.plist.template" && ok || bad "plist template is not valid"; fi

echo "unattended tests passed: $PASS  failed: $FAILN"
[ "$FAILN" = 0 ]
