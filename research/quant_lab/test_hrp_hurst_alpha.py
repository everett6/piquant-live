"""Unit tests for hrp_hurst_alpha.py: zero pyarrow, HRP properties, Hurst scaling,
strict t-1 -> t causality, and pre-2024 calibration freeze.
"""
from __future__ import annotations

import os
import subprocess
import sys

import numpy as np
import pandas as pd

import hrp_hurst_alpha as HHA


def _make_synthetic_panel(
    seed: int = 17,
    n_days: int = 2200,
) -> tuple[pd.Series, pd.DataFrame, pd.Series, pd.Series]:
    """Generate synthetic daily prices, cross-asset returns, returns, and trend."""
    rng = np.random.default_rng(seed)
    idx = pd.date_range("2020-01-01", periods=n_days, freq="D", tz="UTC")
    common = rng.normal(0.0004, 0.012, size=n_days)
    ca_dict = {}
    for k in range(5):
        ca_dict[f"A{k}"] = 0.55 * common + rng.normal(0.0002, 0.010, size=n_days)
    ca_df = pd.DataFrame(ca_dict, index=idx)
    strat_ret = pd.Series(0.5 * common + rng.normal(0.0003, 0.006, size=n_days), index=idx)
    prices = pd.Series(100.0 * np.exp(np.cumsum(strat_ret.to_numpy())), index=idx)
    # Causal 1-day shifted trend strength in [0, 1]
    ma_fast = prices.rolling(10, min_periods=1).mean().shift(1)
    ma_slow = prices.rolling(40, min_periods=1).mean().shift(1)
    trend = pd.Series(
        np.clip(0.5 + 0.5 * np.tanh(12.0 * (ma_fast / ma_slow.clip(lower=1e-9) - 1.0)), 0.0, 1.0),
        index=idx,
    ).fillna(0.5)
    return prices, ca_df, strat_ret, trend


