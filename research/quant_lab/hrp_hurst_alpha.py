"""Hierarchical Risk Parity, Hurst Roughness & Split-Conformal Conviction Alpha Module.

Implements strictly causal (``t-1 -> t``), pure NumPy/Pandas/SciPy primitives
with zero ``pyarrow`` dependency at import time and strict pre-``2024-01-01``
calibration freeze:

1. ``hrp_weights``:
   Causal Hierarchical Risk Parity (HRP) portfolio allocator using correlation
   distance ``d_{ij} = sqrt(0.5 * (1 - rho_{ij}))``, hierarchical linkage
   seriation (quasi-diagonalization), and top-down recursive inverse-variance
   bisection — avoiding unstable covariance matrix inversion and handling
   collinear or degenerate/zero-variance columns cleanly.

2. ``rolling_hurst_variogram``:
   Strictly causal rolling Hurst exponent via multi-lag log-price variance
   scaling (order-2 structure function / variogram ``E[|Delta_tau ln P|^2] ~ tau^(2H)``
   over lags ``tau in {1, 2, 4, 8, 16}``), shifted by 1 day so bar ``t`` uses
   only prices up to ``t - 1``.

3. ``conformal_conviction_gate``:
   Pre-2024 calibrated asymmetric split-conformal conviction multiplier mapping
   ``(hurst, entropy, trend_strength) -> [0.0, 1.0]`` via finite-sample
   conformal p-values / transducer odds with asymmetric persistent-uptrend
   preservation (avoiding over-clipping strong persistent uptrends).

4. ``benchmark_vs_baselines``:
   Full ``2020-2026`` and locked-box ``2024-2026`` OOS benchmark integrated with
   ``mft_lft_fund.py`` against both ``baseline_mft_lft_1x`` and ``Q56_hybrid_1x``.

Academic & Quantitative References:
- López de Prado, M. (2016). "Building Diversified Portfolios that Outperform
  Out-of-Sample." *The Journal of Portfolio Management*, 42(4), 59-69.
- Mandelbrot, B. B., & Van Ness, J. W. (1968). "Fractional Brownian Motions,
  Fractional Noises and Applications." *SIAM Review*, 10(4), 422-437.
- Gatheral, J., Jaisson, T., & Rosenbaum, M. (2018). "Volatility is rough."
  *Quantitative Finance*, 18(6), 933-949.
- Shafer, G., & Vovk, V. (2008). "A Tutorial on Conformal Prediction."
  *Journal of Machine Learning Research*, 9, 371-421.
- Angelopoulos, A. N., & Bates, S. (2023). "Conformal Prediction: A Gentle
  Introduction." *Foundations and Trends in Machine Learning*, 16(4), 494-591.
  DOI: 10.1561/2200000101.
"""
from __future__ import annotations

import json
import math
import os
from typing import Any

import numpy as np
import pandas as pd
from scipy.cluster.hierarchy import leaves_list, linkage
from scipy.spatial.distance import squareform

DEFAULT_CALIBRATION_CUTOFF = "2024-01-01"
HURST_LAGS: tuple[int, ...] = (1, 2, 4, 8, 16)


def hrp_weights(
    ret_window: np.ndarray,
    linkage_method: str = "single",
    min_std: float = 1e-12,
) -> np.ndarray:
    """Compute Hierarchical Risk Parity weights from a causal return window.

    Implements López de Prado (2016):
      1. Correlation distance ``d_{ij} = sqrt(0.5 * (1 - rho_{ij}))``.
      2. Hierarchical tree clustering & quasi-diagonal leaf seriation.
      3. Top-down recursive inverse-variance bisection.

    Cleanly handles degenerate/zero-variance columns (assigning them zero weight
    when at least one active column exists, or uniform ``1/N`` if all columns
    are degenerate) and collinear assets, always returning non-negative weights
    summing to 1.0.
    """
    r = np.asarray(ret_window, dtype=np.float64)
    if r.ndim != 2:
        raise ValueError(f"ret_window must be 2D (T, N), got shape {r.shape}")

    t_len, n_assets = r.shape
    if n_assets == 0:
        return np.empty(0, dtype=np.float64)
    if n_assets == 1:
        return np.array([1.0], dtype=np.float64)
    if t_len < 2:
        return np.full(n_assets, 1.0 / float(n_assets), dtype=np.float64)

    finite_counts = np.sum(np.isfinite(r), axis=0)
    r_clean = np.where(np.isfinite(r), r, 0.0)
    stds = np.std(r_clean, axis=0, ddof=1)
    valid_idx = np.where((finite_counts >= 2) & (stds > min_std))[0]

    if len(valid_idx) == 0:
        return np.full(n_assets, 1.0 / float(n_assets), dtype=np.float64)
    if len(valid_idx) == 1:
        w = np.zeros(n_assets, dtype=np.float64)
        w[valid_idx[0]] = 1.0
        return w

    rv = r_clean[:, valid_idx]
    cov = np.atleast_2d(np.cov(rv, rowvar=False, ddof=1))
    cov = 0.5 * (cov + cov.T)

    diag_std = np.sqrt(np.maximum(np.diag(cov), 1e-24))
    corr = cov / np.outer(diag_std, diag_std)
    corr = np.nan_to_num(np.clip(corr, -1.0, 1.0), nan=0.0)
    np.fill_diagonal(corr, 1.0)

    dist = np.sqrt(np.maximum(0.0, 0.5 * (1.0 - corr)))
    dist = 0.5 * (dist + dist.T)
    np.fill_diagonal(dist, 0.0)

    condensed = squareform(dist, checks=False)
    z_link = linkage(condensed, method=linkage_method)
    sort_order = leaves_list(z_link).tolist()

    w_sub = np.ones(len(valid_idx), dtype=np.float64)
    clusters: list[list[int]] = [sort_order]

    def _cluster_var(items: list[int]) -> float:
        sub_cov = cov[np.ix_(items, items)]
        diag_v = np.maximum(np.diag(sub_cov), 1e-18)
        ivp = 1.0 / diag_v
        ivp /= float(np.sum(ivp))
        c_var = float(ivp @ sub_cov @ ivp)
        scale_floor = 1e-12 * max(float(np.mean(diag_v)), 1e-12)
        return max(c_var, scale_floor)

    while clusters:
        next_clusters: list[list[int]] = []
        for cl in clusters:
            if len(cl) <= 1:
                continue
            mid = len(cl) // 2
            left = cl[:mid]
            right = cl[mid:]
            var_l = _cluster_var(left)
            var_r = _cluster_var(right)
            denom = var_l + var_r
            alpha = 1.0 - var_l / denom if denom > 0.0 else 0.5
            w_sub[left] *= alpha
            w_sub[right] *= 1.0 - alpha
            if len(left) > 1:
                next_clusters.append(left)
            if len(right) > 1:
                next_clusters.append(right)
        clusters = next_clusters

    w = np.zeros(n_assets, dtype=np.float64)
    w[valid_idx] = w_sub / float(np.sum(w_sub))
    return w


