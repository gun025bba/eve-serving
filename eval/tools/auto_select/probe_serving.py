#!/usr/bin/env python3
"""Serving probes for the auto tool selection design (docs/architecture/auto-tool-selection.md §11).
One request at a time against the model server (default 127.0.0.1:8080); refuses the gateway port. Synthetic text only.

  toolchoice   does mlx_lm.server honour tool_choice ("none", forced function)?
  stream       streamed tool calls: shape of the delta, text before the call
  thinking     chat_template_kwargs enable_thinking=false vs /no_think vs default (tokens, reasoning field, latency)
  prefill      cold prefill time vs tool definition count and system/memory length (usage.prompt_tokens, max_tokens=1)
  bigresult    answer round after a large tool result (0 / 6000 / 12000 / 24000 chars = 0 / ~4.1k / ~8.3k / ~16.6k Qwen3 tokens for the synthetic Korean filler): time to first token and total
Usage: python probe_serving.py <probe> [--out results/probe_<probe>.json]
"""
import argparse, json, random, statistics, sys, time, urllib.request
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1])); sys.path.insert(0, str(HERE.parent))
from run_eval import refuse_gateway  # noqa: E402
from run_tools import post, parse_calls, strip_think  # noqa: E402
from run_auto_select import load_defs, build_system  # noqa: E402

MODEL = "mlx-community/Qwen3-30B-A3B-4bit"
SYSTEM = None


def system():
    global SYSTEM
    if SYSTEM is None:
        SYSTEM = build_system("web_on", (HERE / "gateway_tools_system.txt").read_text(encoding="utf-8"), None)
    return SYSTEM


def req(base, body):
    t0 = time.time(); r = post(base, body, ""); return r, round(time.time() - t0, 2)


def stream(base, body):
    """returns (first_chunk_s, first_content_or_tool_s, total_s, chunks)"""
    body = dict(body, stream=True)
    rq = urllib.request.Request(base.rstrip("/") + "/v1/chat/completions", json.dumps(body).encode(), {"Content-Type": "application/json"})
    t0 = time.time(); first = first_out = None; chunks = []
    with urllib.request.urlopen(rq, timeout=900) as r:
        for line in r:
            line = line.decode("utf-8").strip()
            if not line.startswith("data:"): continue
            d = line[5:].strip()
            if d == "[DONE]": break
            now = time.time() - t0
            j = json.loads(d); chunks.append(j)
            if first is None: first = now
            dl = (j.get("choices") or [{}])[0].get("delta") or {}
            if first_out is None and (dl.get("content") or dl.get("tool_calls")): first_out = now
    return round(first or 0, 2), round(first_out or 0, 2), round(time.time() - t0, 2), chunks


def med(v): return round(statistics.median(v), 2) if v else None


def p_toolchoice(base):
    pos = "오늘 카카오 소식 좀 알려줘"; neg = "안녕! 간단히 자기소개 해줘"
    out = {}
    for name, tc in [("auto", "auto"), ("none", "none"), ("forced_web_search", {"type": "function", "function": {"name": "web_search"}}),
                     ("required", "required")]:
        for lab, q in [("pos", pos), ("neg", neg)]:
            res = []
            for _ in range(5):
                body = {"model": MODEL, "messages": [{"role": "system", "content": system()}, {"role": "user", "content": q + " /no_think"}],
                        "tools": load_defs(""), "tool_choice": tc, "temperature": 0.7, "top_p": 0.9, "max_tokens": 512, "stream": False}
                try:
                    r, dt = req(base, body); m = r["choices"][0]["message"]; res.append(bool(parse_calls(m) and any(c["name"] == "web_search" for c in parse_calls(m))))
                except Exception as e: res.append("error:" + repr(e)[:60])
            out[f"{name}/{lab}"] = {"search_called": f"{sum(1 for x in res if x is True)}/{len(res)}", "errors": [x for x in res if isinstance(x, str)][:1]}
    return out


