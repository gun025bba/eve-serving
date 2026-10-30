#!/bin/bash
# Offline tests for deploy/nopasswd/eve-session.sh and eve-measure.sh. No network, no sudo, no GitHub, no real key: sudo, gh, git (ls-remote/clone/pull only), ssh-keygen and the scripts the session runs
# (one-shot, release-tag, setup-unattended) are fakes in a throw-away git repository; every call is logged and checked in order.
#   bash deploy/nopasswd/test/session-test.sh        (also run by test/run.sh)
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd); D=$(cd "$HERE/.." && pwd)
T=$(cd "$(mktemp -d)" && pwd -P); trap 'rm -rf "$T"' EXIT
PASS=0; FAILN=0
ok()  { PASS=$((PASS + 1)); }
bad() { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()  { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
has() { if printf '%s' "$2" | grep -qF -- "$3"; then ok; else bad "$1: output lacks [$3]"; fi; }
hasnt() { if printf '%s' "$2" | grep -qF -- "$3"; then bad "$1: output has [$3]"; else ok; fi; }
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
REALGIT=$(command -v git)
[ "$(id -u)" != 0 ] || { echo "SKIP session tests: run as an unprivileged user (the session refuses to run as root)"; echo "session tests passed: 0 failed: 0 (skipped)"; exit 0; }

# ---------------- static ----------------
S=$D/eve-session.sh; M=$D/eve-measure.sh
bash -n "$S" && ok || bad "eve-session.sh syntax"; bash -n "$M" && ok || bad "eve-measure.sh syntax"
[ -x "$S" ] && ok || bad "eve-session.sh must be executable"; [ -x "$M" ] && ok || bad "eve-measure.sh must be executable"
pin() { sed -n "s/^$1=\\([0-9a-f]\\{64\\}\\).*/\\1/p" "$S" | head -1; }
eq "pin: PIN_ONESHOT_SHA256" "$(pin PIN_ONESHOT_SHA256)" "$(sha256 "$D/eve-oneshot.sh")"
eq "pin: PIN_MEASURE_SHA256" "$(pin PIN_MEASURE_SHA256)" "$(sha256 "$M")"
eq "pin: PIN_RELEASE_TAG_SHA256" "$(pin PIN_RELEASE_TAG_SHA256)" "$(sha256 "$D/release-tag.sh")"
eq "pin: PIN_RELEASE_TAG_WEB_SHA256" "$(pin PIN_RELEASE_TAG_WEB_SHA256)" "$(sha256 "$D/release-tag.eve-web.constants")"
eq "pin: the switch script is not shipped, its pin is empty" "$(sed -n 's/^PIN_SWITCH_SHA256=\([^ ]*\).*/\1/p' "$S")" ""
eq "no file switch-service-account.sh ships yet" "$([ -e "$D/switch-service-account.sh" ] && echo yes || echo no)" no
if grep -nE 'Mac Studio|\.ts\.net|([0-9]{1,3}\.){3}[0-9]{1,3}|gh[pousr]_[A-Za-z0-9]{20}|github_pat_|BEGIN [A-Z ]*PRIVATE KEY' "$S" "$M" >/dev/null; then bad "forbidden string in eve-session.sh or eve-measure.sh"; else ok; fi
if grep -vE '^\s*#' "$S" "$M" | grep -E 'curl[^|]*\|[^|]*(ba)?sh|(^|[^a-z_])eval |--force( |$)|push -f|--no-verify' >/dev/null; then bad "eve-session.sh/eve-measure.sh use curl|sh, eval, force or --no-verify"; else ok; fi
if grep -vE '^\s*#' "$M" | grep -E '(^|[^a-z_])(dd |mkfs|diskutil (erase|partition)|launchctl (bootout|unload|kickstart|stop|start|load))|rm -rf [^"]' >/dev/null; then bad "eve-measure.sh must be read-only"; else ok; fi
if grep -vE '^\s*#' "$M" | grep -E '(^|[ ;|(`])sudo( |$)' >/dev/null; then bad "eve-measure.sh must not call sudo"; else ok; fi
eq "session: the manual rollback verbs are never called" "$(grep -vE '^\s*#' "$S" | grep -E 'su_ .*rollback|sudo.*rollback' | wc -l | tr -d ' ')" 0

# ---------------- the fake world ----------------
mkenv() { # NAME -> sets E, H (home), FB (fake bin), C (calls log), FX (fixtures), R (repo), MC (merge commit)
  E=$T/$1; H=$E/home; FB=$E/bin; FX=$E/fx; R=$E/repo; ST=$E/state; C=$E/calls
  mkdir -p "$H" "$FB" "$FX" "$R/deploy/nopasswd" "$ST"; : >"$C"
  cp "$D/eve-session.sh" "$D/eve-measure.sh" "$D/release-tag.eve-web.constants" "$D/github_known_hosts" "$R/deploy/nopasswd/" 2>/dev/null || cp "$D/eve-session.sh" "$D/eve-measure.sh" "$D/release-tag.eve-web.constants" "$R/deploy/nopasswd/"
  cat >"$R/deploy/nopasswd/setup-unattended.sh" <<'FAKE'
#!/bin/bash
# fake setup-unattended: logs, edits the fake state dir $FAKE_STATE
echo "SU $*" >>"$FAKE_CALLS"
[ "$1" = --src-commit ] && shift 2
cmd=$1; kind=${2:-}; ver=${3:-}
cur=current_version; [ "$kind" = web ] && cur=current_web_version
case "$cmd" in
  deploy) [ ! -f "$FAKE_FX/deploy-fails" ] || exit 1; [ ! -f "$FAKE_STATE/PAUSE" ] || { rm -f "$FAKE_STATE/PAUSE"; touch "$FAKE_STATE/.restore"; }; echo "$ver" >"$FAKE_STATE/$cur"; [ ! -f "$FAKE_STATE/.restore" ] || { echo "reason=install" >"$FAKE_STATE/PAUSE"; rm -f "$FAKE_STATE/.restore"; };;
  rehearse) [ ! -f "$FAKE_FX/rehearse-fails" ] || exit 1; echo "$kind $ver" >>"$FAKE_STATE/blocklist"; touch "$FAKE_STATE/PAUSE"; date >"$FAKE_STATE/rehearsal_ok";;
  unpause) [ -f "$FAKE_STATE/rehearsal_ok" ] || exit 1; rm -f "$FAKE_STATE/PAUSE" "$FAKE_STATE/rehearsal_ok";;
  verify) [ ! -f "$FAKE_FX/verify-fails" ] || { echo "  FAIL  something"; exit 1; }; echo "  PASS  all";;
  *) exit 2;;
