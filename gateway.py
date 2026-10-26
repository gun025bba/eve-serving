"""Local LLM gateway: auth, queue, metrics, structured logs in front of mlx_lm.server."""
import asyncio, json, logging, os, time, uuid, collections, statistics
import httpx, psutil, yaml
from fastapi import FastAPI, Request, Depends, Body
from fastapi.responses import StreamingResponse, JSONResponse, Response
from fastapi.security import HTTPBearer, HTTPAuthorizationCredentials
from prometheus_client import Counter, Histogram, Gauge, generate_latest, CONTENT_TYPE_LATEST

BASE = os.path.expanduser("~/llm-serving")
def load_cfg():
    c = yaml.safe_load(open(os.path.join(BASE, "config.yaml"))) or {}
    for k, v in list(c.items()):
        e = os.getenv("GW_" + k.upper())
        if e is not None and not isinstance(v, (dict, list)):
            c[k] = (float(e) if isinstance(v, (int, float)) and not isinstance(v, bool) else e)
    if os.getenv("GW_CORS_ORIGINS"): c["cors_origins"] = os.getenv("GW_CORS_ORIGINS").split(",")
    return c
CFG = load_cfg()
KEY = open(os.path.join(BASE, ".api_key")).read().strip()
MAX_CONC, MAX_QUEUE = int(CFG["max_conc"]), int(CFG["max_queue"])

# ---- structured JSON logging ----
class JsonFmt(logging.Formatter):
    def format(self, r):
        d = {"ts": time.strftime("%Y-%m-%dT%H:%M:%S%z", time.localtime(r.created)), "level": r.levelname, "msg": r.getMessage()}
        d.update(getattr(r, "extra", {}) or {})
        return json.dumps(d, ensure_ascii=False)
log = logging.getLogger("gateway"); log.propagate = False
_h = logging.FileHandler(os.path.expanduser(CFG["log_file"])); _h.setFormatter(JsonFmt()); log.addHandler(_h)
log.setLevel(CFG.get("log_level", "INFO").upper())
def jlog(level, msg, **kw): log.log(level, msg, extra={"extra": kw})

# ---- metrics ----
B = (.05, .1, .25, .5, 1, 2, 5, 10, 20, 30, 60, 120, 300)
REQS = Counter("llm_requests_total", "Requests by endpoint/status", ["endpoint", "status"])
ERRS = Counter("llm_errors_total", "Errors by code", ["code"])
LAT = Histogram("llm_request_latency_seconds", "Total request latency", ["stream"], buckets=B)
QW = Histogram("llm_queue_wait_seconds", "Time waiting for a slot", buckets=B)
TTFT = Histogram("llm_time_to_first_token_seconds", "Streaming time to first token", buckets=B)
TPS = Histogram("llm_tokens_per_second", "Completion tokens/sec", buckets=(5, 10, 20, 30, 40, 50, 60, 70, 80, 100, 150))
PTOK = Counter("llm_prompt_tokens_total", "Prompt tokens"); CTOK = Counter("llm_completion_tokens_total", "Completion tokens")
INFL = Gauge("llm_inflight_requests", "Requests being generated"); QD = Gauge("llm_queue_depth", "Requests waiting")
MEM_SYS = Gauge("llm_system_memory_bytes", "System memory", ["kind"]); MEM_MLX = Gauge("llm_mlx_server_rss_bytes", "mlx_lm.server RSS")
RECENT = collections.deque(maxlen=int(CFG["stats_window"])); START = time.time()
_mlx_pid = None
def mlx_proc():
    global _mlx_pid
    try:
        if _mlx_pid: return psutil.Process(_mlx_pid)
    except psutil.NoSuchProcess: pass
    for p in psutil.process_iter(["cmdline"]):
        if any("mlx_lm.server" in a for a in (p.info["cmdline"] or [])): _mlx_pid = p.pid; return p
def mem():
    vm = psutil.virtual_memory(); p = mlx_proc(); rss = None
    try: rss = p.memory_info().rss if p else None
    except psutil.Error: pass
    MEM_SYS.labels("total").set(vm.total); MEM_SYS.labels("available").set(vm.available); MEM_SYS.labels("used").set(vm.total - vm.available)
    if rss: MEM_MLX.set(rss)
    return {"total_gb": round(vm.total/1e9, 1), "available_gb": round(vm.available/1e9, 1), "percent": vm.percent, "mlx_server_rss_gb": round(rss/1e9, 2) if rss else None}

# ---- errors ----
class GwError(Exception):
    def __init__(s, status, code, msg, etype="api_error"): s.status, s.code, s.msg, s.etype = status, code, msg, etype
