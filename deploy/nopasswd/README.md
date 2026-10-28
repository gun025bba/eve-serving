# Passwordless deploy (optional, 5.0.0)

Deploys of a **signed** release no longer need a sudo password. This is a privilege-boundary change, so it is an
**optional step the host owner approves by running the setup script himself**. Nothing here is installed by the
normal 5.0.0 deploy.

## Design

| Item | Value |
|---|---|
| Entrypoint | `/usr/local/libexec/eve/deploy` (source: `deploy/nopasswd/eve-deploy`), `root:wheel 0755` |
| Pinned public key | `/usr/local/libexec/eve/allowed_signers`, `root:wheel 0644`, namespace-pinned (`eve-deploy`), one ed25519 key |
| sudoers | `/etc/sudoers.d/eve-deploy`, `root:wheel 0440`, validated with `visudo -c` before and after install |
| Staging (written by the deploy user) | `/usr/local/libexec/eve/staging/eve-<X.Y.Z>.tar` + `.tar.sig` (dir `0700`, owned by the deploy user) |
| State (root only) | `/var/db/eve-deploy` (`0700`): `current_version`, `schema`, `allowed_user`, `previous/` |
| Log | `/var/log/eve-deploy.log` (`0600`) + syslog tag `eve-deploy`; versions and hashes only, no secrets |

sudoers (two lines, no wildcard, no `SETENV`, env reset):

```
Defaults!/usr/local/libexec/eve/deploy env_reset, !setenv
<deploy user> ALL=(root) NOPASSWD: NOSETENV: /usr/local/libexec/eve/deploy
```

The entrypoint accepts **only**:

- `deploy <X.Y.Z>` where `X.Y.Z` is strictly higher than the installed version (numeric compare; `^(0|[1-9][0-9]{0,8})(\.…){2}$`,
  no leading zeros, no suffixes), and
- `rollback` to the **one** previous version it kept (must be lower, same DB schema, hashes unchanged). No second rollback.

Everything else exits `2`. It does not trust the environment (every inherited variable and imported shell function is dropped at the top of the script, fixed `PATH`, absolute command paths, `LC_ALL=C`; sudoers also resets the environment).
Only the recorded deploy user may call it (not root-by-itself, not `_evegw`; checked again against `SUDO_UID`). Nothing in the
gateway or over HTTP can trigger it.

`deploy` steps: copy the two staged files into a **root-only `mktemp -d` under `/var/db/eve-deploy` (umask 077)** (regular
file, not a symlink, owned by the deploy user, size-capped) → **verify the copy, never the staged original**
(`ssh-keygen -Y verify`, pinned key, namespace `eve-deploy`) → unpack the copy (exactly `MANIFEST` + `gateway`, and optionally `eve-gw`; all
regular files) → manifest `version` equals the argument, `schema` equals the installed schema (a schema change goes through the
normal migration deploy script), hashes match → check `/opt/eve-gw` and `/opt/eve-gw/bin` are root-owned and closed to group/world writes → snapshot the live binaries → atomic replace (`root:wheel 0755`, never writable
by `_evegw`) → `launchctl kickstart -k` → `/health` must return `{"status":"ok"}` → on failure the snapshot is restored
automatically (signals are ignored from the moment the lock is taken until the swap has been checked or restored, so a Ctrl-C cannot leave new binaries live with old state). The database, config and secrets are never touched and no migration (up or down) runs.

### What is in a bundle (and what is not)

- **`gateway`** is required: the runbooks install only `/opt/eve-gw/bin/gateway`.
- **`eve-gw`** is **optional**. It is replaced only if the bundle carries it. A bundle without it leaves a live `eve-gw` untouched;
  `rollback` restores exactly what the kept version had (and removes an `eve-gw` that the rolled-back version had added).
- **The web UI (`/opt/eve-gw/web`) is deliberately not part of the bundle.** It stays on the existing UI deploy path
  (runbook §3: `cp -RP web/. /opt/eve-gw/web/` with the symlink check). Reason: a recursive tree copy as root from a staged archive
  needs its own symlink/ownership handling and review; this entrypoint stays a two-binary swap. A release that changes both
  binary and UI needs the UI step done separately (with sudo, as today).

## One-time install (the host owner, with sudo)

On the signing machine (**not the Mac server**), create the key once and keep the private key there:

```
ssh-keygen -t ed25519 -f eve-deploy-signing -C eve-deploy-release     # passphrase recommended
```

Copy only `eve-deploy-signing.pub` to the Mac, and read its fingerprint **on the signing machine** (not on the Mac):

```
ssh-keygen -l -f eve-deploy-signing.pub        # prints SHA256:<43 chars> ...  (compare it out-of-band with what the Mac shows)
```

Then, from a checkout at the reviewed commit, run this **one line** (one `sudo`, at the very end). It stops before `sudo` if the
checkout is not the reviewed commit, is dirty, or any of the three scripts differs from the hashes below:

