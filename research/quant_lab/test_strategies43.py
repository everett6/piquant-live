"""Synthetic unit tests for PREREG43 helpers."""
from __future__ import annotations

import os
import sys

import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import strategies43 as S43


def _synth_ohlc(n: int = 90, drift: float = 0.002) -> pd.DataFrame:
    idx = pd.date_range("2024-01-01", periods=n, freq="1D", tz="UTC")
    px = 100.0 * np.exp(np.cumsum(np.full(n, drift)))
    return pd.DataFrame({"o": px, "h": px * 1.01, "l": px * 0.99, "c": px, "v": 1000.0}, index=idx)


def test_asymmetric_dualspeed_and_causality():
    btc = _synth_ohlc(80, 0.002)
    eth = _synth_ohlc(80, 0.0015)
    idx = btc.index
    qd = pd.Series(1.0, index=idx)
    r3 = pd.Series(0.5, index=idx)
    mix = S43.asymmetric_dualspeed_positions(qd, r3, qd_fraction=25.0 / 45.0)
    expected = (25.0 / 45.0) * 1.0 + (20.0 / 45.0) * 0.5
    assert np.allclose(mix.to_numpy(), expected)

    # Strict causality: perturbing closes on last day must not alter prior daily returns or taper mults.
    closes = pd.DataFrame({"BTC": btc["c"], "ETH": eth["c"]}, index=idx)
    lw = pd.DataFrame({"BTC": 0.5, "ETH": 0.5}, index=idx)
    carry = pd.Series(0.0002, index=idx)
    rate = pd.Series(4.5, index=idx)

    ret1, mult1, diag1 = S43.evaluate_prereg43_variant(
        btc, eth, mix, mix, closes, lw, carry, rate,
        trend_share=0.45, leader_share=0.20, carry_share=0.35,
        taker_bps=2.0, slip_bps=1.0, funding_bps_h=0.0, spot_threshold=0.65,
    )
    btc2 = btc.copy()
    btc2.iloc[-1, btc2.columns.get_loc("c")] *= 0.70
    ret2, mult2, _ = S43.evaluate_prereg43_variant(
        btc2, eth, mix, mix, closes, lw, carry, rate,
        trend_share=0.45, leader_share=0.20, carry_share=0.35,
        taker_bps=2.0, slip_bps=1.0, funding_bps_h=0.0, spot_threshold=0.65,
    )
    assert np.allclose(ret1.iloc[:-1].to_numpy(), ret2.iloc[:-1].to_numpy())
    assert np.allclose(mult1.iloc[:-1].to_numpy(), mult2.iloc[:-1].to_numpy())
    assert diag1["mean_dd_pct"] >= 0.0 and diag1["ulcer_index_pct"] >= diag1["mean_dd_pct"]
