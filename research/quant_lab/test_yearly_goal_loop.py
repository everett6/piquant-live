"""Unit tests for yearly_goal_loop.py (per-year evaluator, causal simulation, optimization loop)."""
from __future__ import annotations

import numpy as np
import pandas as pd

import yearly_goal_loop as YG


def test_evaluate_by_year_computes_worst_year_and_annualizes_partial():
    idx = pd.date_range("2024-01-01", "2026-06-30", freq="D", tz="UTC")
    rets = pd.Series(0.001, index=idx)
    # Make 2025 negative
    rets.loc[rets.index.year == 2025] = -0.0005
    res = YG.evaluate_by_year(rets, min_days=120)
    assert res["worst_year"] == 2025
    assert res["min_ann_pct"] < 0.0
    assert res["total_years"] == 3
    assert "2026" in res["years"]
    assert res["years"]["2026"]["ann_pct"] > res["years"]["2026"]["raw_pct"]


def test_simulate_config_strictly_causal():
    """Corrupting future prices (t >= cut) must not change any return at t < cut."""
    universe = YG.load_universe()
    pre1 = YG.precompute_signals(universe)
    cfg = YG.StrategyConfig(venue="long_short_perp", top_k=3, target_vol=0.35, max_lev=2.0)
    r1 = YG.simulate_config(pre1, cfg)

    # Corrupt BTC/ETH/SOL prices from 2025-06-01 onward
    cut = pd.Timestamp("2025-06-01", tz="UTC")
    for c in universe["daily"]:
        mask = universe["daily"][c].index >= cut
        universe["daily"][c].loc[mask, "c"] *= 0.25
    pre2 = YG.precompute_signals(universe)
    r2 = YG.simulate_config(pre2, cfg)

    before = r1.index < cut
    assert np.allclose(r1[before].to_numpy(), r2[before].to_numpy(), atol=1e-12)


def test_run_optimization_loop_writes_state(tmp_path):
    out = tmp_path / "loop_state.json"
    res = YG.run_optimization_loop(venue="long_short_perp", goal_ann_pct=50.0, max_iters=3, out_path=str(out))
    assert out.exists()
    assert res["iters_run"] == 3
    assert len(res["history"]) == 4
    assert "best_stats" in res and "oos_2024_2026" in res
