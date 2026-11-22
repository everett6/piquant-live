"""Unit tests for monthly_reset_audit.py (causality of circuit breaker & $1000 monthly reset)."""
from __future__ import annotations

import numpy as np
import pandas as pd

import monthly_reset_audit as MRA


def test_apply_vol_tox_circuit_breaker_strictly_causal():
    idx = pd.date_range("2023-01-01", "2024-06-30", freq="D", tz="UTC")
    rng = np.random.default_rng(101)
    ret = pd.Series(rng.normal(0.001, 0.015, size=len(idx)), index=idx)
    mm = pd.DataFrame({"micro_tox_score": rng.uniform(0.1, 0.8, size=len(idx))}, index=idx)

    s1 = MRA.apply_vol_tox_circuit_breaker(ret, mm, target_vol=0.16, max_lev=1.8)
    cut = pd.Timestamp("2024-01-01", tz="UTC")
    ret2 = ret.copy()
    mm2 = mm.copy()
    ret2[ret2.index >= cut] = -0.20
    mm2.loc[mm2.index >= cut, "micro_tox_score"] = 0.99

    s2 = MRA.apply_vol_tox_circuit_breaker(ret2, mm2, target_vol=0.16, max_lev=1.8)
    before = idx < cut
    assert np.allclose(s1[before].to_numpy(), s2[before].to_numpy(), atol=1e-12)


def test_monthly_reset_report_resets_every_month():
    idx = pd.date_range("2025-01-01", "2025-03-31", freq="D", tz="UTC")
    ret = pd.Series(0.001, index=idx)
    rep = MRA.monthly_reset_report(ret, start_cap=1000.0)
    assert rep["total_months"] == 3
    assert rep["winning_months"] == 3
    assert rep["win_rate_pct"] == 100.0
