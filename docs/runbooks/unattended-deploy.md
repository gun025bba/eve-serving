# Unattended deploy: install, brakes, rehearsal, revocation

What this adds on top of the passwordless, CI-signed deploy ([`deploy-signing.md`](../deploy-signing.md), [`passwordless-deploy.md`](passwordless-deploy.md)):
a small poller (`evedeploy`, no root, no token, no key) looks for a new release every 5 minutes and hands it to the **existing** fetch + `sudo deploy` path.
It decides nothing about trust. The root entrypoint still verifies signature, CI identity, version (must be higher), sha256 and main-tip, and now also three brakes.

## The brakes (root-owned, `/private/var/db/eve-deploy`, written only by the entrypoint and by you)

| file | meaning |
|---|---|
| `PAUSE` | while it exists no `deploy` / `deploy-web` runs (rollbacks still do). Created by the installer **before** the sudoers rule goes live (reason=install), and by every failed deploy and every rollback (the latch is written FIRST; if the file cannot be written the entrypoint falls back to a dangling-symlink `PAUSE`, then to removing `allowed_user`, and if even that fails it prints `HOLD-LATCH-FAILED` and exits non-zero: the poller then halts itself, see below). **Only you remove it.** |
| `blocklist` | lines `<kind> <version>`: a release that failed its health check, or that was rolled back, is never deployed again |
| `floor`, `floor_web` | the lowest version a rollback may restore. **Fail-closed:** a missing or invalid floor means a rollback is refused. Created by the installer at the live version, raised (never lowered) to the previous version after each healthy deploy. The only manual change is `setup-unattended raise-floor KIND X.Y.Z`, which can only RAISE it (lower = refused, above the live version = refused) |
| `rehearsal_ok` | written by a PASSED rehearsal, **consumed by `unpause`**, and removed by any real failure or rollback (`hold`) and by every re-install of the poller |

Remove the pause by hand: `sudo rm /private/var/db/eve-deploy/PAUSE` (the poller then deploys the newest release it has not handled yet). Read why it was set: `cat /private/var/db/eve-deploy/PAUSE`; the log is `/var/log/eve-deploy.log`.
A blocklisted version you really want: edit `blocklist` by hand (root) and ship a NEW version, which is the normal answer.

## Order of the one-shot (`eve-oneshot.sh`) and why

1. **pins**: the merge commit is on main and belongs to a merged PR; installer, `setup-unattended.sh` and the settings files have the sha256 pinned in the script. Any mismatch stops here. The verified merge commit is then passed on as `--src-commit` to the installer and to `setup-unattended.sh` (they download only from that commit, and trust only their pinned sha256).
2. **login** (browser, throwaway `gh` config, OAuth) as `gun025bba`, admin of eve-serving and eve-web.
3. **settings** (`deploy/nopasswd/settings/*.json`, idempotent) and 4. **read-back table**. A FAIL row stops the run: **no sudo**. Write order: the `release-tags-lock` rulesets first (before `release-tags` is rewritten and before any other PUT), so a failure part-way never leaves the tags unprotected. The `pull_request` rules carry `require_extra_approval_for_unattributed_changes: true` (as the live ruleset) and the table compares it.
5. **sudo, once** (one password prompt): accounts `evedeploy` + `evebackup`, installer (`--deploy-user evedeploy`, `WEB_ENABLED=yes`, entrypoint with the brakes), today's supervised web deploy (`--web-deploy X.Y.Z`; the installer already made PAUSE, which the supervised deploy lifts and puts back), `state` (blocklist, poller + plist, then re-hash of every installed file), optional rehearsal, `load` (poller), install table.
6. **clean-up**: local logout; the links to revoke the OAuth token and the temporary token; you type `revoked`; both are proven dead with HTTP 401. A token that is still alive, or one that cannot be proven, ends the run FAIL (non-zero).

## The temporary token (the one that opens the PR)

Conditions, all required: **fine-grained**; repositories **eve-serving and eve-web only**; permissions **Contents: write and Pull requests: write only, no Administration**, no other permission; **expiry 1 day**; used **from the environment only** (never in a file, a command line, a log, a gh config).
It is never used to change a setting. `eve-oneshot.sh` only (a) refuses to start if the token can read an admin-only endpoint (a token with Administration violates the conditions) when `EVE_TEMP_TOKEN` is set, and (b) at the end proves with a 401, through curl's stdin, that it is revoked.
Revoke: https://github.com/settings/personal-access-tokens (Delete). The OAuth login of the run: https://github.com/settings/applications ("GitHub CLI", Revoke).

