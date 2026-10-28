# CI-signed deploy: design choice and threat model

Scope: `deploy/nopasswd/` (the optional passwordless deploy of the gateway and the UI to the macOS host). Supersedes the laptop-key
design of PR #90 (an `ssh-keygen -Y` signature from a key on a person's machine), which made every deploy wait for that person.

## Flow

```
tag vX.Y.Z on the CURRENT TIP of main (any other commit is refused)
   |  push
   v
release.yml  --calls-->  gun025bba/eve-serving/.github/workflows/deploy-sign.yml@main   (reusable; the SIGNER identity; inputs: kind and paths only)
   |  job build   (contents: read, no token)   release-guard.sh from main's checkout: push event, tag ref, commit == tip of origin/main
   |                                           go build darwin/arm64 gateway + eve-gw  (web kind: tar the tracked UI files)
   |                                           MANIFEST: version, git_sha, repo, ref, branch=main, schema, sha256 of the tar and of each member
   |  job sign    (id-token: write only, environment deploy, runs no repository code)
   |                                           re-checks event/ref/manifest, cosign sign-blob --bundle (keyless: Fulcio certificate + Rekor entry), verifies it
   v  job publish (contents: write only)       GitHub release: eve-<kind>-X.Y.Z.{MANIFEST,sigstore.json,tar}
Mac:  fetch (user, downloads, trusts nothing)  ->  sudo deploy <verb> (root, exact verbs, no argument)
        copies the three files into a root-only dir and, on the copies: verifies the signature OFFLINE, then the manifest, then the shas
```

## The choice: Sigstore keyless, verified offline, identity pinned to a reusable workflow

Chosen over the alternatives for one reason: **there is no signing secret to steal.** Whatever a signature proves, it must come from the
release workflow of this repository, running at `main`, for a pushed tag, and nobody holds a key that works anywhere else.