def rolling_hurst_variogram(
    prices: pd.Series,
    window: int = 64,
    lags: tuple[int, ...] = HURST_LAGS,
) -> pd.Series:
    """Strictly causal rolling Hurst exponent via multi-lag log-price variogram.

    Estimates ``H`` from the order-2 structure function scaling
    ``E[|Delta_tau ln P|^2] ~ tau^(2H)`` over ``tau in {1, 2, 4, 8, 16}``
    (Mandelbrot & Van Ness 1968; Gatheral, Jaisson & Rosenbaum 2018) inside a
    trailing window of ``window`` bars, then shifts by 1 day so output at bar
    ``t`` depends strictly on ``prices.iloc[:t]``.
    """
    if not isinstance(prices, pd.Series):
        prices = pd.Series(prices, dtype=np.float64)
    if len(prices) == 0:
        return pd.Series([], index=prices.index, dtype=np.float64)

    eff_lags = tuple(int(tau) for tau in lags if int(tau) >= 1 and window - int(tau) >= 4)
    if len(eff_lags) < 2:
        eff_lags = (1, 2)

    px_clean = prices.astype(np.float64).where(prices > 0.0)
    log_p = np.log(px_clean)

    log_vars = []
    for tau in eff_lags:
        d2 = (log_p - log_p.shift(tau)) ** 2
        w_eff = max(4, int(window) - tau)
        mean_d2 = d2.rolling(w_eff, min_periods=w_eff).mean()
        log_vars.append(np.log(mean_d2.clip(lower=1e-16)).to_numpy(dtype=np.float64))

    mat = np.column_stack(log_vars)
    valid_rows = np.all(np.isfinite(mat), axis=1)

    log_taus = np.log(np.asarray(eff_lags, dtype=np.float64))
    x_centered = log_taus - float(np.mean(log_taus))
    denom = float(np.sum(x_centered * x_centered))

    hurst_raw = np.full(len(prices), np.nan, dtype=np.float64)
    if np.any(valid_rows) and denom > 1e-12:
        sub_mat = mat[valid_rows]
        y_centered = sub_mat - np.mean(sub_mat, axis=1, keepdims=True)
        slope = np.sum(y_centered * x_centered[None, :], axis=1) / denom
        hurst_raw[valid_rows] = np.clip(0.5 * slope, 0.01, 0.99)

    return pd.Series(hurst_raw, index=prices.index, dtype=np.float64).shift(1)


def _causal_spectral_entropy_z(
    ret_df: pd.DataFrame,
    window: int = 30,
    z_window: int = 60,
    min_periods: int = 15,
    eps: float = 1e-12,
) -> pd.DataFrame:
    """Strictly causal (t-1 -> t) Von Neumann spectral entropy over cross-asset returns."""
    arr = ret_df.to_numpy(dtype=np.float64)
    n_rows, n_cols = arr.shape
    idx = ret_df.index

    ent = np.full(n_rows, np.nan, dtype=np.float64)
    pur = np.full(n_rows, np.nan, dtype=np.float64)

    for t in range(min_periods, n_rows):
        sub = arr[max(0, t - window) : t]
        valid_cols = []
        for j in range(n_cols):
            col = sub[:, j]
            fin = col[np.isfinite(col)]
            if len(fin) >= min_periods and float(np.std(fin)) > 1e-10:
                valid_cols.append(j)
        if len(valid_cols) < 2:
            ent[t] = 1.0
            pur[t] = 1.0 / max(1, n_cols)
            continue
        sub_v = np.nan_to_num(sub[:, valid_cols], nan=0.0)
        cov = np.atleast_2d(np.cov(sub_v, rowvar=False, ddof=1))
        sds = np.sqrt(np.maximum(np.diag(cov), 1e-24))
        corr = cov / np.outer(sds, sds)
        corr = np.nan_to_num(np.clip(corr, -1.0, 1.0), nan=0.0)
        np.fill_diagonal(corr, 1.0)
        eigvals = np.clip(np.linalg.eigvalsh(0.5 * (corr + corr.T)), eps, None)
        probs = eigvals / float(np.sum(eigvals))
        ent[t] = float(np.clip(-np.sum(probs * np.log(probs)) / math.log(float(len(valid_cols))), 0.0, 1.0))
        pur[t] = float(np.clip(np.sum(probs * probs), 1.0 / float(len(valid_cols)), 1.0))

    ent_s = pd.Series(ent, index=idx, dtype=np.float64)
    pur_s = pd.Series(pur, index=idx, dtype=np.float64)
    roll_mean = ent_s.rolling(z_window, min_periods=min_periods).mean()
    roll_std = ent_s.rolling(z_window, min_periods=min_periods).std().clip(lower=1e-4)
    ent_z = ((ent_s - roll_mean) / roll_std).fillna(0.0)

    return pd.DataFrame(
        {
            "vn_entropy": ent_s.ffill().fillna(1.0),
            "vn_purity": pur_s.ffill().fillna(1.0 / max(1, n_cols)),
            "vn_entropy_z": ent_z,
        },
        index=idx,
    )


