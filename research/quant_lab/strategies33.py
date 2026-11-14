"""Round-33 (PREREG33): the PREREG19 funding carry held only while a P1 sleeve is flat. Pure functions; nothing
here reads data from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies19 as S19

WAIT_D = 5


def want_idle(p1_pos: np.ndarray, wait: int = 1) -> np.ndarray:
    """Carry wanted at close t: the sleeve has been flat at each of the last `wait` closes (t included)."""
    flat = pd.Series(np.asarray(p1_pos, dtype=float) == 0)
    return flat.rolling(wait, min_periods=wait).min().fillna(0).to_numpy(dtype=bool)


def carry_when(daily: pd.DataFrame, f: pd.Series, want: np.ndarray, costs: S19.Costs) -> pd.DataFrame:
    """strategies19.coin_returns with the holding decision supplied: want[i] is decided at close i and the carry is
    held over day i+1. Columns: r (net return on the sleeve's equity), held, liq, funding, switch_cost."""
    idx = daily.index
    pos, neg, _ = S19.daily_funding(f, idx)
    c, h = daily["c"].to_numpy(float), daily["h"].to_numpy(float)
    want = np.asarray(want, dtype=bool)
    pair_cost = (costs.spot + costs.perp) * S19.N_PER_E
    n = len(idx)
    r, fund, sw = np.zeros(n), np.zeros(n), np.zeros(n)
    held_out, liq = np.zeros(n, bool), np.zeros(n, bool)
    held = False
    for i in range(n):
        if held and i > 0:
            fund[i] = (pos.iloc[i] * costs.funding_haircut + neg.iloc[i]) * S19.N_PER_E
            ret = fund[i] - abs(c[i] / c[i - 1] - 1) * (costs.spot + costs.perp) * S19.N_PER_E
            if h[i] >= S19.LIQ_MULT * c[i - 1]:
                ret -= (S19.LIQ_FEE + costs.perp) * S19.N_PER_E
                liq[i] = True
            r[i] = ret
        held_out[i] = held
        target = bool(want[i]) and not np.isnan(c[i])
        if target != held:
            r[i] -= pair_cost
            sw[i] = pair_cost
            held = target
    return pd.DataFrame({"r": r, "held": held_out, "liq": liq, "funding": fund, "switch_cost": sw}, index=idx)


def bills_on_idle(p1_pos: np.ndarray, rate_pct: pd.Series, index: pd.DatetimeIndex) -> pd.Series:
    """Daily T-bill accrual on a sleeve for the days it is held flat (decided at the previous close)."""
    flat_held = np.concatenate([[False], np.asarray(p1_pos, dtype=float)[:-1] == 0])
    rate = rate_pct.reindex(index, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100 / 365
    return pd.Series(np.where(flat_held, rate, 0.0), index=index)
