#!/usr/bin/env python3
"""Auto tool selection eval: does the model call web_search on its own (tool_choice=auto, no button)?

Single decision round only: the first model response to the conversation, with the gateway's tool
definitions and the gateway tools system text. Nothing is searched or fetched (no tool results are
needed to judge the decision). Talks to the model server directly (default 127.0.0.1:8080), one
request at a time, and refuses the operational gateway port (shared guard from eval/run_eval.py).

Variants (system prompt):
  web_on   Eve v3b + web-on section + gateway tools text   (what runs today when the web button is on)
  no_hint  Eve v3b, web section left out + gateway tools text   (simulates "no button": tools are
           attached, but no prompt says web search is on)
  bare     Eve v3b, no web section, no tools text   (extra lower bound; tools are still attached)
  v1       web_on + search rules (rules_v1.txt)                      (design V1; the v3b prompt already carries the date line)
  v2       v1 + Gate hint line for questions with a freshness word   (design V2; Gate = keyword list from the design doc)
  classifier  one request per item, no tools: model answers "web" or "none", max_tokens 8, /no_think (design V4)
--no-date removes the date sentence from the Eve prompt (the gateway code does not inject a date today, design F6).
Sampling defaults match the gateway config (temperature 0.7, top_p 0.9, max_tokens 8192).
--thinking on = Qwen3 default (what the gateway sends today); off appends /no_think to the last user turn;
kwargs sends chat_template_kwargs {"enable_thinking": false} in the request body.
"""
import argparse, json, re, statistics, sys, time
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1])); sys.path.insert(0, str(HERE.parent))  # eval/, eval/tools/
from run_eval import refuse_gateway, render_template  # noqa: E402
from run_tools import post, parse_calls, strip_think  # noqa: E402

PROMPT = HERE.parents[1] / "prompts" / "eve_unified_v3b.txt"
SCHEMA_TR = ("", "day", "week", "month", "year"); SCHEMA_LANG = ("auto", "ko", "en")


def load_defs(repo):
    p = Path(repo) / "internal/tools/testdata/tooljson_v1.1.0.golden" if repo else HERE / "gateway_tool_defs.json"
    return json.loads(p.read_text(encoding="utf-8"))


GATE = re.compile(r"오늘|어제|지금|최근|요즘|이번\s*주|최신|현재|시세|날씨|뉴스|소식|실시간|올해|20\d\d\s*년")
GATE_LINE = "이 질문은 최신 정보일 가능성이 높습니다. 검색을 먼저 고려하세요."


def gate_hint(text):
    return "fresh" if GATE.search(text) else "none"


def build_system(variant, tools_text, date, no_date=False):
    base = PROMPT.read_text(encoding="utf-8").strip()
    web = "on" if variant in ("web_on", "v1", "v2") else ""
    eve = render_template(base, str(PROMPT), web, date or "")
    if no_date:
        eve = "\n".join(l for l in eve.split("\n") if "오늘 날짜는" not in l)
    if variant == "bare": return eve
    out = eve + "\n\n" + tools_text.strip()
    if variant in ("v1", "v2"): out += "\n\n" + (HERE / "rules_v1.txt").read_text(encoding="utf-8").strip()
    return out


def run_classifier(it, args):
    hist = "".join(("사용자: " if m["role"] == "user" else "Eve: ") + m["content"] + "\n" for m in (it.get("history") or []))
    user = (hist + "마지막 질문: " if hist else "질문: ") + it["query"] + " /no_think"
    body = {"model": args.model, "messages": [{"role": "system", "content": (HERE / "classifier_prompt.txt").read_text(encoding="utf-8").strip()},
            {"role": "user", "content": user}], "temperature": args.temperature, "top_p": args.top_p, "max_tokens": 8, "stream": False}
    t0 = time.time(); r = post(args.base, body, args.key); dt = round(time.time() - t0, 2)
    ch = r["choices"][0]; text = strip_think(ch["message"].get("content") or "").strip().lower()
    calls = [{"name": "web_search", "arguments": {}}] if text.startswith("web") else []
    return {"id": it["id"], "calls": calls, "native_tool_calls": False, "classifier": True, "finish_reason": ch.get("finish_reason"),
            "completion_tokens": (r.get("usage") or {}).get("completion_tokens"), "latency_s": dt, "content_head": text[:20]}


