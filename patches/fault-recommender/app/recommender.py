"""
Turns a service profile into ranked fault-injection recommendations.

  1. rules     deterministic candidates (MAST gaps, silent existing modes, infra faults, known BL ideas)
  2. llm       Ollama adds service-specific faults and rewrites rationale, given the profile and step 1
  3. merge     dedupe by id, mark modes the service already has, score and rank, attach a code stub

Step 1 always runs, so the service still gives useful output when Ollama is down.
"""
import json
import re

from app import llm
from app.catalog import INFRA_FAULTS, MAST_MODES, RPC_PAYLOADS

ID_RE = re.compile(r"^(FM_\d_\d|BL_[A-Z0-9_]+|INF_[A-Z0-9_]+)$")
PRIORITY_SCORE = {"high": 3, "medium": 2, "low": 1}

# MAST modes worth adding to a tool-calling agent, with how to inject them in this codebase
MAST_INJECTIONS = {
    "FM_1_3": ("Repeat the downstream tool call (same request sent twice)",
               "Duplicate side effects or double-counted results", "count/side-effect check at the *_DONE checkpoint"),
    "FM_1_4": ("Drop handoff_contract / user context before the tool call",
               "Request answered for the wrong user or with defaults", "TASK_START vs *_DONE input comparison"),
    "FM_1_5": ("Loop the graph without reaching FINAL_ANSWER until a max-step guard trips",
               "Request hangs or times out", "FINAL_ANSWER missing; elapsed_ms spike"),
    "FM_2_4": ("Strip one field from the tool result before it is handed back (e.g. currency_code)",
               "Caller gets a partial object and fills in defaults", "BOUNDARY_CHECK on required fields"),
    "FM_2_6": ("LLM classifier says one route, executor runs another",
               "Logged reasoning disagrees with the action taken", "compare route in TASK_START with action in *_DONE"),
    "FM_3_2": ("Skip the BOUNDARY_CHECK step entirely",
               "Faults downstream pass through undetected", "BOUNDARY_CHECK missing from steps_reached"),
    "FM_3_3": ("BOUNDARY_CHECK always reports ok, even for bad data",
               "False sense of safety: combine with a BL_* fault to see it slip through", "boundary result vs ground truth"),
}

# Business-logic ideas per Online Boutique service (used when the LLM is off or adds nothing)
BL_IDEAS = {
    "currency": [("BL_WRONG_RATE", "Convert uses an inverted exchange rate", "Convert"),
                 ("BL_NANOS_TRUNCATED", "Convert drops the nanos field (rounding errors)", "Convert"),
                 ("BL_UNSUPPORTED_CURRENCY", "GetSupportedCurrencies omits the user's currency", "GetSupportedCurrencies")],
    "email": [("BL_WRONG_RECIPIENT", "Confirmation sent to a different address", "SendOrderConfirmation"),
              ("BL_MISSING_ORDER_ITEMS", "Email body lists no items", "SendOrderConfirmation"),
              ("BL_DUPLICATE_SEND", "Confirmation sent twice", "SendOrderConfirmation")],
    "ad": [("BL_IRRELEVANT_AD", "Ignores context_keys and returns random ads", "GetAds"),
           ("BL_BROKEN_REDIRECT", "Ad redirect_url points to a missing product", "GetAds"),
           ("BL_EMPTY_ADS", "Returns an empty ad list", "GetAds")],
    "productcatalog": [("BL_WRONG_CURRENCY_CODE", "price_usd.currency_code set to EUR without conversion", "ListProducts"),
                       ("BL_SEARCH_CASE_SENSITIVE", "SearchProducts misses matches that differ in case", "SearchProducts"),
                       ("BL_GET_WRONG_PRODUCT", "GetProduct returns a different product id", "GetProduct")],
    "cart": [("BL_QUANTITY_DRIFT", "AddItem stores quantity +1", "AddItem"),
             ("BL_CART_NOT_EMPTIED", "EmptyCart returns ok but keeps the items", "EmptyCart")],
    "payment": [("BL_DOUBLE_CHARGE", "Charge processes the amount twice", "Charge"),
                ("BL_ACCEPT_EXPIRED_CARD", "Charge accepts an expired card", "Charge")],
    "shipping": [("BL_ZERO_SHIPPING", "GetQuote returns 0.00", "GetQuote"),
                 ("BL_MISSING_TRACKING", "ShipOrder returns an empty tracking_id", "ShipOrder")],
    "checkout": [("BL_TOTAL_MISMATCH", "Order total differs from the sum of items + shipping", "PlaceOrder"),
                 ("BL_CONFIRMATION_WRONG_ORDER", "Confirmation email carries another order's id/tracking", "PlaceOrder"),
                 ("BL_UNCONVERTED_PRICE", "Item prices passed on in USD without converting to the user's currency", "PlaceOrder"),
                 ("BL_STALE_TRACKING", "OrderResult returns a tracking id from a previous order", "PlaceOrder")],
    "recommendation": [("BL_RECOMMEND_SAME_PRODUCT", "Recommends the product being viewed", "ListRecommendations"),
                       ("BL_NONEXISTENT_ID", "Recommends ids that are not in the catalog", "ListRecommendations")],
}


