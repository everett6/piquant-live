"""Round-13 (PREREG13) drawdown-first trend sleeves: fast exit (R3) and trailing stop (R4)."""
from __future__ import annotations

import numpy as np
import pandas as pd


def fast_exit_positions(daily: pd.DataFrame, lev: float) -> np.ndarray:
    """R3: enter when close > SMA50 and >= SMA20; exit when close < SMA20 or < SMA50."""
    c = daily["c"].to_numpy()
    s50 = daily["c"].rolling(50, min_periods=50).mean().to_numpy()
    s20 = daily["c"].rolling(20, min_periods=20).mean().to_numpy()
    pos = np.zeros(len(c))
    inn = False
    for t in range(len(c)):
        if np.isnan(s50[t]):
            continue
        if inn and (c[t] < s20[t] or c[t] < s50[t]):
            inn = False
        elif not inn and c[t] > s50[t] and c[t] >= s20[t]:
            inn = True
        pos[t] = lev if inn else 0.0
    return pos


def trailing_stop_positions(daily: pd.DataFrame, lev: float, stop: float = 0.15) -> np.ndarray:
    """R4: P1 rule plus a stop at `stop` below the highest close since entry; after a stop, wait for the close to
    go back to <= SMA50 and cross above it again."""
    c = daily["c"].to_numpy()
    s50 = daily["c"].rolling(50, min_periods=50).mean().to_numpy()
    pos = np.zeros(len(c))
    inn, armed, hi = False, True, 0.0
    for t in range(len(c)):
        if np.isnan(s50[t]):
            continue
        if inn:
            hi = max(hi, c[t])
            if c[t] <= (1 - stop) * hi:
                inn, armed = False, False
            elif c[t] <= s50[t]:
                inn = False
        else:
            if not armed and c[t] <= s50[t]:
                armed = True
            elif armed and c[t] > s50[t]:
                inn, hi = True, c[t]
        pos[t] = lev if inn else 0.0
    return pos
