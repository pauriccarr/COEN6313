"""
Automated fault campaign: the recommender picks which faults to run, runs them, reads the evidence and
re-ranks, so nobody has to choose faults by hand.

  1. RECOMMEND  recommend() on the live profile (rules + LLM)
  2. PLAN       pick up to `budget` of the agent's existing modes for this round, each with a hypothesis:
                the infection point we expect and the check that should catch it.
                strategy "llm"   the LLM picks from the candidates (rules fill any gap)
                strategy "rules" deterministic: never run > uncaught > silent > errored, spread over stages
                strategy "all"   every mode (the manual "run everything" baseline, for comparison)
                NONE always runs first as the control.
  3. RUN        POST {agent}/ui/run, poll /ui/status, GET /ui/results. This is the agent's test runner
                (mock or test orders), never FAULT_MODE on live traffic.
  4. JUDGE      compare each hypothesis with what happened: right infection point? caught as predicted?
                Faults that entered but passed every check are detection gaps.
  5. LEARN      merge the results into the profile and run recommend() again, so the gaps become
                high-priority DETECTION items and the next round plans from what was just learned.
"""
import json
import os
import time

import httpx

from app import llm
from app.discovery import _summarise_results, base_name, discover
from app.recommender import recommend

RUN_TIMEOUT = float(os.environ.get("AGENT_RUN_TIMEOUT", "3600"))  # live runner: minutes per mode on CPU
POLL = float(os.environ.get("AGENT_POLL_SECONDS", "3"))
STATUS_RANK = {None: 40, "uncaught": 30, "silent": 25, "errored": 15, "caught": 0}


# ── 2. PLAN ───────────────────────────────────────────────────────────────────

def _candidates(p: dict, done: set) -> list[dict]:
    agent = p.get("agent") or {}
    last = (agent.get("results") or {}).get("modes", {})
    out = []
    for m in agent.get("modes", []):
        if m == "NONE" or m in done:
            continue
        r = last.get(m) or {}
        out.append({"mode": m, "last_status": r.get("status"), "last_infection_point": r.get("infection_point"),
                    "last_caught_by": r.get("caught_by"), "last_depth": r.get("propagation_depth")})
    return out


def _rule_hypothesis(c: dict) -> dict:
    """With history, expect the same outcome again; without it, no prediction."""
    return {"expect_infection_point": c["last_infection_point"], "expect_caught_by": c["last_caught_by"]}


def plan_rules(p: dict, recs: list[dict], budget: int, done: set) -> list[dict]:
    rec_score = {r["id"]: r["score"] for r in recs}
    cands = _candidates(p, done)
    for c in cands:
        c["score"] = STATUS_RANK.get(c["last_status"], 0) + rec_score.get(c["mode"], 0) / 2
    chosen, stages, families = [], set(), set()
    while cands and len(chosen) < budget:
        # spread the round over different pipeline stages and both fault families
        for c in cands:
            c["adj"] = c["score"] - (8 if c["last_infection_point"] in stages else 0) \
                       - (4 if c["mode"][:2] in families else 0)
        best = max(cands, key=lambda c: (c["adj"], c["mode"]))
        cands.remove(best)
        stages.add(best["last_infection_point"])
        families.add(best["mode"][:2])
        why = {None: "never run yet", "uncaught": "entered but no check caught it last time",
               "silent": "left no evidence last time", "errored": "errored last time",
               "caught": "re-check: was caught last time"}[best["last_status"]]
        chosen.append({"mode": best["mode"], "why": why, "source": "rules", **_rule_hypothesis(best)})
    return chosen


PLAN_SYSTEM = """You plan fault-injection test runs for an LLM agent that replaced a microservice in Google's
Online Boutique. Each run injects one existing FAULT_MODE into a test order and records checkpoints
(the infection point is where the fault entered) and correctness checks (which ones failed).
Pick the runs that teach us the most: modes never run, modes whose last run was not caught, and a spread
over pipeline stages. Reply with JSON only."""

PLAN_PROMPT = """Service: {name}
Checkpoints in order: {checkpoints}
Correctness checks available: {detectors}
Open recommendations (highest first): {recs}

Candidate modes with their last result (last_status null = never run):
{cands}

Choose up to {budget} modes for the next round. For each, predict the checkpoint where the fault enters and
which correctness checks will fail (empty list if you think none will catch it).
Return: {{"plan": [{{"mode": "<one of the candidates>", "why": "<one sentence>",
"expect_infection_point": "<checkpoint>", "expect_caught_by": ["<check>", ...]}}]}}"""


