"""Synthetic unit tests for PREREG38 (strategies38.py). Zero real market data read."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies36 as S36
import strategies38 as S38


def _make_panel(n_days: int = 120) -> pd.DataFrame:
    idx = pd.date_range("2025-01-06", periods=n_days, freq="D", tz="UTC")  # starts Monday
    data = {}
    for k, c in enumerate(["BTC", "ETH", "SOL", "DOGE", "LINK", "ADA"]):
        slope = 1.0 + 0.008 * (k + 1)
        # Drop last 20 days on DOGE to test mid-week SMA50 stop-out
        arr = 100.0 * (slope ** np.arange(n_days))
        if c == "DOGE":
            arr[-10:] = arr[-11] * 0.30
        data[c] = arr
    return pd.DataFrame(data, index=idx)


def test_kuramoto_breadth_and_qd_targets_causal():
    px = _make_panel(100)
    br = S38.kuramoto_breadth(px, sma_n=50, min_hist=60)
    # First 60 rows must be 0.0 because earlier < 60
    assert (br.iloc[:60] == 0.0).all()
    assert br.iloc[70] == 1.0
    # After DOGE drops below its SMA50, breadth is 5/6
    assert abs(br.iloc[-1] - 5.0 / 6.0) < 1e-9

    # Causality check: changing future prices must not change br up to t
    px_pert = px.copy()
    px_pert.iloc[75:, :] *= 0.2
    br_pert = S38.kuramoto_breadth(px_pert, sma_n=50, min_hist=60)
    assert np.allclose(br.iloc[:75], br_pert.iloc[:75])

    qd_b = pd.Series(1.0, index=px.index)
    qd_e = pd.Series(0.5, index=px.index)
    tb, te = S38.kuramoto_qd_targets(qd_b, qd_e, br, sync_thresh=0.70, base_lev=1.0, boost_lev=1.65)
    assert tb.iloc[59] == 1.0
    assert tb.iloc[70] == 1.65
    assert abs(te.iloc[70] - 0.825) < 1e-9


def test_leader_basket_weights_gate_and_midweek_exit():
    px = _make_panel(120)
    w = S38.leader_basket_weights(px, sma_n=50, min_hist=60, form_d=21, top_k=4)
    # Before 60 prior closes all weights 0
    assert (w.iloc[:60] == 0.0).all().all()
    # In steady uptrend, total sum <= 1.0
    assert (w.sum(axis=1) <= 1.0 + 1e-9).all()
    # DOGE stops out on drop below SMA50 even mid-week
    assert w["DOGE"].iloc[-1] == 0.0

    # When BTC drops below SMA50, ENTIRE basket flattens to 0.0 immediately
    px_bear = px.copy()
    px_bear.loc[px_bear.index[-5]:, "BTC"] *= 0.4
    w_bear = S38.leader_basket_weights(px_bear, sma_n=50, min_hist=60, form_d=21, top_k=4)
    assert (w_bear.iloc[-5:] == 0.0).all().all()


def test_multi_sleeve_matches_s36_when_leader_zero():
    idx = pd.date_range("2025-01-01", periods=90, freq="D", tz="UTC")
    btc = pd.DataFrame({"c": 100.0 * (1.01 ** np.arange(90))}, index=idx)
    eth = pd.DataFrame({"c": 50.0 * (1.012 ** np.arange(90))}, index=idx)
    pos_b = pd.Series(1.0, index=idx)
    pos_e = pd.Series(0.8, index=idx)
    carry = pd.Series(0.0002, index=idx)
    rate = pd.Series(4.5, index=idx)

    r36, m36, l36 = S36.simulate_qd_carry_portfolio(
        btc, eth, pos_b, pos_e, carry, rate,
        trend_share=0.70, carry_share=0.30, taker_bps=6.0, slip_bps=1.0, funding_bps_h=0.1,
    )
    empty_w = pd.DataFrame(0.0, index=idx, columns=["BTC"])
    r38, m38, l38 = S38.simulate_multi_sleeve_portfolio(
        btc, eth, pos_b, pos_e, btc[["c"]].rename(columns={"c": "BTC"}), empty_w,
        carry, rate,
        trend_share=0.70, leader_share=0.0, carry_share=0.30,
        taker_bps=6.0, slip_bps=1.0, funding_bps_h=0.1,
    )
    assert np.allclose(r36.to_numpy(), r38.to_numpy(), atol=1e-12)
    assert np.allclose(m36.to_numpy(), m38.to_numpy(), atol=1e-12)
    assert np.allclose(l36.to_numpy(), l38.to_numpy(), atol=1e-12)
