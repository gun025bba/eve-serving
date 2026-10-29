# Passwordless, CI-signed deploy (`deploy/nopasswd/`)

Optional path: after **one** install line, a deploy of a *released, CI-signed* gateway or UI needs no password, no laptop and no
key. The signature is produced by GitHub Actions (Sigstore keyless); the Mac only **verifies**. Design, threat model and the repo
settings this relies on: [`docs/deploy-signing.md`](../../docs/deploy-signing.md). Checks to run on the real Mac after installing:
[`docs/runbooks/passwordless-deploy.md`](../../docs/runbooks/passwordless-deploy.md).

## One-time install (the only thing a person types)

Run this **once**, in a terminal on the Mac, as the user that will deploy (it asks for your sudo password once). It downloads the
installer from a fixed commit, refuses to continue unless its sha256 matches, and hands exactly the checked text to `sudo /bin/bash -s`:

```
s=$(curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/gun025bba/eve-serving/77ab92731d9ee77d1fda63947a6261bba78b8c04/deploy/nopasswd/setup-deploy-nopasswd.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = e4e574abffbce0c0f48eff68524c6ccb428b7b5e36cc790f0bc6b30da29edaf7 ] && printf '%s\n' "$s" | sudo /bin/bash -s
```

It installs, root-owned and not writable by anyone else: `/usr/local/libexec/eve/{deploy,fetch,uninstall,cosign,trusted_root.json}`
(`cosign` and the trust root are pinned by sha256 inside the installer), a state directory under `/private/var/db/eve-deploy`, a
staging directory owned by you, and `/etc/sudoers.d/eve-deploy` with **one user, one exact path, four exact verbs (no wildcard), no SETENV**:

```
deploy   deploy-web   rollback   rollback-web
```

No key is generated, copied or read. The installer refuses if a directory in the chain is user-writable, if `visudo -c` fails, or if the
live version cannot be determined. Options (append after `-s --`): `--schema N`, `--current X.Y.Z`, `--current-web X.Y.Z`.
Remove it: `sudo /usr/local/libexec/eve/uninstall` (or `sudo rm /etc/sudoers.d/eve-deploy`, which ends the NOPASSWD rule at once).

**One-shot for the Mac** (install + the `eve-web` repository settings + the web tag key, one command, `--dry-run` first if you like): `bash deploy/nopasswd/mac-oneshot.sh` (from a clone) or the clone-free line below. It runs the install line above
(same pins), logs in to GitHub in the browser in a throwaway `gh` config, applies and verifies the `eve-web` settings (`docs/deploy-signing.md`, item 6) and prints a PASS/FAIL table. `kind=web` deploys stay
**refused** by the installed entrypoint (`WEB_ENABLED=no`) until a reviewed change turns them on.

```bash
s=$(curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/gun025bba/eve-serving/8a1c2441ab8ffd72f382c1b2cfca7d00f7338120/deploy/nopasswd/mac-oneshot.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = eb54046bf3880ac311b962697f00837c6a671937c5099b5bd06fd1f9c45ad2d5 ] && bash -c "$s" mac-oneshot
```