def _rec(id_, title, category, target, inject, symptom, detect, priority, rationale, source="rules", mast=None):
    return {"id": id_, "title": title, "category": category, "mast": mast, "target": target,
            "inject": inject, "expected_symptom": symptom, "detect": detect,
            "priority": priority, "rationale": rationale, "source": source}


def rule_based(p: dict) -> list[dict]:
    recs = []
    agent = p.get("agent") or {}
    existing = set(agent.get("modes", []))
    results = agent.get("results") or {}
    methods = (p.get("grpc") or {}).get("methods", [])
    main_rpc = methods[0] if methods else "main request"
    fan_in = len(p.get("called_by", []))
    callers = ", ".join(p.get("called_by", [])) or "no in-cluster callers found"

    # Existing modes that ran but left no trace are the most useful thing to fix first
    for m in results.get("silent", []):
        recs.append(_rec(m, f"{m} ran but produced no evidence", "DETECTION", "checkpoints",
                         "Already injected", "Fault is invisible in steps_lost / infection_point",
                         "Add the matching flag to the *_DONE checkpoint data and rip_summary()",
                         "high", f"{m} ran without leaving any evidence, so the RIP metrics can't see it."))
    for m in results.get("uncaught", []):
        d = (results.get("modes") or {}).get(m, {})
        ip = d.get("infection_point") or "?"
        recs.append(_rec(m, f"{m} entered at {ip} but no check caught it", "DETECTION", ip,
                         "Already injected", f"Wrong order goes through; only steps_lost shows {', '.join(d.get('steps_lost') or []) or 'it'}",
                         f"Add a correctness rule (next to {', '.join(results.get('detectors') or []) or 'the existing ones'}) that fails when {ip} is missing or wrong",
                         "high", f"{m} reached {ip} and propagated {d.get('propagation_depth', '?')} checkpoint(s), "
                                 "yet every check passed, so the canary correctness metric would not see it."))
    for m in results.get("errored", []):
        recs.append(_rec(m, f"{m} errored in the test runner", "DETECTION", "test_fault_injection.py",
                         "Already injected", "Runner raised instead of recording a result",
                         "Fix the mock or the exception path", "medium",
                         f"{m} raised an exception during the last run, so it has no result."))

    if p.get("is_agent"):
        for code, (inject, symptom, detect) in MAST_INJECTIONS.items():
            if code in existing:
                continue
            pr = "high" if code in ("FM_1_3", "FM_3_3") and p["base"] in ("payment", "email", "cart", "checkout") \
                else "medium" if code in ("FM_2_4", "FM_3_2", "FM_1_3") else "low"
            recs.append(_rec(code, MAST_MODES[code], "MAST", main_rpc, inject, symptom, detect, pr,
                             f"No {code} mode yet ({len(existing - {'NONE'})} other modes exist). {symptom} is not tested today.",
                             mast=code))
        recs.append(_rec("INF_LLM_DOWN", "Ollama unreachable or slow", "INF", "LLM call",
                         INFRA_FAULTS["INF_LLM_DOWN"], "Agent falls back, hangs or returns 5xx",
                         "TASK_START reached, no route decided; latency", "medium" if fan_in else "low",
                         "Every agent shares one in-cluster Ollama, so if it fails, every agent fails at the same time."))

    for id_, title, rpc in BL_IDEAS.get(p["base"], []):
        if id_ in existing:
            continue
        recs.append(_rec(id_, title, "BL", rpc, f"Corrupt the {rpc} result: {title[0].lower() + title[1:]}",
                         f"Callers ({callers}) act on wrong but well-formed data",
                         "BOUNDARY_CHECK against the expected schema/invariant",
                         "medium", f"{rpc} returns {RPC_PAYLOADS.get(rpc, 'data')}, and nothing checks that invariant today."))

    if fan_in:
        pr = "high" if fan_in >= 2 else "medium"
        recs.append(_rec("INF_LATENCY", f"Slow {main_rpc}", "INF", main_rpc, INFRA_FAULTS["INF_LATENCY"],
                         f"Callers ({callers}) slow down or time out", "caller-side latency / DEADLINE_EXCEEDED",
                         pr, f"{fan_in} service(s) call this one ({callers}), so delays spread to them."))
        recs.append(_rec("INF_UNAVAILABLE", f"{main_rpc} returns UNAVAILABLE", "INF", main_rpc,
                         INFRA_FAULTS["INF_UNAVAILABLE"], "Caller error pages or retries",
                         "frontend 5xx rate; caller logs", pr,
                         f"This shows whether {callers} degrade gracefully when this service is down."))
    if p.get("restarts"):
        recs.append(_rec("INF_TIMEOUT", "Hang past the caller deadline", "INF", main_rpc, INFRA_FAULTS["INF_TIMEOUT"],
                         "Liveness probe kills the pod", "restartCount", "medium",
                         f"Pods have already restarted {p['restarts']} time(s), so this checks probe tuning."))
    for r in recs:
        if r["id"] in existing and r["category"] != "DETECTION":
            r["status"] = "exists"
    return recs


