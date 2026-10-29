# Unattended deploy: install, brakes, rehearsal, revocation

What this adds on top of the passwordless, CI-signed deploy ([`deploy-signing.md`](../deploy-signing.md), [`passwordless-deploy.md`](passwordless-deploy.md)):
a small poller (`evedeploy`, no root, no token, no key) looks for a new release every 5 minutes and hands it to the **existing** fetch + `sudo deploy` path.
It decides nothing about trust. The root entrypoint still verifies signature, CI identity, version (must be higher), sha256 and main-tip, and now also three brakes.

## The brakes (root-owned, `/private/var/db/eve-deploy`, written only by the entrypoint and by you)

| file | meaning |
|---|---|
| `PAUSE` | while it exists no `deploy` / `deploy-web` runs (rollbacks still do). Created by the install (reason=initial) and by every failed deploy and every rollback. **Only you remove it.** |
| `blocklist` | lines `<kind> <version>`: a release that failed its health check, or that was rolled back, is never deployed again |
| `floor`, `floor_web` | the lowest version a rollback may restore. Set to the live version at install, raised (never lowered) to the previous version after each healthy deploy |

Remove the pause by hand: `sudo rm /private/var/db/eve-deploy/PAUSE` (the poller then deploys the newest release it has not handled yet). Read why it was set: `cat /private/var/db/eve-deploy/PAUSE`; the log is `/var/log/eve-deploy.log`.
A blocklisted version you really want: edit `blocklist` by hand (root) and ship a NEW version, which is the normal answer.

## Order of the one-shot (`eve-oneshot.sh`) and why

1. **pins**: the merge commit is on main and belongs to a merged PR; installer, `setup-unattended.sh` and the settings files have the sha256 pinned in the script. Any mismatch stops here.
2. **login** (browser, throwaway `gh` config, OAuth) as `gun025bba`, admin of eve-serving and eve-web.
3. **settings** (`deploy/nopasswd/settings/*.json`, idempotent) and 4. **read-back table**. A FAIL row stops the run: **no sudo**.
5. **sudo, once** (one password prompt): accounts `evedeploy` + `evebackup`, installer (`--deploy-user evedeploy`, `WEB_ENABLED=yes`, entrypoint with the brakes), today's supervised web deploy (`--web-deploy X.Y.Z`, before PAUSE is set), `state` (PAUSE, floors, blocklist, poller + plist), optional rehearsal, `load` (poller), install table.
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

`rehearse` removes the initial PAUSE, fetches and deploys the test release as `evedeploy` through the sudoers rule, and checks six things: it **failed**, the previous version is **live again**, **PAUSE exists**, the version is **blocklisted**, the **floor** is valid, the log shows the **automatic restore**. All six PASS writes `rehearsal_ok`; anything else leaves PAUSE set. PAUSE is recreated on every exit path (trap), also on Ctrl-C.

### How the test release is made (signing path)

The entrypoint accepts only what `deploy-sign.yml@main` signed for a tag `vX.Y.Z` on the **current tip of main**. A fixture or a hand-signed bundle is rejected by design, so the test release is a **real, ordinary release whose build is unhealthy**:

1. A tiny PR to main (code-owner reviewed like any other under `/internal/`): a build whose `/health` does not answer `{"status":"ok"}` (gateway), or whose `/ui/` is not 200 (web). It changes nothing else.
2. Merge it, tag the next patch `vX.Y.Z` the usual way (`release-tag.sh` / the release label); `deploy-sign.yml` signs it; wait for the `deploy` environment timer.
3. Run `rehearse` before anything else lands on main. The release is then blocklisted. A follow-up PR reverts the change and the next patch is the real release.

Open points for the backend / web owners: the smallest safe switch that makes the build unhealthy (a build tag vs a revert-able commit), and whether the web packer can produce a bundle that fails `/ui/`. Until a test release exists the install stays paused: that is the intended safe state.

## Things to know

* The `deploy` environment now has a **wait timer** (20 min, `settings/env-deploy.json`): every release waits that long before `deploy-sign.yml` signs it, including hand-made ones. Lower it in that JSON and re-run the one-shot.
* sudoers names **only `evedeploy`**. Your own manual deploys: `sudo -u evedeploy /usr/bin/sudo -n /usr/local/libexec/eve/deploy deploy` (after `fetch` as that user), or remove the rule's user restriction in a reviewed change.
* The poller keeps `done_*`, `staged_*`, `try_*` marks in `/var/evedeploy/poll` (0700, `evedeploy`). It cannot read or write the brakes. Its log is `/var/evedeploy/poll/poll.log`.
* Out of scope of this change: the tag agent and the move of the tag key into `evedeploy`, the backup job and DB roles for `evebackup`, `release-tag.sh` for other repositories.
