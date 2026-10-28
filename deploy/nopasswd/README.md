# Passwordless, CI-signed deploy (`deploy/nopasswd/`)

Optional path: after **one** install line, a deploy of a *released, CI-signed* gateway or UI needs no password, no laptop and no
key. The signature is produced by GitHub Actions (Sigstore keyless); the Mac only **verifies**. Design, threat model and the repo
settings this relies on: [`docs/deploy-signing.md`](../../docs/deploy-signing.md). Checks to run on the real Mac after installing:
[`docs/runbooks/passwordless-deploy.md`](../../docs/runbooks/passwordless-deploy.md).

## One-time install (the only thing a person types)

Run this **once**, in a terminal on the Mac, as the user that will deploy (it asks for your sudo password once). It downloads the
installer from a fixed commit, refuses to continue unless its sha256 matches, and hands exactly the checked text to `sudo /bin/bash -s`:

```
s=$(curl -fsSL --proto '=https' --tlsv1.2 https://raw.githubusercontent.com/gun025bba/eve-serving/089ff96d2aa1106aad78fff2ac5926152e7189e6/deploy/nopasswd/setup-deploy-nopasswd.sh) && [ "$(printf '%s\n' "$s" | shasum -a 256 | cut -d' ' -f1)" = aa6e6cd5013e9466eaeee91ced382c12802bcd1df205c4a2d756391bc0bc928d ] && printf '%s\n' "$s" | sudo /bin/bash -s
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

## Releasing (no one touches a key)

1. Merge to `main` (or a `release/X.Y.Z` branch). 2. Tag that commit `vX.Y.Z` and push the tag.
3. `release` (`.github/workflows/release.yml`) builds darwin/arm64 `gateway` + `eve-gw`, writes `MANIFEST` (version, git sha, repo, ref, branch,
   schema, sha256 of the tar and of every member), signs the manifest with the job's OIDC identity and publishes
   `eve-gateway-X.Y.Z.{MANIFEST,sigstore.json,tar}` as a GitHub release. It refuses to sign a tag whose commit is not on `main` / `release/*`.
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
tag ref, `push` trigger), manifest shape and kind, `version` equals the one staged (`fetch` leaves exactly one per kind; the bare verb takes it) and is **higher** than the installed one, the schema is not
older than the database's, `ref` is that exact tag, `branch` is `main` or `release/X.Y.Z`, the sha256 of the tar and of every file, and the
machine architecture. The files are first copied into a root-only directory and verified there, so the unprivileged staging dir cannot race it.

## Maintainers: pins inside the installer

The installer pins the exact bytes of what it installs. After any change to `eve-deploy`, `eve-fetch`, `trusted_root.json` or
`uninstall-deploy-nopasswd.sh`: commit it, then `bash deploy/nopasswd/pin-installer.sh <that commit>`, commit the installer, then
`bash deploy/nopasswd/pin-installer.sh --print <installer commit>` prints the install line for this README. `test/run.sh` fails on stale pins
and on a README that does not pin the current installer hash. The cosign version and the Sigstore trust root are bumped the same way (new
hashes in `eve-deploy`, `setup-deploy-nopasswd.sh`, `test/run.sh`; a trust-root change needs the one-time install again).

## Tests

`bash deploy/nopasswd/test/run.sh` runs in a user-level sandbox (no root, nothing on the host is touched; CI: Ubuntu and macOS, bash 3.2, BSD tools).
It verifies **real** CI-signed fixtures (`test/fixtures`, produced by `.github/workflows/test-fixtures.yml` from a throwaway branch) with real cosign
(downloaded and sha256-checked): bad signature, tampered manifest/tar/member, wrong sha, downgrade and equal version, wrong ref, wrong branch,
wrong repo/identity/trigger, wrong kind, hostile tars, TOCTOU, signals during the swap, health-check restore, rollback, the installer and sudoers.
The real `sudo`, `/etc/sudoers` and `/var/db` on the Mac are covered only by the manual runbook.
