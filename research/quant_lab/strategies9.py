"""Round-9 (PREREG9) bear-regime minutes-horizon shorts: cascade short with a time-decay ratchet, rally fade."""
from __future__ import annotations

import numpy as np
import pandas as pd

from strategies8 import FUNDING_BPS_H, MAKER_BPS, SLIP_BPS, TAKER_BPS, TICK, ENTRY_WINDOW

SL_BPS = 30.0
A_HOLD = 45
B_HOLD = 30


def ratchet_schedule() -> np.ndarray:
    """Net target (bps) by minutes since fill, 0..A_HOLD."""
    k = np.arange(A_HOLD + 1)
    return np.select([k < 5, k < 15, k < 25], [65.0, 35.0, 12.0], 0.0)


def sim_short(o, h, l, c, times_ns, trig, maker_entry: bool, hold: int, sched_bps=None, tp_off=None) -> pd.DataFrame:
    """Short-only, one position at a time. Exit TP is a maker limit at either entry*(1-(sched[k]+rt)/1e4)
    (k = minutes since fill) or entry*(1-tp_off[t]). Stop SL_BPS above entry (taker), hard time stop at hold bars."""
    n = len(c)
    rt = MAKER_BPS + (MAKER_BPS if maker_entry else TAKER_BPS)
    rows = []
    free_from = 0
    for t in np.flatnonzero(trig):
        if t < free_from or t + 1 >= n:
            continue
        if maker_entry:
            lo, hi = t + 1, min(n, t + 1 + ENTRY_WINDOW)
            p = c[t]
            f = np.flatnonzero(h[lo:hi] > p * (1 + TICK))
            if len(f) == 0:
                free_from = hi
                continue
            j = lo + int(f[0])
            entry_fee = MAKER_BPS
        else:
            j = t + 1
            p = o[j] * (1 - SLIP_BPS * 1e-4)
            entry_fee = TAKER_BPS
        sl = p * (1 + SL_BPS * 1e-4)
        end = min(n - 1, j + hold)
        k = np.arange(end - j + 1)
        if sched_bps is not None:
            tp = p * (1 - (sched_bps[np.minimum(k, len(sched_bps) - 1)] + rt) * 1e-4)
        else:
            tp = np.full(len(k), p * (1 - tp_off[t]))
        seg_h, seg_l, seg_o = h[j:end + 1], l[j:end + 1], o[j:end + 1]
        sl_hit = seg_h >= sl
        tp_hit = seg_l < tp * (1 - TICK)
        if maker_entry:
            tp_hit[0] = False  # inside the fill bar the order of moves is unknown
        ev = np.flatnonzero(sl_hit | tp_hit)
        if len(ev):
            x = int(ev[0])
            if sl_hit[x]:
                px = sl if x == 0 else max(sl, seg_o[x])
                px *= 1 + SLIP_BPS * 1e-4
                fee, reason = TAKER_BPS, "SL"
            else:
                px, fee, reason = tp[x], MAKER_BPS, "TP"
            xb = j + x
        else:
            xb = end
            px, fee, reason = c[xb] * (1 + SLIP_BPS * 1e-4), TAKER_BPS, "TIME"
        gross = (p - px) / p
        hours = max(times_ns[xb] - times_ns[j], 0) / 3.6e12
        net = gross - (entry_fee + fee + FUNDING_BPS_H * hours) * 1e-4
        rows.append((times_ns[j], times_ns[xb], -1, gross, net, reason))
        free_from = xb + 1
    return pd.DataFrame(rows, columns=["entry_ns", "exit_ns", "side", "gross", "net", "reason"])


def a_trigger(c: pd.Series, regime: np.ndarray) -> np.ndarray:
    r = c.pct_change().to_numpy()
    return (regime < 0) & (r < -15e-4)


def b_signal(df: pd.DataFrame, regime: np.ndarray):
    tp_ = (df["h"] + df["l"] + df["c"]) / 3
    vwap = (tp_ * df["v"]).rolling(30, min_periods=30).sum() / df["v"].rolling(30, min_periods=30).sum()
    dev = df["c"] / vwap - 1
    sd = dev.rolling(1440, min_periods=1440).std().shift(1)
    trig = ((regime < 0) & (dev >= 35e-4) & (dev / sd > 2)).fillna(False).to_numpy()
    tp_off = ((df["c"] - vwap) / df["c"]).fillna(0).to_numpy()
    return trig, tp_off