def p_stream(base):
    qs = ["오늘 카카오 소식 좀 알려줘", "서울 날씨 지금 어때?", "이번 주 코스피 흐름 알려줘", "파이썬 최신 버전이 뭐야?", "어제 프로야구 결과 알려줘"]
    rows = []
    for q in qs:
        body = {"model": MODEL, "messages": [{"role": "system", "content": system()}, {"role": "user", "content": q + " /no_think"}],
                "tools": load_defs(""), "tool_choice": "auto", "temperature": 0.7, "top_p": 0.9, "max_tokens": 512}
        f, fo, tot, ch = stream(base, body)
        tcs = [(c["choices"][0]["delta"].get("tool_calls") or []) for c in ch if c.get("choices")]
        n_tc_chunks = sum(1 for t in tcs if t)
        pre_text = "".join((c["choices"][0]["delta"].get("content") or "") for c in ch if c.get("choices"))
        fin = [c["choices"][0].get("finish_reason") for c in ch if c.get("choices") and c["choices"][0].get("finish_reason")]
        first_tc = next((t[0] for t in tcs if t), None)
        rows.append({"first_chunk_s": f, "first_output_s": fo, "total_s": tot, "tool_call_chunks": n_tc_chunks, "text_before_call_chars": len(pre_text),
                     "finish_reason": fin[-1] if fin else None, "args_is_complete_json_string": bool(first_tc) and _is_json((first_tc.get("function") or {}).get("arguments")),
                     "call_has_id_and_index": bool(first_tc) and bool(first_tc.get("id")) and "index" in first_tc, "n_chunks": len(ch)})
    return rows


def _is_json(s):
    try: json.loads(s); return True
    except Exception: return False


def p_thinking(base):
    qs = ["오늘 카카오 소식 좀 알려줘", "서울 날씨 지금 어때?", "파이썬에서 리스트 컴프리헨션이 뭐야?", "안녕! 오늘 기분 어때?", "비트코인 지금 얼마야?", "피보나치 수열 설명해줘"]
    out = {}
    for mode in ("default", "no_think_suffix", "kwargs_false"):
        rows = []
        for q in qs:
            u = q + (" /no_think" if mode == "no_think_suffix" else "")
            body = {"model": MODEL, "messages": [{"role": "system", "content": system()}, {"role": "user", "content": u}], "tools": load_defs(""),
                    "tool_choice": "auto", "temperature": 0.7, "top_p": 0.9, "max_tokens": 4096, "stream": False}
            if mode == "kwargs_false": body["chat_template_kwargs"] = {"enable_thinking": False}
            r, dt = req(base, body); m = r["choices"][0]["message"]
            rows.append({"latency_s": dt, "completion_tokens": r["usage"]["completion_tokens"], "reasoning_chars": len(m.get("reasoning") or ""),
                         "think_tag_in_content": "<think>" in (m.get("content") or ""), "search_called": any(c["name"] == "web_search" for c in parse_calls(m))})
        out[mode] = {"latency_median_s": med([x["latency_s"] for x in rows]), "completion_tokens_median": med([x["completion_tokens"] for x in rows]),
                     "reasoning_nonempty": f"{sum(1 for x in rows if x['reasoning_chars'] > 0)}/{len(rows)}", "rows": rows}
    return out


def dummy_tool(i):
    names = ["calendar_list_events", "calendar_search", "mail_search", "mail_read", "notes_search", "contacts_lookup", "weather_now", "stock_quote", "maps_route", "unit_convert",
             "translate_text", "file_search", "task_list", "reminder_list", "music_search", "news_headlines", "currency_rate", "sports_scores", "timezone_now", "dictionary_lookup"]
    n = names[i % len(names)] + (f"_{i // len(names)}" if i >= len(names) else "")
    return {"type": "function", "function": {"name": n, "description": f"Read-only lookup tool {n}. Returns a short list of matching items for the user.",
            "parameters": {"type": "object", "properties": {"query": {"type": "string", "maxLength": 256}, "start": {"type": "string", "description": "ISO date"},
                           "end": {"type": "string", "description": "ISO date"}, "limit": {"type": "integer", "minimum": 1, "maximum": 20}}, "required": ["query"]}}}


FILLER = ("사용자는 평소 아침에 산책을 하고 커피를 마시며 하루 일정을 확인한다. 이번 달에는 프로젝트 문서를 정리하고 주간 회의를 준비해야 한다. "
          "참고 자료는 여러 출처에서 모은 요약이며 표현이 다소 겹칠 수 있다. 항목마다 번호와 짧은 설명이 붙어 있다. ")


def filler(n_chars, seed):
    rnd = random.Random(seed); out = []; size = 0
    while size < n_chars:
        s = FILLER + f"항목 {rnd.randint(1000, 9999)}: 구분값 {rnd.random():.6f}. "
        out.append(s); size += len(s)
    return "".join(out)[:n_chars]


