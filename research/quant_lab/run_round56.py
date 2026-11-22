"""PREREG56: Synthesize Quantum-Meta Precision (Subagent 1) + Fast Universal Portfolio (Subagent 2).

Compares:
  - baseline_mft_lft_1x / baseline_mft_lft_1p8x
  - Q56_hybrid_1x (Fast EG Universal + GK Crash Brake + Pre-2024 Quantum-Meta Gate)
  - Q56_hybrid_scaled_1p8x / Q56_hybrid_scaled_2p5x (net of 5.5%/yr borrow on >1.0x)
Writes results/round56.json.
"""
from __future__ import annotations

import json
import math
import os
from typing import Any

import numpy as np
import pandas as pd

import fast_universal_alpha as FUA
import lab
import mft_lft_fund as MLF
import monthly_reset_audit as MRA
import quantum_meta_alpha as QMA
import run_round52 as R52
import strategies36 as S36
import yearly_goal_loop as YG

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_PATH = os.path.join(HERE, "results", "round56.json")
N_TRIALS = 885


def _scale_with_borrow(ret: pd.Series, mm: pd.DataFrame, target_vol: float, max_lev: float, borrow_apr: float = 0.055) -> pd.Series:
    arr = ret.to_numpy(dtype=float)
    tox = mm["micro_tox_score"].reindex(ret.index).ffill().fillna(0.3).to_numpy(dtype=float)
    out = np.zeros(len(arr))
    eq, peak = 1.0, 1.0
    for t in range(len(arr)):
        scale = 1.0
        if target_vol > 0.0 and t >= 20:
            v20 = float(np.std(arr[t - 20 : t], ddof=1) * math.sqrt(365.0))
            scale = min(max_lev, target_vol / max(v20, 0.025))
        if t >= 1 and tox[t - 1] > 0.72:
            scale *= 0.80
        dm = S36.soft_taper(1.0 - eq / peak, dd_start=0.05, dd_full=0.15, floor=0.35)
        lev = min(max_lev, scale * dm)
        r = lev * arr[t] - max(0.0, lev - 1.0) * (borrow_apr / 365.0)
        out[t] = r
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)
    return pd.Series(out, index=ret.index)


def _pack(s: pd.Series, gw: pd.Series | None = None) -> dict[str, Any]:
    st = YG.evaluate_by_year(s)
    oos_s = s[s.index >= pd.Timestamp("2024-01-01", tz="UTC")]
    oos = YG.evaluate_by_year(oos_s)
    mo = MRA.monthly_reset_report(s)
    active = s[gw > 0.0] if gw is not None else s
    return {
        "cagr_pct": st["cagr_pct"],
        "max_dd_pct": st["max_dd_pct"],
        "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
        "sharpe": st["sharpe"],
        "deflated_sharpe": round(float(lab.deflated_sharpe(s, N_TRIALS)), 4),
        "y2022_pct": st["years"]["2022"]["ann_pct"],
        "y2026_pct": st["years"]["2026"]["ann_pct"],
        "monthly_win_rate_pct": mo["win_rate_pct"],
        "active_day_win_rate_pct": round(float((active > 0.0).mean() * 100.0), 2),
        "oos_2024_2026_cagr_pct": oos["cagr_pct"],
        "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
        "oos_2024_2026_calmar": round(oos["cagr_pct"] / max(0.01, oos["max_dd_pct"]), 3),
        "oos_2024_2026_sharpe": oos["sharpe"],
    }


