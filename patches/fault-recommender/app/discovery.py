"""
Builds a profile of every service in the namespace:

  Kubernetes API   Services, Deployments (image, env, FAULT_MODE, *_ADDR deps), Pods (ready/restarts)
  AGENTS map       each agent's /ui/modes (faults it already has) and /ui/results (which showed evidence)
  catalog          gRPC methods from demo.proto

Outside a cluster (no service-account token) it falls back to the AGENTS map only.
"""
import json
import os
import re
from pathlib import Path

import httpx

from app.catalog import BOUTIQUE_RPCS, EVIDENCE_KEYS

SA_DIR = Path("/var/run/secrets/kubernetes.io/serviceaccount")
NAMESPACE = os.environ.get("POD_NAMESPACE") or (
    (SA_DIR / "namespace").read_text().strip() if (SA_DIR / "namespace").exists() else "default")
AGENTS = json.loads(os.environ.get("AGENTS", "{}"))
EXCLUDE = set(os.environ.get("EXCLUDE", "kubernetes,faultlab-dashboard,fault-recommender,edge-proxy,"
                                        "frontend-external,loadgenerator").split(","))
TIMEOUT = httpx.Timeout(10.0, connect=3.0)
# Language of each agent's fault code, so code stubs come out in the right language
AGENT_LANG = json.loads(os.environ.get("AGENT_LANG", '{"checkout": "go"}'))


def base_name(name: str) -> str:
    """adserviceagent -> ad, productcatalogservice -> productcatalog, currencyagent -> currency"""
    n = name.lower().replace("-", "").replace("_", "")
    for suffix in ("agent", "service", "agent", "service"):
        if n.endswith(suffix) and len(n) > len(suffix):
            n = n[: -len(suffix)]
    return n


def _host(url_or_addr: str) -> str:
    """http://emailservice:5000 -> emailservice, cartservice.default.svc:7070 -> cartservice"""
    h = re.sub(r"^\w+://", "", url_or_addr).split("/")[0].split(":")[0]
    return h if re.fullmatch(r"[\d.]+", h) else h.split(".")[0]


# ── Kubernetes ────────────────────────────────────────────────────────────────

def _k8s_get(client: httpx.Client, path: str) -> list:
    r = client.get(path)
    r.raise_for_status()
    return r.json().get("items", [])


def scan_k8s() -> dict:
    """Return {service_name: profile} from the K8s API, or {} when not in a cluster."""
    token_file = SA_DIR / "token"
    if not token_file.exists():
        return {}
    api = f"https://{os.environ['KUBERNETES_SERVICE_HOST']}:{os.environ.get('KUBERNETES_SERVICE_PORT', '443')}"
    with httpx.Client(base_url=api, timeout=TIMEOUT, verify=str(SA_DIR / "ca.crt"),
                      headers={"Authorization": f"Bearer {token_file.read_text().strip()}"}) as c:
        services = _k8s_get(c, f"/api/v1/namespaces/{NAMESPACE}/services")
        deployments = _k8s_get(c, f"/apis/apps/v1/namespaces/{NAMESPACE}/deployments")
        pods = _k8s_get(c, f"/api/v1/namespaces/{NAMESPACE}/pods")

    out = {}
    for s in services:
        name = s["metadata"]["name"]
        if name in EXCLUDE:
            continue
        out[name] = {
            "name": name,
            "selector": s["spec"].get("selector") or {},
            "ports": [{"name": p.get("name"), "port": p["port"]} for p in s["spec"].get("ports", [])],
        }

    for d in deployments:
        labels = d["spec"]["template"]["metadata"].get("labels", {})
        # attach the deployment to every service whose selector matches its pod labels
        targets = [p for p in out.values()
                   if p["selector"] and all(labels.get(k) == v for k, v in p["selector"].items())]
        for c in d["spec"]["template"]["spec"].get("containers", []):
            env = {e["name"]: e.get("value", "") for e in c.get("env", []) if "name" in e}
            for p in targets:
                p.update({
                    "deployment": d["metadata"]["name"],
                    "image": c.get("image"),
                    "fault_mode": env.get("FAULT_MODE"),
                    "uses_llm": any(k.startswith(("OLLAMA", "QWEN", "LLM")) for k in env),
                    "depends_on": sorted({_host(v) for k, v in env.items() if k.endswith("_ADDR") and v}),
                    "resources": c.get("resources", {}),
                })

    for pod in pods:
        labels = pod["metadata"].get("labels", {})
        for p in out.values():
            if p["selector"] and all(labels.get(k) == v for k, v in p["selector"].items()):
                cs = pod.get("status", {}).get("containerStatuses", [])
                p["pods"] = p.get("pods", 0) + 1
                p["ready"] = p.get("ready", 0) + int(all(x.get("ready") for x in cs) and bool(cs))
                p["restarts"] = p.get("restarts", 0) + sum(x.get("restartCount", 0) for x in cs)
    return out


