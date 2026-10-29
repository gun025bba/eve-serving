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

The branch rule is enforced **in CI** (the workflow refuses to sign unless the event is a push of `refs/tags/vX.Y.Z` and the tagged commit *is* the tip of `main` at signing time; `release/*` branches remain usable for maintenance, a push to one may be built by `ci.yml` but never signs, and a tag on a `release/*` commit that is not the `main` tip is refused; a higher tag on an older `main` commit is refused too) and recorded in the signed manifest; the Mac cannot
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
  "name": "main-protect",
  "target": "branch",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["~DEFAULT_BRANCH"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "required_reviewers": [],
        "require_code_owner_review": true,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true,
        "require_extra_approval_for_unattributed_changes": true,
        "allowed_merge_methods": [ "merge", "squash", "rebase" ] } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": true,
        "do_not_enforce_on_create": false,
        "required_status_checks": [ { "context": "test" }, { "context": "deploy-nopasswd-macos" } ] } }
  ]
}
```

   Linear history is **on** (`required_linear_history`): pull requests land by **squash or rebase** only, never as a merge commit (the `allowed_merge_methods` list still names `merge`, but
   linear history makes GitHub refuse it); the tag is cut on the tip after the squash/rebase. The JSON above is the live ruleset (read with `gh api repos/gun025bba/eve-serving/rulesets/<id>`).
   **Open points before install** (not yet in the live ruleset; the pre-install conditions from security): `required_approving_review_count` 1 instead of 0, and `require_last_push_approval` true
   once the agent uses its own account, so the person who pushed last cannot be the approver.
   CODEOWNERS (`.github/CODEOWNERS`) is a single rule, `* @gun025bba`: the owner for every path, so `require_code_owner_review` turns every change to the trust root into a review by the owner.
2. **Ruleset on tags** `v*` (target `tag`, include `refs/tags/v*`), live as `release-tags`, enforcement `active`, **no bypass actors**:

```json
{
  "name": "release-tags",
  "target": "tag",
  "enforcement": "active",
  "conditions": { "ref_name": { "include": ["refs/tags/v*"], "exclude": [] } },
  "bypass_actors": [],
  "rules": [
    { "type": "creation" },
    { "type": "update" },
    { "type": "deletion" },
    { "type": "non_fast_forward" }
  ]
}
```

   With no bypass actor nobody can push a `v*` tag. Release tags are pushed by `deploy/nopasswd/release-tag.sh` with the **release-tag deploy key** (next section), which is the
   **only** bypass actor of **this** ruleset (actor type "Deploy key"; no person, no bot account, no app, no repository role). The deploy key is in no other ruleset's bypass list, in particular not in `main-protect`.
   **Open point before install** (security's condition): register the key and add it as that one bypass actor; then verify that creating a `v*` tag as the agent account is refused.
   A tag that already exists cannot be moved or deleted. Verify that creating a tag as the agent account is refused: the exact bypass semantics differ between repository roles.
3. **Environment `deploy`** (Settings, Environments): *Deployment branches and tags*: "Selected branches and tags" with a **tag** rule `v*` and nothing else. Required reviewers are optional
   (add one if every release should wait for approval). No secrets in the environment.
4. **Immutable releases** (Settings, General, Releases): enabled, so published assets cannot be replaced or deleted and a tag of a release cannot be re-pointed.
5. **Actions**: "Require actions to be pinned to a full-length commit SHA" on (Settings, Actions, General; every workflow in this repository is already pinned, `test/run.sh` checks it).
   Workflow permissions default: read-only. "Allow GitHub Actions to create and approve pull requests": off.
6. **`eve-web`**: the same rulesets, environment, immutable releases and SHA pinning. Its release workflow only calls the reusable workflow above (`eve-web-release.yml.example`).
   `deploy/nopasswd/mac-oneshot.sh` applies it (run once by the owner on the Mac, idempotent, `--dry-run` available, verify table at the end). Order: local key first; pinned install; throwaway `gh` login (must be `gun025bba`, admin of `eve-web`);
   **preflight (read-only): list the repository's deploy keys, and if any WRITE key other than ours exists (same public key or same title `eve-web-release-tagger`) abort with no change at all**; get the write deploy key
   `eve-web-release-tagger` (`~/.eve-web-release-tagger/id_ed25519`) registered **by the owner in the browser** (the script copies the public key to the clipboard, opens the page, waits for `added` and reads the key list back; **a deploy key
   created through the API with the setup login's OAuth token is deleted by GitHub when that token is revoked** (docs, "REST API endpoints for deploy keys"), and the last step revokes it); **M-1b: right after you added it, the deploy keys are read again and any write key other than this public key aborts the run before a ruleset is touched; if our key is not registered, `release-tags` is applied without the DeployKey bypass (admin bypass only)**; then the settings: ruleset `release-tags` on `refs/tags/v*` (**creation only**; bypass = the repository admin role **and** the `DeployKey` actor), ruleset
   `release-tags-lock` on `refs/tags/v*` (update/delete/non-fast-forward blocked; bypass = the **admin role only**, so the deploy key can create a tag but never move or delete one); `main-protect` with linear history and the strict
   required check `test`; environment `deploy` (tags `v*` only, admins cannot bypass; whether GitHub applied `can_admins_bypass=false` is decided by the verify table, not by the PUT's status); immutable releases; SHA pinning;
   read-only default token permission; fork PR approval for all external contributors.
   Differences from this repository, on purpose: `eve-web` has the admin role as a second bypass actor on `release-tags`, and no `pull_request` rule on its main (not requested). **A `DeployKey` bypass is not tied to one key:
   it covers every write deploy key of the repository**, hence the preflight above, and the verify step fails if another write key exists afterwards.
   **`--only-fix`** (repair run after a first run with FAIL rows): no sudo install, the key files are only read (never created, chmod-ed or rewritten), rulesets / environment / immutable releases are only read back;
   it logs in again, keeps the foreign-write-key preflight and the M-1b re-check, gets the existing public key registered (browser, as above), re-applies only `release-tags` (its DeployKey bypass depends on the key), applies the three Actions settings, runs `ssh -T`, revokes the token (401 proof) and prints the whole table.
   The revocation proof sends the token to curl on stdin (never in the environment or on a command line); xtrace, `SHELLOPTS`, `GH_DEBUG` and `DEBUG` are switched off first and error text is masked; the revoke instructions are shown even if the token could not be read; the verify table also checks `main-protect`'s bypass actors (none) and its required check's `integration_id` (GitHub Actions) and that the `.pub` is the public key of the private key.
   Every failing API call prints its HTTP status and GitHub's message (`API ERROR: ...`, never a token); a failing Actions call is retried once with the other REST API version, and the table ends with by-hand instructions for what failed.
   **The OAuth token of the setup login**: `gh auth logout` only deletes the local copy; GitHub keeps honouring the token. The script therefore logs out, deletes its throwaway config dir, prints
   <https://github.com/settings/applications> (Authorized OAuth Apps, "GitHub CLI", Revoke), waits for the owner to type `revoked`, and then proves it by calling `gh api user` with the token and requiring HTTP 401;
   anything else is a FAIL row and a non-zero exit. This also runs when the script stops early after the login (error, Ctrl-C). **SIGKILL (`kill -9`) and power loss cannot run any cleanup**: the throwaway directory
   (`$TMPDIR/eve-oneshot-gh.*`, mode 0700) then stays with the token inside; delete it by hand and revoke the authorization at that link.
   Until that repository is hardened and the owner has checked the summary, the installed entrypoint
   refuses `kind=web` altogether (`WEB_ENABLED=no` in `eve-deploy`; turning it on = a reviewed change of that constant, a new pin and a new one-time install).
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

## The release-tag deploy key

Tags are what the signing workflow reacts to, so who can create one decides what can be signed. The tag is created by `deploy/nopasswd/release-tag.sh`, run by the owner on the Mac, with a
key that exists for this one purpose.

- **Location and permissions.** On the Mac, `$HOME/.eve-release-tagger/id_ed25519` (override with `EVE_RELEASE_TAG_KEY`), directory mode `0700`, file mode `0600` (`0400` also works), owned by the user who runs the script.
  The script refuses a missing key, a symlink, another owner, or any mode that gives group or others access. The private key is never in the repository, in CI, in an Actions secret, in the bots' environment, or on the box
  that prepares releases (any copy there was deleted); only the public half (`id_ed25519.pub`) is ever shown or registered. The script never reads, copies, prints or commits the key: it only passes its path to `ssh -i`.
- **Registered as** a repository **deploy key** of `gun025bba/eve-serving` with write access (Settings, Deploy keys), titled `release-tagger`, and nowhere else (not an account SSH key).
- **Bypass.** The deploy key is in the `release-tags` ruleset's bypass list **only** (checklist item 2). It is not a bypass actor of `main-protect`, so it cannot push to `main`; the script itself pushes exactly one ref,
  `refs/tags/vX.Y.Z` (no branch, no force, no `--tags`/`--all`/`--delete`; `release-tag-test.sh` fails if that changes).
- **Revocation: delete the deploy key.** If the key may have leaked (a copy of the file, a backup, a compromised Mac account), delete it in Settings, Deploy keys (or
  `gh api -X DELETE repos/gun025bba/eve-serving/keys/<id>`, the id comes from `gh api repos/gun025bba/eve-serving/keys`). With the key deleted GitHub accepts nothing signed by it, and with it
  disappears the only bypass of `release-tags`, so nobody can create a `v*` tag until a new key is registered. Then remove the file on the Mac, and review what the key could have done meanwhile:
  `gh api repos/gun025bba/eve-serving/git/matching-refs/tags/v` (tags that you did not cut), the releases page, and the Rekor entries for the repository. A tag of an installed version cannot be re-used (the Mac only accepts higher versions)
  and tags cannot be moved or deleted. To continue: `ssh-keygen -t ed25519 -f ~/.eve-release-tagger/id_ed25519` on the Mac (no passphrase is required by the script, a passphrase needs `ssh-agent` and is not supported by it), register the new public key, and put it in the bypass list again.
- **No human review before tagging (태그 전 사람 리뷰 없음).** The script does not look for any reviewer, review or approval, and a person does not have to approve a tag. What stands between a change and a tag is the branch protection of `main`
  (a pull request and the required checks, see the ruleset above) and the gates below; an approval step per release was rejected because the owner accepted unattended releases.
- **What the script requires before it pushes** (it refuses, exit 1, and pushes nothing, unless all hold):
  1. the target SHA equals the tip of `main` on the remote (`git ls-remote`, read again right before the push; the default target is that tip);
  2. the version is strictly greater than the highest existing `vX.Y.Z` tag on the remote;
  3. the checks `test` and `deploy-nopasswd-macos` have a newest run from GitHub Actions for that SHA with conclusion `success`, and that run belongs to a workflow run of **`.github/workflows/ci.yml`** of this repository on exactly that
     commit (the script reads `GET /repos/.../actions/runs/<id>` of each candidate; a same-named job of another workflow file, repository or commit is ignored, so it can neither pass nor mask the real check).
  Nothing else is queried from GitHub: no pull request, review, comment or account is read.
- **Malformed tags: the script refuses.** The version order is only defined for strict `vX.Y.Z` (no leading zeros). If the remote has **any** other tag matching `v*` (for example `v5.0.0-rc1`, `v9`, `v05.0.0`), the script refuses to tag at all
  and names the tag, instead of ignoring it: an ignored `v99.0.0-rc1` would hide how high the line really is, and refusing is the fail-safe choice. The `release-tags` ruleset blocks creating, updating and deleting `v*` tags for everybody but its bypass actors,
  so such a tag can only come from a bypass actor (the deploy key); recovery is a reviewed decision (use the "version too high" recovery below or have an admin remove the stray tag through the ruleset), not something the script works around.
  Peeled `^{}` lines of annotated tags are expected and fine.
- **Run it from a clean clone of `main`, with git 2.32 or newer.** The script refuses unless `main` is the checked-out branch and no tracked file is modified (the script itself included), unless `git --version` is 2.32 or newer
  (older git ignores `GIT_CONFIG_GLOBAL`/`GIT_CONFIG_NOSYSTEM`, which the config neutralisation relies on), and unless `git ls-remote --get-url git@github.com:gun025bba/eve-serving.git` prints exactly that URL (no active URL rewriting).
  Untracked files are not checked (they cannot change what is tagged: the tag is made from the remote's SHA). A fresh `git clone` of the repository is the intended place; do not run an edited copy.
- **Constants, not environment.** The repository (`gun025bba/eve-serving`) and the remote (`git@github.com:gun025bba/eve-serving.git`) are constants in one marked block at the top of `release-tag.sh`; no environment variable
  changes them (`EVE_REPO`, `EVE_RELEASE_TAG_REMOTE` do not exist). The only variable is `EVE_RELEASE_TAG_KEY` (path of the key).
- **How the script reaches GitHub.** `git` talks to the remote only through `ssh -F /dev/null -i <key> -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=<repo>/deploy/nopasswd/github_known_hosts
  -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes -o IdentityAgent=none -o UpdateHostKeys=no`: no ssh config, no agent, no prompts, no host key updates written by the server, and **only GitHub's published host keys** (`deploy/nopasswd/github_known_hosts`, copied from
  [GitHub's SSH key fingerprints](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints) and `https://api.github.com/meta`; a rotation is a reviewed change to that file).
  Nothing has to be added to `~/.ssh/known_hosts`. Git's global and system config are switched off (`GIT_CONFIG_GLOBAL=/dev/null`, `GIT_CONFIG_NOSYSTEM=1`, git 2.32 or newer) so no `url.*.insteadOf` can redirect the push, and the script refuses a clone whose
  own config sets `url.*.insteadOf` or `pushInsteadOf`. If `ssh` is missing (`openssh-client`), the script stops with a message before touching anything.
