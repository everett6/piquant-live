"""PREREG53: Dual-Momentum Cash Gate + Trend-Filtered MR + Asymmetric Fast-Risk-Off InvVol.

Tests first-principles improvements on Crypto (Q51) + US Equities (10 tickers) without tuning on 2026.
Writes results/round53.json.
"""
from __future__ import annotations

import json
import math
import os
from typing import Any

import numpy as np
import pandas as pd

import lab
import run_round52 as R52
import strategies36 as S36
import yearly_goal_loop as YG
from run_round33 import W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 860


def simulate_dual_momentum_spot(
    pre: dict[str, Any],
    top_k: int = 3,
    mom_fast: int = 7,
    mom_slow: int = 28,
    trend_weight: float = 0.50,
    xs_weight: float = 0.40,
    mr_weight: float = 0.10,
    dd_start: float = 0.08,
    dd_full: float = 0.35,
    dd_floor: float = 0.35,
) -> pd.Series:
    """Strictly causal 1x long-only simulation with:
    1) Absolute momentum gate on trend sleeve (m > 0 required, else idle -> DTB3)
    2) Trend-filtered MR (only buy 5d oversold dips when tr > 0.25, no falling knives)
    """
    idx = pre["idx"]
    coins = pre["coins"]
    n = len(idx)
    px_df = pre["px_df"]
    ret_arr = {c: pre["ret_df"][c].to_numpy(dtype=float) for c in coins}
    vol_arr = {c: pre["vol20_df"][c].to_numpy(dtype=float) for c in coins}
    tr_arr = pre["trend_pos"]
    r5z_arr = {c: pre["ret5_z"][c].to_numpy(dtype=float) for c in coins}

    mom_f = px_df.pct_change(mom_fast)
    mom_s = px_df.pct_change(mom_slow)
    mom_score = (0.5 * mom_f + 0.5 * mom_s) / (pre["vol20_df"].clip(lower=0.10))
    mom_np = {c: mom_score[c].to_numpy(dtype=float) for c in coins}

    ann_days = int(pre.get("annual_days", 365))
    cost_rate = 3.0 * 1e-4 if ann_days == 365 else 1.5 * 1e-4

    out_ret = np.zeros(n, dtype=float)
    eq = 1.0
    peak = 1.0
    prev_w = {c: 0.0 for c in coins}
    held_w = {c: 0.0 for c in coins}

    for t in range(n):
        turn = sum(abs(held_w[c] - prev_w[c]) for c in coins)
        gross = sum(abs(held_w[c]) for c in coins)
        pnl = sum(held_w[c] * ret_arr[c][t] for c in coins)
        idle = max(0.0, 1.0 - gross)
        bills = 0.0 if t == 0 else idle * pre["rate"][t - 1]
        total_r = pnl - turn * cost_rate + bills
        out_ret[t] = total_r

        eq = max(1e-12, eq * (1.0 + total_r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        dd_mult = S36.soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor)

        scored = []
        for c in coins:
            m = mom_np[c][t]
            v = vol_arr[c][t]
            if np.isfinite(m) and np.isfinite(v) and v > 0.05:
                scored.append((m, c, v))
        scored.sort(reverse=True)

        raw_w = {c: 0.0 for c in coins}
        active = [c for _, c, _ in scored]
        if active:
            inv_v = {c: 1.0 / max(vol_arr[c][t], 0.15) for c in active}
            tot_iv = sum(inv_v.values())

            # 1) Trend sleeve with absolute momentum gate (m > 0)
            w_tr = {
                c: ((inv_v[c] / tot_iv) * tr_arr[c][t] if m > 0.0 else 0.0)
                for m, c, _ in scored
            }
            tr_gross = sum(w_tr.values())
            if tr_gross > 1.0:
                for c in active:
                    w_tr[c] /= tr_gross

            # 2) Top-K positive-momentum cross-sectional sleeve
            w_xs = {c: 0.0 for c in active}
            long_c = [c for m, c, _ in scored[: min(top_k, len(scored))] if m > 0.0]
            if long_c:
                iv_l = sum(inv_v[c] for c in long_c)
                for c in long_c:
                    w_xs[c] = inv_v[c] / iv_l

            # 3) Trend-filtered MR (only buy oversold dips in confirmed uptrend tr > 0.25)
            w_mr = {c: 0.0 for c in active}
            if mr_weight > 0.0:
                mr_long = [c for m, c, _ in scored if r5z_arr[c][t] < -1.5 and tr_arr[c][t] > 0.25 and m > -0.2]
                for c in mr_long:
                    w_mr[c] = 1.0 / len(mr_long)

            for c in active:
                raw_w[c] = max(0.0, trend_weight * w_tr[c] + xs_weight * w_xs[c] + mr_weight * w_mr[c])

        for c in coins:
            prev_w[c] = held_w[c]
            held_w[c] = raw_w[c] * dd_mult

    return pd.Series(out_ret, index=idx)


def asymmetric_invvol_blend(
    r_a: pd.Series,
    r_b: pd.Series,
    fast_lb: int = 10,
    slow_lb: int = 20,
    min_w: float = 0.20,
    max_w: float = 0.80,
) -> tuple[pd.Series, pd.Series]:
    """Causal asymmetric fast-risk-off inverse-vol blend: max(vol_fast, vol_slow) shifted 1d."""
    va = pd.concat([
        r_a.rolling(fast_lb, min_periods=5).std(),
        r_a.rolling(slow_lb, min_periods=10).std(),
    ], axis=1).max(axis=1).shift(1)
    vb = pd.concat([
        r_b.rolling(fast_lb, min_periods=5).std(),
        r_b.rolling(slow_lb, min_periods=10).std(),
    ], axis=1).max(axis=1).shift(1)
    inv_a = 1.0 / va.clip(lower=1e-4)
    inv_b = 1.0 / vb.clip(lower=1e-4)
    w_a = (inv_a / (inv_a + inv_b)).fillna(0.5).clip(lower=min_w, upper=max_w)
    return w_a * r_a + (1.0 - w_a) * r_b, w_a


def _summarize(ret: pd.Series, reg: pd.Series) -> dict[str, Any]:
    m = extended_metrics(ret, reg)
    m[f"deflated_sharpe_{N_TRIALS}"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    by_yr = YG.evaluate_by_year(ret, min_days=120, annual_days=365)
    oos = YG.evaluate_by_year(ret[ret.index >= pd.Timestamp("2024-01-01", tz="UTC")], min_days=120, annual_days=365)
    m["per_year"] = by_yr["years"]
    m["cagr_pct"] = by_yr["cagr_pct"]
    m["worst_year"] = by_yr["worst_year"]
    m["min_ann_pct"] = by_yr["min_ann_pct"]
    m["y2026_ann_pct"] = by_yr["years"].get("2026", {}).get("ann_pct", 0.0)
    m["oos_2024_2026_cagr_pct"] = oos["cagr_pct"]
    m["oos_2024_2026_sharpe"] = oos["sharpe"]
    m["oos_2024_2026_max_dd_pct"] = oos["max_dd_pct"]
    return m


def main() -> None:
    r_crypto, reg = R52.build_crypto_q51_returns()
    r_eq_b2 = R52.build_equities_spot_returns(r_crypto.index)
    r_q52_invvol, _ = R52.invvol_blend(r_crypto, r_eq_b2, lookback=20, min_w=0.20, max_w=0.80)

    # Upgraded Equities sleeve (dual-momentum cash gate + trend-filtered MR)
    eq_univ = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(eq_univ)
    r_eq_q53_tr = simulate_dual_momentum_spot(pre_eq, top_k=3, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)
    r_eq_norm = r_eq_q53_tr.copy()
    r_eq_norm.index = r_eq_norm.index.normalize()
    r_eq_norm = r_eq_norm.groupby(level=0).sum()
    r_eq_q53 = r_eq_norm.reindex(r_crypto.index.normalize()).fillna(0.0).set_axis(r_crypto.index)

    # Asymmetric fast-risk-off InvVol blend
    r_q53_invvol, w_c = asymmetric_invvol_blend(r_crypto, r_eq_q53, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)

    variants = {
        "Q52_invvol_baseline": _summarize(r_q52_invvol, reg),
        "B2_equities_old": _summarize(r_eq_b2, reg),
        "Q53_equities_upgraded": _summarize(r_eq_q53, reg),
        "Q53_invvol_upgraded": _summarize(r_q53_invvol, reg),
    }

    b = variants["Q52_invvol_baseline"]
    c = variants["Q53_invvol_upgraded"]
    checks = {
        "1_sharpe_gt_q52": c["sharpe"] > b["sharpe"],
        "2_max_dd_ge_q52": c["max_dd_pct"] >= b["max_dd_pct"],
        "3_y2026_ann_gt_q52": c["y2026_ann_pct"] > b["y2026_ann_pct"],
        "4_oos_2024_2026_sharpe_gt_q52": c["oos_2024_2026_sharpe"] > b["oos_2024_2026_sharpe"],
        "5_dsr_ge_0p95": c[f"deflated_sharpe_{N_TRIALS}"] >= 0.95,
    }

    out = {
        "prereg": "PREREG53",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "mean_crypto_weight": round(float(w_c.mean()), 4),
        "variants": variants,
        "checks": checks,
        "overall_pass": all(checks.values()),
    }
    out_path = os.path.join(HERE, "results", "round53.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