def run_item(it, args, system, defs):
    if args.variant == "classifier": return run_classifier(it, args)
    if args.variant == "v2" and gate_hint(it["query"]) == "fresh": system = system + "\n\n" + GATE_LINE
    msgs = [{"role": "system", "content": system}] + list(it.get("history") or [])
    user = it["query"] + (" /no_think" if args.thinking == "off" else "")
    msgs.append({"role": "user", "content": user})
    body = {"model": args.model, "messages": msgs, "tools": defs, "tool_choice": "auto", "temperature": args.temperature,
            "top_p": args.top_p, "max_tokens": args.max_tokens, "stream": False}
    if args.thinking == "kwargs": body["chat_template_kwargs"] = {"enable_thinking": False}
    t0 = time.time()
    r = post(args.base, body, args.key)
    dt = round(time.time() - t0, 2)
    ch = r["choices"][0]; msg = ch["message"]
    native = bool(msg.get("tool_calls"))
    calls = parse_calls(msg)
    return {"id": it["id"], "calls": [{"name": c["name"], "arguments": c["arguments"]} for c in calls], "native_tool_calls": native,
            "finish_reason": ch.get("finish_reason"), "completion_tokens": (r.get("usage") or {}).get("completion_tokens"),
            "latency_s": dt, "content_head": strip_think(msg.get("content"))[:120]}


def grade(it, r):
    calls = r["calls"]; searches = [c for c in calls if c["name"] == "web_search"]
    other = [c for c in calls if c["name"] != "web_search"]
    pos = it["expected_tool"] == "web_search"
    g = {"called_search": bool(searches), "n_calls": len(calls), "gate": gate_hint(it["query"])}
    if pos and r.get("classifier"):
        g["correct"] = bool(searches)
    elif pos:
        g["correct"] = bool(searches)
        if searches:
            qs = [c["arguments"] for c in searches]
            q_ok, rel, clean, tr_ok_all, tr_app = [], [], [], [], []
            for a in qs:
                q = a.get("query"); tr = a.get("time_range", ""); lang = a.get("lang", "auto"); mr = a.get("max_results", 5)
                valid = isinstance(q, str) and 0 < len(q.strip()) <= 256 and tr in SCHEMA_TR and lang in SCHEMA_LANG \
                    and isinstance(mr, int) and 1 <= mr <= 8
                q_ok.append(valid)
                ql = (q or "").lower() if isinstance(q, str) else ""
                rel.append(any(k.lower() in ql for k in it.get("query_any", [])) if it.get("query_any") else True)
                clean.append(not any(k.lower() in ql for k in it.get("query_forbid", [])))
                tr_app.append(tr in it["tr_ok"] if it.get("tr_ok") else True)
            g.update(args_valid=all(q_ok), args_relevant=any(rel), args_clean=all(clean), time_range_appropriate=all(tr_app))
    else:
        bad = list(searches) + [c for c in other if c["name"] not in it.get("ok_extra", [])]
        g["correct"] = not bad
        g["unnecessary_search"] = bool(searches)
        g["unnecessary_any_tool"] = bool(bad)
        if searches and it.get("query_forbid"):
            g["args_clean"] = True
    return g


