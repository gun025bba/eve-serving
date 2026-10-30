#!/bin/bash
# eve-measure.sh: the READ-ONLY measurement of this Mac before the switch to root-free deploys (ticket "[OPS] sudo zero", section H, 14 items). Run by eve-session.sh first, or alone:
#   bash deploy/nopasswd/eve-measure.sh [--out FILE]          (bash, not zsh; no sudo, no network, nothing is changed)
# Output: one line per item "NN  STATUS  name: detail"; STATUS is OK, FAIL, INFO or UNKNOWN (UNKNOWN = not readable without root or the tool is missing: it is never guessed).
# GATE items decide whether the switch step may run: 01 (macOS on Apple silicon), 03 (the trust chain of paths), 04 (link swap works, same volume, room to copy), 09 (a gateway binary that exists must pass codesign).
# A gate that is FAIL or UNKNOWN means: the switch step is SKIPPED and the deploy goes through the existing root path (exit status 3). Everything else is information for the reviewers.
# Safety: no write outside one mktemp -d (the link-swap probe) and the optional --out file; no sudo, no launchctl start/stop, no network; file CONTENTS of keys, settings and databases are never read (path, owner, mode only);
# environment variable VALUES are never printed (keys only); user names, host names and addresses are not printed (numeric uid/gid only). The script prints its own sha256 and the time (Asia/Seoul).
# shellcheck disable=SC2015
set -u
export LC_ALL=C
export PATH=${EVE_MEASURE_PATH:-/usr/bin:/bin:/usr/sbin:/sbin:/usr/local/bin:/opt/homebrew/bin}
OUT_FILE=""
while [ $# -gt 0 ]; do case "$1" in --out) OUT_FILE=${2:-}; [ -n "$OUT_FILE" ] || { echo "eve-measure: --out needs a file" >&2; exit 1; }; shift 2;; *) echo "usage: eve-measure.sh [--out FILE]" >&2; exit 1;; esac; done
T=$(mktemp -d) || exit 1; trap 'rm -rf "$T"' EXIT
GATE_BAD=0
sha_of() { if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | cut -d' ' -f1; else sha256sum "$1" | cut -d' ' -f1; fi; }
emit() { # $1 number  $2 status  $3 gate(0/1)  $4 text
  printf '%s  %-7s %s\n' "$1" "$2" "$4"
  if [ "$3" = 1 ] && [ "$2" != OK ]; then GATE_BAD=$((GATE_BAD + 1)); fi
}
have() { command -v "$1" >/dev/null 2>&1; }
# owner uid, octal mode, file type word (BSD stat first, GNU stat second)
if stat --version >/dev/null 2>&1; then
  st() { stat -c '%u %a %F' "$1" 2>/dev/null | sed 's/ /_/3g'; }; dev_of() { stat -c %d "$1" 2>/dev/null; }
else
  st() { stat -f '%u %Lp %HT' "$1" 2>/dev/null | sed 's/ /_/3g'; }; dev_of() { stat -f %d "$1" 2>/dev/null; }
fi
vendor_mask() { sed -e 's#/Users/[^/ ]*#/Users/<user>#g'; }

main() {
printf 'eve-measure  %s  script-sha256=%s\n' "$(TZ=Asia/Seoul date '+%Y-%m-%d %H:%M %Z')" "$(sha_of "$0" 2>/dev/null || echo unknown)"

# 01 OS, architecture, SIP (gate: macOS on Apple silicon)
if have sw_vers && [ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ]; then
  emit 01 OK 1 "os: macOS $(sw_vers -productVersion 2>/dev/null) arm64, $(csrutil status 2>/dev/null | head -1 | cut -c1-60)"
else emit 01 UNKNOWN 1 "os: not macOS on Apple silicon ($(uname -s) $(uname -m))"; fi

# 02 accounts and every service group's membership (numeric ids only)
for a in _evegw evedeploy evebackup _evepg _evemlx; do
  if have dscl; then
    if u=$(dscl . -read "/Users/$a" UniqueID PrimaryGroupID UserShell 2>/dev/null); then emit 02 INFO 0 "account $a: $(printf '%s' "$u" | tr '\n' ' ' | sed 's/UniqueID: /uid /;s/PrimaryGroupID: /gid /;s/UserShell: /shell /')"
    else emit 02 INFO 0 "account $a: absent"; fi
    if g=$(dscl . -read "/Groups/$a" GroupMembership 2>/dev/null); then emit 02 INFO 0 "group $a members: $(printf '%s' "$g" | sed 's/GroupMembership: *//' | wc -w | tr -d ' ') (names not printed)"; fi
  else emit 02 UNKNOWN 0 "account $a: dscl is not available"; fi
done

# 03 the trust chain of paths (gate): every EXISTING component must be a real directory owned by root, not group/world writable, with no ACL. Components below /var, /etc, /tmp are not used in the chain (they are symlinks on macOS).
bad=0; seen=0
for p in / /opt /opt/eve-gw /opt/eve-gw/bin /opt/eve-gw/web /usr /usr/local /usr/local/libexec /usr/local/libexec/eve /Library /Library/LaunchDaemons /Library/LaunchDaemons/com.llmserving.evepoll.plist /opt/eve-pim; do
  if [ ! -e "$p" ] && [ ! -L "$p" ]; then emit 03 INFO 0 "chain $p: absent (created by the install)"; continue; fi
  seen=$((seen + 1)); s=$(st "$p") || { emit 03 UNKNOWN 1 "chain $p: not readable"; bad=1; continue; }
  set -- $s; uid=$1; mode=$2; kind=$3
  acl=$(ls -ledO@ "$p" 2>/dev/null | sed -n '2,$p' | grep -c '^ *[0-9]*:' || true); [ -n "$acl" ] || acl=0
  problem=""
  [ -L "$p" ] && problem="$problem symlink"
  [ "$uid" = 0 ] || problem="$problem owner-uid-$uid"
  m=$((8#$mode)); [ $((m & 8#022)) = 0 ] || problem="$problem group-or-world-writable($mode)"
  [ "$acl" = 0 ] || problem="$problem acl($acl)"
  if [ -n "$problem" ]; then emit 03 FAIL 1 "chain $p: mode $mode uid $uid:$problem"; bad=1; else emit 03 OK 0 "chain $p: mode $mode uid 0 no acl"; fi
done
[ "$bad" = 0 ] && emit 03 OK 1 "chain: $seen existing components are root-owned, not writable by group/others, no ACL, no symlink" || emit 03 FAIL 1 "chain: at least one component fails (see above)"

# 04 link swap (gate): mv -fh replaces a directory link in place, source and target on one volume, room to copy
d=$T/swap; mkdir -p "$d/a" "$d/b"; : >"$d/a/x"; : >"$d/b/y"; ln -s a "$d/cur"; ln -s b "$d/next"
if mv -fh "$d/next" "$d/cur" 2>/dev/null && [ "$(readlink "$d/cur")" = b ] && [ ! -e "$d/b/b" ] && [ ! -e "$d/a/next" ]; then emit 04 OK 1 "link swap: mv -fh replaces the link itself (not the directory behind it)"
else emit 04 FAIL 1 "link swap: mv -fh did not swap the link (this mv has no -h, or follows the link)"; fi
base=/opt; [ -d /opt/eve-gw ] && base=/opt/eve-gw; [ -d "$base" ] || base=/
if [ "$(dev_of "$base")" = "$(dev_of /)" ] || [ -z "$(dev_of "$base")" ]; then emit 04 OK 0 "volume: $base is on the volume of /"; else emit 04 INFO 0 "volume: $base is on its own volume (the link swap stays inside it)"; fi
sz=0; for p in /opt/eve-gw /usr/local/libexec/eve; do [ -d "$p" ] && sz=$((sz + $(du -sk "$p" 2>/dev/null | cut -f1))); done
free=$(df -k "$base" 2>/dev/null | awk 'NR==2{print $4}'); free=${free:-0}
if [ "$free" -gt $((sz * 3 + 1048576)) ]; then emit 04 OK 1 "space: ${free} KB free, installed trees ${sz} KB (needs 3x + 1 GB)"; else emit 04 FAIL 1 "space: ${free} KB free, installed trees ${sz} KB (needs 3x + 1 GB)"; fi

# 05 gateway plist: keys only
pl=""; for c in /Library/LaunchDaemons/com.llmserving.gateway.plist "$HOME/Library/LaunchAgents/com.llmserving.gateway.plist"; do [ -f "$c" ] && pl=$c && break; done
if [ -n "$pl" ] && have plutil; then
  emit 05 INFO 0 "gateway plist $(printf '%s' "$pl" | vendor_mask): $(st "$pl"); keys: $(plutil -p "$pl" 2>/dev/null | sed -n 's/^  "\([A-Za-z]*\)" =>.*/\1/p' | tr '\n' ' ')"
  emit 05 INFO 0 "gateway plist env keys (values never printed): $(plutil -extract EnvironmentVariables raw -o - "$pl" 2>/dev/null | sed 's/=.*//' | tr '\n' ' ')"
else emit 05 UNKNOWN 0 "gateway plist: not found or plutil missing"; fi

# 06 listeners
if have lsof; then emit 06 INFO 0 "listeners 8000/8080/5432: $(lsof -nP -iTCP:8000 -iTCP:8080 -iTCP:5432 -sTCP:LISTEN 2>/dev/null | awk 'NR>1{print $1" uid:"$3" "$9}' | sort -u | tr '\n' ';')"
else emit 06 UNKNOWN 0 "listeners: lsof missing"; fi

# 07 key, kill-switch and database settings: path, owner, mode only (contents are never read)
for p in /opt/eve-gw/etc /var/db/eve-deploy /var/db/eve-deploy/allowed_user; do
  if [ -e "$p" ]; then emit 07 INFO 0 "file $p: $(st "$p")"; else emit 07 INFO 0 "file $p: absent or not visible"; fi
done

# 08 sizes, file counts and the time one hash pass takes
for p in /opt/eve-gw/bin /opt/eve-gw/web; do
  if [ -d "$p" ]; then
    t0=$(date +%s); n=$(find "$p" -type f 2>/dev/null | wc -l | tr -d ' '); k=$(du -sk "$p" 2>/dev/null | cut -f1)
    find "$p" -type f -exec shasum -a 256 {} + >/dev/null 2>&1 || true
    emit 08 INFO 0 "tree $p: ${k:-?} KB, ${n} files, one hash pass $(( $(date +%s) - t0 )) s"
  else emit 08 INFO 0 "tree $p: absent"; fi
done

# 09 code signature, Gatekeeper, quarantine of an existing gateway binary (gate only when one exists)
gw=""; for c in /opt/eve-gw/bin/gateway /opt/eve-gw/bin/eve-gateway; do [ -f "$c" ] && gw=$c && break; done
if [ -z "$gw" ]; then emit 09 INFO 0 "gateway binary: none at the new location yet (nothing to check)"
elif have codesign; then
  if codesign --verify --strict "$gw" >/dev/null 2>&1; then emit 09 OK 1 "gateway binary $gw: code signature valid; quarantine: $(xattr -l "$gw" 2>/dev/null | grep -c quarantine)"; else emit 09 FAIL 1 "gateway binary $gw: code signature does not verify"; fi
  have spctl && emit 09 INFO 0 "gatekeeper: $(spctl --assess --type execute "$gw" 2>&1 | head -1 | cut -c1-80)"
else emit 09 UNKNOWN 1 "gateway binary $gw: codesign missing"; fi

# 10 baseline of the present root path: sudoers file and tools (list, owner, mode, sha256)
for p in /etc/sudoers.d/eve-deploy /usr/local/libexec/eve/deploy /usr/local/libexec/eve/fetch /usr/local/libexec/eve/poll; do
  if [ -f "$p" ]; then emit 10 INFO 0 "file $p: $(st "$p") sha256 $(sha_of "$p" 2>/dev/null || echo unreadable)"; else emit 10 INFO 0 "file $p: absent or not readable without root"; fi
done

# 11 GUI session and the calendar adapter's LaunchAgent
if have launchctl; then emit 11 INFO 0 "gui session: $(launchctl print "gui/$(id -u)" >/dev/null 2>&1 && echo present || echo none); eve-pim agent: $(launchctl print "gui/$(id -u)/com.llmserving.evepim" >/dev/null 2>&1 && echo loaded || echo not-loaded)"
else emit 11 UNKNOWN 0 "gui session: launchctl missing"; fi

# 12 PostgreSQL and mlx baseline (paths and sizes, never contents)
emit 12 INFO 0 "postgres: $(lsof -nP -iTCP:5432 -sTCP:LISTEN 2>/dev/null | awk 'NR>1{print $1" uid:"$3}' | sort -u | tr '\n' ';')  mlx: $(ps -axo uid=,comm= 2>/dev/null | grep -i '[m]lx' | awk '{print "uid:"$1}' | sort -u | tr '\n' ';')"

# 13 FileVault and boot evidence
if have fdesetup; then emit 13 INFO 0 "filevault: $(fdesetup status 2>/dev/null | head -1 | cut -c1-60); boot: $(sysctl -n kern.boottime 2>/dev/null | sed 's/.*sec = \([0-9]*\).*/\1/')"
else emit 13 UNKNOWN 0 "filevault: fdesetup missing"; fi

# 14 tailscale serve and pf (pfctl needs root: recorded as UNKNOWN)
if have tailscale; then emit 14 INFO 0 "tailscale serve: $(tailscale serve status 2>&1 | sed -e 's#https\?://[^ ]*#<url>#g' | head -3 | tr '\n' ' ' | cut -c1-100)"; else emit 14 UNKNOWN 0 "tailscale: not installed or not on PATH"; fi
if [ -e /etc/pf.conf ]; then emit 14 INFO 0 "pf.conf: $(st /etc/pf.conf); pfctl rules: not measurable without root"; else emit 14 INFO 0 "pf.conf: absent"; fi

if [ "$GATE_BAD" = 0 ]; then echo "GATES OK: the switch step may run"; else echo "GATES NOT OK ($GATE_BAD): the switch step is SKIPPED, the deploy uses the existing root path"; fi
}
main >"$T/out" 2>&1
cat "$T/out"
[ -z "$OUT_FILE" ] || cp "$T/out" "$OUT_FILE" || { echo "eve-measure: cannot write $OUT_FILE" >&2; exit 1; }
[ "$GATE_BAD" = 0 ] || exit 3
exit 0