def main() -> None:
    baseline = MLF.run_fund_backtest()

    r_crypto, _ = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(["BTC", "ETH", "SOL"])
    mm = u_c["mm"]
    btc_df = u_c["daily"]["BTC"].reindex(r_crypto.index).ffill()

    u_eq = YG.load_equities_universe(tickers=YG.MACRO_EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_eq_tr = MLF.simulate_lft_mft_sleeve(pre_eq, top_k=3, rebal_buffer=0.03)
    r_eq = r_eq_tr.copy()
    r_eq.index = r_eq.index.normalize()
    r_eq = r_eq.groupby(level=0).sum().reindex(r_crypto.index.normalize()).fillna(0.0).set_axis(r_crypto.index)

    # 1. Subagent 2: Risk-Parity Online EG Universal Portfolio + Garman-Klass/SuperSmoother Crash Brake
    ret_mat = np.column_stack([r_crypto.to_numpy(dtype=np.float64), r_eq.to_numpy(dtype=np.float64)])
    vol_c = np.maximum(pd.Series(ret_mat[:, 0]).rolling(20, min_periods=10).std().shift(1).fillna(0.015).to_numpy(), 1e-4)
    vol_e = np.maximum(pd.Series(ret_mat[:, 1]).rolling(20, min_periods=10).std().shift(1).fillna(0.008).to_numpy(), 1e-4)
    inv_v = np.column_stack([1.0 / vol_c, 1.0 / vol_e])
    inv_v = inv_v / np.sum(inv_v, axis=1, keepdims=True)
    eg_raw = FUA.eg_universal_weights(ret_mat * inv_v, eta=0.10, shrinkage=0.05, rebal_buffer=0.02, cost_rate=2.5e-4)
    eg_w = (eg_raw * inv_v)
    eg_w = eg_w / np.sum(eg_w, axis=1, keepdims=True)
    r_eg = pd.Series(np.sum(eg_w * ret_mat, axis=1), index=r_crypto.index)

    ss_btc = FUA.fast_supersmoother_signal(
        btc_df["o"].to_numpy(dtype=np.float64),
        btc_df["h"].to_numpy(dtype=np.float64),
        btc_df["l"].to_numpy(dtype=np.float64),
        btc_df["c"].to_numpy(dtype=np.float64),
        crash_ratio_thresh=2.20,
        crash_floor=0.55,
    )
    brake = pd.Series(ss_btc["crash_brake"], index=r_crypto.index)
    r_fast_1x = MRA.apply_vol_tox_circuit_breaker(r_eg, mm, target_vol=0.0, max_lev=1.0) * (0.7 + 0.3 * brake)

    # 2. Subagent 1: Quantum Von Neumann Spectral Entropy + Pre-2024 Frozen Meta-Labeling Precision Gate
    ca_crypto = pd.DataFrame({c: u_c["daily"][c]["c"].pct_change() for c in ["BTC", "ETH", "SOL"]}).reindex(r_crypto.index)
    ca_eq = pre_eq["ret_df"].copy()
    ca_eq.index = ca_eq.index.normalize()
    ca_eq = ca_eq.groupby(level=0).sum().reindex(r_crypto.index.normalize()).set_axis(r_crypto.index)
    ca_df = pd.concat([ca_crypto, ca_eq], axis=1).fillna(0.0)
    idle_rate = pd.Series(u_c["rate"].to_numpy(dtype=float) / 100.0 / 365.0, index=r_crypto.index)

    qma_res = QMA.meta_label_precision_gate(
        r_fast_1x,
        ca_df,
        tox_series=mm["micro_tox_score"],
        idle_rate_series=idle_rate,
        prob_threshold=0.52,
        entropy_floor=0.40,
        gate_floor=0.25,
        soft_sizing=True,
    )
    r_q56_1x = qma_res["gated_ret"]
    gw = qma_res["gate_weight"]

    # 3. Portfolio-Level Vol-Targeted Scaling (net of 5.5% borrow)
    r_q56_1p8x = _scale_with_borrow(r_q56_1x, mm, target_vol=0.12, max_lev=1.8, borrow_apr=0.055)
    r_q56_2p5x = _scale_with_borrow(r_q56_1x, mm, target_vol=0.16, max_lev=2.5, borrow_apr=0.055)

    variants = {
        "baseline_mft_lft_1x": baseline["fund_1x_spot"],
        "baseline_mft_lft_1p8x": baseline["fund_1p8x_borrow_adj"],
        "Q56_hybrid_1x": _pack(r_q56_1x, gw),
        "Q56_hybrid_scaled_1p8x": _pack(r_q56_1p8x, gw),
        "Q56_hybrid_scaled_2p5x": _pack(r_q56_2p5x, gw),
    }

    b1 = variants["baseline_mft_lft_1x"]
    bs = variants["baseline_mft_lft_1p8x"]
    h1 = variants["Q56_hybrid_1x"]
    hs = variants["Q56_hybrid_scaled_2p5x"]
    checks = {
        "1_1x_calmar_gt_baseline": h1["calmar"] > b1["calmar"],
        "2_1x_max_dd_lt_5pct": h1["max_dd_pct"] < 5.0,
        "3_scaled_sharpe_gt_baseline": hs["sharpe"] > bs["sharpe"],
        "4_scaled_calmar_gt_baseline": hs["calmar"] > bs["calmar"],
        "5_oos_2024_2026_sharpe_gt_baseline": hs["oos_2024_2026_sharpe"] > bs["oos_2024_2026_sharpe"],
    }

    out = {
        "prereg": "PREREG56",
        "n_trials": N_TRIALS,
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
