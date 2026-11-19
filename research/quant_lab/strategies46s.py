"""Round-46 (PREREG46): V1 constant-mix rebalancing against cash, V2/V3 gated 30-day put-spread ladders (sold when
options are expensive, bought when cheap), V4 ramped short in established downtrends. Pure functions; nothing here
reads data from disk."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

import strategies5 as S5
from strategies26 import BASE, STRESS, OptCosts, realized_vol_iv  # noqa: F401
from strategies27 import YEAR_D, _fee, bs_put_vec

TENOR_D, Q = 30, 0.5
W_COIN, BAND = 0.25, 0.05
VRP_SELL = 0.15
TAU = 5.0


def rebalance_mix(close_b: pd.Series, close_e: pd.Series, rate_pct: pd.Series, w: float = W_COIN, band: float = BAND,
                  cost_bps: float = 3.0) -> tuple[pd.Series, int]:
    """V1: w in BTC, w in ETH, 1 - 2w in T-bills. At each close, if either coin's weight is more than `band` away from
    w, all three are reset to target, paying cost_bps on the traded amount. Returns (daily return, rebalances)."""
    idx = close_b.index
    rb = close_b.pct_change().fillna(0.0).to_numpy()
    re = close_e.reindex(idx).pct_change().fillna(0.0).to_numpy()
    cash = rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy() / 100 / 365
    hb, he, hc = w, w, 1 - 2 * w  # holdings as fractions of start-of-day equity
    out, n = np.zeros(len(idx)), 0
    for t in range(len(idx)):
        if t > 0:
            vb, ve, vc = hb * (1 + rb[t]), he * (1 + re[t]), hc * (1 + cash[t - 1])
            eq = vb + ve + vc
            out[t] = eq - 1
            hb, he, hc = vb / eq, ve / eq, vc / eq
        if abs(hb - w) > band or abs(he - w) > band:
            traded = abs(hb - w) + abs(he - w)
            out[t] = (1 + out[t]) * (1 - traded * cost_bps * 1e-4) - 1
            hb, he, hc = w, w, 1 - 2 * w
            n += 1
    return pd.Series(out, index=idx), n


def gated_spread_ladder(close: pd.Series, iv: pd.Series, open_mask: np.ndarray, side: int, q: float = Q,
                        oc: OptCosts = BASE, tenor: int = TENOR_D) -> pd.DataFrame:
    """Ladder of 30-day put spreads (long K1 = close, short K2 = close * exp(-iv * sqrt(tenor/365)) when bought;
    the reverse when sold), one tranche of q/tenor opened at each close where open_mask and iv are true, held to
    expiry, marked daily at that day's iv. side = +1 buys the spread (pays the ask), -1 sells it (receives the bid).
    ret[t] uses data up to close t. Columns: ret, premium (paid if bought, received if sold), payoff (settled value),
    open (tranches held)."""
    s = close.to_numpy(dtype=float)
    sig = pd.Series(iv.reindex(close.index).to_numpy(dtype=float)).ffill().to_numpy()
    n, tau, w = len(s), tenor / YEAR_D, q / tenor
    ok = np.isfinite(sig) & np.isfinite(s) & (s > 0) & np.asarray(open_mask, dtype=bool)
    k1 = np.where(ok, s, np.nan)
    k2 = k1 * np.exp(-sig * math.sqrt(tau))
    mid = bs_put_vec(s, k1, sig, tau) - bs_put_vec(s, k2, sig, tau)
    if side > 0:
        hi = bs_put_vec(s, k1, sig * (1 + oc.iv_markup), tau)
        lo = bs_put_vec(s, k2, sig * (1 - oc.iv_markup), tau)
        price = np.maximum(hi + _fee(hi, s, oc) - (lo - _fee(lo, s, oc)), mid)  # paid
        entry = price - mid
    else:
        hi = bs_put_vec(s, k1, sig * (1 - oc.iv_markup), tau)
        lo = bs_put_vec(s, k2, sig * (1 + oc.iv_markup), tau)
        price = np.minimum(hi - _fee(hi, s, oc) - (lo + _fee(lo, s, oc)), mid)  # received
        entry = mid - price
    ret, prem, pay, n_open = np.zeros(n), np.zeros(n), np.zeros(n), np.zeros(n)
    prem[ok] = w * price[ok] / s[ok]
    ret[ok] -= w * entry[ok] / s[ok]
    v_prev = mid.copy()
    for a in range(1, tenor + 1):
        if a >= n:
            break
        i = np.arange(n - a)
        t = i + a
        rem = (tenor - a) / YEAR_D
        v = bs_put_vec(s[t], k1[i], sig[t], rem) - bs_put_vec(s[t], k2[i], sig[t], rem)
        live = ok[i]
        np.add.at(ret, t, np.where(live, side * w * (v - v_prev[i]) / s[i], 0.0))
        if a < tenor:
            np.add.at(n_open, t, live.astype(float))
        else:
            np.add.at(pay, t, np.where(live, w * v / s[i], 0.0))
        v_prev = np.concatenate([v, np.full(a, np.nan)])
    n_open += ok
    return pd.DataFrame({"ret": ret, "premium": prem, "payoff": pay, "open": n_open}, index=close.index)


def vrp_gates(daily: pd.DataFrame, iv: pd.Series, n: int = 50) -> tuple[np.ndarray, np.ndarray]:
    """(sell gate, buy gate) at each close: trend on (close > SMA(n)) and implied - realised 30-day vol >= VRP_SELL
    (sell) or implied <= realised (buy). False where either volatility is missing."""
    up = S5.m1_positions(daily, n, 1.0) > 0
    ivx = iv.reindex(daily.index).ffill()
    rv = realized_vol_iv(daily["c"], 30, 1.0)
    spread = (ivx - rv).to_numpy()
    fin = np.isfinite(spread)
    return up & fin & (spread >= VRP_SELL), up & fin & (spread <= 0)


def ramp_short_positions(daily: pd.DataFrame, tau: float = TAU) -> np.ndarray:
    """V4: -(1 - exp(-n / tau)) while the close is below both SMA50 and SMA200, n = consecutive closes below SMA50;
    0 otherwise (and until both SMAs exist)."""
    c = daily["c"]
    s50 = c.rolling(50, min_periods=50).mean()
    s200 = c.rolling(200, min_periods=200).mean()
    below50 = ((c < s50) & s50.notna()).to_numpy()
    both = below50 & ((c < s200) & s200.notna()).to_numpy()
    out = np.zeros(len(c))
    run = 0
    for i in range(len(c)):
        run = run + 1 if below50[i] else 0
        if both[i]:
            out[i] = -(1.0 - math.exp(-run / tau))
    return out
