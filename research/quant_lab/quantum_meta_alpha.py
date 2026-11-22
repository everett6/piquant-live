"""Quantum-Inspired Von Neumann Spectral Entropy & Meta-Labeling Precision Gate.

Strictly causal (t-1 -> t, zero lookahead, no pyarrow at import) alpha & risk-gating module.

Academic Foundations:
  1. Felippe, H., Viol, A., de Araujo, D. B., da Luz, M. G. E., Palhano-Fontes, F.,
     Onias, H., Raposo, E. P., & Viswanathan, G. M. (2023).
     "Threshold-free estimation of entropy from a Pearson matrix."
     EPL (Europhysics Letters), 141(3), 31003 (arXiv:2106.05379).
     -> Rescaled Pearson correlation matrix rho = C / N as a quantum density operator;
        Von Neumann spectral entropy S_vN(rho) = -Tr(rho ln rho) / ln(N) detects
        correlation compression and systemic crisis clustering without thresholding.
  2. Billio, M., Getmansky, M., Lo, A. W., & Pelizzon, L. (2012).
     "Econometric measures of systemic risk in the finance and insurance sectors."
     Journal of Financial Economics, 104(3), 535-559.
     -> Eigenvalue spectrum concentration (absorption ratio / purity) for regime risk.
  3. Lopez de Prado, M. (2018).
     Advances in Financial Machine Learning, Wiley (Ch. 3, Sec. 3.6: "Meta-Labeling").
     & Meyer, M., Joubert, J. F., & Alfeus, M. (2022-2023),
     "Meta-Labeling: Model Architectures / Calibration and Position Sizing,"
     The Journal of Financial Data Science.
     -> Secondary ML precision gatekeeper trained to filter out low-probability
        primary trades, maximizing per-trade win rate and Calmar ratio.
  4. Avellaneda, M., & Lee, J.-H. (2010).
     "Statistical arbitrage in the U.S. equities market."
     Quantitative Finance, 10(7), 761-782.
     -> Ornstein-Uhlenbeck (OU) AR(1) residual modeling and standardized s-score
        for mean-reversion quality scoring.
"""
from __future__ import annotations

import math
from dataclasses import dataclass
from typing import TYPE_CHECKING, Any

import numpy as np

if TYPE_CHECKING:
    import pandas as pd
    from sklearn.pipeline import Pipeline

DEFAULT_TRAIN_START = "2020-01-01"
DEFAULT_TRAIN_END = "2023-12-31"
OOS_START = "2024-01-01"


def _density_matrix_entropy_and_purity(corr: np.ndarray, eps: float = 1e-12) -> tuple[float, float]:
    """Compute normalized Von Neumann entropy S_vN in [0, 1] and purity Tr(rho^2) of rho = C / N."""
    n = corr.shape[0]
    if n <= 1:
        return 1.0, 1.0
    sym = 0.5 * (corr + corr.T)
    eigvals = np.linalg.eigvalsh(sym)
    eigvals = np.clip(eigvals, eps, None)
    # Normalize to density matrix spectrum: sum(lambda_i) = 1 (equivalent to rho = C / Tr(C))
    probs = eigvals / float(np.sum(eigvals))
    entropy = -float(np.sum(probs * np.log(probs))) / math.log(float(n))
    purity = float(np.sum(probs * probs))
    return float(np.clip(entropy, 0.0, 1.0)), float(np.clip(purity, 1.0 / n, 1.0))