Run it in a terminal (bash or zsh; it needs the terminal for the browser login and the final `revoked` prompt, hence `bash -c "$s"` and not `| bash`). Add `--dry-run` after `mac-oneshot` to only print what it would do.
Pins: commit `8a1c2441ab8ffd72f382c1b2cfca7d00f7338120`, sha256 `eb54046bf3880ac311b962697f00837c6a671937c5099b5bd06fd1f9c45ad2d5`. At the end it makes you revoke the GitHub CLI authorization at https://github.com/settings/applications and proves it is dead (`gh auth logout` alone does not revoke it).
**Repair run (`--only-fix`)**, after a first run whose table has FAIL rows: no sudo, the key files are only read, the environment / rulesets / immutable releases are only read back. It logs in again, registers the existing public key as the write deploy key `eve-web-release-tagger` (**you add it in the browser**: the script copies the public key to the clipboard, opens the page and waits for `added`; a key created through the API with this login's token would be deleted by GitHub when you revoke the token, so the script no longer does that), applies the three Actions settings (every API error is printed with its HTTP status and message; if one still fails the end of the table says what to set by hand), runs `ssh -T`, and ends with the same `revoked` prompt, the 401 proof and the table. It stops if it finds a write deploy key that is not ours (checked before and again right after you added the key; if ours is not registered, `release-tags` is applied without the DeployKey bypass) or a missing / changed key file; it re-applies only the `release-tags` ruleset (its bypass depends on the key).

```bash
s=$(curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/gun025bba/eve-serving/8a1c2441ab8ffd72f382c1b2cfca7d00f7338120/deploy/nopasswd/mac-oneshot.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = eb54046bf3880ac311b962697f00837c6a671937c5099b5bd06fd1f9c45ad2d5 ] && bash -c "$s" mac-oneshot --only-fix
```

If the script is killed with SIGKILL, delete `$TMPDIR/eve-oneshot-gh.*` by hand and revoke the authorization at that link.

## Releasing (no one touches a key)

1. Squash or rebase the PR into `main` (linear history is required; no merge commits). 2. Tag the **current tip of main** `vX.Y.Z` and push the tag with `bash deploy/nopasswd/release-tag.sh X.Y.Z` (no human review before tagging, 태그 전 사람 리뷰 없음: it checks only that the commit is the tip of main, that the required checks are green and that the version is higher; key and revocation: `docs/deploy-signing.md`, "The release-tag deploy key") (any other commit is refused: a `release/*` branch is never signed, and neither is an older `main` commit, whatever its tag number).
3. `release` (`.github/workflows/release.yml`) builds darwin/arm64 `gateway` + `eve-gw`, writes `MANIFEST` (version, git sha, repo, ref, branch,
   schema, sha256 of the tar and of every member), signs the manifest with the job's OIDC identity and publishes
   `eve-gateway-X.Y.Z.{MANIFEST,sigstore.json,tar}` as a GitHub release. It refuses to sign unless the tag is a push of `refs/tags/vX.Y.Z` on exactly the tip of `main`. Build, sign and publish are three jobs; only the sign job has `id-token` and it runs no repository code.
4. On the Mac, as the deploy user (no sudo for the download; staging is yours):

```
/usr/local/libexec/eve/fetch gateway X.Y.Z && sudo /usr/local/libexec/eve/deploy deploy
sudo /usr/local/libexec/eve/deploy rollback            # back to the single kept previous version
```

**UI (`eve-web`)**: copy `eve-web-release.yml.example` to `.github/workflows/release.yml` in the eve-web repo (it calls the same reusable workflow, which
packs the tracked UI files). Then `fetch web X.Y.Z && sudo /usr/local/libexec/eve/deploy deploy-web` and `rollback-web`. The gateway and the UI
have independent, strictly increasing version lines.

What `deploy` checks before it changes anything (any failure leaves the live system untouched; a failed health check restores the previous version):
the Sigstore signature of `MANIFEST` offline against the pinned trust root and the pinned CI identity (workflow file at `main`, repository,
tag ref, `push` trigger), manifest shape and kind, `version` equals the one staged (`fetch` leaves exactly one per kind; the bare verb takes it) and is **higher** than the installed one, the schema equals
the database's (a different schema needs the migration deploy script), `ref` is that exact tag, `branch` is exactly `main`, `built_at` is at most 14 days old (and not from the future, 5 minutes of skew allowed; the Mac's clock must be right), the sha256 of the tar and of every file, and the
machine architecture. The files are first copied into a root-only directory and verified there, so the unprivileged staging dir cannot race it.

## Maintainers: pins inside the installer

The installer pins the exact bytes of what it installs. After any change to `eve-deploy`, `eve-fetch`, `trusted_root.json` or
`uninstall-deploy-nopasswd.sh`: commit it, then `bash deploy/nopasswd/pin-installer.sh <that commit>`, commit the installer, then
`bash deploy/nopasswd/pin-installer.sh --print <installer commit>` prints the install line for this README. `test/run.sh` fails on stale pins
and on a README that does not pin the current installer hash. The cosign version and the Sigstore trust root are bumped the same way (new
hashes in `eve-deploy`, `setup-deploy-nopasswd.sh`, `test/run.sh`; a trust-root change needs the one-time install again).

## Tests

`bash deploy/nopasswd/test/run.sh` runs in a user-level sandbox (no root, nothing on the host is touched; CI: Ubuntu and macOS, bash 3.2, BSD tools).
It verifies **real** CI-signed fixtures (`test/fixtures`, signed by CI from a throwaway branch; regenerated, if ever needed, by `.github/workflows/deploy-sign-fixture.yml`, a workflow the Mac does not trust) with real cosign
(downloaded and sha256-checked): bad signature, tampered manifest/tar/member, wrong sha, downgrade and equal version, wrong ref, wrong branch (only `main`), a stale or future `built_at`, the release guard,
wrong repo/identity/trigger, wrong kind, hostile tars, TOCTOU, signals during the swap, health-check restore, rollback, the installer and sudoers.
The real `sudo`, `/etc/sudoers` and `/var/db` on the Mac are covered only by the manual runbook.
