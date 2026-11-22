"""Unit tests verifying strict causality, zero pyarrow import, and speed of fast_universal_alpha."""
from __future__ import annotations

import subprocess
import sys
import time

import numpy as np
import pytest

import fast_universal_alpha as FUA


def test_zero_pyarrow_at_import():
    code = (
        "import sys; import fast_universal_alpha; "
        "assert 'pyarrow' not in sys.modules, 'pyarrow must not be imported'"
    )
    proc = subprocess.run(
        [sys.executable, "-c", code],
        cwd="/home/everett/PiQuant/piquant-live/research/quant_lab",
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, proc.stderr


@pytest.mark.parametrize("method", ["garman_klass", "parkinson"])
def test_fast_range_vol_strict_causality(method: str):
    rng = np.random.default_rng(42)
    n = 300
    c = 100.0 * np.exp(np.cumsum(rng.normal(0.0, 0.02, size=n)))
    o = np.roll(c, 1)
    o[0] = 100.0
    h = np.maximum(o, c) * (1.0 + rng.uniform(0.001, 0.03, size=n))
    l = np.minimum(o, c) * (1.0 - rng.uniform(0.001, 0.03, size=n))

    base = FUA.fast_range_vol(o, h, l, c, window=20, method=method, shift=1)

    k = 150
    o2, h2, l2, c2 = o.copy(), h.copy(), l.copy(), c.copy()
    o2[k:] *= 1.8
    h2[k:] *= 2.5
    l2[k:] *= 0.4
    c2[k:] *= 1.5
    pert = FUA.fast_range_vol(o2, h2, l2, c2, window=20, method=method, shift=1)

    # Because shift=1, index k depends only on bars <= k-1, so indices 0..k must match identically
    np.testing.assert_allclose(base[: k + 1], pert[: k + 1], equal_nan=True, rtol=0.0, atol=0.0)
    assert not np.allclose(base[k + 1 :], pert[k + 1 :], equal_nan=True)


def test_eg_universal_weights_strict_causality_and_simplex():
    rng = np.random.default_rng(7)
    t_len, m = 250, 4
    rets = rng.normal(0.0005, 0.02, size=(t_len, m))

    w_base = FUA.eg_universal_weights(rets, eta=0.15, shrinkage=0.01, rebal_buffer=0.01)
    assert w_base.shape == (t_len, m)
    assert np.all(w_base >= 0.0)
    np.testing.assert_allclose(np.sum(w_base, axis=1), np.ones(t_len), atol=1e-9)

    k = 120
    rets2 = rets.copy()
    rets2[k:, 0] += 0.25
    rets2[k:, 1] -= 0.20
    w_pert = FUA.eg_universal_weights(rets2, eta=0.15, shrinkage=0.01, rebal_buffer=0.01)

    # weights[k] must depend strictly on returns[:k]
    np.testing.assert_allclose(w_base[: k + 1], w_pert[: k + 1], rtol=0.0, atol=0.0)
    assert not np.allclose(w_base[k + 1 :], w_pert[k + 1 :])


def test_eg_universal_tracks_winner_and_respects_buffer():
    t_len = 200
    rets = np.zeros((t_len, 2), dtype=np.float64)
    rets[:, 0] = 0.02
    rets[:, 1] = -0.005

    w = FUA.eg_universal_weights(rets, eta=0.25, shrinkage=0.0, rebal_buffer=0.0)
    assert w[-1, 0] > 0.75

    # Huge buffer prevents any rebalance
    w_buf = FUA.eg_universal_weights(rets, eta=0.25, shrinkage=0.0, rebal_buffer=0.99)
    np.testing.assert_allclose(w_buf, np.full((t_len, 2), 0.5))


def test_fast_supersmoother_signal_strict_causality_and_crash_brake():
    rng = np.random.default_rng(123)
    n = 300
    c = 100.0 * np.exp(np.cumsum(rng.normal(0.001, 0.01, size=n)))
    o = np.roll(c, 1)
    o[0] = 100.0
    h = np.maximum(o, c) * 1.005
    l = np.minimum(o, c) * 0.995

    out_base = FUA.fast_supersmoother_signal(o, h, l, c, annual_days=365)

    k = 180
    o2, h2, l2, c2 = o.copy(), h.copy(), l.copy(), c.copy()
    # Inject extreme volatility spike starting at bar k
    h2[k : k + 5] *= 1.25
    l2[k : k + 5] *= 0.75
    c2[k:] *= 0.80
    out_pert = FUA.fast_supersmoother_signal(o2, h2, l2, c2, annual_days=365)

    for key in ("signal", "trend_pos", "crash_brake", "vol_ratio", "range_vol_slow"):
        np.testing.assert_allclose(
            out_base[key][: k + 1], out_pert[key][: k + 1], equal_nan=True, rtol=0.0, atol=0.0
        )

    # After spike at k, bar k+2 must trigger crash_brake < 1.0
    assert np.min(out_pert["crash_brake"][k + 1 : k + 6]) < 1.0


def test_vectorized_speed_benchmarks():
    rng = np.random.default_rng(99)
    n_vol = 100_000
    c = 100.0 * np.exp(np.cumsum(rng.normal(0.0, 0.01, size=n_vol)))
    o = c * 0.999
    h = c * 1.01
    l = c * 0.99

    t0 = time.perf_counter()
    _ = FUA.fast_range_vol(o, h, l, c, window=20, method="garman_klass")
    dt_vol = time.perf_counter() - t0
    assert dt_vol < 0.15, f"fast_range_vol too slow: {dt_vol:.4f}s"

    rets = rng.normal(0.0, 0.01, size=(10_000, 10))
    t1 = time.perf_counter()
    _ = FUA.eg_universal_weights(rets, eta=0.15, shrinkage=1e-3, rebal_buffer=0.02)
    dt_eg = time.perf_counter() - t1
    assert dt_eg < 0.30, f"eg_universal_weights too slow: {dt_eg:.4f}s"

    t2 = time.perf_counter()
    _ = FUA.fast_supersmoother_signal(o[:50_000], h[:50_000], l[:50_000], c[:50_000])
    dt_ss = time.perf_counter() - t2
    assert dt_ss < 0.25, f"fast_supersmoother_signal too slow: {dt_ss:.4f}s"
