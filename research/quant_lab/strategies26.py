"""Round-26 (PREREG26 draft): convex leverage. P1's futures leg plus bought 30-day at-the-money puts priced from
implied volatility (Deribit DVOL). Pure functions; nothing here reads data from disk."""
from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
import pandas as pd

import lab
import strategies5 as S5

TENOR_D = 30
YEAR_D = 365.0
SMA_N = 50
LEV = 2.0
Q = {"K1_convex_leverage": LEV - 1.0, "K2_full_call": LEV}


@dataclass(frozen=True)
class OptCosts:
    name: str
    iv_markup: float  # buy at iv*(1+m), sell at iv*(1-m)
    fee_bps: float    # per trade, bps of spot
    fee_cap: float    # fee is at most this fraction of the premium


BASE = OptCosts("base", 0.10, 3.0, 0.125)
STRESS = OptCosts("stress", 0.25, 6.0, 0.25)


def _ncdf(x: float) -> float:
    return 0.5 * (1.0 + math.erf(x / math.sqrt(2.0)))


def bs_put(s: float, k: float, sigma: float, tau: float) -> float:
    """Black-Scholes put on a futures price (no interest term). Intrinsic value when sigma or tau is not positive."""
    if not (sigma > 0 and tau > 0):
        return max(k - s, 0.0)
    v = sigma * math.sqrt(tau)
    d1 = (math.log(s / k) + 0.5 * v * v) / v
    return k * _ncdf(-(d1 - v)) - s * _ncdf(-d1)


def _fee(premium: float, spot: float, oc: OptCosts) -> float:
    return min(oc.fee_bps * 1e-4 * spot, oc.fee_cap * premium)


def put_overlay(close: pd.Series, iv: pd.Series, on: np.ndarray, q: float, oc: OptCosts,
                tenor: int = TENOR_D) -> pd.DataFrame:
    """Daily return added to a sleeve by holding q puts per unit of equity while `on`.

    on[t] is decided at close t. A put bought at close t has strike close[t] and expires at close t+tenor. It is
    marked each day at the day's iv (last known value if missing), sold when on turns false, and replaced at expiry
    while on stays true. ret[t] uses data up to close t only.
    Columns: ret, premium (paid, fraction of equity), payoff (expiry or sale proceeds), held (put held into t+1).
    """
    s = close.to_numpy(dtype=float)
    sig = iv.reindex(close.index).ffill().to_numpy(dtype=float)
    on = np.asarray(on, dtype=bool)
    n = len(s)
    ret, prem, pay, held_out = np.zeros(n), np.zeros(n), np.zeros(n), np.zeros(n, dtype=bool)
    held, k, exp, v_prev = False, 0.0, 0, 0.0
    for t in range(n):
        if held:
            expired = t >= exp
            v = max(k - s[t], 0.0) if expired else bs_put(s[t], k, sig[t], (exp - t) / YEAR_D)
            ret[t] += q * (v - v_prev) / s[t - 1]
            v_prev = v
            if expired:
                pay[t] += q * v / s[t]
                held = False
            elif not on[t]:
                bid = max(bs_put(s[t], k, sig[t] * (1 - oc.iv_markup), (exp - t) / YEAR_D), max(k - s[t], 0.0))
                bid = min(bid, v)
                net = bid - _fee(bid, s[t], oc)
                ret[t] -= q * (v - net) / s[t]
                pay[t] += q * net / s[t]
                held = False
        if on[t] and not held and np.isfinite(sig[t]) and q > 0:
            tau = tenor / YEAR_D
            mid = bs_put(s[t], s[t], sig[t], tau)
            ask = bs_put(s[t], s[t], sig[t] * (1 + oc.iv_markup), tau)
            cost = ask + _fee(ask, s[t], oc)
            ret[t] -= q * (cost - mid) / s[t]
            prem[t] = q * cost / s[t]
            held, k, exp, v_prev = True, s[t], t + tenor, mid
        held_out[t] = held
    return pd.DataFrame({"ret": ret, "premium": prem, "payoff": pay, "held": held_out}, index=close.index)


def convex_sleeve(daily: pd.DataFrame, iv: pd.Series, q: float, costs: lab.Costs, oc: OptCosts,
                  lev: float = LEV) -> tuple[pd.Series, pd.DataFrame]:
    """One coin: P1's futures leg (lev while close > SMA50) plus the put overlay. Returns (daily return, overlay)."""
    pos = S5.m1_positions(daily, SMA_N, lev)
    ov = put_overlay(daily["c"], iv, pos > 0, q, oc)
    return lab.position_returns(daily, pos, costs) + ov["ret"], ov


def realized_vol_iv(close: pd.Series, n: int = 30, mult: float = 1.2) -> pd.Series:
    """Stand-in implied volatility for the pre-DVOL years (reported outside the verdict): trailing n-day realized
    volatility, annualised, times mult."""
    return close.pct_change().rolling(n, min_periods=n).std() * math.sqrt(YEAR_D) * mult
