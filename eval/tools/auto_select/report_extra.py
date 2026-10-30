#!/usr/bin/env python3
"""Extra aggregates over runs/*.json (offline, no server calls): Wilson 95% intervals, recall without the borderline-leaning
categories (latest_version, implied_recency), preamble rate before a tool call, tool-call latency among called positives.
Usage: python report_extra.py  -> prints markdown, writes results/extra.json"""
import json, math, statistics
from pathlib import Path
HERE = Path(__file__).resolve().parent
ITEMS = {i["id"]: i for i in map(json.loads, (HERE / "items.jsonl").read_text(encoding="utf-8").splitlines())}
BORDER = {"latest_version", "implied_recency"}
RUNS = ["web_on_nothink_r1", "web_on_nothink_r2", "no_hint_nothink_r1", "bare_nothink_r1", "web_on_nodate_nothink_r1", "v1_nothink_r1", "v2_nothink_r1",
        "v2_kwargs_r1", "classifier_nothink_r1", "web_on_think_r1", "v1_think_r1", "v2_think_r1"]

def wilson(k, n, z=1.96):
    if n == 0: return (0.0, 0.0)
    p = k / n; d = 1 + z * z / n; c = p + z * z / (2 * n); h = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n))
    return ((c - h) / d, (c + h) / d)

def pct(x): return f"{100 * max(x, 0.0):.1f}%"
out = {}; lines = ["| run | recall (all 68) [95% CI] | recall without borderline-leaning (54) | unnecessary search (62) [95% CI] | preamble before call | first-answer latency of called positives (median / p95 s) |", "|---|---|---|---|---|---|"]
for r in RUNS:
    p = HERE / "runs" / f"{r}.json"
    if not p.exists(): continue
    rows = json.loads(p.read_text(encoding="utf-8"))["rows"]
    pos = [x for x in rows if ITEMS[x["id"]]["expected_tool"] == "web_search" and ITEMS[x["id"]]["category"] != "query_privacy"]
    pos_all = [x for x in rows if ITEMS[x["id"]]["expected_tool"] == "web_search"]
    neg = [x for x in rows if ITEMS[x["id"]]["expected_tool"] == "none"]
    called = lambda x: any(c["name"] == "web_search" for c in x["calls"])
    tp = sum(called(x) for x in pos_all); fp = sum(called(x) for x in neg)
    nb = [x for x in pos_all if ITEMS[x["id"]]["category"] not in BORDER]; tpb = sum(called(x) for x in nb)
    cl = [x for x in pos_all if called(x)]
    pre = sum(1 for x in rows if x["calls"] and (x.get("content_head") or "").strip()); with_call = sum(1 for x in rows if x["calls"])
    lat = sorted(x["latency_s"] for x in cl)
    lo, hi = wilson(tp, len(pos_all)); flo, fhi = wilson(fp, len(neg))
    out[r] = {"recall": tp / len(pos_all), "recall_ci95": [lo, hi], "recall_without_borderline": tpb / len(nb), "n_without_borderline": len(nb),
              "unnecessary": fp / len(neg), "unnecessary_ci95": [flo, fhi], "preamble": pre, "rows_with_call": with_call,
              "latency_called_median": statistics.median(lat) if lat else None, "latency_called_p95": lat[int(0.95 * (len(lat) - 1))] if lat else None}
    lines.append(f"| {r} | {pct(tp/len(pos_all))} [{pct(lo)}–{pct(hi)}] | {pct(tpb/len(nb))} | {pct(fp/len(neg))} [{pct(flo)}–{pct(fhi)}] | {('n/a' if r.startswith('classifier') else f'{pre}/{with_call}')} | {statistics.median(lat):.2f} / {lat[int(0.95*(len(lat)-1))]:.2f} |")
(HERE / "results" / "extra.json").write_text(json.dumps(out, indent=1), encoding="utf-8")
print("\n".join(lines))
