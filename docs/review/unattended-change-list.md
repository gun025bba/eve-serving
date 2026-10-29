# Change list for the Major security review (one line per file)

Base: `main`, kept in sync by merge commits (see `git merge-base`; the last sync is 3033eac). Branch: `feat/unattended-deploy-v2`. Everything is under `deploy/nopasswd/` unless a path says otherwise.

| file | change | why it needs a look |
|---|---|---|
| `eve-deploy` (root entrypoint) | +`check_brakes` (PAUSE incl. symlink, blocklist), `check_floor`, `raise_floor`, `hold`; deploy/deploy-web refuse under PAUSE or a blocklisted version; a failed swap/health and every successful rollback blocklist the version and write PAUSE; rollbacks refuse below the floor | runs as root; the only trust-boundary code that changes. No new sudoers verb (U-2) |
| `setup-deploy-nopasswd.sh` (root installer) | new option `--deploy-user NAME` (sudoers user instead of `SUDO_USER`; same name/uid/not-root/not-`_evegw` checks); pins re-pinned | decides who may run the entrypoint |
| `eve-fetch` | `curl --max-redirs 5` | runs as the deploy user, stages files only |
| `eve-poll` (new, runs as `evedeploy` via launchd) | reads only the `Location` header of `releases/latest` (`--proto =https --max-redirs 0`, host allow-list, strict `vX.Y.Z`), mkdir lock, 3 tries + backoff, `fetch` then `sudo -n deploy`, 5-minute observation, `sudo -n rollback` after 3 failed probes | untrusted network input to an unprivileged user; it cannot read root state |
| `launchd/com.llmserving.evepoll.plist.template` (new) | LaunchDaemon, `UserName` = deploy user, every 300 s, umask 63 | what runs unattended |
| `setup-unattended.sh` (new, root, same sudo session) | `accounts` (evedeploy, evebackup: hidden, no shell, no password, not admin, uid 460-499), `state` (PAUSE, floors, blocklist, poller + plist, pinned), `deploy`, `rehearse`, `unpause` (needs `rehearsal_ok`), `load`, `verify` | creates accounts and a root-run LaunchDaemon; downloads are sha256-pinned |
| `eve-oneshot.sh` (new, user + one sudo) | pins check, throwaway OAuth login, settings apply + read-back, ONE `sudo /bin/bash -c` with the checked texts as arguments, revoke flow with 401 proof for the OAuth token and the temporary token | orchestrates admin settings and the root install; never uses the temporary token for a change |
| `pin-unattended.sh`, `pin-installer.sh` (maintainer only) | write the sha256 pins; **no commit is pinned** (PR commits do not survive squash/rebase); `--print <main sha>` prints the final one-line command | pin hygiene |
| `settings/*.json` + `SHA256SUMS` (new) | rulesets (`main-protect` for eve-serving: 0 approvals, code-owner review OFF like the live ruleset, dismiss stale reviews, thread resolution, squash/rebase only, `release-tags` creation-only + key variant, `release-tags-lock`), environments `deploy` (wait 20 min, no admin bypass, tag `v*`) and `deploy-major` (reviewer = owner), secret scanning + push protection, Actions settings | repository security settings applied with admin rights |
| `../../.github/CODEOWNERS` | `* @gun025bba` replaced by the security paths (`/.github/`, `/deploy/`, migrations, `go.mod`, `go.sum`, `config.yaml`, `scripts/check-serve.sh`); header says it is NOT enforced | scope of code-owner review (not a merge gate) |
| `mac-oneshot.sh` | `INSTALL_COMMIT` removed; new `--src-commit` argument used for the installer and `github_known_hosts` (sha256 pins stay); the installer gets the same `--src-commit` | the commit is now an argument, not a pin |
| `README.md` | installer pin line, one-shot section | docs |
| `../../docs/runbooks/unattended-deploy.md` (new) | brakes, order, token conditions, rehearsal design, today's fallback | runbook |
| `test/run.sh`, `test/unattended-test.sh` (new) | brake tests on the real signed fixtures; one-shot, root half and poller tests with fakes; CODEOWNERS check | tests |

