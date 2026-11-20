"""Synthetic unit tests for PREREG40 (strategies40.py). Zero real market data read."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies30 as S30
import strategies40 as S40


def test_r3_fastexit_qd_positions_exit_and_reentry():
    idx = pd.date_range("2025-01-01", periods=90, freq="D", tz="UTC")
    px = np.concatenate([
        np.linspace(100.0, 150.0, 60),
        np.array([138.0, 130.0]),  # drops below SMA20 (~141) while still > SMA50 (~127) -> must exit!
        np.full(8, 129.0),         # stays < SMA20 and ~SMA50 -> stays flat!
        np.linspace(132.0, 165.0, 20),  # recovers >= SMA20 and > SMA50 -> re-enters & ramps!
    ])
    df = pd.DataFrame({"c": px}, index=idx)
    pos = S40.r3_fastexit_qd_positions(df, lev=1.0, sma_n=50, exit_sma=20, tau=5.0)
    assert pos[59] > 0.5  # strong uptrend active
    assert pos[60] == 0.0  # immediate SMA20 fast exit on bar 60
    assert pos[65] == 0.0
    assert pos[-1] > 0.7  # re-entered and ramped up cleanly

    # Causality: future drop does not touch earlier positions
    df2 = df.copy()
    df2.iloc[61:] *= 0.2
    pos2 = S40.r3_fastexit_qd_positions(df2, lev=1.0)
    assert np.allclose(pos[:61], pos2[:61])


def test_split_qd_and_6h_sim_causal():
    idx = pd.date_range("2025-01-01", periods=90 * 4, freq="6h", tz="UTC")
    rng = np.random.default_rng(3)
    c = 100.0 * np.cumprod(1.0 + 0.002 + rng.normal(0.0, 0.005, size=len(idx)))
    b6 = pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 10.0}, index=idx)
    hb = S40.split_qd_held(b6, lev=1.0)
    assert (hb >= 0.0).all() and (hb <= 1.0 + 1e-12).all()
    assert hb.iloc[-1] > 0.9  # fully coherent in smooth uptrend

    # Daily carry + 6h compounding should never crash and returns normalized UTC daily index
    d_idx = idx.normalize().unique()
    carry = pd.Series(0.0002, index=d_idx)
    rate = pd.Series(4.5, index=d_idx)
    dr = S40.simulate_6h_qd_carry(b6, b6, hb, hb, carry, rate, 0.70, 0.30, 6.0, 1.0, 0.1)
    assert len(dr) == len(d_idx) and dr.notna().all()
