# M2 — tool_audit ledger and web domain policy (runbook)

Implements docs/tool-platform.md §5/§8/§9 (M2) and §13.

## Database (migration 00004_tool_audit)

Run migrations with a dedicated **migration login** (database owner,
`CREATEROLE`), not with the gateway login:

    GW_DB_AUTO_MIGRATE=false            # gateway config
    GW_DATABASE_URL=<migration login> gateway -migrate

The migration creates NOLOGIN roles `eve_audit_owner` (owns `tool_audit`),
`eve_app` (INSERT, SELECT) and `eve_retention` (SELECT, DELETE for the
future purge job). Then, once, as the migration login:

    GRANT eve_app TO <gateway login>;

The gateway refuses to start when its login is a member of `eve_audit_owner`
(superusers are), or holds UPDATE/DELETE/TRUNCATE on `tool_audit`, or lacks
INSERT/SELECT.

Append-only is enforced three ways: privileges, a BEFORE UPDATE/DELETE/
TRUNCATE trigger (applies to the owner too; only `eve_retention` may DELETE),
and the hash chain (`pg_advisory_xact_lock` + UNIQUE `prev_hash`). The chain
is re-verified at startup and daily (`audit_verify_ok` / `audit_verify_failed`
log, `eve_audit_verify_failures_total`).

### Migration login (security review of PR #13, Med 1)

- The migration grants itself `eve_audit_owner` only to create `tool_audit`
  and ends with `REVOKE eve_audit_owner FROM CURRENT_USER`, so afterwards it
  can no longer `SET ROLE` to the owner, disable the triggers or DROP the table.
- **Residual risk (PostgreSQL 16+):** the CREATEROLE login that created the
  roles keeps `ADMIN OPTION` on `eve_audit_owner` / `eve_app` /
  `eve_retention` even after the REVOKE (verified on PG17: admin=t, set=f,
  inherit=f). It can grant itself the role again, then disable the triggers
  or `SET ROLE eve_retention` and delete rows. The hash chain and the daily
  verify detect such edits, but do not prevent them. Treat the migration
  login like the audit owner.
- Keep the migration login **NOLOGIN** except while a migration runs
  (`ALTER ROLE <migration login> LOGIN;` → migrate → `ALTER ROLE
  <migration login> NOLOGIN;`). Its credentials never go into the gateway's
  environment, launchd plist or config; the gateway only ever gets the
  `eve_app` login (`GW_DB_AUTO_MIGRATE=false`).
- The gateway startup privilege check is unchanged and sufficient.

### Fail-open mitigations (M2) and alerts (Med 2)

Auditing is fail-open in M2: a failed audit write does not block a web tool
call. To bound that:

- **Circuit breaker:** 3 or more audit write or chain-verify failures within
  5 minutes disable `web_search` and `web_fetch`, re-checked before every
  tool call so a running tool loop stops too (`tools_disabled`) (requests asking for web
  search get `400 web_search_disabled`, `/v1/me` reports
  `web_search: false`). It stays open until the gateway is restarted after
  the audit store is fixed. Metrics: `eve_audit_breaker_open` (1 = tripped),
  `eve_web_tools_audit_disabled{reason="breaker"}`; log `audit_breaker_open`.
- **In-memory ledger:** without a database the ledger is in memory only.
  Unless `GW_ENV=dev`, the web tools are then disabled
  (`eve_web_tools_audit_disabled{reason="memory_ledger"}`, log
  `web_tools_disabled`). An unknown `GW_ENV` value is treated as prod and
  logged (`unknown_gw_env`). Production (`GW_ENV` unset or `prod`) must run with
  Postgres.
- **Alert rules** (Prometheus):

      - alert: EveAuditWriteFailures
        expr: increase(eve_audit_write_failures_total[5m]) > 0
        labels: {severity: page}
      - alert: EveAuditVerifyFailures
        expr: increase(eve_audit_verify_failures_total[1d]) > 0
        labels: {severity: page}
      - alert: EveAuditBreakerOpen
        expr: eve_audit_breaker_open == 1 or eve_web_tools_audit_disabled == 1
        labels: {severity: page}

