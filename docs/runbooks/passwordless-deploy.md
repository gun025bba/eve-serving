# Runbook: passwordless deploy, verification on the Mac

Applies to the optional CI-signed passwordless deploy (`deploy/nopasswd/`; install, releases: `deploy/nopasswd/README.md`; design and
threat model: `docs/deploy-signing.md`).

CI proves the scripts in a user-level sandbox (Ubuntu and macOS, bash 3.2, BSD tools) with real CI-signed fixtures. It does **not** prove the real
`sudo`, `/etc/sudoers`, `/var/db` and the real ownership chain on the host. So after the one-line install, walk through the checklist below **on the
Mac**, with a console (or a second admin terminal) kept open. Run every `sudo` step yourself.

**If any step fails: run `sudo rm /etc/sudoers.d/eve-deploy` immediately (the NOPASSWD rule is gone at once), then report the result. Do not work around a failed step.**

## Checklist

1. **Before the install (read-only).**
   - `sw_vers; uname -m` (must be `arm64`); `/bin/bash --version | head -1; sudo -V | head -1`.
   - `ls -ld / /usr /usr/local /var /private /private/var /private/var/db /opt /opt/eve-gw /opt/eve-gw/bin`: every entry is root-owned with no group/world write. If
     `/usr/local` is user-owned (Homebrew on Intel), setup must **refuse**; if it refuses, do not work around it, report.
   - `ls -l /opt/eve-gw/bin/gateway` (a regular file, not a symlink) and `sudo grep -n includedir /etc/sudoers` (`/etc/sudoers.d` is included).
   - Back up the live binaries: `sudo cp -p /opt/eve-gw/bin/gateway ~/gateway.pre-eve-deploy` (same for `eve-gw` if present).
   - Compare the install line's commit with the merged PR, then run it **once**. Nothing else is typed: no key, no fingerprint.
2. **State after the install** (check it yourself, not only from the installer's output).
   - `sudo ls -ld /usr/local/libexec/eve /private/var/db/eve-deploy` (root:wheel; the state dir is `0700`; staging belongs to you).
   - `ls -l /usr/local/libexec/eve/` shows `deploy`, `fetch`, `uninstall`, `cosign`, `trusted_root.json` as root:wheel and not group/world writable.
   - `shasum -a 256 /usr/local/libexec/eve/deploy /usr/local/libexec/eve/cosign /usr/local/libexec/eve/trusted_root.json` equal the pins printed by the installer.
   - `sudo visudo -c` says "parsed OK"; `cat /etc/sudoers.d/eve-deploy` shows **one user, one exact path, four fixed verbs**, `NOSETENV`, no other command.
3. **Scope of the sudo rule.**
   - `sudo -k; sudo -n /usr/local/libexec/eve/deploy bogus; echo $?` runs without a password and rejects the argument.
   - `sudo -k; sudo -n /bin/true; echo $?` needs a password (rc not 0): NOPASSWD applies to that one path only.
   - `sudo -n -E /usr/local/libexec/eve/deploy rollback` and `sudo -n FOO=1 ...` are refused (no `SETENV`).
4. **Signature trust path (no service impact).** With a released version already **installed or older**, run
   `/usr/local/libexec/eve/fetch gateway <that version>` then `sudo /usr/local/libexec/eve/deploy deploy <that version>`: it must be refused with a "not higher than installed" message
   (the signature check already passed, the ratchet stops it). Edit one byte of the staged `.tar` and repeat with a newer, unreleased number: it must fail the signature or sha check.
5. **First real deploy in a low-risk window.** Release a patch version from CI (tag a commit on `main`), then `fetch gateway X.Y.Z && sudo .../deploy deploy X.Y.Z`. Check
   `launchctl print system/com.llmserving.gateway | head`, `curl -s http://127.0.0.1:8000/health` returns exactly `{"status":"ok"}`, and the log under `/var/db/eve-deploy` is `0600`.
   Then `sudo .../deploy rollback` once and confirm it goes back (only one previous version is kept). Repeat for the UI with `fetch web` / `deploy-web` / `rollback-web`.
6. **Removal path.** `sudo rm /etc/sudoers.d/eve-deploy` alone removes NOPASSWD at once: check with `sudo -k; sudo -n -l`. Afterwards, if wanted, run
   `sudo /usr/local/libexec/eve/uninstall` (`--purge` deletes state: only on purpose).
7. **Anything off: stop.** Run `sudo rm /etc/sudoers.d/eve-deploy` right away, then share the result. No workarounds.
