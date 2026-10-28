# CI-signed deploy: design choice and threat model

Scope: `deploy/nopasswd/` (the optional passwordless deploy of the gateway and the UI to the macOS host). Supersedes the laptop-key
design of PR #90 (an `ssh-keygen -Y` signature from a key on a person's machine), which made every deploy wait for that person.

## Flow

```
tag vX.Y.Z (commit on main or release/*)
   |  push
   v
release.yml  --calls-->  gun025bba/eve-serving/.github/workflows/deploy-sign.yml@main   (reusable; the SIGNER identity)
   |  checks the tag commit is reachable from origin/main or origin/release/*
   |  go build darwin/arm64 gateway + eve-gw  (web kind: tar the tracked UI files)
   |  MANIFEST: version, git_sha, repo, ref, branch, schema, sha256 of the tar and of each member
   |  cosign sign-blob --bundle   (keyless: Fulcio certificate bound to this workflow run + Rekor entry)
   v  GitHub release: eve-<kind>-X.Y.Z.{MANIFEST,sigstore.json,tar}
Mac:  fetch (user, downloads, trusts nothing)  ->  sudo deploy <verb> X.Y.Z (root, fixed verbs)
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

## What the Mac verifies (all before anything is installed; the live system is untouched on any failure)

1. Sudoers lets the deploy user run only `deploy <X.Y.Z>`, `deploy-web <X.Y.Z>`, `rollback`, `rollback-web` on one exact path; the entrypoint re-validates
   the arguments itself (the sudoers `*` is only a coarse filter).
2. The entrypoint's own environment is clean (`bash -p`, fixed PATH, no inherited variables), the caller is the allowed user, and the files are **copied**
   into a root-only directory first: everything below runs on the copies (no check/use race against the user-writable staging dir).
3. `cosign` and `trusted_root.json` match the sha256 pinned in the entrypoint (and the installer pins the entrypoint by sha256).
4. `cosign verify-blob --bundle --trusted-root`, with `--certificate-oidc-issuer`, `--certificate-identity` (the workflow at `@main`),
   `--certificate-github-workflow-{repository,ref,sha,trigger}` all pinned. The ref must be `refs/tags/<requested version>`, the trigger `push`, the repository the kind's repository.
5. `MANIFEST` is parsed with a strict allow-list (known keys only, no duplicates, bounded size, safe characters); `kind`, `version` (equals the
   request), `ref`, `branch` (`main` or `release/X.Y.Z`; set by CI after the ancestry check), `git_sha` equal to the certificate's commit, the architecture, and `schema` (not older than the database).
6. **Monotonic version**: strictly greater than the installed version of that kind (separate lines for gateway and UI, stored root-only).
7. sha256 of the tar, then of each member, from the signed manifest; the tar is extracted by a hardened unpacker (no links, devices, absolute or `..` paths, bounded size and count).
8. Swap with one previous version kept, restart, health check; a failed health check restores the previous version automatically. Signals during the swap are handled.

The branch rule is enforced **in CI** (the workflow refuses to sign a tag whose commit is not on `main`/`release/*`) and recorded in the signed manifest; the Mac cannot
check Git ancestry offline and relies on the pinned workflow (at `main`) for it. That is why `deploy-sign.yml` is the trust root and must be protected as described below.

## Threat model

| Threat | Mitigation | Residual risk |
|---|---|---|
| **Repository compromise: attacker can push a branch or tag** | A tag on a commit outside `main`/`release/*` is not signed (ancestry check) and the Mac re-checks the signed `branch`. A modified `deploy-sign.yml` or `release.yml` on that branch or tag cannot produce the pinned subject (`@refs/heads/main`). A fork or another repository has a different `repository` claim. | Attacker who can **merge to `main`** (or to `release/*`) gets code signed, exactly as with any CD. Today **`main` has no branch protection** (checked: the API returns 404); see "Settings to apply". Attacker who can change `deploy-sign.yml` on `main` controls the trust root: same control needed. |
| **CI secret theft** | No signing key and no deploy secret exist anywhere. The only credential is the job's OIDC token, minted inside the run, audience-restricted, short-lived, and only usable to obtain a certificate that names this run. Actions and `cosign` are pinned by commit sha / sha256; the job does not execute code from the tagged commit (it builds and packs; the web kind only tars files). | A step that runs in the signing job could sign what it likes **during that run**; hence the pinned third-party actions, minimal permissions (`contents: write`, `id-token: write` on the one job), and the `deploy` environment as an optional approval gate. The release is public and logged in Rekor, so a rogue signature is detectable after the fact. |
| **Downgrade** | Strictly increasing version per kind, root-only state; the signed `version` must equal the requested one; an equal or lower version is refused even if validly signed. The schema must not be older than the database. `rollback` only restores the single previous version (no signature needed: it re-installs what root had installed, protected by a tree digest). | None for versions at or below the installed one. |
| **Replay of an old signed release** | Signature binds version, commit, ref, repository, kind and every sha256; a signed `X.Y.Z` cannot be relabelled (version/ref/sha mismatch tests). | A genuinely signed release **newer than the installed one** can be re-offered (for example 5.0.3 when 5.0.1 is installed; there is no revocation list). Mitigation: deploy promptly, keep the deploy user from fetching from arbitrary hosts (the fetcher only talks to `github.com/<pinned repo>/releases`), and never delete the release a bad build came from without also shipping a higher fixed one. |
| **Tag moved or re-created** | The certificate records the commit; `git_sha` in the manifest must equal it; the version ratchet makes a re-signed tag of an installed version unusable. | Protect `v*` tags (below) so a tag cannot be moved before its first deploy. |
| **Tampered download or staging dir** | Nothing in staging is trusted: copy, then verify the copies; sha256 of every file from the signed manifest; hostile tars rejected. | None identified. |
| **Compromise of the deploy user (the bot account)** | The sudoers rule gives no shell and no argument freedom: it can deploy a validly signed, strictly newer release, or roll back once. It cannot choose code, bypass the pins, or run anything else as root. | It can cause a deploy of a legitimately signed newer release, or a rollback; that is the point of the path. |
| **Sigstore infrastructure** | Trust root pinned by sha256 and verified offline. | A compromise of the public-good Fulcio CA or Rekor is out of scope. When Sigstore rotates its keys, new releases fail **closed**; fix: update `trusted_root.json` + the pin through a reviewed PR and rerun the one-time install (a new pinned entrypoint). |
| **The one-time install itself** | The README line pins the installer's sha256 and fetches it by commit sha; the installer pins every file it installs (entrypoint, fetcher, uninstaller, trust root, `cosign` by sha256) and drops all `EVE_*` test knobs when root. | The person running it must take the line from a trusted channel and compare the PR's commit; the real `sudo`/sudoers/`/var/db` behaviour is only exercised by the manual runbook, not by CI. |

### Not covered

An approved but malicious change on `main`; theft of a maintainer's GitHub account with push-to-main rights; compromise of the Mac or its
root account; the web swap's brief window between two directory renames (the live UI path is replaced by two renames; a failure restores it, a crash
in the window leaves it recoverable with `rollback-web`).

## Settings to apply (this PR changes none: repository settings are the owner's call)

- A ruleset on `main` and `release/*`: required pull request with review, required status checks (`ci`), no force pushes or deletions, linear history **off** (use merge
  commits: the signed commit must stay reachable from `main`, and a squash re-creates the commit, which is also fine as the tag is cut after the merge).
- A tag ruleset for `v*`: only maintainers can create, no update, no deletion.
- CODEOWNERS (or the ruleset's "restrict file paths") for `.github/workflows/deploy-sign.yml`, `.github/workflows/release.yml`, `deploy/nopasswd/**`.
- Environment `deploy`: deployment branches/tags policy limited to `v*` tags, optionally required reviewers (then every release waits for one approval; leave empty for fully unattended releases).
- Same rulesets in `eve-web` (its workflow only calls the pinned reusable one).
- Enable immutable releases so published assets cannot be replaced.

## Operating notes

- Releases, the web flow, install and removal: `deploy/nopasswd/README.md`. Mac-side checks after install: `docs/runbooks/passwordless-deploy.md`.
- The test fixtures are signed by a real CI run on a throwaway branch (`test-fixtures.yml`) and are inert: a branch is not an accepted ref, so production pins reject them. Regenerate only if the manifest format changes.
