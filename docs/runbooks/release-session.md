# Release-day session (`eve-session.sh`)

One owner session instead of several: reinstall, first tags, signed releases, deploy, rollback rehearsal, table. One `sudo` password. Every stage is **fail-closed** (the first failure stops the run, nothing after it runs)
and **idempotent** (run the same command again; finished stages are recognised from the real state and skipped).

## Command

After the PR is merged, on the merge commit `M` of `main`:

```
bash deploy/nopasswd/pin-unattended.sh --print-session <M> <gateway X.Y.Z> <web X.Y.Z>     # prints the one-line command
```

The printed command clones the repository, checks out `M` as `main`, verifies the sha256 of `eve-session.sh` and runs it. The session script itself pins (sha256) every file it runs: `eve-oneshot.sh`,
`eve-measure.sh`, `release-tag.sh`, `release-tag.eve-web.constants`, and (through the one-shot) `setup-unattended.sh`. A changed file or a stale pin refuses the run before anything happens.

Options: `--rehearse-gateway X.Y.Z --rehearse-web X.Y.Z` (signed test releases built to fail their health check), `--switch`, `--unpause`, `--dry-run`, `--wait-minutes N`, `--force-install`.
`--dry-run` checks the pins, the clone and runs the real read-only measurement; it needs no sudo, no network and changes nothing.

## Stages (hours are wall time, mostly waiting for CI and for you)

| # | stage | h | what |
|---|---|---|---|
| 0 | preflight | 0.1 | pins, clone state, tools, `gh` login, the 14-item read-only measurement (`eve-measure.sh`) |
| 1 | sudo | 0 | one `sudo -v` and a keep-alive |
| 2 | keys | 0.2 | creates the two release-tag deploy keys if missing, tells you where to add them (Title, key, **Allow write access**), you type `registered`; GitHub must accept both |
| 3 | install | 0.3 | `eve-oneshot.sh --merge-commit M` (entrypoint, all rulesets incl. the eve-web `pull_request` rule, PAUSE) |
| 4 | switch | 0.5 | optional, see below |
| 5 | tags | 0.1 | `release-tag.sh` for gateway (tip of main must be `M`) and `release-tag.sh --repo eve-web` from a clone of eve-web; you type `tag`; an existing tag is never touched |
| 6 | releases | 0.4 | waits until each release has exactly `MANIFEST`, `sigstore.json`, `tar` and is published |
| 7 | deploy | 0.3 | `setup-unattended.sh deploy gateway X`, then `deploy web Y` (never a downgrade) |
| 8 | rehearse | 0.5 | `setup-unattended.sh rehearse KIND test-version` for both kinds (optional) |
| 9 | table | 0.05 | live versions, floors, releases, blocklist, `rehearsal_ok`, `verify` as PASS/FAIL rows |
| 10 | unpause | 0 | only with `--unpause`, a passed rehearsal and the word `unpause` typed by you |

### Why the keys come before the install

`eve-oneshot.sh` gives the `release-tags` ruleset its DeployKey bypass only if exactly one write key titled `release-tagger` (and `eve-web-release-tagger`) exists at that moment. With the key added afterwards the
ruleset would be applied in its no-key form and no tag could be pushed.

### The rehearsal is not a manual rollback

A manual `rollback` is a brake release in reverse: it blocklists the rolled-back version for ever, sets PAUSE and removes `rehearsal_ok`, and a version can never be rolled forward again. A sequence
5.1.0 → 5.0.2 → 5.1.0 is therefore refused by the entrypoint by design, and `unpause` needs `rehearsal_ok`, which only `rehearse` can write. The drill is: deploy a signed test release that fails its health check,
the entrypoint rolls back by itself, and the table shows that the live version is unchanged and the test version is blocklisted. Two extra versions are needed (one per kind) and they are burnt by the drill.

### The switch stage (root-free deploy), fail-safe

`--switch` runs `switch-service-account.sh` as root **only if** all gates of the measurement are OK **and** that script is shipped and pinned in `eve-session.sh` (`PIN_SWITCH_SHA256`; empty today, so the stage is skipped).
Gates: 01 macOS on Apple silicon, 03 the trust chain of paths (root-owned, not group/world writable, no ACL, no symlink), 04 link swap with `mv -fh`, same volume and room to copy, 09 code signature of an existing gateway binary.
Any gate that is FAIL **or UNKNOWN** skips the switch; the release continues over the existing root path. Contract of the script: exit 0 = switched, exit 10 = skipped itself (nothing changed), anything else stops the whole session.

## State and secrets

`~/.eve-session/` (0700) holds a small state file (`oneshot=<M>`) and the clone of eve-web. Values are validated and never sourced. No secret is read, printed or stored by this script: the keys are only created with
`ssh-keygen` (no passphrase, as `release-tag.sh` requires) and their public halves printed. Everything root runs is a text whose sha256 was checked before `sudo`.

## What this does not do

- It does not create the 5.1.0 content on `main` (merge first), nor the two failing test releases of the rehearsal.
- It does not touch `#168`'s `--gateway-deploy`; deploys go through `setup-unattended.sh deploy`.
- Not tested here on macOS/bash 3.2 (CI runs `test/run.sh` there).