def plan_llm(p: dict, recs: list[dict], budget: int, done: set) -> tuple[list[dict], dict | None, str | None]:
    cands = _candidates(p, done)
    agent = p.get("agent") or {}
    res = agent.get("results") or {}
    checkpoints = agent.get("checkpoints") or res.get("expected_steps") or []
    detectors = res.get("detectors") or []
    names = {c["mode"] for c in cands}
    chosen, usage, err = [], None, None
    try:
        data, usage = llm.chat_json(PLAN_SYSTEM, PLAN_PROMPT.format(
            name=p["name"], checkpoints=", ".join(checkpoints) or "unknown", detectors=", ".join(detectors) or "unknown",
            recs="; ".join(f"{r['id']} ({r['priority']}): {r['title']}" for r in recs[:8]) or "none",
            cands=json.dumps(cands, indent=1), budget=budget))
        for it in data.get("plan") or []:
            if not isinstance(it, dict):
                continue
            m = str(it.get("mode", "")).upper().strip()
            if m not in names or any(c["mode"] == m for c in chosen) or len(chosen) >= budget:
                continue
            ip = str(it.get("expect_infection_point") or "").upper() or None
            caught = [str(x) for x in it["expect_caught_by"] if str(x) in detectors] \
                if detectors and isinstance(it.get("expect_caught_by"), list) else None
            chosen.append({"mode": m, "why": str(it.get("why", ""))[:240], "source": "llm",
                           "expect_infection_point": ip if ip in checkpoints else None,
                           "expect_caught_by": caught})
        if not chosen:
            err = "LLM picked no usable mode (unknown or already run this campaign)"
    except Exception as e:  # best effort: rules fill the round
        err = f"{type(e).__name__}: {e}"
    if len(chosen) < budget:
        chosen += plan_rules(p, recs, budget - len(chosen), done | {c["mode"] for c in chosen})
    return chosen, usage, err


# ── 3. RUN ────────────────────────────────────────────────────────────────────

def run_on_agent(url: str, modes: list[str], log) -> dict:
    url = url.rstrip("/")
    with httpx.Client(timeout=httpx.Timeout(30.0, connect=5.0)) as c:
        r = c.post(f"{url}/ui/run", json={"modes": modes}).json()
        if r.get("status") == "busy":
            log("agent busy, waiting for its current run to finish")
            _wait(c, url, log)
            r = c.post(f"{url}/ui/run", json={"modes": modes}).json()
        if r.get("status") != "started":
            raise RuntimeError(f"agent did not start the run: {r}")
        _wait(c, url, log)
        return c.get(f"{url}/ui/results").json()


def _wait(c: httpx.Client, url: str, log):
    t0, seen = time.time(), 0
    while time.time() - t0 < RUN_TIMEOUT:
        time.sleep(POLL)
        s = c.get(f"{url}/ui/status").json()
        lines = (s.get("log") or "").splitlines()
        for line in lines[seen:]:
            log(f"  agent: {line}")
        seen = len(lines)
        if not s.get("running"):
            return
    raise TimeoutError(f"agent run still going after {RUN_TIMEOUT:.0f}s")


# ── 4. JUDGE ──────────────────────────────────────────────────────────────────

def judge(item: dict, actual: dict | None) -> dict:
    if not actual:
        return {**item, "actual": None, "verdict": "missing"}
    ip_ok = None if not item.get("expect_infection_point") else item["expect_infection_point"] == actual.get("infection_point")
    exp, got = set(item.get("expect_caught_by") or []), set(actual.get("caught_by") or [])
    if item.get("expect_caught_by") is None:
        det_ok = None
    else:
        det_ok = bool(exp & got) if exp else not got
    status = actual.get("status")
    verdict = {"caught": "caught", "uncaught": "GAP: entered, not caught", "silent": "GAP: no evidence",
               "errored": "errored", "clean": "clean", "false_positive": "FALSE POSITIVE"}.get(status, status)
    return {**item, "actual": actual, "infection_point_ok": ip_ok, "detection_ok": det_ok, "verdict": verdict}


