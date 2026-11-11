"""Strategies from PREREG.md. Each returns either bracket-trade inputs or a target-position series.

All features use only data up to and including the signal bar's close (rolling windows, shift-free);
the harness fills at the next bar's open.
"""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

import lab

# ---------------------------------------------------------------------------------------------------------
# S1: engine-style short-horizon momentum (or fade) with vol-scaled brackets
# ---------------------------------------------------------------------------------------------------------
S1_GRID = [dict(th=th, k_tp=k_tp, k_sl=k_sl, mode=mode)
           for th in (0.60, 0.65, 0.70, 0.75)
           for k_tp in (1.0, 1.4, 2.0, 3.0)
           for k_sl in (0.8, 1.2, 2.0)
           for mode in (1, -1)]  # 96 configs; mode +1 = momentum (live engine), -1 = fade


def s1_features(df: pd.DataFrame) -> pd.DataFrame:
    lc = np.log(df["c"])
    f = pd.DataFrame(index=df.index)
    for h in (1, 5, 15):
        r = lc.diff(h)
        f[f"z{h}"] = r / r.rolling(1440, min_periods=720).std()
    f["sigma15_bps"] = lc.diff(15).rolling(1440, min_periods=720).std() * 1e4
    f["score"] = 0.5 * f["z1"] + 0.3 * f["z5"] + 0.2 * f["z15"]
    f["conv"] = 0.5 + 0.5 * np.tanh(f["score"] / 2)
    return f


def s1_run(df: pd.DataFrame, f: pd.DataFrame, p: dict, costs: lab.Costs) -> pd.DataFrame:
    conv = f["conv"].to_numpy()
    sig = np.where(conv > p["th"], 1, np.where(conv < 1 - p["th"], -1, 0)) * p["mode"]
    sig = np.where(np.isfinite(conv), sig, 0)
    sigma = f["sigma15_bps"].to_numpy()
    return lab.simulate_brackets(df["o"].to_numpy(), df["h"].to_numpy(), df["l"].to_numpy(), df["c"].to_numpy(),
                                 df.index.asi8, sig, p["k_tp"] * sigma, p["k_sl"] * sigma,
                                 max_hold=240, loser_stop=45, costs=costs)


# ---------------------------------------------------------------------------------------------------------
# S2: classical trend following on 1h bars (EMA cross), position +-1 (futures) or 0/1 (spot)
# ---------------------------------------------------------------------------------------------------------
S2_GRID = [dict(fast=fa, slow=sl, mode=m)
           for fa in (6, 12, 24, 48) for sl in (72, 168, 336) for m in ("ls", "lf")]  # 24 configs


def s2_positions(h1: pd.DataFrame, p: dict) -> np.ndarray:
    fast = h1["c"].ewm(span=p["fast"], adjust=False).mean()
    slow = h1["c"].ewm(span=p["slow"], adjust=False).mean()
    pos = np.where(fast > slow, 1.0, -1.0)
    if p["mode"] == "lf":
        pos = np.where(pos > 0, 1.0, 0.0)
    pos[: p["slow"]] = 0.0  # warm-up
    return pos


# ---------------------------------------------------------------------------------------------------------
# S3: quantum harmonic / anharmonic oscillator price levels (after Lee, "Quantum Finance", QPL method)
# ---------------------------------------------------------------------------------------------------------
def qpl_ratios(daily_ret: np.ndarray, n_levels: int = 5, bins: int = 100) -> np.ndarray | None:
    """Energy-level ratios E_n/E_0 of a quantum anharmonic oscillator fitted to a return distribution.

    1. |psi|^2 ~ histogram of returns; r0 = mode, dr = bin width.
    2. Anharmonic coefficient lambda from the ground-state wave function around the mode.
    3. For each level n, the energy E solves E^3 - (2n+1) E - lambda * K_n^3 = 0 (Lee's cubic), with
       K_n = ((1.1924 + 33.2383 n + 56.2169 n^2) / (1 + 43.6106 n))^(1/3).
    Returns E_n / E_0 for n = 0..n_levels (ratio > 1 grows with n), or None if the fit is degenerate.
    """
    r = daily_ret[np.isfinite(daily_ret)]
    if len(r) < 15 or r.std() == 0:
        return None
    hist, edges = np.histogram(r, bins=bins)
    q = hist / hist.sum()
    k = int(np.argmax(q))
    if k == 0:
        return None
    dr = edges[1] - edges[0]
    r0 = (edges[k] + edges[k + 1]) / 2
    r_1 = r0 - dr
    q0, q1 = q[k], q[k - 1]
    num = (r_1 ** 2) * q1 - (r0 ** 2) * q0
    den = (r0 ** 4) * q0 - (r_1 ** 4) * q1
    lam = abs(num / den) if den != 0 else 0.0
    energies = []
    for n in range(n_levels + 1):
        kn = ((1.1924 + 33.2383 * n + 56.2169 * n * n) / (1 + 43.6106 * n)) ** (1 / 3)
        roots = np.roots([1.0, 0.0, -(2 * n + 1), -lam * kn ** 3])
        real = roots[np.abs(roots.imag) < 1e-9].real
        real = real[real > 0]
        if len(real) == 0:
            return None
        energies.append(real.max())
    e = np.array(energies)
    return e / e[0]


