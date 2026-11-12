"""Synthetic tests for strategies20 (PREREG20)."""
import numpy as np
import pandas as pd

import strategies20 as S


def series(n=500, seed=1, beta=0.8):
    rng = np.random.default_rng(seed)
    idx = pd.date_range("2020-01-01", periods=n, freq="D", tz="UTC")
    re = pd.Series(rng.normal(0, 0.04, n), index=idx)
    rb = beta * re + pd.Series(rng.normal(0, 0.005, n), index=idx)
    return rb, re


def test_hedge_ratio_recovers_beta_and_clips():
    rb, re = series()
    b = S.hedge_ratio(rb, re)
    assert b.iloc[:59].isna().all() and abs(b.iloc[-1] - 0.8) < 0.05
    assert S.hedge_ratio(3 * re, re).iloc[-1] == 1.5 and S.hedge_ratio(-re, re).iloc[-1] == 0.0


def test_book_is_hedged_and_causal():
    rb, re = series()
    beta = S.hedge_ratio(rb, re)
    on = pd.Series(True, index=rb.index)
    bk = S.book_returns(rb, re, beta, on, 0.0)
    assert bk["gross"].iloc[61:].std() < 0.25 * rb.iloc[61:].std()  # most of the common move is gone
    assert not bk["held"].iloc[59] and bk["held"].iloc[60]           # decided at close 59, held over day 60
    # position over day t+1 uses beta_t only: changing a later return must not change earlier book returns
    rb2 = rb.copy()
    rb2.iloc[300] += 0.5
    bk2 = S.book_returns(rb2, re, S.hedge_ratio(rb2, re), on, 0.0)
    assert np.allclose(bk["r"].iloc[:300], bk2["r"].iloc[:300])


def test_costs_and_funding():
    idx = pd.date_range("2020-01-01", periods=4, freq="D", tz="UTC")
    z = pd.Series(0.0, index=idx)
    beta = pd.Series(1.0, index=idx)
    on = pd.Series([False, True, True, False], index=idx)
    bk = S.book_returns(z, z, beta, on, 1.0)
    assert abs(bk["r"].iloc[1] + 2 * S.FEE) < 1e-12                        # open both legs
    assert abs(bk["r"].iloc[2] + 2 * 1e-4 * 24) < 1e-12                     # funding on both legs
    assert abs(bk["r"].iloc[3] + 2 * S.FEE + 2 * 1e-4 * 24) < 1e-12         # held that day, then closed


def test_spike_flag_and_pass_rule():
    rng = np.random.default_rng(3)
    idx = pd.date_range("2018-01-01", periods=900, freq="D", tz="UTC")
    r = pd.Series(rng.normal(0, 0.02, 900), index=idx)
    r.iloc[800:830] *= 5
    f = S.spike_flag(r)
    assert not f.iloc[:395].any() and f.iloc[815:830].all()
    good, ok = {"sharpe": 1.2}, {"sharpe": 0.6}
    assert S.passes(good, ok, 4, -0.05, -0.17)
    assert not S.passes(good, ok, 4, -0.10, -0.17)   # tail worse than half of BTC's
    assert not S.passes({"sharpe": 0.9}, ok, 5, -0.05, -0.17)
