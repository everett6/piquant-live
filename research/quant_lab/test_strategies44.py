"""Synthetic unit tests for PREREG44 helpers."""
from __future__ import annotations

import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import strategies44 as S44


def _make_daily(closes: np.ndarray) -> pd.DataFrame:
    idx = pd.date_range("2024-01-01", periods=len(closes), freq="1D", tz="UTC")
    return pd.DataFrame({"o": closes, "h": closes * 1.01, "l": closes * 0.99, "c": closes, "v": 1000.0}, index=idx)


def test_vol_balanced_and_bh_harvester_bounds_and_direction():
    # Low volatility steady rise -> vol_balanced_1x_2p5x hits 2.5x when base_pos == 1, 0 when base_pos == 0
    low_vol_px = 100.0 * np.exp(np.cumsum(np.full(60, 0.002)))
    d_low = _make_daily(low_vol_px)
    base = pd.Series(1.0, index=d_low.index)
    base.iloc[:10] = 0.0
    vb = S44.vol_balanced_1x_2p5x_positions(d_low, base, vol_target_ann=0.75, min_lev=1.0, max_lev=2.5)
    assert np.all(vb.iloc[:10].to_numpy() == 0.0)
    assert np.all((vb.iloc[10:].to_numpy() >= 1.0) & (vb.iloc[10:].to_numpy() <= 2.5))
    assert vb.iloc[-1] == 2.5

    # High volatility alternating +-8% -> vol_balanced drops to 1.0x floor
    alt_rets = np.where(np.arange(60) % 2 == 0, 0.08, -0.07)
    d_hi = _make_daily(100.0 * np.cumprod(1.0 + alt_rets))
    vb_hi = S44.vol_balanced_1x_2p5x_positions(d_hi, pd.Series(1.0, index=d_hi.index), vol_target_ann=0.75)
    assert np.isclose(vb_hi.iloc[-1], 1.0)

    # BH Harvester buys dips (> 1.0) and trims rips (< 1.0)
    px = np.full(40, 100.0)
    px[20:30] = np.linspace(100.0, 120.0, 10)  # rip
    px[30:40] = np.linspace(120.0, 92.0, 10)   # sharp dip
    d_bh = _make_daily(px)
    bh = S44.bh_vol_harvester_positions(d_bh, base_lev=1.0, min_mult=0.65, max_mult=1.35)
    assert np.all((bh.to_numpy() >= 0.65) & (bh.to_numpy() <= 1.35))
    assert bh.iloc[29] < 1.0  # trimmed on rip
    assert bh.iloc[39] > 1.0  # boosted on dip


def test_strict_causality_no_same_day_lookahead():
    rng = np.random.default_rng(42)
    px_b = 100.0 * np.cumprod(1.0 + rng.normal(0.001, 0.02, 80))
    px_e = 50.0 * np.cumprod(1.0 + rng.normal(0.001, 0.025, 80))
    btc1 = _make_daily(px_b)
    eth1 = _make_daily(px_e)
    idx = btc1.index
    base = pd.Series(1.0, index=idx)
    tb1 = S44.vol_balanced_1x_2p5x_positions(btc1, base)
    te1 = S44.vol_balanced_1x_2p5x_positions(eth1, base)
    bb1 = S44.bh_vol_harvester_positions(btc1)
    be1 = S44.bh_vol_harvester_positions(eth1)
    carry = pd.Series(0.0002, index=idx)
    rate = pd.Series(4.5, index=idx)

    r1, m1, l1 = S44.simulate_bh_vol_balanced_portfolio(
        btc1, eth1, tb1, te1, bb1, be1, carry, rate,
        trend_share=0.55, bh_share=0.20, carry_share=0.25,
        taker_bps=6.0, slip_bps=1.0, funding_bps_h=0.1, spot_threshold=0.75,
    )

    # Shock day 60 onwards -> all returns, taper multipliers, and effective leverage prior to day 60 must be identical
    px_b2 = px_b.copy()
    px_b2[60:] *= 0.45
    btc2 = _make_daily(px_b2)
    tb2 = S44.vol_balanced_1x_2p5x_positions(btc2, base)
    bb2 = S44.bh_vol_harvester_positions(btc2)
    r2, m2, l2 = S44.simulate_bh_vol_balanced_portfolio(
        btc2, eth1, tb2, te1, bb2, be1, carry, rate,
        trend_share=0.55, bh_share=0.20, carry_share=0.25,
        taker_bps=6.0, slip_bps=1.0, funding_bps_h=0.1, spot_threshold=0.75,
    )
    assert np.allclose(r1.iloc[:60].to_numpy(), r2.iloc[:60].to_numpy())
    assert np.allclose(m1.iloc[:60].to_numpy(), m2.iloc[:60].to_numpy())
    assert np.allclose(l1.iloc[:60].to_numpy(), l2.iloc[:60].to_numpy())
