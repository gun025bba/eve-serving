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

Everything else exits `2`. It does not trust the environment (`env -i`, fixed `PATH`, absolute command paths, `LC_ALL=C`).
Only the recorded deploy user may call it (not root-by-itself, not `_evegw`; checked again against `SUDO_UID`). Nothing in the
gateway or over HTTP can trigger it.

`deploy` steps: copy the two staged files into a **root-only `mktemp -d` under `/var/db/eve-deploy` (umask 077)** (regular
file, not a symlink, owned by the deploy user, size-capped) → **verify the copy, never the staged original**
(`ssh-keygen -Y verify`, pinned key, namespace `eve-deploy`) → unpack the copy (exactly `MANIFEST`, `eve-gw`, `gateway`, all
regular files) → manifest `version` equals the argument, `schema` equals the installed schema (a schema change goes through the
normal migration deploy script), hashes match → snapshot the live binaries → atomic replace (`root:wheel 0755`, never writable
by `_evegw`) → `launchctl kickstart -k` → `/health` must return `{"status":"ok"}` → on failure the snapshot is restored
automatically. The database, config and secrets are never touched and no migration (up or down) runs.

## One-time install (the host owner, with sudo)

On the signing machine (**not the Mac server**), create the key once and keep the private key there:

```
ssh-keygen -t ed25519 -f eve-deploy-signing -C eve-deploy-release     # passphrase recommended
```

Copy only `eve-deploy-signing.pub` to the Mac. Then, from a checkout at the reviewed commit:

```
sha256sum deploy/nopasswd/eve-deploy        # shasum -a 256 on macOS; compare with the reviewed value
sudo bash deploy/nopasswd/setup-deploy-nopasswd.sh \
  --expect-sha256 <that sha256> --pubkey eve-deploy-signing.pub --current <installed X.Y.Z> --schema <newest goose version>
```

Setup refuses to continue if the entrypoint hash differs, the key is not a single ed25519 public key, any parent directory is
not root-owned or is group/world writable, or `visudo -c` fails (it then installs nothing / removes its sudoers file).

## Releasing and deploying

On the signing machine:

```
bash deploy/nopasswd/sign-deploy-bundle.sh --key eve-deploy-signing --gateway <gateway> --eve-gw <eve-gw> \
  --version 5.0.1 --schema 8 --out out/
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

## Tests

`bash deploy/nopasswd/test/run.sh` (also a CI step): argument whitelist, version validation and comparison (malformed,
leading zeros, 5.9.0 vs 5.10.0), signatures (wrong key, wrong namespace, tampered, wrong name), TOCTOU (staged file swapped
after the copy), health-failure restore, rollback guards, caller guards, and the setup/uninstall output (sudoers content, modes).
