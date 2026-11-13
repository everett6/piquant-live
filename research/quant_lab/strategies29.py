"""Round-29 (PREREG29): cross-sectional momentum. Each Monday close, long the top third and short the bottom third of
eligible coins by 21-day return, equal weights, gross 2x, net 0. Pure functions; reads nothing from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

COINS = ["BTC", "ETH", "LTC", "BCH", "ETC", "XLM", "LINK", "XRP", "ADA", "DOGE", "SOL", "DOT", "AVAX", "UNI", "SHIB",
         "HBAR"]
FORM_D = 21
MIN_HIST = 60
MIN_COINS = 6
REBAL_WEEKDAY = 0  # Monday


def scores(closes: pd.DataFrame) -> pd.DataFrame:
    """21-day return at each close for eligible coins (a close today, >= 60 earlier closes, a close 21 days ago);
    NaN otherwise."""
    earlier = closes.notna().cumsum() - closes.notna().astype(int)
    sc = closes / closes.shift(FORM_D) - 1.0
    return sc.where(closes.notna() & (earlier >= MIN_HIST))


def target_weights(closes: pd.DataFrame) -> pd.DataFrame:
    """Weights decided at each close. Set on Mondays, held until the next Monday; rows before the first rebalance
    are 0. w[t] uses closes up to t only."""
    sc = scores(closes[COINS] if all(c in closes for c in COINS) else closes)
    cols = list(sc.columns)
    w = pd.DataFrame(np.nan, index=sc.index, columns=cols)
    rebal = sc.index.dayofweek == REBAL_WEEKDAY
    w.iloc[0] = 0.0
    for t in np.flatnonzero(rebal):
        row = sc.iloc[t]
        ok = row.dropna()
        new = pd.Series(0.0, index=cols)
        if len(ok) >= MIN_COINS and ok.max() > ok.min():
            k = len(ok) // 3
            order = ok.sort_values(ascending=False, kind="mergesort")  # stable: ties keep the fixed coin order
            new[order.index[:k]] = 1.0 / k
            new[order.index[-k:]] = -1.0 / k
        w.iloc[t] = new
    return w.ffill().fillna(0.0)


def book_returns(closes: pd.DataFrame, w: pd.DataFrame, taker_bps: float, slip_bps: float,
                 funding_bps_h: float) -> pd.DataFrame:
    """Day returns of the book: weights decided at close t are held over day t+1 (lab.position_returns timing and
    cost model, per coin). A coin with no price on either day earns 0 that day.
    Columns: total, long, short (legs before costs), cost."""
    ret = closes[w.columns].pct_change(fill_method=None).fillna(0.0)
    held = w.shift(1).fillna(0.0)
    turnover = held.diff().abs().fillna(held.abs()).sum(axis=1)
    cost = turnover * (taker_bps + slip_bps) * 1e-4 + held.abs().sum(axis=1) * funding_bps_h * 1e-4 * 24
    gross = held * ret
    long_leg, short_leg = gross.where(held > 0, 0.0).sum(axis=1), gross.where(held < 0, 0.0).sum(axis=1)
    return pd.DataFrame({"total": long_leg + short_leg - cost, "long": long_leg, "short": short_leg, "cost": cost})
