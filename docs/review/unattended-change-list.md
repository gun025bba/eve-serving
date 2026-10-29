# Change list for the Major security review (one line per file)

Base: `feat/enable-web-deploy` (d5b246d). Branch: `feat/unattended-deploy`. Everything is under `deploy/nopasswd/` unless a path says otherwise.

| file | change | why it needs a look |
|---|---|---|
| `eve-deploy` (root entrypoint) | +`check_brakes` (PAUSE incl. symlink, blocklist), `check_floor`, `raise_floor`, `hold`; deploy/deploy-web refuse under PAUSE or a blocklisted version; a failed swap/health and every successful rollback blocklist the version and write PAUSE; rollbacks refuse below the floor | runs as root; the only trust-boundary code that changes. No new sudoers verb (U-2) |
| `setup-deploy-nopasswd.sh` (root installer) | new option `--deploy-user NAME` (sudoers user instead of `SUDO_USER`; same name/uid/not-root/not-`_evegw` checks); pins re-pinned | decides who may run the entrypoint |
| `eve-fetch` | `curl --max-redirs 5` | runs as the deploy user, stages files only |
| `eve-poll` (new, runs as `evedeploy` via launchd) | reads only the `Location` header of `releases/latest` (`--proto =https --max-redirs 0`, host allow-list, strict `vX.Y.Z`), mkdir lock, 3 tries + backoff, `fetch` then `sudo -n deploy`, 5-minute observation, `sudo -n rollback` after 3 failed probes | untrusted network input to an unprivileged user; it cannot read root state |
| `launchd/com.llmserving.evepoll.plist.template` (new) | LaunchDaemon, `UserName` = deploy user, every 300 s, umask 63 | what runs unattended |
| `setup-unattended.sh` (new, root, same sudo session) | `accounts` (evedeploy, evebackup: hidden, no shell, no password, not admin, uid 460-499), `state` (PAUSE, floors, blocklist, poller + plist, pinned), `deploy`, `rehearse`, `unpause` (needs `rehearsal_ok`), `load`, `verify` | creates accounts and a root-run LaunchDaemon; downloads are sha256-pinned |
| `eve-oneshot.sh` (new, user + one sudo) | pins check, throwaway OAuth login, settings apply + read-back, ONE `sudo /bin/bash -c` with the checked texts as arguments, revoke flow with 401 proof for the OAuth token and the temporary token | orchestrates admin settings and the root install; never uses the temporary token for a change |
| `pin-unattended.sh` (new, maintainer only) | writes the pins (setup-unattended, SHA256SUMS, one-shot, mac-oneshot) | pin hygiene |
| `settings/*.json` + `SHA256SUMS` (new) | rulesets (`main-protect` with code-owner review for eve-serving, `release-tags` creation-only + key variant, `release-tags-lock`), environments `deploy` (wait 20 min, no admin bypass, tag `v*`) and `deploy-major` (reviewer = owner), secret scanning + push protection, Actions settings | repository security settings applied with admin rights |
| `../../.github/CODEOWNERS` | `* @gun025bba` replaced by the security paths (`/.github/`, `/deploy/`, migrations, `go.mod`, `go.sum`, `config.yaml`, `scripts/check-serve.sh`) | scope of code-owner review |
| `mac-oneshot.sh` | only its three installer pins | keeps the old one-shot consistent |
| `README.md` | installer pin line, one-shot section | docs |
| `../../docs/runbooks/unattended-deploy.md` (new) | brakes, order, token conditions, rehearsal design, today's fallback | runbook |
| `test/run.sh`, `test/unattended-test.sh` (new) | brake tests on the real signed fixtures; one-shot, root half and poller tests with fakes; CODEOWNERS check | tests |

Assumptions to confirm in the review: code-owner review is enforced with `required_approving_review_count: 0`; the `deploy-major` reviewer API shape; wait timer 20 min as default; environment protection on a public repository of a Free plan; `dscl`/`launchctl` behaviour on macOS (only stubs run in the tests); the nested `sudo -u evedeploy /usr/bin/sudo -n` flow; the floor definition (previous version).
