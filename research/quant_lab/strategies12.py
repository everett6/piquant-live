"""Round-12 (PREREG12): drawdown-braked BTC+ETH trend core (Q1) and BTC/ETH/SOL thirds (Q2)."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab
import strategies5 as S5


def n_sleeve(rets: list[pd.Series], index: pd.DatetimeIndex) -> pd.Series:
    """Equal weights, rebalanced daily; a sleeve with no data that day earns 0 on its share (cash)."""
    w = 1.0 / len(rets)
    return sum(w * r.reindex(index).fillna(0.0) for r in rets)


def braked_pair(dailies: list[pd.DataFrame], n: int, lev: float, costs: lab.Costs,
                dd_on: float = 0.40, dd_off: float = 0.20) -> pd.Series:
    """Equal-weight trend sleeves; while portfolio drawdown > dd_on the leverage halves until it is < dd_off.
    Brake state is decided at each close from equity through that close and applies from the next day."""
    idx = dailies[0].index
    ups, rets = [], []
    for d in dailies:
        up = pd.Series(S5.m1_positions(d, n, 1.0), index=d.index).reindex(idx).fillna(0.0).to_numpy()
        ups.append(up)
        rets.append(d["c"].pct_change().reindex(idx).fillna(0.0).to_numpy())
    bar_h = 24.0
    w = 1.0 / len(dailies)
    held = np.zeros(len(dailies))       # position held over day i (decided at close i-1)
    prev = np.zeros(len(dailies))       # position held over day i-1
    eq, peak, braked = 1.0, 1.0, False
    out = np.zeros(len(idx))
    for i in range(len(idx)):
        r = 0.0
        for k in range(len(dailies)):
            r += w * (held[k] * rets[k][i] - abs(held[k] - prev[k]) * (costs.taker_bps + costs.slip_bps) * 1e-4
                      - abs(held[k]) * costs.funding_bps_h * 1e-4 * bar_h)
        out[i] = r
        eq *= 1 + r
        peak = max(peak, eq)
        dd = 1 - eq / peak
        if not braked and dd > dd_on:
            braked = True
        elif braked and dd < dd_off:
            braked = False
        f = 0.5 if braked else 1.0
        prev = held.copy()
        held = np.array([lev * f * ups[k][i] for k in range(len(dailies))])
    return pd.Series(out, index=idx)
