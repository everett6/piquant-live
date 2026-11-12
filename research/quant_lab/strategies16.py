"""Round-16 (PREREG16): daily dip-buying — D1 uptrend dip, D2 uptrend pullback, D3 any dip."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab


def dip_trades(daily: pd.DataFrame, variant: str) -> list[tuple[int, int]]:
    """Non-overlapping (entry_index, exit_index) pairs; entry and exit at those days' closes."""
    c = daily["c"].to_numpy()
    r = daily["c"].pct_change(fill_method=None).to_numpy()
    sig = daily["c"].pct_change(fill_method=None).rolling(30, min_periods=30).std().to_numpy()
    s200 = daily["c"].rolling(200, min_periods=200).mean().to_numpy()
    s20 = daily["c"].rolling(20, min_periods=20).mean().to_numpy()
    hi20 = daily["c"].rolling(20, min_periods=20).max().to_numpy()
    n = len(c)
    out, t = [], 0
    while t < n - 1:
        up = c[t] > s200[t]  # False while SMA200 is NaN
        dip = r[t] <= -2.0 * sig[t]  # False while sigma is NaN
        if variant == "D1":
            go = up and dip
        elif variant == "D2":
            go = up and c[t] <= 0.85 * hi20[t]
        elif variant == "D3":
            go = dip
        else:
            raise ValueError(variant)
        if not go:
            t += 1
            continue
        if variant == "D2":
            ex = min(t + 10, n - 1)
            for j in range(t + 1, min(t + 10, n - 1) + 1):
                if c[j] > s20[j]:
                    ex = j
                    break
        else:
            ex = t + 5
        if ex >= n:
            break
        out.append((t, ex))
        t = ex + 1
    return out


def trade_returns(daily: pd.DataFrame, trades, costs: lab.Costs) -> pd.DataFrame:
    """Gross and net return per trade (two taker sides, funding for the hold), indexed by entry date."""
    c = daily["c"].to_numpy()
    cpt = (costs.taker_bps + costs.slip_bps) * 1e-4
    rows = []
    for e, x in trades:
        g = c[x] / c[e] - 1
        rows.append((daily.index[e], g, g - 2 * cpt - costs.funding_bps_h * 1e-4 * 24 * (x - e)))
    return pd.DataFrame(rows, columns=["entry", "gross", "net"]).set_index("entry")


def positions(n: int, trades) -> np.ndarray:
    """1.0 decided at the entry close through the day before exit (lab.position_returns convention)."""
    pos = np.zeros(n)
    for e, x in trades:
        pos[e:x] = 1.0
    return pos