esac
FAKE
  SUSHA=$(sha256 "$R/deploy/nopasswd/setup-unattended.sh")
  cat >"$R/deploy/nopasswd/eve-oneshot.sh" <<FAKE
#!/bin/bash
echo "ONESHOT \$*" >>"\$FAKE_CALLS"
PIN_UNATTENDED_SHA256=$SUSHA   # fake
[ ! -f "\$FAKE_FX/oneshot-fails" ] || exit 1
for a in "\$@"; do [ "\$a" = --dry-run ] && { echo "oneshot dry run"; exit 0; }; done
echo installed >"\$FAKE_STATE/unattended_installed"; echo 5.0.2 >"\$FAKE_STATE/current_version"; echo 1.9.0 >"\$FAKE_STATE/current_web_version"
echo "reason=install" >"\$FAKE_STATE/PAUSE"; echo 5.0.2 >"\$FAKE_STATE/floor"; echo 1.9.0 >"\$FAKE_STATE/floor_web"
FAKE
  cat >"$R/deploy/nopasswd/release-tag.sh" <<'FAKE'
#!/bin/bash
echo "TAG cwd=$(basename "$PWD") $*" >>"$FAKE_CALLS"
[ ! -f "$FAKE_FX/tag-refused" ] || { echo "release-tag: refused: fake" >&2; exit 1; }
repo=eve-serving; [ "$1" = --repo ] && { repo=$2; shift 2; }
echo "$repo v${1#v}" >>"$FAKE_FX/tags"
case "$repo" in eve-serving) k=gateway;; *) k=web;; esac
echo "eve-$k-${1#v}.MANIFEST eve-$k-${1#v}.sigstore.json eve-$k-${1#v}.tar" >"$FAKE_FX/rel.$repo.${1#v}"
FAKE
  cp "$D/eve-measure.sh" "$R/deploy/nopasswd/eve-measure.sh"   # real one unless a test replaces it
  repin
  ( cd "$R" && "$REALGIT" init -q -b main && "$REALGIT" add -A && "$REALGIT" -c user.name=t -c user.email=t@example.invalid commit -q -m "merge commit of the fake main" )
  MC=$("$REALGIT" -C "$R" rev-parse HEAD)
  : >"$FX/tags"; echo "$MC" >"$FX/tip.eve-serving"
  W=$H/.eve-session/eve-web; mkdir -p "$W"; ( cd "$W" && "$REALGIT" init -q -b main && "$REALGIT" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "web main" ); "$REALGIT" -C "$W" rev-parse HEAD >"$FX/tip.eve-web"
  chmod 700 "$H/.eve-session"; : >"$H/.eve-session/state"; chmod 600 "$H/.eve-session/state"
  mkdir -p "$H/.eve-release-tagger" "$H/.eve-web-release-tagger"; chmod 700 "$H"/.eve-*release-tagger; for k in .eve-release-tagger .eve-web-release-tagger; do echo k >"$H/$k/id_ed25519"; chmod 600 "$H/$k/id_ed25519"; echo pub >"$H/$k/id_ed25519.pub"; done
  : >"$FX/keysok"
  fakes
}
repin() { # recompute the pins of the repo's session copy after a fake changed
  local n=$R/deploy/nopasswd
  sed -i.bak -e "s/^PIN_ONESHOT_SHA256=[0-9a-f]*/PIN_ONESHOT_SHA256=$(sha256 "$n/eve-oneshot.sh")/" -e "s/^PIN_MEASURE_SHA256=[0-9a-f]*/PIN_MEASURE_SHA256=$(sha256 "$n/eve-measure.sh")/" \
    -e "s/^PIN_RELEASE_TAG_SHA256=[0-9a-f]*/PIN_RELEASE_TAG_SHA256=$(sha256 "$n/release-tag.sh")/" -e "s/^PIN_RELEASE_TAG_WEB_SHA256=[0-9a-f]*/PIN_RELEASE_TAG_WEB_SHA256=$(sha256 "$n/release-tag.eve-web.constants")/" "$n/eve-session.sh"; rm -f "$n/eve-session.sh.bak"
}
recommit() { ( cd "$R" && "$REALGIT" add -A && "$REALGIT" -c user.name=t -c user.email=t@example.invalid commit -q -m "next" ); MC=$("$REALGIT" -C "$R" rev-parse HEAD); echo "$MC" >"$FX/tip.eve-serving"; }
fakes() {
  cat >"$FB/sudo" <<'FAKE'
#!/bin/bash
echo "SUDO $1 $2" >>"$FAKE_CALLS"
[ ! -f "$FAKE_FX/sudo-refused" ] || exit 1
while [ $# -gt 0 ]; do case "$1" in -n|-v) shift;; *) break;; esac; done
[ $# -gt 0 ] || exit 0
exec "$@"
FAKE
  cat >"$FB/gh" <<'FAKE'
#!/bin/bash
echo "GH $*" >>"$FAKE_CALLS"
[ "$1" = auth ] && exit 0
[ "$1" = api ] || exit 1
p=$2
case "$p" in
  repos/*/git/matching-refs/tags/v*) repo=${p#repos/gun025bba/}; repo=${repo%%/*}; v=${p##*/tags/}; grep -qxF "$repo $v" "$FAKE_FX/tags" && echo 1 || echo 0;;
  repos/*/releases/tags/v*) repo=${p#repos/gun025bba/}; repo=${repo%%/*}; v=${p##*/tags/v}; cat "$FAKE_FX/rel.$repo.$v" 2>/dev/null | tr ' ' '\n' | sort | tr '\n' ' ' | sed 's/ $//';;
  repos/*/git/ref/heads/main) repo=${p#repos/gun025bba/}; repo=${repo%%/*}; cat "$FAKE_FX/tip.$repo";;
  *) exit 1;;
esac
FAKE
  cat >"$FB/git" <<FAKE
#!/bin/bash
case "\$1" in
  ls-remote) echo "LSREMOTE \$*" >>"\$FAKE_CALLS"; [ -f "\$FAKE_FX/keysok" ] && exit 0 || exit 128;;
  pull) exit 0;;
esac
a=("\$@"); [ "\${a[0]}" = -C ] && [ "\${a[2]}" = pull ] && exit 0
exec $REALGIT "\$@"
FAKE
  cat >"$FB/ssh-keygen" <<'FAKE'
#!/bin/bash
echo "KEYGEN $*" >>"$FAKE_CALLS"
while [ $# -gt 0 ]; do [ "$1" = -f ] && f=$2; shift; done
echo key >"$f"; echo "ssh-ed25519 AAAAFAKE" >"$f.pub"
FAKE
  for t in ssh curl jq; do command -v "$t" >/dev/null 2>&1 || printf '#!/bin/bash\nexit 0\n' >"$FB/$t"; done
  chmod +x "$FB"/*
}
# run: ARGS... ; sets OUT and RC; env passes the fake world; stdin answers from $FX/tty
run() {
  local tty=$FX/tty; [ -f "$tty" ] || : >"$tty"
  OUT=$(cd "$R" && HOME=$H PATH="$FB:$PATH" EVE_SESSION_EXTRA_PATH=$FB FAKE_CALLS=$C FAKE_FX=$FX FAKE_STATE=$ST EVE_SESSION_STATE_DIR=$ST EVE_SESSION_POLL_SEC=1 EVE_SESSION_TTY=$tty EVE_MEASURE_PATH="$FB:/usr/bin:/bin" \
        bash deploy/nopasswd/eve-session.sh "$@" 2>&1); RC=$?
}
calls() { cat "$C"; }
ARGS_BASE() { echo "--merge-commit $MC --gateway 5.1.0 --web 2.0.0"; }

# ---------------- argument checks (nothing runs) ----------------
eq "static: the session refuses to run as root" "$(grep -c 'id -u)" != 0 \] || die "run this as your own user' "$S" | tr -d ' ')" 1
mkenv args
run --merge-commit "$MC" --gateway 5.1 --web 2.0.0; eq "args: bad gateway version" "$RC" 1; has "args: bad gateway version" "$OUT" "--gateway X.Y.Z is required"
run --merge-commit abc --gateway 5.1.0 --web 2.0.0; eq "args: bad sha" "$RC" 1; has "args: bad sha" "$OUT" "40-hex"
run --merge-commit "$MC" --gateway 5.1.0 --web 2.0.0 --rehearse-gateway 5.1.1; eq "args: one-sided rehearsal" "$RC" 1; has "args: one-sided rehearsal" "$OUT" "BOTH"
run --merge-commit "$MC" --gateway 5.1.0 --web 2.0.0 --bogus; eq "args: unknown option" "$RC" 1; eq "args: nothing was called" "$(calls | wc -l | tr -d ' ')" 0

# ---------------- preflight refusals ----------------
mkenv pre
run --merge-commit 1111111111111111111111111111111111111111 --gateway 5.1.0 --web 2.0.0; eq "pre: clone not at the merge commit" "$RC" 1; has "pre: clone not at the merge commit" "$OUT" "not at the merge commit"
echo x >>"$R/deploy/nopasswd/eve-session.sh"; run $(ARGS_BASE); eq "pre: dirty clone" "$RC" 1; has "pre: dirty clone" "$OUT" "uncommitted changes"
( cd "$R" && "$REALGIT" checkout -q -- . )
echo "# changed" >>"$R/deploy/nopasswd/release-tag.sh"; recommit   # the file changed but the pin was not renewed
run $(ARGS_BASE); eq "pre: stale pin refused" "$RC" 1; has "pre: stale pin refused" "$OUT" "release-tag.sh does not match the sha256 pinned"
eq "pre: nothing ran" "$(calls | grep -cE 'SUDO|ONESHOT|TAG|SU ' | tr -d ' ')" 0
mkenv pre2; rm -f "$FB/gh"; run $(ARGS_BASE) --dry-run >/dev/null; mkenv pre3; echo 'x' >"$R/deploy/nopasswd/eve-oneshot.sh"; recommit; run $(ARGS_BASE); has "pre: changed one-shot refused" "$OUT" "eve-oneshot.sh does not match the sha256 pinned"

# ---------------- dry run: nothing is changed ----------------
mkenv dry
run $(ARGS_BASE) --rehearse-gateway 5.1.1 --rehearse-web 2.0.1 --switch --dry-run
eq "dry: exit 0" "$RC" 0; has "dry: finished" "$OUT" "session finished"; has "dry: plan" "$OUT" "plan: install"
has "dry: measurement printed" "$OUT" "measurement (read-only, 14 items)"; has "dry: stage list" "$OUT" "stage rehearse"
eq "dry: no sudo, no gh, no tag, no deploy" "$(calls | grep -cE '^(SUDO|GH|TAG|SU |KEYGEN|LSREMOTE)' | tr -d ' ')" 0
eq "dry: the one-shot was only dry-run" "$(calls | grep -c '^ONESHOT' | tr -d ' ')" 1; has "dry: one-shot dry" "$(calls)" "ONESHOT --dry-run"
eq "dry: no state change" "$(ls "$ST" | wc -l | tr -d ' ')" 0

# ---------------- the full run ----------------
mkenv full
printf 'tag\nunpause\n' >"$FX/tty"
# the releases appear when the tags are pushed (the fake release-tag writes them); the test releases exist already
echo "eve-gateway-5.1.1.MANIFEST eve-gateway-5.1.1.sigstore.json eve-gateway-5.1.1.tar" >"$FX/rel.eve-serving.5.1.1"; echo "eve-web-2.0.1.MANIFEST eve-web-2.0.1.sigstore.json eve-web-2.0.1.tar" >"$FX/rel.eve-web.2.0.1"
run $(ARGS_BASE) --rehearse-gateway 5.1.1 --rehearse-web 2.0.1
eq "full: exit 0" "$RC" 0; has "full: finished" "$OUT" "session finished"
CALLS=$(calls)
ln_of() { printf '%s\n' "$CALLS" | grep -n "$1" | head -1 | cut -d: -f1; }
lt() { [ -n "$2" ] && [ -n "$3" ] && [ "$2" -lt "$3" ] && ok || bad "$1 (order $2 !< $3)"; }
lt "full: sudo password first, then the one-shot" "$(ln_of '^SUDO -v')" "$(ln_of '^ONESHOT')"
lt "full: one-shot before the tags" "$(ln_of '^ONESHOT')" "$(ln_of '^TAG')"
has "full: one-shot gets the merge commit and nothing else" "$CALLS" "ONESHOT --merge-commit $MC"
eq "full: the one-shot got no deploy options" "$(printf '%s\n' "$CALLS" | grep '^ONESHOT' | grep -cE 'web-deploy|rehearse')" 0
has "full: gateway tag from the clone, with the merge commit" "$CALLS" "TAG cwd=repo 5.1.0 $MC"
has "full: web tag from the web clone with --repo eve-web" "$CALLS" "TAG cwd=eve-web --repo eve-web 2.0.0"
lt "full: tags before the deploy" "$(ln_of '^TAG cwd=eve-web')" "$(ln_of '^SU .*deploy gateway')"
lt "full: gateway deploy before the web deploy" "$(ln_of '^SU .*deploy gateway 5.1.0')" "$(ln_of '^SU .*deploy web 2.0.0')"
lt "full: deploys before the rehearsal" "$(ln_of '^SU .*deploy web 2.0.0')" "$(ln_of '^SU .*rehearse gateway 5.1.1')"
lt "full: gateway rehearsal before the web rehearsal" "$(ln_of '^SU .*rehearse gateway')" "$(ln_of '^SU .*rehearse web')"
lt "full: verify after the rehearsal" "$(ln_of '^SU .*rehearse web')" "$(ln_of '^SU .*verify')"
eq "full: no rollback verb is ever used" "$(printf '%s\n' "$CALLS" | grep -ci 'rollback' | tr -d ' ')" 0
has "full: --src-commit is the merge commit" "$CALLS" "SU --src-commit $MC deploy gateway 5.1.0"
eq "full: gateway live" "$(cat "$ST/current_version")" 5.1.0; eq "full: web live" "$(cat "$ST/current_web_version")" 2.0.0
eq "full: PAUSE stays without --unpause" "$([ -e "$ST/PAUSE" ] && echo yes || echo no)" yes
has "full: PAUSE hint" "$OUT" "run the same command again with --unpause"
has "full: table pass" "$OUT" "PASS  gateway live"; hasnt "full: no table fail" "$(printf '%s\n' "$OUT" | sed -n '/stage table/,/stage unpause/p')" "  FAIL  "
eq "full: the unpause word was not consumed" "$(sed -n 2p "$FX/tty")" unpause
eq "full: session state records the one-shot" "$(sed -n 's/^oneshot=//p' "$H/.eve-session/state")" "$MC"
eq "full: nothing about a key was created (they existed)" "$(calls | grep -c KEYGEN | tr -d ' ')" 0

# ---------------- idempotent re-run, then unpause only on the word ----------------
: >"$C"; printf 'unpause\n' >"$FX/tty"
run $(ARGS_BASE) --rehearse-gateway 5.1.1 --rehearse-web 2.0.1 --unpause
eq "rerun: exit 0" "$RC" 0
eq "rerun: no one-shot" "$(calls | grep -c '^ONESHOT' | tr -d ' ')" 0; eq "rerun: no tag" "$(calls | grep -c '^TAG' | tr -d ' ')" 0
eq "rerun: no deploy" "$(calls | grep -cE '^SU .*deploy ' | tr -d ' ')" 0; eq "rerun: no second rehearsal" "$(calls | grep -cE '^SU .*rehearse' | tr -d ' ')" 0
has "rerun: skipped messages" "$OUT" "is already live: skipped"; has "rerun: tag skipped" "$OUT" "v5.1.0 exists in eve-serving: not touched"
eq "rerun: PAUSE removed after the word" "$([ -e "$ST/PAUSE" ] && echo yes || echo no)" no; has "rerun: unpause call" "$(calls)" "unpause"
# --unpause with the wrong word keeps PAUSE
touch "$ST/PAUSE"; date >"$ST/rehearsal_ok"; : >"$C"; printf 'no\nno\nno\n' >"$FX/tty"
run $(ARGS_BASE) --rehearse-gateway 5.1.1 --rehearse-web 2.0.1 --unpause; eq "unpause: wrong word stops" "$RC" 1; has "unpause: wrong word" "$OUT" "no confirmation: PAUSE stays"
eq "unpause: PAUSE kept" "$([ -e "$ST/PAUSE" ] && echo yes || echo no)" yes
# --unpause without a passed rehearsal is refused
rm -f "$ST/rehearsal_ok"; run $(ARGS_BASE) --unpause; eq "unpause: no rehearsal_ok is refused" "$RC" 1; has "unpause: no rehearsal_ok" "$OUT" "no rehearsal_ok"

# ---------------- keys ----------------
mkenv keys; rm -f "$FX/keysok" "$H/.eve-release-tagger/id_ed25519" "$H/.eve-release-tagger/id_ed25519.pub"
printf 'registered\n' >"$FX/tty"
( sleep 0; ) ; run $(ARGS_BASE)    # keys never become valid in this fake: stops at the keys stage
eq "keys: refused when GitHub does not accept them" "$RC" 1; has "keys: stage" "$OUT" 'STOPPED at stage "keys"'; has "keys: tells where to add" "$OUT" "settings/keys/new"; has "keys: the title" "$OUT" "Title: release-tagger"
has "keys: web key title" "$OUT" "Title: eve-web-release-tagger"; has "keys: write access" "$OUT" "Allow write access"
eq "keys: the missing key was created" "$(calls | grep -c KEYGEN | tr -d ' ')" 1; eq "keys: key file mode" "$(stat -c %a "$H/.eve-release-tagger/id_ed25519" 2>/dev/null || stat -f %Lp "$H/.eve-release-tagger/id_ed25519")" 600
eq "keys: no one-shot after a key failure" "$(calls | grep -c '^ONESHOT' | tr -d ' ')" 0
# the keys become valid while the owner adds them
: >"$FX/keysok"; : >"$C"; run $(ARGS_BASE) --dry-run; eq "keys: dry run does not call ssh/git" "$(calls | grep -c LSREMOTE | tr -d ' ')" 0

# ---------------- fail-closed stages ----------------
mkenv f1; touch "$FX/oneshot-fails"; run $(ARGS_BASE); eq "fail: one-shot failure" "$RC" 1; has "fail: one-shot failure" "$OUT" 'STOPPED at stage "install"'; eq "fail: no tag after a one-shot failure" "$(calls | grep -c '^TAG' | tr -d ' ')" 0
eq "fail: no state after a failed one-shot" "$(grep -c oneshot= "$H/.eve-session/state" | tr -d ' ')" 0
mkenv f2; printf 'tag\n' >"$FX/tty"; touch "$FX/tag-refused"; run $(ARGS_BASE); eq "fail: tag refused" "$RC" 1; has "fail: tag refused" "$OUT" 'STOPPED at stage "tags"'; eq "fail: no deploy after a tag refusal" "$(calls | grep -cE '^SU ' | tr -d ' ')" 0; eq "fail: web tag not tried" "$(calls | grep -c 'repo eve-web' | tr -d ' ')" 0
mkenv f3; echo 2222222222222222222222222222222222222222 >"$FX/tip.eve-serving"; printf 'tag\n' >"$FX/tty"; run $(ARGS_BASE); eq "fail: main moved" "$RC" 1; has "fail: main moved" "$OUT" "main moved"; eq "fail: no tag when main moved" "$(calls | grep -c '^TAG' | tr -d ' ')" 0
# a release that never gets its files: pre-existing tags but no release
mkenv f5; printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; run $(ARGS_BASE) --wait-minutes 0; eq "fail: no signed release in time" "$RC" 1; has "fail: no signed release" "$OUT" 'STOPPED at stage "releases"'; eq "fail: nothing deployed" "$(calls | grep -cE '^SU ' | tr -d ' ')" 0
mkenv f6; printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; echo "eve-gateway-5.1.0.MANIFEST eve-gateway-5.1.0.tar" >"$FX/rel.eve-serving.5.1.0"; echo "eve-web-2.0.0.MANIFEST eve-web-2.0.0.sigstore.json eve-web-2.0.0.tar" >"$FX/rel.eve-web.2.0.0"
run $(ARGS_BASE) --wait-minutes 0; eq "fail: a release without its signature file" "$RC" 1; has "fail: release files" "$OUT" "has no complete signed release"
mkenv f7; printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; echo "eve-gateway-5.1.0.MANIFEST eve-gateway-5.1.0.sigstore.json eve-gateway-5.1.0.tar" >"$FX/rel.eve-serving.5.1.0"; echo "eve-web-2.0.0.MANIFEST eve-web-2.0.0.sigstore.json eve-web-2.0.0.tar" >"$FX/rel.eve-web.2.0.0"
touch "$FX/deploy-fails"; run $(ARGS_BASE) --rehearse-gateway 5.1.1 --rehearse-web 2.0.1 --wait-minutes 0; eq "fail: test releases missing" "$RC" 1; has "fail: test releases missing" "$OUT" 'STOPPED at stage "releases"'; eq "fail: nothing deployed while the test releases are missing" "$(calls | grep -cE '^SU ' | tr -d ' ')" 0
mkenv f8; printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; echo "eve-gateway-5.1.0.MANIFEST eve-gateway-5.1.0.sigstore.json eve-gateway-5.1.0.tar" >"$FX/rel.eve-serving.5.1.0"; echo "eve-web-2.0.0.MANIFEST eve-web-2.0.0.sigstore.json eve-web-2.0.0.tar" >"$FX/rel.eve-web.2.0.0"
touch "$FX/deploy-fails"; run $(ARGS_BASE) --wait-minutes 0; eq "fail: deploy failure stops" "$RC" 1; has "fail: deploy failure" "$OUT" 'STOPPED at stage "deploy"'; eq "fail: the web deploy is not tried after a gateway failure" "$(calls | grep -c 'deploy web' | tr -d ' ')" 0
rm -f "$FX/deploy-fails"; echo 6.0.0 >"$ST/current_version"; run $(ARGS_BASE) --wait-minutes 0; eq "fail: a downgrade is never done" "$RC" 1; has "fail: downgrade" "$OUT" "NEWER than 5.1.0"
# rehearsal failure keeps PAUSE and stops before the table
mkenv f9; printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; for p in "gateway 5.1.0 eve-serving" "web 2.0.0 eve-web" "gateway 5.1.1 eve-serving" "web 2.0.1 eve-web"; do set -- $p; echo "eve-$1-$2.MANIFEST eve-$1-$2.sigstore.json eve-$1-$2.tar" >"$FX/rel.$3.$2"; done
touch "$FX/rehearse-fails"; run $(ARGS_BASE) --rehearse-gateway 5.1.1 --rehearse-web 2.0.1; eq "fail: rehearsal failure" "$RC" 1; has "fail: rehearsal failure" "$OUT" 'STOPPED at stage "rehearse"'; eq "fail: PAUSE not released" "$([ -e "$ST/PAUSE" ] && echo yes || echo no)" yes
# a failing verify makes the table fail and PAUSE stays
mkenv f10; printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; echo "eve-gateway-5.1.0.MANIFEST eve-gateway-5.1.0.sigstore.json eve-gateway-5.1.0.tar" >"$FX/rel.eve-serving.5.1.0"; echo "eve-web-2.0.0.MANIFEST eve-web-2.0.0.sigstore.json eve-web-2.0.0.tar" >"$FX/rel.eve-web.2.0.0"
touch "$FX/verify-fails"; printf 'unpause\n' >"$FX/tty"; run $(ARGS_BASE) --unpause; eq "fail: verify fails the table" "$RC" 1; has "fail: table" "$OUT" "PAUSE is NOT released"
# sudo refused
mkenv f11; touch "$FX/sudo-refused"; run $(ARGS_BASE); eq "fail: sudo refused" "$RC" 1; has "fail: sudo refused" "$OUT" 'STOPPED at stage "sudo"'; eq "fail: nothing after a refused sudo" "$(calls | grep -cE '^(ONESHOT|TAG|SU )' | tr -d ' ')" 0

# ---------------- the switch stage is fail-safe ----------------
okmeasure() { printf '#!/bin/bash\necho "01  OK  fake"\necho "GATES OK: the switch step may run"\nexit 0\n' >"$R/deploy/nopasswd/eve-measure.sh"; }
badmeasure() { printf '#!/bin/bash\necho "03  FAIL  chain"\necho "GATES NOT OK (1): skipped"\nexit 3\n' >"$R/deploy/nopasswd/eve-measure.sh"; }
ready() { printf '%s\n' "eve-serving v5.1.0" "eve-web v2.0.0" >"$FX/tags"; echo "eve-gateway-5.1.0.MANIFEST eve-gateway-5.1.0.sigstore.json eve-gateway-5.1.0.tar" >"$FX/rel.eve-serving.5.1.0"; echo "eve-web-2.0.0.MANIFEST eve-web-2.0.0.sigstore.json eve-web-2.0.0.tar" >"$FX/rel.eve-web.2.0.0"; }
mkenv sw1; ready; badmeasure; repin; recommit; run $(ARGS_BASE) --switch; eq "switch: failed gates skip only the switch" "$RC" 0; has "switch: skipped message" "$OUT" "SKIPPED: the measurement gates are not OK"; eq "switch: the deploy still ran" "$(cat "$ST/current_version")" 5.1.0
mkenv sw2; ready; okmeasure; repin; recommit; run $(ARGS_BASE) --switch; eq "switch: gates ok but no switch script" "$RC" 0; has "switch: not shipped" "$OUT" "the switch script is not shipped"; eq "switch: the deploy still ran (2)" "$(cat "$ST/current_version")" 5.1.0
mkenv sw3; ready; okmeasure; repin; recommit; run $(ARGS_BASE); eq "switch: not requested" "$RC" 0; has "switch: not requested" "$OUT" "not requested (--switch)"
swscript() { printf '#!/bin/bash\necho "SWITCH $*" >>"$FAKE_CALLS"\nexit %s\n' "$1" >"$R/deploy/nopasswd/switch-service-account.sh"; sed -i.bak "s/^PIN_SWITCH_SHA256=[^ ]*/PIN_SWITCH_SHA256=$(sha256 "$R/deploy/nopasswd/switch-service-account.sh")/" "$R/deploy/nopasswd/eve-session.sh"; rm -f "$R/deploy/nopasswd/eve-session.sh.bak"; }
mkenv sw4; ready; okmeasure; swscript 0; repin; recommit; run $(ARGS_BASE) --switch; eq "switch: runs when gates are ok and the script is pinned" "$RC" 0; has "switch: switched" "$OUT" "switched"; has "switch: root call with the merge commit" "$(calls)" "SWITCH --merge-commit $MC"
lt "switch: before the tags and the deploy" "$(calls | grep -n '^SWITCH' | head -1 | cut -d: -f1)" "$(calls | grep -n '^SU ' | head -1 | cut -d: -f1)"
mkenv sw5; ready; okmeasure; swscript 10; repin; recommit; run $(ARGS_BASE) --switch; eq "switch: exit 10 = skipped itself, deploy continues" "$RC" 0; has "switch: skipped itself" "$OUT" "skipped itself"; eq "switch: deploy ran after exit 10" "$(cat "$ST/current_version")" 5.1.0
mkenv sw6; ready; okmeasure; swscript 5; repin; recommit; run $(ARGS_BASE) --switch; eq "switch: a failure after the start stops everything" "$RC" 1; has "switch: failure" "$OUT" 'STOPPED at stage "switch"'; eq "switch: no deploy after a switch failure" "$(calls | grep -cE '^SU ' | tr -d ' ')" 0
mkenv sw7; ready; okmeasure; swscript 0; repin; echo "# tampered" >>"$R/deploy/nopasswd/switch-service-account.sh"; recommit; run $(ARGS_BASE) --switch; eq "switch: a changed switch script is refused at preflight" "$RC" 1; has "switch: stale pin" "$OUT" "switch-service-account.sh does not match the sha256 pinned"

# ---------------- eve-measure.sh ----------------
mm() { # runs the REAL measure script with PATH fakes: $1 = dir with fake commands
  MOUT=$(PATH=$1:/usr/bin:/bin EVE_MEASURE_PATH=$1:/usr/bin:/bin bash "$M" "${@:2}" 2>&1); MRC=$?
}
FM=$T/measure-bin; mkdir -p "$FM"
mm "$FM"; has "measure: prints its own hash and time" "$MOUT" "script-sha256="; has "measure: asks for a result line" "$MOUT" "GATES"
for n in 01 02 03 04 05 06 07 08 09 10 11 12 13 14; do has "measure: item $n present" "$MOUT" "
$n  "; done
# not macOS: the gate 01 is UNKNOWN => skip
if [ "$(uname -s)" != Darwin ]; then eq "measure: not macOS = gate not ok, exit 3" "$MRC" 3; has "measure: skip text" "$MOUT" "the switch step is SKIPPED"; fi
# a fake macOS, arm64, with a good world: sw_vers/uname/csrutil faked; gates 03/04 come from the real filesystem of this box
printf '#!/bin/bash\necho 15.0\n' >"$FM/sw_vers"; printf '#!/bin/bash\nif [ "$1" = -s ]; then echo Darwin; elif [ "$1" = -m ]; then echo arm64; else /usr/bin/uname "$@"; fi\n' >"$FM/uname"; printf '#!/bin/bash\necho "System Integrity Protection status: enabled."\n' >"$FM/csrutil"
printf '#!/bin/bash\nexit 1\n' >"$FM/dscl"; chmod +x "$FM"/*
mm "$FM"; has "measure: fake macOS passes gate 01" "$MOUT" "01  OK"
eq "measure: SIP text is shortened and printed" "$(printf '%s' "$MOUT" | grep -c 'System Integrity')" 1
# no secrets, user names or addresses: the output never contains the user name of this run
eq "measure: output has no user name" "$(printf '%s' "$MOUT" | grep -c "$(id -un)")" 0
# --out writes a copy and keeps the exit status
mm "$FM" --out "$T/m.out"; eq "measure: --out copy equals the output" "$(cat "$T/m.out")" "$MOUT"
mm "$FM" --bogus; eq "measure: unknown option" "$MRC" 1
# writes nothing outside its temp dir: the box tree is unchanged
B1=$(find "$D" -newer "$T/m.out" -type f 2>/dev/null | wc -l | tr -d ' '); eq "measure: no repository file touched" "$B1" 0

echo "session tests passed: $PASS  failed: $FAILN"
[ "$FAILN" = 0 ]