def quantum_spectral_entropy(
    ret_df: pd.DataFrame,
    window: int = 30,
    z_window: int = 60,
    min_periods: int = 15,
    eps: float = 1e-12,
) -> pd.DataFrame:
    """Strictly causal (t-1 -> t) rolling Von Neumann Spectral Entropy over cross-asset returns.

    At bar t, the correlation matrix is estimated strictly from ret_df.iloc[max(0, t-window) : t],
    so no return at bar t or later is ever touched.
    """
    import pandas as pd

    n_rows, n_cols = ret_df.shape
    idx = ret_df.index
    arr = ret_df.to_numpy(dtype=float)

    vn_ent = np.full(n_rows, np.nan, dtype=float)
    vn_pur = np.full(n_rows, np.nan, dtype=float)

    for t in range(min_periods, n_rows):
        start = max(0, t - window)
        sub = arr[start:t]
        # Drop columns with zero variance or too many NaNs in this causal window
        valid_cols = []
        for j in range(n_cols):
            col = sub[:, j]
            finite = col[np.isfinite(col)]
            if len(finite) >= min_periods and np.std(finite) > 1e-10:
                valid_cols.append(j)
        if len(valid_cols) < 2:
            vn_ent[t] = 1.0
            vn_pur[t] = 1.0 / max(1, n_cols)
            continue
        sub_v = np.nan_to_num(sub[:, valid_cols], nan=0.0)
        corr = np.corrcoef(sub_v, rowvar=False)
        corr = np.nan_to_num(corr, nan=0.0)
        np.fill_diagonal(corr, 1.0)
        ent, pur = _density_matrix_entropy_and_purity(corr, eps=eps)
        vn_ent[t] = ent
        vn_pur[t] = pur

    ent_s = pd.Series(vn_ent, index=idx, dtype=float)
    pur_s = pd.Series(vn_pur, index=idx, dtype=float)

    # Causal rolling z-score of entropy (already computed from t-window..t-1, so no extra shift needed)
    roll_mean = ent_s.rolling(z_window, min_periods=15).mean()
    roll_std = ent_s.rolling(z_window, min_periods=15).std().clip(lower=1e-4)
    ent_z = ((ent_s - roll_mean) / roll_std).fillna(0.0)
    ent_ema_fast = ent_s.ewm(span=10, min_periods=5).mean()
    ent_ema_slow = ent_s.ewm(span=40, min_periods=10).mean()
    ent_trend = (ent_ema_fast - ent_ema_slow).fillna(0.0)

    return pd.DataFrame(
        {
            "vn_entropy": ent_s.ffill().fillna(1.0),
            "vn_purity": pur_s.ffill().fillna(1.0 / max(1, n_cols)),
            "vn_entropy_z": ent_z,
            "vn_entropy_trend": ent_trend,
        },
        index=idx,
    )


def quantum_regime_scale(
    vn_df: pd.DataFrame,
    z_low: float = -1.75,
    z_high: float = 0.25,
    floor: float = 0.35,
) -> pd.Series:
    """Map causal Von Neumann entropy z-score to a smooth risk multiplier in [floor, 1.0].

    Sharp drops in spectral entropy (vn_entropy_z < z_high) signal correlation compression
    and crisis clustering; exposure smoothly tapers down to `floor` at `z_low`.
    """
    import pandas as pd

    z = vn_df["vn_entropy_z"].to_numpy(dtype=float)
    span = max(1e-6, z_high - z_low)
    frac = np.clip((z - z_low) / span, 0.0, 1.0)
    scale = floor + (1.0 - floor) * frac
    return pd.Series(scale, index=vn_df.index, dtype=float)


def compute_ou_residual_features(
    ret_series: pd.Series,
    window: int = 60,
    min_periods: int = 25,
) -> pd.DataFrame:
    """Strictly causal (t-1 -> t) Ornstein-Uhlenbeck AR(1) residual features (Avellaneda & Lee 2010).

    At bar t, uses only ret_series.iloc[max(0, t-window) : t].
    Models cumulative demeaned return X_k as dX = theta*(mu - X)dt + sigma dW via AR(1) fit.
    """
    import pandas as pd

    arr = ret_series.to_numpy(dtype=float)
    n = len(arr)
    idx = ret_series.index

    s_score = np.zeros(n, dtype=float)
    theta_arr = np.zeros(n, dtype=float)
    eq_vol_arr = np.zeros(n, dtype=float)

    for t in range(min_periods, n):
        start = max(0, t - window)
        r_win = np.nan_to_num(arr[start:t], nan=0.0)
        # Cumulative demeaned residual spread
        eps_r = r_win - float(np.mean(r_win))
        x = np.cumsum(eps_r)
        if len(x) < 10 or np.std(x) < 1e-10:
            continue
        x_lag = x[:-1]
        x_cur = x[1:]
        var_lag = float(np.var(x_lag))
        if var_lag < 1e-12:
            continue
        cov = float(np.mean((x_lag - np.mean(x_lag)) * (x_cur - np.mean(x_cur))))
        b = float(np.clip(cov / var_lag, 1e-4, 0.995))
        a = float(np.mean(x_cur) - b * np.mean(x_lag))
        resid = x_cur - (a + b * x_lag)
        sigma_eta = float(np.std(resid))
        if sigma_eta < 1e-10:
            continue
        mu_eq = a / (1.0 - b)
        sigma_eq = sigma_eta / math.sqrt(max(1e-8, 1.0 - b * b))
        theta = -math.log(b)
        s_score[t] = float(np.clip(-(x[-1] - mu_eq) / max(1e-8, sigma_eq), -5.0, 5.0))
        theta_arr[t] = theta
        eq_vol_arr[t] = sigma_eq

    return pd.DataFrame(
        {
            "ou_s_score": s_score,
            "ou_theta": theta_arr,
            "ou_eq_vol": eq_vol_arr,
        },
        index=idx,
    )