| Option | Verdict |
|---|---|
| Sigstore keyless (`cosign sign-blob`) | **Chosen.** Short-lived (about 10 minutes) Fulcio certificate whose subject is the workflow and whose extensions carry repository, ref, commit and trigger; the signature is also logged in Rekor. The bundle holds the certificate and the transparency proof, so the Mac verifies with a pinned trust root and **no network and no state** (tested with an empty `HOME`, dead proxies, and `unshare -rn`). |
| GitHub artifact attestations (`gh attestation verify`) | Same Sigstore machinery, but the Mac would need the `gh` CLI (a large, frequently updated binary to pin and trust as root's verifier) and GitHub's attestation API or a hand-built trusted-root. `cosign` is one static binary pinned by sha256 and exposes every identity claim as a flag. |
| ssh key in an Actions secret / `deploy` environment | Rejected. The secret can be exfiltrated by anything that runs in the job (a poisoned action, a malicious workflow edit on a branch that is allowed into the environment) and then signs **forever**, from anywhere; it needs rotation and an approval gate to be safe. A keyless certificate is useless off the run that obtained it. |

### Why the identity is a reusable workflow at `@main`

A certificate's subject (`job_workflow_ref`) is the workflow file that contains the job. If `release.yml` ran its own steps from the tagged
commit, anyone able to push a tag could edit that file in the tagged commit and mint a certificate with the same subject. So the signing job
lives in `deploy-sign.yml`, callers must reference it as `gun025bba/eve-serving/.github/workflows/deploy-sign.yml@main` (never `./`), and the Mac pins
the subject `https://github.com/gun025bba/eve-serving/.github/workflows/deploy-sign.yml@refs/heads/main`. The other claims in the certificate are the
**caller's**: repository, `refs/tags/vX.Y.Z`, the commit and `push`. A modified workflow in a tagged commit (or on any other branch) cannot obtain the
pinned subject; this was confirmed with a real CI run (the fixtures in `deploy/nopasswd/test/fixtures` are real certificates and the tests assert
that the production pins reject them). eve-web calls the same reusable workflow (`eve-web-release.yml.example`); the Mac pins repository
`gun025bba/eve-serving` for `kind=gateway` and `gun025bba/eve-web` for `kind=web`, so one repository cannot ship the other's artifact kind.

**There is no test, fixture or bypass mode in the signing workflow** (a former `fixture` input would have let any caller who can reach `@main` mint a certificate with
the pinned subject for an arbitrary ref). The workflow takes only `kind` and three path inputs; the scripts it runs always come from a checkout of `main`
(`.eve-tools`), never from the tagged commit; the guard (push event, `refs/tags/vX.Y.Z`, commit equals the tip of `origin/main`) always runs and has no switch. Test
signatures come from a *different* workflow file, `deploy-sign-fixture.yml`, whose certificate subject names that other file, which the Mac does not trust (it cannot
publish, it is not reusable, it runs only for branches named `fixture/*` or by hand). `test/run.sh` fails if any of this changes.

## What the Mac verifies (all before anything is installed; the live system is untouched on any failure)

1. Sudoers lets the deploy user run only `deploy`, `deploy-web`, `rollback`, `rollback-web` on one exact path, with **exactly those arguments and no wildcard**
   (a sudoers glob would also be rejected by sudo-rs, and an exact list cannot be widened by a crafted argument). The version is not an argument: `deploy` takes the
   one staged release of that kind (`fetch` leaves exactly one; more or none is refused) and the signed manifest must carry that very version. The entrypoint also re-validates its arguments.
2. The entrypoint's own environment is clean (`bash -p`, fixed PATH, no inherited variables), the caller is the allowed user, and the files are **copied**
   into a root-only directory first: everything below runs on the copies (no check/use race against the user-writable staging dir).
3. `cosign` and `trusted_root.json` match the sha256 pinned in the entrypoint (and the installer pins the entrypoint by sha256).
4. `cosign verify-blob --bundle --trusted-root`, with `--certificate-oidc-issuer`, `--certificate-identity` (the workflow at `@main`),
   `--certificate-github-workflow-{repository,ref,sha,trigger}` all pinned. The ref must be `refs/tags/v<staged version>`, the trigger `push`, the repository the kind's repository.
5. `MANIFEST` is parsed with a strict allow-list (known keys only, no duplicates, bounded size, safe characters); `kind`, `version` (equals the
   request), `ref`, `branch` (exactly `main`; set by CI after the tip check), `git_sha` equal to the certificate's commit, the architecture, and `schema` (equal to the database's schema: a different schema needs the migration deploy script, not this path).
6. **Monotonic version**: strictly greater than the installed version of that kind (separate lines for gateway and UI, stored root-only).
   **Freshness**: the signed `built_at` must not be older than 14 days, nor more than 5 minutes in the future (checked after the signature, against the Mac's clock;
   a Mac with a wrong clock fails closed). A validly signed old release therefore cannot be replayed after two weeks even if it is newer than the installed one.
7. sha256 of the tar, then of each member, from the signed manifest; the tar is extracted by a hardened unpacker (no links, devices, absolute or `..` paths, bounded size and count).
8. Swap with one previous version kept, restart, health check; a failed health check restores the previous version automatically. Signals during the swap are handled.

The branch rule is enforced **in CI** (the workflow refuses to sign a tag unless its commit *is* the tip of `main`; there are no `release/*` branches) and recorded in the signed manifest; the Mac cannot
check Git ancestry offline and relies on the pinned workflow (at `main`) for it. That is why `deploy-sign.yml` is the trust root and must be protected as described below.

## Threat model

