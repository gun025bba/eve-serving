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

Data returned: events `{id,title,start,end,all_day,calendar,calendar_id}`, reminders
`{id,title,due,list,done}`; events ≤ 31-day range and ≤ 50 items, reminders
≤ 100 items, `truncated:true` when cut. No notes, attendees, location, URL.

## Protocol

One request per connection, newline-terminated JSON:

```
{"v":1,"id":"<≤64>","method":"list_events","params":{"from":"<RFC3339>","to":"<RFC3339>"}}
{"v":1,"id":"…","method":"list_reminders","params":{"due_before":"<RFC3339>?","include_done":false}}
{"v":1,"id":"…","method":"add_reminder","params":{"title":"…","due":"<RFC3339>?","list":"…?"}}
{"v":1,"id":"…","method":"complete_reminder","params":{"id":"…"}}
{"v":1,"id":"…","method":"list_calendars","params":{}}
{"v":1,"id":"…","method":"add_event","params":{"title":"…","start":"<RFC3339>","end":"<RFC3339>","all_day":false,"location":"…?","notes":"…?","calendar_id":"…?"}}
→ {"v":1,"id":"…","ok":true,"result":{…}} | {"v":1,"id":"…","ok":false,"error":"<code>"}
```

Six methods in total (design §2 / §11.4). `list_calendars` is a read: it returns
`{"calendars":[{id,name,writable}]}` and nothing about events. `add_event` is a write
(`--writes-enabled`, else `writes_disabled`): title 1–200, `start` < `end` both required
(the gateway fills the default end), timed events ≤ 7 days, all-day ≤ 31 days
(`range_too_long`), location ≤ 200, notes ≤ 500, control characters rejected, and
`calendar_id` absent = the default calendar for new events; an unknown or read-only
calendar is `not_found`. For all-day events `end` is the exclusive start of the next day
(stored in EventKit as the last second of the last day). It returns the created event in
the `list_events` shape; location and notes are never returned.

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