def build_causal_meta_features(
    primary_ret: pd.Series,
    vn_df: pd.DataFrame,
    tox_series: pd.Series | None = None,
    ou_window: int = 60,
) -> pd.DataFrame:
    """Assemble strictly causal (t-1 -> t) feature matrix for Meta-Labeling."""
    import pandas as pd

    idx = primary_ret.index
    r = primary_ret.fillna(0.0)

    # All rolling stats shifted by 1 so row t uses only returns <= t-1
    r_shift = r.shift(1).fillna(0.0)
    ret_5d = r_shift.rolling(5, min_periods=1).sum()
    ret_20d = r_shift.rolling(20, min_periods=5).sum()
    vol_5d = r_shift.rolling(5, min_periods=3).std().fillna(0.0)
    vol_20d = r_shift.rolling(20, min_periods=10).std().fillna(0.0)
    vol_ratio = (vol_5d / vol_20d.clip(lower=1e-5)).clip(0.0, 5.0).fillna(1.0)

    # Causal running drawdown up to t-1
    eq_lag = (1.0 + r_shift).cumprod()
    peak_lag = eq_lag.cummax()
    dd_lag = (1.0 - eq_lag / peak_lag.clip(lower=1e-12)).fillna(0.0)

    ou_df = compute_ou_residual_features(r, window=ou_window)
    vn_aligned = vn_df.reindex(idx).ffill().fillna(0.0)

    if tox_series is not None:
        tox_lag = tox_series.reindex(idx).ffill().shift(1).fillna(0.3)
    else:
        tox_lag = pd.Series(0.3, index=idx, dtype=float)

    feats = pd.DataFrame(
        {
            "vn_entropy": vn_aligned["vn_entropy"],
            "vn_purity": vn_aligned["vn_purity"],
            "vn_entropy_z": vn_aligned["vn_entropy_z"],
            "vn_entropy_trend": vn_aligned["vn_entropy_trend"],
            "ou_s_score": ou_df["ou_s_score"],
            "ou_theta": ou_df["ou_theta"],
            "ou_eq_vol": ou_df["ou_eq_vol"],
            "ret_5d": ret_5d,
            "ret_20d": ret_20d,
            "vol_5d": vol_5d,
            "vol_20d": vol_20d,
            "vol_ratio": vol_ratio,
            "dd_lag": dd_lag,
            "tox_lag": tox_lag,
        },
        index=idx,
    )
    return feats.replace([np.inf, -np.inf], np.nan).fillna(0.0)


@dataclass
class MetaLabelModel:
    """Frozen pre-2024 Meta-Labeling classifier."""

    pipeline: Pipeline
    feature_names: list[str]
    train_start: str
    train_end: str
    prob_threshold: float
    horizon_days: int
    n_train_samples: int
    train_positive_rate: float


def fit_meta_label_model(
    primary_ret: pd.Series,
    features: pd.DataFrame,
    train_start: str = DEFAULT_TRAIN_START,
    train_end: str = DEFAULT_TRAIN_END,
    horizon_days: int = 5,
    cost_hurdle: float = 0.0,
    prob_threshold: float = 0.52,
    c_reg: float = 0.25,
) -> MetaLabelModel:
    """Fit Meta-Labeling classifier strictly on [train_start, train_end] with zero OOS leakage.

    Ensures both feature timestamp t and forward-label end timestamp (t + horizon_days - 1)
    are <= train_end so no 2024+ return ever enters training.
    """
    import pandas as pd
    from sklearn.linear_model import LogisticRegression
    from sklearn.pipeline import Pipeline
    from sklearn.preprocessing import StandardScaler

    t0 = pd.Timestamp(train_start, tz="UTC") if primary_ret.index.tz is not None else pd.Timestamp(train_start)
    t1 = pd.Timestamp(train_end, tz="UTC") if primary_ret.index.tz is not None else pd.Timestamp(train_end)

    # Forward horizon cumulative return starting at bar t: prod_{k=0..h-1} (1 + r[t+k]) - 1
    arr = primary_ret.to_numpy(dtype=float)
    n = len(arr)
    fwd_ret = np.full(n, np.nan, dtype=float)
    for t in range(n - horizon_days + 1):
        fwd_ret[t] = float(np.prod(1.0 + arr[t : t + horizon_days]) - 1.0)

    idx = primary_ret.index
    # Strict boundary: label end index t + horizon_days - 1 must be <= t1
    valid_end = np.zeros(n, dtype=bool)
    for t in range(n - horizon_days + 1):
        if idx[t] >= t0 and idx[t + horizon_days - 1] <= t1:
            valid_end[t] = True

    # Also skip initial warmup rows (first 60 bars) where rolling stats are cold
    warmup = np.arange(n) >= 60
    mask = valid_end & warmup & np.isfinite(fwd_ret)

    X_train = features.iloc[mask].to_numpy(dtype=float)
    y_train = (fwd_ret[mask] > cost_hurdle).astype(int)

    pipe = Pipeline([
        ("scaler", StandardScaler()),
        ("clf", LogisticRegression(C=c_reg, max_iter=1000, random_state=42)),
    ])
    pipe.fit(X_train, y_train)

    return MetaLabelModel(
        pipeline=pipe,
        feature_names=list(features.columns),
        train_start=train_start,
        train_end=train_end,
        prob_threshold=prob_threshold,
        horizon_days=horizon_days,
        n_train_samples=int(len(y_train)),
        train_positive_rate=float(np.mean(y_train)),
    )


