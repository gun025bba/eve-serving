#!/bin/bash
# Offline tests for deploy/nopasswd/release-tag.sh. No network, no key, no GitHub: `gh`, `git` (and `ssh`) are fakes on PATH that log every call and
# answer from fixtures; one last block uses the REAL git against a local bare repository (file transport) with a fake `gh`.
#   bash deploy/nopasswd/test/release-tag-test.sh          (also run by test/run.sh, so CI runs it on Ubuntu and on macOS with bash 3.2)
# Needs: bash, git, jq, coreutils/BSD tools.
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
D=$(cd "$HERE/.." && pwd)
REALSCRIPT=$D/release-tag.sh
T=$(cd "$(mktemp -d)" && pwd -P); trap 'rm -rf "$T"' EXIT
PASS=0; FAILN=0
ok()  { PASS=$((PASS + 1)); }
bad() { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()  { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
REALGIT=$(command -v git); REALBASH=$(command -v bash)
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is required for the release-tag tests"; exit 1; }

SHA=1111111111111111111111111111111111111111
OLD=2222222222222222222222222222222222222222
KEY=$T/keys/id_ed25519
SECRET=FAKE-PRIVATE-KEY-MARKER-0f9a
mkdir -p "$T/keys" "$T/bin" "$T/fix"; chmod 700 "$T/keys"
# The script has its repository, remote and reviewer as constants (no environment override), so the tests run patched COPIES of it: the copy differs from the
# original only in the REVIEWER block (and, for the real-git block, the REMOTE line). The unpatched script is tested as it ships (fails closed) below.
KH=$D/github_known_hosts
mkcopy() { # mkcopy dir login id [remote-line]
  mkdir -p "$1"; cp "$KH" "$1/github_known_hosts"
  sed -e "s/^REVIEWER_LOGIN=.*/REVIEWER_LOGIN=\"$2\"/" -e "s/^REVIEWER_ID=.*/REVIEWER_ID=\"$3\"/" ${4:+-e "s|^REMOTE=.*|REMOTE=\"$4\"|"} "$REALSCRIPT" >"$1/release-tag.sh"
}
mkcopy "$T/rt" sec-bot 900
SCRIPT=$T/rt/release-tag.sh
printf '%s\n' "$SECRET" >"$KEY"; chmod 600 "$KEY"

# ---------------- fakes ----------------
cat >"$T/bin/gh" <<'FAKE'
#!/bin/bash
# fake gh: only `gh api [--paginate] <path>` (a GET) is accepted; answers from $FAKE_FIX/<path with non-alphanumerics replaced by _>
echo "gh $*" >>"$FAKE_LOG"
[ "${1:-}" = api ] || { echo "BAD gh: $*" >>"$FAKE_LOG"; exit 2; }
shift; [ "${1:-}" = --paginate ] && shift
[ $# = 1 ] || { echo "BAD gh: extra args" >>"$FAKE_LOG"; exit 2; }
f=$FAKE_FIX/$(printf '%s' "$1" | tr -c 'A-Za-z0-9' _)
[ -f "$f" ] || { echo "gh: HTTP 404" >&2; exit 1; }
cat "$f"
FAKE
cat >"$T/bin/git" <<'FAKE'
#!/bin/bash
# fake git: logs the arguments and the ssh command it was given; answers ls-remote from FAKE_MAIN_1/FAKE_MAIN_2 (first call / later calls) and FAKE_TAGS
printf 'git %s | SSH=%s\n' "$*" "${GIT_SSH_COMMAND:-}" >>"$FAKE_LOG"
while [ "${1:-}" = -c ]; do shift 2; done
cmd=${1:-}; shift
case "$cmd" in
  rev-parse)
    case "$*" in
      *--git-dir*) echo .git ;;
      *"refs/tags/"*) [ "${FAKE_LOCAL_TAG:-0}" = 1 ] && { echo deadbeef; exit 0; }; exit 1 ;;
      *) exit 1 ;;
    esac ;;
  ls-remote)
    case "$*" in
      *--tags*) [ -n "${FAKE_TAGS:-}" ] && printf '%b' "$FAKE_TAGS"; exit "${FAKE_LSTAGS_RC:-0}" ;;
      *refs/heads/main*)
        n=$(cat "$FAKE_LOG.n" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" >"$FAKE_LOG.n"
        tip=$FAKE_MAIN_1; [ "$n" -gt 1 ] && tip=${FAKE_MAIN_2:-$FAKE_MAIN_1}
        [ "${FAKE_LSMAIN_RC:-0}" = 0 ] || exit "$FAKE_LSMAIN_RC"
        [ -n "$tip" ] && printf '%s\trefs/heads/main\n' "$tip"; exit 0 ;;
    esac ;;
  config) exit "${FAKE_URLREWRITE:-1}" ;;
  cat-file) [ "${FAKE_NO_OBJECT:-0}" = 1 ] && [ ! -f "$FAKE_LOG.fetched" ] && exit 1; exit 0 ;;
  fetch) : >"$FAKE_LOG.fetched"; exit 0 ;;
  tag) exit 0 ;;
  push) exit "${FAKE_PUSH_RC:-0}" ;;
  *) echo "fake git: unexpected command $cmd" >&2; exit 99 ;;