```
cd <checkout> && [ "$(git rev-parse HEAD)" = <REVIEWED_COMMIT> ] && git diff --quiet HEAD -- deploy/nopasswd && cd deploy/nopasswd && printf '%s  %s\n' 9ac53befd1397270d75b2b68197a5b4b93163d62da14ce75c47c0e81c8b8c356 setup-deploy-nopasswd.sh 2e73f98a0de55f46203f1725b650b08e36ba56ce0bc9fc7a2e1a376101d2fa03 eve-deploy d2a39c20ff12219d609d50a4c87f5f2149062924e815896b801653a402a1949d uninstall-deploy-nopasswd.sh | shasum -a 256 -c - && sudo bash setup-deploy-nopasswd.sh --expect-sha256 2e73f98a0de55f46203f1725b650b08e36ba56ce0bc9fc7a2e1a376101d2fa03 --expect-key-fingerprint <SHA256:fingerprint> --pubkey <path>/eve-deploy-signing.pub --current <installed X.Y.Z> --schema <newest goose version>
```

`<REVIEWED_COMMIT>` is the full commit SHA announced with the review (a README cannot contain its own commit). The three hashes
are the reviewed `sha256` of `setup-deploy-nopasswd.sh`, `eve-deploy` and `uninstall-deploy-nopasswd.sh` at that commit.

Setup refuses to continue if: the entrypoint hash differs; the key is not a single ed25519 public key or its fingerprint differs
from `--expect-key-fingerprint` (it prints the fingerprint it found); any parent of `/usr/local/libexec/eve`, `/var/db/eve-deploy`
or `/opt/eve-gw/bin` is not root-owned or is group/world writable (paths are resolved physically first, so macOS's `/var` ->
`/private/var` symlink is handled; the install directories themselves must not be symlinks); `/opt/eve-gw/bin/gateway` does not
exist; or `visudo -c` fails (it then installs nothing / removes its sudoers file). The entrypoint and the key are first copied to a
root-only temp directory and **only the verified copies are hashed, syntax-checked and installed**, so a swap of the checkout
files after the check cannot change what gets installed.

## Releasing and deploying

On the signing machine:

```
bash deploy/nopasswd/sign-deploy-bundle.sh --key eve-deploy-signing --gateway <gateway> --eve-gw <eve-gw> \
  --version 5.0.1 --schema 8 --out out/
# --eve-gw is optional (omit it for a gateway-only release); --out may be relative
scp out/eve-5.0.1.tar out/eve-5.0.1.tar.sig <host>:/usr/local/libexec/eve/staging/
```

On the Mac, no password:

```
sudo /usr/local/libexec/eve/deploy deploy 5.0.1
sudo /usr/local/libexec/eve/deploy rollback
```

## Remove

```
sudo bash deploy/nopasswd/uninstall-deploy-nopasswd.sh            # sudoers first, then entrypoint + pinned key; keeps state and log
sudo bash deploy/nopasswd/uninstall-deploy-nopasswd.sh --purge    # also deletes /var/db/eve-deploy, staging and the log
sudo -n -l /usr/local/libexec/eve/deploy                           # must now ask for a password / not be allowed
```

Equivalent by hand: `sudo rm /etc/sudoers.d/eve-deploy`.

## Operations notes

- **Stale lock / work dirs.** A deploy that was killed hard (power loss, `kill -9`) can leave `/var/db/eve-deploy/lock` (a directory)
  and `work.*` dirs. Check no deploy is running (`ps aux | grep eve/deploy`), then `sudo rmdir /var/db/eve-deploy/lock` and
  `sudo rm -rf /var/db/eve-deploy/work.*`. Normal exits and Ctrl-C clean both up.
- **State after a manual / migration deploy.** The entrypoint tracks `current_version` and `schema` in `/var/db/eve-deploy`. After
  a deploy done the old way (with sudo) or a release that changes the DB schema, update them (root only):
  `echo <X.Y.Z> | sudo tee /var/db/eve-deploy/current_version` and `echo <N> | sudo tee /var/db/eve-deploy/schema`; delete
  `/var/db/eve-deploy/previous` (a kept version from before a schema change must not be rolled back to; the entrypoint also
  refuses it when schemas differ).
- **Health wait** is up to 60 s after `launchctl kickstart -k`.
- **Known limits.** The staged files are checked (`regular file`, owner, size) and then opened by path. The deploy user swapping a
  staged file for a FIFO between those two steps can only make the deploy hang (Ctrl-C works); it can never get unsigned content
  installed, because everything that is installed was copied first and verified afterwards. `BASH_ENV`/`SHELLOPTS` are stripped by
  sudo's `env_reset`.

## Tests

`bash deploy/nopasswd/test/run.sh` (also a CI step): argument whitelist, version validation and comparison (malformed,
leading zeros, 5.9.0 vs 5.10.0), signatures (wrong key, wrong namespace, tampered, wrong name), TOCTOU (staged file swapped
after the copy), health-failure restore, optional `eve-gw`, `BIN_DIR` guards, signals during the swap, relative `--out`, rollback guards,
caller guards, the setup chain check (symlinked parents, writable parents, wrong owner, with no skip switch), the key fingerprint,
and the setup/uninstall output (sudoers content, modes). CI runs it on Ubuntu **and on `macos-latest`** (system `/bin/bash` 3.2, real
BSD `install`/`tar`/`stat`, `ssh-keygen -Y`, and `visudo -c` against a temp file; nothing runs under sudo).