| Threat | Mitigation | Residual risk |
|---|---|---|
| **Repository compromise: attacker can push a branch or tag** | A tag on any commit other than the tip of `main` is not signed and the Mac re-checks the signed `branch`. A modified `deploy-sign.yml` or `release.yml` on that branch or tag cannot produce the pinned subject (`@refs/heads/main`). A fork or another repository has a different `repository` claim. | Attacker who can **merge to `main`** gets code signed, exactly as with any CD. At the time of writing **`main` has no branch protection**; apply the checklist below before the one-time install. Attacker who can change `deploy-sign.yml` on `main` controls the trust root: same control needed. |
| **CI secret theft** | No signing key and no deploy secret exist anywhere. The only credential is the job's OIDC token, minted inside the run, audience-restricted, short-lived, and only usable to obtain a certificate that names this run. Actions and `cosign` are pinned by commit sha / sha256; the sign job executes no repository code at all (the build job, which has no token, compiles the tagged commit; the sign job only checks and signs its output). | A step that runs in the signing job could sign what it likes **during that run**; hence the pinned third-party actions, per-job permissions (`id-token: write` only on the sign job, `contents: write` only on the publish job) and the `deploy` environment as an optional approval gate. The release is public and logged in Rekor, so a rogue signature is detectable after the fact. |
| **Downgrade** | Strictly increasing version per kind, root-only state; the signed `version` must equal the staged one (the file name the bare verb read); an equal or lower version is refused even if validly signed. The schema must equal the database's. A signed release older than 14 days is refused. `rollback` only restores the single previous version (no signature needed: it re-installs what root had installed, protected by a tree digest). | None for versions at or below the installed one. |
| **Replay of an old signed release** | Signature binds version, commit, ref, repository, kind and every sha256; a signed `X.Y.Z` cannot be relabelled (version/ref/sha mismatch tests). | A genuinely signed release **newer than the installed one and at most 14 days old** can be re-offered (for example 5.0.3 when 5.0.1 is installed; there is no revocation list). Mitigation: deploy promptly, keep the deploy user from fetching from arbitrary hosts (the fetcher only talks to `github.com/<pinned repo>/releases`), and never delete the release a bad build came from without also shipping a higher fixed one. |
| **Tag moved or re-created** | The certificate records the commit; `git_sha` in the manifest must equal it; the version ratchet makes a re-signed tag of an installed version unusable. | Protect `v*` tags (below) so a tag cannot be moved before its first deploy. |
| **Tampered download or staging dir** | Nothing in staging is trusted: copy, then verify the copies; sha256 of every file from the signed manifest; hostile tars rejected. | None identified. |
| **Compromise of the deploy user (the bot account)** | The sudoers rule gives no shell and no argument freedom: it can deploy a validly signed, strictly newer release, or roll back once. It cannot choose code, bypass the pins, or run anything else as root. | It can cause a deploy of a legitimately signed newer release, or a rollback; that is the point of the path. |
| **Sigstore infrastructure** | Trust root pinned by sha256 and verified offline. | A compromise of the public-good Fulcio CA or Rekor is out of scope. When Sigstore rotates its keys, new releases fail **closed**; fix: update `trusted_root.json` + the pin through a reviewed PR and rerun the one-time install (a new pinned entrypoint). |
| **The one-time install itself** | The README line pins the installer's sha256 and fetches it by commit sha; the installer pins every file it installs (entrypoint, fetcher, uninstaller, trust root, `cosign` by sha256) and drops all `EVE_*` test knobs when root. | The person running it must take the line from a trusted channel and compare the PR's commit; the real `sudo`/sudoers/`/var/db` behaviour is only exercised by the manual runbook, not by CI. |

### Not covered

An approved but malicious change on `main`; theft of a maintainer's GitHub account with push-to-main rights; compromise of the Mac or its
root account; the web swap's brief window between two directory renames (the live UI path is replaced by two renames; a failure restores it, a crash
in the window leaves it recoverable with `rollback-web`).

## Pre-install checklist (repository settings; the owner applies them, this PR changes none)

The Mac trusts "whatever `deploy-sign.yml` on `main` signs", so the one-time install must not be run until all of these are in place and checked. Names are those of the
GitHub UI (Settings, Rules, Environments); the JSON is the ruleset body for `gh api --method POST repos/gun025bba/eve-serving/rulesets --input -`.
The agent that prepares releases must use a separate GitHub account or App, **not** the owner's account, and must not be an admin or a bypass actor.

1. **Ruleset on the default branch** (`main`), enforcement `active`, **no bypass actors**:

```json
{
  "name": "main",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 1,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": true,
        "require_last_push_approval": true,
        "required_review_thread_resolution": true } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [ { "context": "test" }, { "context": "deploy-nopasswd-macos" } ] } }
  ]
}
```

   Linear history stays **off** (merge commits; the tag is cut on the tip after the merge). `require_last_push_approval` means the person who pushed last cannot be the approver.
   CODEOWNERS (`.github/CODEOWNERS`, in this PR) names the owner for `.github/`, `deploy/nopasswd/` and the two signing docs, so `require_code_owner_review` turns every change to the trust root into a review by the owner.
