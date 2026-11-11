"""Round-2 algorithms from PREREG2.md: Q1 (QHO tail coefficient as a vol feature), S5 (vol-managed BTC),
S6 (quantum-walk-inspired diffusion regime via variance ratio). Causal features only."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab
import strategies as S


# ---------------------------------------------------------------------------------------------------------
# Q1: anharmonic coefficient lambda vs next-7-day realised volatility
# ---------------------------------------------------------------------------------------------------------
def qho_lambda(daily_ret: np.ndarray, bins: int = 100) -> float:
    r = daily_ret[np.isfinite(daily_ret)]
    if len(r) < 15 or r.std() == 0:
        return np.nan
    hist, edges = np.histogram(r, bins=bins)
    q = hist / hist.sum()
    k = int(np.argmax(q))
    if k == 0:
        return np.nan
    dr = edges[1] - edges[0]
    r0 = (edges[k] + edges[k + 1]) / 2
    r1 = r0 - dr
    den = (r0 ** 4) * q[k] - (r1 ** 4) * q[k - 1]
    return abs(((r1 ** 2) * q[k - 1] - (r0 ** 2) * q[k]) / den) if den != 0 else np.nan


def q1_frame(df: pd.DataFrame) -> pd.DataFrame:
    d = lab.resample(df, "1D")
    ret = d["c"].pct_change()
    lam = [np.nan] * len(d)
    vals = ret.to_numpy()
    for i in range(61, len(d)):
        lam[i] = qho_lambda(vals[i - 60:i])
    f = pd.DataFrame(index=d.index)
    f["lam"] = lam
    f["rv30"] = ret.rolling(30).std().shift(0)  # uses returns up to day i (known at day i close)
    f["rv_next7"] = ret[::-1].rolling(7).std()[::-1].shift(-1)  # days i+1..i+7
    return f


def q1_regression(f: pd.DataFrame) -> dict:
    x = f[["lam", "rv30", "rv_next7"]].replace([np.inf, -np.inf], np.nan).dropna()
    x = x[(x["lam"] > 0) & (x["rv30"] > 0) & (x["rv_next7"] > 0)]
    if len(x) < 60:
        return {"n": len(x)}
    y = np.log(x["rv_next7"].to_numpy())
    base = np.column_stack([np.ones(len(x)), np.log(x["rv30"].to_numpy())])
    full = np.column_stack([base, np.log(x["lam"].to_numpy())])

    def fit(X):
        beta, *_ = np.linalg.lstsq(X, y, rcond=None)
        resid = y - X @ beta
        r2 = 1 - resid.var() / y.var()
        sigma2 = resid @ resid / (len(y) - X.shape[1])
        se = np.sqrt(np.diag(sigma2 * np.linalg.inv(X.T @ X)))
        return beta, r2, se
    _, r2b, _ = fit(base)
    beta, r2f, se = fit(full)
    return {"n": int(len(x)), "r2_base": float(r2b), "r2_with_lambda": float(r2f), "r2_gain": float(r2f - r2b),
            "lambda_coef": float(beta[2]), "lambda_t": float(beta[2] / se[2])}


# ---------------------------------------------------------------------------------------------------------
# S5: volatility-managed long BTC on daily bars
# ---------------------------------------------------------------------------------------------------------
S5_GRID = [dict(target=t, lookback=lb) for t in (0.30, 0.50, 0.70) for lb in (20, 60)]


def s5_positions(d: pd.DataFrame, p: dict, cap: float) -> np.ndarray:
    rv = d["c"].pct_change().rolling(p["lookback"]).std() * np.sqrt(365)
    w = (p["target"] / rv).clip(upper=cap).fillna(0.0)
    return w.to_numpy()


# ---------------------------------------------------------------------------------------------------------
# S6: quantum-walk-inspired diffusion regime (variance ratio) on 1h bars
# ---------------------------------------------------------------------------------------------------------
S6_GRID = [dict(delta=dl, window_d=w, k=k) for dl in (0.1, 0.2) for w in (14, 30) for k in (12, 24)]


def s6_positions(h1: pd.DataFrame, p: dict) -> np.ndarray:
    lr = np.log(h1["c"]).diff()
    k, W = p["k"], p["window_d"] * 24
    rk = np.log(h1["c"]).diff(k)
    vr = rk.rolling(W, min_periods=W // 2).var() / (k * lr.rolling(W, min_periods=W // 2).var())
    sgn = np.sign(rk)
    pos = np.where(vr > 1 + p["delta"], sgn, np.where(vr < 1 - p["delta"], -sgn, 0.0))
    return np.nan_to_num(pos)
