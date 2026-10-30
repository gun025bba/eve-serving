#!/usr/bin/env python3
"""Frozen-prompt runs of the auto tool selection eval on the #158 prompt (items_c/items_d/items_h and the original items.jsonl).
One decision round per item, no tool is executed, one request at a time against the model server (default 127.0.0.1:8080; the gateway port is refused).

Variants
  p158  system = Eve v3b (web on) + #158 gateway text (gateway_tools_system_158.txt, #158 head a6f8440) + date line (+ Gate hint when the #158 Gate regex matches)
        tools = web_search, web_fetch (+ the 5 calendar/reminder tools with --tools pim); tool_choice auto; sampling temperature 0.7, top_p 0.9, max_tokens 8192
  v4    design rev 5 §4.4.1 emulated at the model-server level: one-word classifier call (current user message only, enable_thinking=false), then the p158 decision
        request; on "web" the instruction line is added to the system text, on "none" nothing changes and the tools stay exposed. Latency = classifier + decision.
--thinking on = Qwen3 default (what the gateway sends today); kwargs = chat_template_kwargs enable_thinking=false for the decision request.
"""
import argparse, json, statistics, sys, time
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import run_auto_select as ras  # noqa: E402
from probe_latency_158 import sys158, gate  # noqa: E402
from run_eval import refuse_gateway  # noqa: E402
from run_tools import post, parse_calls, strip_think  # noqa: E402

V4_LINE = "\n이 질문은 검색이 필요하다. web_search를 호출하라."


def decide(it, args, system, defs):
    msgs = [{"role": "system", "content": system}] + list(it.get("history") or []) + [{"role": "user", "content": it["query"]}]
    body = {"model": args.model, "messages": msgs, "tools": defs, "tool_choice": "auto", "temperature": args.temperature, "top_p": args.top_p, "max_tokens": args.max_tokens, "stream": False}
    if args.thinking == "kwargs": body["chat_template_kwargs"] = {"enable_thinking": False}
    t0 = time.time(); r = post(args.base, body, args.key); dt = round(time.time() - t0, 2)
    ch = r["choices"][0]; msg = ch["message"]
    return {"calls": [{"name": c["name"], "arguments": c["arguments"]} for c in parse_calls(msg)], "native_tool_calls": bool(msg.get("tool_calls")),
            "finish_reason": ch.get("finish_reason"), "completion_tokens": (r.get("usage") or {}).get("completion_tokens"), "latency_s": dt,
            "content_head": strip_think(msg.get("content"))[:80]}


def run_item(it, args, defs):
    system = sys158(gate(it["query"]))
    extra = {}
    if args.variant == "v4":
        c = ras.run_classifier(it, args)
        yes = bool(c["calls"]); extra = {"v4_yes": yes, "v4_latency_s": c["latency_s"]}
        if yes: system += V4_LINE
    r = decide(it, args, system, defs)
    if args.variant == "v4": r["latency_decision_s"] = r["latency_s"]; r["latency_s"] = round(r["latency_s"] + extra["v4_latency_s"], 2)
    r.update(extra); r["id"] = it["id"]
    return r


def grade_d(it, r):
    names = [c["name"] for c in r["calls"]]; exp = it["expected_any"]
    if exp == ["none"]: ok = not names
    else: ok = bool(names) and names[0] in exp
    return {"correct": ok, "first_tool": names[0] if names else None, "n_calls": len(names), "any_expected": any(n in exp for n in names),
            "web_search_called": "web_search" in names, "unnecessary_any_tool": exp == ["none"] and bool(names)}


def summarize_d(items, rows):
    by = {r["id"]: r for r in rows}; cats = {}
    for it in items:
        g = by[it["id"]]["grade"]; c = cats.setdefault(it["category"], {"n": 0, "first_tool_ok": 0, "any_expected": 0, "wrong_first": {}})
        c["n"] += 1; c["first_tool_ok"] += g["correct"]; c["any_expected"] += g["any_expected"]
        if not g["correct"]: k = g["first_tool"] or "none"; c["wrong_first"][k] = c["wrong_first"].get(k, 0) + 1
    pos = [by[i["id"]]["grade"] for i in items if i["expected_any"] != ["none"]]
    neg = [by[i["id"]]["grade"] for i in items if i["expected_any"] == ["none"]]
    return {"n": len(rows), "positive_first_tool_ok": sum(g["correct"] for g in pos), "n_positive": len(pos), "positive_any_expected": sum(g["any_expected"] for g in pos),
            "negative_no_tool": sum(g["correct"] for g in neg), "n_negative": len(neg), "per_category": cats,
            "latency_s_median": round(statistics.median(r["latency_s"] for r in rows), 2)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8080"); ap.add_argument("--key", default="")
    ap.add_argument("--model", default="mlx-community/Qwen3-30B-A3B-4bit")
    ap.add_argument("--variant", choices=["p158", "v4"], default="p158"); ap.add_argument("--thinking", choices=["on", "kwargs"], default="on")
    ap.add_argument("--tools", choices=["web", "pim"], default="web")
    ap.add_argument("--temperature", type=float, default=0.7); ap.add_argument("--top-p", type=float, default=0.9); ap.add_argument("--max-tokens", type=int, default=8192)
    ap.add_argument("--items", required=True); ap.add_argument("--limit", type=int, default=0); ap.add_argument("--out", required=True)
    args = ap.parse_args(); refuse_gateway(args.base)
    defs = ras.load_defs("") + (json.load(open(HERE / "pim_tool_defs.json")) if args.tools == "pim" else [])
    items = [json.loads(l) for l in open(args.items, encoding="utf-8") if l.strip()]
    if args.limit: items = items[: args.limit]
    d_mode = "expected_any" in items[0]
    rows = []
    for it in items:
        try: r = run_item(it, args, defs)
        except Exception as e: r = {"id": it["id"], "calls": [], "native_tool_calls": False, "finish_reason": "error", "latency_s": 0, "error": repr(e), "content_head": ""}
        r["grade"] = grade_d(it, r) if d_mode else ras.grade(it, r); rows.append(r)
        print(f"[{it['id']}] {it['category']:<18} exp={it.get('expected_any') or it['expected_tool']} calls={[c['name'] for c in r['calls']]} ok={r['grade']['correct']} {r['latency_s']}s", flush=True)
    m = summarize_d(items, rows) if d_mode else ras.metrics(items, rows)
    cfg = {k: getattr(args, k.replace("-", "_")) for k in ("model", "variant", "thinking", "tools", "temperature", "top-p", "max-tokens")}
    json.dump({"config": cfg, "items_file": Path(args.items).name, "metrics": m, "rows": rows}, open(args.out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(json.dumps({k: v for k, v in m.items() if k not in ("per_category", "latency_s", "args_on_called_positives")}, ensure_ascii=False))


if __name__ == "__main__":
    main()
