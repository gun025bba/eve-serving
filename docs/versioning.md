# Versioning

Eve uses Semantic Versioning (`MAJOR.MINOR.PATCH`). 1.0.0 is the production state as of 2026-10-26
(gateway `c2d83e1`, UI v17 `199f7bd`). Releases are annotated git tags `vX.Y.Z` in each repo.

## Rules

| Bump  | When | Approval |
|-------|------|----------|
| Major | Breaking API change; irreversible DB migration (drops data or cannot be rolled back); base model swap; change to exposure or security boundary (who/what can reach the service) | **건영님's direct approval required before deploy** |
| Minor | Backward-compatible feature | No approval; report after deploy |
| Patch | Bug fix | No approval; report after deploy |

**When unsure, treat it as major** and get approval.

Every deploy, approved or not, is reported with: version, changes, rollback steps. CI must pass,
tests must run, no force-push, and commits carry the `Co-authored-by: Grok <grok-bot@noreply.invalid>` trailer.

## Where the version shows

- **Gateway:** `make build` injects `git describe --tags` (leading `v` stripped) via `-ldflags -X main.version`.
  It appears in the startup log (`gateway_start version=...`), `GET /health` and `GET /v1/me` (`"version"`),
  and the `X-Eve-Version` response header on every response. Untagged builds show e.g. `1.1.0-3-gabcdef`; `go run` shows `dev`.
- **UI:** `sw.js` cache name, the version shown in settings, and package/manifest (owned by the frontend).

## Gateway release and rollback

```
git tag -a vX.Y.Z -m "..." && git push origin vX.Y.Z   # after CI passes on main
# server: ~/llm-serving
git pull --ff-only && make build
launchctl bootout gui/$(id -u)/com.llmserving.gateway
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.llmserving.gateway.plist
curl -s http://127.0.0.1:8001/health      # check "version"
```

Rollback: `git checkout vPREV && make build`, then the same bootout/bootstrap.
