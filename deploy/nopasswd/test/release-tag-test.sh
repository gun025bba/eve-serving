#!/bin/bash
# Offline tests for deploy/nopasswd/release-tag.sh. No network, no key, no GitHub: `gh`, `git` (and `ssh`) are fakes on PATH that log every call and
# answer from fixtures; one last block uses the REAL git against a local bare repository (file transport) with a fake `gh`.
#   bash deploy/nopasswd/test/release-tag-test.sh          (also run by test/run.sh, so CI runs it on Ubuntu and on macOS with bash 3.2)
# Needs: bash, git, jq, coreutils/BSD tools.
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
D=$(cd "$HERE/.." && pwd)
SCRIPT=$D/release-tag.sh
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
for t in grep head stat id jq sort tr cat dirname mktemp; do p=$(command -v "$t") && ln -s "$p" "$T/nossh/$t"; done

# ---------------- fixtures ----------------
fx() { printf '%s' "$1" | tr -c 'A-Za-z0-9' _; }
put() { printf '%s\n' "$2" >"$T/fix/$(fx "$1")"; }
R=repos/gun025bba/eve-serving
reset_fixtures() {
  rm -f "$T/fix"/* "$T/log" "$T/log.n" "$T/log.fetched"
  put "$R/commits/$SHA/pulls" '[{"number":7}]'
  put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"sec-bot\"},\"state\":\"COMMENTED\",\"body\":\"approved at $SHA\"}]"
  put "$R/pulls/7/comments" '[]'
  put "$R/issues/7/comments" '[]'
  put "$R/commits/$SHA/check-runs?per_page=100" "$(checks '{"id":10,"name":"test","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}' '{"id":11,"name":"deploy-nopasswd-macos","status":"completed","conclusion":"success","app":{"slug":"github-actions"}}')"
}
checks() { local a; a=$(printf '%s,' "$@"); printf '{"total_count":%s,"check_runs":[%s]}' "$#" "${a%,}"; }
run() { # run [VAR=val ...] -- args ; sets RC, OUT (stdout+stderr)
  local envs=()
  while [ "$1" != -- ]; do envs+=("$1"); shift; done; shift
  OUT=$(cd "$WORK" && env -i HOME="$T/home" PATH="${USE_PATH:-$T/bin:/usr/bin:/bin}" FAKE_LOG="$T/log" FAKE_FIX="$T/fix" FAKE_MAIN_1="$SHA" FAKE_TAGS="" \
        EVE_SECURITY_REVIEWER=sec-bot EVE_RELEASE_TAG_KEY="$KEY" ${envs[@]+"${envs[@]}"} "$REALBASH" "$SCRIPT" "$@" 2>&1); RC=$?
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
eq "happy: every git call after the checks uses exactly the deploy-key ssh command" "$(grep '^git ' "$T/log" | grep -vc -e '--git-dir')" "$(grep -c "| SSH=ssh -i $KEY -o IdentitiesOnly=yes\$" "$T/log")"
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
reset_fixtures; put "$R/commits/$SHA/pulls" '[]'; refuses "no pull request for the commit" -- 5.1.0; collect
reset_fixtures; refuses "reviewer not configured" EVE_SECURITY_REVIEWER= -- 5.1.0; collect
reset_fixtures; refuses "review by somebody else" EVE_SECURITY_REVIEWER=other -- 5.1.0; collect
reset_fixtures; refuses "invalid reviewer login" 'EVE_SECURITY_REVIEWER=a b;id' -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/reviews" '[{"id":1,"user":{"login":"sec-bot"},"state":"COMMENTED","body":"approved at 1111111"}]'
refuses "review names only a short sha" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"sec-bot\"},\"state\":\"COMMENTED\",\"body\":\"approved at $OLD\"}]"
refuses "review names another sha" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"sec-bot\"},\"state\":\"DISMISSED\",\"body\":\"approved at $SHA\"}]"
refuses "dismissed review" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"sec-bot\"},\"state\":\"CHANGES_REQUESTED\",\"body\":\"not $SHA\"}]"
refuses "changes requested" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"someone\"},\"state\":\"APPROVED\",\"body\":\"approved at $SHA\"}]"
refuses "right sha, wrong author" -- 5.1.0; collect
reset_fixtures; put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"SEC-Bot\"},\"state\":\"APPROVED\",\"body\":\"APPROVED AT $(printf '%s' "$SHA" | tr a-f A-F)\"}]"
run -- 5.1.0; eq "login and sha compare case-insensitively" "$RC" 0; collect
reset_fixtures; put "$R/pulls/7/reviews" '[]'; put "$R/pulls/7/comments" "[{\"id\":2,\"user\":{\"login\":\"sec-bot\"},\"body\":\"line note for $SHA\"}]"
run -- 5.1.0; eq "a PR review comment naming the sha is enough" "$RC" 0; collect
reset_fixtures; put "$R/pulls/7/reviews" '[]'; put "$R/issues/7/comments" "[{\"id\":3,\"user\":{\"login\":\"sec-bot\"},\"body\":\"merge at $SHA ok\"}]"
run -- 5.1.0; eq "a PR comment naming the sha is enough" "$RC" 0; collect
reset_fixtures; put "$R/pulls/7/reviews" '[]'; put "$R/issues/7/comments" "[{\"id\":3,\"user\":{\"login\":\"intruder\"},\"body\":\"merge at $SHA ok\"}]"
refuses "a comment by another user naming the sha" -- 5.1.0; collect
reset_fixtures; put "$R/commits/$SHA/pulls" '[{"number":6},{"number":7}]'; put "$R/pulls/6/reviews" '[]'; put "$R/pulls/6/comments" '[]'; put "$R/issues/6/comments" '[]'
run -- 5.1.0; eq "the review may be on any PR of the commit" "$RC" 0; collect
reset_fixtures; rm -f "$T/fix/$(fx "$R/pulls/7/comments")"; refuses "an API error reading comments refuses (fail closed)" -- 5.1.0; collect

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
reset_fixtures; refuses "key missing" EVE_RELEASE_TAG_KEY="$T/keys/nope" -- 5.1.0; collect
ln -sf "$KEY" "$T/keys/link"; reset_fixtures; refuses "key is a symlink" EVE_RELEASE_TAG_KEY="$T/keys/link" -- 5.1.0; collect
reset_fixtures; refuses "key path with a space or quote" "EVE_RELEASE_TAG_KEY=$T/keys/a b" -- 5.1.0; refuses "key path with a quote" "EVE_RELEASE_TAG_KEY=$T/keys/a'b" -- 5.1.0; collect
reset_fixtures; run HOME="$T/home" EVE_RELEASE_TAG_KEY= -- 5.1.0; eq "default key path is under HOME and absent here: refused" "$RC" 1; collect
mkdir -p "$T/home/.eve-release-tagger"; cp "$KEY" "$T/home/.eve-release-tagger/id_ed25519"; chmod 600 "$T/home/.eve-release-tagger/id_ed25519"
reset_fixtures; run EVE_RELEASE_TAG_KEY= -- 5.1.0 >/dev/null; USE=$(cat "$T/log" 2>/dev/null)
grep -q "SSH=ssh -i $T/home/.eve-release-tagger/id_ed25519 -o IdentitiesOnly=yes" "$T/log" && ok || bad "default key path is \$HOME/.eve-release-tagger/id_ed25519"; collect
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
eq "the script contains exactly one 'git push'" "$(grep -c '^[^#]*git push' "$SCRIPT")" 1
grep '^[^#]*git push' "$SCRIPT" | grep -q 'refs/tags/\$TAG:refs/tags/\$TAG' && ok || bad "the one push line pushes refs/tags/\$TAG only"
grep -nE '^[^#]*git (-c [^ ]+ )*push' "$SCRIPT" | grep -E -- '--force|--mirror|--delete|--all|--tags|\+refs| -f ' | grep -q . && bad "script has a forcing or wide push flag" || ok
eq "GIT_SSH_COMMAND is set exactly once, to the spec" "$(grep -c '^export GIT_SSH_COMMAND="ssh -i \$KEY -o IdentitiesOnly=yes"$' "$SCRIPT")" 1
grep -nE 'cat +"?\$KEY|<"?\$KEY|echo.*\$KEY|printf.*\$KEY|ssh-add|ssh-keygen|\bcp .*\$KEY' "$SCRIPT" | grep -v '^[0-9]*:#' | grep -q . && bad "script reads, copies or prints the key" || ok
fw="$(printf 'jar%s' vis)|$(printf '\354\236\220\353\271\204\354\212\244')"; if grep -qiE "$fw" "$SCRIPT" "$HERE/release-tag-test.sh"; then bad "forbidden word"; else ok; fi

# ---------------- real git against a local bare repository (file transport; gh and ssh are still fakes) ----------------
G="$T/real"; mkdir -p "$G"; export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.invalid
( cd "$G" && "$REALGIT" init -q --bare origin.git && "$REALGIT" init -q -b main seed && cd seed && echo 1 >f && "$REALGIT" add f && "$REALGIT" commit -q -m one \
  && "$REALGIT" tag -a -m old v1.0.0 && "$REALGIT" branch dev && "$REALGIT" remote add o "$G/origin.git" && "$REALGIT" push -q o main dev v1.0.0 && echo 2 >f && "$REALGIT" commit -q -a -m two && "$REALGIT" push -q o main ) >/dev/null 2>&1
"$REALGIT" clone -q "$G/origin.git" "$G/clone" >/dev/null 2>&1
RTIP=$("$REALGIT" -C "$G/origin.git" rev-parse refs/heads/main); BEFORE=$("$REALGIT" -C "$G/origin.git" for-each-ref refs/heads)
reset_fixtures
put "$R/commits/$RTIP/pulls" '[{"number":7}]'
put "$R/pulls/7/reviews" "[{\"id\":1,\"user\":{\"login\":\"sec-bot\"},\"state\":\"COMMENTED\",\"body\":\"ok $RTIP\"}]"
put "$R/commits/$RTIP/check-runs?per_page=100" "$(checks "$okc" "$okm")"
rrun() { ( cd "$G/clone" && env -i HOME="$T/home" PATH="$T/rbin:/usr/bin:/bin" FAKE_LOG="$T/rlog" FAKE_FIX="$T/fix" EVE_SECURITY_REVIEWER=sec-bot EVE_RELEASE_TAG_KEY="$KEY" \
   EVE_RELEASE_TAG_REMOTE="$G/origin.git" GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.invalid "$REALBASH" "$SCRIPT" "$@" ) >"$T/rout" 2>&1; }
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
