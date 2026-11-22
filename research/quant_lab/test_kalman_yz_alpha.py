"""Unit tests for kalman_yz_alpha: zero pyarrow, strict t-1->t causality, gap vol, AdaHedge & speed."""
from __future__ import annotations

import math
import os
import subprocess
import sys
import time

import numpy as np
import pytest

import fast_universal_alpha as FUA
import kalman_yz_alpha as KYZ

HERE = os.path.dirname(os.path.abspath(__file__))


def test_zero_pyarrow_at_import():
    code = (
        "import sys; "
        "sys.modules['pyarrow'] = None; "
        "import kalman_yz_alpha; "
        "assert sys.modules.get('pyarrow', None) is None"
    )
    proc = subprocess.run(
        [sys.executable, "-c", code],
        cwd=HERE,
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, f"pyarrow import attempted: {proc.stderr}"


def test_yang_zhang_vol_strict_causality_and_fallback():
    rng = np.random.default_rng(42)
    n = 300
    c = 100.0 * np.exp(np.cumsum(rng.normal(0.0, 0.02, size=n)))
    o = np.roll(c, 1) * np.exp(rng.normal(0.0, 0.01, size=n))
    o[0] = 100.0
    h = np.maximum(o, c) * (1.0 + rng.uniform(0.002, 0.03, size=n))
    l = np.minimum(o, c) * (1.0 - rng.uniform(0.002, 0.03, size=n))

    base = KYZ.yang_zhang_vol(o, h, l, c, window=20, annual_days=252, shift=1)
    assert base.shape == (n,)
    assert np.all(np.isfinite(base[21:]))

    k = 150
    o2, h2, l2, c2 = o.copy(), h.copy(), l.copy(), c.copy()
    o2[k:] *= 1.8
    h2[k:] *= 2.5
    l2[k:] *= 0.4
    c2[k:] *= 1.5
    pert = KYZ.yang_zhang_vol(o2, h2, l2, c2, window=20, annual_days=252, shift=1)

    # shift=1 => index k depends strictly on bars <= k-1, so [:k+1] must match bit-for-bit
    np.testing.assert_allclose(base[: k + 1], pert[: k + 1], equal_nan=True, rtol=0.0, atol=0.0)
    assert not np.allclose(base[k + 1 :], pert[k + 1 :], equal_nan=True)

    # Close-only fallback (None OHL vs NaN/invalid OHL)
    fb_none = KYZ.yang_zhang_vol(None, None, None, c, window=20, annual_days=252, shift=1)
    nan_arr = np.full(n, np.nan)
    fb_nan = KYZ.yang_zhang_vol(nan_arr, nan_arr, nan_arr, c, window=20, annual_days=252, shift=1)
    np.testing.assert_allclose(fb_none, fb_nan, equal_nan=True, rtol=0.0, atol=0.0)
    assert np.all(np.isfinite(fb_none[21:]))

    # Partial invalid OHLC bars fall back cleanly without NaNs after warmup
    o_bad, h_bad, l_bad = o.copy(), h.copy(), l.copy()
    h_bad[40:65] = l_bad[40:65] * 0.5  # malformed H < L
    fb_partial = KYZ.yang_zhang_vol(o_bad, h_bad, l_bad, c, window=20, annual_days=252, shift=1)
    assert np.all(np.isfinite(fb_partial[21:]))
    fb_pert = KYZ.yang_zhang_vol(None, None, None, c2, window=20, annual_days=252, shift=1)
    np.testing.assert_allclose(fb_none[: k + 1], fb_pert[: k + 1], equal_nan=True, rtol=0.0, atol=0.0)


def test_yang_zhang_overnight_gap_sensitivity_vs_garman_klass():
    n = 120
    # Alternating +-4% overnight gaps with zero intraday range (H = L = O = C)
    gaps = np.where(np.arange(n) % 2 == 0, 0.04, -0.04)
    gaps[0] = 0.0
    c = np.empty(n, dtype=np.float64)
    o = np.empty(n, dtype=np.float64)
    c[0] = 100.0
    o[0] = 100.0
    for t in range(1, n):
        o[t] = c[t - 1] * math.exp(gaps[t])
        c[t] = o[t]  # flat intraday bar
    h = o * 1.0005
    l = o * 0.9995

    yz = KYZ.yang_zhang_vol(o, h, l, c, window=20, annual_days=252, shift=1)
    gk = FUA.fast_range_vol(o, h, l, c, window=20, method="garman_klass", annual_days=252, shift=1)

    # Garman-Klass ignores O_t / C_{t-1} and reports near-zero vol (< 0.02),
    # whereas Yang-Zhang captures the true ~4% daily gap vol (~0.63 annualized)
    expected_ann = 0.04 * math.sqrt(252.0) * math.sqrt(20.0 / 19.0)
    assert np.nanmean(gk[30:]) < 0.02
    assert np.nanmean(yz[30:]) > 0.55
    np.testing.assert_allclose(yz[30:], expected_ann, rtol=0.05)


def test_kalman_trend_velocity_strict_causality_and_direction():
    rng = np.random.default_rng(7)
    n = 300
    c = 100.0 * np.exp(np.cumsum(rng.normal(0.0005, 0.015, size=n)))

    out_base = KYZ.kalman_trend_velocity(c, q_pos=1e-4, q_vel=1e-5, r_obs=1e-3)
    assert set(out_base.keys()) == {"level", "velocity", "vel_zscore", "innovation_z"}
    for arr in out_base.values():
        assert arr.shape == (n,)

    k = 140
    c2 = c.copy()
    c2[k:] *= 0.45
    out_pert = KYZ.kalman_trend_velocity(c2, q_pos=1e-4, q_vel=1e-5, r_obs=1e-3)

    for key in ("level", "velocity", "vel_zscore", "innovation_z"):
        np.testing.assert_allclose(
            out_base[key][: k + 1], out_pert[key][: k + 1], equal_nan=True, rtol=0.0, atol=0.0
        )
        assert not np.allclose(out_base[key][k + 1 :], out_pert[key][k + 1 :], equal_nan=True)

    # Verify directional response: strong uptrend vs strong downtrend
    t_idx = np.arange(80, dtype=np.float64)
    up_px = 100.0 * np.exp(0.01 * t_idx)
    dn_px = 100.0 * np.exp(-0.01 * t_idx)
    up_kf = KYZ.kalman_trend_velocity(up_px)
    dn_kf = KYZ.kalman_trend_velocity(dn_px)
    assert up_kf["velocity"][-1] > 0.005
    assert up_kf["vel_zscore"][-1] > 0.5
    assert dn_kf["velocity"][-1] < -0.005
    assert dn_kf["vel_zscore"][-1] < -0.5


@pytest.mark.parametrize("k_assets,min_w,max_w", [(2, 0.20, 0.80), (4, 0.10, 0.45)])
def test_adahedge_weights_causality_bounds_and_simplex(k_assets: int, min_w: float, max_w: float):
    rng = np.random.default_rng(123)
    t_len = 260
    rets = rng.normal(0.0004, 0.02, size=(t_len, k_assets))

    w_base = KYZ.adahedge_weights(rets, min_w=min_w, max_w=max_w, rebal_buffer=0.02)
    assert w_base.shape == (t_len, k_assets)
    assert np.all(np.isfinite(w_base))
    assert np.all(w_base >= min_w - 1e-12)
    assert np.all(w_base <= max_w + 1e-12)
    np.testing.assert_allclose(np.sum(w_base, axis=1), np.ones(t_len), atol=1e-10)

    k = 130
    rets2 = rets.copy()
    rets2[k:, 0] += 0.25
    rets2[k:, 1] -= 0.20
    w_pert = KYZ.adahedge_weights(rets2, min_w=min_w, max_w=max_w, rebal_buffer=0.02)

    # weights[k] uses strictly returns[:k], so [:k+1] is bitwise identical
    np.testing.assert_allclose(w_base[: k + 1], w_pert[: k + 1], rtol=0.0, atol=0.0)
    assert not np.allclose(w_base[k + 1 :], w_pert[k + 1 :])


def test_adahedge_tracks_winner_and_respects_rebal_buffer():
    rng = np.random.default_rng(9)
    t_len = 220
    rets = np.column_stack([
        rng.normal(0.02, 0.005, size=t_len),
        rng.normal(-0.005, 0.005, size=t_len),
    ])

    w_free = KYZ.adahedge_weights(rets, min_w=0.0, max_w=1.0, rebal_buffer=0.0)
    assert w_free[-1, 0] > 0.85

    w_bounded = KYZ.adahedge_weights(rets, min_w=0.20, max_w=0.80, rebal_buffer=0.0)
    assert np.isclose(w_bounded[-1, 0], 0.80, atol=1e-6)
    assert np.isclose(w_bounded[-1, 1], 0.20, atol=1e-6)

    # Large rebalance buffer suppresses all trades after bar 0
    w_frozen = KYZ.adahedge_weights(rets, min_w=0.20, max_w=0.80, rebal_buffer=0.99)
    np.testing.assert_allclose(w_frozen, np.full((t_len, 2), 0.5))

    # Moderate buffer reduces turnover events relative to zero buffer on noisy oscillating returns
    noisy_rets = np.column_stack([
        rng.normal(0.001, 0.02, size=t_len),
        rng.normal(0.0005, 0.015, size=t_len),
    ])
    w_zero = KYZ.adahedge_weights(noisy_rets, min_w=0.20, max_w=0.80, rebal_buffer=0.0)
    w_buf = KYZ.adahedge_weights(noisy_rets, min_w=0.20, max_w=0.80, rebal_buffer=0.05)
    trades_zero = int(np.sum(np.any(np.abs(np.diff(w_zero, axis=0)) > 1e-12, axis=1)))
    trades_buf = int(np.sum(np.any(np.abs(np.diff(w_buf, axis=0)) > 1e-12, axis=1)))
    assert trades_zero > 5
    assert trades_buf < trades_zero


def test_vectorized_speed_benchmarks():
    rng = np.random.default_rng(99)
    n_bars = 100_000
    c = 100.0 * np.exp(np.cumsum(rng.normal(0.0, 0.01, size=n_bars)))
    o = c * 0.999
    h = c * 1.01
    l = c * 0.99

    t0 = time.perf_counter()
    _ = KYZ.yang_zhang_vol(o, h, l, c, window=20, annual_days=252, shift=1)
    dt_yz = time.perf_counter() - t0
    assert dt_yz < 0.20, f"yang_zhang_vol too slow: {dt_yz:.4f}s"

    t1 = time.perf_counter()
    _ = KYZ.kalman_trend_velocity(c)
    dt_kf = time.perf_counter() - t1
    assert dt_kf < 0.25, f"kalman_trend_velocity too slow: {dt_kf:.4f}s"

    rets = rng.normal(0.0, 0.01, size=(10_000, 10))
    t2 = time.perf_counter()
    _ = KYZ.adahedge_weights(rets, min_w=0.05, max_w=0.40, rebal_buffer=0.02)
    dt_ada = time.perf_counter() - t2
    assert dt_ada < 1.00, f"adahedge_weights too slow: {dt_ada:.4f}s"
