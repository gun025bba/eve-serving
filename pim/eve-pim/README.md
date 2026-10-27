# eve-pim — Jarvis M1 EventKit helper (#68)

Small Swift daemon that reads (and, only with `--writes-enabled`, writes)
Calendar/Reminders through EventKit for the gateway. Design:
`docs/architecture/jarvis-m1.md` §2 (PR #66). **Not deployed; default off.**

## Security properties (issue #68 acceptance criteria)

| Criterion | Where |
|---|---|
| socket dir 0750, owner `<login user>:_evepim`, socket 0660 | `PathGuard.checkDir`, `umask`+`chmod`+`PathGuard.checkSocket` in `Server.swift` |
| existing file/symlink at the socket path → refuse to start, never delete | `PathGuard.refuseExisting` (lstat) |
| `getpeereid` uid must equal `_evegw` exactly | `PeerPolicy.allowed`, `handleConn` |
| fixed schema, 8 KiB cap, unknown fields/methods rejected | `parseRequest`, `testdata/protocol/cases.json` |
| no network | no network code (CI greps for it); `eve-pim.sb` denies IP in/out/bind |
| logs carry method, count, error code only | `LogRecord` |
| writes switchable by a server flag | `--writes-enabled` (absent = `writes_disabled`) |

Data returned: events `{id,title,start,end,all_day,calendar}`, reminders
`{id,title,due,list,done}`; events ≤ 31-day range and ≤ 50 items, reminders
≤ 100 items, `truncated:true` when cut. No notes, attendees, location, URL.

## Protocol

One request per connection, newline-terminated JSON:

```
{"v":1,"id":"<≤64>","method":"list_events","params":{"from":"<RFC3339>","to":"<RFC3339>"}}
{"v":1,"id":"…","method":"list_reminders","params":{"due_before":"<RFC3339>?","include_done":false}}
{"v":1,"id":"…","method":"add_reminder","params":{"title":"…","due":"<RFC3339>?","list":"…?"}}
{"v":1,"id":"…","method":"complete_reminder","params":{"id":"…"}}
→ {"v":1,"id":"…","ok":true,"result":{…}} | {"v":1,"id":"…","ok":false,"error":"<code>"}
```

Error codes: `invalid_request`, `unknown_method`, `too_large`,
`range_too_long`, `permission_denied`, `writes_disabled`, `not_found`, `internal`.

## Build / test

macOS only (EventKit). `swift build -c release && swift test` in this
directory; CI runs it on a GitHub-hosted macOS runner (job `pim-helper`).

## Install (owner's hands; not done by any bot)

1. `sudo dscl . -create /Groups/_evepim` (+ `PrimaryGroupID`, a free id) and
   `sudo dseditgroup -o edit -a _evegw -t user _evepim` — only `_evegw` is a member.
2. Socket dir: `sudo install -d -m 0750 -o "$USER" -g _evepim <socket-dir>`.
3. Copy the binary and `eve-pim.sb` to `~/eve-pim/`, render
   `deploy/launchd/com.eve.pim.plist.template` (`__HOME__`, `__SOCKET_DIR__`)
   into `~/Library/LaunchAgents/`, `launchctl bootstrap gui/$(id -u) …`.
4. Approve the Calendar and Reminders TCC prompts once.

## Gateway write verbs (#70) — both switches off by default

The gateway can offer two write verbs, `reminders_add` and `reminders_complete`
(scope `reminders.write`). They need **two independent switches**, both off by default:

| Switch | Where | Effect |
|---|---|---|
| `GW_PIM_WRITES_ENABLED=true` (needs `GW_PIM_ENABLED=true`) | gateway env / `pim.writes_enabled` | the owner's model may *propose* a write |
| `--writes-enabled` | helper LaunchAgent arguments | the helper executes `add_reminder` / `complete_reminder` |

Reminders sync to the Mac's linked accounts (iCloud etc.), so enabling either switch is an
owner decision (explicit approval); `install-pim.sh` never sets them.

Flow: the model call only creates a `pending_actions` card whose `payload_view` shows the exact
title / due / list (for complete: the reminder's current title). Nothing is written until the owner
approves it in a browser session (`POST /v1/actions/{id}/approve`, `interactive` scope; an API key
gets 403 `scope_denied`). Approve re-checks the gates, re-validates the payload, writes an audit row
(fail-closed), claims the card (one winner) and makes exactly one helper call. A definite refusal
(`writes_disabled`, `not_found`, `permission_denied`, connect failure) releases the card; an unknown
outcome (timeout, dropped connection) leaves it consumed and answers `502` with `outcome_unknown:true`,
because EventKit has no idempotency key and a retry could create a duplicate. Logs and audit rows
carry only the method (`add_reminder` / `complete_reminder`), a count and an error code.
Authorization uses the principal's scope and `users.id` only, never the way the request authenticated.
