"""Offline checks (no cluster, no Ollama, no agent):  python -m pytest -q tests  (from fault-recommender/)"""
import json
from pathlib import Path

from app.campaign import _score, canary_pick, judge, plan_rules
from app.discovery import _summarise_results
from app.recommender import recommend

SAMPLE = json.loads((Path(__file__).parent / "checkout_results_sample.json").read_text())


def profile(results=None):
    modes = [r["fault_mode"] for r in SAMPLE["results"]]
    return {"name": "checkoutservice-agent", "base": "checkout", "lang": "go", "is_agent": True,
            "called_by": ["frontend"], "depends_on": [], "grpc": {"methods": ["PlaceOrder"]},
            "agent": {"url": "http://x", "modes": modes, "checkpoints": [], "results": results}}


def test_summary_separates_caught_from_uncaught():
    s = _summarise_results(SAMPLE)
    assert s["uncaught"] == ["FM_3_2"]          # entered at ORDER_VERIFIED, every check passed
    assert len(s["detected"]) == 14 and not s["silent"] and not s["errored"]
    assert s["modes"]["NONE"]["status"] == "clean"
    assert "charge_total" in s["detectors"]


def test_python_agent_rows_without_checks_still_count_evidence():
    s = _summarise_results({"results": [{"fault_mode": "FM_2_4", "steps_lost": ["BOUNDARY_CHECK"]},
                                        {"fault_mode": "BL_X", "steps_lost": []}]})
    assert s["detected"] == ["FM_2_4"] and s["silent"] == ["BL_X"]


def test_uncaught_mode_becomes_top_detection_gap_with_go_stubs():
    out = recommend(profile(_summarise_results(SAMPLE)), use_llm=False)
    top = out["recommendations"][0]
    assert top["id"] == "FM_3_2" and top["category"] == "DETECTION"
    assert all("case mode ==" in r["stub"] for r in out["recommendations"] if r.get("stub"))


def test_rules_plan_prefers_never_run_then_gaps_and_spreads_stages():
    p = profile(None)
    plan = plan_rules(p, [], 4, set())
    assert len(plan) == 4 and all(x["why"] == "never run yet" for x in plan)
    p = profile(_summarise_results(SAMPLE))
    assert plan_rules(p, [], 1, set())[0]["mode"] == "FM_3_2"


def test_judge_and_score():
    s = _summarise_results(SAMPLE)["modes"]
    j1 = judge({"mode": "BL_WRONG_TOTAL", "expect_infection_point": "PAYMENT_CHARGED", "expect_caught_by": ["charge_total"]}, s["BL_WRONG_TOTAL"])
    j2 = judge({"mode": "FM_3_2", "expect_infection_point": "FINAL_ANSWER", "expect_caught_by": ["charge_total"]}, s["FM_3_2"])
    assert j1["infection_point_ok"] and j1["detection_ok"] and j1["verdict"] == "caught"
    assert j2["infection_point_ok"] is False and j2["detection_ok"] is False and j2["verdict"].startswith("GAP")
    sc = _score([j1, j2])
    assert sc["gaps"] == ["FM_3_2"] and sc["hypothesis_detection_acc"] == 0.5


def test_canary_pick_is_a_caught_money_fault_and_avoids_gaps():
    pick = canary_pick(_summarise_results(SAMPLE))
    assert pick["fault_mode"] == "BL_SKIP_PAYMENT"
    assert pick["avoid"] == ["FM_3_2"]
    assert canary_pick(None)["fault_mode"] == "NONE"
