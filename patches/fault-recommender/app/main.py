"""
Fault Injection Recommender: scans the services in the namespace and recommends fault injections.

Config (env):
    AGENTS              same JSON map as the dashboard (ConfigMap faultlab-dashboard)
    OLLAMA_SERVICE_URL  in-cluster Ollama, e.g. http://ollama.pauric-test.svc.cluster.local:11434
    OLLAMA_MODEL        default llama3
    RESULTS_DIR         where recommendations.json is written (default /results)
    SCAN_ON_START       run a scan when the pod starts (default true)
    SCAN_INTERVAL       seconds between automatic scans, 0 = off (default 0)
"""
import json
import os
import threading
import time
from contextlib import asynccontextmanager
from datetime import datetime, timezone
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import HTMLResponse, PlainTextResponse

from app import llm
from app.campaign import canary_pick, run_campaign
from app.discovery import discover
from app.recommender import recommend

RESULTS = Path(os.environ.get("RESULTS_DIR", "/results")) / "recommendations.json"
CAMPAIGNS = RESULTS.with_name("campaigns.json")
SCAN_ON_START = os.environ.get("SCAN_ON_START", "true").lower() == "true"
SCAN_INTERVAL = int(os.environ.get("SCAN_INTERVAL", "0"))

_lock = threading.Lock()
_state = {"running": False, "log": "", "started_at": None, "finished_at": None, "progress": ""}
_latest: dict = {}


def _now():
    return datetime.now(timezone.utc).isoformat()


def _log(line: str):
    _state["log"] += f"{time.strftime('%H:%M:%S')} {line}\n"


def _scan(only: list[str] | None, use_llm: bool):
    global _latest
    try:
        _state.update(log="", started_at=_now(), finished_at=None)
        d = discover()
        names = [n for n in d["services"] if not only or n in only]
        _log(f"discovered {len(d['services'])} services in '{d['namespace']}' via {d['source']}")
        use_llm = use_llm and llm.available()
        _log(f"LLM {'on (' + llm.OLLAMA_MODEL + ')' if use_llm else 'off or unreachable, rules only'}")
        out = dict(_latest.get("services", {})) if only else {}
        for i, name in enumerate(names, 1):
            _state["progress"] = f"{i}/{len(names)} {name}"
            t = time.time()
            out[name] = recommend(d["services"][name], use_llm=use_llm)
            out[name]["profile"] = d["services"][name]
            err = out[name]["llm_error"]
            _log(f"{name}: {len(out[name]['recommendations'])} recs in {time.time() - t:.1f}s"
                 + (f" (LLM error: {err})" if err else ""))
        _latest = {"generated_at": _now(), "namespace": d["namespace"], "source": d["source"],
                   "llm": llm.OLLAMA_MODEL if use_llm else None, "services": out}
        try:
            RESULTS.parent.mkdir(parents=True, exist_ok=True)
            RESULTS.write_text(json.dumps(_latest, indent=2))
        except OSError as e:
            _log(f"could not save {RESULTS}: {e}")
        _log("done")
    except Exception as e:
        _log(f"scan failed: {type(e).__name__}: {e}")
    finally:
        _state.update(running=False, finished_at=_now(), progress="")


def start_scan(only=None, use_llm=True) -> bool:
    with _lock:
        if _state["running"]:
            return False
        _state["running"] = True
    threading.Thread(target=_scan, args=(only, use_llm), daemon=True).start()
    return True


# ── automated campaigns (recommend -> plan -> run -> judge -> learn) ─────────

_camp_lock = threading.Lock()
_camp = {"running": False, "log": "", "started_at": None, "finished_at": None, "request": None, "error": None}
_campaigns: list = []


def _clog(line: str):
    _camp["log"] += f"{time.strftime('%H:%M:%S')} {line}\n"


def _run_campaign(req: dict):
    try:
        _camp.update(log="", started_at=_now(), finished_at=None, error=None, request=req)
        rep = run_campaign(req["service"], budget=int(req.get("budget", 4)), rounds=int(req.get("rounds", 1)),
                           strategy=req.get("strategy", "llm"), use_llm=req.get("llm", True), log=_clog)
        profile = rep.pop("_profile")
        _campaigns.append(rep)
        del _campaigns[:-20]
        try:
            CAMPAIGNS.parent.mkdir(parents=True, exist_ok=True)
            CAMPAIGNS.write_text(json.dumps(_campaigns, indent=2))
        except OSError as e:
            _clog(f"could not save {CAMPAIGNS}: {e}")
        # keep the recommendations tab in step with what the campaign learned
        svc = rep["service"]
        _latest.setdefault("services", {})
        _latest.setdefault("generated_at", rep["finished_at"])
        _latest.setdefault("namespace", profile.get("namespace", ""))
        _latest.setdefault("source", "campaign")
        _latest.setdefault("llm", rep["llm"])
        _latest["services"][svc] = {**(_latest["services"].get(svc) or {}), "service": svc, "base": profile.get("base"),
                                    "is_agent": True, "lang": rep["lang"], "profile": profile,
                                    "recommendations": rep["next_recommendations"],
                                    "summary": rep.get("summary") or (_latest["services"].get(svc) or {}).get("summary", ""),
                                    "llm_error": None, "campaign_at": rep["finished_at"]}
    except Exception as e:
        _camp["error"] = f"{type(e).__name__}: {e}"
        _clog(f"campaign failed: {_camp['error']}")
    finally:
        _camp.update(running=False, finished_at=_now())


