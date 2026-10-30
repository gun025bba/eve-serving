# Automatic tool selection (6.0.0): deploy order and rollback

Design: `docs/architecture/auto-tool-selection.md` (PR #151, §6.7, §10.1). This is the operator's checklist. It contains no keys, hosts or addresses;
paths below are examples.

## 1. Deploy order (do not reorder)

1. **Migration `00009_tool_calls_auto_select.sql` first**, with the old binary still running. It only adds columns (`selected_by`, `error_code`,
   `result_count`, `args_expires_at`) and the `denied` status; 5.x ignores them. There is no DB rollback and none is needed.
2. **HMAC key file** (`tools.log_hmac_key_file` or `GW_TOOLS_LOG_HMAC_KEY_FILE`): a 0600 file owned by the gateway account with 64 or more hex
   characters. Without a usable key the gateway turns automatic selection off and denies web calls with `no_hmac_key` (fail closed). Create the key
   **before** the new binary starts. Never print or commit it.
3. **Old config keys removed**: `web.default_mode: force` must be deleted (6.0.0 refuses to load a config that has it). `web.max_searches` above 2 is
   capped at 2. Set `tools.pim_window_start` to the time 5.1.0 went live (empty means "earliest", which is safe but makes more requests search-free)
   and, after the first 6.0.0 start, `tools.pim_window_end` to that start time.
4. **Kill switch file `tools-killswitch.yaml` is REQUIRED before 6.0.0 starts** (`tools_killswitch_file` / `GW_TOOLS_KILLSWITCH_FILE`, same directory as the other
   gateway files, 0600, owned by the gateway account). Without the setting, or with a file that does not exist at start-up, automatic web tool selection stays
   off (fail closed; the log says `web_auto_select_off reason=no_killswitch_file`) because there would be no 5 s off switch. Create the file first (an empty
   `disabled: []` is valid), check it with `eve-gw killswitch-check <file>` (exit 0 = valid; 3 = valid here but a 5.x build would reject it; 1 = invalid).
   A config that still has `web.default_mode: off` is read as `auto_select_off` (delete the key to turn the tools on); `force` refuses to load.
5. **Start the new binary** with `auto_select_off: true` in the kill switch file if you want it dark first; remove that key to enable.
6. **Enable gate (first week, every day):** automatic search rate, `echo_rejected`, `injection_suspect` and `approve_refused` audit events, budget use,
   `llm_pim_window_assumed_total`. **If more than 10% of normal queries are rejected by the echo filter, do not enable (or set `auto_select_off`) and tell the PM.**
   The measured rate on the committed corpus (`internal/tools/testdata/echo_normal_queries_results.txt`, over 300 queries, with the Wilson 95% bound) is
   the baseline; compare production against it.
   The shared API-key budget has its own audit event `budget_exhausted` (once per day, kind and code); look for it when API clients report missing search.
   The echo rule ids are in the audit events only; the model and the client always see one fixed message.

## 2. The kill switch (no restart, no signal)

`tools-killswitch.yaml` is polled every 2 s (mtime/inode) and applies within 5 s, in the middle of a request too (the next tool round gets
`error_code: kill_switch`). Keys:

```yaml
auto_select_off: true            # automatic selection off for everyone (6.0.0 and later only)
disabled: [web_search, web_fetch]  # these tools are removed (also understood by 5.x)
all_off: true                    # every tool, including memory_propose
```

Always replace the file **atomically**: write a temp file in the same directory, `chmod 600`, `mv` over the target. A file that cannot be parsed
turns every tool off (fail closed).

## 3. Rollback to 5.x, in this order

A 5.x gateway rejects unknown keys (`auto_select_off`) and would turn **every** tool off. So:

1. Write `disabled: [web_search, web_fetch]` **and** keep `auto_select_off: true`. The new build applies `disabled` within 5 s; web tools are off.
2. Write the file again with **only** `disabled: [web_search, web_fetch]` (remove `auto_select_off`). Run `eve-gw killswitch-check <file>`: it must say
   "5.x build: valid" and "web tools off: true".
3. `SIGHUP` or restart (only needed for the config; the kill switch needs neither).
4. Roll back the binary to 5.x. The migration stays (additive).
5. Verify the web tools stay off (a test request must show no tool calls).
6. Clear `disabled` only as a separate decision.
7. **On every redeploy of 6.0.0 after a rollback (M-j), set `tools.pim_window_end` (T_600) to the time of that redeploy**, not to the first 6.0.0 start.
   While 5.x ran, its assistant messages carry no `metadata.tools`; an old T_600 would leave them outside the window and the next 6.0.0 turn would
   treat a possible personal-data read as a normal answer. Check the value before removing `disabled`.

Config keys introduced in 6.0.0 (`tools.*`, `web.args_retention_days`) must be removed before a 5.x binary starts if it refuses unknown keys; start
the 5.x binary against a copy of the config first.

## 4. Data

Stored tool arguments are cleared after `web.args_retention_days` (90; a smaller value is refused at start-up) by the daily retention job; the call row stays
until `web.retention_days` (90) purges it. This is the same for browser-session and API-key requests. Rejected queries never store arguments. The HMAC key is not part of any backup
bundle that leaves the machine.
