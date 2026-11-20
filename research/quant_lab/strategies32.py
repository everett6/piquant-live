"""Round-32 (PREREG32): a stablecoin supply gate on P1's leverage. Each sleeve holds 1x instead of 2x while total
stablecoin supply has not grown over the last 30 days. Pure functions; nothing here reads data from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

DAYS, LAG = 30, 1


def supply_growth(supply: pd.Series, index: pd.DatetimeIndex, days: int = DAYS, lag: int = LAG) -> pd.Series:
    """growth[t] = U[t-lag] / U[t-lag-days] - 1 on the daily candle index, with U the supply stamped on each day.
    A missing day gives no signal (no filling)."""
    u = supply[~supply.index.duplicated(keep="last")].reindex(index).where(lambda x: x > 0)
    return u.shift(lag) / u.shift(lag + days) - 1


def gate_shrinking(growth: pd.Series) -> np.ndarray:
    return (growth <= 0).fillna(False).to_numpy(dtype=bool)
