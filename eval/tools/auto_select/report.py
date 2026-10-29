#!/usr/bin/env python3
"""Aggregates runs/*.json into results/summary.json (metrics + compact per-item decisions) and prints markdown tables.
Raw runs stay in runs/ (git-ignored); the summary holds decisions and tool arguments, no model text."""
import glob, json, re, sys
from collections import defaultdict
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from run_auto_select import gate_hint  # noqa: E402

items = [json.loads(l) for l in open(HERE / "items.jsonl", encoding="utf-8") if l.strip()]
IT = {i["id"]: i for i in items}
runs = defaultdict(list)  # config name -> list of run dicts
for p in sorted(glob.glob(str(HERE / "runs" / "*.json"))):
    m = re.match(r"(.+)_r(\d+)\.json$", Path(p).name)
    if m: runs[m.group(1)].append(json.load(open(p, encoding="utf-8")))
NAMES = {
    "web_on_nothink": "web_on (V0 prompt), /no_think",
    "no_hint_nothink": "no_hint (no web section), /no_think",
    "bare_nothink": "bare (no web section, no tools text), /no_think",
    "web_on_nodate_nothink": "web_on without the date line, /no_think",
    "v1_nothink": "V1 (+search rules), /no_think",
    "v2_nothink": "V2 (+Gate hint), /no_think",
    "classifier_nothink": "V4 classifier (one word, no tools)",
    "web_on_think": "V0 web_on, thinking on",
    "v1_think": "V1 +search rules, thinking on",
    "v2_think": "V2 +Gate hint, thinking on",
    "v2_kwargs": "V3 = V2 with enable_thinking=false (request kwargs)",
    "no_hint_think": "no_hint, thinking on",
}
order = [k for k in NAMES if k in runs]


def sub(rows, pred):
    """recall / unnecessary-search rate on the items selected by pred"""
    tp = fp = fn = tn = 0
    for r in rows:
        it = IT[r["id"]]
        if not pred(it): continue
        c = r["grade"]["called_search"]
        if it["expected_tool"] == "web_search": tp += c; fn += not c
        else: fp += c; tn += not c
    return {"n_pos": tp + fn, "n_neg": fp + tn, "tp": tp, "fp": fp, "fn": fn, "recall": tp / (tp + fn) if tp + fn else None,
            "unnecessary": fp / (fp + tn) if fp + tn else None, "precision": tp / (tp + fp) if tp + fp else None}


def pct(x): return "-" if x is None else f"{100*x:.1f}%"
def avg(v): v = [x for x in v if x is not None]; return sum(v) / len(v) if v else None


summary = {"n_items": len(items), "configs": {}, "items": {}}
for k in order:
    summary["configs"][k] = {"label": NAMES[k], "repeats": len(runs[k]), "metrics": [r["metrics"] for r in runs[k]],
                             "holdout": [sub(r["rows"], lambda i: i["split"] == "holdout") for r in runs[k]],
                             "dev": [sub(r["rows"], lambda i: i["split"] == "dev") for r in runs[k]]}
for it in items:
    row = {"category": it["category"], "group": it["group"], "split": it["split"], "expected": it["expected_tool"], "tricky": bool(it.get("tricky")), "gate": gate_hint(it["query"]),
           "search_calls": {}, "query_example": None}
    for k in order:
        row["search_calls"][k] = f"{sum(1 for r in runs[k] for x in r['rows'] if x['id']==it['id'] and x['grade']['called_search'])}/{len(runs[k])}"
    for k in order:
        for r in runs[k]:
            for x in r["rows"]:
                if x["id"] == it["id"] and x["grade"]["called_search"] and row["query_example"] is None and x["calls"] and x["calls"][0]["arguments"]:
                    row["query_example"] = x["calls"][0]["arguments"]
    summary["items"][it["id"]] = row
(HERE / "results").mkdir(exist_ok=True)
json.dump(summary, open(HERE / "results" / "summary.json", "w", encoding="utf-8"), ensure_ascii=False, indent=1)

out = []
out.append("### Whole set (130 items)\n")
out.append("| config | runs | precision | recall | F1 | unnecessary search | missed search | any tool on none | tricky acc | latency mean / p95 (s) |\n|---|---|---|---|---|---|---|---|---|---|")
for k in order:
    ms = [r["metrics"] for r in runs[k]]
    cell = lambda key: " / ".join(pct(m[key]) for m in ms)
    la = avg([m["latency_s"]["all"]["mean"] for m in ms]); lp = avg([m["latency_s"]["all"]["p95"] for m in ms])
    out.append(f"| {NAMES[k]} | {len(ms)} | {cell('precision')} | {cell('recall')} | {cell('f1')} | {cell('unnecessary_search_rate')} | {cell('missed_search_rate')} | {cell('unnecessary_any_tool_rate')} | {cell('tricky_accuracy')} | {la:.2f} / {lp:.2f} |")
