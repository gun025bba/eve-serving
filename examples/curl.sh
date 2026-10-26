#!/bin/zsh
# Usage: examples/curl.sh [base_url]   (default http://127.0.0.1:8000)
B=${1:-http://127.0.0.1:8000}; K=${LLM_API_KEY:-$(cat ~/llm-serving/.api_key)}
echo "# health/ready"; curl -s $B/health; echo; curl -s $B/ready; echo
echo "# chat"; curl -s $B/v1/chat/completions -H "Authorization: Bearer $K" -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"안녕 /no_think"}],"max_tokens":60}'; echo
echo "# stream"; curl -sN $B/v1/chat/completions -H "Authorization: Bearer $K" -H 'Content-Type: application/json' \
  -d '{"messages":[{"role":"user","content":"1부터 5까지 /no_think"}],"max_tokens":40,"stream":true}'
echo "# stats"; curl -s $B/stats; echo
echo "# metrics (llm_*)"; curl -s $B/metrics | grep '^llm_' | head -30