## Install with PAUSE, rehearse later (works without a signed test release today)

The install ends with PAUSE set, so **nothing deploys unattended** until the rehearsal has passed. Later, in a session that has sudo:

```
sudo /bin/bash -c "$(curl ... setup-unattended.sh, checked against its pin)" setup-unattended rehearse gateway X.Y.Z    # X.Y.Z = the test release
sudo ... setup-unattended unpause                                                                                      # only works after the rehearsal wrote rehearsal_ok
```
(`eve-oneshot.sh --rehearse KIND X.Y.Z --unpause-after-pass` does both inside the install's sudo session when the test release already exists.)

`rehearse` first checks (before the test release) that the deploy user may call **both** rollback verbs without a password (`sudo -n -l`) and that the installed poller is the pinned file and executable; then it removes the initial PAUSE, fetches and deploys the test release as `evedeploy` through the sudoers rule, and checks six things: it **failed**, the previous version is **live again**, **PAUSE exists**, the version is **blocklisted**, the **floor** is valid, the log shows the **automatic restore**. All six PASS writes `rehearsal_ok`; anything else leaves PAUSE set. PAUSE is recreated on every exit path (trap), also on Ctrl-C.

### How the test release is made (signing path)

The entrypoint accepts only what `deploy-sign.yml@main` signed for a tag `vX.Y.Z` on the **current tip of main**. A fixture or a hand-signed bundle is rejected by design, so the test release is a **real, ordinary release whose build is unhealthy**:

1. A tiny PR to main (code-owner reviewed like any other under `/internal/`): a build whose `/health` does not answer `{"status":"ok"}` (gateway), or whose `/ui/` is not 200 (web). It changes nothing else.
2. Merge it, tag the next patch `vX.Y.Z` the usual way (`release-tag.sh` / the release label); `deploy-sign.yml` signs it; wait for the `deploy` environment timer.
3. Run `rehearse` before anything else lands on main. The release is then blocklisted. A follow-up PR reverts the change and the next patch is the real release.

Open points for the backend / web owners: the smallest safe switch that makes the build unhealthy (a build tag vs a revert-able commit), and whether the web packer can produce a bundle that fails `/ui/`. Until a test release exists the install stays paused: that is the intended safe state.

## Pins: no PR commit is pinned anywhere

`main-protect` requires linear history, so the PR is squash- or rebase-merged and its branch commits do not exist on `main`. Therefore **no file contains a commit sha**: every file fetched at run time is downloaded from ONE main commit (the merge commit) and trusted only by the sha256 embedded in the file that fetches it
(`eve-deploy`/`eve-fetch`/trust root/uninstall in the installer; `eve-poll`/plist template in `setup-unattended.sh`; the installer, `setup-unattended.sh` and `settings/SHA256SUMS` in `eve-oneshot.sh`; the installer and entrypoint in `mac-oneshot.sh`). The main commit is an **argument** of the final command (`--merge-commit` for `eve-oneshot.sh`, `--src-commit` for `mac-oneshot.sh` and the installer), and the command carries the sha256 of the entry script (no self-reference).
Maintainers: after any change run, in this order and with no commit in between, `pin-installer.sh`, `pin-unattended.sh`, `pin-unattended.sh --oneshot`, then commit; after the merge `pin-unattended.sh --print <merge commit>` prints the command. The tests fail on a stale pin.

## Review gate (CODEOWNERS is NOT enforced)

The live ruleset has `require_code_owner_review=false` and 0 required approvals, and `settings/main-protect.eve-serving.json` says the same (plus `dismiss_stale_reviews_on_push`, `required_review_thread_resolution`, merge methods `squash`/`rebase`, linear history). `.github/CODEOWNERS` therefore marks the trust-root paths but does not block a merge. **The gate is CI** (required checks from the Actions app, integration 15368: `test` and `deploy-nopasswd-macos`) **plus the security review of the PR**. Turning enforcement on is a separate proposal: 1 required approval + `require_code_owner_review=true` + PRs opened by the `gallix-grok` account (the author cannot approve their own PR).
`deploy-major` keeps `prevent_self_review=false` on purpose: its only reviewer is the owner, who also triggers the release, and `true` would make every major release un-approvable (deadlock); the 20-minute timer and the required reviewer remain.

## The poller halts itself

`eve-poll` ignores every `EVE_POLL_*` setting unless `EVE_POLL_TESTING=1` (tests only; the plist never sets it). It writes `/var/evedeploy/poll/halted` and does nothing further (no fetch, no deploy) when its rollback call fails or when a failed deploy printed `HOLD-LATCH-FAILED` (root could not pause deploys). Read `poll.log`, fix the cause, then `rm /var/evedeploy/poll/halted`.

## Emergency hotfix (a bad release is live and a fix must go out fast)

1. A failed health check already rolled back, blocklisted the version and set PAUSE. If the bad release is still live, run `sudo -u evedeploy /usr/bin/sudo -n /usr/local/libexec/eve/deploy rollback` (allowed under PAUSE; floor applies) or `sudo touch /private/var/db/eve-deploy/PAUSE` to stop everything first.
2. Fix via a normal PR (CI + security review), merge, tag the NEXT patch with `release-tag.sh`. The `deploy` wait timer (20 min) still applies: it is the only window to stop a bad signature, so do not lower it in an emergency.
3. Read `cat /private/var/db/eve-deploy/PAUSE` and the log, confirm the new release is the fix, then `sudo rm /private/var/db/eve-deploy/PAUSE`. The poller deploys it within 5 minutes, or run `sudo -u evedeploy /usr/local/libexec/eve/fetch gateway X.Y.Z && sudo -u evedeploy /usr/bin/sudo -n /usr/local/libexec/eve/deploy deploy`.
4. A rollback below the floor is never possible through the sudoers verbs. If you really need it, the owner edits `/private/var/db/eve-deploy/floor` by hand as root (the one manual way to LOWER it; `raise-floor` only raises).
5. A blocklisted version stays blocked: ship a new version.

## Rulesets added by the one-shot besides the release tags

* **eve-serving `release-branches`** (`settings/release-branches.eve-serving.json`, `refs/heads/release/*`): creation, deletion and force-push are blocked (only the repository admin role bypasses, like `release-tags-lock`); changes need a PR with 0 approvals, squash/rebase, and the checks `test` + `deploy-nopasswd-macos` (Actions app, integration 15368). `ci.yml` runs on every pull request, so a PR into `release/*` gets both checks. A `release/*` branch never signs anything (`docs/deploy-signing.md`).
* **eve-ios `main-protect`** (`settings/main-protect.eve-ios.json`, only this ruleset is applied to eve-ios): no force-push, no deletion, linear history, PR required (0 approvals, squash/rebase), required check `compile` (Actions app, integration 15368). The check is the job `compile` of `.github/workflows/compile.yml` in eve-ios, which runs on `pull_request` to `main` only. **That workflow must be on `main` (or in the PR itself) before this ruleset can be satisfied**: a PR branched from a `main` without `compile.yml` never gets the check and cannot merge until it is rebased.
Both are read back in the PASS/FAIL table (a FAIL row stops the run before sudo).

## iOS shell and `GW_ALLOWED_ORIGINS`

The iOS shell is a WKWebView that loads `https://<EVE_HOST>/ui/` and only that origin, so its browser writes carry `Origin: https://<EVE_HOST>` (no port: 443), the same as the web app. The gateway has **no default and no derivation** for the origin list: `GW_ALLOWED_ORIGINS` (set only in the gateway's own plist, `https://<TS_HOST>`; empty = every browser write is refused) and `GW_ALLOWED_HOSTS` come from the owner. Therefore no code change is needed as long as `EVE_HOST` in the shell's `Local.xcconfig` is the same name as `<TS_HOST>` (lower case, bare host). If it is ever a different name, add that origin to the comma separated list; the installer does not touch the gateway's plist.

## GitHub CLI OAuth scope

The one-shot logs in with `--scopes repo`. Cutting it to `public_repo` was **not tested**: GitHub documents `repo` as required for the environment write endpoints, and the effect on rulesets and Actions settings can only be seen against the real repositories. The login is throwaway and revoked at the end of the run (401 proof), so the scope stays `repo` until a real run shows that `public_repo` is enough.

## Things to know

* The `deploy` environment now has a **wait timer** (20 min, `settings/env-deploy.json`): every release waits that long before `deploy-sign.yml` signs it, including hand-made ones. Lower it in that JSON and re-run the one-shot.
* sudoers names **only `evedeploy`**. Your own manual deploys: `sudo -u evedeploy /usr/bin/sudo -n /usr/local/libexec/eve/deploy deploy` (after `fetch` as that user), or remove the rule's user restriction in a reviewed change.
* The poller keeps `done_*`, `staged_*`, `try_*` marks in `/var/evedeploy/poll` (0700, `evedeploy`). It cannot read or write the brakes. Its log is `/var/evedeploy/poll/poll.log`.
* Out of scope of this change: the tag agent and the move of the tag key into `evedeploy`, the backup job and DB roles for `evebackup`, `release-tag.sh` for other repositories.