SYSTEM = """You are a reliability engineer designing fault-injection experiments for microservices in
Google's Online Boutique, where some services have been rewritten as LLM agents (FastAPI + LangGraph + Ollama).
Agents record checkpoints (TASK_START, <TOOL>_DONE, BOUNDARY_CHECK, FINAL_ANSWER) and are scored on
reachability, infection point and propagation depth. Faults are toggled with the FAULT_MODE env var.
Fault ids: FM_x_y for MAST agent failure modes, BL_<NAME> for business-logic corruption, INF_<NAME> for infrastructure.
Reply with JSON only."""

PROMPT = """Service profile:
{profile}

MAST failure modes:
{mast}

Infrastructure faults:
{infra}

Already proposed (do not repeat these ids): {proposed}
Already implemented in the service: {existing}

Propose up to {n} NEW, service-specific fault injections that would teach us the most about how failures in
this service propagate to its callers. Prefer faults tied to a concrete RPC and its payload.
Return: {{"summary": "<one sentence on this service's main risk>",
"recommendations": [{{"id": "BL_...|FM_x_y|INF_...", "title": "...", "category": "BL|MAST|INF",
"mast": "FM_x_y or null", "target": "<RPC or step>", "inject": "<what the code does when FAULT_MODE=id>",
"expected_symptom": "...", "detect": "<checkpoint or metric that should catch it>",
"priority": "high|medium|low", "rationale": "<why this one, citing the profile>"}}]}}"""


def _llm_profile(p: dict) -> dict:
    keep = ("name", "base", "is_agent", "image", "fault_mode", "depends_on", "called_by", "grpc", "restarts")
    out = {k: p[k] for k in keep if p.get(k) not in (None, [], "")}
    if out.get("grpc"):
        out["grpc"]["payloads"] = {m: RPC_PAYLOADS.get(m, "") for m in out["grpc"]["methods"]}
    if p.get("agent"):
        out["existing_modes"] = p["agent"].get("modes")
        res = p["agent"].get("results") or {}
        out["last_results"] = {k: res.get(k) for k in ("detected", "uncaught", "silent", "errored")}
        out["detectors"] = res.get("detectors")
        out["checkpoints"] = p["agent"].get("checkpoints")
    return out


