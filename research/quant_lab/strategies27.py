"""Round-27 (PREREG27 draft): an always-on daily ladder of 30-day put spreads (long at-the-money, short one implied
standard deviation below), priced from implied volatility. Pure functions; nothing here reads data from disk."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

from strategies26 import BASE, STRESS, OptCosts, realized_vol_iv  # noqa: F401  (same cost model as PREREG26)

TENOR_D = 30
YEAR_D = 365.0
Q = 0.5

_erf = np.vectorize(math.erf, otypes=[float])


def _ncdf(x: np.ndarray) -> np.ndarray:
    return 0.5 * (1.0 + _erf(x / math.sqrt(2.0)))


def bs_put_vec(s, k, sigma, tau: float) -> np.ndarray:
    """Black-Scholes put on a futures price, elementwise. Intrinsic value where sigma or tau is not positive.
    NaN inputs give NaN."""
    s, k, sigma = (np.asarray(x, dtype=float) for x in (s, k, sigma))
    intrinsic = np.maximum(k - s, 0.0)
    if tau <= 0:
        return intrinsic
    live = sigma > 0
    v = np.where(live, sigma, 1.0) * math.sqrt(tau)
    with np.errstate(invalid="ignore", divide="ignore"):
        d1 = (np.log(s / k) + 0.5 * v * v) / v
        out = k * _ncdf(-(d1 - v)) - s * _ncdf(-d1)
    out = np.where(live, out, intrinsic)
    return np.where(np.isnan(s + k + sigma), np.nan, out)


def _fee(premium: np.ndarray, spot: np.ndarray, oc: OptCosts) -> np.ndarray:
    return np.minimum(oc.fee_bps * 1e-4 * spot, oc.fee_cap * premium)


def put_spread_ladder(close: pd.Series, iv: pd.Series, q: float = Q, oc: OptCosts = BASE,
                      tenor: int = TENOR_D) -> pd.DataFrame:
    """Daily return of holding q units of notional (per unit of equity) in put spreads, one tranche of q/tenor
    opened at every close with an implied volatility and held to expiry.

    Tranche opened at close i: long put K1 = close[i], short put K2 = close[i] * exp(-iv[i] * sqrt(tenor/365)).
    It is marked each day at that day's iv (last known value if missing). ret[t] uses data up to close t only.
    Columns: ret, premium (paid at t), payoff (received at t from the tranche expiring), open (tranches held).
    """
    s = close.to_numpy(dtype=float)
    sig_raw = iv.reindex(close.index).to_numpy(dtype=float)
    sig = pd.Series(sig_raw).ffill().to_numpy()
    n, tau, w = len(s), tenor / YEAR_D, q / tenor
    ok = np.isfinite(sig) & np.isfinite(s) & (s > 0)
    k1 = np.where(ok, s, np.nan)
    k2 = k1 * np.exp(-sig * math.sqrt(tau))
    mid = bs_put_vec(s, k1, sig, tau) - bs_put_vec(s, k2, sig, tau)
    buy = bs_put_vec(s, k1, sig * (1 + oc.iv_markup), tau)
    sell = bs_put_vec(s, k2, sig * (1 - oc.iv_markup), tau)
    cost = buy + _fee(buy, s, oc) - (sell - _fee(sell, s, oc))
    cost = np.maximum(cost, mid)
    ret, prem, pay, n_open = np.zeros(n), np.zeros(n), np.zeros(n), np.zeros(n)
    prem[ok] = w * cost[ok] / s[ok]
    ret[ok] -= w * (cost[ok] - mid[ok]) / s[ok]
    v_prev = mid.copy()  # value at age a-1, indexed by the opening day i
    for a in range(1, tenor + 1):
        if a >= n:
            break
        i = np.arange(n - a)
        t = i + a
        rem = (tenor - a) / YEAR_D
        v = bs_put_vec(s[t], k1[i], sig[t], rem) - bs_put_vec(s[t], k2[i], sig[t], rem)
        live = ok[i]
        d = np.where(live, w * (v - v_prev[i]) / s[i], 0.0)
        np.add.at(ret, t, d)
        if a < tenor:
            np.add.at(n_open, t, live.astype(float))
        else:
            np.add.at(pay, t, np.where(live, w * v / s[i], 0.0))
        v_prev = np.concatenate([v, np.full(a, np.nan)])
    n_open += ok  # the tranche opened today is held into tomorrow
    return pd.DataFrame({"ret": ret, "premium": prem, "payoff": pay, "open": n_open}, index=close.index)


def splice_iv(close: pd.Series, dvol: pd.Series) -> pd.Series:
    """Implied volatility for the whole history: the DVOL series where it exists, and before its first day the
    PREREG26 stand-in (1.2 x trailing 30-day realized volatility)."""
    d = dvol.reindex(close.index)
    first = dvol.dropna().index.min()
    stand_in = realized_vol_iv(close)
    return d.where(close.index >= first, stand_in) if first is not None and first == first else stand_in
