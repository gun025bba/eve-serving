# Runbook: passwordless deploy, verification on the Mac

Applies to the optional passwordless-deploy path (`deploy/nopasswd/`, design and install: `deploy/nopasswd/README.md`).

CI proves the scripts in a user-level sandbox (Ubuntu and macOS, bash 3.2, BSD tools). It does **not** prove the real
`sudo`, `/etc/sudoers`, `/var/db` and the real ownership chain on the host. So after the one-line install, walk through the
checklist below **on the Mac**, with a console (or a second admin terminal) kept open. Run every `sudo` step yourself.

**If any step fails: run `sudo rm /etc/sudoers.d/eve-deploy` immediately (the NOPASSWD rule is gone at once), then report
the result. Do not try to work around a failed step.**

## Checklist (7 steps)

1. **Before the install (read-only).**
   - `sw_vers; /bin/bash --version | head -1; sudo -V | head -1; /usr/bin/ssh-keygen -Y 2>&1 | head -2`
     (`-Y` support means OpenSSH 8.1 or newer).
   - `ls -ld / /usr /usr/local /var /private /private/var /private/var/db /opt /opt/eve-gw /opt/eve-gw/bin`: every entry is
     root-owned with no group/world write. If `/usr/local` is user-owned (Homebrew on Intel), setup must **refuse**; if it
     refuses, do not work around it, report.
   - `ls -l /opt/eve-gw/bin/gateway` (a regular file, not a symlink) and `sudo grep -n includedir /etc/sudoers`
     (`/etc/sudoers.d` is included).
   - Back up the live binaries: `sudo cp -p /opt/eve-gw/bin/gateway ~/gateway.pre-eve-deploy` (same for `eve-gw` if present).
   - Read the signing key fingerprint **on the signing machine** with `ssh-keygen -l -f eve-deploy-signing.pub` and paste it in.
     After setup, compare the `signing key fingerprint:` line it prints with that value by eye.
2. **State after the one-line install** (check it yourself, not only from setup's output).
   - `sudo ls -ld /usr/local/libexec/eve /private/var/db/eve-deploy` (root:wheel; the state dir is `0700`; staging is `0700`).
   - `ls -l /usr/local/libexec/eve/deploy /usr/local/libexec/eve/allowed_signers /etc/sudoers.d/eve-deploy` shows
     `-rwxr-xr-x root wheel` / `-rw-r--r-- root wheel` / `-r--r----- root wheel`.
   - `shasum -a 256 /usr/local/libexec/eve/deploy` equals the `eve-deploy` hash pinned in the README one-liner.
   - `sudo visudo -c` says "parsed OK", and `cat /etc/sudoers.d/eve-deploy` shows **one user and one exact path**, no wildcard,
     no `SETENV`, `env_reset` / `!setenv` kept.
3. **Scope of the sudo rule.**
   - `sudo -k; sudo -n /usr/local/libexec/eve/deploy bogus; echo $?` runs without a password and rejects the argument (rc 2).
   - `sudo -k; sudo -n /bin/true; echo $?` says "a password is required" (rc not 0): NOPASSWD applies to that one path only.
   - `sudo -k; sudo -n -E /usr/local/libexec/eve/deploy rollback` and `sudo -n FOO=1 ...` are refused or ignored (no `SETENV`).
4. **Signature trust path (no service impact).** On the signing machine sign any file `x`:
   `ssh-keygen -Y sign -f <key> -n eve-deploy x`. On the Mac:
   `/usr/bin/ssh-keygen -Y verify -f /usr/local/libexec/eve/allowed_signers -I eve-release -n eve-deploy -s x.sig < x`
   must succeed, and the same with `-n other` must fail.
5. **First real deploy in a low-risk window.** Use a **signed, identical binary** with only the patch version raised, then
   `sudo /usr/local/libexec/eve/deploy deploy <X.Y.Z>`. Check `launchctl print system/com.llmserving.gateway | head`,
   `curl -s http://127.0.0.1:8000/health` returns exactly `{"status":"ok"}`, and the log under `/var/db/eve-deploy` is `0600`.
   Then run `sudo /usr/local/libexec/eve/deploy rollback` once and confirm it goes back (only one rollback is kept).
6. **Removal path.** `sudo rm /etc/sudoers.d/eve-deploy` alone removes NOPASSWD at once: check with `sudo -k; sudo -n -l`.
   Afterwards, if wanted, run `uninstall-deploy-nopasswd.sh` (`--purge` deletes state: only on purpose).
7. **Anything off: stop.** Run `sudo rm /etc/sudoers.d/eve-deploy` right away, then share the result. No workarounds.
