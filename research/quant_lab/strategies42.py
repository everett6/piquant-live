"""Round-42 (PREREG42): Expected Drawdown reduction & recovery speed strategies.
Pure functions; zero disk I/O."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd


def drawdown_diagnostics(daily_ret: pd.Series) -> dict[str, float]:
    """Compute Expected Drawdown (E[DD]), Ulcer Index, P95/CDaR95, and underwater duration stats."""
    r = daily_ret.dropna().to_numpy(dtype=float)
    n = len(r)
    if n == 0:
        return {
            "mean_dd_pct": 0.0,
            "ulcer_index_pct": 0.0,
            "dd_p95_pct": 0.0,
            "cdar_95_pct": 0.0,
            "pct_days_underwater": 0.0,
            "max_underwater_days": 0,
            "mean_recovery_days": 0.0,
        }
    eq = np.cumprod(1.0 + r)
    peak = np.maximum.accumulate(eq)
    dd = np.maximum(0.0, 1.0 - eq / np.maximum(peak, 1e-12)) * 100.0  # positive % depths

    mean_dd = float(np.mean(dd))
    ulcer = float(np.sqrt(np.mean(dd * dd)))
    p95 = float(np.percentile(dd, 95))
    tail = dd[dd >= p95]
    cdar95 = float(np.mean(tail)) if len(tail) > 0 else p95

    underwater = dd > 1e-6
    pct_uw = float(np.mean(underwater) * 100.0)
    max_streak = 0
    cur_streak = 0
    ep_lengths = []
    for u in underwater:
        if u:
            cur_streak += 1
            if cur_streak > max_streak:
                max_streak = cur_streak
        elif cur_streak > 0:
            ep_lengths.append(cur_streak)
            cur_streak = 0
    if cur_streak > 0:
        ep_lengths.append(cur_streak)

    return {
        "mean_dd_pct": round(mean_dd, 4),
        "ulcer_index_pct": round(ulcer, 4),
        "dd_p95_pct": round(p95, 4),
        "cdar_95_pct": round(cdar95, 4),
        "pct_days_underwater": round(pct_uw, 2),
        "max_underwater_days": int(max_streak),
        "mean_recovery_days": round(float(np.mean(ep_lengths)) if ep_lengths else 0.0, 2),
    }


def downside_vol_cap_positions(
    daily: pd.DataFrame,
    base_pos: pd.Series,
    vol_target_ann: float = 0.45,
    vol_window: int = 20,
) -> pd.Series:
    """Strictly causal one-sided protective volatility cap (multiplier in (0, 1.0]).

    At close t, scales `base_pos[t]` down when trailing `vol_window`-day annualized volatility
    exceeds `vol_target_ann`, and never increases leverage above `base_pos[t]`.
    """
    rets = daily["c"].pct_change().fillna(0.0)
    ann_vol = rets.rolling(vol_window, min_periods=vol_window).std(ddof=1) * math.sqrt(365.0)
    mult = (vol_target_ann / ann_vol.clip(lower=1e-6)).clip(lower=0.0, upper=1.0).fillna(1.0)
    aligned = base_pos.reindex(daily.index).fillna(0.0)
    return aligned * mult
