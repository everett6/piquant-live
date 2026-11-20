"""Round-31 (PREREG31): a funding crowding gate on P1's leverage. Each sleeve holds 1x instead of 2x while its
coin's trailing 7-day mean funding rate is high. Pure functions; nothing here reads data from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies5 as S5

SMA_N, LEV, LOW = 50, 2.0, 1.0
DAYS = 7
THR_ABS = 2e-4  # 0.02% per 8 hours
Q, LOOK, MIN_OBS = 0.80, 365, 350


def crowd_signal(f8: pd.Series, index: pd.DatetimeIndex, days: int = DAYS) -> pd.Series:
    """Trailing `days`-day mean of the daily mean funding rate, on the daily candle index (labelled by day start).
    The value at day t uses payments stamped on day t or earlier, all before that day's close."""
    daily = f8.resample("1D").mean()
    return daily.rolling(days, min_periods=days).mean().reindex(index)


def gate_abs(sig: pd.Series, thr: float = THR_ABS) -> np.ndarray:
    return (sig > thr).fillna(False).to_numpy(dtype=bool)


def gate_rel(sig: pd.Series, q: float = Q, look: int = LOOK, min_obs: int = MIN_OBS) -> np.ndarray:
    """On while the signal is above the q-quantile of its previous `look` days (today not included)."""
    thr = sig.rolling(look, min_periods=min_obs).quantile(q).shift(1)
    return (sig > thr).fillna(False).to_numpy(dtype=bool)


def gated_positions(daily: pd.DataFrame, gate: np.ndarray, lev: float = LEV, low: float = LOW,
                    n: int = SMA_N) -> np.ndarray:
    """P1's position, cut to `low` where the gate is on. Never raises a position."""
    pos = S5.m1_positions(daily, n, lev)
    return np.where(np.asarray(gate, dtype=bool) & (pos > low), low, pos)
