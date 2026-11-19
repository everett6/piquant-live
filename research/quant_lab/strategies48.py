"""PREREG48: blend A's dual-speed trend rule on a stock ETF, long/flat 1x, idle cash at T-bills."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies35 as S35
import strategies40 as S40


def dual_positions(close: pd.Series) -> pd.Series:
    d = pd.DataFrame({"c": close})
    p = 0.5 * (S35.coherence_positions(d, lev=1.0) + S40.r3_fastexit_qd_positions(d, lev=1.0))
    return pd.Series(np.clip(p, 0.0, 1.0), index=close.index)


def etf_sleeve(close: pd.Series, pos: pd.Series, rate_pct: pd.Series, cost_bps: float) -> pd.Series:
    """Return on each trading day: prior position x ETF return + idle share x T-bills over the elapsed calendar
    days - cost x |position change| (the change made at the prior close)."""
    r = close.pct_change().fillna(0.0)
    held = pos.shift(1).fillna(0.0)
    days = close.index.to_series().diff().dt.days.fillna(1).to_numpy()
    rf = rate_pct.reindex(close.index, method="ffill").fillna(0.0).to_numpy() / 100 / 365 * days
    trade = pos.diff().abs().fillna(pos.abs()).shift(1).fillna(0.0)
    return held * r + (1 - held) * rf - trade * cost_bps / 1e4


def on_calendar(ret: pd.Series, idx: pd.DatetimeIndex) -> pd.Series:
    """Place trading-day returns on a calendar (UTC daily) index; non-trading days are 0."""
    s = ret.copy()
    s.index = pd.DatetimeIndex(s.index).tz_localize("UTC") if s.index.tz is None else s.index
    return s.reindex(idx).fillna(0.0)