def meta_label_precision_gate(
    primary_ret: pd.Series,
    cross_asset_ret_df: pd.DataFrame,
    tox_series: pd.Series | None = None,
    idle_rate_series: pd.Series | None = None,
    train_start: str = DEFAULT_TRAIN_START,
    train_end: str = DEFAULT_TRAIN_END,
    horizon_days: int = 5,
    prob_threshold: float = 0.52,
    entropy_window: int = 30,
    z_low: float = -1.75,
    z_high: float = 0.25,
    entropy_floor: float = 0.35,
    gate_floor: float = 0.0,
    soft_sizing: bool = True,
) -> dict[str, Any]:
    """Apply Quantum Spectral Entropy regime filter + frozen pre-2024 Meta-Labeling precision gate.

    Returns dict with:
      - gated_ret: pd.Series of gated portfolio returns (earning idle_rate on suppressed capital)
      - probs: pd.Series of calibrated meta-label probabilities P(win | F_{t-1})
      - gate_weight: pd.Series of effective position multiplier in [0, 1]
      - vn_df: DataFrame of causal Von Neumann spectral entropy metrics
      - features: DataFrame of causal features
      - model: frozen MetaLabelModel
    """
    import pandas as pd

    idx = primary_ret.index
    # Align cross-asset returns to primary_ret index (ffill=0 on non-trading days)
    ca_aligned = cross_asset_ret_df.reindex(idx).fillna(0.0)
    vn_df = quantum_spectral_entropy(ca_aligned, window=entropy_window)
    reg_scale = quantum_regime_scale(vn_df, z_low=z_low, z_high=z_high, floor=entropy_floor)

    features = build_causal_meta_features(primary_ret, vn_df, tox_series=tox_series)
    model = fit_meta_label_model(
        primary_ret=primary_ret,
        features=features,
        train_start=train_start,
        train_end=train_end,
        horizon_days=horizon_days,
        prob_threshold=prob_threshold,
    )

    probs_arr = model.pipeline.predict_proba(features[model.feature_names].to_numpy(dtype=float))[:, 1]
    probs = pd.Series(probs_arr, index=idx, dtype=float)

    # Precision gate: suppress low-probability trades below prob_threshold
    if soft_sizing:
        # Lopez de Prado confidence scaling above threshold, 0 (or gate_floor) below threshold
        span = max(1e-4, 1.0 - prob_threshold)
        raw_gate = np.where(
            probs_arr >= prob_threshold,
            0.55 + 0.45 * np.clip((probs_arr - prob_threshold) / span, 0.0, 1.0),
            gate_floor,
        )
    else:
        raw_gate = np.where(probs_arr >= prob_threshold, 1.0, gate_floor)

    eff_weight = pd.Series(np.clip(raw_gate * reg_scale.to_numpy(dtype=float), 0.0, 1.0), index=idx, dtype=float)

    if idle_rate_series is not None:
        idle_r = idle_rate_series.reindex(idx).ffill().shift(1).fillna(0.0).to_numpy(dtype=float)
    else:
        idle_r = np.zeros(len(idx), dtype=float)

    w_arr = eff_weight.to_numpy(dtype=float)
    p_arr = primary_ret.to_numpy(dtype=float)
    gated_arr = w_arr * p_arr + (1.0 - w_arr) * idle_r
    gated_ret = pd.Series(gated_arr, index=idx, dtype=float)

    return {
        "gated_ret": gated_ret,
        "probs": probs,
        "gate_weight": eff_weight,
        "regime_scale": reg_scale,
        "vn_df": vn_df,
        "features": features,
        "model": model,
    }