def llm_based(p: dict, proposed: list[str], n: int = 5) -> tuple[list[dict], str, dict]:
    existing = (p.get("agent") or {}).get("modes", [])
    data, usage = llm.chat_json(SYSTEM, PROMPT.format(
        profile=json.dumps(_llm_profile(p), indent=1),
        mast="\n".join(f"{k}: {v}" for k, v in MAST_MODES.items()),
        infra="\n".join(f"{k}: {v}" for k, v in INFRA_FAULTS.items()),
        proposed=", ".join(proposed) or "none", existing=", ".join(existing) or "none", n=n))
    recs = []
    for r in (data.get("recommendations") or [])[: n * 2]:
        if not isinstance(r, dict):
            continue
        rid = re.sub(r"[^A-Z0-9_]", "_", str(r.get("id", "")).upper().replace("-", "_").replace(".", "_"))
        if not ID_RE.match(rid):
            continue
        cat = {"F": "MAST", "B": "BL", "I": "INF"}[rid[0]]
        recs.append(_rec(rid, str(r.get("title", rid))[:120], cat, str(r.get("target", ""))[:80],
                         str(r.get("inject", "")), str(r.get("expected_symptom", "")), str(r.get("detect", "")),
                         r.get("priority") if r.get("priority") in PRIORITY_SCORE else "medium",
                         str(r.get("rationale", "")), source="llm",
                         mast=rid if cat == "MAST" else (r.get("mast") if ID_RE.match(str(r.get("mast") or "")) else None)))
    return recs, str(data.get("summary", ""))[:300], usage


def go_stub(r: dict) -> str:
    """A case for the checkout agent's dispatch switch (agent/faults.go)."""
    return (f'// {r["id"]}: {r["title"]}.\n'
            f'// Add "{r["id"]}" to Modes, then in dispatch():\n'
            f'case mode == "{r["id"]}" && toolName == "TODO_tool":\n'
            f'\ta.log.Warnf("[FAULT {r["id"]}] {r["title"]}")\n'
            f'\t// TODO: {r["inject"]}\n'
            f'\t// expected infection point: {r.get("target") or "?"}; should be caught by: {r.get("detect") or "?"}\n')


def stub(r: dict, lang: str = "python") -> str:
    """A maybe_* helper in the same shape as the agents' fault_injection.py."""
    if lang == "go":
        return go_stub(r)
    fn = "maybe_" + r["id"].lower()
    return (f'def {fn}(data):\n'
            f'    """{r["id"]}: {r["title"]}."""\n'
            f'    if FAULT_MODE == "{r["id"]}":\n'
            f'        log.warning("[FAULT {r["id"]}] {r["title"]}")\n'
            f'        # TODO: {r["inject"]}\n'
            f'        return data, True\n'
            f'    return data, False\n')


def score(r: dict, p: dict) -> float:
    s = PRIORITY_SCORE.get(r["priority"], 2) * 10
    s += min(len(p.get("called_by", [])), 4) * 2          # blast radius
    s += 5 if r["category"] == "DETECTION" else 0          # fix invisible faults first
    s += 2 if r["category"] == "BL" and p.get("is_agent") else 0
    s -= 15 if r.get("status") == "exists" else 0
    return s


def recommend(p: dict, use_llm: bool = True, max_items: int = 12) -> dict:
    recs = rule_based(p)
    summary, usage, llm_error = "", None, None
    if use_llm:
        try:
            extra, summary, usage = llm_based(p, [r["id"] for r in recs])
            existing = set((p.get("agent") or {}).get("modes", []))
            seen = {r["id"]: r for r in recs}
            for r in extra:
                if r["id"] in seen:
                    # keep the rule's structure, take the LLM's (usually more specific) rationale
                    if r["rationale"]:
                        seen[r["id"]]["rationale"] += f" LLM: {r['rationale']}"
                    continue
                if r["id"] in existing:
                    r["status"] = "exists"
                recs.append(r)
                seen[r["id"]] = r
        except Exception as e:  # LLM is best-effort; rules still stand
            llm_error = f"{type(e).__name__}: {e}"
    for r in recs:
        r.setdefault("status", "new")
        r["score"] = score(r, p)
        if r["category"] != "DETECTION":
            r["stub"] = stub(r, p.get("lang", "python"))
    recs.sort(key=lambda r: -r["score"])
    return {"service": p["name"], "base": p["base"], "is_agent": p.get("is_agent", False),
            "lang": p.get("lang", "python"), "summary": summary, "llm_usage": usage, "llm_error": llm_error,
            "recommendations": recs[:max_items]}
