# PR #99 QA (gateway self-description + memory_propose with web off)

QA instance only (127.0.0.1:8002). Uses a local capture/stub upstream so the exact prompt the gateway sends to the model can be asserted without MLX load.

1. `node tests/e2e/pr99/capture-proxy.js &`  (127.0.0.1:8090 -> real MLX 127.0.0.1:8080; request bodies kept in memory only)
2. back up `~/eve-qa/config.yaml`, set `upstream: http://127.0.0.1:8090`, `~/eve-qa/qa-env.sh use <PR build>`
3. `EVE_MODEL=<model id> EVE_BASE_URL=http://127.0.0.1:8002 node tests/e2e/pr99/run.js [--ttft N] [--skip-ttft]`
   (`EVE_TTFT_MAXTOK`, `EVE_TTFT_PROMPT` tune the latency arm)
4. restore the config, stop the proxy, `~/eve-qa/qa-env.sh forward && ~/eve-qa/qa-env.sh verify`

d045b10 re-verify: also run `node tests/e2e/pr99/oldcards.js` right after deploying (approves pending cards created by an older build; expect payload_mismatch for cards whose key order JSONB changed). `run.js` also covers the legacy-hash path (rewrites one QA row), Bearer-without-self-description, UI-style thread turns, the stateless fail-closed rule and the streaming latency arm (`content_deltas`).
