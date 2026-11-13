"""Round-25 (PREREG25): long 1x BTC / short 1x equal-weight altcoin basket, as an overlay on the P1 core."""
from __future__ import annotations

import numpy as np
import pandas as pd

MIN_HIST = 60
COINS = ("LTC", "BCH", "ETC", "XLM", "LINK", "XRP", "ADA", "DOGE", "SOL", "DOT", "AVAX", "UNI", "SHIB", "HBAR")


def basket_weights(closes: pd.DataFrame) -> pd.DataFrame:
    """Equal weights (summing to 1) over coins with a close at t and >= 60 earlier closes; rows with none are 0."""
    has = closes.notna()
    earlier = has.astype(int).cumsum().shift(1).fillna(0)
    elig = has & (earlier >= MIN_HIST)
    n = elig.sum(axis=1)
    return elig.astype(float).div(n.where(n > 0), axis=0).fillna(0.0)


def asset_returns(closes: pd.DataFrame) -> pd.DataFrame:
    """Close-to-close day returns; 0 when either day's close is missing."""
    r = closes / closes.shift(1) - 1.0
    return r.replace([np.inf, -np.inf], np.nan).fillna(0.0)


def btc_downtrend(btc_close: pd.Series, n: int = 50) -> pd.Series:
    """True at close t when P1's BTC sleeve is flat: SMA50 exists and close <= SMA50."""
    sma = btc_close.rolling(n, min_periods=n).mean()
    return (sma.notna() & ~(btc_close > sma)).astype(bool)


def pair_returns(btc_close: pd.Series, closes: pd.DataFrame, gate: pd.Series, taker_bps: float, slip_bps: float,
                 funding_bps_h: float):
    """Overlay day returns on btc_close's index. Positions decided at close t (gate_t and basket_t), held over t+1.
    Returns (total, per-asset contribution DataFrame, positions DataFrame)."""
    idx = btc_close.index
    closes = closes.reindex(idx)
    w = basket_weights(closes)
    on = gate.reindex(idx).fillna(False).astype(bool) & (w.sum(axis=1) > 0)
    pos = pd.concat([on.astype(float).rename("BTC"), -w.mul(on.astype(float), axis=0)], axis=1)
    held = pos.shift(1).fillna(0.0)
    rets = pd.concat([asset_returns(btc_close.to_frame("BTC")), asset_returns(closes)], axis=1)
    turnover = held.diff().abs()
    turnover.iloc[0] = held.iloc[0].abs()
    contrib = held * rets - turnover * (taker_bps + slip_bps) * 1e-4 - held.abs() * funding_bps_h * 24 * 1e-4
    return contrib.sum(axis=1), contrib, pos
