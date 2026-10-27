# 4.0.0 cutover test plan (PR #23, unix-socket identity)

Reviewed head: `98be55f` (feat/unix-socket-identity), base `8bf80e9` (= v3.1.1). Runbook: `docs/runbooks/4.0.0-unix-socket.md`.
Scope of change: auth boundary only — new unix-socket listener (`GW_UNIX_SOCKET`), Tailscale identity trusted only on it,
TCP = API key only, optional `GW_TCP_DISABLED`, e2e-only identity injection (`-tags e2e`), LaunchDaemon as `_evegw`,
`tailscale serve` → `unix:`. **No DB migration in the PR. No API/contract change for API-key clients. CSRF/Origin logic unchanged.**

## Open blockers (QA, 2026-10-27)
1. **Base is 84 commits behind main** (memory P1, `internal/apierr`, migration `00007_user_settings`, …). Merge with main
   conflicts in `cmd/gateway/main.go`, `internal/config/config.go`, `internal/handlers/server.go`. The reviewed/tested
   binary ≠ whatever `v4.0.0` will be tagged from after merge. Decide which commit is tagged, rebase, re-run tests on it.
2. If 4.0.0 is tagged from main after merge: runbook says "no DB migration" but `GW_DB_AUTO_MIGRATE=false` and main
   carries `00007` — apply/verify goose version before start, and rollback must handle a schema at 7.
3. Not E2E-verified on 8002 (8002 runs 3.1.1-79-g64a4c4c, a main build).
4. Rollback text expects `"version":"3.1.0"` and `gateway.v3.1.0.bak`; current tag is v3.1.1 — fix names / expected version
   to the actually running prod version (read it, don't assume).

## A. Pre-checks (before downtime)
- [ ] `git rev-parse HEAD` of the build tree == reviewed SHA; `gateway-4.0.0 -version` prints the expected version.
- [ ] `go test ./...` green **in a worktree outside /tmp** (listen/socket tests refuse world-writable ancestors; /private/tmp fails by design).
- [ ] CI step: default binary has no e2e symbols, no `X-Eve-Test-Identity` string, refuses start with `GW_TEST_IDENTITY_INJECTION=true`.
- [ ] `_evegw` uid/gid 451 free; `dscl . -read /Groups/_evegw GroupMembership` empty after creation.
- [ ] `ls -ld / /opt /opt/eve-gw /opt/eve-gw/run`: no group/world-writable; run 0700 `_evegw`.
- [ ] Backups: `pg_dump -Fc` (umask 077) exists, root:wheel 0600; **test-restore it into a scratch DB** (`pg_restore -l` at minimum).
- [ ] Old LaunchAgent plist copied to `/var/root/eve-rollback` (0600 root); old binary `.bak` exists and `-version` works.
- [ ] `tailscale serve status --json` saved; `EXPECT=http://127.0.0.1:8000 scripts/check-serve.sh` = OK; funnel off.
- [ ] Goose version of prod DB recorded and matches what the 4.0.0 binary expects (see blocker 2).
- [ ] Source `web/` has no symlinks (`find web -type l` empty). Note: commands doc step 5 uses `cp -R`; runbook uses `cp -RP` — use `-RP`.
- [ ] New plist: `plutil -lint`; contains no `GW_DATABASE_URL`, no credentials in `GW_SEARXNG_URL` (file is 0644).

## B. Smoke after cutover (§4–§5)
- [ ] `sudo ls -l /opt/eve-gw/run/gw.sock` → `srw-rw---- _evegw _evegw`.
- [ ] Log line `gateway_start` with `unix_socket:true tcp:true`, correct version; no `tailscale_identity_inactive` warning.
- [ ] `scripts/check-serve.sh` (EXPECT=unix:…) = OK; `tailscale funnel status` = tailnet only.
- [ ] `curl -H 'Tailscale-User-Login: <allowlisted>' 127.0.0.1:8000/whoami` → **401**.
- [ ] Same with API key → `"user":"api-key"`; `/health` 200 over TCP.
- [ ] Playwright `EVE_EXPECT_AUTH_BOUNDARY=1 npx playwright test specs/api-auth-boundary.spec.js` (against :8000 with prod key only if 건영 approves; otherwise on 8002 once 8002 runs 4.0.0).
- [ ] UI (iPhone / tailnet browser) `https://<TS_HOST>/ui`: logs in as allowlisted user, send chat, persists, reload shows thread.
- [ ] UI CSRF: same-origin POST 200; cross-origin 403 (unchanged contract; UI 1.2.0 sends X-Eve-Request + Origin).
- [ ] UI background resume (stream detach + resume after reload) still works over the socket (ui-background-resume spec flow, manual on prod).
- [ ] Gateway log: UI requests `"conn":"unix"`, curl `"conn":"tcp"`; test identities only as `t-<12hex>`.
- [ ] Non-allowlisted tailnet user → 403/401 over socket.

## C. Rollback verification (§8, before §9)
- [ ] Rehearse on paper: every file referenced exists (`/var/root/eve-rollback/…plist`, `bin/gateway.*.bak`).
- [ ] After rollback: `curl 127.0.0.1:8000/health` = **previous prod version** (not hard-coded 3.1.0).
- [ ] `tailscale serve status` → `http://127.0.0.1:8000`; `EXPECT=http://127.0.0.1:8000 scripts/check-serve.sh` OK.
- [ ] UI login + chat works; DB goose version unchanged; no daemon left loaded (`sudo launchctl print system/com.llmserving.gateway` → not found).
- [ ] If pf was enabled: restored `pf.conf.pre-eve`.
- [ ] After §9 rotation, rollback plist/key must be updated first — mark rollback window closed once §9 step 11 runs.

## D. Phase 2 (GW_TCP_DISABLED) — separate change, not tonight
- [ ] 7 days of no TCP traffic incl. denied-auth metric and `lsof` sampling; health via `sudo curl --unix-socket`.
