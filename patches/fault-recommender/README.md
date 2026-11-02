# Fault Injection Recommender

**v2 (1 Nov): automated campaigns.** The recommender no longer stops at a list. A campaign lets the LLM
choose which faults to run, runs them on the agent's FaultLab test runner, compares what happened with
what it predicted, and re-ranks from the new evidence. Faults that get in but pass every check come back
as top-priority detection gaps, and the campaign names the fault the canary workflow should use for its
rollback run. See [Automated campaigns](#automated-campaigns) below. The first scan-only version is
described after that and still works the same way.

## Automated campaigns

```
                 ┌──────────────────────────────────────────────────────────────┐
                 │                                                              │
  1. RECOMMEND   recommend(profile)          rules + LLM, as before             │
        │                                                                       │
  2. PLAN        LLM picks up to `budget` of the agent's existing modes and     │
        │        predicts, for each, the infection point and the check that     │
        │        should catch it (rules fill in if the LLM fails or repeats)    │
        │                                                                       │
  3. RUN         POST {agent}/ui/run ["NONE", ...picks]; poll /ui/status;       │
        │        GET /ui/results          (test runner only, never FAULT_MODE)  │
        │                                                                       │
  4. JUDGE       prediction vs result per mode: infection point right?          │
        │        caught as predicted?  entered but uncaught → GAP               │
        │                                                                       │
  5. LEARN       merge results into the profile ─────── next round ─────────────┘
        │
        └─► report: score, gaps, next recommendations, canary_pick
```

| Strategy | Who picks the faults | Use it for |
|---|---|---|
| `llm` | Ollama (llama3.1), from the candidates, last results, checkpoints and checks | the automated method |
| `rules` | deterministic: never run > uncaught > silent > errored, spread over stages and FM/BL | LLM-free baseline |
| `all` | every mode | the old manual "run everything" baseline |

Each campaign is saved, and `GET /api/campaigns` gives one row per campaign (faults run, caught, gaps,
prediction accuracy, LLM tokens, time), which is the comparison table for the report:
how many runs each strategy needs to find the same gaps, and how often the LLM's predictions were right.

**What "caught" means now.** v1 counted any evidence (steps_lost, infection_point) as detected. The
checkout agent also reports which correctness checks failed, so results are now split into
`caught` (a check or a boundary alert flagged it), `uncaught` (it entered but every check passed, e.g.
FM_3_2), `silent` (no trace) and `errored`. The Python agents have no checks, so for them evidence
still counts as caught, as before.

**Canary pick.** `GET /api/canary-fault/checkoutservice-agent` returns the fault to switch on for the
rollback run (item 1b): a fault that was caught, still completes the order (so it counts as an incorrect
order, not an error), is a deterministic BL_* fault and fails a money check (`charge_total`/`paid_once`),
so `correct orders < 95%` breaks. It also lists the uncaught modes to avoid, because with those the
canary should *not* roll back. `canary-auto-fault.patch` adds `fault_mode: AUTO` to
`canary_rollout.yml`, which asks the recommender and falls back to BL_WRONG_TOTAL if it has no pick.

**Go stubs.** The checkout agent is Go, so its stubs are now `case` lines for `dispatch()` in
`agent/faults.go` (set per agent with `AGENT_LANG`, default `{"checkout": "go"}`).

### Run a campaign

From the **★ recommendations** tab (or the recommender's own page): pick the agent, the strategy, a budget
and rounds, then **Run campaign**. Or from inside the cluster:

```bash
kubectl exec deploy/fault-recommender -- python -c "import httpx;print(httpx.post('http://localhost:8080/api/campaign',json={'service':'checkoutservice-agent','strategy':'llm','budget':4,'rounds':2}).text)"
kubectl exec deploy/fault-recommender -- python -c "import httpx;print(httpx.get('http://localhost:8080/api/campaign').json()['log'])"
kubectl exec deploy/fault-recommender -- python -c "import httpx;print(httpx.get('http://localhost:8080/api/campaigns').text)"
kubectl exec deploy/fault-recommender -- python -c "import httpx;print(httpx.get('http://localhost:8080/api/canary-fault/checkoutservice-agent').text)"
```

With the agent's default `FAULTLAB_RUNNER=live`, every mode is a real test order through Ollama (minutes
each on CPU), so keep the budget small or set `kubectl set env deploy/checkoutservice-agent FAULTLAB_RUNNER=mock`
for campaigns (seconds). To compare strategies fairly, start each from the same state: the campaign reads
the agent's last results as its history.

### Tested (1 Nov)

Against the real checkout agent v6 binary with `FAULTLAB_RUNNER=mock` and a fake Ollama, plus
`tests/test_offline.py` (`python -m pytest -q tests`, 6 pass). One campaign per strategy, budget 4, fresh agent each:

| Strategy | Faults run | Caught | Gaps found | Predictions right (infection point) |
|---|---|---|---|---|
| llm | 4 | 3 | FM_3_2 | 3/3 |
| rules | 4 | 3 | FM_3_2 | n/a (no history) |
| all (manual baseline) | 15 | 14 | FM_3_2 | n/a |

The fake Ollama gives fixed answers, so this shows the loop works, not how good llama3.1's choices are.
That needs a run on the cluster.


A new in-cluster service (`fault-recommender`, ClusterIP :8080) that scans the services in the
namespace and recommends which faults to inject next, with a reason for each one. It has the same
shape as the agents (FastAPI + uvicorn, Ollama in-cluster) and appears as a **★ recommendations**
tab in the FaultLab dashboard.

## How it works

```
 K8s API (read-only)          AGENTS ConfigMap            demo.proto catalog
 Services, Deployments,       each agent's /ui/modes      RPCs + payload shapes
 Pods, *_ADDR env vars        and /ui/results
          \                          |                          /
           └──────────────►  1. DISCOVER: one profile per service  ◄──┘
                              (call graph, fan-in, existing modes,
                               which modes showed evidence / were silent)
                                         │
                              2. RULES (always run, no LLM needed)
                                 • existing modes that ran but left no evidence → fix detection first
                                 • MAST modes the agent doesn't cover yet (FM_1_3, FM_2_4, FM_3_2, ...)
                                 • business-logic ideas per Boutique RPC (BL_WRONG_RATE, BL_DOUBLE_CHARGE, ...)
                                 • INF_LATENCY / INF_UNAVAILABLE scaled by how many services call this one
                                         │
                              3. LLM (Ollama llama3, JSON mode)
                                 gets the profile + rule output, adds service-specific faults
                                 and a one-line risk summary; bad ids are dropped
                                         │
                              4. MERGE + RANK: dedupe, mark modes that already exist,
                                 score = priority + blast radius (+ detection gaps first),
                                 attach a paste-ready maybe_* stub for fault_injection.py
                                         │
                              /api/recommendations  →  dashboard tab / own page at /
```

Each recommendation has: `id` (FM_x_y / BL_* / INF_*), `title`, `category`, `target` RPC,
`inject` (what the code does), `expected_symptom`, `detect` (the checkpoint that should catch it),
`priority`, `rationale`, `source` (rules or llm), `status` (new / exists) and `stub`.

If Ollama is down or slow, the scan still finishes with rule-based recommendations, and the log says why the LLM was skipped.

## Files

| File | What |
|---|---|
| `app/main.py` | API, background scan with lock (same pattern as `/ui/run`), optional schedule |
| `app/discovery.py` | K8s API + AGENTS probing, builds the call graph |
| `app/recommender.py` | rules, LLM prompt, merge/score, code stubs (Python and Go) |
| `app/campaign.py` | automated campaign: plan, run, judge, learn; canary pick |
| `tests/test_offline.py` | offline tests on the checkout agent's sample results |
| `canary-auto-fault.patch` | adds `fault_mode: AUTO` to `canary_rollout.yml` |
| `app/catalog.py` | demo.proto RPCs, MAST taxonomy, infra faults |
| `app/llm.py` | Ollama `/api/chat` call (JSON mode) |
| `app/ui.html` | standalone page |
| `k8s-fault-recommender.yaml` | ServiceAccount, read-only Role, Deployment, Service |
| `dashboard/main.py`, `dashboard/index.html` | updated FaultLab dashboard (full files) |
| `dashboard/dashboard.diff` | the same change as a diff against your dashboard before the recommender (v1 + v2) |
| `dashboard/campaign-ui.diff` | only the v2 campaign box, if you already applied v1 |
| `dashboard/campaign-screenshot.png` | the campaign box in the dashboard tab (local test) |

## Deploy

1. Build and push the recommender image:
   ```bash
   cd fault-recommender
   docker build -t ghcr.io/pauriccarr/engr-fault-recommender:latest .
   docker push ghcr.io/pauriccarr/engr-fault-recommender:latest
   ```
2. Apply it in the Boutique namespace. It reads `AGENTS` from your existing `faultlab-dashboard` ConfigMap,
   so new agents show up in both places.
   ```bash
   kubectl apply -f k8s-fault-recommender.yaml
   kubectl rollout status deploy/fault-recommender
   kubectl logs deploy/fault-recommender -f     # first scan runs on start
   ```
3. Update the dashboard: copy `dashboard/main.py` and `dashboard/index.html` over yours (or apply
   `dashboard.diff`), rebuild and push the dashboard image, then:
   ```bash
   kubectl set env deploy/faultlab-dashboard RECOMMENDER_URL=http://fault-recommender:8080
   kubectl rollout restart deploy/faultlab-dashboard
   ```
   Open the dashboard and click **★ recommendations** (the old IP 23.236.60.215 was on the previous cluster;
   check the new one with `kubectl get svc faultlab-dashboard`). If `RECOMMENDER_URL` isn't set, the tab is hidden and the dashboard works as before.

## Check it from inside the cluster

```bash
kubectl exec deploy/faultlab-dashboard -- python -c "import httpx;print(httpx.get('http://fault-recommender:8080/api/services').text[:800])"
kubectl exec deploy/faultlab-dashboard -- python -c "import httpx;print(httpx.post('http://fault-recommender:8080/api/scan',json={'llm':False}).text)"
kubectl exec deploy/faultlab-dashboard -- python -c "import httpx;print(httpx.get('http://fault-recommender:8080/api/recommendations/currencyagent/stubs').text)"
```

If `/api/services` says `"source": "agents-only"`, the K8s API call failed. Check the RoleBinding
(`kubectl auth can-i list deployments --as=system:serviceaccount:<ns>:fault-recommender`).

## API

| Method | Path | |
|---|---|---|
| GET | `/api/services` | live discovery only (fast, no LLM) |
| POST | `/api/scan` | `{"services": ["currencyagent"], "llm": true}`; both fields are optional |
| GET | `/api/status` | `running`, `progress`, `log` |
| GET | `/api/recommendations` | latest scan, all services |
| GET | `/api/recommendations/{svc}` | one service, with its profile |
| GET | `/api/recommendations/{svc}/stubs` | paste-ready `maybe_*` helpers + new `ALL_MODES` line (Go `case` lines for checkout) |
| POST | `/api/campaign` | `{"service": "checkoutservice-agent", "strategy": "llm\|rules\|all", "budget": 4, "rounds": 1, "llm": true}` |
| GET | `/api/campaign` | current campaign: `running`, `log`, `error`, and the `latest` report |
| GET | `/api/campaigns` | one summary row per saved campaign (for the report) |
| GET | `/api/canary-fault/{svc}` | the fault for the canary rollback run, with reason, alternatives and modes to avoid |

## Env

| Var | Default | |
|---|---|---|
| `AGENTS` | `{}` | from ConfigMap `faultlab-dashboard` |
| `OLLAMA_SERVICE_URL` | `http://ollama:11434` | the manifest now uses `http://ollama:11434` (Ollama is in the shop's namespace on the new cluster) |
| `OLLAMA_MODEL` | `llama3` | the manifest sets `llama3.1` |
| `AGENT_RUN_TIMEOUT` | `3600` | seconds a campaign waits for one agent run |
| `AGENT_POLL_SECONDS` | `3` | how often a campaign polls the agent's `/ui/status` |
| `AGENT_LANG` | `{"checkout": "go"}` | language of each agent's fault code, for stubs |
| `LLM_TIMEOUT` | `240` | seconds per service (llama3 on CPU is slow) |
| `SCAN_ON_START` | `true` | |
| `SCAN_INTERVAL` | `0` | seconds between automatic scans; 0 = off |
| `EXCLUDE` | `kubernetes,faultlab-dashboard,fault-recommender,edge-proxy,frontend-external,loadgenerator` | services to skip |

## Notes

- The Role is read-only (`get`/`list` on services, pods, deployments). The recommender never changes
  `FAULT_MODE`, so it only suggests. You still decide what to implement and run from the dashboard.
- A full LLM scan makes one Ollama call per service, so expect a few minutes for ~12 services on CPU.
  **Scan (rules only)** finishes in about a second.
- Results live in an emptyDir, so they reset when the pod restarts and the first scan runs again on start.
- Tested locally against fake agents (real productcatalog results JSON), a fake Ollama and fake
  K8s API responses, and (v2) against the real checkout agent binary on its mock runner. It has not been run on GKE yet.
- Campaigns only run modes the agent already has. New faults it recommends still need code (use the stubs);
  after you add one, the next campaign picks it up as "never run".
- The recommender keeps campaigns in the same emptyDir, so they reset when its pod restarts.
