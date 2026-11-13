"""Round-24 (PREREG24): a 4-market 12-month time-series-momentum overlay (SPY, TLT, GLD, UUP) on the P1 core."""
from __future__ import annotations

import numpy as np
import pandas as pd

TSMOM_N, VOL_N, CAP = 252, 60, 4.0
MARKETS = ("SPY", "TLT", "GLD", "UUP")


def tsmom_weights(adj: pd.Series, target: pd.Series | float) -> pd.Series:
    """w_t = sign(adj_t / adj_{t-252} - 1) * min(4, target_t / sigma60_t), decided at close t; 0 before 252 returns."""
    r = adj.pct_change()
    sig = np.sign(adj / adj.shift(TSMOM_N) - 1.0)
    vol = r.rolling(VOL_N, min_periods=VOL_N).std() * np.sqrt(252)
    tgt = target if np.isscalar(target) else pd.Series(target).reindex(adj.index).fillna(0.0)
    w = (tgt / vol.where(vol > 0)).clip(upper=CAP)
    return (sig * w).fillna(0.0)


def overlay_returns(prices: pd.DataFrame, rf_pct: pd.Series, target: pd.Series | float, cost_bps: float):
    """Day returns of the overlay on ETF dates (index = dates where every market has a price).
    Returns (total, per-market contribution DataFrame). Weights decided at close t-1 earn the excess return of t."""
    prices = prices.dropna()
    rf = rf_pct.reindex(prices.index.union(rf_pct.index)).ffill().reindex(prices.index)
    rf_day = (rf.shift(1) / 100.0 / 252.0).fillna(0.0)
    contrib = {}
    for m in prices.columns:
        w = tsmom_weights(prices[m], target)
        held = w.shift(1).fillna(0.0)
        turnover = held.diff().abs().fillna(held.abs())
        x = prices[m].pct_change().fillna(0.0) - rf_day
        contrib[m] = held * x - turnover * cost_bps * 1e-4
    c = pd.DataFrame(contrib)
    return c.sum(axis=1), c


def idle_share(pos_btc: pd.Series, pos_eth: pd.Series, etf_dates: pd.DatetimeIndex) -> pd.Series:
    """k_t for ETF date t: 0.5*[BTC flat] + 0.5*[ETH flat or no data], from P1 decisions at the crypto close of t-1.
    pos_* are P1 decisions indexed by crypto day (decided at that day's close)."""
    prev = etf_dates - pd.Timedelta(days=1)
    b = pos_btc.reindex(prev).to_numpy()
    e = pos_eth.reindex(prev).to_numpy()
    k = 0.5 * (np.nan_to_num(b, nan=0.0) == 0) + 0.5 * (np.nan_to_num(e, nan=0.0) == 0)
    return pd.Series(k, index=etf_dates)


def to_crypto_days(etf_ret: pd.Series, crypto_index: pd.DatetimeIndex) -> pd.Series:
    """Put each ETF-date return on the crypto day with the same UTC date; other days earn 0."""
    s = etf_ret.copy()
    s.index = pd.DatetimeIndex(s.index).tz_localize(None).normalize().tz_localize("UTC")
    ci = pd.DatetimeIndex(crypto_index)
    key = ci.tz_convert("UTC").normalize() if ci.tz is not None else ci.normalize().tz_localize("UTC")
    return pd.Series(s.reindex(key).fillna(0.0).to_numpy(), index=crypto_index)
