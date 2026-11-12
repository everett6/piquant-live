"""Round-14 (PREREG14): drawdown-first trend core — intraday vol stop, Grossman-Zhou cushion sizing, ensemble."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab

LEV_MAX = 2.0
ALPHA = 0.70
M_GZ = LEV_MAX / (1 - ALPHA)


def stop_fraction(daily: pd.DataFrame) -> np.ndarray:
    """s_t = clip(2 * sigma_30(t), 3%, 12%) from closes through t (NaN during warm-up)."""
    sig = daily["c"].pct_change(fill_method=None).rolling(30, min_periods=30).std().to_numpy()
    return np.clip(2.0 * sig, 0.03, 0.12)


def first_touch(min1: pd.DataFrame, days: pd.DatetimeIndex, level: np.ndarray) -> np.ndarray:
    """For each day i, the fill of a stop at level[i] during that UTC day: min(open, level) at the first 1m bar
    with low <= level. NaN where untouched or level is NaN."""
    out = np.full(len(days), np.nan)
    day_of = min1.index.floor("D")
    pos = days.get_indexer(day_of)
    ok = pos >= 0
    pos, lo, op = pos[ok], min1["l"].to_numpy()[ok], min1["o"].to_numpy()[ok]
    lvl = level[pos]
    hit = lo <= lvl  # NaN levels compare False
    if not hit.any():
        return out
    hp = pos[hit]
    first = np.unique(hp, return_index=True)[1]
    idx = np.flatnonzero(hit)[first]
    out[pos[idx]] = np.minimum(op[idx], lvl[idx])
    return out


def p1_signal(daily: pd.DataFrame) -> np.ndarray:
    c = daily["c"]
    sma = c.rolling(50, min_periods=50).mean()
    return np.where((c > sma).to_numpy() & sma.notna().to_numpy(), 1.0, 0.0)


def ensemble_signal(daily: pd.DataFrame) -> np.ndarray:
    """Fraction of SMA20/50/100/200 the close is above, over the SMAs with history; 0 until SMA50 exists."""
    c = daily["c"]
    above, have = np.zeros(len(c)), np.zeros(len(c))
    for n in (20, 50, 100, 200):
        s = c.rolling(n, min_periods=n).mean()
        v = s.notna().to_numpy()
        have += v
        above += v & (c > s).to_numpy()
    sig = np.divide(above, have, out=np.zeros(len(c)), where=have > 0)
    sig[c.rolling(50, min_periods=50).mean().isna().to_numpy()] = 0.0
    return sig


def gz_cap(w: float, m: float) -> float:
    """Grossman-Zhou leverage cap: min(LEV_MAX, M_GZ * (1 - ALPHA * m / w)), floored at 0."""
    if w <= 0:
        return 0.0
    return float(min(LEV_MAX, max(0.0, M_GZ * (1.0 - ALPHA * m / w))))


def simulate(sleeves: list[dict], costs: lab.Costs, gz: bool) -> pd.DataFrame:
    """Daily portfolio of equal sleeves. Each sleeve dict: daily (OHLC df on the common index, NaN before data),
    signal (array in [0,1]), stopfill (array: fill price on day i if the stop set at close i-1 is touched).
    Returns a frame with portfolio return r, and per-sleeve held leverage and stop flags."""
    idx = sleeves[0]["daily"].index
    n, k = len(idx), len(sleeves)
    cpt = (costs.taker_bps + costs.slip_bps) * 1e-4
    fund = costs.funding_bps_h * 1e-4 * 24
    held = np.zeros(k)          # leverage held over the current day (set at the previous close)
    w, peak = 1.0, 1.0
    r_out = np.zeros(n)
    lev_out = np.zeros((n, k))
    stop_out = np.zeros((n, k), dtype=bool)
    closes = [s["daily"]["c"].to_numpy() for s in sleeves]
    for i in range(n):
        r_sleeve = np.zeros(k)
        end_pos = held.copy()
        for j, s in enumerate(sleeves):
            c = closes[j]
            if i == 0 or np.isnan(c[i]) or np.isnan(c[i - 1]):
                end_pos[j] = 0.0
                continue
            L = held[j]
            if L > 0:
                f = s["stopfill"][i]
                if not np.isnan(f):
                    r_sleeve[j] = L * (f / c[i - 1] - 1) - L * cpt - L * fund
                    end_pos[j] = 0.0
                    stop_out[i, j] = True
                else:
                    r_sleeve[j] = L * (c[i] / c[i - 1] - 1) - L * fund
        r = r_sleeve.mean()
        w_new = w * (1 + r)
        # decide at this close
        cap = gz_cap(w_new, max(peak, w_new)) if gz else LEV_MAX
        target = np.zeros(k)
        for j, s in enumerate(sleeves):
            if not np.isnan(closes[j][i]):
                target[j] = s["signal"][i] * cap
        rebalance = (np.abs(target - end_pos) * cpt).mean()
        r -= rebalance
        w = w * (1 + r)
        peak = max(peak, w)
        r_out[i] = r
        lev_out[i] = held
        held = target
    out = pd.DataFrame({"r": r_out}, index=idx)
    for j in range(k):
        out[f"lev{j}"] = lev_out[:, j]
        out[f"stop{j}"] = stop_out[:, j]
    return out


def build_sleeve(daily: pd.DataFrame, min1: pd.DataFrame, index: pd.DatetimeIndex, signal_fn) -> dict:
    """Align a coin to the common daily index and precompute its signal and stop fills."""
    d = daily.reindex(index)
    sig = np.nan_to_num(signal_fn(d), nan=0.0)
    s = stop_fraction(d)
    level = np.full(len(index), np.nan)
    level[1:] = d["c"].to_numpy()[:-1] * (1 - s[:-1])  # set at close i-1, live during day i
    return {"daily": d, "signal": sig, "stopfill": first_touch(min1, index, level)}


def drop_bad_prints(min1: pd.DataFrame) -> pd.DataFrame:
    """PREREG15 data rule: drop 1m bars with o == h == l == c below 50% of the prior daily close."""
    prev = lab.resample(min1, "1D")["c"].shift(1).reindex(min1.index.floor("D")).to_numpy()
    flat = (min1["o"] == min1["h"]) & (min1["h"] == min1["l"]) & (min1["l"] == min1["c"])
    bad = flat.to_numpy() & (min1["c"].to_numpy() < 0.5 * prev)
    return min1[~bad]