out.append("\n(several repeats show `r1 / r2`)\n")
out.append("### Dev vs holdout (recall and unnecessary-search rate)\n")
out.append("| config | dev recall | dev unnecessary | holdout recall | holdout unnecessary | holdout n (pos/neg) | holdout vs thresholds (recall ≥ 90%, unnecessary ≤ 8%) |\n|---|---|---|---|---|---|---|")
for k in order:
    d = summary["configs"][k]
    f = lambda part, key: " / ".join(pct(x[key]) for x in d[part])
    ok = ["pass" if (x["recall"] is not None and x["recall"] >= 0.9 and x["unnecessary"] <= 0.08) else "fail" for x in d["holdout"]]
    out.append(f"| {NAMES[k]} | {f('dev','recall')} | {f('dev','unnecessary')} | {f('holdout','recall')} | {f('holdout','unnecessary')} | {d['holdout'][0]['n_pos']}/{d['holdout'][0]['n_neg']} | {' / '.join(ok)} |")
out.append("\n### Argument quality (called positives; web_search calls)\n")
out.append("| config | n | schema valid | query relevant | no forbidden text | time_range appropriate |\n|---|---|---|---|---|---|")
for k in order:
    if k.startswith("classifier"): continue
    ms = [r["metrics"]["args_on_called_positives"] for r in runs[k]]
    f = lambda key: " / ".join(pct(m[key]) for m in ms)
    out.append(f"| {NAMES[k]} | {' / '.join(str(m['n']) for m in ms)} | {f('schema_valid')} | {f('query_relevant')} | {f('no_forbidden_text')} | {f('time_range_appropriate')} |")
out.append("\n### Parsing reliability (all rows of every run, tools attached)\n")
out.append("| config | requests | native tool_calls | text-form `<tool_call>` only | truncated (finish=length) | request errors | calls with unparsable args |\n|---|---|---|---|---|---|---|")
for k in order:
    if k.startswith("classifier"): continue
    rows = [x for r in runs[k] for x in r["rows"]]
    nat = sum(1 for x in rows if x["calls"] and x["native_tool_calls"]); txt = sum(1 for x in rows if x["calls"] and not x["native_tool_calls"])
    bad = sum(1 for x in rows for c in x["calls"] if "_raw" in (c["arguments"] or {}))
    out.append(f"| {NAMES[k]} | {len(rows)} | {nat} | {txt} | {sum(1 for x in rows if x['finish_reason']=='length')} | {sum(1 for x in rows if x['finish_reason']=='error')} | {bad} |")
out.append("\n### Gate (keyword list from the design doc, no model) vs labels\n")
pos = [i for i in items if i["expected_tool"] == "web_search"]; neg = [i for i in items if i["expected_tool"] == "none"]
gp = sum(1 for i in pos if gate_hint(i["query"]) == "fresh"); gn = sum(1 for i in neg if gate_hint(i["query"]) == "fresh")
out.append(f"- Gate fires on {gp}/{len(pos)} positives ({pct(gp/len(pos))}) and on {gn}/{len(neg)} negatives ({pct(gn/len(neg))}).\n")
out.append("### Per category (correct/n, first run; fp = unnecessary search, fn = missed)\n")
cats = list(dict.fromkeys(i["category"] for i in items))
out.append("| category | n | exp | " + " | ".join(NAMES[k] for k in order) + " |\n|---|---|---|" + "---|" * len(order))
for c in cats:
    n = sum(1 for i in items if i["category"] == c); exp = {i["expected_tool"] for i in items if i["category"] == c}
    cells = []
    for k in order:
        pc = runs[k][0]["metrics"]["per_category"][c]
        cells.append(f"{pc['correct']}/{pc['n']}" + (f" fp{pc['fp']}" if pc["fp"] else "") + (f" fn{pc['fn']}" if pc["fn"] else ""))
    out.append(f"| {c} | {n} | {'/'.join(sorted(exp))} | " + " | ".join(cells) + " |")
out.append("\n### Latency (s, first run, whole request incl. thinking and answer text)\n")
out.append("| config | positives mean / median / p95 | negatives mean / median / p95 |\n|---|---|---|")
for k in order:
    L = runs[k][0]["metrics"]["latency_s"]
    f = lambda x: f"{x['mean']} / {x['median']} / {x['p95']}" if x else "-"
    out.append(f"| {NAMES[k]} | {f(L['positive'])} | {f(L['negative'])} |")
out.append("\n### Items decided wrongly in some run (search calls / repeats)\n")
out.append("| id | category | split | expected | query | " + " | ".join(NAMES[k] for k in order) + " |\n|---|---|---|---|---|" + "---|" * len(order))
for it in items:
    row = summary["items"][it["id"]]; bad = False
    for k in order:
        a, b = map(int, row["search_calls"][k].split("/"))
        if (it["expected_tool"] == "web_search" and a < b) or (it["expected_tool"] == "none" and a > 0): bad = True
    if bad:
        q = it["query"].replace("\n", " ").replace("|", "\\|")
        out.append(f"| {it['id']} | {it['category']} | {it['split']} | {it['expected_tool']} | {q[:50]} | " + " | ".join(row["search_calls"][k] for k in order) + " |")
print("\n".join(out))