def p_prefill(base):
    web = load_defs("")
    configs = [("0 tools", []), ("2 tools (web_search, web_fetch)", web), ("5 tools", web + [dummy_tool(i) for i in range(3)]),
               ("10 tools", web + [dummy_tool(i) for i in range(8)]), ("20 tools", web + [dummy_tool(i) for i in range(18)])]
    out = {"cold_by_tool_count": [], "cold_by_system_len": [], "cache": {}}
    def one(tools, sys_text, nonce, user="안녕"):
        body = {"model": MODEL, "messages": [{"role": "system", "content": f"[{nonce}] " + sys_text}, {"role": "user", "content": user + " /no_think"}],
                "temperature": 0, "max_tokens": 1, "stream": False}
        if tools: body["tools"] = tools
        r, dt = req(base, body); return r["usage"]["prompt_tokens"], dt
    for label, tools in configs:
        reps = [one(tools, system(), f"n{random.random():.9f}") for _ in range(3)]
        out["cold_by_tool_count"].append({"config": label, "prompt_tokens": reps[0][0], "latency_s": [x[1] for x in reps], "median_s": med([x[1] for x in reps])})
    for extra in (0, 3000, 9000, 18000):  # chars of memory-like text appended to the system prompt
        sys_text = system() + ("\n\n" + filler(extra, extra) if extra else "")
        reps = [one(web, sys_text, f"n{random.random():.9f}") for _ in range(3)]
        out["cold_by_system_len"].append({"memory_chars": extra, "prompt_tokens": reps[0][0], "latency_s": [x[1] for x in reps], "median_s": med([x[1] for x in reps])})
    # prompt cache: same system, tools 2 -> repeat exact (hit); then same system, 5 tools (system prefix hit, tools tail miss); then back
    s = system() + "\n\n" + filler(9000, 1); nonce = f"c{random.random():.9f}"
    t2 = web; t5 = web + [dummy_tool(i) for i in range(3)]
    seq = [("cold, 2 tools", t2), ("repeat, 2 tools (full prefix hit)", t2), ("same system, 5 tools (tool block changed)", t5), ("back to 2 tools", t2), ("repeat 2 tools", t2)]
    out["cache"] = [{"step": lab, "prompt_tokens": (r := one(t, s, nonce))[0], "latency_s": r[1]} for lab, t in seq]
    # user turn change only, tools same, long shared prefix (typical chat turn): second turn with same prefix
    return out


def p_bigresult(base):
    web = load_defs(""); out = []
    for n_chars in (0, 6000, 12000, 24000):
        ttfts = []; toks = None
        for rep in range(2):
            tool_text = "<untrusted_web_content tool=\"web_search\" source=\"x\">\n" + filler(n_chars, 100 + rep) + "\n</untrusted_web_content>"
            msgs = [{"role": "system", "content": f"[n{random.random():.9f}] " + system()}, {"role": "user", "content": "오늘 카카오 소식 좀 알려줘 /no_think"},
                    {"role": "assistant", "content": "", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "web_search", "arguments": json.dumps({"query": "카카오 오늘 소식", "time_range": "day"}, ensure_ascii=False)}}]},
                    {"role": "tool", "tool_call_id": "call_1", "content": tool_text}]
            body = {"model": MODEL, "messages": msgs, "tools": web, "temperature": 0.7, "top_p": 0.9, "max_tokens": 32}
            f, fo, tot, ch = stream(base, body)
            ttfts.append((f, fo, tot))
            u = [c.get("usage") for c in ch if c.get("usage")]
            toks = (u[-1] or {}).get("prompt_tokens") if u else toks
        out.append({"tool_result_chars": n_chars, "prompt_tokens": toks, "first_chunk_s": [t[0] for t in ttfts], "first_output_s": [t[1] for t in ttfts], "median_first_chunk_s": med([t[0] for t in ttfts])})
    return out


def main():
    ap = argparse.ArgumentParser(); ap.add_argument("probe", choices=["toolchoice", "stream", "thinking", "prefill", "bigresult"])
    ap.add_argument("--base", default="http://127.0.0.1:8080"); ap.add_argument("--out", default="")
    a = ap.parse_args(); refuse_gateway(a.base)
    res = {"toolchoice": p_toolchoice, "stream": p_stream, "thinking": p_thinking, "prefill": p_prefill, "bigresult": p_bigresult}[a.probe](a.base)
    out = a.out or str(HERE / "results" / f"probe_{a.probe}.json")
    Path(out).parent.mkdir(exist_ok=True); json.dump(res, open(out, "w", encoding="utf-8"), ensure_ascii=False, indent=1)
    print(json.dumps(res, ensure_ascii=False, indent=1)[:6000])


if __name__ == "__main__":
    main()
