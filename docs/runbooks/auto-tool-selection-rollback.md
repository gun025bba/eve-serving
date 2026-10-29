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
4. **Kill switch file** `tools-killswitch.yaml` (same directory as the other gateway files, 0600, owned by the gateway account) may exist from the start. Check any
   candidate first: `eve-gw killswitch-check <file>` (exit 0 = valid; 3 = valid here but a 5.x build would reject it; 1 = invalid).
5. **Start the new binary** with `auto_select_off: true` in the kill switch file if you want it dark first; remove that key to enable.
6. **Enable gate (first week, every day):** automatic search rate, `echo_rejected`, `injection_suspect` and `approve_refused` audit events, budget use,
   `llm_pim_window_assumed_total`. **If more than 10% of normal queries are rejected by the echo filter, do not enable (or set `auto_select_off`) and tell the PM.**
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

Config keys introduced in 6.0.0 (`tools.*`, `web.args_retention_days`) must be removed before a 5.x binary starts if it refuses unknown keys; start
the 5.x binary against a copy of the config first.

## 4. Data

Stored tool arguments are cleared after `web.args_retention_days` (90). Rejected queries never store arguments. The HMAC key is not part of any backup
bundle that leaves the machine.
