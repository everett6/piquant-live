"""Round-10 (PREREG10): session x regime drift (T1) and US opening-range breakout (T2), taker entries."""
from __future__ import annotations

import numpy as np
import pandas as pd

from strategies8 import FUNDING_BPS_H, SLIP_BPS, TAKER_BPS

# (name, start minute of day, end minute exclusive, expected bars)
SESSIONS = [("asia", 0, 480), ("europe", 480, 810), ("us", 810, 1260), ("late", 1260, 1440)]
MIN_COVERAGE = 0.8
COLS = ["entry_ns", "exit_ns", "side", "gross", "net", "reason"]


def _cost(side, entry_px, exit_px, entry_ns, exit_ns, reason="TIME"):
    e = entry_px * (1 + side * SLIP_BPS * 1e-4)
    x = exit_px * (1 - side * SLIP_BPS * 1e-4)
    gross = side * (x - e) / e
    hours = max(exit_ns - entry_ns, 0) / 3.6e12
    return (entry_ns, exit_ns, side, gross, gross - (2 * TAKER_BPS + FUNDING_BPS_H * hours) * 1e-4, reason)


def session_table(df: pd.DataFrame, regime: np.ndarray) -> pd.DataFrame:
    """One row per (day, session) with enough bars: open, close, times, regime at the first bar, raw long return."""
    mod = df.index.hour * 60 + df.index.minute
    sess = np.full(len(df), -1)
    for i, (_, a, b) in enumerate(SESSIONS):
        sess[(mod >= a) & (mod < b)] = i
    g = pd.DataFrame({"day": df.index.floor("D"), "sess": sess, "o": df["o"].to_numpy(), "c": df["c"].to_numpy(),
                      "ns": df.index.as_unit("ns").asi8, "reg": regime})
    agg = g.groupby(["day", "sess"], sort=True).agg(o=("o", "first"), c=("c", "last"), entry_ns=("ns", "first"),
                                                    exit_ns=("ns", "last"), reg=("reg", "first"), n=("o", "size"))
    agg = agg.reset_index()
    exp = agg["sess"].map({i: b - a for i, (_, a, b) in enumerate(SESSIONS)})
    agg = agg[(agg["n"] >= MIN_COVERAGE * exp) & (agg["reg"] != 0)]
    agg["raw"] = agg["c"] / agg["o"] - 1
    return agg.reset_index(drop=True)


def t1_select(disc: pd.DataFrame) -> dict:
    """{(sess, reg): direction} for cells with |t| > 2 on discovery raw long returns."""
    out = {}
    for (s, r), x in disc.groupby(["sess", "reg"]):
        v = x["raw"].to_numpy()
        if len(v) > 2 and v.std(ddof=1) > 0:
            t = v.mean() / (v.std(ddof=1) / np.sqrt(len(v)))
            if abs(t) > 2:
                out[(int(s), int(r))] = int(np.sign(v.mean()))
    return out


def t1_trades(tab: pd.DataFrame, cells: dict) -> pd.DataFrame:
    rows = [_cost(cells[(int(s), int(r))], o, c, en, ex) for s, r, o, c, en, ex in
            tab[["sess", "reg", "o", "c", "entry_ns", "exit_ns"]].itertuples(index=False) if (int(s), int(r)) in cells]
    return pd.DataFrame(rows, columns=COLS)


def t2_trades(df: pd.DataFrame, regime: np.ndarray) -> pd.DataFrame:
    mod = (df.index.hour * 60 + df.index.minute).to_numpy()
    o, h, l, c = (df[k].to_numpy() for k in ("o", "h", "l", "c"))
    ns = df.index.as_unit("ns").asi8
    day = df.index.floor("D").as_unit("ns").asi8
    starts = np.flatnonzero(np.r_[True, day[1:] != day[:-1]])
    ends = np.r_[starts[1:], len(df)]
    rows = []
    for a, b in zip(starts, ends):
        m = mod[a:b]
        rng = np.flatnonzero((m >= 810) & (m < 870)) + a
        win = np.flatnonzero((m >= 870) & (m <= 1259)) + a
        if len(rng) < 0.8 * 60 or len(win) < 2:
            continue
        reg = regime[rng[0]]
        if reg == 0:
            continue
        hi, lo = h[rng].max(), l[rng].min()
        brk = np.flatnonzero(c[win] > hi) if reg > 0 else np.flatnonzero(c[win] < lo)
        if len(brk) == 0 or brk[0] + 1 >= len(win):
            continue
        ent = win[brk[0] + 1]
        last = win[-1]
        stop = lo if reg > 0 else hi
        seg = np.arange(ent, last + 1)
        hit = np.flatnonzero(l[seg] <= stop) if reg > 0 else np.flatnonzero(h[seg] >= stop)
        if len(hit):
            k = seg[hit[0]]
            px = (min(stop, o[k]) if reg > 0 else max(stop, o[k])) if k > ent else stop
            rows.append(_cost(reg, o[ent], px, ns[ent], ns[k], "SL"))
        else:
            rows.append(_cost(reg, o[ent], c[last], ns[ent], ns[last]))
    return pd.DataFrame(rows, columns=COLS)
