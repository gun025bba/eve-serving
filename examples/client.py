"""Test client. Usage: .venv/bin/python examples/client.py [--base http://<host>:8000]"""
import argparse, json, os, time, httpx
from openai import OpenAI
ap = argparse.ArgumentParser(); ap.add_argument("--base", default="http://127.0.0.1:8000"); a = ap.parse_args()
KEY = os.getenv("LLM_API_KEY") or open(os.path.expanduser("~/llm-serving/.api_key")).read().strip()
c = OpenAI(base_url=a.base + "/v1", api_key=KEY, max_retries=2, timeout=300)
print("== OpenAI SDK (non-stream)")
r = c.chat.completions.create(model="mlx-community/Qwen3-30B-A3B-4bit", messages=[{"role": "user", "content": "한 문장 인사 /no_think"}], max_tokens=60)
print(r.choices[0].message.content.strip(), "|", r.usage)
print("== OpenAI SDK (stream)"); t0 = time.time(); first = None
for ch in c.chat.completions.create(model="mlx-community/Qwen3-30B-A3B-4bit", messages=[{"role": "user", "content": "1부터 10까지 세줘 /no_think"}], max_tokens=80, stream=True):
    if ch.choices and ch.choices[0].delta.content:
        first = first or time.time(); print(ch.choices[0].delta.content, end="", flush=True)
print(f"\n(TTFT {first - t0:.2f}s)" if first else "")
print("== raw httpx")
with httpx.Client(base_url=a.base, headers={"Authorization": f"Bearer {KEY}"}, timeout=300) as h:
    r = h.post("/v1/chat/completions", json={"messages": [{"role": "user", "content": "ping /no_think"}], "max_tokens": 20})
    print(r.status_code, r.headers.get("x-request-id"), r.json().get("usage"))
    print("ready:", h.get("/ready").json()["checks"]); print("stats:", json.dumps(h.get("/stats").json(), ensure_ascii=False)[:300])