# ── Agents ────────────────────────────────────────────────────────────────────

def _caught(r: dict, has_checks: bool) -> list[str]:
    """What caught this row's fault: failed correctness checks, a boundary alert, or the agent itself."""
    out = list(r.get("failed_checks") or [])
    if r.get("boundary_alert_point"):
        out.append(f"boundary:{r['boundary_alert_point']}")
    if r.get("detected_by") == "agent":
        out.append("agent")
    if not has_checks and not out and any(r.get(k) for k in EVIDENCE_KEYS):
        out.append("checkpoints")   # older agents have no checks: evidence is the only detector
    return out


def _summarise_results(res: dict) -> dict:
    """Per-mode outcome of the agent's last /ui/run, grouped into caught / uncaught / silent / errored.

    caught    the fault left evidence and something flagged it (a failed check, a boundary alert)
    uncaught  the fault entered (infection_point / steps_lost) but no check flagged it, e.g. FM_3_2
    silent    the fault left no trace at all
    For agents whose results carry no checks (the Python agents), any evidence counts as caught.
    """
    rows = res.get("results", []) if isinstance(res, dict) else []
    has_checks = any("checks" in r or "detected_by" in r for r in rows)
    detected, uncaught, silent, errored, modes = [], [], [], [], {}
    detectors = sorted({c.get("rule") for r in rows for c in (r.get("checks") or []) if c.get("rule")})
    for r in rows:
        mode = r.get("fault_mode")
        if not mode:
            continue
        caught = _caught(r, has_checks)
        injected = any(r.get(k) for k in EVIDENCE_KEYS) or bool(caught)
        if mode == "NONE":
            # a control run should be clean; a failed check here is a false positive
            status = "false_positive" if r.get("failed_checks") else "clean"
        elif r.get("error"):
            status = "errored"; errored.append(mode)
        elif caught:
            status = "caught"; detected.append(mode)
        elif injected:
            status = "uncaught"; uncaught.append(mode)
        else:
            status = "silent"; silent.append(mode)
        modes[mode] = {"status": status, "infection_point": r.get("infection_point"),
                       "propagation_depth": r.get("propagation_depth"), "caught_by": caught,
                       "steps_lost": r.get("steps_lost") or [], "outcome": r.get("outcome"),
                       "order_accepted": r.get("order_accepted"), "elapsed_ms": r.get("elapsed_ms"),
                       "error": r.get("error")}
    return {"generated_at": res.get("generated_at") if isinstance(res, dict) else None,
            "backend": res.get("backend") if isinstance(res, dict) else None,
            "detected": detected, "uncaught": uncaught, "silent": silent, "errored": errored,
            "detectors": detectors, "modes": modes,
            "expected_steps": sorted({s for r in rows for s in (r.get("steps_reached") or [])})}


def probe_agent(url: str) -> dict:
    url = url.rstrip("/")
    info = {"url": url, "modes": [], "checkpoints": [], "results": None, "never_run": [], "error": None}
    try:
        with httpx.Client(timeout=TIMEOUT) as c:
            m = c.get(f"{url}/ui/modes").json()
            info["modes"] = m.get("modes", [])
            info["checkpoints"] = m.get("checkpoints") or []
            r = c.get(f"{url}/ui/results")
            if r.status_code < 400:
                info["results"] = _summarise_results(r.json())
    except (httpx.HTTPError, ValueError) as e:
        info["error"] = f"{type(e).__name__}: {e}"
    ran = set((info["results"] or {}).get("modes", {}))
    info["never_run"] = [m for m in info["modes"] if m != "NONE" and m not in ran]
    return info


# ── Combined ──────────────────────────────────────────────────────────────────

def discover() -> dict:
    """Return {"namespace", "source", "services": {name: profile}}."""
    services = scan_k8s()
    source = "kubernetes" if services else "agents-only"

    # services named in AGENTS but not found in K8s (e.g. running locally)
    for agent_name, url in AGENTS.items():
        key = _host(url) if _host(url) in services else agent_name
        services.setdefault(key, {"name": key})
        services[key]["agent_name"] = agent_name
        services[key]["agent"] = probe_agent(url)

    # reverse edges: who calls me (fan-in = blast radius when I fail)
    for p in services.values():
        p.setdefault("depends_on", [])
    for p in services.values():
        for dep in p["depends_on"]:
            if dep in services:
                services[dep].setdefault("called_by", []).append(p["name"])

    for p in services.values():
        b = base_name(p["name"])
        p["base"] = b
        p.setdefault("called_by", [])
        p["lang"] = AGENT_LANG.get(b, "python")
        p["is_agent"] = bool(p.get("agent") or p.get("fault_mode") is not None or p.get("uses_llm"))
        proto = BOUTIQUE_RPCS.get(b)
        p["grpc"] = {"service": proto[0], "port": proto[1], "methods": proto[2]} if proto else None
    return {"namespace": NAMESPACE, "source": source, "services": services}