S3_GRID = [dict(window=w, level=lv, gate=g, target=t)
           for w in (20, 60) for lv in (1, 2, 3) for g in (True, False) for t in ("ground", "inner")]  # 24 configs


def s3_levels(df: pd.DataFrame, window: int) -> pd.DataFrame:
    """Daily QPL bands from the previous `window` days of daily returns: level_n = prevclose*(1 +- sigma*(R_n-1)*... )."""
    d = lab.resample(df, "1D")
    ret = d["c"].pct_change()
    rows = []
    for i in range(len(d)):
        if i < window + 1:
            rows.append([np.nan] * 4)
            continue
        hist = ret.iloc[i - window:i].to_numpy()
        ratios = qpl_ratios(hist)
        sig = np.nanstd(hist)
        if ratios is None or not np.isfinite(sig):
            rows.append([np.nan] * 4)
            continue
        # band width per level: sigma * E_n/E_0 (n=1..3), ground band = previous close
        rows.append([sig * ratios[1], sig * ratios[2], sig * ratios[3], sig])
    lv = pd.DataFrame(rows, index=d.index, columns=["b1", "b2", "b3", "sigma"])
    lv["anchor"] = d["c"].shift(1)  # previous day's close is the ground state
    lv["trend"] = (d["c"].shift(1) / d["c"].shift(21) - 1) / (lv["sigma"] * math.sqrt(20))
    return lv


def s3_run(df: pd.DataFrame, h1: pd.DataFrame, lv: pd.DataFrame, p: dict, costs: lab.Costs) -> pd.DataFrame:
    """1h bars: fade a close beyond band `level` toward the anchor (or the next inner band); stop beyond next band."""
    day = h1.index.floor("D")
    L = lv.reindex(day)
    anchor = L["anchor"].to_numpy()
    k = p["level"]
    band = L[f"b{k}"].to_numpy()
    nxt = L[f"b{min(k + 1, 3)}"].to_numpy() * (1.0 if k < 3 else 1.5)
    inner = L[f"b{k - 1}"].to_numpy() if k > 1 else np.zeros(len(L))
    c = h1["c"].to_numpy()
    up = anchor * (1 + band)
    dn = anchor * (1 - band)
    sig = np.where(c > up, -1, np.where(c < dn, 1, 0))
    if p["gate"]:
        sig = np.where(np.abs(L["trend"].to_numpy()) > 1.0, 0, sig)  # skip fades in strong 20-day trends
    sig = np.where(np.isfinite(anchor) & np.isfinite(band), sig, 0)
    tgt_frac = 0.0 if p["target"] == "ground" else inner
    target_px = np.where(sig < 0, anchor * (1 + tgt_frac), anchor * (1 - tgt_frac))
    stop_bps = np.abs(anchor * (1 + nxt) / c - 1) * 1e4  # distance from current price to the next band
    stop_bps = np.where(sig > 0, np.abs(c / (anchor * (1 - nxt)) - 1) * 1e4, stop_bps)
    tp_dummy = np.full(len(c), 1e4)
    return lab.simulate_brackets(h1["o"].to_numpy(), h1["h"].to_numpy(), h1["l"].to_numpy(), c, h1.index.asi8,
                                 sig, tp_dummy, np.maximum(stop_bps, 10.0), max_hold=72, loser_stop=48,
                                 costs=costs, target_px=target_px)
