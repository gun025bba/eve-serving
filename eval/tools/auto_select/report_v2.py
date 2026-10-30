#!/usr/bin/env python3
"""Aggregates the #158 frozen-prompt runs (runs/p158_*_rN.json, runs/v4_*_rN.json, results/latency_158_*.json) into results/summary_v2.json and prints markdown tables.
Raw runs stay in runs/ (git-ignored); the summary keeps per-item decisions and tool names only, no model text."""
import glob, json, math, re, statistics
from collections import defaultdict
from pathlib import Path

HERE = Path(__file__).resolve().parent
SETS = {"orig": "items.jsonl", "c": "items_c.jsonl", "h": "items_h.jsonl", "d": "items_d.jsonl"}
ITEMS = {k: {i["id"]: i for i in map(json.loads, open(HERE / f, encoding="utf-8")) } for k, f in SETS.items()}


def wilson(k, n, z=1.96):
    if n == 0: return (None, None)
    p = k / n; d = 1 + z * z / n; c = p + z * z / (2 * n); h = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n))
    return ((c - h) / d, (c + h) / d)


def pct(x): return "-" if x is None else f"{100*x:.1f}%"
def ci(k, n): lo, hi = wilson(k, n); return f"{pct(k/n if n else None)} ({pct(lo)}-{pct(hi)})"


runs = defaultdict(list)
for p in sorted(glob.glob(str(HERE / "runs" / "*_r[0-9].json"))):
    m = re.match(r"(.+)_(orig|c|h|d)_r(\d+)\.json$", Path(p).name)
    if m: runs[(m.group(1), m.group(2))].append(json.load(open(p, encoding="utf-8")))

summary = {}; lines = []
lines.append("| 구성 | 세트 | 실행 | 재현율 평균 [실행별] (Wilson 95%) | 불필요 검색 평균 [실행별] (상한) | 인자 유효 | 중앙 지연 |")
lines.append("|---|---|---|---|---|---|---|")
for (cfg, st), rs in sorted(runs.items()):
    if st == "d": continue
    items = ITEMS[st]; recs = []; fps = []; per_item = defaultdict(list); tpk = tpn = fpk = fpn = 0; lat = []
    av = []
    for r in rs:
        m = r["metrics"]; recs.append(m["recall"]); fps.append(m["unnecessary_search_rate"])
        tpk += m["tp"]; tpn += m["n_positive"]; fpk += m["fp"]; fpn += m["n_negative"]
        for row in r["rows"]:
            per_item[row["id"]].append(bool(row["grade"]["called_search"]))
            lat.append(row["latency_s"])
        if m["args_on_called_positives"]["schema_valid"] is not None: av.append(m["args_on_called_positives"]["schema_valid"])
    pos_ids = [i for i, it in items.items() if it["expected_tool"] == "web_search"]
    stab = {"all_hit": sum(all(per_item[i]) for i in pos_ids), "none_hit": sum(not any(per_item[i]) for i in pos_ids), "mixed": sum(0 < sum(per_item[i]) < len(per_item[i]) for i in pos_ids)}
    lo_fp = wilson(fpk, fpn)[1]
    summary[f"{cfg}/{st}"] = {"runs": len(rs), "recall_by_run": recs, "fp_by_run": fps, "recall_pooled": [tpk, tpn], "fp_pooled": [fpk, fpn], "positive_item_stability": stab,
                              "latency_median_s": round(statistics.median(lat), 2), "latency_p95_s": round(sorted(lat)[max(0, int(round(.95 * len(lat))) - 1)], 2),
                              "per_category": [r["metrics"]["per_category"] for r in rs],
                              "decisions": {i: per_item[i] for i in items}}
    if cfg.startswith("v4"):
        summary[f"{cfg}/{st}"]["v4_yes"] = [sum(1 for row in r["rows"] if row.get("v4_yes")) for r in rs]
        summary[f"{cfg}/{st}"]["v4_latency_median_s"] = round(statistics.median(row["v4_latency_s"] for r in rs for row in r["rows"] if "v4_latency_s" in row), 2)
    lines.append(f"| {cfg} | {st} | {len(rs)} | {pct(statistics.mean(recs))} {[pct(x) for x in recs]} ({ci(tpk, tpn).split(' ',1)[1]} | "
                 f"{pct(statistics.mean(fps))} {[pct(x) for x in fps]} (≤{pct(lo_fp)}) | {pct(statistics.mean(av)) if av else '-'} | {summary[f'{cfg}/{st}']['latency_median_s']}초 |")
print("\n".join(lines))
print()
dl = ["| 구성 | 그룹 D 범주 | n | 첫 도구 정답 | 기대 도구를 한 번이라도 호출 | 틀린 첫 호출 |", "|---|---|---|---|---|---|"]
for (cfg, st), rs in sorted(runs.items()):
    if st != "d": continue
    m = rs[0]["metrics"]; summary[f"{cfg}/d"] = {"metrics": m, "decisions": {r["id"]: [c["name"] for c in r["calls"]] for r in rs[0]["rows"]}}
    for cat, c in m["per_category"].items(): dl.append(f"| {cfg} | {cat} | {c['n']} | {c['first_tool_ok']}/{c['n']} | {c['any_expected']}/{c['n']} | {c['wrong_first'] or '-'} |")
print("\n".join(dl))
for f in sorted(glob.glob(str(HERE / "results" / "latency_158_*.json"))):
    summary[Path(f).stem] = json.load(open(f, encoding="utf-8"))
    if "decision" in f:
        for v in summary[Path(f).stem].values(): v.pop("rows", None)
json.dump(summary, open(HERE / "results" / "summary_v2.json", "w", encoding="utf-8"), ensure_ascii=False, indent=1)
(HERE / "results" / "summary_v2.md").write_text("\n".join(lines) + "\n\n" + "\n".join(dl) + "\n", encoding="utf-8")
