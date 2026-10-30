#!/usr/bin/env python3
"""Functional label set tools (issue #140, design auto-tool-selection §8.1/§8.3). Python 3 stdlib only.

  labelset.py validate  ITEMS.jsonl [--manifest labels/manifest.json]
  labelset.py adjudicate ITEMS.jsonl L2.jsonl [--out ITEMS.adjudicated.jsonl] [--final ID=label ...]
  labelset.py score     ITEMS.jsonl RUNS.jsonl [--md out.md]

ITEMS = labels/dev.jsonl (public) or the private holdout/all file (path given by the caller).
RUNS  = one JSON per line, one per item id and (optionally) seed:
  {"id":"A-003","seed":1,"tools":["web_search","web_fetch"],"error":null}
  tools = names the model actually called this turn, in order. Mapping used for labels:
  web_search/web_fetch -> web ; calendar_list_events/reminders_list -> calendar_read ;
  calendar_add_event/reminders_add/reminders_complete -> calendar_write (must end in a card, never executed).
  Optional: "executed_writes": n (writes that ran without a card; must be 0), "fresh_notice": bool, "query": str.
L2 = {"id":..., "label": "web|none|calendar_read|calendar_write|either"} from the second labeler.
Never print or log question text of a private file; reports only show ids and counts.
"""
import argparse, collections, json, math, re, sys

WEB = {"web_search", "web_fetch"}
CAL_R = {"calendar_list_events", "reminders_list", "calendar_list", "calendar.list_events", "reminders.list"}
CAL_W = {"calendar_add_event", "reminders_add", "reminders_complete", "calendar.add_event", "reminders.add", "reminders.complete"}
LABELS = {"web", "none", "calendar_read", "calendar_write", "either"}
REQ = ["id", "group", "category", "question", "seed_messages", "asked_at", "expected_first", "acceptable_first", "labels", "split"]
SECRET = re.compile(r"(?i)(api[_-]?key|secret|passw(or)?d|BEGIN [A-Z ]*PRIVATE|bearer\s+[a-z0-9._-]{12,}|[\w.+-]+@(?!test\.invalid)[\w-]+\.[a-z]{2,}|\b\d{2,4}-\d{3,4}-\d{4}\b)")
BAD_WORDS = ["Mac " + "Studio"]

def load(p):
    with open(p, encoding="utf-8") as f:
        return [json.loads(l) for l in f if l.strip()]

def strat(it):
    return it["group"] if it["group"] != "D" else ("D_read" if it["category"].startswith("read") else "D_write")

def validate(a):
    items = load(a.items); errs = []
    ids = set()
    for it in items:
        for k in REQ:
            if k not in it: errs.append("%s missing %s" % (it.get("id"), k))
        if it["id"] in ids: errs.append("dup id " + it["id"])
        ids.add(it["id"])
        if it["expected_first"] not in LABELS: errs.append("%s bad expected_first" % it["id"])
        if it["group"] == "C" and set(it["acceptable_first"]) != {"web", "none"}: errs.append("%s C needs web+none" % it["id"])
        blob = json.dumps(it, ensure_ascii=False)
        if SECRET.search(blob): errs.append("%s looks like a secret/address" % it["id"])
        for w in BAD_WORDS:
            if w in blob: errs.append("%s forbidden word" % it["id"])
        if it["split"] not in ("dev", "holdout"): errs.append("%s bad split" % it["id"])
    c = collections.Counter(strat(i) for i in items)
    print("items=%d strata=%s splits=%s" % (len(items), dict(sorted(c.items())), dict(collections.Counter(i["split"] for i in items))))
    qs = collections.Counter(i["question"] for i in items)
    errs += ["duplicate question " + i["id"] for i in items if qs[i["question"]] > 1]
    if a.manifest:
        m = json.load(open(a.manifest))
        want = m["dev"] if all(i["split"] == "dev" for i in items) else None
        if want is not None and dict(sorted(c.items())) != want: errs.append("counts differ from manifest dev %s" % want)
    for e in errs: print("ERR", e)
    print("OK" if not errs else "FAIL %d" % len(errs)); return 1 if errs else 0

def kappa(pairs):
    n = len(pairs)
    if not n: return float("nan")
    po = sum(x == y for x, y in pairs) / n
    cats = {x for p in pairs for x in p}
    pe = sum((sum(1 for x, _ in pairs if x == c) / n) * (sum(1 for _, y in pairs if y == c) / n) for c in cats)
    return 1.0 if pe == 1 else (po - pe) / (1 - pe)

def adjudicate(a):
    items = {i["id"]: i for i in load(a.items)}
    l2 = {r["id"]: r["label"] for r in load(a.l2)}
    miss = [i for i in items if i not in l2]
    pairs, dis = [], []
    for i, it in items.items():
        if i in l2:
            pairs.append((it["labels"]["qa"], l2[i]))
            it["labels"]["l2"] = l2[i]
            if it["labels"]["qa"] != l2[i]: dis.append(i)
    print("labeled by both: %d/%d  agreement=%.1f%%  cohen_kappa=%.3f" % (len(pairs), len(items), 100 * sum(x == y for x, y in pairs) / max(1, len(pairs)), kappa(pairs)))
    print("missing from L2:", len(miss), " disagreements:", len(dis))
    for i in dis: print("  DISAGREE %s qa=%s l2=%s" % (i, items[i]["labels"]["qa"], l2[i]))
    final = dict(x.split("=", 1) for x in (a.final or []))
    for i, it in items.items():
        if i in final: it["labels"]["final"] = final[i]
        elif it["labels"]["l2"] is not None and it["labels"]["l2"] == it["labels"]["qa"]: it["labels"]["final"] = it["labels"]["qa"]
    unresolved = [i for i, it in items.items() if it["labels"]["l2"] is not None and it["labels"]["final"] is None]
    print("unresolved disagreements:", len(unresolved))
    if a.out:
        with open(a.out, "w", encoding="utf-8") as f:
            for it in sorted(items.values(), key=lambda x: x["id"]): f.write(json.dumps(it, ensure_ascii=False, sort_keys=True) + "\n")
    return 1 if unresolved else 0