def _scheduler():
    while True:
        time.sleep(SCAN_INTERVAL)
        start_scan()


@asynccontextmanager
async def lifespan(app: FastAPI):
    global _latest
    try:
        _latest = json.loads(RESULTS.read_text())
    except (OSError, ValueError):
        pass
    try:
        _campaigns.extend(json.loads(CAMPAIGNS.read_text()))
    except (OSError, ValueError):
        pass
    if SCAN_ON_START:
        start_scan()
    if SCAN_INTERVAL > 0:
        threading.Thread(target=_scheduler, daemon=True).start()
    yield


app = FastAPI(title="Fault Injection Recommender", lifespan=lifespan)


@app.get("/api/services")
def services():
    """Live discovery only (no LLM): what the recommender can see right now."""
    return discover()


@app.post("/api/scan")
def scan(body: dict | None = None):
    body = body or {}
    if not start_scan(body.get("services") or None, body.get("llm", True)):
        return {"status": "busy"}
    return {"status": "started"}


@app.get("/api/status")
def status():
    return _state


@app.get("/api/recommendations")
def recommendations():
    return _latest or {"services": {}}


@app.get("/api/recommendations/{service}")
def recommendation(service: str):
    s = (_latest.get("services") or {}).get(service)
    if not s:
        raise HTTPException(404, f"no recommendations for '{service}' yet")
    return s


@app.get("/api/recommendations/{service}/stubs", response_class=PlainTextResponse)
def stubs(service: str):
    """Paste-ready maybe_* helpers and the ALL_MODES line for the agent's fault_injection.py."""
    s = recommendation(service)
    new = [r for r in s["recommendations"] if r.get("stub") and r["status"] == "new"]
    modes = (s["profile"].get("agent") or {}).get("modes") or ["NONE"]
    if s.get("lang") == "go":
        head = f"// {service}: add to agent/faults.go\n// var Modes = []string{{{', '.join(chr(34) + m + chr(34) for m in modes + [r['id'] for r in new])}}}\n\n"
    else:
        head = f"# {service}: add to fault_injection.py\n# ALL_MODES = {modes + [r['id'] for r in new]}\n\n"
    return head + "\n\n".join(r["stub"] for r in new)


@app.post("/api/campaign")
def campaign(body: dict):
    """{"service": "checkoutservice-agent", "budget": 4, "rounds": 1, "strategy": "llm|rules|all", "llm": true}"""
    if not body.get("service"):
        raise HTTPException(400, "service is required")
    if body.get("strategy", "llm") not in ("llm", "rules", "all"):
        raise HTTPException(400, "strategy must be llm, rules or all")
    with _camp_lock:
        if _camp["running"]:
            return {"status": "busy"}
        _camp["running"] = True
    threading.Thread(target=_run_campaign, args=(body,), daemon=True).start()
    return {"status": "started"}


@app.get("/api/campaign")
def campaign_status():
    """Progress of the current campaign plus the latest report."""
    return {**_camp, "latest": _campaigns[-1] if _campaigns else None}


@app.get("/api/campaigns")
def campaigns():
    """Every saved campaign, one summary row each (for comparing strategies in the report)."""
    return [{"service": c["service"], "strategy": c["strategy"], "llm": c["llm"], "started_at": c["started_at"],
             "elapsed_s": c["elapsed_s"], "rounds": len(c["rounds"]), **c["score"],
             "llm_tokens": c["llm_usage"]["input_tokens"] + c["llm_usage"]["output_tokens"]} for c in _campaigns]


@app.get("/api/canary-fault/{service}")
def canary_fault(service: str):
    """The fault to use for the canary rollback demo, picked from the agent's latest evidence."""
    d = discover()
    from app.campaign import _find
    try:
        p = _find(d, service)
    except KeyError as e:
        raise HTTPException(404, str(e)) from e
    for c in reversed(_campaigns):   # campaign history covers more modes than the agent's last run
        if c["service"] == p["name"]:
            return {**c["canary_pick"], "source": "campaign"}
    return {**canary_pick((p.get("agent") or {}).get("results")), "source": "agent results"}


@app.get("/healthz")
def healthz():
    return {"ok": True}


@app.get("/", response_class=HTMLResponse)
def index():
    return (Path(__file__).with_name("ui.html")).read_text()
