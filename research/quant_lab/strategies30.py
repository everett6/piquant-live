"""Round-30 (PREREG30): P1's trend rule decided on four clocks (daily candles closing at 00, 06, 12, 18 UTC) and the
equal split of the four. Pure functions on 1-minute or 6-hour candles; reads nothing from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab
import strategies5 as S5

CLOCKS = (0, 6, 12, 18)
SMA_N = 50
LEV = 2.0
AGG = {"o": "first", "h": "max", "l": "min", "c": "last", "v": "sum"}


def bars_6h(df: pd.DataFrame) -> pd.DataFrame:
    """6-hour bars labelled by their start (00, 06, 12, 18 UTC)."""
    return df.resample("6h", label="left", closed="left").agg(AGG).dropna()


def clock_daily(b6: pd.DataFrame, hour: int) -> pd.DataFrame:
    """Daily candles that open and close at `hour` UTC, labelled by their start. Days with fewer than four 6-hour
    bars are dropped, so every close is a real 24-hour close."""
    g = b6.resample("24h", label="left", closed="left", offset=f"{hour}h")
    d = g.agg(AGG)
    return d[g["c"].count() == 4].dropna()


def clock_held(b6: pd.DataFrame, hour: int, lev: float = LEV, n: int = SMA_N) -> pd.Series:
    """Position held over each 6-hour bar by the clock-`hour` tranche: decided at its daily close (P1's rule on
    that clock's closes), held for the following 24 hours. Uses data up to each bar's start only."""
    d = clock_daily(b6, hour)
    pos = pd.Series(S5.m1_positions(d, n, lev), index=d.index + pd.Timedelta(days=1))  # effective from the close
    return pos.reindex(b6.index).ffill(limit=3).fillna(0.0)


def split_held(b6: pd.DataFrame, clocks=CLOCKS, lev: float = LEV, n: int = SMA_N) -> pd.Series:
    """Average of the clocks' positions."""
    return sum(clock_held(b6, h, lev, n) for h in clocks) / len(clocks)


BAR_H = 6.0


def grid_returns(b6: pd.DataFrame, held: pd.Series, costs: lab.Costs) -> pd.Series:
    """6-hour returns of a position series given as 'held over this bar': lab.position_returns' formula with the
    bar length fixed at 6 hours. lab infers the bar length from the first two rows, which is wrong when the data
    starts with a missing bar (ETH's first two 6-hour bars are 12 hours apart)."""
    h = held.reindex(b6.index).fillna(0.0).to_numpy(dtype=float)
    if not costs.can_short:
        h = np.clip(h, 0, None)
    ret = b6["c"].pct_change().fillna(0).to_numpy()
    turnover = np.abs(np.diff(np.concatenate([[0.0], h])))
    r = h * ret - turnover * (costs.taker_bps + costs.slip_bps) * 1e-4 - np.abs(h) * costs.funding_bps_h * 1e-4 * BAR_H
    return pd.Series(r, index=b6.index)