- **Limits that remain (documented Lows).**
  - *The gate lives only in the script on the Mac.* GitHub does not enforce it: whoever holds the deploy key can push a `v*` tag by hand (ssh with the key, no script). Protection is the key's location and permissions
    (the Mac only, never in CI or on the bots' box), the revocation above, and the Mac's own checks (pinned signing identity, strictly increasing version, branch `main`, `built_at` window).
  - *The `gh` token on the Mac must be read-only.* The script only does `GET` requests and never passes the key to `gh`, but a token with write scope would let the same user do more than the script allows; use a read-only login
    (for example a fine-grained token with read access to contents, pull requests and checks, or `gh auth login` without extra scopes) and never the release bot's token.
  - *The key's directory is checked too.* The script refuses a directory that is a symlink, not owned by you, or has any group/other access (only mode `0700`, or `0500`, passes), because whoever can write there could swap the key file after the check.
  - *A squash merge needs nothing extra.* The tag is made on the merged commit (the new tip of `main`); no review names it, because none is required.
  - *The CI workflow path is pinned.* The signing identity the Mac trusts is the workflow path of `release.yml` and `deploy-sign.yml@main` on a `refs/tags/vX.Y.Z` ref; a change of those paths (rename, move, a fork or a different
    workflow) changes the identity and the Mac then refuses the artifact until the pin is updated by a reviewed change. The checks the script looks at are the ones named `test` and `deploy-nopasswd-macos` from GitHub Actions only.
- **Use** (on the Mac, inside a clean clone of `main`, with `gh` logged in read-only and `jq` installed; macOS 15 and newer ship `jq`, otherwise `brew install jq`; start it with `bash`):
  `bash deploy/nopasswd/release-tag.sh --dry-run 5.1.0` checks everything and pushes nothing; without `--dry-run` it creates the annotated tag and pushes it.
- **Tests**: `deploy/nopasswd/test/release-tag-test.sh` (run by `test/run.sh`, hence by both CI jobs) uses fakes for `gh`, `git` and `ssh` and a local bare repository; it needs no network and no key, and never creates a real tag.

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
