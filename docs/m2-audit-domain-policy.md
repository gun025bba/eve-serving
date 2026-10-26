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

**Rollback:** redeploy the previous binary. `tool_audit` is additive and is
never dropped (goose Down is a no-op; `TestMigrations_DownNeverDropsAudit`).

## Domain policy (§13)

`deploy/policy/web-domains.yaml` + `categories/*.txt`, embedded in the binary.
`malware.txt` is an ed25519-signed snapshot (`malware.txt.sig`, public key in
`keys/`). Refresh:

    go run ./cmd/eve-policy import -source urlhaus -in hosts.txt -out deploy/policy/categories/malware.txt
    cp deploy/policy/categories/malware.txt{,.sig} → malware.prev.txt{,.sig}   # keep last good
    go run ./cmd/eve-policy sign -key <private key, outside the repo> deploy/policy/categories/malware.txt
    go run ./cmd/eve-policy verify      # CI runs this; fails if older than 30 days

If the policy fails to load the gateway still starts but both web tools
fail closed (`policy_unavailable`).
