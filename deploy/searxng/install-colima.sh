#!/usr/bin/env bash
# Runbook: run SearXNG for the gateway under colima on the host (macOS).
# NOT run automatically. Read each step; run as the gateway's user.
#   ./install-colima.sh check     # preflight only
#   ./install-colima.sh install   # colima + compose up (asks before each step)
#   ./install-colima.sh verify    # §3.5 checks
#   ./install-colima.sh rollback  # stop/remove SearXNG; gateway keeps working with web off
set -euo pipefail
cd "$(dirname "$0")"
PORT=8888
confirm() { read -r -p "$1 [y/N] " a; [[ "$a" == y* ]]; }

check() {
  command -v brew >/dev/null || { echo "need Homebrew"; exit 1; }
  if lsof -nP -iTCP:$PORT -sTCP:LISTEN >/dev/null 2>&1; then echo "port $PORT already in use"; exit 1; fi
  [[ -f .env ]] || { echo "create .env from .env.example (SEARXNG_SECRET)"; exit 1; }
  grep -q '^SEARXNG_SECRET=[0-9a-f]\{64\}$' .env || { echo "SEARXNG_SECRET must be 64 hex chars"; exit 1; }
  grep -q '^VALKEY_PASSWORD=[0-9a-f]\{64\}$' .env || { echo "VALKEY_PASSWORD must be 64 hex chars"; exit 1; }
  chmod 600 .env
  git check-ignore -q .env || { echo ".env is not git-ignored"; exit 1; }
  echo "preflight ok"
}

install() {
  check
  confirm "brew install colima docker docker-compose?" && brew install colima docker docker-compose
  mkdir -p ~/.colima/_lima/_config && cp lima-override.yaml ~/.colima/_lima/_config/override.yaml
  confirm "start colima (2 cpu, 2 GiB, no k8s)?" && colima start --cpu 2 --memory 2 --disk 10
  confirm "docker compose up -d?" && docker compose --env-file .env up -d
  verify
  cat <<'NEXT'
Next (manual):
  1. In the gateway config set web.enabled: true (or GW_WEB_ENABLED=true), keep default_mode: off.
  2. Restart the gateway the usual way; check /v1/me -> capabilities.web_search=true.
  3. Try one request with "web_search":"auto"; watch llm_tool_calls_total and logs.
Autostart: brew services start colima (DOCKER_CONFIG=~/.docker-colima avoids Docker Desktop creds; compose services use restart: unless-stopped).
NEXT
}

verify() {
  echo "- listening sockets for :$PORT (must be 127.0.0.1 only):"
  lsof -nP -iTCP:$PORT -sTCP:LISTEN || true
  if lsof -nP -iTCP:$PORT -sTCP:LISTEN | grep -v '127.0.0.1' | grep -q LISTEN; then echo "FAIL: $PORT bound beyond loopback"; exit 1; fi
  if lsof -nP -iTCP -sTCP:LISTEN | grep -E '^(limactl|ssh) ' | grep -v '127.0.0.1:' | grep -q LISTEN; then echo "FAIL: colima forwards a port beyond loopback (see lima-override.yaml)"; exit 1; fi
  if lsof -nP -iTCP:6379 -sTCP:LISTEN >/dev/null 2>&1; then echo "FAIL: 6379 published on host"; exit 1; fi
  echo "- JSON search:"
  curl -fsS "http://127.0.0.1:$PORT/search?q=test&format=json" | head -c 200; echo
  echo "- container hardening:"
  for c in $(docker compose ps -q); do
    docker inspect -f '{{.Name}} ro={{.HostConfig.ReadonlyRootfs}} capdrop={{.HostConfig.CapDrop}} sec={{.HostConfig.SecurityOpt}}' "$c"
  done
  echo "- query not in container logs (item 7):"
  q="logcheck$RANDOM$RANDOM"; curl -fsS -o /dev/null "http://127.0.0.1:$PORT/search?q=$q&format=json" || true
  if docker compose logs 2>&1 | grep -q "$q"; then echo "FAIL: query appears in container logs"; exit 1; else echo "ok"; fi
  echo "Also confirm from another tailnet device that http://<host-tailnet-name>:$PORT is unreachable."
}

rollback() {
  # 1. turn the feature off in the gateway first (web.enabled: false / GW_WEB_ENABLED=false) and restart it;
  #    chat keeps working without web. 2. then stop SearXNG:
  docker compose down --remove-orphans || true
  confirm "also stop colima?" && colima stop || true
  echo "rolled back. Data: none persisted by SearXNG (valkey is tmpfs)."
  echo "DB: tool_calls/web_sources stay (purged after retention_days); to drop entirely: goose down to 00001."
}

case "${1:-}" in check|install|verify|rollback) "$1";; *) sed -n 2,9p "$0"; exit 2;; esac