def test_zero_pyarrow_at_import():
    here = os.path.dirname(os.path.abspath(__file__))
    code = (
        "import sys; "
        "sys.modules['pyarrow'] = None; "
        "import hrp_hurst_alpha; "
        "assert sys.modules.get('pyarrow') is None"
    )
    proc = subprocess.run(
        [sys.executable, "-c", code],
        cwd=here,
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, f"pyarrow import blocked test failed: {proc.stderr}"


def test_hrp_weights_simplex_collinear_and_degenerate():
    rng = np.random.default_rng(101)
    t_len = 120

    # 1. Collinear duplicate assets: {a, b} are identical, c is independent equal-vol
    a = rng.normal(0.0, 0.02, size=t_len)
    c = rng.normal(0.0, 0.02, size=t_len)
    mat_collinear = np.column_stack([a, a.copy(), c])
    w_col = HHA.hrp_weights(mat_collinear)
    assert w_col.shape == (3,)
    assert np.all(np.isfinite(w_col))
    assert np.all(w_col >= 0.0)
    np.testing.assert_allclose(np.sum(w_col), 1.0, atol=1e-12)
    # HRP clusters {0, 1} together so their combined weight ~0.50, independent asset ~0.50
    assert abs((w_col[0] + w_col[1]) - 0.50) < 0.08
    assert abs(w_col[2] - 0.50) < 0.08

    # 2. Mixed zero-variance / constant / NaN columns
    mat_degen = np.column_stack([
        a,
        np.zeros(t_len),
        np.full(t_len, 0.001),  # constant non-zero -> zero variance
        c,
        np.full(t_len, np.nan),
    ])
    w_deg = HHA.hrp_weights(mat_degen)
    assert w_deg.shape == (5,)
    assert np.all(np.isfinite(w_deg))
    assert np.all(w_deg >= 0.0)
    np.testing.assert_allclose(np.sum(w_deg), 1.0, atol=1e-12)
    assert w_deg[1] == 0.0
    assert w_deg[2] == 0.0
    assert w_deg[4] == 0.0
    assert w_deg[0] > 0.0 and w_deg[3] > 0.0

    # 3. Single live column
    w_one_live = HHA.hrp_weights(np.column_stack([np.zeros(t_len), c, np.zeros(t_len)]))
    np.testing.assert_allclose(w_one_live, np.array([0.0, 1.0, 0.0]), atol=1e-12)

    # 4. All zero-variance columns -> uniform fallback
    w_all_zero = HHA.hrp_weights(np.zeros((t_len, 4)))
    np.testing.assert_allclose(w_all_zero, np.full(4, 0.25), atol=1e-12)

    # 5. Edge shapes
    assert HHA.hrp_weights(np.empty((10, 0))).shape == (0,)
    np.testing.assert_allclose(HHA.hrp_weights(np.ones((10, 1))), np.array([1.0]))


def test_hrp_weights_rolling_strict_causality():
    rng = np.random.default_rng(7)
    t_len, m = 200, 5
    rets = rng.normal(0.0, 0.015, size=(t_len, m))

    def _roll_hrp(mat: np.ndarray, win: int = 60) -> np.ndarray:
        out = np.full((len(mat), mat.shape[1]), 1.0 / mat.shape[1], dtype=np.float64)
        for t in range(20, len(mat)):
            out[t] = HHA.hrp_weights(mat[max(0, t - win) : t])  # strictly t-1 -> t
        return out

    w_orig = _roll_hrp(rets)
    cutoff = 120
    rets_corrupt = rets.copy()
    rets_corrupt[cutoff:, 0] += 0.50
    rets_corrupt[cutoff:, 1] -= 0.40
    w_corr = _roll_hrp(rets_corrupt)

    # At bar cutoff, weight depends strictly on rets[:cutoff], so [:cutoff+1] must match
    np.testing.assert_array_equal(w_orig[: cutoff + 1], w_corr[: cutoff + 1])
    assert not np.allclose(w_orig[cutoff + 1 :], w_corr[cutoff + 1 :])


def test_rolling_hurst_variogram_regimes_and_causality():
    rng = np.random.default_rng(42)
    n = 400
    idx = pd.date_range("2020-01-01", periods=n, freq="D", tz="UTC")

    # Persistent trend series (strong drift + noise -> H > 0.60)
    px_trend = pd.Series(100.0 * np.exp(np.cumsum(0.008 + rng.normal(0.0, 0.004, size=n))), index=idx)
    # Random walk (Brownian motion -> H ~ 0.50)
    px_rw = pd.Series(100.0 * np.exp(np.cumsum(rng.normal(0.0, 0.015, size=n))), index=idx)
    # Anti-persistent / mean-reverting OU series -> H < 0.35
    x = np.zeros(n)
    for t in range(1, n):
        x[t] = -0.6 * x[t - 1] + rng.normal(0.0, 0.02)
    px_mr = pd.Series(100.0 * np.exp(x), index=idx)

    h_trend = HHA.rolling_hurst_variogram(px_trend, window=64)
    h_rw = HHA.rolling_hurst_variogram(px_rw, window=64)
    h_mr = HHA.rolling_hurst_variogram(px_mr, window=64)

    assert np.isnan(h_rw.iloc[0])
    assert h_trend.dropna().median() > 0.60
    assert 0.38 <= h_rw.dropna().median() <= 0.62
    assert h_mr.dropna().median() < 0.35

    # Strict t-1 -> t causality: corrupt prices from bar k onward
    k = 200
    px_corrupt = px_rw.copy()
    px_corrupt.iloc[k:] *= 4.0
    h_corr = HHA.rolling_hurst_variogram(px_corrupt, window=64)

    pd.testing.assert_series_equal(h_rw.iloc[: k + 1], h_corr.iloc[: k + 1])
    assert not np.allclose(h_rw.iloc[k + 1 :].dropna(), h_corr.iloc[k + 1 :].dropna())


def test_conformal_conviction_gate_causality_and_uptrend_preservation():
    prices, ca_df, strat_ret, trend = _make_synthetic_panel(seed=23, n_days=2200)
    hurst = HHA.rolling_hurst_variogram(prices, window=64).fillna(0.5)
    vn_df = HHA._causal_spectral_entropy_z(ca_df, window=30, z_window=60)
    ent_z = vn_df["vn_entropy_z"]

    gate_orig, diag_orig = HHA.conformal_conviction_gate(
        hurst=hurst,
        entropy=ent_z,
        trend_strength=trend,
        returns=strat_ret,
        calibration_cutoff="2024-01-01",
    )

    assert ((gate_orig >= 0.0) & (gate_orig <= 1.0)).all()
    assert diag_orig["mode"] == "split_conformal_frozen"

    # Strong persistent uptrends (trend >= trend_preserve and non-crisis entropy) are unclipped
    strong_up = (trend >= 0.58) & (ent_z >= -1.25)
    assert np.any(strong_up)
    np.testing.assert_allclose(gate_orig.loc[strong_up], 1.0, atol=1e-12)

    # End-to-end strict t-1 -> t causality at pre-2024 cutoff k = 900
    k = 900
    prices_c = prices.copy()
    prices_c.iloc[k:] *= 0.25
    ca_c = ca_df.copy()
    ca_c.iloc[k:] = -0.35
    ret_c = strat_ret.copy()
    ret_c.iloc[k:] = -0.20

    # Recompute causal signals from corrupted market data
    hurst_c = HHA.rolling_hurst_variogram(prices_c, window=64).fillna(0.5)
    ent_c = HHA._causal_spectral_entropy_z(ca_c, window=30, z_window=60)["vn_entropy_z"]
    ma_fast_c = prices_c.rolling(10, min_periods=1).mean().shift(1)
    ma_slow_c = prices_c.rolling(40, min_periods=1).mean().shift(1)
    trend_c = pd.Series(
        np.clip(0.5 + 0.5 * np.tanh(12.0 * (ma_fast_c / ma_slow_c.clip(lower=1e-9) - 1.0)), 0.0, 1.0),
        index=prices.index,
    ).fillna(0.5)

    gate_corr, _ = HHA.conformal_conviction_gate(
        hurst=hurst_c,
        entropy=ent_c,
        trend_strength=trend_c,
        returns=ret_c,
        calibration_cutoff="2024-01-01",
    )
    # Bar k depends strictly on data <= k-1, so [:k+1] must match bit-for-bit
    pd.testing.assert_series_equal(gate_orig.iloc[: k + 1], gate_corr.iloc[: k + 1])

    # Direct feature perturbation with input_shift=1 must also match [:k+1]
    g_shift_base, _ = HHA.conformal_conviction_gate(hurst, ent_z, trend, returns=strat_ret, input_shift=1)
    h_pert = hurst.copy()
    h_pert.iloc[k:] = 0.05
    e_pert = ent_z.copy()
    e_pert.iloc[k:] = -4.0
    t_pert = trend.copy()
    t_pert.iloc[k:] = 0.01
    g_shift_pert, _ = HHA.conformal_conviction_gate(h_pert, e_pert, t_pert, returns=ret_c, input_shift=1)
    pd.testing.assert_series_equal(g_shift_base.iloc[: k + 1], g_shift_pert.iloc[: k + 1])


def test_pre_2024_calibration_freeze():
    prices, ca_df, strat_ret, trend = _make_synthetic_panel(seed=31, n_days=2200)  # 2020-01-01..2026-01-09
    hurst = HHA.rolling_hurst_variogram(prices, window=64).fillna(0.5)
    ent_z = HHA._causal_spectral_entropy_z(ca_df, window=30, z_window=60)["vn_entropy_z"]

    gate_orig, diag_orig = HHA.conformal_conviction_gate(
        hurst=hurst,
        entropy=ent_z,
        trend_strength=trend,
        returns=strat_ret,
        calibration_cutoff="2024-01-01",
    )

    # Corrupt ALL 2024+ data (returns, hurst, entropy, trend)
    oos_mask = strat_ret.index >= pd.Timestamp("2024-01-01", tz="UTC")
    ret_oos_corrupt = strat_ret.copy()
    ret_oos_corrupt.loc[oos_mask] = -0.35
    hurst_oos_corrupt = hurst.copy()
    hurst_oos_corrupt.loc[oos_mask] = 0.05
    ent_oos_corrupt = ent_z.copy()
    ent_oos_corrupt.loc[oos_mask] = -5.0
    trend_oos_corrupt = trend.copy()
    trend_oos_corrupt.loc[oos_mask] = 0.01

    gate_corr, diag_corr = HHA.conformal_conviction_gate(
        hurst=hurst_oos_corrupt,
        entropy=ent_oos_corrupt,
        trend_strength=trend_oos_corrupt,
        returns=ret_oos_corrupt,
        calibration_cutoff="2024-01-01",
    )

    # Frozen pre-2024 fitted parameters must be bit-for-bit identical
    for key in (
        "n_cal_total",
        "n_proper_train",
        "n_cal_holdout",
        "frozen_q_alpha",
        "frozen_coef",
        "frozen_mean",
        "frozen_std",
        "mean_gate_pre_2024",
    ):
        assert diag_orig[key] == diag_corr[key], f"Mismatch in frozen calibration key {key}"

    # Pre-2024 predictions must be bit-for-bit identical
    pre_mask = strat_ret.index < pd.Timestamp("2024-01-01", tz="UTC")
    pd.testing.assert_series_equal(gate_orig.loc[pre_mask], gate_corr.loc[pre_mask])

    # Corrupting only 2025+ data must leave 2020..2024 OOS prefix predictions identical
    y25_mask = strat_ret.index >= pd.Timestamp("2025-01-01", tz="UTC")
    ret_25 = strat_ret.copy()
    ret_25.loc[y25_mask] = 0.45
    h_25 = hurst.copy()
    h_25.loc[y25_mask] = 0.95
    e_25 = ent_z.copy()
    e_25.loc[y25_mask] = 4.0
    t_25 = trend.copy()
    t_25.loc[y25_mask] = 0.99

    gate_25, diag_25 = HHA.conformal_conviction_gate(
        hurst=h_25,
        entropy=e_25,
        trend_strength=t_25,
        returns=ret_25,
        calibration_cutoff="2024-01-01",
    )
    assert diag_orig["frozen_coef"] == diag_25["frozen_coef"]
    pd.testing.assert_series_equal(gate_orig.loc[~y25_mask], gate_25.loc[~y25_mask])


def test_conformal_gate_prior_mode_and_entropy_causality():
    prices, ca_df, _, trend = _make_synthetic_panel(seed=99, n_days=300)
    vn_orig = HHA._causal_spectral_entropy_z(ca_df, window=30, z_window=60)

    k = 180
    ca_corr = ca_df.copy()
    ca_corr.iloc[k:] = -ca_corr.iloc[k:] * 10.0 + 0.15
    vn_corr = HHA._causal_spectral_entropy_z(ca_corr, window=30, z_window=60)
    pd.testing.assert_frame_equal(vn_orig.iloc[: k + 1], vn_corr.iloc[: k + 1])

    hurst = HHA.rolling_hurst_variogram(prices, window=64).fillna(0.5)
    g_prior, d_prior = HHA.conformal_conviction_gate(
        hurst=hurst,
        entropy=vn_orig["vn_entropy_z"],
        trend_strength=trend,
        returns=None,
    )
    assert d_prior["mode"] == "prior_uncalibrated"
    assert ((g_prior >= 0.0) & (g_prior <= 1.0)).all()
    strong_up = (trend >= 0.58) & (vn_orig["vn_entropy_z"] >= -1.25)
    if np.any(strong_up):
        np.testing.assert_allclose(g_prior.loc[strong_up], 1.0, atol=1e-12)

