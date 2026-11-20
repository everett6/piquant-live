"""Round-28 (PREREG28 draft): a daily ladder of sold 30-day iron butterflies (short at-the-money put and call, long
wings two implied standard deviations away), sized by worst-case loss. Pure functions; reads nothing from disk."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

from strategies26 import BASE, STRESS, OptCosts  # noqa: F401  (same cost model as PREREG26/27)
from strategies27 import _fee, bs_put_vec

TENOR_D = 30
YEAR_D = 365.0
WING_SD = 2.0
RISK_F = 0.25


def fly_value(s, k, kd, ku, sigma, tau: float) -> np.ndarray:
    """Model value per coin of the butterfly the sleeve is short: put(k) + call(k) - put(kd) - call(ku), with
    call = put + s - strike on a futures price. At tau = 0 this is min(|s - k|, wing)."""
    return (2 * bs_put_vec(s, k, sigma, tau) + (s - k) - bs_put_vec(s, kd, sigma, tau)
            - (bs_put_vec(s, ku, sigma, tau) + s - ku))


def iron_fly_ladder(close: pd.Series, iv: pd.Series, f: float = RISK_F, oc: OptCosts = BASE,
                    tenor: int = TENOR_D, wing_sd: float = WING_SD) -> pd.DataFrame:
    """Daily return of the sold-butterfly ladder. One tranche opens at every close that has an implied volatility,
    sized so its largest possible loss is f/tenor of equity, and is held to expiry.

    ret[t] uses data up to close t only. Columns: ret, credit (received at t), paid (settled at t for the tranche
    expiring), notional (opened at t, per unit of equity), open (tranches held into t+1).
    """
    s = close.to_numpy(dtype=float)
    sig = pd.Series(iv.reindex(close.index).to_numpy(dtype=float)).ffill().to_numpy()
    n, tau = len(s), tenor / YEAR_D
    v = sig * math.sqrt(tau)
    ok = np.isfinite(sig) & np.isfinite(s) & (s > 0) & (v > 0)
    k = np.where(ok, s, np.nan)
    kd, ku = k * np.exp(-wing_sd * v), k * np.exp(wing_sd * v)
    mid = fly_value(s, k, kd, ku, sig, tau)
    hi, lo = sig * (1 + oc.iv_markup), sig * (1 - oc.iv_markup)
    body = bs_put_vec(s, k, lo, tau)  # at the money, call = put
    wing_p = bs_put_vec(s, kd, hi, tau)
    wing_c = bs_put_vec(s, ku, hi, tau) + s - ku
    credit = 2 * (body - _fee(body, s, oc)) - (wing_p + _fee(wing_p, s, oc)) - (wing_c + _fee(wing_c, s, oc))
    credit = np.minimum(credit, mid)
    max_loss = (ku - k) - credit  # the upper wing is the wider one
    ok &= np.isfinite(max_loss) & (max_loss > 0) & (credit > 0)
    units = np.where(ok, (f / tenor) / np.where(ok, max_loss, 1.0), 0.0)  # coins per unit of equity
    ret, cred, paid, n_open = np.zeros(n), np.zeros(n), np.zeros(n), np.zeros(n)
    cred[ok] = units[ok] * credit[ok]
    ret[ok] -= units[ok] * (mid[ok] - credit[ok])
    v_prev = mid.copy()
    for a in range(1, tenor + 1):
        if a >= n:
            break
        i = np.arange(n - a)
        t = i + a
        val = fly_value(s[t], k[i], kd[i], ku[i], sig[t], (tenor - a) / YEAR_D)
        live = ok[i]
        np.add.at(ret, t, np.where(live, -units[i] * (val - v_prev[i]), 0.0))
        if a < tenor:
            np.add.at(n_open, t, live.astype(float))
        else:
            np.add.at(paid, t, np.where(live, units[i] * val, 0.0))
        v_prev = np.concatenate([val, np.full(a, np.nan)])
    n_open += ok
    return pd.DataFrame({"ret": ret, "credit": cred, "paid": paid, "notional": units * np.where(ok, s, 0.0),
                         "open": n_open}, index=close.index)
