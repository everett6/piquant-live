"""Round-5 (PREREG5) M1 trend-gated leverage and the monthly market-beat metric."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

M1_GRID = [dict(n=n, lev=lev) for n in (20, 50, 100, 200) for lev in (1.0, 1.5, 2.0)]


def m1_positions(daily: pd.DataFrame, n: int, lev: float) -> np.ndarray:
    """Position decided at day t's close (held over day t+1 by lab.position_returns): lev if close > SMA(n)."""
    c = daily["c"]
    sma = c.rolling(n, min_periods=n).mean()
    return np.where((c > sma).to_numpy() & sma.notna().to_numpy(), float(lev), 0.0)


def complete_months(r: pd.Series) -> pd.Series:
    """Monthly compounded returns, dropping a partial first/last month of the series."""
    if r.empty:
        return pd.Series(dtype=float)
    m = np.expm1(np.log1p(r).resample("ME").sum())
    first, last = r.index[0], r.index[-1]
    if first.day != 1:
        m = m.iloc[1:]
    if (last + pd.Timedelta(days=1)).month == last.month:
        m = m.iloc[:-1]
    return m


def beat_stats(strat_daily: pd.Series, bh_daily: pd.Series) -> dict:
    s, b = complete_months(strat_daily), complete_months(bh_daily)
    s, b = s.align(b, join="inner")
    n = int(len(s))
    wins = int((s > b).sum())
    return {"months": n, "beats": wins, "beat_rate": wins / n if n else float("nan"),
            "mean_monthly_excess_pct": float((s - b).mean() * 100) if n else float("nan")}


def binom_p_one_sided(k: int, n: int) -> float:
    """P(X >= k) for X ~ Binomial(n, 0.5)."""
    if n == 0:
        return float("nan")
    return sum(math.comb(n, i) for i in range(k, n + 1)) / 2 ** n
