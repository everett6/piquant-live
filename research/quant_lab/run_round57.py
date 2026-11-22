"""PREREG57: Synthesize Subagent A (HRP + Hurst + Conformal) & Subagent B (AdaHedge + Yang-Zhang + Kalman).

Compares:
  - baseline_mft_lft_1x / Q56_hybrid_1x
  - Q57_hrp_conformal_1x (Subagent A standalone)
  - Q57_ada_hrp_unclipped_1x (HRP + AdaHedge + YZ/Kalman brake without conformal gate)
  - Q57_synth_1x (Full synthesis: HRP + AdaHedge + YZ/Kalman brake + Pre-2024 Hurst/Conformal gate)
  - Q57_synth_scaled_1p8x (16% target vol, 1.8x cap, net of 5.5%/yr borrow on >1.0x)
Writes results/round57.json.
"""
from __future__ import annotations

import json
import math
import os
from typing import Any

import numpy as np
import pandas as pd

import fast_universal_alpha as FUA
import hrp_hurst_alpha as HHA
import kalman_yz_alpha as KYZ
import lab
import mft_lft_fund as MLF
import monthly_reset_audit as MRA
import run_round52 as R52
import yearly_goal_loop as YG

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_PATH = os.path.join(HERE, "results", "round57.json")
N_TRIALS = 900


def build_q57_series(
    calibration_cutoff: str = HHA.DEFAULT_CALIBRATION_CUTOFF,
) -> dict[str, Any]:
    """Build causal Round 57 return series and diagnostics (strict pre-2024 calibration)."""
    u_c = YG.load_universe(["BTC", "ETH", "SOL"])
    mm = u_c["mm"]

    # Subagent A: HRP inside Crypto (Q51) & US Equities/Macro (12 tickers)
    r_crypto_hrp, c_ret_df, btc_df, rate = HHA._simulate_hrp_crypto_q51(hrp_window=60)
    idx = r_crypto_hrp.index

    u_eq = YG.load_equities_universe(tickers=YG.MACRO_EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_eq_hrp_tr = HHA._simulate_hrp_equities_sleeve(pre_eq, hrp_window=60, top_k=3, rebal_buffer=0.03)
    r_eq_hrp = r_eq_hrp_tr.copy()
    r_eq_hrp.index = r_eq_hrp.index.normalize()
    r_eq_hrp = r_eq_hrp.groupby(level=0).sum().reindex(idx.normalize()).fillna(0.0).set_axis(idx)

    spy_df = u_eq["daily"]["SPY"].reindex(idx.normalize()).ffill().set_axis(idx)
    idle_daily = pd.Series(
        rate.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=np.float64) / 100.0 / 365.0,
        index=idx,
    )
    idle_lag = idle_daily.shift(1).fillna(0.0)

    # Subagent B: Yang-Zhang Gap-Vol + Kalman Velocity Crash Brake + AdaHedge Cross-Sleeve Allocator
    b_btc = KYZ.kalman_yz_regime_scale(
        btc_df["o"], btc_df["h"], btc_df["l"], btc_df["c"],
        annual_days=365, yz_fast_window=5, yz_slow_window=30,
        vol_ratio_thresh=2.10, vel_z_thresh=-1.00, brake_floor=0.55,
    )
    b_spy = KYZ.kalman_yz_regime_scale(
        spy_df["o"], spy_df["h"], spy_df["l"], spy_df["c"],
        annual_days=252, yz_fast_window=5, yz_slow_window=30,
        vol_ratio_thresh=2.10, vel_z_thresh=-0.80, brake_floor=0.65,
    )
    p_brake = 0.70 + 0.30 * np.minimum(b_btc, b_spy)

    ret_mat = np.column_stack([r_crypto_hrp.to_numpy(dtype=np.float64), r_eq_hrp.to_numpy(dtype=np.float64)])
    w_ada = KYZ.adahedge_weights(ret_mat, min_w=0.20, max_w=0.80, rebal_buffer=0.02, decay=0.97)
    r_ada_raw = pd.Series(np.sum(w_ada * ret_mat, axis=1), index=idx)
    r_ada_braked = r_ada_raw * p_brake + (1.0 - p_brake) * idle_lag
    r_ada_hrp_1x = MRA.apply_vol_tox_circuit_breaker(r_ada_braked, mm, target_vol=0.0, max_lev=1.0)

    # Subagent A: Causal Spectral Entropy + Rolling Hurst Variogram + Pre-2024 Conformal Conviction Gate
    ca_eq = pre_eq["ret_df"].copy()
    ca_eq.index = ca_eq.index.normalize()
    ca_eq = ca_eq.groupby(level=0).sum().reindex(idx.normalize()).set_axis(idx)
    ca_df = pd.concat([c_ret_df, ca_eq], axis=1).fillna(0.0)
    vn_df = HHA._causal_spectral_entropy_z(ca_df, window=30, z_window=60)

    port_px = (1.0 + r_ada_raw).cumprod()
    hurst_port = HHA.rolling_hurst_variogram(port_px, window=64).fillna(0.5)

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
        r_ada_raw.rolling(20, min_periods=10).std().shift(1).fillna(0.005).clip(lower=1e-4).to_numpy(dtype=np.float64)
    )
    tr_port = pd.Series(np.clip(0.5 + 0.5 * np.tanh(vel_p / (vol_p * math.sqrt(5.0))), 0.0, 1.0), index=idx)
    trend_strength = 0.5 * tr_btc + 0.5 * tr_port

    gate_mult, gate_diag = HHA.conformal_conviction_gate(
        hurst=hurst_port,
        entropy=vn_df["vn_entropy_z"],
        trend_strength=trend_strength,
        returns=r_ada_hrp_1x,
        idle_rate=idle_daily,
        calibration_cutoff=calibration_cutoff,
        alpha=0.15,
        horizon_days=5,
        gate_floor=0.45,
        trend_preserve=0.58,
    )

    r_synth_1x = gate_mult * r_ada_hrp_1x + (1.0 - gate_mult) * idle_lag
    r_synth_1p8x = KYZ._vol_target_with_borrow(r_synth_1x, idle_lag, mm, target_vol=0.16, max_lev=1.8, borrow_apr=0.055)

    return {
        "r_ada_hrp_1x": r_ada_hrp_1x,
        "r_synth_1x": r_synth_1x,
        "r_synth_1p8x": r_synth_1p8x,
        "gate_diag": gate_diag,
        "mean_ada_crypto_weight": round(float(w_ada[:, 0].mean()), 4),
    }


