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

Options: `--web-only` (shortest path: preflight, deploy key, one-shot, web tag, release, deploy, rehearse, table; no gateway, no switch, PAUSE never released; `--gateway`, `--switch`, `--unpause` are refused), `--rehearse-gateway X.Y.Z --rehearse-web X.Y.Z` (signed test releases built to fail their health check), `--switch`, `--unpause`, `--dry-run`, `--wait-minutes N`, `--force-install`.
`--dry-run` checks the pins, the clone and runs the real read-only measurement; it needs no sudo, no network and changes nothing.

## Stages (hours are wall time, mostly waiting for CI and for you)

| # | stage | h | what |
|---|---|---|---|
| 0 | preflight | 0.1 | pins, clone state, tools, `gh` login, the 14-item read-only measurement (`eve-measure.sh`) |
| 1 | sudo | 0 | one `sudo -v` and a keep-alive |
| 2 | keys | 0.2 | creates the two release-tag deploy keys if missing, tells you where to add them (Title, key, **Allow write access**), you type `registered`; GitHub must accept both |
| 3 | install | 0.3 | `eve-oneshot.sh --merge-commit M` (entrypoint, all rulesets incl. the eve-web `pull_request` rule, PAUSE) |
| 4 | switch | 0.5 | optional, see below |
| 5 | web tag | 0.1 | `release-tag.sh --repo eve-web` from a clone of eve-web; you type `tag`; an existing tag is never touched. **Fatal** on failure |
| 6 | web release | 0.4 | waits until the web release has exactly `MANIFEST`, `sigstore.json`, `tar` and is published. **Fatal** |
| 7 | web deploy | 0.3 | `setup-unattended.sh deploy web Y` (never a downgrade). **Fatal** |
| 8 | web rehearse | 0.5 | `setup-unattended.sh rehearse web test-version` (optional) |
| 5g-8g | gateway branch | 1.0 | after the web path: gateway tag (tip of main must be `M`, else "main moved": skipped, re-run with the new merge commit), release, deploy, rehearse. **Independent: a failure here is a FAIL row and exit 1, never undoes or blocks the web release** |
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

### The temporary-token proof is a WARN, the OAuth proof is not

At its end `eve-oneshot.sh` makes you revoke the OAuth token of the run (you type `revoked`) and **proves it dead with HTTP 401: unchanged, a FAIL otherwise**. For the temporary token (the fine-grained token that opened the PR) it asks you to paste it, hidden:
- GitHub answers 401: proven, nothing more to do.
- Empty input (nothing pasted): **WARN** (the run is not stopped, exit 0). Follow-up: open the fine-grained tokens page and check that the token is gone from the list (delete it if not). `eve-session.sh` repeats it as a `WARN  temporary token revocation` row in its table (state key `oneshot_warn`, not a failure).
- A token that was pasted but gets no clear 401 (200 still accepted, 403, 5xx, timeout, curl error): FAIL, as before.
- A token that is already 401 when the run starts (`EVE_TEMP_TOKEN`): a WARN line instead of a stop.

### The rehearsal drill invariants (checked before and after)

Before: the live version is valid, the test version is **greater** than it, unused and not on the blocklist. After: floors, `current_version`/`current_web_version`, `releases` and `PAUSE` are unchanged, nothing was promoted, and the blocklist gained **exactly one line** (the test release) and lost none.

### The deploy-key bypass is fail-closed

`eve-oneshot.sh` grants the ruleset bypass only when **exactly one** write key with the expected title exists and no foreign write key does. The check is repeated after the ruleset write; on any change the no-key ruleset is restored and the run stops.

### The service-account hook is off by default

`--switch` is opt-in, `PIN_SWITCH_SHA256` is empty and no switch script ships, so the stage is always skipped (a test asserts this). The service-account MVP is on hold.

## Mapping: measurement item -> what it shows -> who needs it

`eve-measure.sh` is read-only: no `sudo`, no network, no write outside one `mktemp -d` and the optional `--out` file, environment **keys** only (never values), file contents of keys/settings/databases never read (path, owner, mode). A test asserts all three.

| item | measures | gate | needed for |
|---|---|---|---|
| 01 | macOS, Apple silicon, SIP | yes | switch (any other host skips it) |
| 02 | service accounts and group membership (numeric ids) | no | account design review |
| 03 | trust chain of paths: root-owned, not group/world writable, no ACL | yes | switch |
| 04 | `mv -fh` link swap, one volume, room to copy | yes | switch (atomic release switch) |
| 05 | gateway plist keys (names only) | no | env allow-list design |
| 06 | listeners | no | exposure review |
| 07 | key, kill-switch, database settings: path/owner/mode | no | permission design |
| 08 | sizes, file counts, hash pass time | no | copy and verify budget |
| 09 | codesign, Gatekeeper, quarantine of an existing gateway binary | yes (if one exists) | switch |
| 10 | baseline of the root path: sudoers file and tools | no | before/after comparison |
| 11 | GUI session, calendar adapter LaunchAgent | no | agent placement |
| 12 | PostgreSQL and mlx baseline | no | data-path review |
| 13 | FileVault, boot evidence | no | review |
| 14 | tailscale serve, pf (pf is UNKNOWN without root) | no | exposure review |

The web path needs none of the gates; only the switch does.

## State and secrets

`~/.eve-session/` (0700) holds a small state file (`oneshot=<M>`) and the clone of eve-web. Values are validated and never sourced. No secret is read, printed or stored by this script: the keys are only created with
`ssh-keygen` (no passphrase, as `release-tag.sh` requires) and their public halves printed. Everything root runs is a text whose sha256 was checked before `sudo`.

## What this does not do

- It does not create the failing test releases of the rehearsal (one per kind).
- Not tested here on macOS/bash 3.2 (CI runs `test/run.sh` there).
