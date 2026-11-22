"""Causal Yang-Zhang Volatility, 2-State Kalman Velocity & AdaHedge Online Portfolio Module.

Implements strictly causal (``t-1 -> t``), vectorized NumPy primitives with zero
``pyarrow`` dependency at import time:

1. ``yang_zhang_vol``:
   Causal drift-independent OHLC volatility estimator (Yang & Zhang 2000;
   Rogers & Satchell 1991) combining overnight close-to-open variance,
   open-to-close variance, and intraday Rogers-Satchell range variance with
   minimum-variance optimal weight ``k = 0.34 / (1.34 + (n + 1) / (n - 1))``,
   plus a clean close-to-close fallback when OHLC bars are missing or invalid.

2. ``kalman_trend_velocity``:
   Causal 2-state ``[log_price, velocity]`` Local Linear Trend Kalman filter
   (Kalman 1960; Harvey 1990) returning ``level``, ``velocity``,
   ``vel_zscore``, and ``innovation_z`` shifted by 1 bar for strict execution
   causality.

3. ``adahedge_weights``:
   Causal parameter-free AdaHedge online portfolio allocator
   (de Rooij, van Erven, Grunwald & Koolen 2014) with automatic mixability-gap
   learning rate ``eta_t = ln(K) / Delta_{t-1}``, overflow-safe logsumexp
   potential updates, causal risk-parity standardization, exact bounded-simplex
   projection onto ``[min_w, max_w]``, and a no-trade rebalance buffer.

Academic References:
- Yang, D., & Zhang, Q. (2000). "Drift-Independent Volatility Estimation Based
  on High, Low, Open, and Close Prices." *The Journal of Business*, 73(3),
  477-492. https://doi.org/10.1086/209650
- Rogers, L. C. G., & Satchell, S. E. (1991). "Estimating Variance From High,
  Low and Closing Prices." *The Annals of Applied Probability*, 1(4), 504-512.
- Kalman, R. E. (1960). "A New Approach to Linear Filtering and Prediction
  Problems." *Transactions of the ASME - Journal of Basic Engineering*,
  82(Series D, No. 1), 35-45.
- Harvey, A. C. (1990). *Forecasting, Structural Time Series Models and the
  Kalman Filter*. Cambridge University Press.
- de Rooij, S., van Erven, T., Grunwald, P. D., & Koolen, W. M. (2014).
  "Follow the Leader If You Can, Hedge If You Must." *Journal of Machine
  Learning Research (JMLR)*, 15(37), 1281-1316.
"""
from __future__ import annotations

import json
import math
import os
import time
from typing import Any

import numpy as np
from numpy.lib.stride_tricks import sliding_window_view

HERE = os.path.dirname(os.path.abspath(__file__))


