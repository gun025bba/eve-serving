#!/usr/bin/env python3
"""Prompt/latency re-measurement for the automatic tool selection default (PR #158, 6.0.0).

Compares the 5.x request shapes with the #158 shape on the model server (default 127.0.0.1:8080), one request at a time,
refusing the gateway port. #158 shape = Eve v3b prompt (web on) + the fixed gateway text of #158 (tools.SystemPrompt, copied to
gateway_tools_system_158.txt from the #158 head, a6f8440) + the date line + (for fresh-looking questions) the Gate hint, with the
2 web tool definitions in round 1 and only web_fetch in round 2 (Session.Expose: search only in round 1).
Synthetic text only.

  prefill   cold prefill (nonce in front of the system text, max_tokens=1) per request shape, and the system-prompt token delta
  decision  warm-cache decision latency (time to first tool call or first answer token) on a fixed item sample, thinking on and off
  round2    answer round after a web_search result: first-token time for 3000/6000/12000 result chars, with the round-1 prefix
            cached, tool block unchanged (all tools) vs as #158 sends it (web_fetch only)
Usage: python probe_latency_158.py <probe> [--out results/latency_158_<probe>.json]
"""
import argparse, json, random, statistics, sys, time, urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1])); sys.path.insert(0, str(HERE.parent))
from run_eval import refuse_gateway, render_template  # noqa: E402
from run_tools import post, parse_calls  # noqa: E402
from run_auto_select import load_defs, PROMPT  # noqa: E402

MODEL = "mlx-community/Qwen3-30B-A3B-4bit"
KST_DAY = "2006-01-02 (Mon)"
GATE_HINT = "\nThe latest user message may depend on current information: prefer calling web_search before answering."


def eve(web):
    return render_template(PROMPT.read_text(encoding="utf-8").strip(), str(PROMPT), web, "")


def date_line():
    import datetime as dt
    d = dt.datetime.now(dt.timezone.utc) + dt.timedelta(hours=9)
    return "\nToday is " + d.strftime("%Y-%m-%d (%a)") + " in Asia/Seoul (UTC+9). Your training data may be out of date."


def sys158(fresh=False):
    return eve("on") + "\n\n" + (HERE / "gateway_tools_system_158.txt").read_text(encoding="utf-8").strip() + date_line() + (GATE_HINT if fresh else "")


def sys5_on():  # 5.x web button on: v3b web-on section + the old 3-line gateway tools text
    return eve("on") + "\n\n" + (HERE / "gateway_tools_system.txt").read_text(encoding="utf-8").strip()


def sys5_off():
    return eve("off")


def stream(base, body):
    """(first_chunk_s, first_reasoning_s, first_content_or_call_s, total_s, usage, n_reasoning_chunks, called_tool)"""
    body = dict(body, stream=True, stream_options={"include_usage": True})
    rq = urllib.request.Request(base.rstrip("/") + "/v1/chat/completions", json.dumps(body).encode(), {"Content-Type": "application/json"})
    t0 = time.time(); first = fr = fo = None; usage = None; nr = 0; called = False
    with urllib.request.urlopen(rq, timeout=900) as r:
        for line in r:
            line = line.decode("utf-8").strip()
            if not line.startswith("data:"): continue
            d = line[5:].strip()
            if d == "[DONE]": break
            now = time.time() - t0
            j = json.loads(d)
            if j.get("usage"): usage = j["usage"]
            if first is None: first = now
            dl = (j.get("choices") or [{}])[0].get("delta") or {}
            if dl.get("reasoning") or dl.get("reasoning_content"):
                nr += 1
                if fr is None: fr = now
            if dl.get("tool_calls"): called = True
            if fo is None and (dl.get("content") or dl.get("tool_calls")): fo = now
    return {"first_chunk_s": round(first or 0, 2), "first_reasoning_s": round(fr, 2) if fr else None, "first_output_s": round(fo or 0, 2),
            "total_s": round(time.time() - t0, 2), "usage": usage, "reasoning_chunks": nr, "tool_call": called}


def med(v): v = [x for x in v if x is not None]; return round(statistics.median(v), 2) if v else None
def p95(v): v = sorted(x for x in v if x is not None); return round(v[max(0, int(round(0.95 * len(v))) - 1)], 2) if v else None
def nonce(): return f"[n{random.random():.9f}] "