- **Hard gate:** fail-closed auditing (a tool call does not run, or its
  result is discarded, when its audit row cannot be written) **must be in
  place before any write or send tool ships** (M3 and later, incl. proactive
  messaging). A write/send tool PR without fail-closed audit is rejected in
  security review. Fail-open is only acceptable for the read-only web tools.

### Retention DELETE (TODO for the purge PR)

The trigger's DELETE exception only checks `current_user = 'eve_retention'`:
a SECURITY DEFINER function owned by it passes, and any row can be deleted.
**TODO:** the purge PR must restrict the exception to rows older than the
retention period and covered by a written checkpoint.

**Rollback:** redeploy the previous binary. `tool_audit` is additive and is
never dropped (goose Down is a no-op; `TestMigrations_DownNeverDropsAudit`).

## Domain policy (§13)

`deploy/policy/web-domains.yaml` + `categories/*.txt`, embedded in the binary.
`malware.txt` is an ed25519-signed snapshot (`malware.txt.sig`, format
`KEYID:base64`, public keys in `keys/malware-ed25519*.pub`). `malware.prev.txt`
(+ `.sig`) is the previous verified snapshot; if the current one fails
verification the gateway falls back to it with a warning. CI requires both,
and requires `# source:` and `# upstream-commit:` headers. Refresh:

    git mv deploy/policy/categories/malware.txt deploy/policy/categories/malware.prev.txt
    git mv deploy/policy/categories/malware.txt.sig deploy/policy/categories/malware.prev.txt.sig
    go run ./cmd/eve-policy import -source "<feed name>" -upstream-commit <upstream sha> -in hosts.txt -out deploy/policy/categories/malware.txt
    go run ./cmd/eve-policy sign -key <private key, outside the repo> -key-id <id> deploy/policy/categories/malware.txt
    go run ./cmd/eve-policy verify      # CI runs this; fails if older than 30 days

The fallback snapshot may be at most 60 days old (`MaxPrevSnapshotAge`): an
older one is not used (both web tools fail closed) and fails CI. While the
fallback is in use `eve_policy_snapshot_fallback` is 1 (log
`web_policy_loaded snapshot_fallback=true`). Signatures without a key id are
rejected. Alert:

    - alert: EvePolicySnapshotFallback
      expr: eve_policy_snapshot_fallback == 1
      labels: {severity: warn}

The feed (malware-filter's urlhaus-filter, CC0/MIT) has a single maintainer
and leaves out the top 1M sites; it is a supplement, not a guarantee.

### Signing key

- **Location:** the private key lives only in the host login
  keychain, generic password service `eve-malware-ed25519` (account `eve`),
  stored base64-encoded. It is no longer on the shared box. The gateway never
  needs it; it only verifies with the committed `keys/*.pub`. `*.key` is in
  `.gitignore`.
- **Signing / renewal (on the host):**

      umask 077; k=$(mktemp)
      security find-generic-password -s eve-malware-ed25519 -w | base64 -d > "$k"
      eve-policy sign -key "$k" -key-id k2026a deploy/policy/categories/malware.txt
      rm -P "$k"
      eve-policy verify -dir deploy/policy

- **Rotation (key id + overlap):** generate a new key (`eve-policy keygen`),
  commit its public key as `keys/malware-ed25519-<newid>.pub` with a
  `# key-id: <newid>` line, add `# not-after: <RFC3339>` to the old key file,
  re-sign both snapshots with the new key. During the overlap both keys
  verify; after `not-after` the old key is rejected, and its file is then
  deleted in a follow-up PR.

If the policy fails to load the gateway still starts but both web tools
fail closed (`policy_unavailable`).