2. **Ruleset on tags** `v*` (target `tag`, include `refs/tags/v*`): rules `creation`, `update`, `deletion`. Only the designated human may create a release tag: make that person the one
   bypass actor of **this** ruleset (for example the repository `admin` role), and nobody else. A tag that already exists then cannot be moved or deleted. Verify in the UI that creating
   a tag as the agent account is refused: the exact bypass semantics differ between repository roles.
3. **Environment `deploy`** (Settings, Environments): *Deployment branches and tags*: "Selected branches and tags" with a **tag** rule `v*` and nothing else. Required reviewers are optional
   (add one if every release should wait for approval). No secrets in the environment.
4. **Immutable releases** (Settings, General, Releases): enabled, so published assets cannot be replaced or deleted and a tag of a release cannot be re-pointed.
5. **Actions**: "Require actions to be pinned to a full-length commit SHA" on (Settings, Actions, General; every workflow in this repository is already pinned, `test/run.sh` checks it).
   Workflow permissions default: read-only. "Allow GitHub Actions to create and approve pull requests": off.
6. **`eve-web`**: the same rulesets, environment, immutable releases and SHA pinning. Its release workflow only calls the reusable workflow above (`eve-web-release.yml.example`).
7. **Accounts**: the owner's account has two-factor authentication with a passkey or hardware key; the agent account (or App) has write access only, no admin, no ruleset bypass, no ability
   to edit environments, and cannot approve its own pull requests.

Verify (read-only; each command only reads settings):

```
gh api repos/gun025bba/eve-serving/rulesets --jq '.[] | {name, target, enforcement}'
gh api repos/gun025bba/eve-serving/rulesets/<id> --jq '{bypass_actors, rules: [.rules[].type]}'     # main: bypass_actors must be []
gh api repos/gun025bba/eve-serving/environments/deploy --jq '.deployment_branch_policy'
gh api repos/gun025bba/eve-serving/environments/deploy/deployment-branch-policies --jq '.branch_policies[] | {name, type}'   # one entry: v* of type tag
gh api repos/gun025bba/eve-serving/immutable-releases                                              # enabled: true (if the endpoint is not available yet, check the UI)
gh api repos/gun025bba/eve-serving/actions/permissions --jq '.sha_pinning_required'                # true
```

## If a version that is too high was recorded

The Mac only ever accepts a strictly higher version per kind, so a mistaken release (for example `v9.0.0` instead of `v5.0.3`) that was already deployed would block the normal line.
Recovery needs root on the Mac, and nothing else:

1. As root, write the last legitimate version into the state file: `printf '%s\n' 5.0.2 >/var/db/eve-deploy/current_version` (`current_web_version` for the UI line).
2. Cut the next release with a version above that and deploy it as usual. The mistaken release stays published (releases are immutable) but is below the installed line once a higher one is deployed.

An over-high tag that was **only published, not deployed** needs nothing: the Mac never installed it. Do not delete or re-create it; release the next higher version.

## Operating notes

- Releases, the web flow, install and removal: `deploy/nopasswd/README.md`. Mac-side checks after install: `docs/runbooks/passwordless-deploy.md`.
- The test fixtures are real signatures from CI on a throwaway branch and are inert: a branch is not an accepted ref, so production pins reject them. They were produced by the then-existing
  fixture mode of `deploy-sign.yml` (so their certificate subject names that file at `fixture/...`); the signing workflow no longer has that mode. To regenerate them (only if the manifest format
  changes), push a branch `fixture/<name>`: `deploy-sign-fixture.yml` signs them (subject names that workflow file; run the tests with `EVE_TEST_FX_WORKFLOW=deploy-sign-fixture.yml`), each run adds a permanent public Rekor entry.
- Time: freshness uses the Mac's clock. If releases are refused with "in the future" or "older than 14 days" for a release you just built, check the Mac's time first.
