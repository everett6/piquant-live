"""Round-39 (PREREG39): Quiet-Bull Vol-Calm Scaling + 40/40/20 Vol-Calm QD + Leader Basket + C1 Carry.
Pure functions; reads nothing from disk."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

VOL_WIN = 20
VOL_TARGET = 0.55
VOL_FLOOR = 0.25
MULT_MIN = 0.50
MULT_MAX = 1.35


def vol_calm_multiplier(
    closes: pd.Series,
    win: int = VOL_WIN,
    target_ann_vol: float = VOL_TARGET,
    vol_floor: float = VOL_FLOOR,
    m_min: float = MULT_MIN,
    m_max: float = MULT_MAX,
) -> pd.Series:
    """Strictly causal 20-day inverse realized-vol scaling clipped to [0.50, 1.35].

    Until `win` daily returns exist, defaults safely to 1.00.
    """
    ret = closes.pct_change()
    ann_vol = ret.rolling(win, min_periods=win).std() * math.sqrt(365.0)
    raw = target_ann_vol / ann_vol.clip(lower=vol_floor)
    mult = raw.clip(lower=m_min, upper=m_max)
    return mult.where(ann_vol.notna(), 1.0).astype(float)
