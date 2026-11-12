"""Round-20 (PREREG20): beta-neutral BTC-ETH book. +1 BTC, -beta ETH; always on (N1) or only in BTC volatility
spikes (N2)."""
from __future__ import annotations

import numpy as np
import pandas as pd

FEE = 7e-4  # 6 bps fee + 1 bp slippage per unit of turnover, each leg


def hedge_ratio(rb: pd.Series, re: pd.Series, window: int = 60) -> pd.Series:
    """Slope of BTC's daily return on ETH's over the last `window` days through t, clipped to [0, 1.5]."""
    cov = rb.rolling(window, min_periods=window).cov(re)
    var = re.rolling(window, min_periods=window).var()
    return (cov / var).clip(0, 1.5)


def spike_flag(rb: pd.Series) -> pd.Series:
    """True at close t when BTC 30-day realised vol is at or above the 90th percentile of its previous 365 values."""
    vol = rb.rolling(30, min_periods=30).std()
    thr = vol.shift(1).rolling(365, min_periods=365).quantile(0.90)
    return (vol >= thr) & thr.notna()


def book_returns(rb: pd.Series, re: pd.Series, beta: pd.Series, on: pd.Series, funding_bps_h: float) -> pd.DataFrame:
    """Daily net return per 1 of capital. Positions decided at close t (btc = on, eth = -on*beta) are held over
    day t+1; turnover costs are charged on the day of the change."""
    pb = on.astype(float).where(beta.notna(), 0.0)
    pe = (-pb * beta).fillna(0.0)
    hb, he = pb.shift(1).fillna(0.0), pe.shift(1).fillna(0.0)
    gross = hb * rb + he * re
    fund = (hb.abs() + he.abs()) * funding_bps_h * 1e-4 * 24
    cost = (pb.diff().abs().fillna(pb.abs()) + pe.diff().abs().fillna(pe.abs())) * FEE
    return pd.DataFrame({"r": gross - fund - cost, "gross": gross, "held": hb != 0, "pos_e": he})


def sharpe(r: pd.Series) -> float:
    return float(r.mean() / r.std() * np.sqrt(365)) if r.std() > 0 else 0.0


def tail(r: pd.Series) -> dict:
    x = r.dropna().to_numpy()
    return {"n": int(len(x)), "q99": float(np.quantile(x, 0.01)), "q99.9": float(np.quantile(x, 0.001)),
            "worst": float(x.min())}


def stats(r: pd.Series) -> dict:
    eq = (1 + r).cumprod()
    return {"sharpe": sharpe(r), "ann_return": float(eq.iloc[-1] ** (365 / len(r)) - 1),
            "ann_vol": float(r.std() * np.sqrt(365)), "max_dd": float((eq / eq.cummax() - 1).min())}


def passes(net: dict, stress: dict, periods_pos: int, book_q999: float, btc_q999: float) -> bool:
    return (net["sharpe"] >= 1.0 and periods_pos >= 4 and stress["sharpe"] > 0.5
            and book_q999 >= 0.5 * btc_q999)  # losses are negative: "no worse than half"