def msgs_for(system, user, think):
    return [{"role": "system", "content": system}, {"role": "user", "content": user + (" /no_think" if think == "suffix" else "")}]


def p_prefill(base):
    web = load_defs(""); only_fetch = [d for d in web if d["function"]["name"] == "web_fetch"]
    filler = lambda n: ("사용자는 평소 아침에 산책을 하고 커피를 마시며 하루 일정을 확인한다. 이번 달에는 프로젝트 문서를 정리해야 한다. " * 200)[:n]
    cfgs = [("5.x web off (default today): Eve web-off prompt, no tools", sys5_off(), []),
            ("5.x web on (button): Eve web-on + 3-line tools text + 2 tools", sys5_on(), web),
            ("#158 round 1: Eve web-on + #158 text + date + 2 tools", sys158(False), web),
            ("#158 round 1, fresh question (+ Gate hint)", sys158(True), web),
            ("#158 round 2: same text, tools = web_fetch only", sys158(False), only_fetch),
            ("#158 round 1 + memory block 1500 chars", sys158(False) + "\n\n" + filler(1500), web),
            ("#158 round 1 + memory block 3000 chars", sys158(False) + "\n\n" + filler(3000), web)]
    out = []
    for label, s, tools in cfgs:
        reps = []
        for _ in range(5):
            body = {"model": MODEL, "messages": msgs_for(nonce() + s, "안녕", "suffix"), "temperature": 0, "max_tokens": 1}
            if tools: body["tools"] = tools
            t0 = time.time(); r = post(base, dict(body, stream=False), ""); reps.append((r["usage"]["prompt_tokens"], round(time.time() - t0, 2)))
        out.append({"config": label, "prompt_tokens": reps[0][0], "latency_s": [x[1] for x in reps], "median_s": med([x[1] for x in reps])})
        print(out[-1], flush=True)
    return out


def sample_items(n_each=15):
    its = [json.loads(l) for l in open(HERE / "items.jsonl", encoding="utf-8") if l.strip()]
    rnd = random.Random(158)
    pos = [i for i in its if i["expected_tool"] == "web_search" and not i.get("history")]
    neg = [i for i in its if i["expected_tool"] == "none" and not i.get("history")]
    return rnd.sample(pos, n_each) + rnd.sample(neg, n_each)


def gate(text):
    import re
    return bool(re.search(r"\b(today|tonight|yesterday|right now|these days|recent(ly)?|this week|latest|current(ly)?|weather|news|real[- ]?time|this year|20(2[5-9]|[3-9]\d))\b|오늘|어제|지금|최근|요즘|이번\s*주|최신|현재|시세|날씨|뉴스|소식|실시간|올해|20(2[5-9]|[3-9]\d)\s*년", text.strip(), re.I))


def p_decision(base, reps=2):
    web = load_defs(""); items = sample_items()
    shapes = [("5.x web off (no tools)", lambda q: (sys5_off(), None)),
              ("#158 (tools, auto)", lambda q: (sys158(gate(q)), web))]
    out = {}
    for think in ("on", "kwargs"):
        for label, f in shapes:
            rows = []
            for rep in range(reps):
                for it in items:
                    s, tools = f(it["query"])
                    body = {"model": MODEL, "messages": msgs_for(s, it["query"], None), "temperature": 0.7, "top_p": 0.9, "max_tokens": 8192}
                    if tools: body["tools"] = tools; body["tool_choice"] = "auto"
                    if think == "kwargs": body["chat_template_kwargs"] = {"enable_thinking": False}
                    try: r = stream(base, body)
                    except Exception as e: r = {"error": repr(e)}
                    r["id"] = it["id"]; r["expected"] = it["expected_tool"]; rows.append(r)
            ok = [r for r in rows if "error" not in r]
            key = f"{label} | thinking {'on' if think == 'on' else 'off(kwargs)'}"
            def agg(sel):
                v = [r["first_output_s"] for r in ok if sel(r)]
                return {"n": len(v), "median_s": med(v), "p95_s": p95(v), "mean_s": round(statistics.mean(v), 2) if v else None}
            out[key] = {"all": agg(lambda r: True), "positive": agg(lambda r: r["expected"] == "web_search"), "negative": agg(lambda r: r["expected"] == "none"),
                        "total_all": {"median_s": med([r["total_s"] for r in ok]), "p95_s": p95([r["total_s"] for r in ok])},
                        "first_chunk_median_s": med([r["first_chunk_s"] for r in ok]),
                        "first_reasoning_median_s": med([r["first_reasoning_s"] for r in ok]),
                        "completion_tokens_median": med([(r.get("usage") or {}).get("completion_tokens") for r in ok]),
                        "tool_call_rate_pos": round(sum(r["tool_call"] for r in ok if r["expected"] == "web_search") / max(1, sum(1 for r in ok if r["expected"] == "web_search")), 3),
                        "tool_call_rate_neg": round(sum(r["tool_call"] for r in ok if r["expected"] == "none") / max(1, sum(1 for r in ok if r["expected"] == "none")), 3),
                        "errors": len(rows) - len(ok), "rows": rows}
            print(key, {k: out[key][k] for k in ("all", "positive", "negative")}, flush=True)
    return out


