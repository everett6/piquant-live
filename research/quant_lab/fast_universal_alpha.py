"""Fast Causal Universal Portfolio, Range-Volatility & SuperSmoother Alpha Module.

Implements strictly causal (t-1 -> t), vectorized NumPy primitives with zero
``pyarrow`` dependency at import time:

1. ``fast_range_vol``:
   Causal Garman-Klass (1980) and Parkinson (1980) extreme-value range-based
   volatility estimators (~5.2x to ~7.4x more statistically efficient than
   close-to-close variance under geometric Brownian motion).

2. ``eg_universal_weights``:
   Fast online Exponentiated-Gradient Universal Portfolio allocator
   (Cover 1991; Helmbold, Schapire, Singer & Warmuth 1998) with Dirichlet/uniform
   shrinkage and transaction-cost-aware no-trade rebalance buffering.

3. ``fast_supersmoother_signal``:
   Causal 2-pole Ehlers (2013) Butterworth SuperSmoother low-pass filter with
   2-bar Nyquist alias rejection, normalized velocity trend signal, and
   fast-to-slow range-volatility crash brake.

Academic References:
- Cover, T. M. (1991). "Universal Portfolios." *Mathematical Finance*, 1(1), 1-29.
- Helmbold, D. P., Schapire, R. E., Singer, Y., & Warmuth, M. K. (1998).
  "On-Line Portfolio Selection Using Multiplicative Updates." *Mathematical Finance*,
  8(4), 325-347.
- Parkinson, M. (1980). "The Extreme Value Method for Estimating the Variance of
  the Rate of Return." *The Journal of Business*, 53(1), 61-65.
- Garman, M. B., & Klass, M. J. (1980). "On the Estimation of Security Price
  Volatilities from Historical Data." *The Journal of Business*, 53(1), 67-78.
- Ehlers, J. F. (2013). *Cycle Analytics for Traders: Advanced Technical Trading
  Concepts*. Wiley Trading.
"""
from __future__ import annotations

import json
import math
import time
from typing import Any

import numpy as np


def _rolling_mean_1d(arr: np.ndarray, window: int, min_periods: int = 1) -> np.ndarray:
    """O(N) causal rolling mean over 1D float array using cumulative sums."""
    n = len(arr)
    out = np.full(n, np.nan, dtype=np.float64)
    if n == 0 or window <= 0:
        return out

    valid = np.isfinite(arr)
    clean = np.where(valid, arr, 0.0)
    csum = np.empty(n + 1, dtype=np.float64)
    csum[0] = 0.0
    np.cumsum(clean, out=csum[1:])

    ccnt = np.empty(n + 1, dtype=np.int64)
    ccnt[0] = 0
    np.cumsum(valid.astype(np.int64), out=ccnt[1:])

    idx = np.arange(n, dtype=np.int64)
    lo = np.maximum(0, idx - window + 1)
    sums = csum[idx + 1] - csum[lo]
    cnts = ccnt[idx + 1] - ccnt[lo]

    ok = cnts >= max(1, min_periods)
    out[ok] = sums[ok] / cnts[ok]
    return out


