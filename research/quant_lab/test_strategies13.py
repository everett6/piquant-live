"""Synthetic tests for round-13 code (python test_strategies13.py)."""
import numpy as np
import pandas as pd

import strategies5 as S5
import strategies13 as S13


def _daily(prices):
    idx = pd.date_range("2020-01-01", periods=len(prices), freq="D", tz="UTC")
    c = pd.Series(prices, index=idx, dtype=float)
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def test_fast_exit_leaves_before_sma50():
    px = np.r_[np.full(50, 100.0), np.linspace(100, 160, 40), np.linspace(160, 140, 15)]
    d = _daily(px)
    r3 = S13.fast_exit_positions(d, 2.0)
    p1 = S5.m1_positions(d, 50, 2.0)
    assert r3[:50].sum() == 0 and r3[70] == 2.0
    first_out = int(np.flatnonzero((r3 == 0) & (np.arange(len(px)) > 60))[0])
    assert p1[first_out] == 2.0  # P1 still long when R3 has already exited


def test_trailing_stop_and_rearm():
    up = np.linspace(100, 200, 60)
    drop = np.linspace(200, 165, 5)            # -17.5% from the high, still above SMA50
    px = np.r_[np.full(50, 100.0), up, drop, np.full(10, 170.0)]
    d = _daily(px)
    pos = S13.trailing_stop_positions(d, 2.0)
    i_stop = 50 + 60 + 3                       # 200*0.85 = 170 first crossed in the drop
    assert pos[i_stop - 2] == 2.0 and pos[-1] == 0.0  # stopped, and not re-entered while above SMA50
    px2 = np.r_[px, np.full(80, 150.0), np.full(5, 300.0)]  # sinks below SMA50, then crosses above again
    pos2 = S13.trailing_stop_positions(_daily(px2), 2.0)
    assert pos2[-1] == 2.0


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