def _score(judged: list[dict]) -> dict:
    faults = [j for j in judged if j["mode"] != "NONE"]
    st = [(j.get("actual") or {}).get("status") for j in faults]
    ip = [j["infection_point_ok"] for j in faults if j.get("infection_point_ok") is not None]
    dt = [j["detection_ok"] for j in faults if j.get("detection_ok") is not None]
    return {"faults_run": len(faults), "caught": st.count("caught"), "uncaught": st.count("uncaught"),
            "silent": st.count("silent"), "errored": st.count("errored"),
            "gaps": [j["mode"] for j in faults if (j.get("actual") or {}).get("status") in ("uncaught", "silent")],
            "control_clean": all((j.get("actual") or {}).get("status") == "clean" for j in judged if j["mode"] == "NONE"),
            "hypothesis_infection_point_acc": round(sum(ip) / len(ip), 2) if ip else None,
            "hypothesis_detection_acc": round(sum(dt) / len(dt), 2) if dt else None}


# ── the loop ──────────────────────────────────────────────────────────────────

def _find(d: dict, service: str) -> dict:
    svcs = d["services"]
    if service in svcs:
        return svcs[service]
    for p in svcs.values():
        if service in (p.get("agent_name"), p.get("base"), p.get("deployment")):
            return p
    # "checkoutservice" or "checkout" finds "checkoutservice-agent"; agents win over the original service
    hits = sorted((p for p in svcs.values() if p.get("base") == base_name(service)), key=lambda p: not p.get("agent"))
    if hits:
        return hits[0]
    raise KeyError(f"service '{service}' not found (have: {', '.join(sorted(svcs))})")