def err_body(code, msg, etype, rid): return {"error": {"message": msg, "type": etype, "code": code, "request_id": rid}}

tags = [{"name": "inference", "description": "OpenAI-compatible endpoints (Bearer auth)"},
        {"name": "ops", "description": "Health, readiness, metrics (no auth)"},
        {"name": "admin", "description": "Runtime admin (Bearer auth)"}]
app = FastAPI(title="Local LLM Gateway", version="2.0",
    description="OpenAI-compatible gateway in front of mlx_lm.server (Qwen3-30B-A3B-4bit). "
    "Auth: `Authorization: Bearer <key>`. Errors use OpenAI format `{error:{message,type,code,request_id}}`. "
    "Codes: invalid_api_key 401, bad_request 400, queue_full/queue_timeout 429, upstream_unavailable 503, upstream_timeout 504, upstream_error 502. "
    "Every response carries `X-Request-ID`.", openapi_tags=tags)
if CFG.get("cors_origins"):
    from fastapi.middleware.cors import CORSMiddleware
    app.add_middleware(CORSMiddleware, allow_origins=CFG["cors_origins"], allow_methods=["GET", "POST", "OPTIONS"],
                       allow_headers=["Authorization", "Content-Type", "X-Request-ID"], expose_headers=["X-Request-ID"])
_sd = os.path.expanduser(CFG.get("static_dir") or "")
if _sd and os.path.isdir(_sd):
    from fastapi.staticfiles import StaticFiles
    app.mount("/ui", StaticFiles(directory=_sd, html=True), name="ui")

client = httpx.AsyncClient(base_url=CFG["upstream"], timeout=httpx.Timeout(float(CFG["read_timeout_s"]), connect=float(CFG["connect_timeout_s"])))
sem = asyncio.Semaphore(MAX_CONC); waiting = 0
bearer = HTTPBearer(auto_error=False, description="API key from ~/llm-serving/.api_key")

@app.middleware("http")
async def reqid(request: Request, call_next):
    rid = request.headers.get("x-request-id") or uuid.uuid4().hex[:12]; request.state.rid = rid
    resp = await call_next(request); resp.headers["X-Request-ID"] = rid; return resp

@app.exception_handler(Exception)
async def any_err(request: Request, e: Exception):
    rid = getattr(request.state, "rid", "-"); ERRS.labels("internal_error").inc()
    jlog(logging.ERROR, "internal_error", request_id=rid, path=request.url.path, error=repr(e)[:300])
    return JSONResponse(err_body("internal_error", "Internal gateway error", "server_error", rid), status_code=500, headers={"X-Request-ID": rid})

@app.exception_handler(GwError)
async def gw_err(request: Request, e: GwError):
    rid = getattr(request.state, "rid", "-"); ERRS.labels(e.code).inc(); REQS.labels(request.url.path, str(e.status)).inc()
    RECENT.append({"t": time.time(), "ok": False, "code": e.code})
    jlog(logging.WARNING, "request_error", request_id=rid, path=request.url.path, status=e.status, code=e.code, detail=e.msg)
    return JSONResponse(err_body(e.code, e.msg, e.etype, rid), status_code=e.status, headers={"X-Request-ID": rid})

def auth(cred: HTTPAuthorizationCredentials = Depends(bearer)):
    if not cred or cred.credentials != KEY:
        raise GwError(401, "invalid_api_key", "Missing or invalid API key. Use 'Authorization: Bearer <key>'.", "authentication_error")

async def upstream_call(method, path, **kw):
    """Retry only connection-level failures (request never reached the model)."""
    for i in range(int(CFG["retries"]) + 1):
        try: return await client.request(method, path, **kw)
        except (httpx.ConnectError, httpx.ConnectTimeout) as e:
            jlog(logging.WARNING, "upstream_connect_retry", attempt=i+1, error=type(e).__name__)
            if i == int(CFG["retries"]): raise GwError(503, "upstream_unavailable", "Model server unreachable (is com.llmserving.mlx running?)", "server_error")
            await asyncio.sleep(float(CFG["retry_backoff_s"]) * (2**i))
        except httpx.TimeoutException: raise GwError(504, "upstream_timeout", f"Model server did not respond within {CFG['read_timeout_s']}s", "server_error")

async def acquire(rid):
    global waiting
    if waiting >= MAX_QUEUE: raise GwError(429, "queue_full", f"Queue full ({MAX_QUEUE} waiting). Retry later.", "rate_limit_error")
    waiting += 1; QD.set(waiting); t0 = time.time()
    try: await asyncio.wait_for(sem.acquire(), float(CFG["queue_timeout_s"]))
    except asyncio.TimeoutError: raise GwError(429, "queue_timeout", f"Waited >{CFG['queue_timeout_s']}s for a slot", "rate_limit_error")
    finally: waiting -= 1; QD.set(waiting)
    INFL.inc(); q = time.time() - t0; QW.observe(q); return q