def fast_range_vol(
    open_px: np.ndarray,
    high_px: np.ndarray,
    low_px: np.ndarray,
    close_px: np.ndarray,
    window: int = 20,
    method: str = "garman_klass",
    annual_days: int = 365,
    min_vol: float = 1e-4,
    shift: int = 1,
) -> np.ndarray:
    """Strictly causal Parkinson / Garman-Klass range-based annualized volatility.

    Parameters
    ----------
    open_px, high_px, low_px, close_px : array-like
        1D OHLC price arrays of equal length ``N``.
    window : int
        Rolling lookback window in bars.
    method : {"garman_klass", "parkinson"}
        Range-based variance estimator.
    annual_days : int
        Annualization factor (365 for crypto, 252 for equities).
    min_vol : float
        Floor on annualized volatility.
    shift : int
        Causal lag in bars (default 1: output at index ``t`` uses OHLC up to ``t-1``).

    Returns
    -------
    np.ndarray
        Annualized volatility array of shape ``(N,)``.
    """
    o = np.asarray(open_px, dtype=np.float64)
    h = np.asarray(high_px, dtype=np.float64)
    l = np.asarray(low_px, dtype=np.float64)
    c = np.asarray(close_px, dtype=np.float64)
    n = len(c)
    if not (len(o) == len(h) == len(l) == n):
        raise ValueError("OHLC arrays must have identical length")
    if n == 0:
        return np.empty(0, dtype=np.float64)

    eps = 1e-12
    h_safe = np.maximum(h, eps)
    l_safe = np.maximum(np.minimum(l, h_safe), eps)
    o_safe = np.maximum(o, eps)
    c_safe = np.maximum(c, eps)

    log_hl = np.log(h_safe / l_safe)
    if method == "parkinson":
        daily_var = (log_hl * log_hl) / (4.0 * math.log(2.0))
    elif method == "garman_klass":
        log_co = np.log(c_safe / o_safe)
        daily_var = 0.5 * (log_hl * log_hl) - (2.0 * math.log(2.0) - 1.0) * (log_co * log_co)
        daily_var = np.maximum(daily_var, 0.0)
    else:
        raise ValueError(f"Unknown method: {method!r}. Expected 'garman_klass' or 'parkinson'.")

    invalid = ~(np.isfinite(o) & np.isfinite(h) & np.isfinite(l) & np.isfinite(c) & (o > 0) & (h > 0) & (l > 0) & (c > 0))
    daily_var[invalid] = np.nan

    min_p = max(2, min(window, max(2, window // 2)))
    mean_var = _rolling_mean_1d(daily_var, window=window, min_periods=min_p)
    vol = np.sqrt(np.maximum(mean_var, 0.0) * float(annual_days))
    vol = np.where(np.isfinite(vol), np.maximum(vol, min_vol), np.nan)

    if shift > 0:
        shifted = np.full(n, np.nan, dtype=np.float64)
        if shift < n:
            shifted[shift:] = vol[:-shift]
        return shifted
    return vol


def eg_universal_weights(
    returns: np.ndarray,
    eta: float = 0.15,
    shrinkage: float = 1e-3,
    rebal_buffer: float = 0.02,
    cost_rate: float = 3e-4,
    init_weights: np.ndarray | None = None,
) -> np.ndarray:
    """Fast online Exponentiated-Gradient Universal Portfolio allocator.

    Implements Helmbold et al. (1998) multiplicative updates with uniform
    shrinkage (to bound regret and prevent simplex boundary lock-in) and
    a transaction-cost-aware no-trade rebalance buffer.

    Strictly causal: ``weights[t]`` is computed solely from ``returns[:t]`` and
    applied to earn ``returns[t]``.

    Parameters
    ----------
    returns : np.ndarray
        2D array of shape ``(T, M)`` of simple asset returns.
    eta : float
        Exponentiated-gradient learning rate (> 0).
    shrinkage : float
        Uniform mixing coefficient in ``[0, 1)`` applied after each update.
    rebal_buffer : float
        Minimum L1/2 turnover threshold required to trigger a rebalance.
    cost_rate : float
        One-way transaction cost rate used to damp noisy gradient steps.
    init_weights : np.ndarray | None
        Optional initial portfolio weights on the simplex of length ``M``.

    Returns
    -------
    np.ndarray
        Strictly causal portfolio weights of shape ``(T, M)``.
    """
    r = np.asarray(returns, dtype=np.float64)
    if r.ndim != 2:
        raise ValueError(f"returns must be 2D (T, M), got shape {r.shape}")
    t_len, m = r.shape
    if t_len == 0 or m == 0:
        return np.empty((t_len, m), dtype=np.float64)

    weights = np.empty((t_len, m), dtype=np.float64)
    uniform = np.full(m, 1.0 / m, dtype=np.float64)

    if init_weights is None:
        w = uniform.copy()
    else:
        w = np.asarray(init_weights, dtype=np.float64).copy()
        w = np.maximum(w, 0.0)
        s = float(np.sum(w))
        w = w / s if s > 0.0 else uniform.copy()

    w_held = w.copy()
    # Clean returns to finite values
    r_clean = np.where(np.isfinite(r), r, 0.0)
    # Price relatives x_t = 1 + r_t, floored to avoid division by zero
    x_all = np.maximum(1.0 + r_clean, 1e-6)

    for t in range(t_len):
        weights[t] = w_held
        x_t = x_all[t]
        port_rel = float(np.dot(w, x_t))
        if port_rel <= 1e-9:
            port_rel = 1e-9

        # Exponentiated gradient update: w_{t+1, i} \propto w_{t, i} * exp(eta * x_{t,i} / (w_t^T x_t))
        # Dampen effective step size slightly by transaction cost drag
        grad = x_t / port_rel
        # Subtract max for numerical stability in exp
        log_mult = eta * (grad - np.max(grad))
        # Soft turnover penalty: shrink gradient when cost_rate is high
        if cost_rate > 0.0:
            log_mult *= max(0.25, 1.0 - 50.0 * cost_rate)

        w_cand = w * np.exp(log_mult)
        s = float(np.sum(w_cand))
        if s > 0.0 and np.isfinite(s):
            w = w_cand / s
        else:
            w = uniform.copy()

        if shrinkage > 0.0:
            w = (1.0 - shrinkage) * w + shrinkage * uniform

        # Transaction-cost-aware no-trade buffer on held weights
        half_l1 = 0.5 * float(np.sum(np.abs(w - w_held)))
        if half_l1 >= rebal_buffer:
            w_held = w.copy()

    return weights


def supersmoother_2pole(prices: np.ndarray, period: float = 10.0) -> np.ndarray:
    """Causal 2-pole Ehlers (2013) Butterworth SuperSmoother low-pass filter."""
    p = np.asarray(prices, dtype=np.float64)
    n = len(p)
    if n == 0:
        return np.empty(0, dtype=np.float64)

    period = max(2.0, float(period))
    arg = math.sqrt(2.0) * math.pi / period
    a1 = math.exp(-arg)
    b1 = 2.0 * a1 * math.cos(arg)
    c2 = b1
    c3 = -(a1 * a1)
    c1 = 1.0 - c2 - c3

    out = np.empty(n, dtype=np.float64)
    # Forward-fill any leading NaNs safely
    first_valid = p[0] if np.isfinite(p[0]) else 0.0
    out[0] = first_valid
    if n == 1:
        return out
    p1 = p[1] if np.isfinite(p[1]) else out[0]
    out[1] = p1

    prev_p = p1
    f1 = out[1]
    f2 = out[0]
    for i in range(2, n):
        cur_p = p[i]
        if not np.isfinite(cur_p):
            cur_p = prev_p
        val = c1 * 0.5 * (cur_p + prev_p) + c2 * f1 + c3 * f2
        out[i] = val
        prev_p = cur_p
        f2 = f1
        f1 = val
    return out


def fast_supersmoother_signal(
    open_px: np.ndarray,
    high_px: np.ndarray,
    low_px: np.ndarray,
    close_px: np.ndarray,
    fast_period: float = 10.0,
    slow_period: float = 30.0,
    vel_lag: int = 3,
    vol_fast_window: int = 5,
    vol_slow_window: int = 30,
    crash_ratio_thresh: float = 1.85,
    crash_floor: float = 0.25,
    annual_days: int = 365,
) -> dict[str, np.ndarray]:
    """Strictly causal (t-1 -> t) SuperSmoother velocity trend + range-vol crash brake.

    At bar ``t``, all outputs (``signal``, ``trend_pos``, ``crash_brake``,
    ``vol_ratio``, ``range_vol_slow``) depend strictly on OHLC bars ``<= t-1``.

    Returns
    -------
    dict[str, np.ndarray]
        - ``signal``: Combined position multiplier in ``[0.0, 1.0]`` (long-only)
        - ``trend_pos``: Denoised SuperSmoother velocity-filtered trend in ``[0.0, 1.0]``
        - ``crash_brake``: Range-vol ratio crash-brake multiplier in ``[crash_floor, 1.0]``
        - ``vol_ratio``: Causal ``vol_fast / vol_slow`` ratio
        - ``range_vol_slow``: Causal Garman-Klass slow volatility
    """
    c = np.asarray(close_px, dtype=np.float64)
    n = len(c)
    if n == 0:
        empty = np.empty(0, dtype=np.float64)
        return {
            "signal": empty,
            "trend_pos": empty,
            "crash_brake": empty,
            "vol_ratio": empty,
            "range_vol_slow": empty,
        }

    ss_fast = supersmoother_2pole(c, period=fast_period)
    ss_slow = supersmoother_2pole(c, period=slow_period)

    # Causal Garman-Klass fast & slow range volatility (already shifted by 1)
    rv_fast = fast_range_vol(
        open_px, high_px, low_px, close_px,
        window=vol_fast_window, method="garman_klass", annual_days=annual_days, shift=1,
    )
    rv_slow = fast_range_vol(
        open_px, high_px, low_px, close_px,
        window=vol_slow_window, method="garman_klass", annual_days=annual_days, shift=1,
    )

    # Shift SuperSmoother series by 1 bar so bar t uses only <= t-1
    ss_fast_lag1 = np.empty(n, dtype=np.float64)
    ss_slow_lag1 = np.empty(n, dtype=np.float64)
    ss_fast_lag1[0] = ss_fast[0]
    ss_slow_lag1[0] = ss_slow[0]
    if n > 1:
        ss_fast_lag1[1:] = ss_fast[:-1]
        ss_slow_lag1[1:] = ss_slow[:-1]

    # Causal velocity of fast SuperSmoother over vel_lag bars (using <= t-1)
    vel = np.zeros(n, dtype=np.float64)
    if vel_lag < n:
        denom = np.maximum(ss_fast_lag1[:-vel_lag], 1e-12)
        vel[vel_lag:] = (ss_fast_lag1[vel_lag:] - ss_fast_lag1[:-vel_lag]) / denom

    # Normalize velocity by daily slow vol
    daily_vol = np.where(np.isfinite(rv_slow), rv_slow / math.sqrt(float(annual_days)), 0.02)
    daily_vol = np.maximum(daily_vol, 1e-4)
    z_vel = vel / (daily_vol * math.sqrt(float(max(1, vel_lag))))

    # Dual-speed SuperSmoother spread + velocity filter -> smooth [0, 1] trend_pos
    spread = (ss_fast_lag1 - ss_slow_lag1) / np.maximum(ss_slow_lag1, 1e-12)
    z_spread = spread / daily_vol
    raw_score = 0.6 * np.tanh(0.8 * z_spread) + 0.4 * np.tanh(0.8 * z_vel)
    trend_pos = np.clip(0.5 + 0.5 * raw_score, 0.0, 1.0)
    # Warmup zeroing before slow_period
    warmup = min(n, int(max(vel_lag + 1, vol_slow_window)))
    trend_pos[:warmup] = 0.0

    # Range-volatility ratio crash brake (strictly causal because rv_fast & rv_slow have shift=1)
    # Lag slow vol by vol_fast_window so the denominator reflects pre-shock baseline regime
    rv_slow_base = np.empty(n, dtype=np.float64)
    lag = max(1, int(vol_fast_window))
    if lag < n:
        rv_slow_base[:lag] = rv_slow[:lag]
        rv_slow_base[lag:] = rv_slow[:-lag]
    else:
        rv_slow_base[:] = rv_slow

    vol_ratio = np.where(
        np.isfinite(rv_fast) & np.isfinite(rv_slow_base) & (rv_slow_base > 1e-6),
        rv_fast / rv_slow_base,
        1.0,
    )
    excess = np.maximum(0.0, vol_ratio - crash_ratio_thresh)
    crash_brake = np.clip(1.0 - excess / 1.0, crash_floor, 1.0)
    signal = trend_pos * crash_brake

    return {
        "signal": signal,
        "trend_pos": trend_pos,
        "crash_brake": crash_brake,
        "vol_ratio": vol_ratio,
        "range_vol_slow": rv_slow,
    }


def benchmark_vs_mft_lft_fund() -> dict[str, Any]:
    """Benchmark fast_universal_alpha against mft_lft_fund.py (2020-2026 & 2024-2026 OOS)."""
    import pandas as pd

    import mft_lft_fund as MLF
    import monthly_reset_audit as MRA
    import run_round52 as R52
    import strategies36 as S36
    import yearly_goal_loop as YG

    t0 = time.perf_counter()
    baseline = MLF.run_fund_backtest()
    t_base = time.perf_counter() - t0

    t1 = time.perf_counter()
    r_crypto, _ = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(["BTC"])
    mm = u_c["mm"]
    btc_df = u_c["daily"]["BTC"].reindex(r_crypto.index).ffill()

    u_eq = YG.load_equities_universe(tickers=YG.MACRO_EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_eq_tr = MLF.simulate_lft_mft_sleeve(pre_eq, top_k=3, rebal_buffer=0.03)
    r_eq = r_eq_tr.copy()
    r_eq.index = r_eq.index.normalize()
    r_eq = r_eq.groupby(level=0).sum().reindex(r_crypto.index.normalize()).fillna(0.0).set_axis(r_crypto.index)

    # 1. Online Exponentiated-Gradient Universal Portfolio blend of (Crypto, Equities)
    #    Applied to vol-normalized returns (risk-parity EG) with shrinkage toward 50/50
    ret_mat = np.column_stack([r_crypto.to_numpy(dtype=np.float64), r_eq.to_numpy(dtype=np.float64)])
    vol_c = np.maximum(
        pd.Series(ret_mat[:, 0]).rolling(20, min_periods=10).std().shift(1).fillna(0.015).to_numpy(dtype=np.float64),
        1e-4,
    )
    vol_e = np.maximum(
        pd.Series(ret_mat[:, 1]).rolling(20, min_periods=10).std().shift(1).fillna(0.008).to_numpy(dtype=np.float64),
        1e-4,
    )
    inv_v = np.column_stack([1.0 / vol_c, 1.0 / vol_e])
    inv_v = inv_v / np.sum(inv_v, axis=1, keepdims=True)
    # EG on vol-standardized returns tracks risk-adjusted outperformance
    eg_raw = eg_universal_weights(ret_mat * inv_v, eta=0.10, shrinkage=0.05, rebal_buffer=0.02, cost_rate=2.5e-4)
    eg_w = eg_raw * inv_v
    eg_w = eg_w / np.sum(eg_w, axis=1, keepdims=True)
    r_eg_blend = pd.Series(np.sum(eg_w * ret_mat, axis=1), index=r_crypto.index)

    # 2. Causal Garman-Klass range-vol + SuperSmoother crash brake on BTC regime
    ss_btc = fast_supersmoother_signal(
        btc_df["o"].to_numpy(dtype=np.float64),
        btc_df["h"].to_numpy(dtype=np.float64),
        btc_df["l"].to_numpy(dtype=np.float64),
        btc_df["c"].to_numpy(dtype=np.float64),
        fast_period=10.0,
        slow_period=30.0,
        vel_lag=3,
        vol_fast_window=5,
        vol_slow_window=30,
        crash_ratio_thresh=2.20,
        crash_floor=0.55,
        annual_days=365,
    )
    r_1x_fast = MRA.apply_vol_tox_circuit_breaker(r_eg_blend, mm, target_vol=0.0, max_lev=1.0)
    # Apply range-vol crash brake gently to 1x spot when BTC intraday range vol spikes
    brake = pd.Series(ss_btc["crash_brake"], index=r_crypto.index)
    r_1x_fast = r_1x_fast * (0.7 + 0.3 * brake)

    # 3. 1.8x borrow-adjusted mode using Garman-Klass vol targeting + SuperSmoother crash brake
    arr = r_eg_blend.to_numpy(dtype=np.float64)
    tox = mm["micro_tox_score"].reindex(r_eg_blend.index).ffill().fillna(0.3).to_numpy(dtype=np.float64)
    gk_vol = ss_btc["range_vol_slow"]
    brake_arr = ss_btc["crash_brake"]
    out_sc = np.zeros(len(arr), dtype=np.float64)
    eq, peak = 1.0, 1.0
    for t in range(len(arr)):
        scale = 1.0
        if t >= 20:
            v20 = float(np.std(arr[t - 20 : t], ddof=1) * math.sqrt(365.0))
            # Blend portfolio realized vol with causal Garman-Klass BTC range vol
            gk_t = float(gk_vol[t]) if np.isfinite(gk_vol[t]) else v20
            eff_vol = 0.85 * v20 + 0.15 * (0.35 * gk_t)
            scale = min(1.8, 0.16 / max(eff_vol, 0.04))
        scale *= float(brake_arr[t])
        if t >= 1 and tox[t - 1] > 0.72:
            scale *= 0.75
        dm = S36.soft_taper(1.0 - eq / peak, dd_start=0.06, dd_full=0.18, floor=0.35)
        lev = min(1.8, scale * dm)
        r = lev * arr[t] - max(0.0, lev - 1.0) * (0.055 / 365.0)
        out_sc[t] = r
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)
    r_sc_fast = pd.Series(out_sc, index=r_eg_blend.index)
    t_fast = time.perf_counter() - t1

    def _pack(s: pd.Series) -> dict[str, Any]:
        st = YG.evaluate_by_year(s)
        oos = YG.evaluate_by_year(s[s.index >= pd.Timestamp("2024-01-01", tz="UTC")])
        return {
            "cagr_pct": st["cagr_pct"],
            "max_dd_pct": st["max_dd_pct"],
            "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
            "sharpe": st["sharpe"],
            "y2022_pct": st["years"]["2022"]["ann_pct"],
            "y2026_pct": st["years"]["2026"]["ann_pct"],
            "oos_2024_2026_cagr_pct": oos["cagr_pct"],
            "oos_2024_2026_sharpe": oos["sharpe"],
        }

    return {
        "baseline_mft_lft_fund": baseline,
        "fast_universal_alpha": {
            "fast_1x_spot": _pack(r_1x_fast),
            "fast_1p8x_borrow_adj": _pack(r_sc_fast),
        },
        "runtime_sec": {
            "baseline": round(t_base, 3),
            "fast_universal": round(t_fast, 3),
        },
    }


if __name__ == "__main__":
    print(json.dumps(benchmark_vs_mft_lft_fund(), indent=2))