def results_text(n_chars, seed):
    rnd = random.Random(seed); parts = []; size = 0; i = 0
    while size < n_chars:
        i += 1
        s = f"[{(i - 1) % 5 + 1}] 제목 {rnd.randint(100, 999)}\nURL: https://example.org/a/{rnd.randint(1000, 9999)}\n요약: 이번 달 프로젝트 문서를 정리하고 주간 회의를 준비한다. 참고 자료는 여러 출처에서 모은 요약이며 항목마다 번호가 붙어 있다. 구분값 {rnd.random():.6f}.\n\n"
        parts.append(s); size += len(s)
    return "".join(parts)[:n_chars]


def p_round2(base, reps=3):
    web = load_defs(""); only_fetch = [d for d in web if d["function"]["name"] == "web_fetch"]
    q = "오늘 카카오 소식 좀 알려줘"; out = []
    for think in ("kwargs", "on"):
        for n_chars in (3000, 6000, 12000):
            for tools_label, round2_tools in (("tools unchanged (search+fetch)", web), ("as #158 (web_fetch only)", only_fetch)):
                rows = []
                for rep in range(reps):
                    s = nonce() + sys158(True)
                    base_kw = {"chat_template_kwargs": {"enable_thinking": False}} if think == "kwargs" else {}
                    # round 1: the decision request (warms the prefix); max_tokens small, its output is replaced by a fixed call
                    r1 = {"model": MODEL, "messages": msgs_for(s, q, None), "tools": web, "temperature": 0, "max_tokens": 1, **base_kw}
                    post(base, dict(r1, stream=False), "")
                    call = {"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "web_search", "arguments": json.dumps({"query": "카카오 오늘 소식", "time_range": "day"}, ensure_ascii=False)}}]}
                    tool = {"role": "tool", "tool_call_id": "call_1", "content": "<untrusted_web_content tool=\"web_search\" source=\"1\">\n" + results_text(n_chars, 7 + rep) + "\n</untrusted_web_content>"}
                    r2 = {"model": MODEL, "messages": msgs_for(s, q, None) + [call, tool], "tools": round2_tools, "temperature": 0.7, "top_p": 0.9, "max_tokens": 24 if think == "kwargs" else 4, **base_kw}
                    rows.append(stream(base, r2))
                rec = {"thinking": "on" if think == "on" else "off(kwargs)", "result_chars": n_chars, "round2_tools": tools_label, "prompt_tokens": (rows[0]["usage"] or {}).get("prompt_tokens"),
                       "first_chunk_s": [r["first_chunk_s"] for r in rows], "median_first_chunk_s": med([r["first_chunk_s"] for r in rows]),
                       "first_output_s": [r["first_output_s"] for r in rows], "median_first_output_s": med([r["first_output_s"] for r in rows])}
                out.append(rec); print(rec, flush=True)
    return out


def main():
    ap = argparse.ArgumentParser(); ap.add_argument("probe", choices=["prefill", "decision", "round2"])
    ap.add_argument("--base", default="http://127.0.0.1:8080"); ap.add_argument("--out", default="")
    a = ap.parse_args(); refuse_gateway(a.base)
    res = {"prefill": p_prefill, "decision": p_decision, "round2": p_round2}[a.probe](a.base)
    out = a.out or str(HERE / "results" / f"latency_158_{a.probe}.json")
    Path(out).parent.mkdir(exist_ok=True); json.dump(res, open(out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)


if __name__ == "__main__":
    main()
