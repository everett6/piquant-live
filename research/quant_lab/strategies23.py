"""Round-23 (PREREG23): volatility-expansion exit on the P1 trend sleeve (2x while close > SMA50)."""
from __future__ import annotations

import numpy as np
import pandas as pd

SHORT, LONG, MAX_RATIO = 10, 100, 1.5


def vol_ratio(close: pd.Series, downside: bool) -> pd.Series:
    """std (or downside deviation) of the last 10 daily returns over that of the last 100, through each close.
    NaN until 100 returns exist; a zero denominator gives 0 (counts as calm)."""
    r = close.pct_change()
    if downside:
        d2 = np.minimum(r, 0.0) ** 2
        short = np.sqrt(d2.rolling(SHORT, min_periods=SHORT).mean())
        long = np.sqrt(d2.rolling(LONG, min_periods=LONG).mean())
    else:
        short = r.rolling(SHORT, min_periods=SHORT).std()
        long = r.rolling(LONG, min_periods=LONG).std()
    ratio = short / long.where(long > 0)
    return ratio.where(~(long == 0), 0.0)


def vol_exit_positions(daily: pd.DataFrame, lev: float, downside: bool) -> np.ndarray:
    """lev when close > SMA50 and the volatility ratio is below 1.5, else 0 (also 0 before 100 returns)."""
    c = daily["c"]
    sma = c.rolling(50, min_periods=50).mean()
    ratio = vol_ratio(c, downside)
    on = (c > sma) & sma.notna() & ratio.notna() & (ratio < MAX_RATIO)
    return np.where(on.to_numpy(), float(lev), 0.0)


def gate_days(daily: pd.DataFrame, downside: bool) -> int:
    """Days the trend rule was long but the volatility gate kept the sleeve flat."""
    c = daily["c"]
    sma = c.rolling(50, min_periods=50).mean()
    ratio = vol_ratio(c, downside)
    return int(((c > sma) & sma.notna() & ratio.notna() & (ratio >= MAX_RATIO)).sum())
