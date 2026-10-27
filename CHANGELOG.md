# Changelog

## 4.0.0 (unreleased)

> **Breaking: every existing browser must re-pair.** After upgrading, browsers that used 3.x get `401` until they
> pair once: run `gateway pair --user <login>` (or `eve-gw pair --user <login>`) on the host, then enter the printed
> `XXXX-XXXX` code in the UI within 10 minutes. Scripts keep working with `Authorization: Bearer <api key>`.

Design: `docs/architecture/identity-transport.md`. Cutover: `docs/runbooks/4.0.0-loopback-session.md`.

### Security
- **No request header is identity any more.** `Tailscale-User-*` headers are stripped on every request and only
  logged as `ts_login_hint`. The `tailscale-identity` provider and the unix-socket transport are removed.
- Auth is a browser session cookie (`eve_session`; HttpOnly, Secure, SameSite=Strict, Path=/) or the API key
  (Bearer). Only `GET /health` and `POST /v1/pair` are unauthenticated.
- Pairing codes: CSPRNG, Crockford base32, 8 chars, valid 10 minutes, single use. 5 failed attempts lock pairing
  for 10 minutes and invalidate all outstanding codes.
- Sessions: 256-bit token, 30 days idle / 180 days absolute. Codes and tokens are stored only as
  HMAC-SHA256(pepper, ...). `DELETE /v1/session` logs out; `gateway sessions list|revoke <id>` manages them.
- The gateway binds only to `127.0.0.1` or `::1` and refuses to start on any other address (including `localhost`).
- Host header allowlist: `localhost`, `127.0.0.1`, `::1` plus `allowed_hosts` / `GW_ALLOWED_HOSTS`. Others get `421`,
  a missing Host gets `400`.
- The API key file and the session pepper file must be mode 0600 (no group/other bits), or startup is refused.
- API-key-authenticated requests get `403 scope_denied` on browser-only routes (`GET /v1/actions`, action reissue/revise).

### Changed
- `GET /health` returns exactly `{"status":"ok"}` (no version or other fields).
- Now require auth: `/ready`, `/metrics`, `/stats`, `/openapi.json`, `/docs`. `X-Eve-Version` is sent only on
  authenticated responses. `/ui/` static files stay public so the pairing screen can load.
- Default `auth.providers`: `[session, api-key]`.

### Added
- `pair`, `sessions list`, `sessions revoke` subcommands (in `gateway` and the new `bin/eve-gw`).
- `POST /v1/pair`, `DELETE /v1/session`.
- Config: `allowed_hosts` (`GW_ALLOWED_HOSTS`), `session_pepper_file` (`GW_SESSION_PEPPER_FILE`, default
  `<base>/.session_pepper`).
- Migration `00008_sessions` (reversible; renumbered from 00007 after main added `00007_user_settings`): tables `sessions` and `pairing_codes`. No existing table is changed.

### Removed (startup refuses these if set)
- `unix_socket` / `GW_UNIX_SOCKET`, `tcp_disabled` / `GW_TCP_DISABLED`, provider `tailscale-identity`,
  `BIND_IP` in `deploy/install-launchd-go.sh`.

### UI (eve-web, the renamed UI repo)
- Needs a pairing screen that posts the code to `POST /v1/pair` and handles `401 invalid_session` by showing it.
