"""Synthetic-data tests for run_round59 (PREREG59). No real data."""
from __future__ import annotations

import numpy as np
import pandas as pd
import pytest

import etf_discover as ED
import run_round59 as R


def _closes(n=700, seed=4):
    return ED.make_synthetic_etf_closes(n=n, seed=seed)


def test_universes_match_prereg():
    assert len(R.E1) == 27 and "SHY" not in R.E1
    assert len(R.E2) == 19 and len(set(R.E2)) == 19
    assert not set(R.E2) & {"TLT", "IEF", "GLD", "UUP", "HYG"}


def test_exposure_bounded_and_lagged():
    c = _closes()
    r, held = R.simulate(c, R.E1, None, 2.0)
    gross = held.sum(axis=1)
    assert (held.to_numpy() >= -1e-12).all() and (gross <= 1.0 + 1e-9).all()
    assert gross.iloc[:50].max() == 0.0  # SMA50 first exists at bar 49, held from bar 50
    assert np.isfinite(r).all()


def test_causal_future_shock():
    c = _closes()
    c2 = c.copy()
    c2.iloc[500:] *= np.linspace(0.3, 3.0, c.shape[1])
    r1, h1 = R.simulate(c, R.E1, None, 2.0)
    r2, h2 = R.simulate(c2, R.E1, None, 2.0)
    pd.testing.assert_frame_equal(h1.iloc[:501], h2.iloc[:501])  # held at t uses closes <= t-1
    pd.testing.assert_series_equal(r1.iloc[:500], r2.iloc[:500])


def test_late_listing_gets_no_weight_until_it_trades():
    c = _closes()
    c.loc[c.index[:400], "XLC"] = np.nan
    _, held = R.simulate(c, R.E2, None, 2.0)
    assert held["XLC"].iloc[:451].abs().max() == 0.0


def test_costs_and_cash():
    c = _closes()
    rate = pd.Series(0.05, index=c.index)
    r0, _ = R.simulate(c, R.E1, None, 0.0)
    r_cost, _ = R.simulate(c, R.E1, None, 50.0)
    r_cash, held = R.simulate(c, R.E1, rate, 0.0)
    assert r_cost.sum() < r0.sum()
    idle = (1.0 - held.sum(axis=1)).clip(lower=0.0)
    expect = (idle * 0.05 / 252).shift(0)
    # cash accrual uses the previous day's rate on today's idle share
    assert (r_cash - r0).iloc[1:].to_numpy() == pytest.approx(expect.iloc[1:].to_numpy(), abs=1e-12)


def test_taper_cuts_exposure_in_drawdown():
    idx = pd.bdate_range("2015-01-02", periods=400, tz="UTC")
    up = np.linspace(100, 200, 300)
    down = np.linspace(200, 120, 100)
    c = pd.DataFrame({"SPY": np.r_[up, down], "QQQ": np.r_[up, down] * 1.01}, index=idx)
    c = c * (1 + 0.002 * np.sin(np.arange(400)))[:, None]
    _, held = R.simulate(c, ("SPY", "QQQ"), None, 0.0)
    assert held.sum(axis=1).iloc[280:300].max() > 0.5
    assert held.sum(axis=1).iloc[-1] < 0.05


def test_checks_logic():
    b = {"sharpe": 0.7, "max_dd_pct": -50.0, "cagr_pct": 12.0}
    s = {"sharpe": 0.85, "max_dd_pct": -30.0, "cagr_pct": 10.5, "deflated_sharpe": 0.9, "recent_return_pct": 5.0}
    assert all(R.check(s, {"cagr_pct": 8.0}, b).values())
    c = R.check({**s, "cagr_pct": 9.9, "sharpe": 0.79}, {"cagr_pct": -1.0}, b)
    assert not c["1_sharpe_edge_0.10"] and not c["3_cagr_within_2pts"] and not c["5_stress_and_recent_positive"]


def test_run_trial_synthetic():
    c = _closes(n=900)
    c.index = pd.bdate_range("2006-06-01", periods=900, tz="UTC")
    out = R.run_trial(c, R.E2, {"SPY": 1.0}, None)
    assert set(out["checks"]) == {"1_sharpe_edge_0.10", "2_shallower_dd", "3_cagr_within_2pts",
                                  "4_dsr_0.80", "5_stress_and_recent_positive"}
    assert 0.0 <= out["report"]["mean_gross_exposure"] <= 1.0