Assumptions to confirm in the review: code-owner review is NOT enforced (the gate is CI + the security review, see the runbook); the `deploy-major` reviewer API shape; wait timer 20 min as default; environment protection on a public repository of a Free plan; `dscl`/`launchctl` behaviour on macOS (only stubs run in the tests); the nested `sudo -u evedeploy /usr/bin/sudo -n` flow; the floor definition (previous version).

## Findings of the security review of #144 @eca5289 and their fixes (on top of eca5289)

| id | fix | test |
|---|---|---|
| macOS CI | the fake sudo in `unattended-test.sh` used `sha256sum`, which does not exist in `/usr/bin` on macOS (restricted PATH): the hash was empty and the awk/sed matches broke; now `shasum`-first helpers | `unattended-test.sh` "happy: arg 4/5 sha", "rehearse: args"; reproduced with bash 3.2 + `sha256sum` hidden |
| pins | no commit pinned; `--src-commit` / `--merge-commit` arguments; sha256 chain | `unattended-test.sh` "pin: no 40-hex commit…", `mac-oneshot-test.sh` README/`--src-commit` checks |
| H-1 | ruleset JSON at least as strict as live: `dismiss_stale_reviews_on_push`, `required_review_thread_resolution`, `allowed_merge_methods` squash+rebase; one-shot read-back checks them | `unattended-test.sh` "main-protect serving (H-1)" |
| H-2 | `require_code_owner_review=false`, 0 approvals; CODEOWNERS and runbook say it is not enforced; gate = CI + security review | `unattended-test.sh` "main-protect serving (H-2)", CODEOWNERS check |
| M-1 | `setup-unattended.sh`: one `mktemp -d` (0700), no derived names, cleanup trap; installed poller and rendered plist re-hashed (plist sha recorded, `verify` re-checks); installer re-hashes every destination BEFORE state/sudoers and the sudoers rule at the end | `unattended-test.sh` "M-1: …", `run.sh` "M-1: …" (mutation tests) |
| M-2 | `--src-commit <40hex>` for installer and `setup-unattended`; the one-shot passes the verified merge commit | `run.sh` "M-2/setup: --src-commit", `unattended-test.sh` "M-2", "happy: the verified merge commit…" |
| M-3 | floor fail-closed; installer creates both floors; `raise-floor` raises only | `run.sh` "M-3: …", `unattended-test.sh` "M-3: …" |
| M-4 | `hold()` writes PAUSE first, symlink / `allowed_user` fallbacks, else `HOLD-LATCH-FAILED` + exit 1; the poller halts on it and on a failed rollback | `run.sh` "M-4: …", `unattended-test.sh` "M-4: …" |
| lows | drill checks both rollback verbs + poller; `rehearsal_ok` invalidated by `hold`, re-install and `unpause`; PAUSE exists before the installer's sudoers rule (`--pause`); `EVE_POLL_*` only with `EVE_POLL_TESTING=1`; `prevent_self_review` on deploy-major kept off (documented); OAuth `public_repo` untested (documented); emergency hotfix procedure in the runbook | `unattended-test.sh` "drill:", "knobs:", "low:" |

## Added after the first review round (settings)

| item | change | test |
|---|---|---|
| eve-ios `main-protect` | `settings/main-protect.eve-ios.json` (pinned in SHA256SUMS): required check `compile` @15368 (job `compile` of eve-ios `compile.yml`, pull_request to main only), no force-push/deletion, linear history, PR with 0 approvals, squash/rebase. `eve-oneshot.sh` applies ONLY this ruleset to eve-ios (admin + public checks cover it) and reads it back | `unattended-test.sh` "main-protect ios: …", "ios: …", S8b/S8c |
| eve-serving `release-branches` | `settings/release-branches.eve-serving.json` (pinned): `refs/heads/release/*`, creation + deletion + non_fast_forward blocked, admin role bypass only, PR + `test` and `deploy-nopasswd-macos` | `unattended-test.sh` "release-branches: …", S8d |
| `GW_ALLOWED_ORIGINS` | checked read-only, no change: the iOS shell's Origin is `https://<EVE_HOST>`, the web app's own origin; see the runbook | - |

## Review r2 (conditional approval at 480c574): the two Lows fixed here

