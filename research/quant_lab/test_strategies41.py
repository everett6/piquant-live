"""Synthetic tests for PREREG41 strategies (run and pass BEFORE reading real L2 files)."""
import numpy as np
import pytest

import strategies41 as S41


def test_causal_cvd_and_ema_ignore_future_shocks():
    rng = np.random.default_rng(42)
    base_cvd = rng.normal(0.0, 0.2, size=60)
    base_px = 100.0 * np.exp(np.cumsum(rng.normal(0.0, 0.01, size=60)))

    shocked_cvd = base_cvd.copy()
    shocked_px = base_px.copy()
    shocked_cvd[35:] = -0.99
    shocked_px[35:] *= 5.0

    np.testing.assert_allclose(
        S41.compute_causal_cvd_z(base_cvd, 18)[:35],
        S41.compute_causal_cvd_z(shocked_cvd, 18)[:35],
    )
    np.testing.assert_allclose(
        S41.compute_causal_ema(base_px, 18)[:35],
        S41.compute_causal_ema(shocked_px, 18)[:35],
    )


def test_quantum_walk_influx_propagation_and_zero_safety():
    # Zero flow -> zero influx everywhere
    rets = np.zeros((36, 4))
    assert np.allclose(S41.quantum_walk_influx(rets, np.zeros(4), np.zeros(4)), 0.0)

    # Correlated cluster (0, 1) with impulse on coin 0 transfers wave influx to coin 1
    rng = np.random.default_rng(7)
    r_shared = rng.normal(0.0, 0.02, size=36)
    rets[:, 0] = r_shared
    rets[:, 1] = r_shared + rng.normal(0.0, 0.002, size=36)
    rets[:, 2] = rng.normal(0.0, 0.02, size=36)
    # coin 3 stays constant/zero variance -> must not produce NaNs
    influx = S41.quantum_walk_influx(
        rets,
        obi_vec=np.array([0.4, 0.3, -0.2, 0.0]),
        cvd_z_vec=np.array([2.5, 0.2, -1.0, 0.0]),
        tau=0.60,
    )
    assert np.all(np.isfinite(influx))
    assert influx[0] > 0.0
    assert influx[1] > influx[2]
    assert influx[2] < 0.0


def test_portfolio_sim_and_weights_causal():
    closes = np.array(
        [
            [100.0, 100.0, 100.0],
            [102.0, 101.0, 99.0],
            [105.0, 100.0, 98.0],
            [106.0, 99.0, 97.0],
        ]
    )
    w = np.array(
        [
            [0.5, 0.0, 0.0],
            [0.5, 0.0, 0.0],
            [0.0, 0.0, 0.0],
            [0.0, 0.0, 0.0],
        ]
    )
    res = S41.simulate_4h_portfolio(closes, w, one_way_cost_bps=3.0, funding_bps_per_bar=0.0, start_bar=0, end_bar=4)
    # Bar 0->1: turnover 0.5 * 3 bps, ret +2% on 0.5 = +1.0% - 0.015% = +0.985%
    # Bar 1->2: turnover 0, ret +2.94118% * 0.5 = +1.47059%
    # Bar 2->3: exit turnover 0.5 * 3 bps = -0.015%
    assert res["bars"] == 3
    assert res["active_bars"] == 2
    assert res["cum_ret_pct"] == pytest.approx(((1.00985) * (1.0 + 0.5 * (105.0 / 102.0 - 1.0)) * (1.0 - 0.00015) - 1.0) * 100.0, rel=1e-4)


def test_smart_exec_window_latency_guard_and_fill():
    t0 = 1_000_000_000
    book_ts = np.array([t0 + i * 1000 for i in range(905)], dtype=np.int64)
    bid = np.full(905, 99.95)
    ask = np.full(905, 100.05)  # 10 bps spread, half spread = 5 bps
    obi3 = np.full(905, 0.25)   # favorable for BUY (+1)

    # Trade strictly BEFORE +100ms latency guard must NOT fill passively on slice 0
    # Other slices have trades crossing <= 99.95 -> 3/4 passive fills, 1/4 cross
    tr_ts = np.array([t0 + 50, t0 + 225_500, t0 + 450_500, t0 + 675_500], dtype=np.int64)
    tr_px = np.array([99.90, 99.95, 99.94, 99.95], dtype=float)

    res = S41.simulate_smart_exec_window(
        book_ts, bid, ask, obi3, tr_ts, tr_px, side=1, t0_ms=t0, spread_med_1h_bps=10.0, latency_ms=100
    )
    assert res["valid"] == 1.0
    assert res["passive_fill_frac"] == pytest.approx(0.75)
    # Base sf = +5 bps; smart = 0.75*(-5 bps) + 0.25*(+5 bps) = -2.5 bps => saved +7.5 bps
    assert res["bps_saved"] == pytest.approx(7.5, abs=0.05)
