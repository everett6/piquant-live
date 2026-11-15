"""Synthetic unit tests for PREREG37 (strategies37.py). Run BEFORE reading any real L2 files."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies17 as S17
import strategies37 as S37


def _synthetic_l2(n_sec: int = 3600, seed: int = 19):
    rng = np.random.default_rng(seed)
    bt = np.arange(1, n_sec + 1, dtype=np.int64) * 1000
    mid = 100.0 + np.cumsum(rng.normal(0.0, 0.02, n_sec))
    bid_px = np.round(mid - 0.01, 2)
    bid_sz = rng.uniform(0.5, 4.0, n_sec)
    bid_top3 = bid_sz + rng.uniform(2.0, 8.0, n_sec)
    bid_tot = bid_top3 + rng.uniform(5.0, 15.0, n_sec)
    ask_tot = rng.uniform(3.0, 20.0, n_sec)
    books = pd.DataFrame({
        "t": bt, "bid_px": bid_px, "bid_sz": bid_sz, "bid_top3": bid_top3,
        "bid_tot": bid_tot, "ask_tot": ask_tot, "mid": mid,
    })

    # ~4 trades per second inside the day
    n_tr = n_sec * 3
    tr_t = np.sort(rng.integers(1000, n_sec * 1000, size=n_tr, dtype=np.int64))
    idx = np.clip(np.searchsorted(bt, tr_t, side="right") - 1, 0, n_sec - 1)
    tr_p = bid_px[idx] + rng.choice([-0.02, 0.0, 0.01, 0.02], size=n_tr)
    tr_s = rng.uniform(0.1, 2.0, size=n_tr)
    tr_side = rng.choice(["b", "s"], size=n_tr)
    trades = pd.DataFrame({"t": tr_t, "p": tr_p, "s": tr_s, "side": tr_side})
    return trades, books


def test_fast_simulator_bit_identical_to_strategies17():
    trades, books = _synthetic_l2(3600, seed=42)
    always_on = lambda _t: True
    never_excl = lambda _t: False

    # K2 exact equivalence
    ref_k2 = S17.simulate("BTCUSDT", trades, books, "K2", S17.BASE, always_on, never_excl)
    got_k2 = S37.simulate_with_defense("BTCUSDT", trades, books, S17.BASE, None, use_k2_filter=True)
    assert got_k2["quotes"] == ref_k2["quotes"]
    assert got_k2["defense_cancels"] == ref_k2["defense_cancels"]
    assert got_k2["trades"] == ref_k2["trades"]

    # K3 exact equivalence (supplying K3's exact defense array)
    bt = books["t"].to_numpy(np.int64)
    tr_t = trades["t"].to_numpy(np.int64)
    tr_s = trades["s"].to_numpy(float)
    tr_buy = (trades["side"] == "b").to_numpy()
    r5 = S17.ret5s_bps(bt, books["mid"].to_numpy(float))
    f5 = S17.flow5s(bt, tr_t, tr_s, tr_buy)
    k3_def = ((~np.isnan(r5)) & (r5 < -3.0)) | (f5 < -0.60)

    ref_k3 = S17.simulate("BTCUSDT", trades, books, "K3", S17.BASE, always_on, never_excl)
    got_k3 = S37.simulate_with_defense("BTCUSDT", trades, books, S17.BASE, k3_def, use_k2_filter=True)
    assert got_k3["quotes"] == ref_k3["quotes"]
    assert got_k3["defense_cancels"] == ref_k3["defense_cancels"]
    assert got_k3["trades"] == ref_k3["trades"]
    assert got_k3["shadow_trades"] == ref_k3["shadow_trades"]


def test_barrier_tunnel_prob_and_trigger():
    bt = np.array([1000, 2000, 6000, 12000], dtype=np.int64)
    bid_top3 = np.array([10.0, 10.0, 10.0, 10.0])
    # Trades: 3 sell at t=1500 (< bid_top3=10 -> stays calm), 12 sell at t=5500 (sell_5s=15 > 10 at t=6000 -> hit!)
    # At t=12000 (> 5s after all sells), sell_5s=0 -> no trigger!
    tr_t = np.array([1500, 5500], dtype=np.int64)
    tr_s = np.array([3.0, 12.0])
    tr_buy = np.array([False, False])

    hit = S37.barrier_defense_hit(bid_top3, bt, tr_t, tr_s, tr_buy)
    assert list(hit) == [False, False, True, False]


def test_ising_magnetization_strictly_causal_and_bounded():
    t_grid = np.array([1000, 2000, 3000, 10000], dtype=np.int64)
    c1 = (np.array([1000, 2000, 3000], dtype=np.int64), np.array([1.0, -1.0, -1.0]))
    # c2 updates at t=2500 -> must NOT leak into t=2000; stale (>5000ms) at t=10000
    c2 = (np.array([1000, 2500], dtype=np.int64), np.array([1.0, -1.0]))
    c3 = (np.array([1000, 2000, 3000], dtype=np.int64), np.array([-1.0, -1.0, -1.0]))

    m = S37.ising_magnetization(t_grid, [c1, c2, c3], max_stale_ms=5000)
    # t=1000: (+1, +1, -1) -> +1/3
    # t=2000: (-1, +1 from 1000, -1) -> -1/3 (NOT -1 because 2500 hasn't happened)
    # t=3000: (-1, -1 from 2500, -1) -> -1.0
    # t=10000: all stale -> 0.0
    np.testing.assert_allclose(m, [1.0 / 3.0, -1.0 / 3.0, -1.0, 0.0], atol=1e-12)