def _pack(s: pd.Series) -> dict[str, Any]:
    st = YG.evaluate_by_year(s)
    oos_s = s[s.index >= pd.Timestamp(HHA.DEFAULT_CALIBRATION_CUTOFF, tz="UTC")]
    oos = YG.evaluate_by_year(oos_s)
    mo = MRA.monthly_reset_report(s)
    oos_mo = MRA.monthly_reset_report(oos_s)
    return {
        "cagr_pct": st["cagr_pct"],
        "max_dd_pct": st["max_dd_pct"],
        "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
        "sharpe": st["sharpe"],
        "deflated_sharpe": round(float(lab.deflated_sharpe(s, N_TRIALS)), 4),
        "y2022_pct": st["years"]["2022"]["ann_pct"],
        "y2026_pct": st["years"]["2026"]["ann_pct"],
        "monthly_win_rate_pct": mo["win_rate_pct"],
        "oos_2024_2026_cagr_pct": oos["cagr_pct"],
        "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
        "oos_2024_2026_calmar": round(oos["cagr_pct"] / max(0.01, oos["max_dd_pct"]), 3),
        "oos_2024_2026_sharpe": oos["sharpe"],
        "oos_monthly_win_rate_pct": oos_mo["win_rate_pct"],
    }


def main() -> None:
    baseline = MLF.run_fund_backtest()
    r56_path = os.path.join(HERE, "results", "round56.json")
    with open(r56_path, "r", encoding="utf-8") as fh:
        r56 = json.load(fh)
    hha_bench = HHA.benchmark_vs_baselines(save_json=False)
    q57 = build_q57_series()

    variants = {
        "baseline_mft_lft_1x": baseline["fund_1x_spot"],
        "Q56_hybrid_1x": r56["variants"]["Q56_hybrid_1x"],
        "Q57_hrp_conformal_1x": hha_bench["variants"]["hrp_hurst_conformal_1x"],
        "Q57_ada_hrp_unclipped_1x": _pack(q57["r_ada_hrp_1x"]),
        "Q57_synth_1x": _pack(q57["r_synth_1x"]),
        "Q57_synth_scaled_1p8x": _pack(q57["r_synth_1p8x"]),
    }

    s1 = variants["Q57_synth_1x"]
    checks = {
        "1_cagr_ge_14pct": s1["cagr_pct"] >= 14.0,
        "2_max_dd_lt_6p5pct": s1["max_dd_pct"] < 6.5,
        "3_sharpe_gt_2p15_and_dsr_ge_0p98": (s1["sharpe"] > 2.15) and (s1["deflated_sharpe"] >= 0.98),
        "4_calmar_ge_3p0": s1["calmar"] >= 3.0,
        "5_oos_2024_2026_sharpe_ge_2p0": s1["oos_2024_2026_sharpe"] >= 2.0,
    }

    out = {
        "prereg": "PREREG57",
        "n_trials": N_TRIALS,
        "mean_ada_crypto_weight": q57["mean_ada_crypto_weight"],
        "conformal_gate_diagnostics": q57["gate_diag"],
        "variants": variants,
        "checks": checks,
        "overall_pass": all(checks.values()),
    }
    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    with open(OUT_PATH, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
