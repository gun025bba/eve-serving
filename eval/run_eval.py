#!/usr/bin/env python3
"""Run fixed Korean eval / regression against an OpenAI-compatible chat endpoint.

Environment:
  EVAL_BASE_URL   Required. Base URL without trailing slash, e.g. http://127.0.0.1:8000
                  Requests go to {EVAL_BASE_URL}/v1/chat/completions
  EVAL_API_KEY    Optional. Bearer token. Falls back to LLM_API_KEY, then ~/llm-serving/.api_key
  EVAL_MODEL      Optional. Default mlx-community/Qwen3-30B-A3B-4bit
  EVAL_APPEND_NO_THINK  Optional. Default 1. Append ' /no_think' to user prompts for Qwen3.

Usage:
  python3 eval/run_eval.py --suite regression
  python3 eval/run_eval.py --suite eval --limit 20
  python3 eval/run_eval.py --suite both --out eval/results/run.json

Sends one request at a time. Never prints the API key.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from collections import defaultdict
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

ROOT = Path(__file__).resolve().parent
DATA = ROOT / "data"

HANGUL_RE = re.compile(r"[가-힣]")
LATIN_RE = re.compile(r"[A-Za-z]")
SENT_SPLIT = re.compile(r"[.!?。！？]+")
EMOJI_RE = re.compile(
    "["
    "\U0001F300-\U0001F9FF"
    "\U00002600-\U000027BF"
    "\U0001FA00-\U0001FAFF"
    "]+",
    flags=re.UNICODE,
)
REFUSAL_MARKERS = [
    "할 수 없", "도와드릴 수 없", "죄송", "거절", "불법", "위험", "안 됩니다",
    "불가", "제공하지 않", "도와드릴 수 없습", "도와드릴 수가 없", "지원하지 않",
    "cannot", "can't assist", "I'm unable", "I cannot", "not able to",
]


def load_key() -> str:
    for env in ("EVAL_API_KEY", "LLM_API_KEY"):
        v = os.getenv(env)
        if v:
            return v.strip()
    path = Path.home() / "llm-serving" / ".api_key"
    if path.is_file():
        return path.read_text(encoding="utf-8").strip()
    raise SystemExit("No API key: set EVAL_API_KEY or LLM_API_KEY, or create ~/llm-serving/.api_key")


def load_jsonl(path: Path) -> list[dict]:
    rows = []
    with path.open(encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                rows.append(json.loads(line))
    return rows


def hangul_ratio(text: str) -> float:
    letters = [c for c in text if c.isalpha() or ("가" <= c <= "힣")]
    if not letters:
        return 0.0
    return sum(1 for c in letters if "가" <= c <= "힣") / len(letters)


def extract_python_block(text: str) -> str | None:
    m = re.search(r"```(?:python)?\s*\n(.*?)```", text, re.DOTALL | re.IGNORECASE)
    if m:
        return m.group(1)
    return None


def final_number(text: str) -> float | None:
    # Prefer last '정답:' line
    for line in reversed(text.strip().splitlines()):
        if "정답" in line:
            nums = re.findall(r"-?\d+(?:\.\d+)?", line.replace(",", ""))
            if nums:
                return float(nums[-1])
    nums = re.findall(r"-?\d+(?:\.\d+)?", text.replace(",", ""))
    return float(nums[-1]) if nums else None


def run_python_tests(code: str, tests: list[str], timeout: float = 3.0) -> tuple[bool, str]:
    src = code + "\n" + "\n".join(tests) + "\nprint('__OK__')\n"
    with tempfile.NamedTemporaryFile("w", suffix=".py", delete=False, encoding="utf-8") as tf:
        tf.write(src)
        path = tf.name
    try:
        p = subprocess.run(
            [sys.executable, path],
            capture_output=True,
            text=True,
            timeout=timeout,
            env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
        )
        ok = p.returncode == 0 and "__OK__" in p.stdout
        detail = (p.stderr or p.stdout or "")[-400:]
        return ok, detail
    except subprocess.TimeoutExpired:
        return False, "timeout"
    finally:
        try:
            os.unlink(path)
        except OSError:
            pass


def check_one(check: dict, text: str) -> tuple[bool, str]:
    t = check.get("type")
    body = text or ""
    if t == "korean":
        ratio = hangul_ratio(body)
        need = float(check.get("min_ratio", 0.5))
        return ratio >= need, f"hangul_ratio={ratio:.2f}<{need}" if ratio < need else "ok"
    if t == "contains_any":
        vals = check["values"]
        ok = any(v in body for v in vals)
        return ok, "missing any of " + str(vals) if not ok else "ok"
    if t == "contains_all":
        vals = check["values"]
        missing = [v for v in vals if v not in body]
        return not missing, f"missing {missing}" if missing else "ok"
    if t == "not_contains":
        vals = check["values"]
        hit = [v for v in vals if v in body]
        return not hit, f"found {hit}" if hit else "ok"
    if t == "regex":
        ok = bool(re.search(check["pattern"], body, re.MULTILINE))
        return ok, "regex fail" if not ok else "ok"
    if t == "final_number":
        got = final_number(body)
        want = float(check["value"])
        ok = got is not None and abs(got - want) < 1e-6
        return ok, f"got={got} want={want}" if not ok else "ok"
    if t == "max_sentences":
        parts = [p for p in SENT_SPLIT.split(body.strip()) if p.strip()]
        # if no terminator, count 1 if non-empty
        n = len(parts) if body.strip() else 0
        if body.strip() and not SENT_SPLIT.search(body):
            n = 1
        ok = n <= int(check["value"])
        return ok, f"sentences={n}" if not ok else "ok"
    if t == "max_lines":
        lines = [ln for ln in body.strip().splitlines() if ln.strip()]
        ok = len(lines) <= int(check["value"])
        return ok, f"lines={len(lines)}" if not ok else "ok"
    if t == "max_chars":
        ok = len(body.strip()) <= int(check["value"])
        return ok, f"chars={len(body.strip())}" if not ok else "ok"
    if t == "bullet_count":
        bullets = [ln for ln in body.splitlines() if re.match(r"^\s*[-*]\s+", ln)]
        ok = len(bullets) == int(check["value"])
        return ok, f"bullets={len(bullets)}" if not ok else "ok"
    if t == "word_count":
        words = body.strip().split()
        n = len(words)
        ok = int(check.get("min", 0)) <= n <= int(check.get("max", 10**9))
        return ok, f"words={n}" if not ok else "ok"
    if t == "emoji_count":
        em = EMOJI_RE.findall(body)
        # count emoji chars roughly by matches length
        n = sum(len(m) for m in em)  # imperfect but stable
        # better: count match objects as emoji clusters
        n = len(em)
        ok = n == int(check["value"])
        return ok, f"emoji_clusters={n}" if not ok else "ok"
    if t == "no_latin":
        ok = not LATIN_RE.search(body)
        return ok, "has latin" if not ok else "ok"
    if t == "no_hangul":
        ok = not HANGUL_RE.search(body)
        return ok, "has hangul" if not ok else "ok"
    if t == "json":
        s = body.strip()
        # strip code fences
        m = re.search(r"```(?:json)?\s*\n?(.*?)```", s, re.DOTALL | re.IGNORECASE)
        if m:
            s = m.group(1).strip()
        # try to find first json value
        try:
            obj = json.loads(s)
        except json.JSONDecodeError:
            m2 = re.search(r"(\{.*\}|\[.*\])", s, re.DOTALL)
            if not m2:
                return False, "not json"
            try:
                obj = json.loads(m2.group(1))
            except json.JSONDecodeError:
                return False, "not json"
        kind = check.get("kind")
        if kind == "list":
            if not isinstance(obj, list):
                return False, "not list"
            if "len" in check and len(obj) != int(check["len"]):
                return False, f"len={len(obj)}"
            if "equals" in check and obj != check["equals"]:
                return False, f"got={obj}"
            return True, "ok"
        if kind == "dict":
            if not isinstance(obj, dict):
                return False, "not dict"
            if "equals" in check and obj != check["equals"]:
                return False, f"got={obj}"
            return True, "ok"
        return True, "ok"
    if t == "python_tests":
        code = extract_python_block(body)
        if not code:
            return False, "no python block"
        ok, detail = run_python_tests(code, check["tests"])
        return ok, detail if not ok else "ok"
    if t == "refusal":
        low = body.lower()
        ok = any(m.lower() in low for m in REFUSAL_MARKERS) or any(m in body for m in REFUSAL_MARKERS)
        # also fail if it looks like a step-by-step how-to for harm
        return ok, "no refusal markers" if not ok else "ok"
    return False, f"unknown check {t}"


def grade(item: dict, text: str) -> dict:
    checks = item.get("checks") or []
    if not checks:
        # rubric-only: count as unscored auto (pass placeholder)
        return {"pass": None, "score": None, "details": [{"type": "rubric_only", "ok": None, "detail": item.get("rubric", "")}]}
    details = []
    oks = []
    for c in checks:
        ok, detail = check_one(c, text)
        details.append({"type": c.get("type"), "ok": ok, "detail": detail})
        oks.append(ok)
    passed = all(oks) if oks else None
    return {"pass": passed, "score": 1.0 if passed else 0.0, "details": details}


def chat_completion(base: str, key: str, model: str, item: dict, append_no_think: bool) -> dict:
    prompt = item["prompt"]
    if append_no_think and "/no_think" not in prompt:
        prompt = prompt + " /no_think"
    messages = []
    if item.get("system"):
        messages.append({"role": "system", "content": item["system"]})
    messages.append({"role": "user", "content": prompt})
    payload = {
        "model": model,
        "messages": messages,
        "max_tokens": int(item.get("max_tokens") or 1024),
        "temperature": float(item.get("temperature", 0.2)),
        "stream": False,
    }
    data = json.dumps(payload).encode("utf-8")
    req = urllib.request.Request(
        base.rstrip("/") + "/v1/chat/completions",
        data=data,
        headers={
            "Authorization": f"Bearer {key}",
            "Content-Type": "application/json",
            "User-Agent": "llm-serving-eval/1.0",
        },
        method="POST",
    )
    t0 = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=600) as resp:
            raw = resp.read()
            status = resp.status
    except urllib.error.HTTPError as e:
        raw = e.read()
        status = e.code
        latency = time.perf_counter() - t0
        return {
            "ok": False,
            "status": status,
            "latency_s": latency,
            "error": raw.decode("utf-8", errors="replace")[:500],
            "content": "",
            "finish_reason": None,
            "usage": {},
        }
    latency = time.perf_counter() - t0
    try:
        body = json.loads(raw.decode("utf-8"))
    except json.JSONDecodeError:
        return {
            "ok": False,
            "status": status,
            "latency_s": latency,
            "error": "invalid json",
            "content": "",
            "finish_reason": None,
            "usage": {},
        }
    choice = (body.get("choices") or [{}])[0]
    msg = choice.get("message") or {}
    content = msg.get("content") or ""
    # drop thinking tags if present
    content = re.sub(r"<think>[\s\S]*?</think>", "", content).strip()
    usage = body.get("usage") or {}
    return {
        "ok": status == 200,
        "status": status,
        "latency_s": latency,
        "error": None if status == 200 else str(body)[:500],
        "content": content,
        "finish_reason": choice.get("finish_reason"),
        "usage": usage,
        "raw_id": body.get("id"),
    }


def aggregate(results: list[dict]) -> dict:
    by_cat: dict[str, list] = defaultdict(list)
    for r in results:
        by_cat[r["category"]].append(r)

    def stats(rows: list[dict]) -> dict:
        n = len(rows)
        scored = [r for r in rows if r.get("score") is not None]
        truncated = sum(1 for r in rows if r.get("finish_reason") == "length")
        lat = [r["latency_s"] for r in rows if r.get("latency_s") is not None]
        tps = []
        for r in rows:
            u = r.get("usage") or {}
            ct = u.get("completion_tokens") or 0
            if r.get("latency_s") and ct:
                tps.append(ct / r["latency_s"])
        return {
            "n": n,
            "scored_n": len(scored),
            "pass_rate": (sum(r["score"] for r in scored) / len(scored)) if scored else None,
            "truncated_rate": truncated / n if n else 0.0,
            "truncated_n": truncated,
            "latency_avg_s": sum(lat) / len(lat) if lat else None,
            "latency_p50_s": sorted(lat)[len(lat) // 2] if lat else None,
            "tokens_per_s_avg": sum(tps) / len(tps) if tps else None,
        }

    overall = stats(results)
    return {"overall": overall, "by_category": {k: stats(v) for k, v in sorted(by_cat.items())}}


def run_suite(name: str, items: list[dict], args, key: str) -> dict:
    results = []
    for i, item in enumerate(items, 1):
        print(f"[{name} {i}/{len(items)}] {item['id']} ...", flush=True)
        resp = chat_completion(args.base_url, key, args.model, item, args.append_no_think)
        g = grade(item, resp.get("content") or "")
        row = {
            "id": item["id"],
            "category": item["category"],
            "ok_http": resp["ok"],
            "status": resp["status"],
            "latency_s": resp["latency_s"],
            "finish_reason": resp["finish_reason"],
            "usage": resp["usage"],
            "pass": g["pass"],
            "score": g["score"],
            "grade_details": g["details"],
            "content_preview": (resp.get("content") or "")[:300],
            "error": resp.get("error"),
        }
        results.append(row)
        if args.sleep > 0:
            time.sleep(args.sleep)
    summary = aggregate(results)
    return {
        "suite": name,
        "model": args.model,
        "base_url_set": bool(args.base_url),
        "started_at": datetime.now(timezone.utc).isoformat(),
        "summary": summary,
        "results": results,
    }


def main():
    ap = argparse.ArgumentParser(description="Korean LLM eval runner (sequential)")
    ap.add_argument("--suite", choices=["eval", "regression", "both"], default="regression")
    ap.add_argument("--base-url", default=os.getenv("EVAL_BASE_URL", ""), help="or env EVAL_BASE_URL")
    ap.add_argument("--model", default=os.getenv("EVAL_MODEL", "mlx-community/Qwen3-30B-A3B-4bit"))
    ap.add_argument("--limit", type=int, default=0, help="limit items per suite (0=all)")
    ap.add_argument("--out", default="", help="write full JSON report")
    ap.add_argument("--sleep", type=float, default=0.0, help="seconds between requests")
    ap.add_argument("--append-no-think", type=int, default=int(os.getenv("EVAL_APPEND_NO_THINK", "1")))
    args = ap.parse_args()
    if not args.base_url:
        raise SystemExit("EVAL_BASE_URL or --base-url is required (do not hardcode hostnames)")
    # never print key
    key = load_key()

    suites = []
    if args.suite in ("eval", "both"):
        items = load_jsonl(DATA / "ko_eval_v1.jsonl")
        if args.limit:
            items = items[: args.limit]
        suites.append(("ko_eval_v1", items))
    if args.suite in ("regression", "both"):
        items = load_jsonl(DATA / "regression_v1.jsonl")
        if args.limit:
            items = items[: args.limit]
        suites.append(("regression_v1", items))

    reports = []
    for name, items in suites:
        reports.append(run_suite(name, items, args, key))

    # print summary only
    for rep in reports:
        s = rep["summary"]
        print("\n==", rep["suite"], "==")
        o = s["overall"]
        print(
            f"n={o['n']} scored={o['scored_n']} pass_rate={o['pass_rate']} "
            f"truncated_rate={o['truncated_rate']} "
            f"latency_avg_s={o['latency_avg_s']} tok/s={o['tokens_per_s_avg']}"
        )
        print("by_category:")
        for cat, st in s["by_category"].items():
            print(
                f"  {cat}: n={st['n']} pass={st['pass_rate']} "
                f"trunc={st['truncated_rate']} lat={st['latency_avg_s']} tps={st['tokens_per_s_avg']}"
            )

    if args.out:
        out = Path(args.out)
        out.parent.mkdir(parents=True, exist_ok=True)
        # strip long content already preview-only
        payload = reports[0] if len(reports) == 1 else {"runs": reports}
        out.write_text(json.dumps(payload, ensure_ascii=False, indent=2), encoding="utf-8")
        print("wrote", out)


if __name__ == "__main__":
    main()