def run_campaign(service: str, budget: int = 4, rounds: int = 1, strategy: str = "llm",
                 use_llm: bool = True, log=print) -> dict:
    t_start = time.time()
    d = discover()
    p = _find(d, service)
    agent = p.get("agent") or {}
    if not agent.get("url") or agent.get("error"):
        raise RuntimeError(f"{p['name']} has no reachable FaultLab agent ({agent.get('error') or 'not in AGENTS'})")
    use_llm = use_llm and llm.available()
    if strategy == "llm" and not use_llm:
        log("LLM unreachable: planning with rules")
    log(f"campaign on {p['name']} ({agent['url']}): strategy={strategy}, budget={budget}, rounds={rounds}")

    # results history: the agent's /ui/results only holds its last run, so keep every row here
    with httpx.Client(timeout=10.0) as c:
        prev = c.get(f"{agent['url']}/ui/results").json()
    rows = {r["fault_mode"]: r for r in prev.get("results", []) if r.get("fault_mode")}
    before = dict(agent.get("results") or {})

    report = {"service": p["name"], "agent_url": agent["url"], "strategy": strategy, "budget": budget,
              "llm": llm.OLLAMA_MODEL if use_llm else None, "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "rounds": [], "llm_usage": {"input_tokens": 0, "output_tokens": 0}}
    done: set = set()
    for n in range(1, rounds + 1):
        rec = recommend(p, use_llm=use_llm)
        recs = rec["recommendations"]
        _add_usage(report, rec.get("llm_usage"))
        if strategy == "all":
            plan = [{"mode": c["mode"], "why": "run every mode (manual baseline)", "source": "all",
                     **_rule_hypothesis(c)} for c in _candidates(p, done)]
            plan_err = None
        elif strategy == "llm" and use_llm:
            plan, usage, plan_err = plan_llm(p, recs, budget, done)
            _add_usage(report, usage)
        else:
            plan, plan_err = plan_rules(p, recs, budget, done), None
        if not plan:
            log(f"round {n}: nothing left to run")
            break
        modes = ["NONE"] + [x["mode"] for x in plan]
        log(f"round {n}: plan {', '.join(modes[1:])}" + (f" (LLM plan error: {plan_err})" if plan_err else ""))
        for x in plan:
            log(f"  {x['mode']} [{x['source']}]: {x['why']} -> expect {x.get('expect_infection_point') or '?'}"
                f" caught by {x.get('expect_caught_by') if x.get('expect_caught_by') is not None else '?'}")

        t = time.time()
        res = run_on_agent(agent["url"], modes, log)
        new_rows = {r["fault_mode"]: r for r in res.get("results", []) if r.get("fault_mode")}
        rows.update(new_rows)
        summary_now = _summarise_results({"results": list(new_rows.values())})
        judged = [judge({"mode": "NONE", "why": "control", "source": "control",
                         "expect_infection_point": None, "expect_caught_by": []}, summary_now["modes"].get("NONE"))]
        judged += [judge(x, summary_now["modes"].get(x["mode"])) for x in plan]
        for j in judged:
            log(f"  {j['mode']}: {j['verdict']}" + (f" at {j['actual'].get('infection_point')}" if j.get("actual") and j["actual"].get("infection_point") else "")
                + (f" by {', '.join(j['actual']['caught_by'])}" if j.get("actual") and j["actual"].get("caught_by") else ""))
        done |= {x["mode"] for x in plan}

        # 5. LEARN: the profile now carries every result so far
        agent["results"] = _summarise_results({"results": list(rows.values()), "backend": res.get("backend"),
                                               "generated_at": res.get("generated_at")})
        agent["never_run"] = [m for m in agent.get("modes", []) if m != "NONE" and m not in rows]
        report["rounds"].append({"round": n, "plan": plan, "plan_error": plan_err, "results": judged,
                                 "score": _score(judged), "backend": res.get("backend"),
                                 "elapsed_s": round(time.time() - t, 1)})

    after = recommend(p, use_llm=use_llm)
    _add_usage(report, after.get("llm_usage"))
    all_judged = [j for r in report["rounds"] for j in r["results"]]
    report.update({
        "finished_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "elapsed_s": round(time.time() - t_start, 1),
        "score": _score(all_judged),
        "coverage": {"modes": len([m for m in agent.get("modes", []) if m != "NONE"]),
                     "run_before": len([m for m in (before.get("modes") or {}) if m != "NONE"]),
                     "run_after": len([m for m in rows if m != "NONE"])},
        "next_recommendations": after["recommendations"],
        "summary": after.get("summary"),
        "canary_pick": canary_pick(agent["results"]),
        "lang": p.get("lang", "python"),
        "_profile": p,   # main.py moves this into the recommendations tab and drops it from the report
    })
    log(f"done in {report['elapsed_s']}s: {report['score']['caught']} caught, gaps: {', '.join(report['score']['gaps']) or 'none'}")
    return report


def _add_usage(report: dict, usage: dict | None):
    if usage:
        for k in ("input_tokens", "output_tokens"):
            report["llm_usage"][k] += usage.get(k, 0) or 0


# ── canary ────────────────────────────────────────────────────────────────────

MONEY_CHECKS = {"charge_total", "paid_once"}


def canary_pick(summary: dict | None) -> dict:
    """The fault to switch on for the canary's rollback demo, from the agent's evidence.

    A good pick completes the order (so it shows up as an incorrect order, not as an error the
    caller retries), fails at least one correctness check (so 'correct orders < 95%' breaks), is a
    deterministic BL_* fault (it does not depend on what the model decides on live traffic), and
    touches the charge (charge_total / paid_once fail whatever is in the loadgenerator's cart,
    while e.g. BL_ITEM_DROPPED needs more than one item). Uncaught modes are listed under 'avoid': with them the canary should NOT roll back.
    """
    modes = (summary or {}).get("modes") or {}
    best, why = None, ""
    scored = []
    for m, r in modes.items():
        if r.get("status") != "caught" or m == "NONE":
            continue
        checks = [c for c in r.get("caught_by", []) if not c.startswith(("boundary:", "agent", "checkpoints"))]
        s = (10 if r.get("order_accepted") else 0) + (6 if m.startswith("BL_") else 0) \
            + (5 if MONEY_CHECKS & set(checks) else 0) + min(len(checks), 3) + min(r.get("propagation_depth") or 0, 10) / 10
        scored.append((s, m, r, checks))
    scored.sort(key=lambda x: (-x[0], x[1]))
    if scored:
        s, best, r, checks = scored[0]
        why = (f"It completed the order but failed {', '.join(checks) or 'a check'} "
               f"(entered at {r.get('infection_point')}), so every agent order is counted incorrect and the "
               "'correct orders < 95%' threshold breaks at the first step it is on.")
    return {"fault_mode": best or "NONE", "why": why or "no caught mode in the results yet: run a campaign first",
            "expected_breach": "correct orders < 95%" if best else None,
            "alternatives": [m for _, m, _, _ in scored[1:4]],
            "avoid": sorted((summary or {}).get("uncaught", []) + (summary or {}).get("silent", [])),
            "based_on": (summary or {}).get("generated_at")}
