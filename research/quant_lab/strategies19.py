"""Round-19 (PREREG19): funding carry, long spot + short perpetual (2x on the short leg), daily simulation."""
from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

N_PER_E = 1 / 1.5  # notional per unit of coin equity: spot N plus margin N/2
LIQ_MULT = 1.5     # a day high at 1.5x the previous close liquidates the 2x short
LIQ_FEE = 0.01     # liquidation fee, fraction of N
C2_MIN_RATE = 0.0001  # trailing 7-day mean 8h funding >= 0.01%
PERIODS = {"2020": ("2020-01-01", "2021-01-01"), "2021": ("2021-01-01", "2022-01-01"),
           "2022": ("2022-01-01", "2023-01-01"), "2023_24": ("2023-01-01", "2025-01-01"),
           "2025_end": ("2025-01-01", "2100-01-01")}


@dataclass(frozen=True)
class Costs:
    name: str
    spot: float          # per side, fraction (fee + slippage)
    perp: float
    funding_haircut: float  # multiplier on funding received (paid is never reduced)


BASE = Costs("base", 3e-4, 7e-4, 1.0)
STRESS = Costs("stress", 6e-4, 14e-4, 0.75)


def daily_funding(f: pd.Series, index: pd.DatetimeIndex) -> tuple[pd.Series, pd.Series, pd.Series]:
    """From 8-hourly rates (indexed by UTC funding time): per-day sums of the positive and negative parts, and
    the trailing 7-day (21-payment) mean rate as of each day's close."""
    f = f.sort_index()
    day = f.index.floor("D")
    pos = f.clip(lower=0).groupby(day).sum().reindex(index, fill_value=0.0)
    neg = f.clip(upper=0).groupby(day).sum().reindex(index, fill_value=0.0)
    roll = f.rolling(21, min_periods=21).mean()
    trail = roll.groupby(day).last().reindex(index).ffill()
    return pos, neg, trail


def coin_returns(daily: pd.DataFrame, f: pd.Series, rule: str, costs: Costs) -> pd.DataFrame:
    """Daily net return of one coin's carry book (fraction of that coin's equity) plus held flag and liquidations.
    The position held over day i is decided at close i-1."""
    idx = daily.index
    pos, neg, trail = daily_funding(f, idx)
    c, h = daily["c"].to_numpy(float), daily["h"].to_numpy(float)
    want = np.ones(len(idx), bool) if rule == "C1" else (trail.to_numpy() >= C2_MIN_RATE)
    pair_cost = (costs.spot + costs.perp) * N_PER_E
    r = np.zeros(len(idx))
    held_out = np.zeros(len(idx), bool)
    liq = np.zeros(len(idx), bool)
    held = False
    for i in range(len(idx)):
        if held and i > 0:
            ret = (pos.iloc[i] * costs.funding_haircut + neg.iloc[i]) * N_PER_E
            ret -= abs(c[i] / c[i - 1] - 1) * (costs.spot + costs.perp) * N_PER_E  # daily leg rebalance
            if h[i] >= LIQ_MULT * c[i - 1]:
                ret -= (LIQ_FEE + costs.perp) * N_PER_E
                liq[i] = True
            r[i] = ret
        held_out[i] = held
        target = bool(want[i]) and not np.isnan(c[i])
        if target != held:
            r[i] -= pair_cost
            held = target
    return pd.DataFrame({"r": r, "held": held_out, "liq": liq}, index=idx)


def portfolio(books: list[pd.DataFrame]) -> pd.Series:
    """Equal halves rebalanced daily."""
    return pd.concat([b["r"] for b in books], axis=1).fillna(0.0).mean(axis=1)


def stats(r: pd.Series) -> dict:
    eq = (1 + r).cumprod()
    days = max(len(r), 1)
    ann = float(eq.iloc[-1] ** (365.0 / days) - 1) if len(r) else 0.0
    dd = float((eq / eq.cummax() - 1).min()) if len(r) else 0.0
    under = (eq < eq.cummax()).to_numpy()
    longest = cur = 0
    for u in under:
        cur = cur + 1 if u else 0
        longest = max(longest, cur)
    monthly = float(eq.iloc[-1] ** (30.4375 / days) - 1) if len(r) else 0.0
    per = {}
    for k, (a, b) in PERIODS.items():
        x = r[(r.index >= pd.Timestamp(a, tz="UTC")) & (r.index < pd.Timestamp(b, tz="UTC"))]
        per[k] = float((1 + x).prod() - 1) if len(x) else None
    return {"ann": ann, "max_dd": dd, "monthly": monthly, "longest_underwater_days": longest, "periods": per}


def passes(st_stress: dict, liq_per_coin_year: float) -> bool:
    pos_periods = sum(1 for v in st_stress["periods"].values() if v is not None and v > 0)
    return (st_stress["ann"] >= 0.05 and st_stress["max_dd"] >= -0.10 and pos_periods >= 4
            and liq_per_coin_year <= 1.0)