def _rolling_sample_stats(
    arr: np.ndarray,
    window: int,
    min_periods: int,
) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Compute strictly causal rolling mean, sample variance (ddof=1), and count.

    Uses chunked ``sliding_window_view`` to avoid prefix-sum cancellation from
    distant historical outliers while preserving O(N) vectorized performance.
    """
    n = len(arr)
    mean_out = np.full(n, np.nan, dtype=np.float64)
    var_out = np.full(n, np.nan, dtype=np.float64)
    cnt_out = np.zeros(n, dtype=np.int64)
    if n == 0 or window <= 0:
        return mean_out, var_out, cnt_out

    valid = np.isfinite(arr)
    min_p = max(2, int(min_periods))

    # Warmup slice (at most window - 1 elements)
    warm_end = min(n, window - 1)
    for t in range(min_p - 1, warm_end):
        seg_v = valid[: t + 1]
        c = int(seg_v.sum())
        cnt_out[t] = c
        if c >= min_p:
            vals = arr[: t + 1][seg_v]
            m = float(vals.mean())
            diff = vals - m
            mean_out[t] = m
            var_out[t] = float(np.dot(diff, diff) / float(c - 1))

    if n < window:
        return mean_out, var_out, cnt_out

    # Chunked sliding_window_view for t >= window - 1 (caps peak RAM <= ~32 MB)
    chunk_rows = max(1, 4_000_000 // max(1, window))
    for start in range(window - 1, n, chunk_rows):
        stop = min(n, start + chunk_rows)
        lo = start - (window - 1)
        x_slice = arr[lo:stop]
        v_slice = valid[lo:stop]

        if bool(np.all(v_slice)):
            sw = sliding_window_view(x_slice, window)
            m = sw.mean(axis=1)
            diff = sw - m[:, None]
            v = (diff * diff).sum(axis=1) / float(window - 1)
            mean_out[start:stop] = m
            var_out[start:stop] = v
            cnt_out[start:stop] = window
        else:
            x_zero = np.where(v_slice, x_slice, 0.0)
            sw_x = sliding_window_view(x_zero, window)
            sw_v = sliding_window_view(v_slice, window)
            c = sw_v.sum(axis=1, dtype=np.int64)
            cnt_out[start:stop] = c

            sum_x = sw_x.sum(axis=1)
            m = np.zeros(len(c), dtype=np.float64)
            np.divide(sum_x, c, out=m, where=c >= 1)

            diff = np.where(sw_v, sw_x - m[:, None], 0.0)
            ss = (diff * diff).sum(axis=1)
            v = np.full(len(c), np.nan, dtype=np.float64)
            ok = c >= min_p
            np.divide(ss, c - 1.0, out=v, where=ok)

            mean_out[start:stop] = np.where(ok, m, np.nan)
            var_out[start:stop] = v

    return mean_out, var_out, cnt_out


def yang_zhang_vol(
    open_px: Any,
    high_px: Any,
    low_px: Any,
    close_px: Any,
    window: int = 20,
    annual_days: int = 252,
    shift: int = 1,
    min_vol: float = 1e-4,
) -> np.ndarray:
    """Strictly causal Yang-Zhang (2000) drift-independent OHLC volatility.

    Combines overnight variance (``ln(O_t / C_{t-1})``), open-to-close variance
    (``ln(C_t / O_t)``), and Rogers-Satchell (1991) intraday range variance via
    the minimum-variance optimal weight ``k = 0.34 / (1.34 + (n + 1) / (n - 1))``.
    Falls back cleanly to close-to-close log-return volatility whenever OHLC
    inputs are missing, ``None``, non-positive, or malformed (``H < L``).
    """
    if window < 2:
        raise ValueError(f"window must be >= 2 for Yang-Zhang volatility, got {window}")
    if shift < 0:
        raise ValueError(f"shift must be >= 0 for causality, got {shift}")

    c = np.asarray(close_px, dtype=np.float64)
    n = len(c)
    if n == 0:
        return np.empty(0, dtype=np.float64)

    valid_c = np.isfinite(c) & (c > 0.0)
    r_cc = np.full(n, np.nan, dtype=np.float64)
    if n > 1:
        v_cc = valid_c[1:] & valid_c[:-1]
        idx_cc = np.where(v_cc)[0] + 1
        r_cc[idx_cc] = np.log(c[idx_cc] / c[idx_cc - 1])

    min_p = max(2, min(window, max(2, window // 2)))
    _, cc_var, _ = _rolling_sample_stats(r_cc, window=window, min_periods=min_p)

    has_ohl = (open_px is not None) and (high_px is not None) and (low_px is not None)
    if has_ohl:
        o = np.asarray(open_px, dtype=np.float64)
        h = np.asarray(high_px, dtype=np.float64)
        l = np.asarray(low_px, dtype=np.float64)
        if not (len(o) == len(h) == len(l) == n):
            raise ValueError("OHLC arrays must have identical length")

        valid_ohl = (
            valid_c
            & np.isfinite(o)
            & np.isfinite(h)
            & np.isfinite(l)
            & (o > 0.0)
            & (h > 0.0)
            & (l > 0.0)
            & (h >= l)
        )
        valid_yz = np.zeros(n, dtype=bool)
        if n > 1:
            valid_yz[1:] = valid_ohl[1:] & valid_c[:-1]

        o_ret = np.full(n, np.nan, dtype=np.float64)
        c_ret = np.full(n, np.nan, dtype=np.float64)
        rs_val = np.full(n, np.nan, dtype=np.float64)

        idx_yz = np.where(valid_yz)[0]
        if len(idx_yz) > 0:
            o_v = o[idx_yz]
            c_v = c[idx_yz]
            c_prev = c[idx_yz - 1]
            # Clamp intraday extremes around O/C on valid bars so minor tick rounding cannot invert signs
            h_eff = np.maximum(h[idx_yz], np.maximum(o_v, c_v))
            l_eff = np.minimum(l[idx_yz], np.minimum(o_v, c_v))

            o_r = np.log(o_v / c_prev)
            c_r = np.log(c_v / o_v)
            u = np.log(h_eff / o_v)
            d = np.log(l_eff / o_v)

            o_ret[idx_yz] = o_r
            c_ret[idx_yz] = c_r
            rs_val[idx_yz] = np.maximum(u * (u - c_r) + d * (d - c_r), 0.0)

        _, sigma_o2, cnt_yz = _rolling_sample_stats(o_ret, window=window, min_periods=min_p)
        _, sigma_c2, _ = _rolling_sample_stats(c_ret, window=window, min_periods=min_p)
        sigma_rs2, _, _ = _rolling_sample_stats(rs_val, window=window, min_periods=min_p)

        k = np.full(n, np.nan, dtype=np.float64)
        ok_k = cnt_yz >= min_p
        cnt_f = cnt_yz[ok_k].astype(np.float64)
        k[ok_k] = 0.34 / (1.34 + (cnt_f + 1.0) / (cnt_f - 1.0))

        yz_var = sigma_o2 + k * sigma_c2 + (1.0 - k) * sigma_rs2
        daily_var = np.where(np.isfinite(yz_var), yz_var, cc_var)
    else:
        daily_var = cc_var

    vol = np.full(n, np.nan, dtype=np.float64)
    ok_v = np.isfinite(daily_var)
    vol[ok_v] = np.maximum(np.sqrt(np.maximum(daily_var[ok_v], 0.0) * float(annual_days)), min_vol)

    if shift > 0:
        shifted = np.full(n, np.nan, dtype=np.float64)
        if shift < n:
            shifted[shift:] = vol[:-shift]
        return shifted
    return vol


def kalman_trend_velocity(
    close_px: Any,
    q_pos: float = 1e-4,
    q_vel: float = 1e-5,
    r_obs: float = 1e-3,
    shift: int = 1,
) -> dict[str, np.ndarray]:
    """Strictly causal 2-state ``[log_price, velocity]`` Kalman trend filter.

    Implements the Harvey (1990) Local Linear Trend state-space model on
    ``y_t = ln(close_px[t])`` with transition ``F = [[1, 1], [0, 1]]``,
    process covariance ``Q = diag(q_pos, q_vel)``, and observation variance
    ``r_obs``. Missing/non-positive prices advance via exact prediction-only
    recursions. All outputs are shifted by ``shift`` (default 1) so bar ``t``
    depends strictly on observations ``<= t - 1``.
    """
    if shift < 0:
        raise ValueError(f"shift must be >= 0 for causality, got {shift}")
    if r_obs <= 0.0:
        raise ValueError(f"r_obs must be > 0, got {r_obs}")

    c = np.asarray(close_px, dtype=np.float64)
    n = len(c)
    if n == 0:
        empty = np.empty(0, dtype=np.float64)
        return {"level": empty, "velocity": empty, "vel_zscore": empty, "innovation_z": empty}

    valid = np.isfinite(c) & (c > 0.0)
    level_raw = np.full(n, np.nan, dtype=np.float64)
    vel_raw = np.zeros(n, dtype=np.float64)
    vel_z_raw = np.zeros(n, dtype=np.float64)
    innov_z_raw = np.zeros(n, dtype=np.float64)

    valid_idx = np.where(valid)[0]
    if len(valid_idx) > 0:
        t0 = int(valid_idx[0])
        m = math.log(float(c[t0]))
        v = 0.0
        p00 = float(r_obs)
        p01 = 0.0
        p11 = max(10.0 * float(q_vel), 1e-4)

        level_raw[t0] = m
        vel_raw[t0] = 0.0
        vel_z_raw[t0] = 0.0
        innov_z_raw[t0] = 0.0

        qp = float(q_pos)
        qv = float(q_vel)
        ro = float(r_obs)

        for t in range(t0 + 1, n):
            # Time update (prediction)
            m_p = m + v
            v_p = v
            p00_p = p00 + 2.0 * p01 + p11 + qp
            p01_p = p01 + p11
            p11_p = p11 + qv

            if valid[t]:
                nu = math.log(float(c[t])) - m_p
                s = max(p00_p + ro, 1e-18)
                k0 = p00_p / s
                k1 = p01_p / s

                m = m_p + k0 * nu
                v = v_p + k1 * nu
                p00 = max((1.0 - k0) * p00_p, 1e-18)
                p01 = (1.0 - k0) * p01_p
                p11 = max(p11_p - k1 * p01_p, 1e-18)

                innov_z_raw[t] = nu / math.sqrt(s)
            else:
                m, v = m_p, v_p
                p00, p01, p11 = p00_p, p01_p, p11_p
                innov_z_raw[t] = 0.0

            level_raw[t] = m
            vel_raw[t] = v
            vel_z_raw[t] = v / math.sqrt(p11)

    if shift == 0:
        return {
            "level": level_raw,
            "velocity": vel_raw,
            "vel_zscore": vel_z_raw,
            "innovation_z": innov_z_raw,
        }

    level_out = np.full(n, np.nan, dtype=np.float64)
    vel_out = np.zeros(n, dtype=np.float64)
    vel_z_out = np.zeros(n, dtype=np.float64)
    innov_z_out = np.zeros(n, dtype=np.float64)
    if shift < n:
        level_out[shift:] = level_raw[:-shift]
        vel_out[shift:] = vel_raw[:-shift]
        vel_z_out[shift:] = vel_z_raw[:-shift]
        innov_z_out[shift:] = innov_z_raw[:-shift]

    return {
        "level": level_out,
        "velocity": vel_out,
        "vel_zscore": vel_z_out,
        "innovation_z": innov_z_out,
    }


def _project_bounded_simplex(w: np.ndarray, min_w: float, max_w: float) -> np.ndarray:
    """Euclidean projection onto the box-constrained simplex {sum(x)=1, min_w<=x<=max_w}."""
    k = len(w)
    if k == 1:
        return np.array([1.0], dtype=np.float64)
    if k == 2:
        x0 = 0.5 * (float(w[0]) - float(w[1]) + 1.0)
        lo2 = max(min_w, 1.0 - max_w)
        hi2 = min(max_w, 1.0 - min_w)
        if x0 < lo2:
            x0 = lo2
        elif x0 > hi2:
            x0 = hi2
        return np.array([x0, 1.0 - x0], dtype=np.float64)

    if w.min() >= min_w and w.max() <= max_w and abs(float(w.sum()) - 1.0) <= 1e-12:
        return w.copy()

    # Active-set water-filling solves KKT equations in <= K iterations
    tau = (float(w.sum()) - 1.0) / float(k)
    for _ in range(k + 2):
        x = w - tau
        lower = x <= min_w
        upper = x >= max_w
        free = ~(lower | upper)
        n_free = int(free.sum())
        if n_free == 0:
            break
        tau_new = (
            float(w[free].sum())
            + float(int(upper.sum())) * max_w
            + float(int(lower.sum())) * min_w
            - 1.0
        ) / float(n_free)
        if abs(tau_new - tau) <= 1e-13:
            res = np.clip(w - tau_new, min_w, max_w)
            if abs(float(res.sum()) - 1.0) <= 1e-10:
                return res
        tau = tau_new

    # Exact monotone bisection fallback
    a = float(w.min()) - max_w - 1.0
    b = float(w.max()) - min_w + 1.0
    for _ in range(52):
        mid = 0.5 * (a + b)
        if float(np.clip(w - mid, min_w, max_w).sum()) > 1.0:
            a = mid
        else:
            b = mid
    return np.clip(w - 0.5 * (a + b), min_w, max_w)


def _causal_asset_vols(returns: np.ndarray, vol_window: int, min_periods: int, vol_floor: float = 1e-4) -> np.ndarray:
    """Compute strictly causal (shifted by 1) per-column rolling volatility matrix."""
    t_len, k_num = returns.shape
    sigma = np.ones((t_len, k_num), dtype=np.float64)
    if t_len <= 1:
        return sigma

    min_p = max(2, min(int(min_periods), int(vol_window)))
    fast_w = max(2, min(10, int(vol_window)))
    fast_mp = max(2, min(5, min_p))

    for j in range(k_num):
        col = returns[:, j]
        _, var_slow, _ = _rolling_sample_stats(col, window=vol_window, min_periods=min_p)
        _, var_fast, _ = _rolling_sample_stats(col, window=fast_w, min_periods=fast_mp)
        # Asymmetric fast-risk-off std: max(fast_std, slow_std) shifted by 1 bar
        std_slow = np.sqrt(np.maximum(var_slow, 0.0))
        std_fast = np.sqrt(np.maximum(var_fast, 0.0))
        comb = np.where(np.isfinite(std_slow) & np.isfinite(std_fast), np.maximum(std_fast, std_slow), std_slow)

        shifted = np.ones(t_len, dtype=np.float64)
        ok = np.isfinite(comb[:-1])
        shifted[1:] = np.where(ok, np.maximum(comb[:-1], vol_floor), 1.0)
        sigma[:, j] = shifted

    return sigma


def adahedge_weights(
    returns: Any,
    min_w: float = 0.20,
    max_w: float = 0.80,
    rebal_buffer: float = 0.02,
    vol_window: int = 20,
    min_periods: int = 10,
    decay: float = 1.0,
) -> np.ndarray:
    """Strictly causal AdaHedge (de Rooij et al. 2014) online portfolio allocator.

    Automatically tunes the Hedge learning rate via the cumulative mixability
    gap ``eta_t = ln(K) / Delta_{t-1}`` over risk-parity standardized returns
    ``r_t / sigma_{t-1}``, combines expert probabilities with causal inverse
    volatility, projects onto the bounded simplex ``[min_w, max_w]``, and
    enforces a one-way turnover rebalance buffer ``rebal_buffer``.

    Strictly causal: ``weights[t]`` is determined solely from ``returns[:t]``
    and applied to earn ``returns[t]``.
    """
    r = np.asarray(returns, dtype=np.float64)
    if r.ndim != 2:
        raise ValueError(f"returns must be 2D (T, K), got shape {r.shape}")
    t_len, k_num = r.shape
    if t_len == 0 or k_num == 0:
        return np.empty((t_len, k_num), dtype=np.float64)

    if min_w < 0.0 or max_w > 1.0 or min_w > max_w:
        raise ValueError(f"Invalid weight bounds: min_w={min_w}, max_w={max_w}")
    if k_num * min_w > 1.0 + 1e-12 or k_num * max_w < 1.0 - 1e-12:
        raise ValueError(f"Infeasible bounds [{min_w}, {max_w}] for K={k_num} assets")
    if k_num == 1:
        return np.ones((t_len, 1), dtype=np.float64)
    if not (0.0 < decay <= 1.0):
        raise ValueError(f"decay must be in (0, 1], got {decay}")

    sigma_all = _causal_asset_vols(r, vol_window=vol_window, min_periods=min_periods)
    r_clean = np.where(np.isfinite(r), r, 0.0)

    weights = np.empty((t_len, k_num), dtype=np.float64)
    uniform = np.full(k_num, 1.0 / float(k_num), dtype=np.float64)
    w_held = _project_bounded_simplex(uniform, min_w, max_w)

    cum_loss = np.zeros(k_num, dtype=np.float64)
    delta_cum = 0.0
    log_k = math.log(float(k_num))

    for t in range(t_len):
        if delta_cum <= 1e-15:
            eta = math.inf
            l_min = float(cum_loss.min())
            ties = cum_loss <= (l_min + 1e-15)
            cnt_ties = float(ties.sum())
            p = ties.astype(np.float64) / cnt_ties
            log_p = np.where(ties, -math.log(cnt_ties), -1e9)
        else:
            eta = log_k / delta_cum
            shifted = cum_loss - float(cum_loss.min())
            log_unnorm = -eta * shifted
            max_lu = float(log_unnorm.max())
            w_exp = np.exp(np.maximum(log_unnorm - max_lu, -700.0))
            sum_exp = float(w_exp.sum())
            p = w_exp / sum_exp
            log_p = log_unnorm - (max_lu + math.log(sum_exp))

        sig_t = sigma_all[t]
        raw_w = p / sig_t
        s_raw = float(raw_w.sum())
        raw_w = raw_w / s_raw if (s_raw > 0.0 and np.isfinite(s_raw)) else uniform
        w_target = _project_bounded_simplex(raw_w, min_w, max_w)

        if t > 0:
            half_l1 = 0.5 * float(np.abs(w_target - w_held).sum())
            if half_l1 >= rebal_buffer:
                w_held = w_target

        weights[t] = w_held

        # Observe bar t return and update AdaHedge mixability gap for t + 1
        ell = -(r_clean[t] / sig_t)
        lin_loss = float(np.dot(p, ell))
        if math.isinf(eta):
            mix_loss = float(ell[p > 0.0].min())
        else:
            z = log_p - eta * ell
            z_max = float(z.max())
            lse = z_max + math.log(float(np.exp(np.maximum(z - z_max, -700.0)).sum()))
            mix_loss = -lse / eta

        delta_t = max(0.0, lin_loss - mix_loss)
        delta_cum = decay * delta_cum + delta_t
        cum_loss = decay * cum_loss + ell

    return weights


def _lag_array(arr: np.ndarray, lag: int) -> np.ndarray:
    """Shift array forward by ``lag`` bars, keeping leading values intact."""
    out = arr.copy()
    if lag > 0 and lag < len(arr):
        out[lag:] = arr[:-lag]
    return out


def kalman_yz_regime_scale(
    open_px: Any,
    high_px: Any,
    low_px: Any,
    close_px: Any,
    annual_days: int = 252,
    yz_fast_window: int = 5,
    yz_slow_window: int = 30,
    vol_ratio_thresh: float = 2.0,
    vel_z_thresh: float = -0.45,
    brake_floor: float = 0.35,
) -> np.ndarray:
    """Causal regime multiplier in ``[brake_floor, 1.0]`` from Yang-Zhang gap vol + Kalman velocity."""
    yz_f = yang_zhang_vol(open_px, high_px, low_px, close_px, window=yz_fast_window, annual_days=annual_days, shift=1)
    yz_s = yang_zhang_vol(open_px, high_px, low_px, close_px, window=yz_slow_window, annual_days=annual_days, shift=1)
    yz_s_base = _lag_array(yz_s, lag=yz_fast_window)

    vol_ratio = np.where(
        np.isfinite(yz_f) & np.isfinite(yz_s_base) & (yz_s_base > 1e-6),
        yz_f / yz_s_base,
        1.0,
    )
    yz_brake = np.clip(1.0 - 0.5 * np.maximum(0.0, vol_ratio - vol_ratio_thresh), brake_floor, 1.0)

    kf = kalman_trend_velocity(close_px)
    vz = kf["vel_zscore"]
    kf_mult = np.where(
        vz < vel_z_thresh,
        np.clip(1.0 + 0.40 * (vz - vel_z_thresh), brake_floor, 1.0),
        1.0,
    )
    return np.clip(yz_brake * kf_mult, brake_floor, 1.0)


def _vol_target_with_borrow(
    ret: Any,
    idle_rate: Any,
    mm: Any,
    target_vol: float,
    max_lev: float,
    borrow_apr: float = 0.055,
) -> Any:
    """Strictly causal portfolio vol-targeting with DTB3 idle cash and 5.5% borrow cost."""
    import pandas as pd
    import strategies36 as S36

    arr = ret.to_numpy(dtype=float)
    idl = idle_rate.to_numpy(dtype=float)
    tox = mm["micro_tox_score"].reindex(ret.index).ffill().fillna(0.3).to_numpy(dtype=float)
    n = len(arr)
    out = np.zeros(n, dtype=np.float64)
    eq, peak = 1.0, 1.0
    v5_hist: list[float] = []

    for t in range(n):
        scale = 1.0
        if target_vol > 0.0 and t >= 20:
            v20 = float(np.std(arr[t - 20 : t], ddof=1) * math.sqrt(365.0))
            v10 = float(np.std(arr[t - 10 : t], ddof=1) * math.sqrt(365.0))
            scale = min(max_lev, target_vol / max(max(v10, v20), 0.03))
        if t >= 10:
            v5 = float(np.std(arr[t - 5 : t], ddof=1) * math.sqrt(365.0))
            v5_hist.append(v5)
            if len(v5_hist) >= 30:
                med60 = float(np.median(v5_hist[-60:]))
                if med60 > 1e-4 and v5 > 2.75 * med60:
                    scale *= 0.70
        if t >= 1 and tox[t - 1] > 0.72:
            scale *= 0.75

        dm = S36.soft_taper(1.0 - eq / peak, dd_start=0.06, dd_full=0.16, floor=0.35)
        lev = min(max_lev, scale * dm)
        r = lev * arr[t] + max(0.0, 1.0 - lev) * idl[t] - max(0.0, lev - 1.0) * (borrow_apr / 365.0)
        out[t] = r
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)

    return pd.Series(out, index=ret.index)


def benchmark_vs_baselines() -> dict[str, Any]:
    """Benchmark Kalman+Yang-Zhang+AdaHedge vs baseline_mft_lft_1x and Q56_hybrid_1x.

    Evaluates full 2020-2026 AND locked-box 2024-2026 OOS metrics plus wall-clock
    runtime, loading Q56_hybrid_1x from ``results/round56.json``.
    """
    import pandas as pd

    import lab
    import mft_lft_fund as MLF
    import monthly_reset_audit as MRA
    import run_round52 as R52
    import yearly_goal_loop as YG

    t0 = time.perf_counter()
    baseline = MLF.run_fund_backtest()
    t_base = time.perf_counter() - t0

    round56_path = os.path.join(HERE, "results", "round56.json")
    with open(round56_path, "r", encoding="utf-8") as fh:
        round56 = json.load(fh)
    q56_1x = round56["variants"]["Q56_hybrid_1x"]

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
    spy_df = u_eq["daily"]["SPY"].reindex(r_crypto.index.normalize()).ffill().set_axis(r_crypto.index)

    idle = pd.Series(u_c["rate"].to_numpy(dtype=float) / 100.0 / 365.0, index=r_crypto.index).shift(1).fillna(0.0)

    # 1. Causal Yang-Zhang gap-vol + Kalman trend-velocity portfolio regime brake
    # Applied at the portfolio level (preserving C1 carry & macro ETF rotation)
    b_btc = kalman_yz_regime_scale(
        btc_df["o"], btc_df["h"], btc_df["l"], btc_df["c"],
        annual_days=365, yz_fast_window=5, yz_slow_window=30,
        vol_ratio_thresh=2.10, vel_z_thresh=-1.00, brake_floor=0.55,
    )
    b_spy = kalman_yz_regime_scale(
        spy_df["o"], spy_df["h"], spy_df["l"], spy_df["c"],
        annual_days=252, yz_fast_window=5, yz_slow_window=30,
        vol_ratio_thresh=2.10, vel_z_thresh=-0.80, brake_floor=0.65,
    )
    p_brake = 0.65 + 0.35 * np.minimum(b_btc, b_spy)

    # 2. Causal regime-adaptive AdaHedge online allocator with risk-parity standardization
    ret_mat = np.column_stack([r_crypto.to_numpy(dtype=np.float64), r_eq.to_numpy(dtype=np.float64)])
    w_ada = adahedge_weights(ret_mat, min_w=0.20, max_w=0.80, rebal_buffer=0.02, decay=0.97)
    r_raw_blend = pd.Series(np.sum(w_ada * ret_mat, axis=1), index=r_crypto.index)
    r_blend = r_raw_blend * p_brake + (1.0 - p_brake) * idle

    # 3. Evaluate unlevered 1x raw, vol-matched 1x spot (<=1.0x), and 1.8x borrow-adjusted
    r_1x_raw = MRA.apply_vol_tox_circuit_breaker(r_blend, mm, target_vol=0.0, max_lev=1.0)
    r_1x_spot = _vol_target_with_borrow(r_blend, idle, mm, target_vol=0.09, max_lev=1.0, borrow_apr=0.055)
    r_1p8x = _vol_target_with_borrow(r_blend, idle, mm, target_vol=0.16, max_lev=1.8, borrow_apr=0.055)
    t_kyz = time.perf_counter() - t1

    def _pack(s: pd.Series) -> dict[str, Any]:
        st = YG.evaluate_by_year(s)
        oos_s = s[s.index >= pd.Timestamp("2024-01-01", tz="UTC")]
        oos = YG.evaluate_by_year(oos_s)
        mo = MRA.monthly_reset_report(s)
        return {
            "cagr_pct": st["cagr_pct"],
            "max_dd_pct": st["max_dd_pct"],
            "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
            "sharpe": st["sharpe"],
            "deflated_sharpe": round(float(lab.deflated_sharpe(s, round56["n_trials"])), 4),
            "y2022_pct": st["years"]["2022"]["ann_pct"],
            "y2026_pct": st["years"]["2026"]["ann_pct"],
            "monthly_win_rate_pct": mo["win_rate_pct"],
            "oos_2024_2026_cagr_pct": oos["cagr_pct"],
            "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
            "oos_2024_2026_calmar": round(oos["cagr_pct"] / max(0.01, oos["max_dd_pct"]), 3),
            "oos_2024_2026_sharpe": oos["sharpe"],
        }

    return {
        "baselines": {
            "baseline_mft_lft_1x": baseline["fund_1x_spot"],
            "baseline_mft_lft_1p8x": baseline["fund_1p8x_borrow_adj"],
            "Q56_hybrid_1x": q56_1x,
        },
        "kalman_yz_adahedge": {
            "kyz_1x_spot": _pack(r_1x_spot),
            "kyz_1x_raw": _pack(r_1x_raw),
            "kyz_1p8x_borrow_adj": _pack(r_1p8x),
            "mean_crypto_weight": round(float(w_ada[:, 0].mean()), 4),
        },
        "runtime_sec": {
            "baseline_mft_lft": round(t_base, 3),
            "kalman_yz_adahedge": round(t_kyz, 3),
        },
    }


if __name__ == "__main__":
    print(json.dumps(benchmark_vs_baselines(), indent=2))