esac
FAKE
printf '#!/bin/bash\necho "fake ssh must never run" >&2; exit 97\n' >"$T/bin/ssh"
chmod 755 "$T/bin/gh" "$T/bin/git" "$T/bin/ssh"
# a PATH without ssh: the tools the script needs, plus the fakes
mkdir -p "$T/nossh"
for t in gh git; do ln -s "$T/bin/$t" "$T/nossh/$t"; done
for t in grep head stat id jq sort tr cat dirname mktemp sed cp; do p=$(command -v "$t") && ln -s "$p" "$T/nossh/$t"; done

# ---------------- fixtures ----------------
fx() { printf '%s' "$1" | tr -c 'A-Za-z0-9' _; }
put() { printf '%s\n' "$2" >"$T/fix/$(fx "$1")"; }
R=repos/gun025bba/eve-serving
reset_fixtures() {
  rm -f "$T/fix"/* "$T/log" "$T/log.n" "$T/log.fetched"
  put "$R/commits/$SHA/pulls" '[{"number":7}]'
  put "user" '{"login":"mac-user","id":300}'
  put "$R/commits/$SHA" '{"author":{"login":"coder","id":100},"committer":{"login":"web-flow","id":200}}'
  put "$R/pulls/7" '{"number":7,"user":{"login":"coder","id":100}}'
  put "$R/pulls/7/commits" '[{"author":{"login":"coder","id":100},"committer":{"login":"coder","id":100}},{"author":null,"committer":{"login":"web-flow","id":200}}]'
  put "$R/pulls/7/reviews" "[$(rv 1 sec-bot 900 COMMENTED "looks good\nSECURITY-APPROVED: $SHA")]"
  put "$R/commits/$SHA/check-runs?per_page=100" "$(checks '{"id":10,"name":"test","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}' '{"id":11,"name":"deploy-nopasswd-macos","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}')"
}
rv() { # rv id login userid state body [submitted_at]: one review object (body: \n escapes allowed)
  printf '{"id":%s,"user":{"login":"%s","id":%s},"state":"%s","submitted_at":"%s","body":"%s"}' "$1" "$2" "$3" "$4" "${6:-2026-10-29T01:00:00Z}" "$5"
}
checks() { local a; a=$(printf '%s,' "$@"); printf '{"total_count":%s,"check_runs":[%s]}' "$#" "${a%,}"; }
run() { # run [VAR=val ...] -- args ; sets RC, OUT (stdout+stderr)
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done; shift
  OUT=$(cd "$WORK" && env -i HOME="$T/home" PATH="${USE_PATH:-$T/bin:/usr/bin:/bin}" FAKE_LOG="$T/log" FAKE_FIX="$T/fix" FAKE_MAIN_1="$SHA" FAKE_TAGS="" \
        EVE_RELEASE_TAG_KEY="$KEY" ${envs[@]+"${envs[@]}"} "$REALBASH" "${USE_SCRIPT:-$SCRIPT}" "$@" 2>&1); RC=$?
}
pushed() { if [ -f "$T/log" ]; then grep -c '^git push' "$T/log" || true; else echo 0; fi; }
tagged() { if [ -f "$T/log" ]; then grep -c '^git .*tag -a' "$T/log" || true; else echo 0; fi; }
refuses() { # name, then run args
  local n=$1; shift
  run "$@"; eq "$n: refused (exit 1)" "$RC" 1
  eq "$n: no tag created" "$(tagged | tr -d ' ')" 0; eq "$n: nothing pushed" "$(pushed | tr -d ' ')" 0
}
WORK=$T/work; mkdir -p "$WORK" "$T/home"
cd "$WORK" && "$REALGIT" init -q . 2>/dev/null; cd "$HERE"
: >"$T/all.log"
collect() { [ -f "$T/log" ] && cat "$T/log" >>"$T/all.log"; }

# ---------------- happy path ----------------
reset_fixtures
run FAKE_TAGS="aaa\trefs/tags/v5.0.2\n" -- 5.1.0
eq "happy: exit 0" "$RC" 0
eq "happy: one push" "$(pushed | tr -d ' ')" 1
eq "happy: one annotated tag" "$(tagged | tr -d ' ')" 1
grep -q "^git push --no-verify git@github.com:gun025bba/eve-serving.git refs/tags/v5.1.0:refs/tags/v5.1.0 | " "$T/log" && ok || bad "happy: push line is exactly the tag refspec ($(grep '^git push' "$T/log"))"
grep -q "^git .*tag -a -m Release v5.1.0 v5.1.0 $SHA" "$T/log" && ok || bad "happy: annotated tag on the target sha"
eq "happy: every git call after the checks uses exactly the deploy-key ssh command" "$(grep '^git ' "$T/log" | grep -vc -e '--git-dir' -e '^git config')" "$(grep -c "| SSH=ssh -F /dev/null -i $KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=$T/rt/github_known_hosts -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes -o IdentityAgent=none\$" "$T/log")"
grep -q "$SECRET" "$T/log" && bad "happy: key content in the call log" || ok
case "$OUT" in *"$SECRET"*) bad "happy: key content printed";; *) ok;; esac
eq "happy: gh was only called as 'api [--paginate] <path>'" "$(grep -c '^gh ' "$T/log")" "$(grep -cE '^gh api( --paginate)? [^ ]+$' "$T/log")"
grep -q BAD "$T/log" && bad "happy: gh/git misuse logged" || ok
grep -qE 'gh .* (-X|--method|-f|-F|--field|--input)' "$T/log" && bad "happy: gh write flags used" || ok
collect
reset_fixtures; run -- v5.1.0 "$SHA"; eq "happy: a leading v and an explicit sha are accepted" "$RC" 0; collect
reset_fixtures; run -- --dry-run 5.1.0; eq "dry-run: exit 0 with everything checked" "$RC" 0
eq "dry-run: nothing pushed" "$(pushed | tr -d ' ')" 0; eq "dry-run: no tag" "$(tagged | tr -d ' ')" 0; collect

# ---------------- target commit ----------------
reset_fixtures; refuses "sha is not the main tip" -- 5.1.0 "$OLD"; collect
reset_fixtures; refuses "short sha" -- 5.1.0 1111111; collect
reset_fixtures; refuses "uppercase sha" -- 5.1.0 "$(printf '%s' "$SHA" | tr 1 A)"; collect
reset_fixtures; refuses "main moved after the checks (race, second ls-remote)" FAKE_MAIN_2="$OLD" -- 5.1.0; collect
reset_fixtures; refuses "ls-remote fails" FAKE_LSMAIN_RC=128 -- 5.1.0; collect
reset_fixtures; refuses "ls-remote answers nothing" FAKE_MAIN_1="" -- 5.1.0 "$SHA"; collect

# ---------------- security review ----------------
APP="SECURITY-APPROVED: $SHA"; BLK="SECURITY-BLOCKED: $SHA"
rvs() { put "$R/pulls/7/reviews" "[$(IFS=,; echo "$*")]"; }
reset_fixtures; put "$R/commits/$SHA/pulls" '[]'; refuses "no pull request for the commit" -- 5.1.0; collect
# the shipped script: the reviewer identity is a placeholder, so it fails closed whatever the environment says
reset_fixtures; USE_SCRIPT=$REALSCRIPT; refuses "shipped script: reviewer placeholder fails closed" -- 5.1.0
case "$OUT" in *"TODO reviewer decision"*) ok;; *) bad "placeholder: message names the TODO ($OUT)";; esac
refuses "shipped script: env EVE_SECURITY_REVIEWER does not set the reviewer" EVE_SECURITY_REVIEWER=sec-bot -- 5.1.0; unset USE_SCRIPT; collect
grep -q '^REVIEWER_LOGIN="__TODO_REVIEWER_DECISION__"$' "$REALSCRIPT" && grep -q '^REVIEWER_ID="0"$' "$REALSCRIPT" && ok || bad "shipped script keeps the reviewer placeholder"
mkcopy "$T/rt_bad1" "bad login" 900; mkcopy "$T/rt_bad2" sec-bot abc; mkcopy "$T/rt_bad3" sec-bot 0; mkcopy "$T/rt_bot" Gallix-Grok 900; mkcopy "$T/rt_me" mac-user 301; mkcopy "$T/rt_meid" other-login 300
for c in rt_bad1 rt_bad2 rt_bad3 rt_bot rt_me rt_meid; do reset_fixtures; USE_SCRIPT=$T/$c/release-tag.sh; refuses "reviewer identity [$c] refused" -- 5.1.0; unset USE_SCRIPT; collect; done
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "approved at 1111111")"; refuses "review names only a short sha" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "SECURITY-APPROVED: $OLD")"; refuses "review approves another sha" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "approved at $SHA")"; refuses "sha mentioned without the exact approval line" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "xSECURITY-APPROVED: $SHA")"; refuses "approval line with a prefix" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "SECURITY-APPROVED: $SHA x")"; refuses "approval line with a suffix" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "security-approved: $SHA")"; refuses "approval keyword in lower case" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "SECURITY-APPROVED: $(printf '%s' "$SHA" | tr 1 A)")"; refuses "approval with an upper-case sha" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 COMMENTED "SECURITY-APPROVED:  $SHA")"; refuses "approval line with two spaces" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 DISMISSED "$APP")"; refuses "dismissed review" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 PENDING "$APP")"; refuses "pending review" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 CHANGES_REQUESTED "$APP")"; refuses "changes requested" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 someone 901 APPROVED "$APP")"; refuses "right line, wrong author" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 901 APPROVED "$APP")"; refuses "right login but another numeric id" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 other-login 900 APPROVED "$APP")"; refuses "right id but another login" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 1 SEC-Bot 900 APPROVED "$APP")"; run -- 5.1.0; eq "login compares case-insensitively (id must match too)" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "note\\r\\n$APP\\r\\nthanks")"; run -- 5.1.0; eq "approval line among other lines, CRLF accepted" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP")"; run -- 5.1.0; eq "an APPROVED review with the line passes" "$RC" 0; collect
# comments are not approvals any more
reset_fixtures; rvs; put "$R/pulls/7/comments" "[{\"id\":2,\"user\":{\"login\":\"sec-bot\",\"id\":900},\"body\":\"$APP\"}]"; put "$R/issues/7/comments" "[{\"id\":3,\"user\":{\"login\":\"sec-bot\",\"id\":900},\"body\":\"$APP\"}]"
refuses "a review line comment or a PR comment with the approval line does not count" -- 5.1.0
eq "the script does not even read issue comments or line comments" "$(grep -c -e '/comments' "$T/log")" 0; collect
# a later block of the same sha
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP" 2026-10-29T01:00:00Z)" "$(rv 2 sec-bot 900 CHANGES_REQUESTED "$BLK" 2026-10-29T02:00:00Z)"; refuses "a newer review blocks the same sha" -- 5.1.0
case "$OUT" in *SECURITY-BLOCKED*) ok;; *) bad "block message ($OUT)";; esac; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP" 2026-10-29T01:00:00Z)" "$(rv 2 sec-bot 900 COMMENTED "$BLK" 2026-10-29T02:00:00Z)"; refuses "a newer COMMENTED block blocks too" -- 5.1.0; collect
reset_fixtures; rvs "$(rv 2 sec-bot 900 CHANGES_REQUESTED "$BLK" 2026-10-29T01:00:00Z)" "$(rv 1 sec-bot 900 APPROVED "$APP" 2026-10-29T02:00:00Z)"; run -- 5.1.0; eq "an approval newer than the block passes (ordered by submitted_at, not id)" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP" 2026-10-29T01:00:00Z)" "$(rv 2 sec-bot 900 COMMENTED "SECURITY-BLOCKED: $OLD" 2026-10-29T02:00:00Z)"; run -- 5.1.0; eq "a block of another sha does not block this one" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP" 2026-10-29T01:00:00Z)" "$(rv 2 someone 901 CHANGES_REQUESTED "$BLK" 2026-10-29T02:00:00Z)"; run -- 5.1.0; eq "a block by somebody else is not the reviewer's block" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP" 2026-10-29T01:00:00Z)" "$(rv 2 sec-bot 900 PENDING "$BLK" 2026-10-29T02:00:00Z)"; run -- 5.1.0; eq "a pending (unsubmitted) block is ignored" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP\\nsee $BLK" 2026-10-29T01:00:00Z)"; run -- 5.1.0; eq "a block line that is not on its own line does not match" "$RC" 0; collect
reset_fixtures; rvs "$(rv 1 sec-bot 900 APPROVED "$APP\\n$BLK" 2026-10-29T01:00:00Z)"; refuses "approval and block in the same review: not newer, but still not approved cleanly" -- 5.1.0; collect
# who wrote the change can never be the reviewer
reset_fixtures; put "$R/pulls/7" '{"number":7,"user":{"login":"sec-bot","id":900}}'; refuses "reviewer is the PR author (login and id)" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7" '{"number":7,"user":{"login":"other","id":900}}'; refuses "reviewer id equals the PR author id" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7" '{"number":7,"user":{"login":"SEC-bot","id":1}}'; refuses "reviewer login equals the PR author login" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/commits" '[{"author":{"login":"coder","id":100},"committer":{"login":"sec-bot","id":900}}]'; refuses "reviewer is a commit committer" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/commits" '[{"author":{"login":"sec-bot","id":900},"committer":{"login":"coder","id":100}}]'; refuses "reviewer is a commit author" -- 5.1.0; collect
reset_fixtures; put "$R/commits/$SHA" '{"author":{"login":"sec-bot","id":900},"committer":{"login":"web-flow","id":200}}'; refuses "reviewer authored the target commit" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7" '{"number":7,"user":null}'; refuses "PR without an author in the answer" -- 5.1.0; collect
reset_fixtures; rm -f "$T/fix/user"; refuses "gh account unknown: refuses" -- 5.1.0; collect
reset_fixtures; rm -f "$T/fix/$(fx "$R/pulls/7")"; refuses "an API error reading the PR refuses" -- 5.1.0; collect
reset_fixtures; rm -f "$T/fix/$(fx "$R/pulls/7/commits")"; refuses "an API error reading the PR commits refuses" -- 5.1.0; collect
reset_fixtures; rm -f "$T/fix/$(fx "$R/commits/$SHA")"; refuses "an API error reading the target commit refuses" -- 5.1.0; collect
reset_fixtures; put "$R/commits/$SHA/pulls" '[{"number":6},{"number":7}]'; put "$R/pulls/6" '{"number":6,"user":{"login":"coder","id":100}}'; put "$R/pulls/6/commits" '[]'; put "$R/pulls/6/reviews" '[]'
run -- 5.1.0; eq "the review may be on any PR of the commit" "$RC" 0; collect
reset_fixtures; rm -f "$T/fix/$(fx "$R/pulls/7/reviews")"; refuses "an API error reading reviews refuses (fail closed)" -- 5.1.0; collect
# environment cannot move the constants
reset_fixtures; run EVE_REPO=evil/repo EVE_SECURITY_REVIEWER=someone EVE_RELEASE_TAG_REMOTE=/tmp/evil.git EVE_TAGGER_NAME=x -- 5.1.0; eq "env EVE_REPO/EVE_SECURITY_REVIEWER/EVE_RELEASE_TAG_REMOTE are ignored" "$RC" 0
grep -q "^git push --no-verify git@github.com:gun025bba/eve-serving.git refs/tags/v5.1.0:refs/tags/v5.1.0 | " "$T/log" && ok || bad "env cannot change the remote"
grep -q 'evil' "$T/log" && bad "env value reached git/gh" || ok; collect
for v in EVE_REPO EVE_SECURITY_REVIEWER EVE_RELEASE_TAG_REMOTE EVE_TAGGER_NAME EVE_TAGGER_EMAIL; do grep -v '^ *#' "$REALSCRIPT" | grep -q "$v" && bad "script reads $v" || ok; done

# ---------------- required checks ----------------
okc='{"id":10,"name":"test","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}'
okm='{"id":11,"name":"deploy-nopasswd-macos","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}'
CK="$R/commits/$SHA/check-runs?per_page=100"
reset_fixtures; put "$CK" "$(checks "$okc")"; refuses "macos check missing" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks "$okm")"; refuses "test check missing" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks)"; refuses "no checks at all" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks "$okc" '{"id":11,"name":"deploy-nopasswd-macos","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}}')"; refuses "a check failed" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks "$okc" '{"id":11,"name":"deploy-nopasswd-macos","status":"in_progress","conclusion":null,"app":{"slug":"github-actions"}}')"; refuses "a check still running" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks "$okc" '{"id":11,"name":"deploy-nopasswd-macos","status":"completed","conclusion":"skipped","app":{"slug":"github-actions"}}')"; refuses "a check skipped" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks "$okc" '{"id":11,"name":"deploy-nopasswd-macos","status":"completed","conclusion":"success","app":{"slug":"some-other-app"}}')"; refuses "a success posted by another app" -- 5.1.0; collect
reset_fixtures; put "$CK" "$(checks '{"id":9,"name":"test","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}}' "$okc" "$okm")"
run -- 5.1.0; eq "an old failure superseded by a newer success passes" "$RC" 0; collect
reset_fixtures; put "$CK" "$(checks '{"id":99,"name":"test","status":"completed","conclusion":"failure","app":{"slug":"github-actions"}}' "$okc" "$okm")"
refuses "a newer failure supersedes an older success" -- 5.1.0; collect
reset_fixtures; rm -f "$T/fix/$(fx "$CK")"; refuses "an API error reading checks refuses" -- 5.1.0; collect

# ---------------- version ----------------
reset_fixtures; refuses "equal to the highest tag" FAKE_TAGS="a\trefs/tags/v5.1.0\n" -- 5.1.0; collect
reset_fixtures; refuses "below the highest tag" FAKE_TAGS="a\trefs/tags/v5.0.2\nb\trefs/tags/v5.2.0\n" -- 5.1.9; collect
reset_fixtures; refuses "below an older major" FAKE_TAGS="a\trefs/tags/v6.0.0\n" -- 5.9.9; collect
reset_fixtures; run FAKE_TAGS="a\trefs/tags/v5.9.0\nb\trefs/tags/v5.9.0^{}\n" -- 5.10.0; eq "5.10.0 is above 5.9.0 (numeric, not text)" "$RC" 0; collect
reset_fixtures; run FAKE_TAGS="" -- 0.0.1; eq "no tag yet: the first version is accepted" "$RC" 0; collect
reset_fixtures; run FAKE_TAGS="a\trefs/tags/v5.0.0\nb\trefs/tags/v99.0.0-rc1\nc\trefs/tags/v9\n" -- 5.0.1; eq "malformed v* tags are ignored" "$RC" 0; collect
reset_fixtures; refuses "tag listing fails" FAKE_LSTAGS_RC=128 -- 5.0.1; collect
for v in 05.0.0 5.0 5.0.0.x 5.0.0-rc1 "5.0.0 " 1000000000.0.0 x.y.z "" '5.0.0;id' -1.0.0; do reset_fixtures; refuses "invalid version [$v]" -- "$v"; collect; done
reset_fixtures; refuses "unknown option --force" -- --force 5.1.0; collect
reset_fixtures; refuses "unknown option --tags" -- --tags 5.1.0; collect
reset_fixtures; refuses "three arguments" -- 5.1.0 "$SHA" extra; collect
reset_fixtures; refuses "a local tag of that name exists" FAKE_LOCAL_TAG=1 -- 5.1.0; collect

# ---------------- the key and ssh ----------------
reset_fixtures; chmod 644 "$KEY"; refuses "key readable by others" -- 5.1.0; chmod 600 "$KEY"; collect
reset_fixtures; chmod 660 "$KEY"; refuses "key writable by group" -- 5.1.0; chmod 600 "$KEY"; collect
reset_fixtures; run -- 5.1.0; chmod 400 "$KEY"; reset_fixtures; run -- 5.1.0; eq "mode 0400 is accepted" "$RC" 0; chmod 600 "$KEY"; collect
chmod 755 "$T/keys"; reset_fixtures; refuses "key directory readable by others (0755)" -- 5.1.0; chmod 770 "$T/keys"; reset_fixtures; refuses "key directory open to group (0770)" -- 5.1.0
chmod 750 "$T/keys"; reset_fixtures; refuses "key directory readable by group (0750)" -- 5.1.0; chmod 701 "$T/keys"; reset_fixtures; refuses "key directory searchable by others (0701)" -- 5.1.0
chmod 500 "$T/keys"; reset_fixtures; run -- 5.1.0; eq "key directory 0500 is accepted" "$RC" 0; chmod 700 "$T/keys"; collect
mkdir -p "$T/keys2"; chmod 700 "$T/keys2"; ln -sfn "$T/keys2" "$T/keysln"; cp "$KEY" "$T/keys2/k"; chmod 600 "$T/keys2/k"; reset_fixtures; refuses "key directory is a symlink" EVE_RELEASE_TAG_KEY="$T/keysln/k" -- 5.1.0; collect
reset_fixtures; mkcopy "$T/rt_nokh" sec-bot 900; rm -f "$T/rt_nokh/github_known_hosts"; USE_SCRIPT=$T/rt_nokh/release-tag.sh; refuses "known_hosts file missing" -- 5.1.0; unset USE_SCRIPT; collect
reset_fixtures; refuses "a clone whose own config rewrites URLs (url.*.insteadOf)" FAKE_URLREWRITE=0 -- 5.1.0; collect
reset_fixtures; refuses "key missing" EVE_RELEASE_TAG_KEY="$T/keys/nope" -- 5.1.0; collect
ln -sf "$KEY" "$T/keys/link"; reset_fixtures; refuses "key is a symlink" EVE_RELEASE_TAG_KEY="$T/keys/link" -- 5.1.0; collect
reset_fixtures; refuses "key path with a space or quote" "EVE_RELEASE_TAG_KEY=$T/keys/a b" -- 5.1.0; refuses "key path with a quote" "EVE_RELEASE_TAG_KEY=$T/keys/a'b" -- 5.1.0; collect
reset_fixtures; run HOME="$T/home" EVE_RELEASE_TAG_KEY= -- 5.1.0; eq "default key path is under HOME and absent here: refused" "$RC" 1; collect
mkdir -p "$T/home/.eve-release-tagger"; chmod 700 "$T/home/.eve-release-tagger"; cp "$KEY" "$T/home/.eve-release-tagger/id_ed25519"; chmod 600 "$T/home/.eve-release-tagger/id_ed25519"
reset_fixtures; run EVE_RELEASE_TAG_KEY= -- 5.1.0 >/dev/null; USE=$(cat "$T/log" 2>/dev/null)
grep -q "SSH=ssh -F /dev/null -i $T/home/.eve-release-tagger/id_ed25519 -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes" "$T/log" && ok || bad "default key path is \$HOME/.eve-release-tagger/id_ed25519"; collect
reset_fixtures; USE_PATH="$T/nossh"; refuses "ssh not installed" -- 5.1.0; unset USE_PATH
case "$OUT" in *"'ssh' is not installed"*) ok;; *) bad "ssh missing: message names ssh ($OUT)";; esac; collect

# ---------------- the remote refuses the push ----------------
reset_fixtures; run FAKE_PUSH_RC=1 -- 5.1.0; eq "push rejected: exit 1" "$RC" 1
grep -q '^git tag -d v5.1.0' "$T/log" && ok || bad "push rejected: the local tag is removed again"; collect

# ---------------- the whole log of every scenario: only tag refs are ever pushed ----------------
eq "some pushes happened in these tests" "$([ "$(grep -c '^git push' "$T/all.log")" -ge 4 ] && echo yes)" yes
bad_push=$(grep '^git push' "$T/all.log" | grep -vE '^git push --no-verify [^ ]+ refs/tags/v[0-9]+\.[0-9]+\.[0-9]+:refs/tags/v[0-9]+\.[0-9]+\.[0-9]+ \| SSH=' | wc -l | tr -d ' ')
eq "every push is 'push --no-verify <remote> refs/tags/vX.Y.Z:refs/tags/vX.Y.Z' and nothing else" "$bad_push" 0
grep '^git push' "$T/all.log" | grep -qE -- '--force|--all|--mirror|--tags|--delete|refs/heads| \+|--prune| -f' && bad "a branch ref or a forcing flag was pushed" || ok
eq "no fake ssh was ever executed" "$(grep -c 'fake ssh' "$T/all.log")" 0
eq "no BAD gh/git call was logged anywhere" "$(grep -c BAD "$T/all.log")" 0
eq "key content never appears in any log" "$(grep -c "$SECRET" "$T/all.log")" 0

# ---------------- static properties of the script ----------------
eq "the script contains exactly one 'git push'" "$(grep -c '^[^#]*git push' "$REALSCRIPT")" 1
grep '^[^#]*git push' "$REALSCRIPT" | grep -q 'refs/tags/\$TAG:refs/tags/\$TAG' && ok || bad "the one push line pushes refs/tags/\$TAG only"
grep -nE '^[^#]*git (-c [^ ]+ )*push' "$REALSCRIPT" | grep -E -- '--force|--mirror|--delete|--all|--tags|\+refs| -f ' | grep -q . && bad "script has a forcing or wide push flag" || ok
eq "GIT_SSH_COMMAND is set exactly once, to the spec" "$(grep -c '^export GIT_SSH_COMMAND="ssh -F /dev/null -i \$KEY -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=\$KNOWN_HOSTS -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes -o IdentityAgent=none"$' "$REALSCRIPT")" 1
eq "git config is neutralised (global and system)" "$(grep -c '^export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1$' "$REALSCRIPT")" 1
grep -q 'StrictHostKeyChecking=no\|UserKnownHostsFile=/dev/null' "$REALSCRIPT" && bad "host key checking is weakened" || ok
eq "every git call in the script runs with the neutralised config (it is exported before the first git call)" "$(awk '/^export GIT_CONFIG_GLOBAL/{f=1} /^[^#]*git (ls-remote|push|tag|fetch)/{ if(!f) n++ } END{print n+0}' "$REALSCRIPT")" 0
eq "known_hosts has exactly GitHub's three published host key types" "$(grep -v '^#' "$KH" | awk '{print $1" "$2}' | tr '\n' ';')" "github.com ssh-ed25519;github.com ecdsa-sha2-nistp256;github.com ssh-rsa;"
eq "known_hosts ed25519 key is the one GitHub publishes (SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU)" "$(grep ' ssh-ed25519 ' "$KH" | awk '{print $3}')" "AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
grep -nE 'cat +"?\$KEY|<"?\$KEY|echo.*\$KEY|printf.*\$KEY|ssh-add|ssh-keygen|\bcp .*\$KEY' "$REALSCRIPT" | grep -v '^[0-9]*:#' | grep -q . && bad "script reads, copies or prints the key" || ok
fw="$(printf 'jar%s' vis)|$(printf '\354\236\220\353\271\204\354\212\244')"; if grep -qiE "$fw" "$REALSCRIPT" "$HERE/release-tag-test.sh"; then bad "forbidden word"; else ok; fi

# ---------------- real git against a local bare repository (file transport; gh and ssh are still fakes) ----------------
G="$T/real"; mkdir -p "$G"; export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
( cd "$G" && "$REALGIT" init -q --bare origin.git && "$REALGIT" init -q -b main seed && cd seed && echo 1 >f && "$REALGIT" add f && "$REALGIT" commit -q -m one \
  && "$REALGIT" tag -a -m old v1.0.0 && "$REALGIT" branch dev && "$REALGIT" remote add o "$G/origin.git" && "$REALGIT" push -q o main dev v1.0.0 && echo 2 >f && "$REALGIT" commit -q -a -m two && "$REALGIT" push -q o main ) >/dev/null 2>&1
"$REALGIT" clone -q "$G/origin.git" "$G/clone" >/dev/null 2>&1
RTIP=$("$REALGIT" -C "$G/origin.git" rev-parse refs/heads/main); BEFORE=$("$REALGIT" -C "$G/origin.git" for-each-ref refs/heads)
reset_fixtures
put "$R/commits/$RTIP/pulls" '[{"number":7}]'
put "$R/commits/$RTIP" '{"author":{"login":"coder","id":100},"committer":{"login":"web-flow","id":200}}'
put "$R/pulls/7/reviews" "[$(rv 1 sec-bot 900 COMMENTED "SECURITY-APPROVED: $RTIP")]"
put "$R/commits/$RTIP/check-runs?per_page=100" "$(checks "$okc" "$okm")"
mkcopy "$T/rt_real" sec-bot 900 "$G/origin.git"
rrun() { ( cd "$G/clone" && env -i HOME="$T/home" PATH="$T/rbin:/usr/bin:/bin" FAKE_LOG="$T/rlog" FAKE_FIX="$T/fix" EVE_RELEASE_TAG_KEY="$KEY" \
   GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid "$REALBASH" "$T/rt_real/release-tag.sh" "$@" ) >"$T/rout" 2>&1; }
mkdir -p "$T/rbin"; cp "$T/bin/gh" "$T/rbin/gh"; cp "$T/bin/ssh" "$T/rbin/ssh"; ln -sf "$REALGIT" "$T/rbin/git"; for t in jq; do ln -sf "$(command -v $t)" "$T/rbin/$t"; done
rrun 1.0.0; eq "real git: v1.0.0 already exists, equal version refused" "$?" 1
rrun 1.1.0; eq "real git: 1.1.0 pushed" "$?" 0
eq "real git: the remote now has refs/tags/v1.1.0 as an annotated tag" "$("$REALGIT" -C "$G/origin.git" cat-file -t refs/tags/v1.1.0 2>/dev/null)" tag
eq "real git: the tag points at the tip of main" "$("$REALGIT" -C "$G/origin.git" rev-parse 'refs/tags/v1.1.0^{commit}' 2>/dev/null)" "$RTIP"
eq "real git: no branch changed on the remote" "$("$REALGIT" -C "$G/origin.git" for-each-ref refs/heads)" "$BEFORE"
eq "real git: the remote's refs are the branches and the two tags only" "$("$REALGIT" -C "$G/origin.git" for-each-ref --format='%(refname)' | tr '\n' ' ')" "refs/heads/dev refs/heads/main refs/tags/v1.0.0 refs/tags/v1.1.0 "
rrun 1.1.0; eq "real git: the same version again is refused" "$?" 1
rrun 1.0.5; eq "real git: a lower version is refused" "$?" 1
"$REALGIT" -C "$G/seed" commit -q --allow-empty -m three && "$REALGIT" -C "$G/seed" push -q o main >/dev/null 2>&1
rrun 1.2.0 "$RTIP"; eq "real git: the old tip is refused once main moved on" "$?" 1
eq "real git: nothing else was created on the remote" "$("$REALGIT" -C "$G/origin.git" for-each-ref --format='%(refname)' | grep -c refs/tags/)" 2

echo "release-tag tests passed: $PASS  failed: $FAILN"
[ "$FAILN" = 0 ]
