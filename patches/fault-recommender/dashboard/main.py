"""
Fault-injection dashboard: one UI in front of every agent's /ui/* endpoints.

Config (env):
    AGENTS     JSON map name -> base URL, e.g.
               {"email":"http://emailservice:5000","ad":"http://adserviceagent:8080"}
    DATA_DIR   optional directory where /api/export also saves a timestamped copy (e.g. a PVC mount)
    RECOMMENDER_URL  optional fault-recommender base URL, e.g. http://fault-recommender:8080
"""
import json
import os
import time
from pathlib import Path

import httpx
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import HTMLResponse, JSONResponse, Response

AGENTS = json.loads(os.environ.get("AGENTS", "{}"))
DATA_DIR = os.environ.get("DATA_DIR")
RECOMMENDER = os.environ.get("RECOMMENDER_URL", "").rstrip("/")
TIMEOUT = httpx.Timeout(10.0, connect=3.0)

app = FastAPI(title="Fault Injection Dashboard")


def base(name: str) -> str:
    if name not in AGENTS:
        raise HTTPException(404, f"unknown agent '{name}'")
    return AGENTS[name].rstrip("/")


async def call(method: str, name: str, path: str, **kw):
    """Call an agent and turn network failures into a clean 502 the UI can display."""
    url = f"{base(name)}{path}"
    try:
        async with httpx.AsyncClient(timeout=TIMEOUT) as c:
            r = await c.request(method, url, **kw)
    except httpx.HTTPError as e:
        raise HTTPException(502, f"{name} unreachable at {url}: {type(e).__name__}") from e
    if r.status_code >= 400:
        raise HTTPException(502, f"{name} returned HTTP {r.status_code} for {path}")
    try:
        return r.json()
    except ValueError as e:
        raise HTTPException(502, f"{name} returned non-JSON for {path}") from e


# Recommender routes come before /api/{name}/... so "recommender" is never treated as an agent name
@app.get("/api/recommender/enabled")
def recommender_enabled():
    return {"enabled": bool(RECOMMENDER)}


@app.api_route("/api/recommender/{path:path}", methods=["GET", "POST"])
async def recommender(path: str, request: Request):
    if not RECOMMENDER:
        raise HTTPException(404, "RECOMMENDER_URL is not set")
    url = f"{RECOMMENDER}/api/{path}"
    try:
        async with httpx.AsyncClient(timeout=TIMEOUT) as c:
            r = await c.request(request.method, url, content=await request.body(),
                                headers={"Content-Type": request.headers.get("content-type", "application/json")})
    except httpx.HTTPError as e:
        raise HTTPException(502, f"recommender unreachable at {url}: {type(e).__name__}") from e
    return Response(r.content, status_code=r.status_code,
                    media_type=r.headers.get("content-type", "application/json"))


@app.get("/api/agents")
def agents():
    return list(AGENTS)


@app.get("/api/{name}/modes")
async def modes(name: str):
    return await call("GET", name, "/ui/modes")


@app.post("/api/{name}/run")
async def run(name: str, body: dict):
    return await call("POST", name, "/ui/run", json=body)


@app.get("/api/{name}/status")
async def status(name: str):
    return await call("GET", name, "/ui/status")


@app.get("/api/{name}/results")
async def results(name: str):
    return await call("GET", name, "/ui/results")


@app.post("/api/run-all")
async def run_all():
    """Start every mode on every agent. Agents have their own locks, so this fans out in parallel."""
    out = {}
    for name in AGENTS:
        try:
            out[name] = await call("POST", name, "/ui/run", json={})
        except HTTPException as e:
            out[name] = {"status": "error", "detail": e.detail}
    return out


@app.get("/api/export")
async def export():
    """Combined JSON of every agent's latest results (download). Also saved to DATA_DIR if set."""
    combined = {"exported_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"), "agents": {}}
    for name in AGENTS:
        try:
            combined["agents"][name] = await call("GET", name, "/ui/results")
        except HTTPException as e:
            combined["agents"][name] = {"error": e.detail}
    payload = json.dumps(combined, indent=2)
    stamp = time.strftime("%Y%m%d_%H%M%S")
    if DATA_DIR:
        try:
            Path(DATA_DIR).mkdir(parents=True, exist_ok=True)
            (Path(DATA_DIR) / f"faultlab_{stamp}.json").write_text(payload)
        except OSError:
            pass  # still return the download
    return Response(payload, media_type="application/json",
                    headers={"Content-Disposition": f'attachment; filename="faultlab_{stamp}.json"'})


@app.get("/healthz")
def healthz():
    return {"ok": True}


@app.get("/", response_class=HTMLResponse)
def index():
    return (Path(__file__).with_name("index.html")).read_text()