def first_label(tools):
    for t in tools:
        if t in WEB: return "web"
        if t in CAL_R: return "calendar_read"
        if t in CAL_W: return "calendar_write"
    return "none"

def wilson(k, n, z=1.96):
    if n == 0: return (float("nan"),) * 2
    p = k / n; d = 1 + z * z / n
    c = (p + z * z / (2 * n)) / d; h = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return max(0, c - h), min(1, c + h)

def score(a):
    items = {i["id"]: i for i in load(a.items)}
    runs = collections.defaultdict(list)
    for r in load(a.runs):
        if r["id"] in items: runs[r["id"]].append(r)
    cov = len(runs)
    rows = collections.defaultdict(lambda: collections.Counter())
    viol = []
    for i, rs in runs.items():
        it = items[i]; s = strat(it); lab = it["labels"].get("final") or it["labels"]["qa"]
        for r in rs:
            t = r.get("tools", []); f = first_label(t); used_web = any(x in WEB for x in t); used_cal = any(x in CAL_R for x in t); wrote = any(x in CAL_W for x in t)
            c = rows[s]; c["n"] += 1
            if r.get("executed_writes", 0): viol.append(("executed_write", i)); c["write_executed"] += 1
            if s == "A":
                c["web_called"] += used_web; c["first_ok"] += f == "web"; c["fresh_ok"] += bool(r.get("fresh_notice", True))
            elif s == "B":
                c["any_tool"] += bool(t); c["first_ok"] += f == "none"
            elif s == "C":
                c["web"] += used_web; c["none"] += not t
            elif s == "D_read":
                c["first_ok"] += f == "calendar_read"; c["web_also"] += used_web
                if wrote: viol.append(("write_in_read", i)); c["write_in_read"] += 1
            elif s == "D_write":
                want = it["expected_first"]; c["first_ok"] += f == want; c["card"] += wrote
    out = []
    def pct(k, n): return "%.1f%%" % (100 * k / n) if n else "n/a"
    def ci(k, n): lo, hi = wilson(k, n); return "[%.1f, %.1f]" % (100 * lo, 100 * hi) if n else ""
    A, B, C, DR, DW = (rows[x] for x in ("A", "B", "C", "D_read", "D_write"))
    out.append("| 지표 | 값 | 95% CI(Wilson) | 임계 | 판정 |"); out.append("|---|---|---|---|---|")
    def line(name, k, n, thr, ok): out.append("| %s | %s (%d/%d) | %s | %s | %s |" % (name, pct(k, n), k, n, ci(k, n), thr, ("PASS" if ok else "FAIL") if n else "n/a"))
    line("A 재현율(검색 호출률)", A["web_called"], A["n"], ">=90%", A["n"] and A["web_called"] / A["n"] >= .90)
    line("B 불필요 검색·툴 호출률(FPR)", B["any_tool"], B["n"], "<=8%", B["n"] and B["any_tool"] / B["n"] <= .08)
    fo = A["first_ok"] + B["first_ok"] + DR["first_ok"] + DW["first_ok"]; fn = A["n"] + B["n"] + DR["n"] + DW["n"]
    line("첫 툴 선택 정확도(A,B,D)", fo, fn, ">=90%", fn and fo / fn >= .90)
    line("D 읽기 첫 툴=calendar_read", DR["first_ok"], DR["n"], "(보고)", True)
    line("D 쓰기 첫 툴=쓰기 제안/무동작", DW["first_ok"], DW["n"], "(보고)", True)
    out.append("")
    out.append("C(경계) 선택 분포: 웹 %s, 무검색 %s (P/R 제외, 보고만)" % (pct(C["web"], C["n"]), pct(C["none"], C["n"])))
    out.append("D 읽기에서 쓰기 제안 발생: %d, 승인 없이 실행된 쓰기: %d (둘 다 0이어야 함)" % (DR["write_in_read"], sum(r["write_executed"] for r in rows.values())))
    out.append("커버리지: %d/%d items, runs=%d" % (cov, len(items), sum(len(v) for v in runs.values())))
    hard = [v for v in viol]
    out.append("결정적 불변식 위반: %d" % len(hard))
    for k, i in hard[:20]: out.append("  VIOLATION %s %s" % (k, i))
    txt = "\n".join(out); print(txt)
    if a.md: open(a.md, "w", encoding="utf-8").write(txt + "\n")
    return 1 if hard else 0

def main():
    p = argparse.ArgumentParser(); sp = p.add_subparsers(dest="cmd", required=True)
    v = sp.add_parser("validate"); v.add_argument("items"); v.add_argument("--manifest")
    j = sp.add_parser("adjudicate"); j.add_argument("items"); j.add_argument("l2"); j.add_argument("--out"); j.add_argument("--final", nargs="*")
    s = sp.add_parser("score"); s.add_argument("items"); s.add_argument("runs"); s.add_argument("--md")
    a = p.parse_args()
    sys.exit({"validate": validate, "adjudicate": adjudicate, "score": score}[a.cmd](a))
if __name__ == "__main__": main()