def release(): INFL.dec(); sem.release()

def record(rid, stream, status, t0, q, pt, ct, gen_s, ttft=None, code=None):
    total = time.time() - t0; tps = (ct / gen_s) if ct and gen_s and gen_s > 0 else None
    LAT.labels(str(stream).lower()).observe(total); REQS.labels("/v1/chat/completions", str(status)).inc()
    if pt: PTOK.inc(pt)
    if ct: CTOK.inc(ct)
    if tps: TPS.observe(tps)
    if ttft is not None: TTFT.observe(ttft)
    if code: ERRS.labels(code).inc()
    RECENT.append({"t": time.time(), "ok": code is None and status < 400, "code": code, "lat": total, "q": q, "tps": tps, "ttft": ttft})
    m = mem()
    jlog(logging.INFO, "chat_completion", request_id=rid, stream=stream, status=status, error_code=code, queue_ms=int(q*1000),
         ttft_ms=int(ttft*1000) if ttft is not None else None, total_ms=int(total*1000), prompt_tokens=pt, completion_tokens=ct,
         tokens_per_s=round(tps, 1) if tps else None, mlx_rss_gb=m["mlx_server_rss_gb"], mem_avail_gb=m["available_gb"], queue_depth=waiting, inflight=int(INFL._value.get()))

EXAMPLE = {"messages": [{"role": "user", "content": "안녕"}], "max_tokens": 64, "stream": False}
@app.post("/v1/chat/completions", tags=["inference"], dependencies=[Depends(auth)],
          summary="Chat completion (OpenAI-compatible, supports stream=true SSE)",
          description="Omitted max_tokens/temperature/top_p/model get config defaults. With `stream: true` returns "
          "`text/event-stream`: lines `data: {chat.completion.chunk JSON}` ending with `data: [DONE]`. "
          "Mid-stream failures emit `data: {\"error\":{...}}` then `data: [DONE]`.")
async def chat(request: Request, body: dict = Body(..., examples=[EXAMPLE])):
    rid = request.state.rid
    if not isinstance(body.get("messages"), list) or not body["messages"]:
        raise GwError(400, "bad_request", "'messages' must be a non-empty array", "invalid_request_error")
    body.setdefault("model", CFG["model"])
    for k, v in (CFG.get("defaults") or {}).items(): body.setdefault(k, v)
    t0 = time.time(); q = await acquire(rid)
    if body.get("stream"):
        body.setdefault("stream_options", {"include_usage": True})
        try:
            req = client.build_request("POST", "/v1/chat/completions", json=body)
            for i in range(int(CFG["retries"]) + 1):
                try: up = await client.send(req, stream=True); break
                except (httpx.ConnectError, httpx.ConnectTimeout):
                    if i == int(CFG["retries"]): raise GwError(503, "upstream_unavailable", "Model server unreachable", "server_error")
                    await asyncio.sleep(float(CFG["retry_backoff_s"]) * (2**i))
                except httpx.TimeoutException: raise GwError(504, "upstream_timeout", "Model server did not respond", "server_error")
        except GwError: release(); raise
        if up.status_code >= 400:
            txt = (await up.aread()).decode(errors="replace")[:500]; await up.aclose(); release()
            raise GwError(502 if up.status_code >= 500 else 400, "upstream_error" if up.status_code >= 500 else "bad_request", f"Upstream {up.status_code}: {txt}")
        async def gen():
            ttft = None; ct = 0; pt = None; code = None; status = 200; tfirst = None; buf = ""
            try:
                async for chunk in up.aiter_text():
                    if ttft is None: tfirst = time.time(); ttft = tfirst - t0 - q
                    buf += chunk
                    while "\n" in buf:
                        line, buf = buf.split("\n", 1)
                        if line.startswith("data: ") and line[6:].strip() not in ("", "[DONE]"):
                            try:
                                j = json.loads(line[6:]); u = j.get("usage")
                                if u: pt = u.get("prompt_tokens", pt); ct = u.get("completion_tokens", ct) or ct
                                elif any((c.get("delta") or {}).get("content") for c in j.get("choices", [])): ct += 0 if u else 1
                            except Exception: pass
                    yield chunk
            except httpx.TimeoutException:
                code, status = "upstream_timeout", 504
                yield "data: " + json.dumps(err_body(code, "Upstream stalled mid-stream", "server_error", rid)) + "\n\ndata: [DONE]\n\n"
            except Exception as e:
                code, status = "upstream_error", 502
                yield "data: " + json.dumps(err_body(code, f"Stream aborted: {type(e).__name__}", "server_error", rid)) + "\n\ndata: [DONE]\n\n"
            finally:
                await up.aclose(); release()
                record(rid, True, status, t0, q, pt, ct, (time.time() - tfirst) if tfirst else None, ttft, code)
        return StreamingResponse(gen(), media_type="text/event-stream", headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})
    try:
        tg = time.time(); resp = await upstream_call("POST", "/v1/chat/completions", json=body); gen_s = time.time() - tg
    except GwError as e:
        release()
        raise
    release()
    try: data = resp.json()
    except Exception: raise GwError(502, "upstream_error", f"Upstream returned non-JSON ({resp.status_code})", "server_error")
    if resp.status_code >= 400:
        raise GwError(502 if resp.status_code >= 500 else 400, "upstream_error" if resp.status_code >= 500 else "bad_request", f"Upstream {resp.status_code}: {str(data)[:300]}")
    u = data.get("usage") or {}
    record(rid, False, resp.status_code, t0, q, u.get("prompt_tokens"), u.get("completion_tokens"), gen_s)
    return JSONResponse(data)

