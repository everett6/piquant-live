"""PREREG49: two-asset portfolio rebalanced to fixed weights on the first day of each month, with cost."""
from __future__ import annotations

import numpy as np
import pandas as pd


def monthly_rebalanced(ra: pd.Series, rb: pd.Series, wb: float, cost_bps: float) -> tuple[pd.Series, int]:
    """Daily returns of (1-wb) in A and wb in B, reset to target at the first day of each month (the rebalance is
    done at the open of that day before its return; cost = bps x total amount moved, charged that day)."""
    a, b = ra.to_numpy(dtype=float), rb.reindex(ra.index).fillna(0.0).to_numpy(dtype=float)
    month = ra.index.to_period("M") if ra.index.tz is None else ra.index.tz_convert(None).to_period("M")
    new_month = np.r_[True, month[1:] != month[:-1]]
    out, n_reb = np.zeros(len(a)), 0
    va, vb = 1.0 - wb, wb
    for t in range(len(a)):
        cost = 0.0
        if new_month[t]:
            tot = va + vb
            moved = abs(va - (1 - wb) * tot) + abs(vb - wb * tot)
            if t > 0 and moved > 1e-12:
                n_reb += 1
            cost = moved * cost_bps / 1e4 / tot if t > 0 else 0.0
            va, vb = (1 - wb) * tot * (1 - cost), wb * tot * (1 - cost)
        start = va + vb
        va, vb = va * (1 + a[t]), vb * (1 + b[t])
        out[t] = (va + vb) / (start / (1 - cost)) - 1 if cost else (va + vb) / start - 1
    return pd.Series(out, index=ra.index), n_reb
