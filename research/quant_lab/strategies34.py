"""Round-34 (PREREG34): two sizing rules shaped like quantum-mechanics formulas. QA squares the summed votes of the
four clocks (Born rule); QB sizes by exp(-2 d / sigma), the tunnelling form, with d the log gap above SMA50.
Pure functions; nothing here reads data from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies30 as S30

SMA_N, LEV, VOL_N, KAPPA = 50, 2.0, 30, 2.0


def clocks_on(b6: pd.DataFrame, n: int = SMA_N) -> pd.Series:
    """Number of clocks (0..4) whose tranche is long over each 6-hour bar."""
    return sum(S30.clock_held(b6, h, 1.0, n) for h in S30.CLOCKS)


def interference_held(b6: pd.DataFrame, lev: float = LEV, n: int = SMA_N) -> pd.Series:
    """QA: position held over each 6-hour bar = lev * (clocks on / 4) ** 2."""
    return lev * (clocks_on(b6, n) / len(S30.CLOCKS)) ** 2


def tunnel_positions(daily: pd.DataFrame, lev: float = LEV, n: int = SMA_N, vol_n: int = VOL_N,
                     kappa: float = KAPPA) -> np.ndarray:
    """QB: position decided at each daily close = lev * (1 - exp(-kappa * ln(close / SMA) / sigma)) while the close
    is above its SMA(n), else 0. sigma is the standard deviation of the last vol_n daily returns."""
    c = daily["c"]
    sma = c.rolling(n, min_periods=n).mean()
    sig = c.pct_change().rolling(vol_n, min_periods=vol_n).std()
    d = np.log(c / sma)
    pos = lev * (1.0 - np.exp(-kappa * d / sig))
    ok = (c > sma) & sma.notna() & (sig > 0)
    return np.where(ok.to_numpy(), pos.to_numpy(), 0.0)