def metrics(items, rows):
    by = {r["id"]: r for r in rows}
    tp = fp = fn = tn = 0; per = {}
    for it in items:
        r = by.get(it["id"])
        if not r: continue
        g = r["grade"]; pos = it["expected_tool"] == "web_search"
        if pos: tp += g["called_search"]; fn += not g["called_search"]
        else: fp += g["unnecessary_search"]; tn += not g["unnecessary_search"]
        c = per.setdefault(it["category"], {"n": 0, "correct": 0, "fp": 0, "fn": 0})
        c["n"] += 1; c["correct"] += g["correct"]
        if pos and not g["called_search"]: c["fn"] += 1
        if not pos and g["unnecessary_search"]: c["fp"] += 1
    prec = tp / (tp + fp) if tp + fp else 0.0; rec = tp / (tp + fn) if tp + fn else 0.0
    f1 = 2 * prec * rec / (prec + rec) if prec + rec else 0.0
    npos, nneg = tp + fn, fp + tn
    tp_rows = [by[i["id"]]["grade"] for i in items if i["expected_tool"] == "web_search" and i["id"] in by and by[i["id"]]["grade"]["called_search"]]
    tp_rows = [g for g in tp_rows if "args_valid" in g]
    def rate(k): return round(sum(bool(g[k]) for g in tp_rows) / len(tp_rows), 4) if tp_rows else None
    lat = lambda sel: [by[i["id"]]["latency_s"] for i in items if i["id"] in by and sel(i)]
    def ls(v): return {"n": len(v), "mean": round(statistics.mean(v), 2), "median": round(statistics.median(v), 2),
                       "p95": round(sorted(v)[max(0, int(round(0.95 * len(v))) - 1)], 2)} if v else None
    tr = [i for i in items if i.get("tricky") and i["id"] in by]
    return {"n": len(rows), "n_positive": npos, "n_negative": nneg, "tp": tp, "fp": fp, "fn": fn, "tn": tn,
            "precision": round(prec, 4), "recall": round(rec, 4), "f1": round(f1, 4), "accuracy": round((tp + tn) / max(1, tp + tn + fp + fn), 4),
            "unnecessary_search_rate": round(fp / nneg, 4) if nneg else None, "missed_search_rate": round(fn / npos, 4) if npos else None,
            "unnecessary_any_tool_rate": round(sum(by[i["id"]]["grade"].get("unnecessary_any_tool", False) for i in items if i["expected_tool"] == "none" and i["id"] in by) / nneg, 4) if nneg else None,
            "args_on_called_positives": {"n": len(tp_rows), "schema_valid": rate("args_valid"), "query_relevant": rate("args_relevant"),
                                         "no_forbidden_text": rate("args_clean"), "time_range_appropriate": rate("time_range_appropriate")},
            "tricky_accuracy": round(sum(by[i["id"]]["grade"]["correct"] for i in tr) / len(tr), 4) if tr else None, "n_tricky": len(tr),
            "latency_s": {"all": ls(lat(lambda i: True)), "positive": ls(lat(lambda i: i["expected_tool"] == "web_search")), "negative": ls(lat(lambda i: i["expected_tool"] == "none"))},
            "text_form_tool_calls": sum(1 for r in rows if r["calls"] and not r["native_tool_calls"]),
            "truncated": sum(1 for r in rows if r["finish_reason"] == "length"),
            "per_category": per}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:8080"); ap.add_argument("--key", default="")
    ap.add_argument("--model", default="mlx-community/Qwen3-30B-A3B-4bit")
    ap.add_argument("--variant", choices=["web_on", "no_hint", "bare", "v1", "v2", "classifier"], required=True)
    ap.add_argument("--thinking", choices=["on", "off", "kwargs"], default="on")
    ap.add_argument("--temperature", type=float, default=0.7); ap.add_argument("--top-p", type=float, default=0.9)
    ap.add_argument("--max-tokens", type=int, default=8192)
    ap.add_argument("--items", default=str(HERE / "items.jsonl")); ap.add_argument("--limit", type=int, default=0)
    ap.add_argument("--repo", default="", help="repo checkout to read the tool definitions from (default: committed copy of the gateway golden file)")
    ap.add_argument("--prompt-date", default=None); ap.add_argument("--no-date", action="store_true"); ap.add_argument("--out", required=True)
    args = ap.parse_args()
    refuse_gateway(args.base)
    defs = load_defs(args.repo)
    system = build_system(args.variant, (HERE / "gateway_tools_system.txt").read_text(encoding="utf-8"), args.prompt_date, args.no_date)
    items = [json.loads(l) for l in open(args.items, encoding="utf-8") if l.strip()]
    if args.limit: items = items[: args.limit]
    rows = []
    for it in items:
        try: r = run_item(it, args, system, defs)
        except Exception as e: r = {"id": it["id"], "calls": [], "native_tool_calls": False, "finish_reason": "error", "latency_s": 0, "error": repr(e), "content_head": ""}
        r["grade"] = grade(it, r); rows.append(r)
        print(f"[{it['id']}] {it['category']:<18} exp={it['expected_tool']:<10} calls={[c['name'] for c in r['calls']]} ok={r['grade']['correct']} {r['latency_s']}s", flush=True)
    m = metrics(items, rows)
    cfg = {k: getattr(args, k.replace('-', '_')) for k in ("model", "variant", "thinking", "temperature", "top-p", "max-tokens")}
    cfg["no_date"] = args.no_date
    json.dump({"config": cfg, "items_file": Path(args.items).name, "metrics": m, "rows": rows}, open(args.out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(json.dumps({k: m[k] for k in ("precision", "recall", "f1", "accuracy", "unnecessary_search_rate", "missed_search_rate")}))


if __name__ == "__main__":
    main()