@app.get("/v1/models", tags=["inference"], dependencies=[Depends(auth)], summary="List models")
async def models():
    return JSONResponse((await upstream_call("GET", "/v1/models")).json())

@app.get("/health", tags=["ops"], summary="Liveness: gateway process is up (no upstream check)")
async def health(): return {"status": "ok", "uptime_s": int(time.time() - START)}

@app.get("/ready", tags=["ops"], summary="Readiness: upstream reachable, model loaded, memory headroom")
async def ready():
    checks = {}; m = mem()
    try:
        r = await client.get("/v1/models", timeout=3); ids = [d["id"] for d in r.json().get("data", [])]
        checks["upstream"] = r.status_code == 200; checks["model_loaded"] = CFG["model"] in ids
    except Exception: checks["upstream"] = checks["model_loaded"] = False
    checks["memory_headroom"] = m["available_gb"] >= float(CFG["min_free_mem_gb"])
    ok = all(checks.values())
    return JSONResponse({"ready": ok, "checks": checks, "memory": m, "queue_depth": waiting, "inflight": int(INFL._value.get()),
                         "max_concurrency": MAX_CONC, "max_queue": MAX_QUEUE}, status_code=200 if ok else 503)

@app.get("/metrics", tags=["ops"], summary="Prometheus metrics (text exposition)")
async def metrics(): mem(); return Response(generate_latest(), media_type=CONTENT_TYPE_LATEST)

def pct(xs, p):
    xs = sorted(xs); return round(xs[min(len(xs)-1, int(round(p/100*(len(xs)-1))))], 3) if xs else None
@app.get("/stats", tags=["ops"], summary="Human-readable summary over the recent request window")
async def stats():
    r = list(RECENT); lat = [x["lat"] for x in r if x.get("lat") and x["ok"]]; tps = [x["tps"] for x in r if x.get("tps")]
    ttft = [x["ttft"] for x in r if x.get("ttft") is not None]; qs = [x["q"] for x in r if x.get("q") is not None]
    errs = collections.Counter(x["code"] for x in r if not x["ok"])
    return {"window_requests": len(r), "error_rate": round(sum(errs.values())/len(r), 3) if r else 0, "errors": dict(errs),
            "latency_s": {"p50": pct(lat, 50), "p95": pct(lat, 95)}, "queue_wait_s": {"p50": pct(qs, 50), "p95": pct(qs, 95), "max": round(max(qs), 3) if qs else None},
            "ttft_s": {"p50": pct(ttft, 50), "p95": pct(ttft, 95)}, "tokens_per_s_avg": round(statistics.mean(tps), 1) if tps else None,
            "queue_depth": waiting, "inflight": int(INFL._value.get()), "memory": mem(), "uptime_s": int(time.time() - START)}

@app.get("/admin/config", tags=["admin"], dependencies=[Depends(auth)], summary="Effective config (no secrets)")
async def get_config(): return {**CFG, "log_level": logging.getLevelName(log.level)}
@app.post("/admin/log-level", tags=["admin"], dependencies=[Depends(auth)], summary="Change log level at runtime (DEBUG/INFO/WARNING/ERROR)")
async def set_level(body: dict = Body(..., examples=[{"level": "DEBUG"}])):
    lv = str(body.get("level", "")).upper()
    if lv not in ("DEBUG", "INFO", "WARNING", "ERROR"): raise GwError(400, "bad_request", "level must be DEBUG|INFO|WARNING|ERROR", "invalid_request_error")
    log.setLevel(lv); jlog(logging.WARNING, "log_level_changed", new_level=lv); return {"log_level": lv}
