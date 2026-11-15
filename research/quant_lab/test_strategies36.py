"""Synthetic unit tests for PREREG36 (strategies36.py). Run BEFORE reading any real data."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies35 as S35
import strategies36 as S36


def _make_pair(n_days: int = 160, seed: int = 11):
    rng = np.random.default_rng(seed)
    idx = pd.date_range("2022-01-01", periods=n_days, freq="D", tz="UTC")
    b = 100.0 * np.exp(np.cumsum(0.002 + 0.02 * rng.standard_normal(n_days)))
    e = 50.0 * np.exp(np.cumsum(0.001 + 0.025 * rng.standard_normal(n_days)))
    btc = pd.DataFrame({"c": b, "o": b, "h": b, "l": b, "v": np.ones(n_days)}, index=idx)
    eth = pd.DataFrame({"c": e, "o": e, "h": e, "l": e, "v": np.ones(n_days)}, index=idx)
    return btc, eth


def test_strict_causality_future_shock():
    btc_a, eth_a = _make_pair(160)
    btc_b, eth_b = btc_a.copy(), eth_a.copy()
    btc_b.iloc[121:] *= 4.0
    eth_b.iloc[121:] *= 0.25

    pb_a = pd.Series(S35.coherence_positions(btc_a, lev=1.0), index=btc_a.index)
    pe_a = pd.Series(S35.coherence_positions(eth_a, lev=1.0), index=eth_a.index)
    pb_b = pd.Series(S35.coherence_positions(btc_b, lev=1.0), index=btc_b.index)
    pe_b = pd.Series(S35.coherence_positions(eth_b, lev=1.0), index=eth_b.index)

    carry = pd.Series(0.0002, index=btc_a.index)
    rf = pd.Series(4.5, index=btc_a.index)

    ra, ma, la = S36.simulate_qd_carry_portfolio(btc_a, eth_a, pb_a, pe_a, carry, rf, 0.7, 0.3, 6.0, 1.0, 0.1)
    rb, mb, lb = S36.simulate_qd_carry_portfolio(btc_b, eth_b, pb_b, pe_b, carry, rf, 0.7, 0.3, 6.0, 1.0, 0.1)
    pd.testing.assert_series_equal(ra.iloc[:121], rb.iloc[:121])
    pd.testing.assert_series_equal(ma.iloc[:121], mb.iloc[:121])
    pd.testing.assert_series_equal(la.iloc[:121], lb.iloc[:121])


def test_matches_lab_position_returns_when_untapered_no_carry_no_bills():
    btc, eth = _make_pair(140)
    pb = pd.Series(S35.coherence_positions(btc, lev=1.0), index=btc.index)
    pe = pd.Series(S35.coherence_positions(eth, lev=1.0), index=eth.index)
    carry = pd.Series(0.0, index=btc.index)
    rf = pd.Series(0.0, index=btc.index)

    r36, mult, _ = S36.simulate_qd_carry_portfolio(
        btc, eth, pb, pe, carry, rf, 1.0, 0.0, 6.0, 1.0, 0.1, use_taper=False
    )
    ref = S5.two_sleeve(
        lab.position_returns(btc, pb.to_numpy(), lab.US_FUTURES),
        lab.position_returns(eth, pe.to_numpy(), lab.US_FUTURES),
    )
    assert (mult == 1.0).all()
    np.testing.assert_allclose(r36.to_numpy(), ref.to_numpy(), atol=1e-12)


def test_taper_non_locking_and_idle_tbills_accrual():
    idx = pd.date_range("2024-01-01", periods=10, freq="D", tz="UTC")
    # Flat zero price return on BTC & ETH -> zero trend positions -> 100% idle trend cash
    px = np.full(10, 100.0)
    df = pd.DataFrame({"c": px, "o": px, "h": px, "l": px, "v": np.ones(10)}, index=idx)
    zero_pos = pd.Series(0.0, index=idx)
    carry = pd.Series(0.001, index=idx)  # +10 bps/day carry
    rf = pd.Series(3.65, index=idx)      # 0.01%/day = 1 bp/day when 100% idle

    r, mult, eff = S36.simulate_qd_carry_portfolio(df, df, zero_pos, zero_pos, carry, rf, 0.7, 0.3, 0.0, 0.0, 0.0)
    assert (mult == 1.0).all()
    assert (eff == 0.0).all()
    # Day 0: carry earns 0.3 * 0.001 = 0.0003; Days 1..9 earn carry + 0.7 * (3.65/100/365)
    assert np.isclose(r.iloc[0], 0.0003)
    assert np.isclose(r.iloc[1], 0.0003 + 0.7 * 0.0001)

    # Check taper boundaries & never below 0.40 floor
    assert S36.soft_taper(0.0) == 1.0
    assert S36.soft_taper(0.10) == 1.0
    assert np.isclose(S36.soft_taper(0.19), 0.70)
    assert np.isclose(S36.soft_taper(0.28), 0.40)
    assert np.isclose(S36.soft_taper(0.75), 0.40)
