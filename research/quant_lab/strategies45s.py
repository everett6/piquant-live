"""Round-45 (PREREG45): a drawdown shutoff applied to a strategy's daily portfolio returns, and a block bootstrap of
one-year largest drawdowns. Pure functions; nothing here reads data from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

SWITCH_COST = 0.0015  # 15 bps of equity per stop or restart
RESTART_DAYS = 30


def apply_shutoff(ret: pd.Series, rate_pct: pd.Series, level: float, restart_days: int | None = None,
                  cost: float = SWITCH_COST) -> tuple[pd.Series, list[tuple[str, str]], np.ndarray]:
    """Overlay a stop on a daily return series. ret[t] is the strategy's return over day t.

    The overlay holds the strategy while on. At close t, if its own drawdown from its own peak reaches `level`, it
    pays `cost` and holds cash from day t+1, earning rate_pct (annual percent, last known value) / 100 / 365 per day.
    With restart_days set, at the first close at least that many calendar days after the stop it pays `cost`, holds
    the strategy again from day t+1, and resets its peak to its equity. Returns (overlay returns, events, on-mask
    for each day's holding)."""
    idx = ret.index
    r = ret.to_numpy(dtype=float)
    cash = (rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0)
    out = np.zeros(len(r))
    held = np.zeros(len(r), dtype=bool)
    events: list[tuple[str, str]] = []
    on, eq, peak, off_at = True, 1.0, 1.0, None
    for t in range(len(r)):
        held[t] = on
        out[t] = r[t] if on else (cash[t - 1] if t > 0 else 0.0)
        if on:
            eq *= 1 + out[t]
            peak = max(peak, eq)
            if 1 - eq / peak >= level - 1e-12:
                out[t] = (1 + out[t]) * (1 - cost) - 1
                eq *= 1 - cost
                on, off_at = False, idx[t]
                events.append(("stop", str(idx[t].date())))
        else:
            eq *= 1 + out[t]
            if restart_days is not None and (idx[t] - off_at).days >= restart_days:
                out[t] = (1 + out[t]) * (1 - cost) - 1
                eq *= 1 - cost
                on, peak = True, eq
                events.append(("restart", str(idx[t].date())))
    return pd.Series(out, index=idx), events, held


def max_drawdown(r: np.ndarray) -> float:
    eq = np.cumprod(1 + np.asarray(r, dtype=float))
    return float(1 - (eq / np.maximum.accumulate(eq)).min()) if len(eq) else 0.0


def bootstrap_year_drawdowns(ret: pd.Series, n_paths: int = 2000, days: int = 365, block: int = 20,
                             seed: int = 45) -> np.ndarray:
    """Largest drawdown of n_paths one-year paths, each built from random `block`-day blocks of ret (moving-block
    bootstrap, blocks drawn with replacement)."""
    x = ret.dropna().to_numpy(dtype=float)
    rng = np.random.default_rng(seed)
    starts_max = len(x) - block
    out = np.empty(n_paths)
    k = -(-days // block)
    for p in range(n_paths):
        s = rng.integers(0, starts_max + 1, size=k)
        path = np.concatenate([x[i:i + block] for i in s])[:days]
        out[p] = max_drawdown(path)
    return out
