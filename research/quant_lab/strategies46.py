"""
Strategies 46: Alpha-Decay Detection and Self-Improving Allocation Loop (Pure Functions, Zero Disk I/O).
"""

import numpy as np
import pandas as pd


def rolling_alpha_decay_throttle(
    sleeve_ret: pd.Series, lookback: int = 60, floor_mult: float = 0.35
) -> pd.Series:
    """Strictly causal rolling alpha decay throttle.

    Computes rolling lookback-day mean net return mu and std sd up to t
    (requiring at least 20 bars, else 1.0). Annualized rolling Sharpe
    sr = (mu / max(sd, 1e-6)) * sqrt(365).
    - If sr >= 0.5: multiplier = 1.0 (HEALTHY)
    - If sr <= -0.5: multiplier = floor_mult (DECAYING -> parks 65% in DTB3)
    - Linearly interpolates between floor_mult and 1.0 for sr in (-0.5, 0.5).
    Never uses future bars (t+1..).
    """
    s = sleeve_ret.fillna(0.0)
    # Causal rolling mean and std
    # We want rolling window of size `lookback`, min_periods=20
    rolling_mean = s.rolling(window=lookback, min_periods=20).mean()
    rolling_std = s.rolling(window=lookback, min_periods=20).std()

    # Annualized Sharpe with sqrt(365)
    # If not enough periods (<20), rolling_mean or rolling_std is NaN -> multiplier = 1.0
    sr = (rolling_mean / np.maximum(rolling_std, 1e-6)) * np.sqrt(365.0)

    # Calculate multiplier
    # sr >= 0.5 -> 1.0
    # sr <= -0.5 -> floor_mult
    # else linear interpolation
    def compute_mult(val):
        if np.isnan(val):
            return 1.0
        if val >= 0.5:
            return 1.0
        if val <= -0.5:
            return floor_mult
        # linear interpolation between -0.5 and 0.5
        # range span is 1.0. val + 0.5 is in (0, 1)
        frac = (val - (-0.5)) / (0.5 - (-0.5))
        return floor_mult + frac * (1.0 - floor_mult)

    mults = sr.apply(compute_mult)
    # Ensure any initial NaNs where min_periods not met are 1.0
    mults = mults.fillna(1.0)
    return mults


def self_improving_exp_grad_weights(
    sleeve_rets: pd.DataFrame,
    eta: float = 0.15,
    min_carry: float = 0.25,
    max_trend: float = 0.70,
    lookback: int = 45,
) -> pd.DataFrame:
    """Strictly causal Multiplicative-Weights / Exponential-Gradient self-improving loop

    across columns ["qd", "r3", "carry", "dtb3"]:
    - Initializes w = [0.35, 0.25, 0.30, 0.10] at t = 0.
    - At each close t >= 1, observes realized net return vector r_t = sleeve_rets.iloc[t]
      (already net of fees/funding!), updates unnormalized scores using EWMA-smoothed net return & Sharpe-normalized step:
      step_i = clip(r_t[i] / max(rolling_std_i, 0.005), -3.0, 3.0)
      (computed using rolling lookback std up to t),
      w_raw = w_prev * exp(eta * step).
    - Projects w_raw onto the risk-safe simplex:
      w_carry >= min_carry, w_qd + w_r3 <= max_trend, sum(w) == 1.0, w >= 0.0.
    - Note: w.iloc[t] is decided at close t and held over t+1 (strictly causal!).
    """
    cols = ["qd", "r3", "carry", "dtb3"]
    assert all(c in sleeve_rets.columns for c in cols), f"Missing columns in {sleeve_rets.columns}"

    n = len(sleeve_rets)
    weights = np.zeros((n, 4))

    # Initial weights at t = 0
    w = np.array([0.35, 0.25, 0.30, 0.10])
    weights[0] = w

    # Rolling std up to t (min_periods=10 for stability)
    rolling_std = sleeve_rets[cols].rolling(window=lookback, min_periods=10).std().fillna(0.01)

    for t in range(1, n):
        r_t = sleeve_rets.iloc[t][cols].values
        std_t = rolling_std.iloc[t][cols].values
        std_t = np.maximum(std_t, 0.005)

        step = np.clip(r_t / std_t, -3.0, 3.0)

        # Multiplicative weight update
        w_raw = w * np.exp(eta * step)

        # Projection onto simplex with constraints:
        # 1. w >= 0, sum(w) = 1
        # 2. w_carry >= min_carry (index 2)
        # 3. w_qd + w_r3 <= max_trend (indices 0 and 1)
        w = project_simplex(w_raw, min_carry=min_carry, max_trend=max_trend)

        weights[t] = w

    return pd.DataFrame(weights, index=sleeve_rets.index, columns=cols)


def project_simplex(w_raw: np.ndarray, min_carry: float = 0.25, max_trend: float = 0.70) -> np.ndarray:
    """Projects raw weights onto the constrained risk-safe simplex:

    - sum(w) == 1.0
    - w >= 0.0
    - w_carry (index 2) >= min_carry
    - w_qd + w_r3 (indices 0, 1) <= max_trend
    """
    w = np.maximum(w_raw, 0.0)
    s = np.sum(w)
    if s > 0:
        w = w / s
    else:
        w = np.array([0.35, 0.25, 0.30, 0.10])

    # Enforce constraints iteratively or via clipping/rescaling
    # Constraint 2: carry >= min_carry
    if w[2] < min_carry:
        deficit = min_carry - w[2]
        w[2] = min_carry
        # Take deficit proportionally from qd, r3, dtb3
        other_sum = w[0] + w[1] + w[3]
        if other_sum > 0:
            w[0] -= deficit * (w[0] / other_sum)
            w[1] -= deficit * (w[1] / other_sum)
            w[3] -= deficit * (w[3] / other_sum)

    # Constraint 3: qd + r3 <= max_trend
    trend_sum = w[0] + w[1]
    if trend_sum > max_trend:
        excess = trend_sum - max_trend
        # Reduce qd and r3 proportionally
        if trend_sum > 0:
            w[0] -= excess * (w[0] / trend_sum)
            w[1] -= excess * (w[1] / trend_sum)
        # Add excess to carry and dtb3 proportionally
        cd_sum = w[2] + w[3]
        if cd_sum > 0:
            w[2] += excess * (w[2] / cd_sum)
            w[3] += excess * (w[3] / cd_sum)
        else:
            w[2] += excess * 0.75
            w[3] += excess * 0.25

    # Final normalization & clipping to ensure strictly valid probability vector
    w = np.maximum(w, 0.0)
    s = np.sum(w)
    if s > 0:
        w = w / s
    else:
        w = np.array([0.35, 0.25, 0.30, 0.10])

    return w
