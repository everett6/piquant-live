"""Round-8 (PREREG8) medium-frequency trades with maker entries: fill simulator and the M1/M2 signals."""
from __future__ import annotations

import numpy as np
import pandas as pd

TICK = 1e-4          # price must trade through a limit by 1 bp to count as filled
MAKER_BPS = 2.0
TAKER_BPS = 6.0
SLIP_BPS = 1.0
FUNDING_BPS_H = 0.1
ENTRY_WINDOW = 5

M1_GRID = [dict(k=k, hold=h) for k in (4.0, 6.0) for h in (30, 120)]
M2_GRID = [dict(q=q, hold=h) for q in (1.0, 2.0) for h in (120, 720)]


def sim_maker(o, h, l, c, times_ns, side, tp_off, sl_off, hold, entry_window=ENTRY_WINDOW) -> pd.DataFrame:
    """side[t] in {-1,0,+1}: place a limit at c[t] (live entry_window bars). Offsets are price fractions.
    One position at a time. Returns trades with gross/net returns (fractions of notional)."""
    n = len(c)
    cand = np.flatnonzero(side)
    rows = []
    free_from = 0
    for t in cand:
        if t < free_from or t + 1 >= n:
            continue
        s = int(side[t])
        p = c[t]
        lo, hi = t + 1, min(n, t + 1 + entry_window)
        fills = np.flatnonzero(l[lo:hi] < p * (1 - TICK)) if s > 0 else np.flatnonzero(h[lo:hi] > p * (1 + TICK))
        if len(fills) == 0:
            free_from = hi
            continue
        j = lo + int(fills[0])
        tp = p * (1 + s * tp_off[t])
        sl = p * (1 - s * sl_off[t])
        end = min(n - 1, j + hold)
        # fill bar: only the stop can be judged (order of moves inside the bar is unknown)
        seg_l, seg_h, seg_o = l[j:end + 1], h[j:end + 1], o[j:end + 1]
        if s > 0:
            sl_hit = seg_l <= sl
            tp_hit = seg_h > tp * (1 + TICK)
        else:
            sl_hit = seg_h >= sl
            tp_hit = seg_l < tp * (1 - TICK)
        tp_hit[0] = False
        ev = np.flatnonzero(sl_hit | tp_hit)
        if len(ev):
            k = int(ev[0])
            if sl_hit[k]:
                px = min(sl, seg_o[k]) if s > 0 else max(sl, seg_o[k])
                if k == 0:
                    px = sl
                px *= 1 - s * SLIP_BPS * 1e-4
                exit_fee, reason = TAKER_BPS, "SL"
            else:
                px, exit_fee, reason = tp, MAKER_BPS, "TP"
            x = j + k
        else:
            x = end
            px, exit_fee, reason = c[x] * (1 - s * SLIP_BPS * 1e-4), TAKER_BPS, "TIME"
        gross = s * (px - p) / p
        hours = max(times_ns[x] - times_ns[j], 0) / 3.6e12
        net = gross - (MAKER_BPS + exit_fee + FUNDING_BPS_H * hours) * 1e-4
        rows.append((times_ns[j], times_ns[x], s, gross, net, reason))
        free_from = x + 1
    return pd.DataFrame(rows, columns=["entry_ns", "exit_ns", "side", "gross", "net", "reason"])


def sigma1(c: pd.Series) -> np.ndarray:
    r = np.log(c).diff()
    return r.rolling(1440, min_periods=1440).std().shift(1).to_numpy()


def m1_signal(c: pd.Series, k: float):
    r = np.log(c).diff().to_numpy()
    sg = sigma1(c)
    trig = np.abs(r) > k * sg
    trig &= np.isfinite(sg)
    side = np.where(trig, -np.sign(r), 0).astype(int)
    a = np.nan_to_num(np.abs(r))
    return side, 0.5 * a, 1.0 * a


def daily_regime_1m(c_daily: pd.Series, n: int, idx_1m: pd.DatetimeIndex) -> np.ndarray:
    """+1 UP / -1 DOWN / 0 unknown, from the previous completed day's close vs SMA(n)."""
    sma = c_daily.rolling(n, min_periods=n).mean()
    reg = pd.Series(np.where(sma.isna(), 0, np.where(c_daily > sma, 1, -1)), index=c_daily.index + pd.Timedelta(days=1))
    return reg.reindex(reg.index.union(idx_1m)).ffill().reindex(idx_1m).fillna(0).to_numpy().astype(int)


def m2_signal(c: pd.Series, regime: np.ndarray, q: float):
    low = c.rolling(240, min_periods=240).min().shift(1).to_numpy()
    high = c.rolling(240, min_periods=240).max().shift(1).to_numpy()
    cv = c.to_numpy()
    sg = sigma1(c)
    ok = np.isfinite(sg) & np.isfinite(low)
    side = np.where(ok & (regime > 0) & (cv < low), 1, np.where(ok & (regime < 0) & (cv > high), -1, 0))
    off = np.nan_to_num(q * sg * np.sqrt(60))
    return side.astype(int), off, 2 * off
