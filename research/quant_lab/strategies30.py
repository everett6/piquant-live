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


def grid_returns(b6: pd.DataFrame, held: pd.Series, costs: lab.Costs) -> pd.Series:
    """6-hour returns of a position series given as 'held over this bar' (lab.position_returns' formula)."""
    dec = held.shift(-1).fillna(held.iloc[-1] if len(held) else 0.0).to_numpy()  # decided at the previous bar's close
    return lab.position_returns(b6, dec, costs)
