"""PREREG54: Apply Dual-Momentum Cash Gate + Trend-Filtered MR directly to Crypto (BTC+ETH+SOL).

Compares Q54_crypto_dual_mom (60% dual-mom BTC+ETH+SOL + 40% C1 carry, US_SPOT)
against Q51_btc_eth_sol_volparity_spot, plus the combined Q54_crypto + Q53_equities blend.
Writes results/round54.json.
"""
from __future__ import annotations

import json
import os
from typing import Any

import numpy as np
import pandas as pd

import lab
import run_round52 as R52
import run_round53 as R53
import strategies19 as S19
import yearly_goal_loop as YG
from run_round19 import load_funding
from run_round33 import W0, W1

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 870


def build_crypto_q54_returns() -> tuple[pd.Series, pd.Series]:
    """60% dual-momentum BTC+ETH+SOL + 40% C1 carry under US_SPOT costs."""
    r_q51, reg = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(coins=["BTC", "ETH", "SOL"])
    pre_c = YG.precompute_signals(u_c)

    # 100% dual-momentum trend sleeve on BTC+ETH+SOL (with DTB3 on idle cash)
    r_trend = R53.simulate_dual_momentum_spot(
        pre_c, top_k=2, mom_fast=7, mom_slow=28,
        trend_weight=0.50, xs_weight=0.40, mr_weight=0.10,
        dd_start=0.06, dd_full=0.20, dd_floor=0.40,
    )
    # 60% trend + 40% C1 carry
    c1 = pd.Series(pre_c["c1_ret"], index=pre_c["idx"])
    r_q54 = 0.60 * r_trend + 0.40 * c1
    return r_q54.reindex(r_q51.index).fillna(0.0), reg


def main() -> None:
    r_q51, reg = R52.build_crypto_q51_returns()
    r_q54, _ = build_crypto_q54_returns()

    # Also test combining Q54_crypto with Q53_equities via asymmetric fast-risk-off invvol
    eq_univ = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(eq_univ)
    r_eq_tr = R53.simulate_dual_momentum_spot(pre_eq, top_k=3, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)
    r_eq_norm = r_eq_tr.copy()
    r_eq_norm.index = r_eq_norm.index.normalize()
    r_eq_norm = r_eq_norm.groupby(level=0).sum()
    r_eq_q53 = r_eq_norm.reindex(r_q51.index.normalize()).fillna(0.0).set_axis(r_q51.index)

    r_combo, w_c = R53.asymmetric_invvol_blend(r_q54, r_eq_q53, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)

    def _sum(ret: pd.Series) -> dict[str, Any]:
        m = lab.metrics(ret, reg) if not hasattr(R53, "_summarize") else R53._summarize(ret, reg)
        m[f"deflated_sharpe_{N_TRIALS}"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
        return m

    variants = {
        "Q51_crypto_baseline": _sum(r_q51),
        "Q54_crypto_dual_mom": _sum(r_q54),
        "Q54_plus_Q53_invvol": _sum(r_combo),
    }

    b = variants["Q51_crypto_baseline"]
    c = variants["Q54_crypto_dual_mom"]
    checks = {
        "1_sharpe_ge_q51_minus_0p05": c["sharpe"] >= b["sharpe"] - 0.05,
        "2_max_dd_better_than_q51": c["max_dd_pct"] > b["max_dd_pct"],
        "3_y2022_better_than_q51": c["per_year"]["2022"]["ann_pct"] > b["per_year"]["2022"]["ann_pct"],
        "4_y2026_ann_gt_q51": c["y2026_ann_pct"] > b["y2026_ann_pct"],
        "5_dsr_ge_0p85": c[f"deflated_sharpe_{N_TRIALS}"] >= 0.85,
    }

    out = {
        "prereg": "PREREG54",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "mean_combo_crypto_weight": round(float(w_c.mean()), 4),
        "variants": variants,
        "checks": checks,
        "overall_pass": all(checks.values()),
    }
    out_path = os.path.join(HERE, "results", "round54.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