| id | fix | test |
|---|---|---|
| L-1 | `require_extra_approval_for_unattributed_changes: true` added to the `pull_request` rule `parameters` (the only valid place, boolean, optional) of `main-protect.eve-serving.json`, `main-protect.eve-ios.json`, `release-branches.eve-serving.json`; the eve-web `main-protect` has no `pull_request` rule, so none there. The read-back (`RS_SUM`) now compares it (`extra=`), so a PUT that loses it shows as a FAIL row | `unattended-test.sh` "r2 L-1: …", S8e |
| L-2 | `release-tags-lock` is created/updated FIRST of all writes (both repositories), before `release-tags` is rewritten and before any PUT of another setting | `unattended-test.sh` "r2 L-2: …", S8f |
| L-5 | the PR description's merge condition ("merge commit … pins reference commit SHA") was wrong: squash or rebase only, no commit is pinned (corrected text in the PR hand-off) | - |
| L-6 | the header no longer names a stale base commit | - |

## Calendar adapter in the one-shot (`feat/oneshot-calendar-adapter`): files to review

Everything under `deploy/nopasswd/` unless a path says otherwise. Trust model unchanged: one main commit (`--merge-commit`), every fetched file checked by the sha256 embedded in the file that fetches it, a single 0700 work dir, fail-closed, no new sudoers verb (the installer is NOT touched; still exactly 4 verbs), unattended stays PAUSE.

| file | change | why it needs a look |
|---|---|---|
| `setup-pim-adapter.sh` (new, root, same sudo session) | verifies the Sigstore-keyless signed manifest of release `vX.Y.Z` with the pinned `cosign` + trusted root + the SAME CI identity as `eve-deploy` (a test compares the constants), `pim_sha` == pinned `PIM_SHA`, sha256 of the adapter, hardened-runtime code signature + calendars entitlement only; installs by atomic rename into root-owned `/opt/eve-pim/bin` (every parent root-owned and closed), re-verifies, restores the old adapter on failure; then the reviewed #154 steps §1-§6 (group `_evepim`, socket dir, sandbox profile, LaunchAgent, one planned TCC prompt, smoke test, gateway env keys with flags false) kept line for line | runs as root; downloads from the release with curl (https only, size caps) into one 0700 dir; edits the gateway LaunchDaemon env (§6) |
| `eve-oneshot.sh` | new options `--calendar-adapter X.Y.Z` and `--gateway-deploy X.Y.Z`; `PIN_PIMADAPTER_SHA256`; the adapter text is fetched from the merge commit and checked ONLY when the option is given; the sudo call has 14 arguments (3 new: adapter text or `-`, its version or `-`, gateway version or `-`); session order: accounts, installer (PAUSE), [adapter], [gateway deploy], [web deploy], state, [rehearse], load, verify | what runs as root in the one session |
| `pin-unattended.sh` | `--oneshot` also rewrites `PIN_PIMADAPTER_SHA256`; `--print <merge> [options]` appends the given one-shot options (`[0-9a-z. -]` only) to the printed command | pin hygiene |
| `test/pim-adapter-test.sh` (new), `test/run.sh`, `test/unattended-test.sh` | fake `cosign`/`codesign`: manifest format, tool checks, identity arguments, signature/commit/hash/code-signature refusals, atomic install, same-sha256 not replaced, restore after a failed step, open/symlinked dirs refused; the constants equal `eve-deploy`'s; one-shot: 14 arguments, pin check, tamper, order in the session, nothing fetched without the option | tests |
| `README.md`, `../../docs/runbooks/unattended-deploy.md` | as-is / to-be pipeline diagram, TCC rule, how to re-run | docs |

Open points for the review: (1) a separate CI PR must make `deploy-sign.yml` publish `eve-pim-X.Y.Z`, `.MANIFEST`, `.sigstore.json` (manifest keys: `eve-pim-manifest=1`, `pim_sha`, `pim_sha256`); until then `--calendar-adapter` stops at the download, before anything is installed. (2) `PIM_SHA` must stay reachable (a `keep/pim-...` branch); confirm the value. (3) `--certificate-github-workflow-sha` is not checked for the adapter (the manifest carries no tag sha); the repository, tag ref, trigger, issuer and identity are. (4) the adapter and the gateway of the same release are deployed in one session: the entrypoint swaps the binary and kickstarts the service, it does not rewrite the LaunchDaemon plist, so the env keys of §6 survive. (5) the gateway schema of 5.1.0 must match the installed one (as in #154).
