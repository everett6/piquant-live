"""Round-35 (PREREG35): three sizing rules in quantum-mechanics notation. QC sizes a coin by whether the other coin
is also above its SMA50; QD ramps the size with the age of the trend as 1 - exp(-n / tau); QE halves leverage while
the von Neumann entropy of the cross-coin correlation matrix is low. Pure functions; nothing reads data from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies5 as S5

SMA_N, LEV, TAU = 50, 2.0, 5.0
ENT_WIN, ENT_MIN_COINS, ENT_Q, ENT_LOOK, ENT_MIN_OBS = 60, 8, 0.20, 365, 350


def entangled_positions(daily_i: pd.DataFrame, daily_j: pd.DataFrame, lev: float = LEV, n: int = SMA_N) -> np.ndarray:
    """QC: lev * a_i * (1 + a_j) / 2 on coin i's index. Where coin j has no SMA(n) yet the factor is 1 (P1)."""
    a_i = S5.m1_positions(daily_i, n, 1.0)
    cj = daily_j["c"]
    sma_j = cj.rolling(n, min_periods=n).mean()
    a_j = (cj > sma_j).astype(float).where(sma_j.notna()).reindex(daily_i.index)
    factor = ((1.0 + a_j) / 2.0).fillna(1.0).to_numpy()
    return lev * a_i * factor


def trend_age(daily: pd.DataFrame, n: int = SMA_N) -> np.ndarray:
    """Consecutive closes above SMA(n), today included; 0 when not above."""
    on = S5.m1_positions(daily, n, 1.0) > 0
    age = np.zeros(len(on))
    run = 0
    for i, x in enumerate(on):
        run = run + 1 if x else 0
        age[i] = run
    return age


def coherence_positions(daily: pd.DataFrame, lev: float = LEV, n: int = SMA_N, tau: float = TAU) -> np.ndarray:
    """QD: lev * (1 - exp(-age / tau))."""
    return lev * (1.0 - np.exp(-trend_age(daily, n) / tau))


def market_entropy(closes: pd.DataFrame, win: int = ENT_WIN, min_coins: int = ENT_MIN_COINS) -> pd.Series:
    """Normalised von Neumann entropy of rho = C / N, C the correlation matrix of the last `win` daily returns of the
    coins that have all of them. NaN with fewer than min_coins such coins. Uses data up to each close only."""
    r = closes.pct_change()
    x = r.to_numpy(dtype=float)
    out = np.full(len(r), np.nan)
    for t in range(win, len(r)):
        w = x[t - win + 1:t + 1]
        keep = np.isfinite(w).all(axis=0) & (w.std(axis=0) > 0)
        k = int(keep.sum())
        if k < min_coins:
            continue
        lam = np.linalg.eigvalsh(np.corrcoef(w[:, keep], rowvar=False)) / k
        lam = lam[lam > 1e-12]
        out[t] = float(-(lam * np.log(lam)).sum() / np.log(k))
    return pd.Series(out, index=closes.index)


def gate_low(sig: pd.Series, q: float = ENT_Q, look: int = ENT_LOOK, min_obs: int = ENT_MIN_OBS) -> np.ndarray:
    """On while the signal is below the q-quantile of its previous `look` days (today not included)."""
    thr = sig.rolling(look, min_periods=min_obs).quantile(q).shift(1)
    return (sig < thr).fillna(False).to_numpy(dtype=bool)


def gate_defined(sig: pd.Series, look: int = ENT_LOOK, min_obs: int = ENT_MIN_OBS) -> pd.Series:
    """True on days where gate_low has both a signal and a threshold."""
    return sig.notna() & sig.rolling(look, min_periods=min_obs).quantile(0.5).shift(1).notna()
