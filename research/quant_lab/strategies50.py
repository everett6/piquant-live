"""PREREG50: hold a daily position series fixed between decision days (weekly or monthly)."""
from __future__ import annotations

import numpy as np
import pandas as pd


def decision_mask(idx: pd.DatetimeIndex, schedule: str, day: int | None = None) -> np.ndarray:
    """True on the daily closes where a new decision is taken.
    weekly: closes on weekday `day` (default 6 = Sunday bar, i.e. the 00:00 UTC Monday close).
    monthly: the last close of each month, or the close on day-of-month `day` if given."""
    if schedule == "weekly":
        return np.asarray(idx.dayofweek == (6 if day is None else day))
    if schedule == "monthly":
        if day is not None:
            return np.asarray(idx.day == day)
        m = idx.tz_convert(None).to_period("M") if idx.tz is not None else idx.to_period("M")
        return np.r_[m[1:] != m[:-1], True]
    raise ValueError(schedule)


def hold_between(pos: pd.Series, mask: np.ndarray) -> pd.Series:
    """Position read only on decision closes and carried forward; 0 before the first decision."""
    return pos.where(mask).ffill().fillna(0.0)


def turnover_per_year(pos: pd.Series) -> float:
    return float(pos.diff().abs().sum() / (len(pos) / 365))
