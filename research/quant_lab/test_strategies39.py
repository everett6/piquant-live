"""Synthetic unit tests for PREREG39 (strategies39.py). Zero real market data read."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies39 as S39


def test_vol_calm_multiplier_bounds_and_causality():
    idx = pd.date_range("2025-01-01", periods=80, freq="D", tz="UTC")
    rng = np.random.default_rng(7)
    # First 40 days calm (+0.5% alternating noise ~ 10% ann vol), last 40 days violent (~150% ann vol)
    r_calm = 0.002 + rng.normal(0.0, 0.005, size=40)
    r_wild = rng.normal(0.0, 0.08, size=40)
    px = pd.Series(100.0 * np.cumprod(1.0 + np.concatenate([r_calm, r_wild])), index=idx)

    m = S39.vol_calm_multiplier(px)
    assert (m.iloc[:20] == 1.0).all()
    assert abs(m.iloc[35] - S39.MULT_MAX) < 1e-9  # calm uptrend hits 1.35x cap
    assert m.iloc[-1] <= 0.65 and m.iloc[-1] >= S39.MULT_MIN  # wild regime cuts hard toward 0.50x

    # Causality: shocking future rows does not alter earlier multipliers
    px2 = px.copy()
    px2.iloc[36:] *= 0.1
    m2 = S39.vol_calm_multiplier(px2)
    assert np.allclose(m.iloc[:36].to_numpy(), m2.iloc[:36].to_numpy())