def benchmark_mft_lft_fund(prob_threshold: float = 0.54) -> dict[str, Any]:
    """Benchmark Quantum Spectral Entropy + Meta-Labeling Precision Gate on mft_lft_fund.py."""
    import pandas as pd

    import mft_lft_fund as MLF
    import monthly_reset_audit as MRA
    import run_round52 as R52
    import run_round53 as R53
    import yearly_goal_loop as YG

    r_crypto, _ = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(["BTC", "ETH", "SOL"])
    mm = u_c["mm"]

    u_eq = YG.load_equities_universe(tickers=YG.MACRO_EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_eq_tr = MLF.simulate_lft_mft_sleeve(pre_eq, top_k=3, rebal_buffer=0.03)
    r_eq = r_eq_tr.copy()
    r_eq.index = r_eq.index.normalize()
    r_eq = r_eq.groupby(level=0).sum().reindex(r_crypto.index.normalize()).fillna(0.0).set_axis(r_crypto.index)

    r_blend, _ = R53.asymmetric_invvol_blend(r_crypto, r_eq, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_base_1x = MRA.apply_vol_tox_circuit_breaker(r_blend, mm, target_vol=0.0, max_lev=1.0)

    ca_crypto = pd.DataFrame({c: u_c["daily"][c]["c"].pct_change() for c in ["BTC", "ETH", "SOL"]}).reindex(r_crypto.index)
    ca_eq = pre_eq["ret_df"].copy()
    ca_eq.index = ca_eq.index.normalize()
    ca_eq = ca_eq.groupby(level=0).sum().reindex(r_crypto.index.normalize()).set_axis(r_crypto.index)
    ca_df = pd.concat([ca_crypto, ca_eq], axis=1).fillna(0.0)
    idle_rate = pd.Series(u_c["rate"].to_numpy(dtype=float) / 100.0 / 365.0, index=r_crypto.index)

    res = meta_label_precision_gate(
        r_base_1x,
        ca_df,
        tox_series=mm["micro_tox_score"],
        idle_rate_series=idle_rate,
        prob_threshold=prob_threshold,
        entropy_floor=0.40,
        gate_floor=0.0,
        soft_sizing=True,
    )

    def _pack(s: pd.Series, gw: pd.Series | None = None) -> dict[str, Any]:
        st = YG.evaluate_by_year(s)
        oos_s = s[s.index >= pd.Timestamp(OOS_START, tz="UTC")]
        oos = YG.evaluate_by_year(oos_s)
        m_full = MRA.monthly_reset_report(s)
        m_oos = MRA.monthly_reset_report(oos_s)
        active = s[gw > 0.0] if gw is not None else s
        active_oos = oos_s[gw.loc[oos_s.index] > 0.0] if gw is not None else oos_s
        return {
            "cagr_pct": st["cagr_pct"],
            "max_dd_pct": st["max_dd_pct"],
            "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
            "sharpe": st["sharpe"],
            "y2022_pct": st["years"]["2022"]["ann_pct"],
            "y2026_pct": st["years"]["2026"]["ann_pct"],
            "monthly_win_rate_pct": m_full["win_rate_pct"],
            "active_day_win_rate_pct": round(float((active > 0.0).mean() * 100.0), 2),
            "active_days": int(len(active)),
            "oos_2024_2026_cagr_pct": oos["cagr_pct"],
            "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
            "oos_2024_2026_calmar": round(oos["cagr_pct"] / max(0.01, oos["max_dd_pct"]), 3),
            "oos_2024_2026_sharpe": oos["sharpe"],
            "oos_monthly_win_rate_pct": m_oos["win_rate_pct"],
            "oos_active_day_win_rate_pct": round(float((active_oos > 0.0).mean() * 100.0), 2),
            "oos_active_days": int(len(active_oos)),
        }

    return {
        "baseline_1x_spot": _pack(r_base_1x),
        f"qma_1x_spot_t{prob_threshold}": _pack(res["gated_ret"], res["gate_weight"]),
        "n_train_samples": res["model"].n_train_samples,
        "train_end": res["model"].train_end,
    }


if __name__ == "__main__":
    import json

    print(json.dumps(benchmark_mft_lft_fund(prob_threshold=0.54), indent=2))