def _fit_l2_logistic_np(
    X: np.ndarray,
    y: np.ndarray,
    reg: float = 5.0,
    n_iter: int = 20,
) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Pure-NumPy standardized L2-regularized logistic regression via damped IRLS."""
    n, d = X.shape
    mu = np.mean(X, axis=0)
    sd = np.maximum(np.std(X, axis=0), 1e-6)
    xs = np.hstack([np.ones((n, 1), dtype=np.float64), (X - mu) / sd])

    w = np.zeros(d + 1, dtype=np.float64)
    reg_vec = np.array([0.0] + [float(reg)] * d, dtype=np.float64)
    reg_mat = np.diag(reg_vec) + 1e-6 * np.eye(d + 1, dtype=np.float64)

    for _ in range(n_iter):
        z = np.clip(xs @ w, -20.0, 20.0)
        p = 1.0 / (1.0 + np.exp(-z))
        grad = xs.T @ (p - y) + reg_vec * w
        wt = np.maximum(p * (1.0 - p), 1e-6)
        hess = (xs.T * wt) @ xs + reg_mat
        step = np.linalg.solve(hess, grad)
        w -= step
        if float(np.max(np.abs(step))) < 1e-6:
            break
    return w, mu, sd


def _predict_l2_logistic_np(
    X: np.ndarray,
    w: np.ndarray,
    mu: np.ndarray,
    sd: np.ndarray,
) -> np.ndarray:
    """Predict calibrated probabilities from pure-NumPy L2 logistic model."""
    xs = np.hstack([np.ones((X.shape[0], 1), dtype=np.float64), (X - mu) / sd])
    z = np.clip(xs @ w, -20.0, 20.0)
    return 1.0 / (1.0 + np.exp(-z))


def _build_regime_design(
    hurst_arr: np.ndarray,
    ent_arr: np.ndarray,
    trend_arr: np.ndarray,
) -> np.ndarray:
    """Build asymmetric regime feature matrix from (hurst, entropy, trend_strength)."""
    return np.column_stack([
        hurst_arr,
        ent_arr,
        trend_arr,
        np.maximum(0.0, hurst_arr - 0.50) * np.maximum(0.0, 0.50 - trend_arr),
        np.maximum(0.0, 0.50 - hurst_arr) * np.maximum(0.0, 0.50 - trend_arr),
        np.maximum(0.0, -ent_arr) * np.maximum(0.0, 0.55 - trend_arr),
    ])


def conformal_conviction_gate(
    hurst: pd.Series,
    entropy: pd.Series,
    trend_strength: pd.Series,
    returns: pd.Series | None = None,
    idle_rate: pd.Series | None = None,
    calibration_cutoff: str = DEFAULT_CALIBRATION_CUTOFF,
    alpha: float = 0.15,
    horizon_days: int = 5,
    gate_floor: float = 0.45,
    trend_preserve: float = 0.58,
    entropy_crisis_floor: float = -1.25,
    reg: float = 5.0,
    refit_step: int = 21,
    min_cal_samples: int = 160,
    warmup_bars: int = 64,
    input_shift: int = 0,
) -> tuple[pd.Series, dict[str, Any]]:
    """Pre-2024 calibrated asymmetric split-conformal conviction gate.

    Maps causal ``(hurst, entropy, trend_strength)`` into ``[0.0, 1.0]`` using
    strictly causal expanding split-conformal transducer odds (Shafer & Vovk
    2008; Angelopoulos & Bates 2023) that freeze permanently at
    ``calibration_cutoff`` (default ``"2024-01-01"``).

    Strong persistent uptrends (``trend_strength >= trend_preserve`` without
    systemic entropy collapse) remain unclipped at ``1.0``, while rough or
    compressed-entropy downturns with weak conformal odds scale smoothly toward
    ``gate_floor``.

    Returns
    -------
    tuple[pd.Series, dict[str, Any]]
        - ``gate_multiplier``: Series in ``[0.0, 1.0]`` indexed like ``hurst``.
        - ``diagnostics``: Frozen pre-2024 calibration parameters and summary stats.
    """
    idx = hurst.index
    n = len(idx)
    if n == 0:
        return pd.Series([], index=idx, dtype=np.float64), {"n_samples": 0, "mode": "empty"}

    h_s = hurst.reindex(idx).astype(np.float64)
    e_s = entropy.reindex(idx).astype(np.float64)
    tr_s = trend_strength.reindex(idx).astype(np.float64)
    if input_shift > 0:
        h_s = h_s.shift(input_shift)
        e_s = e_s.shift(input_shift)
        tr_s = tr_s.shift(input_shift)

    h_arr = np.nan_to_num(h_s.to_numpy(dtype=np.float64), nan=0.5)
    e_arr = np.nan_to_num(e_s.to_numpy(dtype=np.float64), nan=0.0)
    tr_arr = np.nan_to_num(tr_s.to_numpy(dtype=np.float64), nan=0.5)
    X = _build_regime_design(h_arr, e_arr, tr_arr)

    cutoff_ts = (
        pd.Timestamp(calibration_cutoff, tz=idx.tz)
        if getattr(idx, "tz", None) is not None
        else pd.Timestamp(calibration_cutoff)
    )
    pre_mask = np.asarray(idx < cutoff_ts, dtype=bool)
    has_pre = bool(np.any(pre_mask))
    last_pre_idx = int(np.where(pre_mask)[0][-1]) if has_pre else -1

    gate = np.ones(n, dtype=np.float64)
    uptrend_preserved = np.zeros(n, dtype=bool)

    if returns is None:
        # Prior uncalibrated asymmetric gate when return history is not provided
        for t in range(n):
            if tr_arr[t] >= trend_preserve and e_arr[t] >= entropy_crisis_floor:
                gate[t] = 1.0
                uptrend_preserved[t] = True
            else:
                rough_pen = max(0.0, 0.50 - h_arr[t]) * 1.4
                pers_down = max(0.0, h_arr[t] - 0.50) * max(0.0, 0.50 - tr_arr[t]) * 3.0
                ent_pen = max(0.0, -e_arr[t] - 0.3) * max(0.0, 0.55 - tr_arr[t]) * 0.6
                tr_pen = max(0.0, 0.55 - tr_arr[t]) * 0.8
                risk = rough_pen + pers_down + ent_pen + tr_pen
                gate[t] = float(np.clip(1.0 - risk, gate_floor, 1.0))

        gate_s = pd.Series(gate, index=idx, dtype=np.float64)
        diag = {
            "mode": "prior_uncalibrated",
            "calibration_cutoff": calibration_cutoff,
            "alpha": float(alpha),
            "gate_floor": float(gate_floor),
            "mean_gate": round(float(np.mean(gate)), 4),
            "pct_unclipped_full": round(float(np.mean(gate >= 1.0 - 1e-9) * 100.0), 2),
            "pct_uptrend_preserved": round(float(np.mean(uptrend_preserved) * 100.0), 2),
        }
        return gate_s, diag

    r_arr = np.nan_to_num(returns.reindex(idx).to_numpy(dtype=np.float64), nan=0.0)
    if idle_rate is not None:
        ir_arr = np.nan_to_num(idle_rate.reindex(idx).ffill().shift(1).to_numpy(dtype=np.float64), nan=0.0)
    else:
        ir_arr = np.zeros(n, dtype=np.float64)

    h_days = max(1, int(horizon_days))
    fwd = np.full(n, np.nan, dtype=np.float64)
    fwd_rf = np.full(n, np.nan, dtype=np.float64)
    for t in range(n - h_days + 1):
        fwd[t] = float(np.prod(1.0 + r_arr[t : t + h_days]) - 1.0)
        fwd_rf[t] = float(np.sum(ir_arr[t : t + h_days]))

    w_log: np.ndarray | None = None
    mu_fit: np.ndarray | None = None
    sd_fit: np.ndarray | None = None
    cal_fav: np.ndarray | None = None
    cal_adv: np.ndarray | None = None
    q_fav_alpha: float = 0.5
    n_cal_total = 0
    n_proper_train = 0
    n_cal_holdout = 0
    last_fit = -10_000
    frozen_done = False

    start_bar = min(n, max(warmup_bars + h_days, min_cal_samples + h_days))
    for t in range(start_bar, n):
        max_s = t - h_days
        if has_pre:
            idx_end = min(max_s, last_pre_idx - h_days + 1)
        else:
            idx_end = -1

        entering_oos = has_pre and (t > last_pre_idx) and not frozen_done
        need_refit = (t - last_fit >= refit_step) and (not has_pre or t <= last_pre_idx)

        if idx_end >= warmup_bars + min_cal_samples - 1 and (need_refit or entering_oos):
            cal_idx = np.arange(warmup_bars, idx_end + 1)
            split = int(0.55 * len(cal_idx))
            tr_i = cal_idx[:split]
            ca_i = cal_idx[split:]
            y_tr = (fwd[tr_i] > fwd_rf[tr_i]).astype(np.float64)
            y_ca = (fwd[ca_i] > fwd_rf[ca_i]).astype(int)
            if (
                len(np.unique(y_tr)) >= 2
                and int(np.sum(y_ca == 1)) >= 10
                and int(np.sum(y_ca == 0)) >= 10
            ):
                w_log, mu_fit, sd_fit = _fit_l2_logistic_np(X[tr_i], y_tr, reg=reg)
                p_ca = _predict_l2_logistic_np(X[ca_i], w_log, mu_fit, sd_fit)
                cal_fav = np.sort(p_ca[y_ca == 1])
                cal_adv = np.sort(p_ca[y_ca == 0])
                n_f = len(cal_fav)
                q_idx = min(n_f - 1, max(0, int(math.floor(alpha * (n_f + 1))) - 1))
                q_fav_alpha = float(cal_fav[q_idx])
                n_cal_total = int(len(cal_idx))
                n_proper_train = int(len(tr_i))
                n_cal_holdout = int(len(ca_i))
                last_fit = t
                if entering_oos or (has_pre and idx_end == last_pre_idx - h_days + 1):
                    frozen_done = True

        if w_log is not None and cal_fav is not None and cal_adv is not None:
            p_t = float(_predict_l2_logistic_np(X[t : t + 1], w_log, mu_fit, sd_fit)[0])
            n_f = len(cal_fav)
            n_a = len(cal_adv)
            p_val_fav = (1.0 + float(np.searchsorted(cal_fav, p_t, side="right"))) / (n_f + 1.0)
            p_val_adv = (1.0 + float(n_a - np.searchsorted(cal_adv, p_t, side="left"))) / (n_a + 1.0)
            conv = p_val_fav / (p_val_fav + p_val_adv)

            if tr_arr[t] >= trend_preserve and e_arr[t] >= entropy_crisis_floor:
                gate[t] = 1.0
                uptrend_preserved[t] = True
            elif conv >= 0.50 and p_val_fav > alpha:
                gate[t] = 1.0
            else:
                frac = float(np.clip((conv - 0.15) / 0.35, 0.0, 1.0))
                gate[t] = float(gate_floor + (1.0 - gate_floor) * frac)

    gate_s = pd.Series(gate, index=idx, dtype=np.float64)
    post_mask = ~pre_mask
    diagnostics: dict[str, Any] = {
        "mode": "split_conformal_frozen",
        "calibration_cutoff": calibration_cutoff,
        "alpha": float(alpha),
        "horizon_days": h_days,
        "gate_floor": float(gate_floor),
        "trend_preserve": float(trend_preserve),
        "n_cal_total": n_cal_total,
        "n_proper_train": n_proper_train,
        "n_cal_holdout": n_cal_holdout,
        "frozen_q_alpha": round(float(q_fav_alpha), 6),
        "frozen_coef": [round(float(v), 6) for v in w_log] if w_log is not None else [],
        "frozen_mean": [round(float(v), 6) for v in mu_fit] if mu_fit is not None else [],
        "frozen_std": [round(float(v), 6) for v in sd_fit] if sd_fit is not None else [],
        "mean_gate_all": round(float(np.mean(gate)), 4),
        "mean_gate_pre_2024": round(float(np.mean(gate[pre_mask])), 4) if np.any(pre_mask) else None,
        "mean_gate_post_2024": round(float(np.mean(gate[post_mask])), 4) if np.any(post_mask) else None,
        "pct_unclipped_full": round(float(np.mean(gate >= 1.0 - 1e-9) * 100.0), 2),
        "pct_uptrend_preserved": round(float(np.mean(uptrend_preserved) * 100.0), 2),
    }
    return gate_s, diagnostics


def _simulate_hrp_crypto_q51(
    hrp_window: int = 60,
    dd_start: float = 0.06,
    dd_full: float = 0.20,
    dd_floor: float = 0.40,
) -> tuple[pd.Series, pd.DataFrame, pd.DataFrame, pd.Series]:
    """Causal BTC+ETH+SOL HRP-parity trend + C1 carry spot return series."""
    import lab
    import run_round51 as R51
    import strategies19 as S19
    import strategies35 as S35
    import strategies36 as S36
    import strategies40 as S40
    import yearly_goal_loop as YG
    from run_round19 import END, START, load_funding
    from run_round33 import DTB3

    coins = ("BTC", "ETH", "SOL")
    daily_full = {
        "BTC": R51._load_daily_csv("~/piquant-collect/daily_csv/BTC-USD_1D.csv"),
        "ETH": R51._load_daily_csv("~/piquant-collect/daily_csv/ETH-USD_1D.csv"),
        "SOL": R51._load_daily_csv("~/piquant-data/alts/SOL.csv"),
    }
    d = {
        c: daily_full[c][(daily_full[c].index >= START) & (daily_full[c].index < END)]
        for c in coins
    }
    idx = d["BTC"].index

    dual_1x = {}
    for c in coins:
        qd = pd.Series(S35.coherence_positions(daily_full[c], lev=1.0), index=daily_full[c].index)
        r3 = pd.Series(S40.r3_fastexit_qd_positions(daily_full[c], lev=1.0), index=daily_full[c].index)
        dual_1x[c] = (0.5 * (qd + r3)).reindex(idx).fillna(0.0)

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=np.float64), index=raw["date"].dt.tz_localize("UTC")).dropna()
    c1_ret = S19.portfolio(
        [S19.coin_returns(d[c], f[c], "C1", S19.BASE) for c in ("BTC", "ETH")]
    ).reindex(idx).fillna(0.0)

    c_ret_df = pd.DataFrame(
        {c: d[c]["c"].reindex(idx).pct_change().fillna(0.0) for c in coins},
        index=idx,
    )
    c_ret_mat = c_ret_df.to_numpy(dtype=np.float64)
    pos_mat = np.column_stack([dual_1x[c].to_numpy(dtype=np.float64) for c in coins])
    rf_d = rate.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=np.float64) / 100.0 / 365.0
    cr_arr = c1_ret.to_numpy(dtype=np.float64)

    sp = lab.US_SPOT
    cost_rate = (sp.taker_bps + sp.slip_bps) * 1e-4
    n = len(idx)
    out = np.zeros(n, dtype=np.float64)
    eq, peak = 1.0, 1.0
    act = np.zeros(len(coins), dtype=np.float64)
    prev = np.zeros(len(coins), dtype=np.float64)
    prev_idle = 0.60

    for t in range(n):
        held = np.zeros(len(coins), dtype=np.float64) if t == 0 else act.copy()
        turn = float(np.sum(np.abs(held - prev)))
        gpnl = float(np.sum(held * c_ret_mat[t]))
        trend_net = 0.60 * (gpnl - turn * cost_rate)
        bills = 0.0 if t == 0 else prev_idle * rf_d[t - 1]
        tr = trend_net + 0.40 * cr_arr[t] + bills
        out[t] = tr

        eq = max(1e-12, eq * (1.0 + tr))
        peak = max(peak, eq)
        mult = S36.soft_taper(1.0 - eq / peak, dd_start=dd_start, dd_full=dd_full, floor=dd_floor)

        if t >= 20:
            w = hrp_weights(c_ret_mat[max(0, t - hrp_window + 1) : t + 1])
        else:
            w = np.array([0.5, 0.5, 0.0], dtype=np.float64)
        act = w * pos_mat[t] * mult
        prev = held
        prev_idle = max(0.0, 0.60 * (1.0 - float(np.sum(act))))

    u_c = YG.load_universe(["BTC"])
    btc_df = u_c["daily"]["BTC"].reindex(idx).ffill()
    return pd.Series(out, index=idx, dtype=np.float64), c_ret_df, btc_df, rate


def _simulate_hrp_equities_sleeve(
    pre: dict[str, Any],
    hrp_window: int = 60,
    top_k: int = 3,
    lft_weight: float = 0.50,
    xs_weight: float = 0.40,
    mft_mr_weight: float = 0.10,
    rebal_buffer: float = 0.03,
) -> pd.Series:
    """Strictly causal LFT+MFT equities/macro sleeve with HRP risk budgeting in LFT."""
    import strategies36 as S36

    idx = pre["idx"]
    coins = pre["coins"]
    n = len(idx)
    px_df = pre["px_df"]
    ret_df = pre["ret_df"]
    ret_mat_all = ret_df.to_numpy(dtype=np.float64)
    ret_arr = {c: ret_df[c].to_numpy(dtype=np.float64) for c in coins}
    vol_arr = {c: pre["vol20_df"][c].to_numpy(dtype=np.float64) for c in coins}
    tr_arr = pre["trend_pos"]
    r5z_arr = {c: pre["ret5_z"][c].to_numpy(dtype=np.float64) for c in coins}

    m7 = px_df.pct_change(7)
    m28 = px_df.pct_change(28)
    m60 = px_df.pct_change(60)
    mom_score = ((m7 + m28 + m60) / 3.0) / pre["vol20_df"].clip(lower=0.10)
    mom_np = {c: mom_score[c].to_numpy(dtype=np.float64) for c in coins}

    ann_days = int(pre.get("annual_days", 252))
    cost_rate = 3.0 * 1e-4 if ann_days == 365 else 1.5 * 1e-4

    out = np.zeros(n, dtype=np.float64)
    eq, peak = 1.0, 1.0
    prev_w = {c: 0.0 for c in coins}
    held_w = {c: 0.0 for c in coins}

    for t in range(n):
        turn = sum(abs(held_w[c] - prev_w[c]) for c in coins)
        gross = sum(abs(held_w[c]) for c in coins)
        pnl = sum(held_w[c] * ret_arr[c][t] for c in coins)
        idle = max(0.0, 1.0 - gross)
        bills = 0.0 if t == 0 else idle * pre["rate"][t - 1]
        total_r = pnl - turn * cost_rate + bills
        out[t] = total_r

        eq = max(1e-12, eq * (1.0 + total_r))
        peak = max(peak, eq)
        dm = S36.soft_taper(1.0 - eq / peak, dd_start=0.08, dd_full=0.35, floor=0.35)

        scored = []
        for c in coins:
            m = mom_np[c][t]
            v = vol_arr[c][t]
            if np.isfinite(m) and np.isfinite(v) and v > 0.05:
                scored.append((m, c, v))
        scored.sort(reverse=True)

        target_w = {c: 0.0 for c in coins}
        active = [c for _, c, _ in scored]
        if active:
            inv_v = {c: 1.0 / max(vol_arr[c][t], 0.15) for c in active}
            tot_iv = sum(inv_v.values())
            w_iv = {c: inv_v[c] / tot_iv for c in active}

            if t >= 20:
                wh = hrp_weights(ret_mat_all[max(0, t - hrp_window + 1) : t + 1])
                w_hrp_all = {c: float(wh[i]) for i, c in enumerate(coins)}
                s_act = sum(w_hrp_all[c] for c in active)
                w_hrp = {c: w_hrp_all[c] / s_act for c in active} if s_act > 1e-12 else dict(w_iv)
            else:
                w_hrp = dict(w_iv)

            w_lft = {c: (w_hrp[c] * tr_arr[c][t] if m > 0.0 else 0.0) for m, c, _ in scored}
            lft_g = sum(w_lft.values())
            if lft_g > 1.0:
                for c in active:
                    w_lft[c] /= lft_g

            w_xs = {c: 0.0 for c in active}
            long_c = [c for m, c, _ in scored[: min(top_k, len(scored))] if m > 0.0]
            if long_c:
                iv_l = sum(inv_v[c] for c in long_c)
                for c in long_c:
                    w_xs[c] = inv_v[c] / iv_l

            w_mr = {c: 0.0 for c in active}
            mr_long = [c for m, c, _ in scored if r5z_arr[c][t] < -1.5 and tr_arr[c][t] > 0.25 and m > -0.2]
            for c in mr_long:
                w_mr[c] = 1.0 / len(mr_long)

            for c in active:
                target_w[c] = max(0.0, lft_weight * w_lft[c] + xs_weight * w_xs[c] + mft_mr_weight * w_mr[c]) * dm

        for c in coins:
            prev_w[c] = held_w[c]
            if abs(target_w[c] - held_w[c]) >= rebal_buffer or target_w[c] == 0.0:
                held_w[c] = target_w[c]

    return pd.Series(out, index=idx, dtype=np.float64)


def _scale_with_borrow(
    ret: pd.Series,
    mm: pd.DataFrame,
    target_vol: float = 0.16,
    max_lev: float = 1.8,
    borrow_apr: float = 0.055,
) -> pd.Series:
    """Apply causal 1.8x borrow-adjusted vol targeting matching mft_lft_fund.py."""
    import strategies36 as S36

    arr = ret.to_numpy(dtype=np.float64)
    tox = mm["micro_tox_score"].reindex(ret.index).ffill().fillna(0.3).to_numpy(dtype=np.float64)
    out = np.zeros(len(arr), dtype=np.float64)
    eq, peak = 1.0, 1.0
    v5_hist: list[float] = []

    for t in range(len(arr)):
        scale = 1.0
        if t >= 20:
            v20 = float(np.std(arr[t - 20 : t], ddof=1) * math.sqrt(365.0))
            scale = min(max_lev, target_vol / max(v20, 0.04))
        if t >= 10:
            v5 = float(np.std(arr[t - 5 : t], ddof=1) * math.sqrt(365.0))
            v5_hist.append(v5)
            if len(v5_hist) >= 30:
                med60 = float(np.median(v5_hist[-60:]))
                if med60 > 1e-4 and v5 > 2.75 * med60:
                    scale *= 0.70
        if t >= 1 and tox[t - 1] > 0.72:
            scale *= 0.75
        dm = S36.soft_taper(1.0 - eq / peak, dd_start=0.06, dd_full=0.18, floor=0.35)
        lev = min(max_lev, scale * dm)
        r = lev * arr[t] - max(0.0, lev - 1.0) * (borrow_apr / 365.0)
        out[t] = r
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)

    return pd.Series(out, index=ret.index, dtype=np.float64)


def benchmark_vs_baselines(save_json: bool = False) -> dict[str, Any]:
    """Benchmark HRP + Hurst + Conformal Conviction Alpha against mft_lft_fund & Q56.

    Evaluates full ``2020-2026`` and locked-box ``2024-2026`` OOS against BOTH
    ``baseline_mft_lft_1x`` (computed directly via ``mft_lft_fund.py``) and
    ``Q56_hybrid_1x`` (read from ``results/round56.json``, or computed directly
    if absent).
    """
    import fast_universal_alpha as FUA
    import lab
    import mft_lft_fund as MLF
    import monthly_reset_audit as MRA
    import run_round52 as R52
    import run_round53 as R53
    import yearly_goal_loop as YG

    here = os.path.dirname(os.path.abspath(__file__))
    r56_path = os.path.join(here, "results", "round56.json")

    # 1. Compute baseline_mft_lft directly (full series for OOS Calmar/DD + official pack)
    baseline_pack = MLF.run_fund_backtest()
    r_crypto_base, _ = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(["BTC", "ETH", "SOL"])
    mm = u_c["mm"]

    u_eq = YG.load_equities_universe(tickers=YG.MACRO_EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_eq_base_tr = MLF.simulate_lft_mft_sleeve(pre_eq, top_k=3, rebal_buffer=0.03)
    r_eq_base = r_eq_base_tr.copy()
    r_eq_base.index = r_eq_base.index.normalize()
    r_eq_base = r_eq_base.groupby(level=0).sum().reindex(r_crypto_base.index.normalize()).fillna(0.0).set_axis(r_crypto_base.index)
    r_blend_base, _ = R53.asymmetric_invvol_blend(r_crypto_base, r_eq_base, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_base_1x = MRA.apply_vol_tox_circuit_breaker(r_blend_base, mm, target_vol=0.0, max_lev=1.0)

    # 2. Load Q56_hybrid_1x from results/round56.json (or compute via run_round56 if missing)
    if os.path.exists(r56_path):
        with open(r56_path, "r", encoding="utf-8") as fh:
            r56_data = json.load(fh)
    else:
        import run_round56 as R56

        R56.main()
        with open(r56_path, "r", encoding="utf-8") as fh:
            r56_data = json.load(fh)

    q56_1x_pack = r56_data["variants"]["Q56_hybrid_1x"]
    q56_1p8x_pack = r56_data["variants"]["Q56_hybrid_scaled_1p8x"]

    # 3. Build HRP + Hurst Roughness + Pre-2024 Split-Conformal Alpha
    r_crypto_hrp, c_ret_df, btc_df, rate = _simulate_hrp_crypto_q51(hrp_window=60)
    idx = r_crypto_hrp.index

    r_eq_hrp_tr = _simulate_hrp_equities_sleeve(pre_eq, hrp_window=60, top_k=3, rebal_buffer=0.03)
    r_eq_hrp = r_eq_hrp_tr.copy()
    r_eq_hrp.index = r_eq_hrp.index.normalize()
    r_eq_hrp = r_eq_hrp.groupby(level=0).sum().reindex(idx.normalize()).fillna(0.0).set_axis(idx)

    r_blend_hrp, _ = R53.asymmetric_invvol_blend(r_crypto_hrp, r_eq_hrp, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_hrp_only_1x = MRA.apply_vol_tox_circuit_breaker(r_blend_hrp, mm, target_vol=0.0, max_lev=1.0)

    # Cross-asset causal Von Neumann spectral entropy z-score
    ca_eq = pre_eq["ret_df"].copy()
    ca_eq.index = ca_eq.index.normalize()
    ca_eq = ca_eq.groupby(level=0).sum().reindex(idx.normalize()).set_axis(idx)
    ca_df = pd.concat([c_ret_df, ca_eq], axis=1).fillna(0.0)
    vn_df = _causal_spectral_entropy_z(ca_df, window=30, z_window=60)

    # Causal rolling Hurst variogram on cumulative HRP blend index
    port_px = (1.0 + r_blend_hrp).cumprod()
    hurst_port = rolling_hurst_variogram(port_px, window=64).fillna(0.5)

    # Causal composite trend_strength (BTC SuperSmoother + HRP portfolio SuperSmoother velocity)
    ss_btc = FUA.fast_supersmoother_signal(
        btc_df["o"].to_numpy(dtype=np.float64),
        btc_df["h"].to_numpy(dtype=np.float64),
        btc_df["l"].to_numpy(dtype=np.float64),
        btc_df["c"].to_numpy(dtype=np.float64),
    )
    tr_btc = pd.Series(ss_btc["trend_pos"], index=idx, dtype=np.float64)

    p_arr = port_px.to_numpy(dtype=np.float64)
    ss_p = FUA.supersmoother_2pole(p_arr, period=15.0)
    ss_p_lag = np.empty_like(ss_p)
    ss_p_lag[0] = ss_p[0]
    if len(ss_p) > 1:
        ss_p_lag[1:] = ss_p[:-1]
    vel_p = np.zeros_like(p_arr)
    if len(p_arr) > 5:
        vel_p[5:] = (ss_p_lag[5:] - ss_p_lag[:-5]) / np.maximum(ss_p_lag[:-5], 1e-12)
    vol_p = (
        pd.Series(r_blend_hrp, index=idx)
        .rolling(20, min_periods=10)
        .std()
        .shift(1)
        .fillna(0.005)
        .clip(lower=1e-4)
        .to_numpy(dtype=np.float64)
    )
    tr_port = pd.Series(np.clip(0.5 + 0.5 * np.tanh(vel_p / (vol_p * math.sqrt(5.0))), 0.0, 1.0), index=idx)
    trend_strength = 0.5 * tr_btc + 0.5 * tr_port

    idle_rate_daily = pd.Series(rate.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=np.float64) / 100.0 / 365.0, index=idx)

    gate_mult, gate_diag = conformal_conviction_gate(
        hurst=hurst_port,
        entropy=vn_df["vn_entropy_z"],
        trend_strength=trend_strength,
        returns=r_hrp_only_1x,
        idle_rate=idle_rate_daily,
        calibration_cutoff=DEFAULT_CALIBRATION_CUTOFF,
        alpha=0.15,
        horizon_days=5,
        gate_floor=0.45,
        trend_preserve=0.58,
    )

    idle_r_lag = idle_rate_daily.shift(1).fillna(0.0)
    r_hrp_hurst_1x = gate_mult * r_hrp_only_1x + (1.0 - gate_mult) * idle_r_lag
    r_hrp_hurst_1p8x = _scale_with_borrow(r_hrp_hurst_1x, mm, target_vol=0.16, max_lev=1.8, borrow_apr=0.055)

    n_trials = int(r56_data.get("n_trials", 885))

    def _pack(s: pd.Series) -> dict[str, Any]:
        st = YG.evaluate_by_year(s)
        oos_s = s[s.index >= pd.Timestamp(DEFAULT_CALIBRATION_CUTOFF, tz="UTC")]
        oos = YG.evaluate_by_year(oos_s)
        mo = MRA.monthly_reset_report(s)
        oos_mo = MRA.monthly_reset_report(oos_s)
        return {
            "cagr_pct": st["cagr_pct"],
            "max_dd_pct": st["max_dd_pct"],
            "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
            "sharpe": st["sharpe"],
            "deflated_sharpe": round(float(lab.deflated_sharpe(s, n_trials)), 4),
            "y2022_pct": st["years"]["2022"]["ann_pct"],
            "y2026_pct": st["years"]["2026"]["ann_pct"],
            "monthly_win_rate_pct": mo["win_rate_pct"],
            "oos_2024_2026_cagr_pct": oos["cagr_pct"],
            "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
            "oos_2024_2026_calmar": round(oos["cagr_pct"] / max(0.01, oos["max_dd_pct"]), 3),
            "oos_2024_2026_sharpe": oos["sharpe"],
            "oos_2024_2026_monthly_win_rate_pct": oos_mo["win_rate_pct"],
        }

    base_1x_full = _pack(r_base_1x)
    # Keep official round56/mft_lft_fund fields exact while adding OOS DD/Calmar
    base_1x_merged = {**base_1x_full, **baseline_pack["fund_1x_spot"]}
    hrp_only_pack = _pack(r_hrp_only_1x)
    hrp_hurst_1x_pack = _pack(r_hrp_hurst_1x)
    hrp_hurst_1p8x_pack = _pack(r_hrp_hurst_1p8x)

    comparisons = {
        "1x_vs_baseline_mft_lft_1x": {
            "cagr_diff_pct": round(hrp_hurst_1x_pack["cagr_pct"] - base_1x_merged["cagr_pct"], 2),
            "max_dd_reduction_pct": round(base_1x_merged["max_dd_pct"] - hrp_hurst_1x_pack["max_dd_pct"], 2),
            "calmar_gt_baseline": hrp_hurst_1x_pack["calmar"] > base_1x_merged["calmar"],
            "sharpe_gt_baseline": hrp_hurst_1x_pack["sharpe"] > base_1x_merged["sharpe"],
            "oos_sharpe_gt_baseline": hrp_hurst_1x_pack["oos_2024_2026_sharpe"] > base_1x_merged["oos_2024_2026_sharpe"],
            "oos_calmar_gt_baseline": hrp_hurst_1x_pack["oos_2024_2026_calmar"] > base_1x_merged["oos_2024_2026_calmar"],
        },
        "1x_vs_Q56_hybrid_1x": {
            "cagr_diff_pct": round(hrp_hurst_1x_pack["cagr_pct"] - q56_1x_pack["cagr_pct"], 2),
            "calmar_diff": round(hrp_hurst_1x_pack["calmar"] - q56_1x_pack["calmar"], 3),
            "sharpe_diff": round(hrp_hurst_1x_pack["sharpe"] - q56_1x_pack["sharpe"], 3),
            "oos_cagr_diff_pct": round(
                hrp_hurst_1x_pack["oos_2024_2026_cagr_pct"] - q56_1x_pack["oos_2024_2026_cagr_pct"], 2
            ),
            "cagr_gt_q56": hrp_hurst_1x_pack["cagr_pct"] > q56_1x_pack["cagr_pct"],
            "oos_cagr_gt_q56": hrp_hurst_1x_pack["oos_2024_2026_cagr_pct"] > q56_1x_pack["oos_2024_2026_cagr_pct"],
            "calmar_gt_q56": hrp_hurst_1x_pack["calmar"] > q56_1x_pack["calmar"],
            "sharpe_gt_q56": hrp_hurst_1x_pack["sharpe"] > q56_1x_pack["sharpe"],
        },
    }

    res = {
        "module": "hrp_hurst_alpha",
        "calibration_cutoff": DEFAULT_CALIBRATION_CUTOFF,
        "baselines": {
            "baseline_mft_lft_1x": base_1x_merged,
            "baseline_mft_lft_1p8x": baseline_pack["fund_1p8x_borrow_adj"],
            "Q56_hybrid_1x": q56_1x_pack,
            "Q56_hybrid_scaled_1p8x": q56_1p8x_pack,
        },
        "variants": {
            "hrp_only_1x": hrp_only_pack,
            "hrp_hurst_conformal_1x": hrp_hurst_1x_pack,
            "hrp_hurst_conformal_1p8x": hrp_hurst_1p8x_pack,
        },
        "conformal_diagnostics": gate_diag,
        "comparisons": comparisons,
    }

    if save_json:
        out_path = os.path.join(here, "results", "hrp_hurst_alpha.json")
        os.makedirs(os.path.dirname(out_path), exist_ok=True)
        with open(out_path, "w", encoding="utf-8") as fh:
            json.dump(res, fh, indent=2)

    return res


if __name__ == "__main__":
    print(json.dumps(benchmark_vs_baselines(save_json=True), indent=2))
