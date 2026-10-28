#!/bin/bash
# Tests for the passwordless-deploy entrypoint (eve-deploy) and its install/uninstall/sign scripts.
# Runs unprivileged: eve-deploy is SOURCED (it only runs main when executed) and its fixed paths are pointed at a temp dir.
#   bash deploy/nopasswd/test/run.sh
# Needs: bash, ssh-keygen, tar, visudo (sudo package), install, sha256sum or shasum. Runs on Linux and macOS (bash 3.2).
# shellcheck disable=all
set -u
HERE=$(cd "$(dirname "$0")" && pwd)
D=$(cd "$HERE/.." && pwd)
ENTRY=$D/eve-deploy
umask 022
T=$(cd "$(mktemp -d)" && pwd -P); trap 'rm -rf "$T"' EXIT
PASS=0; FAILN=0
ok()   { PASS=$((PASS + 1)); }
bad()  { FAILN=$((FAILN + 1)); echo "FAIL: $*"; }
eq()   { if [ "$2" = "$3" ]; then ok; else bad "$1: got [$2] want [$3]"; fi; }
yes_() { local n=$1; shift; if "$@" >/dev/null 2>&1; then ok; else bad "$n: expected success: $*"; fi; }
no_()  { local n=$1; shift; if "$@" >/dev/null 2>&1; then bad "$n: expected failure: $*"; else ok; fi; }

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }
modeof() { stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1"; }
# shellcheck source=../eve-deploy
. "$ENTRY"

# ---------------- version validation ----------------
for v in 5.0.0 0.0.0 0.0.1 1.2.3 10.20.30 999999999.999999999.999999999; do yes_ "valid $v" valid_version "$v"; done
for v in "" 1 1.2 1.2.3.4 01.2.3 1.02.3 1.2.03 v1.2.3 1.2.3-rc1 1.2.3+b1 "1.2.3 " " 1.2.3" $'1.2.3\n' $'\n1.2.3' 1.2.x -1.2.3 1..3 .1.2 1.2. \
         "1.2.3;id" '$(id).0.0' 1000000000.0.0 1.2.3/../x '1.2.3 5.0.0' 1e3.0.0 0x1.0.0; do no_ "invalid [$v]" valid_version "$v"; done

# ---------------- version comparison (numeric, not lexical) ----------------
cmp() { ver_cmp "$1" "$2"; }
eq "5.0.1 > 5.0.0" "$(cmp 5.0.1 5.0.0)" 1
eq "5.0.0 = 5.0.0" "$(cmp 5.0.0 5.0.0)" 0
eq "4.9.9 < 5.0.0" "$(cmp 4.9.9 5.0.0)" -1
eq "5.10.0 > 5.9.0" "$(cmp 5.10.0 5.9.0)" 1
eq "5.9.0 < 5.10.0" "$(cmp 5.9.0 5.10.0)" -1
eq "10.0.0 > 9.0.0" "$(cmp 10.0.0 9.0.0)" 1
eq "5.0.10 > 5.0.9" "$(cmp 5.0.10 5.0.9)" 1
eq "6.0.0 > 5.99.99" "$(cmp 6.0.0 5.99.99)" 1
eq "0.0.1 > 0.0.0" "$(cmp 0.0.1 0.0.0)" 1
eq "999999999.0.0 > 999999998.9.9" "$(cmp 999999999.0.0 999999998.9.9)" 1
for pair in "5.0.0 bad" "bad 5.0.0" "5.0 5.0.0" "5.0.0 05.0.0" "5.0.0 ''"; do
  # shellcheck disable=SC2086
  set -- $pair; ver_cmp "$1" "$2" >/dev/null 2>&1; eq "cmp malformed [$pair]" "$?" 2
done

# ---------------- argument parsing: only `deploy <ver>` and `rollback` ----------------
yes_ "args deploy 5.0.1" parse_args deploy 5.0.1
eq "action deploy" "$ACTION $TARGET" "deploy 5.0.1"
yes_ "args rollback" parse_args rollback
eq "action rollback" "$ACTION:$TARGET" "rollback:"
no_ "args none" parse_args
no_ "args deploy only" parse_args deploy
no_ "args empty" parse_args ""
no_ "args deploy empty version" parse_args deploy ""
no_ "args rollback extra" parse_args rollback now
no_ "args rollback version" parse_args rollback 5.0.0
no_ "args deploy 3 args" parse_args deploy 5.0.1 extra
no_ "args deploy bad version" parse_args deploy bad
no_ "args deploy 2 parts" parse_args deploy 5.0
no_ "args deploy newline" parse_args deploy $'5.0.1\n'
no_ "args Deploy caps" parse_args Deploy 5.0.1
no_ "args ROLLBACK caps" parse_args ROLLBACK
no_ "args install" parse_args install 5.0.1
no_ "args --help" parse_args --help
no_ "args -h" parse_args -h
no_ "args --" parse_args -- 5.0.1
no_ "args deploy --" parse_args deploy --
no_ "args shell" parse_args sh -c id
no_ "args version first" parse_args 5.0.1 deploy
no_ "args deploy+rollback" parse_args deploy rollback
no_ "args single quoted string" parse_args "deploy 5.0.1"

# executed (not sourced): every bad argument list is rejected with exit 2 before anything else happens
for args in "" "deploy" "rollback x" "deploy 5.0" "deploy 5.0.1 x" "foo" "-h" "deploy 05.0.1"; do
  # shellcheck disable=SC2086
  bash "$ENTRY" $args >/dev/null 2>&1; eq "exec rejects [$args]" "$?" 2
done
bash "$ENTRY" deploy ''  >/dev/null 2>&1; eq "exec rejects empty version" "$?" 2
# well-formed args still stop before touching anything when not root
if [ "$(id -u)" != 0 ]; then bash "$ENTRY" deploy 5.0.1 >/dev/null 2>&1; eq "exec non-root refused" "$?" 1; fi

# ---------------- end to end in a sandbox ----------------
KEY=$T/signer; ssh-keygen -q -t ed25519 -N '' -C test -f "$KEY"
KEY2=$T/other; ssh-keygen -q -t ed25519 -N '' -C other -f "$KEY2"
ME=$(id -un); MYUID=$(id -u)

new_box() { # fresh sandbox: live 5.0.0 binaries, state, signers, staging
  B=$T/box.$1; rm -rf "$B"; mkdir -p "$B/bin" "$B/state" "$B/staging" "$B/libexec" "$B/out"
  echo "gw-old" >"$B/bin/gateway"; echo "eg-old" >"$B/bin/eve-gw"; chmod 0755 "$B/bin/"*
  echo 5.0.0 >"$B/state/current_version"; echo 8 >"$B/state/schema"; echo "$ME" >"$B/state/allowed_user"
  printf 'eve-release namespaces="eve-deploy" %s\n' "$(cut -d' ' -f1,2 "$KEY.pub")" >"$B/libexec/allowed_signers"
  echo "gw-$1" >"$B/gw"; echo "eg-$1" >"$B/eg"
  HEALTH=ok
}
sandbox_env() {
  STATE=$B/state; BIN_DIR=$B/bin; STAGING=$B/staging; SIGNERS=$B/libexec/allowed_signers; LOGF=$B/log
  OWNER_ARGS=""; ROOT_UID=$MYUID; SUDO_USER=$ME; SUDO_UID=$MYUID
  require_root() { :; }
  svc_restart() { echo restart >>"$B/svc"; }
  health_ok() { [ "$(cat "$B/health" 2>/dev/null || echo ok)" = ok ]; }
  # the sandbox has no root: let the "uid 0 / gateway uid" guards see a normal user
  GW_UID=$((MYUID + 100000))
}
sign() { # $1 version  $2 schema  [key] [namespace]
  local egarg="--eve-gw $B/eg"; [ -z "${NOEG:-}" ] || egarg=""
  bash "$D/sign-deploy-bundle.sh" --key "${3:-$KEY}" --gateway "$B/gw" $egarg --version "$1" --schema "$2" --out "$B/out" >/dev/null 2>&1
  if [ -n "${4:-}" ]; then rm -f "$B/out/eve-$1.tar.sig"; ssh-keygen -Y sign -f "${3:-$KEY}" -n "$4" "$B/out/eve-$1.tar" >/dev/null 2>&1; fi
}
stage() { cp "$B/out/eve-$1.tar" "$B/staging/eve-$2.tar"; cp "$B/out/eve-$1.tar.sig" "$B/staging/eve-$2.tar.sig"; }
run_main() { ( sandbox_env; main "$@" ) >"$B/stdout" 2>"$B/stderr"; }
live() { echo "$(cat "$B/bin/gateway")/$(cat "$B/bin/eve-gw")/$(cat "$B/state/current_version")"; }

# 1. happy path: deploy higher version, previous is kept, work dir is gone, lock is released
new_box 1; sign 5.0.1 8; stage 5.0.1 5.0.1
run_main deploy 5.0.1; eq "deploy ok rc" "$?" 0
eq "deploy ok live" "$(live)" "gw-1/eg-1/5.0.1"
eq "previous version kept" "$(cat "$B/state/previous/version")" 5.0.0
eq "previous binary kept" "$(cat "$B/state/previous/gateway")" gw-old
eq "service restarted" "$(wc -l <"$B/svc" | tr -d ' ')" 1
no_ "work dir removed" bash -c "ls $B/state | grep -q '^work\.'"
[ ! -d "$B/state/lock" ] && ok || bad "lock released"
# 1b. rollback goes back to the one kept version, then there is nothing left to roll back to
run_main rollback; eq "rollback rc" "$?" 0
eq "rollback live" "$(live)" "gw-old/eg-old/5.0.0"
[ ! -e "$B/state/previous" ] && ok || bad "previous consumed by rollback"
run_main rollback; eq "second rollback refused" "$?" 1
# 1c. after a rollback, deploying again needs a higher version than the (now lower) current one
run_main deploy 5.0.0; eq "deploy same version refused" "$?" 1

# 2. downgrade / same version / lower version refused, live untouched
new_box 2; sign 5.0.0 8; stage 5.0.0 5.0.0
run_main deploy 5.0.0; eq "same version refused" "$?" 1
sign 4.9.9 8; stage 4.9.9 4.9.9
run_main deploy 4.9.9; eq "lower version refused" "$?" 1
sign 5.0.1 8; stage 5.0.1 5.0.1; echo 5.0.2 >"$B/state/current_version"
run_main deploy 5.0.1; eq "lower than installed 5.0.2 refused" "$?" 1
eq "live untouched after refusals" "$(cat "$B/bin/gateway")" gw-old
# numeric comparison in the real flow: 5.10.0 installed, 5.9.0 refused, 5.11.0 accepted
echo 5.10.0 >"$B/state/current_version"; sign 5.9.0 8; stage 5.9.0 5.9.0
run_main deploy 5.9.0; eq "5.9.0 < installed 5.10.0 refused" "$?" 1
sign 5.11.0 8; stage 5.11.0 5.11.0; run_main deploy 5.11.0; eq "5.11.0 > 5.10.0 accepted" "$?" 0

# 3. tampered bundle (changed after signing) refused
new_box 3; sign 5.0.1 8; echo evil >"$B/gw"; mkdir "$B/t" && ( cd "$B/t" && tar -xf "$B/out/eve-5.0.1.tar" && echo evil >gateway && tar -cf "$B/out/eve-5.0.1.tar" MANIFEST eve-gw gateway )
stage 5.0.1 5.0.1; run_main deploy 5.0.1; eq "tampered refused" "$?" 1; eq "tampered: live untouched" "$(live)" "gw-old/eg-old/5.0.0"

# 4. wrong key / wrong namespace refused
new_box 4; sign 5.0.1 8 "$KEY2"; stage 5.0.1 5.0.1; run_main deploy 5.0.1; eq "other signing key refused" "$?" 1
new_box 5; sign 5.0.1 8 "$KEY" other-namespace; stage 5.0.1 5.0.1; run_main deploy 5.0.1; eq "wrong namespace refused" "$?" 1
eq "wrong key/ns: live untouched" "$(live)" "gw-old/eg-old/5.0.0"

# 5. bundle signed for another version cannot be staged under this version's name
new_box 6; sign 5.0.2 8; stage 5.0.2 5.0.1; run_main deploy 5.0.1; eq "manifest version mismatch refused" "$?" 1

# 6. schema change is not for this path
new_box 7; sign 5.0.1 9; stage 5.0.1 5.0.1; run_main deploy 5.0.1; eq "schema mismatch refused" "$?" 1

# 7. staging: symlink and missing signature refused; bundle with extra member refused
new_box 8; sign 5.0.1 8; stage 5.0.1 5.0.1; rm "$B/staging/eve-5.0.1.tar"; ln -s "$B/out/eve-5.0.1.tar" "$B/staging/eve-5.0.1.tar"
run_main deploy 5.0.1; eq "symlinked staging file refused" "$?" 1
new_box 9; sign 5.0.1 8; cp "$B/out/eve-5.0.1.tar" "$B/staging/eve-5.0.1.tar"; run_main deploy 5.0.1; eq "missing .sig refused" "$?" 1
new_box 10; sign 5.0.1 8; mkdir "$B/t" && ( cd "$B/t" && tar -xf "$B/out/eve-5.0.1.tar" && echo x >extra && tar -cf "$B/out/eve-5.0.1.tar" MANIFEST eve-gw gateway extra )
ssh-keygen -Y sign -f "$KEY" -n eve-deploy "$B/out/eve-5.0.1.tar" >/dev/null 2>&1; stage 5.0.1 5.0.1
run_main deploy 5.0.1; eq "extra member refused" "$?" 1

# 8. TOCTOU: the staged file is swapped AFTER the copy; the install must still use the verified copy
new_box 11; sign 5.0.1 8; stage 5.0.1 5.0.1
(
  sandbox_env
  orig_verify=$(declare -f verify_sig)
  eval "orig_${orig_verify#verify_sig }"           # keep the original as orig_verify_sig
  verify_sig() {
    echo "mode:$(modeof "$(dirname "$1")")" >"$B/workmode"
    case "$1" in "$STATE"/work.*/*) echo inside-state >"$B/workloc";; esac
    echo garbage >"$STAGING/eve-5.0.1.tar"; echo garbage >"$STAGING/eve-5.0.1.tar.sig"    # attacker swaps the originals now
    orig_verify_sig "$@"
  }
  main deploy 5.0.1
) >/dev/null 2>&1; eq "TOCTOU: deploy used the verified copy" "$?" 0
eq "TOCTOU: live is the signed content" "$(live)" "gw-11/eg-11/5.0.1"
eq "verify ran in a 0700 work dir" "$(cat "$B/workmode")" "mode:700"
eq "work dir is under the root-only state dir" "$(cat "$B/workloc")" inside-state

# 9. health failure: automatic restore of the previous binaries, version unchanged, old previous not overwritten
new_box 12; sign 5.0.1 8; stage 5.0.1 5.0.1; echo bad >"$B/health"
( sandbox_env; health_ok() { [ -f "$B/restored" ] ; }; svc_restart() { [ "$(cat "$B/bin/gateway")" = gw-old ] && touch "$B/restored"; echo r >>"$B/svc"; }; main deploy 5.0.1 ) >/dev/null 2>&1
eq "unhealthy deploy: rc 1" "$?" 1
eq "unhealthy deploy: old binaries restored" "$(live)" "gw-old/eg-old/5.0.0"
[ ! -e "$B/state/previous" ] && ok || bad "unhealthy deploy must not create 'previous'"

# 10. rollback guards: schema differs, kept version not lower, kept hash mismatch
new_box 13; sign 5.0.1 8; stage 5.0.1 5.0.1; run_main deploy 5.0.1
echo 9 >"$B/state/previous/schema"; run_main rollback; eq "rollback across schema refused" "$?" 1
echo 8 >"$B/state/previous/schema"; echo 5.0.1 >"$B/state/previous/version"; run_main rollback; eq "rollback to non-lower refused" "$?" 1
echo 5.0.0 >"$B/state/previous/version"; echo tamper >"$B/state/previous/gateway"; run_main rollback; eq "rollback with tampered kept binary refused" "$?" 1
eq "live untouched by refused rollbacks" "$(live)" "gw-13/eg-13/5.0.1"
new_box 14; run_main rollback; eq "rollback with nothing kept refused" "$?" 1

# 11. caller guards
new_box 15; sign 5.0.1 8; stage 5.0.1 5.0.1
( sandbox_env; SUDO_USER=root; main deploy 5.0.1 ) >/dev/null 2>&1; eq "root caller refused" "$?" 1
( sandbox_env; SUDO_USER=_evegw; main deploy 5.0.1 ) >/dev/null 2>&1; eq "_evegw caller refused" "$?" 1
( sandbox_env; SUDO_USER=nobody; main deploy 5.0.1 ) >/dev/null 2>&1; eq "other user refused" "$?" 1
( sandbox_env; SUDO_UID=0; main deploy 5.0.1 ) >/dev/null 2>&1; eq "SUDO_UID mismatch refused" "$?" 1
( sandbox_env; SUDO_USER=""; main deploy 5.0.1 ) >/dev/null 2>&1; eq "no SUDO_USER refused" "$?" 1
mkdir "$B/state/lock"; run_main deploy 5.0.1; eq "concurrent deploy refused" "$?" 1; rmdir "$B/state/lock"
eq "caller guards: live untouched" "$(live)" "gw-old/eg-old/5.0.0"
# the log never holds key material or paths of the private key
if grep -qE 'PRIVATE KEY|BEGIN OPENSSH' "$B/log" 2>/dev/null; then bad "log leaks key text"; else ok; fi

# 12. eve-gw is optional: gateway-only bundle on a gateway-only host, and on a host that has eve-gw
new_box 16; rm -f "$B/bin/eve-gw"; NOEG=1 sign 5.0.1 8; stage 5.0.1 5.0.1
run_main deploy 5.0.1; eq "gateway-only deploy rc" "$?" 0
eq "gateway-only deploy: gateway live" "$(cat "$B/bin/gateway")" gw-16
[ ! -e "$B/bin/eve-gw" ] && ok || bad "gateway-only host must not gain eve-gw"
run_main rollback; eq "gateway-only rollback rc" "$?" 0
eq "gateway-only rollback: gateway back" "$(cat "$B/bin/gateway")" gw-old
new_box 17; NOEG=1 sign 5.0.1 8; stage 5.0.1 5.0.1
run_main deploy 5.0.1; eq "gateway-only bundle on host with eve-gw: rc" "$?" 0
eq "bundle without eve-gw leaves a live eve-gw alone" "$(cat "$B/bin/eve-gw")" eg-old
run_main rollback; eq "…and rollback restores it exactly" "$(live)" "gw-old/eg-old/5.0.0"
# a bundle that ADDS eve-gw to a gateway-only host: rollback removes it again
new_box 18; rm -f "$B/bin/eve-gw"; sign 5.0.1 8; stage 5.0.1 5.0.1
run_main deploy 5.0.1; eq "bundle adds eve-gw: rc" "$?" 0
eq "bundle adds eve-gw: installed" "$(cat "$B/bin/eve-gw")" eg-18
run_main rollback; eq "rollback of added eve-gw: rc" "$?" 0
[ ! -e "$B/bin/eve-gw" ] && ok || bad "rollback must remove an eve-gw the rolled-back version added"
# failed health check on a gateway-only host also leaves no eve-gw behind
new_box 19; rm -f "$B/bin/eve-gw"; sign 5.0.1 8; stage 5.0.1 5.0.1
( sandbox_env; health_ok() { [ -f "$B/restored" ] ; }; svc_restart() { [ "$(cat "$B/bin/gateway")" = gw-old ] && touch "$B/restored"; echo r >>"$B/svc"; }; main deploy 5.0.1 ) >/dev/null 2>&1
eq "failed health, gateway-only host: rc" "$?" 1
[ ! -e "$B/bin/eve-gw" ] && eq "failed health: old gateway restored" "$(cat "$B/bin/gateway")" gw-old || bad "failed health left eve-gw behind"
# manifest listing eve-gw without the member is refused
new_box 20; sign 5.0.1 8; mkdir "$B/t" && ( cd "$B/t" && tar -xf "$B/out/eve-5.0.1.tar" && rm eve-gw && tar -cf "$B/out/eve-5.0.1.tar" MANIFEST gateway ) \
  && ssh-keygen -Y sign -f "$KEY" -n eve-deploy "$B/out/eve-5.0.1.tar" >/dev/null 2>&1 || true
rm -f "$B/out/eve-5.0.1.tar.sig"; ssh-keygen -Y sign -f "$KEY" -n eve-deploy "$B/out/eve-5.0.1.tar" >/dev/null 2>&1; stage 5.0.1 5.0.1
run_main deploy 5.0.1; eq "manifest lists eve-gw but bundle has none: refused" "$?" 1

# 13. BIN_DIR must be root-owned and closed to group/world writes (checked at deploy time)
new_box 21; sign 5.0.1 8; stage 5.0.1 5.0.1; chmod 0777 "$B/bin"
run_main deploy 5.0.1; eq "world-writable BIN_DIR refused" "$?" 1
chmod 0775 "$B/bin"; run_main deploy 5.0.1; eq "group-writable BIN_DIR refused" "$?" 1
chmod 0755 "$B/bin"; chmod 0777 "$B"; run_main deploy 5.0.1; eq "writable parent of BIN_DIR refused" "$?" 1; chmod 0755 "$B"
( sandbox_env; ROOT_UID=$((MYUID + 1)); main deploy 5.0.1 ) >/dev/null 2>&1; eq "BIN_DIR not root-owned refused" "$?" 1
eq "refused BIN_DIR: live untouched" "$(live)" "gw-old/eg-old/5.0.0"
chmod 0755 "$B/bin"; run_main deploy 5.0.1; eq "closed BIN_DIR accepted" "$?" 0

# 14. a TERM/INT/HUP during the swap must not leave new binaries live with old state
new_box 22; sign 5.0.1 8; stage 5.0.1 5.0.1
( sandbox_env; svc_restart() { echo restart >>"$B/svc"; sh -c 'kill -TERM $PPID; kill -INT $PPID; kill -HUP $PPID'; sleep 1; }; main deploy 5.0.1 ) >/dev/null 2>&1
eq "signals during the swap: deploy still completes" "$?" 0
eq "signals during the swap: consistent end state" "$(live)" "gw-22/eg-22/5.0.1"

# 14b. outside the swap window (waiting for / reading the staged files) a TERM/HUP aborts at once and cleans up the lock and work dir
for sig in TERM HUP; do
  new_box "30$sig"; sign 5.0.1 8; stage 5.0.1 5.0.1
  ( sandbox_env; copy_staged() { sleep 30; }; main deploy 5.0.1 ) >/dev/null 2>&1 &
  pid=$!
  for _ in $(seq 1 100); do [ -d "$B/state/lock" ] && break; sleep 0.1; done
  [ -d "$B/state/lock" ] && ok || bad "$sig test: the deploy never reached the staged-file copy"
  kill -$sig $pid 2>/dev/null
  for _ in $(seq 1 50); do kill -0 $pid 2>/dev/null || break; sleep 0.1; done
  if kill -0 $pid 2>/dev/null; then bad "$sig outside the swap window must abort the deploy"; kill -9 $pid 2>/dev/null; else ok; fi
  { wait $pid; } 2>/dev/null   # reap quietly (bash prints "Hangup"/"Terminated" otherwise)
  [ ! -d "$B/state/lock" ] && ok || bad "$sig outside the swap window: lock dir left behind"
  eq "$sig outside the swap window: no work dir left" "$(ls "$B/state" | grep -c '^work\.')" 0
  eq "$sig outside the swap window: live untouched" "$(live)" "gw-old/eg-old/5.0.0"
done 2>/dev/null   # bash reports the killed background job ("Hangup"/"Terminated") on stderr: expected here
# the window itself is only the swap: the file must ignore the signals in swap_window() and nowhere else
eq "signals are ignored in exactly one place" "$(grep -c "trap '' INT" "$ENTRY")" 1
eq "swap_window is entered once per action" "$(grep -c '^  swap_window$' "$ENTRY")" 2

# 14c. `#!/bin/bash -p`: the executed file ignores BASH_ENV and exported functions even when the caller's environment is NOT reset
eq "shebang is #!/bin/bash -p" "$(head -1 "$ENTRY")" '#!/bin/bash -p'
new_box 31; cp "$ENTRY" "$B/ed"; chmod 0755 "$B/ed"
printf 'echo pwned-benv >"%s/pwn-benv"\n' "$B" >"$B/benv"
( env -i PATH=/usr/bin:/bin BASH_ENV="$B/benv" bash "$B/ed" rollback ) >/dev/null 2>&1
[ -e "$B/pwn-benv" ] && ok || bad "control: plain bash must run BASH_ENV (otherwise this test proves nothing)"
rm -f "$B/pwn-benv"
( env -i PATH=/usr/bin:/bin BASH_ENV="$B/benv" "$B/ed" rollback ) >/dev/null 2>&1; eq "executed via shebang: rc (non-root refused)" "$?" 1
[ ! -e "$B/pwn-benv" ] && ok || bad "BASH_ENV ran although the shebang is bash -p"
for fn in builtin '[' printf; do
  ( env -i PATH=/usr/bin:/bin "BASH_FUNC_${fn}%%=() { echo pwned >\"$B/pwn-fn\"; }" "$B/ed" rollback ) >/dev/null 2>&1
  [ ! -e "$B/pwn-fn" ] && ok || bad "exported function $fn ran"
done

# 15. relative --out of the signing script
new_box 23; ( cd "$B" && bash "$D/sign-deploy-bundle.sh" --key "$KEY" --gateway gw --eve-gw eg --version 5.0.1 --schema 8 --out out/rel ) >/dev/null 2>&1; eq "sign with relative --out: rc" "$?" 0
[ -s "$B/out/rel/eve-5.0.1.tar" ] && [ -s "$B/out/rel/eve-5.0.1.tar.sig" ] && ok || bad "relative --out must produce tar + sig"
( cd "$B" && bash "$D/sign-deploy-bundle.sh" --key "$KEY" --gateway gw --version 5.0.1 --schema 8 --out . ) >/dev/null 2>&1; eq "sign with --out . and no eve-gw: rc" "$?" 0
eq "no-eve-gw bundle members" "$(tar -tf "$B/eve-5.0.1.tar" | LC_ALL=C sort | tr '\n' ' ')" "MANIFEST gateway "
cp "$B/out/rel/eve-5.0.1.tar" "$B/staging/eve-5.0.1.tar"; cp "$B/out/rel/eve-5.0.1.tar.sig" "$B/staging/eve-5.0.1.tar.sig"
run_main deploy 5.0.1; eq "relatively-signed bundle deploys" "$?" 0

# 16. the entrypoint starts from a clean environment and ignores inherited functions (executed path)
new_box 24
( env -i PATH=/usr/bin:/bin BASH_FUNC_printf%%='() { echo pwned >"'"$B"'/pwn"; }' EVE_STATE=/nonexistent bash "$ENTRY" rollback ) >/dev/null 2>&1; eq "executed as non-root: rc" "$?" 1
[ ! -e "$B/pwn" ] && ok || bad "inherited exported function ran"

# ---------------- setup / uninstall (sandboxed; real run needs root + macOS) ----------------
S=$T/setup; mkdir -p "$S/sudoers.d"; echo "#includedir $S/sudoers.d" >"$S/sudoers"
SHA=$(sha256 "$ENTRY")
FP=$(ssh-keygen -l -f "$KEY.pub" | awk '{print $2}')
mkdir -p "$S/bin"; echo gw >"$S/bin/gateway"
setup_env() { env EVE_LIBEXEC=$S/libexec EVE_STATE=$S/state EVE_SUDOERS_D=$S/sudoers.d EVE_SUDOERS_MAIN=$S/sudoers EVE_LOGF=$S/log \
  EVE_OWNER="$ME:$(id -gn)" EVE_SKIP_ROOT=1 EVE_CHAIN_UID=$MYUID EVE_CHAIN_STOP=$T EVE_BIN_DIR=$S/bin EVE_SKIP_FULL_VISUDO=1 SUDO_USER="$ME" "$@"; }
run_setup() { setup_env bash "$D/setup-deploy-nopasswd.sh" "$@" >"$S/out" 2>&1; }
no_ "setup: no args" run_setup
no_ "setup: wrong sha" run_setup --expect-sha256 0000 --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
no_ "setup: bad version" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0 --schema 8
no_ "setup: bad schema" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema x
no_ "setup: private key refused as pubkey" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY" --current 5.0.0 --schema 8
no_ "setup: unknown arg" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8 --evil 1
( setup_env SUDO_USER=root bash "$D/setup-deploy-nopasswd.sh" --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8 ) >/dev/null 2>&1; eq "setup: root user refused" "$?" 1
[ ! -e "$S/sudoers.d/eve-deploy" ] && ok || bad "failed setups must install nothing"
yes_ "setup: ok" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
F=$S/sudoers.d/eve-deploy
yes_ "sudoers snippet passes visudo -c" /usr/sbin/visudo -c -q -f "$F"
eq "sudoers: exactly one rule line + one Defaults" "$(grep -vc '^#' "$F")" 2
eq "sudoers: rule is user + exact path" "$(grep -v '^#' "$F" | grep -c "^$ME ALL=(root) NOPASSWD: NOSETENV: $S/libexec/deploy\$")" 1
if grep -v '^#' "$F" | grep -Eq '[*?]|[ :]SETENV:|, *ALL|: *ALL *$|\(ALL'; then bad "sudoers has wildcard / ALL command"; else ok; fi
eq "sudoers: env_reset, no setenv" "$(grep -c "^Defaults!$S/libexec/deploy env_reset, !setenv\$" "$F")" 1
eq "sudoers mode 0440" "$(modeof "$F")" 440
eq "entrypoint mode 0755" "$(modeof "$S/libexec/deploy")" 755
eq "allowed_signers mode 0644" "$(modeof "$S/libexec/allowed_signers")" 644
eq "allowed_signers is namespace-pinned" "$(cut -d' ' -f1,2 "$S/libexec/allowed_signers")" 'eve-release namespaces="eve-deploy"'
eq "state dir 0700" "$(modeof "$S/state")" 700
eq "staging dir 0700" "$(modeof "$S/libexec/staging")" 700
eq "state seeded" "$(cat "$S/state/current_version")/$(cat "$S/state/schema")/$(cat "$S/state/allowed_user")" "5.0.0/8/$ME"
eq "installed entrypoint == reviewed file" "$(sha256 "$S/libexec/deploy")" "$SHA"
# a bundle signed by the key that setup pinned verifies through the real ssh-keygen path
( SIGNERS=$S/libexec/allowed_signers; ROOT_UID=$MYUID; B=$T/box.1; SUDO_USER=$ME; verify_sig "$B/out/eve-5.0.1.tar" "$B/out/eve-5.0.1.tar.sig" ) >/dev/null 2>&1; eq "setup-pinned key verifies a signed bundle" "$?" 0

# N4: setup leaves no temp files behind: neither the copy dir (eve-setup.*) nor the sudoers temp (eve-sudoers.*), on success or failure
mkdir -p "$S/tmp"
( setup_env TMPDIR="$S/tmp" bash "$D/setup-deploy-nopasswd.sh" --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8 ) >/dev/null 2>&1; eq "setup (tmp check): rc" "$?" 0
eq "setup success leaves no temp files" "$(ls -A "$S/tmp" | wc -l | tr -d ' ')" 0
( setup_env TMPDIR="$S/tmp" bash "$D/setup-deploy-nopasswd.sh" --expect-sha256 0000 --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8 ) >/dev/null 2>&1; eq "setup (bad sha): rc" "$?" 1
eq "setup failure leaves no temp files" "$(ls -A "$S/tmp" | wc -l | tr -d ' ')" 0
eq "setup has exactly one EXIT trap" "$(grep -c '^trap .* EXIT' "$D/setup-deploy-nopasswd.sh")" 1

# N3: with uid 0 the EVE_* test knobs are dropped before they are read (the tests cannot run as root, so the block is run with a fake `id`)
for f in setup-deploy-nopasswd.sh uninstall-deploy-nopasswd.sh; do
  blk=$(sed -n '/^# --- drop EVE_\* overrides when running as root ---$/,/^# --- end drop ---$/p' "$D/$f" | grep -v '^#')
  [ -n "$blk" ] && ok || bad "$f: root EVE_* block missing"
  r=$( id() { echo 0; }; export EVE_STATE=/x EVE_BIN_DIR=/y EVE_SKIP_ROOT=1 KEEP_ME=1; eval "$blk"; echo "${EVE_STATE-unset}/${EVE_BIN_DIR-unset}/${EVE_SKIP_ROOT-unset}/${KEEP_ME-}" )
  eq "$f: root drops EVE_*" "$r" "unset/unset/unset/1"
  r=$( id() { echo 1000; }; export EVE_STATE=/x; eval "$blk"; echo "${EVE_STATE-unset}" )
  eq "$f: non-root keeps EVE_* (tests)" "$r" "/x"
  # the block must come before the first read of any EVE_ variable
  first=$(grep -n 'EVE_[A-Z_]*:-\|EVE_[A-Z_]*:=' "$D/$f" | head -1 | cut -d: -f1); endb=$(grep -n '^# --- end drop ---$' "$D/$f" | cut -d: -f1)
  [ "$endb" -lt "$first" ] && ok || bad "$f: EVE_* is read before the root drop"
done
# uninstall: sudoers goes first and always; --purge is the only thing that deletes state
run_un() { setup_env bash "$D/uninstall-deploy-nopasswd.sh" "$@" >"$S/out" 2>&1; }
no_ "uninstall: unknown arg" run_un --everything
yes_ "uninstall: plain" run_un
[ ! -e "$F" ] && [ ! -e "$S/libexec/deploy" ] && [ ! -e "$S/libexec/allowed_signers" ] && ok || bad "uninstall removes sudoers, entrypoint, signers"
[ -d "$S/state" ] && ok || bad "plain uninstall keeps state"
yes_ "uninstall: purge" run_un --purge
[ ! -e "$S/state" ] && ok || bad "purge removes state"

# ---- setup: H1/H2/M1/M3 -- the chain check runs for real (no skip), fingerprint is required, copies are what gets installed ----
rm -rf "$S/libexec" "$S/state" "$S/log" "$S/sudoers.d/eve-deploy"
no_ "setup: fingerprint required" run_setup --expect-sha256 "$SHA" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
no_ "setup: wrong fingerprint refused" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "SHA256:$(printf 'A%.0s' $(seq 1 43))" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
no_ "setup: malformed fingerprint refused" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "SHA256:short" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
no_ "setup: other key's fingerprint refused" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$(ssh-keygen -l -f "$KEY2.pub" | awk '{print $2}')" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
[ ! -e "$S/libexec" ] && ok || bad "refused setups must not create anything"
yes_ "setup prints the fingerprint" run_setup --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8
grep -q "signing key fingerprint: $FP" "$S/out" && ok || bad "setup must print the fingerprint"
eq "log file mode 0600 (no install /dev/null)" "$(modeof "$S/log")" 600
if grep -n 'install .*/dev/null' "$D/setup-deploy-nopasswd.sh" | grep -v '^[0-9]*:[[:space:]]*#' | grep -v 'BSD install rejects' | grep -q .; then bad "setup must not 'install … /dev/null' (BSD install rejects it)"; else ok; fi
# chain: group-writable parent, a parent that resolves (through a symlink) to a world-writable dir, wrong owner, missing gateway, BIN_DIR perms
CH=$T/chain; mkdir -p "$CH/real/lib" "$CH/ww" "$CH/bin"; chmod 0777 "$CH/ww"; echo gw >"$CH/bin/gateway"
chain_setup() { # $1 libexec  $2 state  [env…]
  local l=$1 st=$2; shift 2
  env EVE_LIBEXEC="$l" EVE_STATE="$st" EVE_SUDOERS_D=$S/sudoers.d EVE_SUDOERS_MAIN=$S/sudoers EVE_LOGF=$S/log2 EVE_OWNER="$ME:$(id -gn)" EVE_SKIP_ROOT=1 \
    EVE_CHAIN_UID=$MYUID EVE_CHAIN_STOP=$T EVE_BIN_DIR=$CH/bin EVE_SKIP_FULL_VISUDO=1 SUDO_USER="$ME" "$@" \
    bash "$D/setup-deploy-nopasswd.sh" --expect-sha256 "$SHA" --expect-key-fingerprint "$FP" --pubkey "$KEY.pub" --current 5.0.0 --schema 8 >"$S/out" 2>&1; }
yes_ "chain: normal sandbox passes" chain_setup "$CH/ok/libexec" "$CH/ok/state"
chmod 0775 "$CH/real"; no_ "chain: group-writable parent refused" chain_setup "$CH/real/libexec" "$CH/real/state"; chmod 0755 "$CH/real"
ln -s "$CH/real" "$CH/ln"; yes_ "chain: symlinked parent that resolves to a closed dir is fine (macOS /var)" chain_setup "$CH/ln/libexec" "$CH/ln/state"
ln -s "$CH/ww" "$CH/lnw"; no_ "chain: symlinked parent resolving to a world-writable dir refused" chain_setup "$CH/lnw/libexec" "$CH/lnw/state"
mkdir -p "$CH/fin"; ln -s "$CH/real/lib" "$CH/fin/libexec"; no_ "chain: install dir itself a symlink refused" chain_setup "$CH/fin/libexec" "$CH/fin/state"
no_ "chain: wrong owner refused" chain_setup "$CH/own/libexec" "$CH/own/state" EVE_CHAIN_UID=$((MYUID + 1))
no_ "chain: BIN_DIR without gateway refused" chain_setup "$CH/nb/libexec" "$CH/nb/state" EVE_BIN_DIR=$CH/ww
chmod 0775 "$CH/bin"; no_ "chain: group-writable BIN_DIR refused" chain_setup "$CH/gb/libexec" "$CH/gb/state"; chmod 0755 "$CH/bin"
# without the stop dir the real /tmp (world-writable) ancestors make the chain fail: the check is live, not skipped
no_ "chain: runs for real (sandbox under a world-writable parent refused)" chain_setup "$CH/rl/libexec" "$CH/rl/state" EVE_CHAIN_STOP=

# ---------------- static checks on the entrypoint ----------------
if grep -nE '\beval\b|\bsource\b|^\s*\. |\bsh -c\b|\bbash -c\b' "$ENTRY" | grep -v '^[0-9]*:\s*#' | grep -q .; then bad "entrypoint must not eval/source/sh -c"; else ok; fi
if grep -nE '(^|[ ;(`|&])(rm|cp|mv|install|chmod|chown|tar|mktemp|head|cat|sed|awk|grep|sort|tr|id|curl|stat|ssh-keygen|launchctl|logger|seq|sleep|date|shasum|sha256sum|mkdir|rmdir) ' "$ENTRY" | grep -vE '^[0-9]+:\s*#' | grep -vE '/(usr/)?(s?bin)/' | grep -E '^[0-9]+:\s*[a-z]' | grep -vE '^[0-9]+:\s*(local|log|die|case|if|for|\[)' | grep -q .; then
  echo "note: review unqualified command names in eve-deploy"; fi
no_ "entrypoint reads no getenv-style overrides" grep -nE '\$\{(LIBEXEC|STATE|SIGNERS|BIN_DIR|STAGING|LOGF)[:=-]' "$ENTRY"
eq "entrypoint is executable bash 3.2-safe (no assoc arrays / \${,,})" "$(grep -cE 'declare -A|\$\{[A-Za-z_]+(,,|\^\^)\}|mapfile|readarray' "$ENTRY")" 0

# N5: the one-liner runs the setup with an absolute bash (sudo on macOS has no secure_path)
no_ "README one-liner has no bare 'sudo bash'" grep -nE 'sudo bash' "$D/README.md"
yes_ "README one-liner uses sudo /bin/bash" grep -q 'sudo /bin/bash setup-deploy-nopasswd.sh' "$D/README.md"
# the README must not claim what the code does not do
no_ "README no longer says signals are ignored from the lock" grep -n 'from the moment the lock is taken' "$D/README.md"

# the README one-liner pins the real hashes of the three root-run scripts
for f in eve-deploy setup-deploy-nopasswd.sh uninstall-deploy-nopasswd.sh; do
  grep -q "$(sha256 "$D/$f")" "$D/README.md" && ok || bad "README hash for $f is stale (update the one-line install command)"
done

echo "passed: $PASS  failed: $FAILN"
[ "$FAILN" = 0 ]
