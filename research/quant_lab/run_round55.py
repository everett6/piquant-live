"""PREREG55: Test Macro Crisis Rotation (DBC+UUP+XLE) + V1 Volatility Harvesting.

Compares each addition against the Q55 baseline; keeps what passes, scraps what fails.
Writes results/round55.json.
"""
from __future__ import annotations

import json
import os
from typing import Any

import numpy as np
import pandas as pd

import monthly_reset_audit as MRA
import run_round52 as R52
import run_round53 as R53
import yearly_goal_loop as YG

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_PATH = os.path.join(HERE, "results", "round55.json")


def simulate_v1_vol_harvest(
    btc: pd.Series,
    eth: pd.Series,
    rate_pct: pd.Series,
    target_w: float = 0.25,
    drift_band: float = 0.05,
    cost_bps: float = 3.0,
) -> pd.Series:
    """Strictly causal V1 rebalancing harvester (25% BTC, 25% ETH, 50% DTB3 T-bills)."""
    idx = btc.index
    r_b = btc.pct_change().fillna(0.0).to_numpy(dtype=float)
    r_e = eth.reindex(idx).pct_change().fillna(0.0).to_numpy(dtype=float)
    rf = rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0

    n = len(idx)
    out = np.zeros(n, dtype=float)
    wb, we, wc = target_w, target_w, 1.0 - 2.0 * target_w
    cost_rate = cost_bps * 1e-4

    for t in range(n):
        rb, re = r_b[t], r_e[t]
        rc = 0.0 if t == 0 else rf[t - 1]
        port_r = wb * rb + we * re + wc * rc

        # Post-return drifted weights
        denom = max(1e-12, 1.0 + port_r)
        wb_d = wb * (1.0 + rb) / denom
        we_d = we * (1.0 + re) / denom
        wc_d = wc * (1.0 + rc) / denom

        # Rebalance if any coin drifts > drift_band from target_w
        if abs(wb_d - target_w) > drift_band or abs(we_d - target_w) > drift_band:
            turn = abs(wb_d - target_w) + abs(we_d - target_w)
            cost = turn * cost_rate
            port_r -= cost
            wb, we, wc = target_w, target_w, 1.0 - 2.0 * target_w
        else:
            wb, we, wc = wb_d, we_d, wc_d

        out[t] = port_r

    return pd.Series(out, index=idx)


def _eval(name: str, s: pd.Series) -> dict[str, Any]:
    st = YG.evaluate_by_year(s)
    oos = YG.evaluate_by_year(s[s.index >= pd.Timestamp("2024-01-01", tz="UTC")])
    mo = MRA.monthly_reset_report(s, start_cap=1000.0)
    yr = MRA.yearly_reset_report(s, start_cap=1000.0)
    return {
        "cagr_pct": st["cagr_pct"],
        "max_dd_pct": st["max_dd_pct"],
        "calmar": round(st["cagr_pct"] / max(0.01, st["max_dd_pct"]), 3),
        "sharpe": st["sharpe"],
        "y2022_pct": st["years"]["2022"]["ann_pct"],
        "y2026_pct": st["years"]["2026"]["ann_pct"],
        "oos_2024_2026_cagr_pct": oos["cagr_pct"],
        "oos_2024_2026_sharpe": oos["sharpe"],
        "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
        "monthly_win_rate_pct": mo["win_rate_pct"],
        "worst_month": mo["worst_month"],
        "total_yearly_profit_usd": yr["total_yearly_profit_usd"],
    }


def main() -> None:
    r_c, _ = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(coins=["BTC", "ETH"])
    mm = u_c["mm"]

    # Baseline Equities (10 tickers)
    u_eq10 = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS)
    r_eq10_tr = R53.simulate_dual_momentum_spot(YG.precompute_signals(u_eq10), top_k=3, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)
    r_eq10 = r_eq10_tr.copy(); r_eq10.index = r_eq10.index.normalize(); r_eq10 = r_eq10.groupby(level=0).sum().reindex(r_c.index.normalize()).fillna(0.0).set_axis(r_c.index)

    # Macro Equities (+ DBC + UUP)
    u_eq12 = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS + ["DBC", "UUP"])
    r_eq12_tr = R53.simulate_dual_momentum_spot(YG.precompute_signals(u_eq12), top_k=3, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)
    r_eq12 = r_eq12_tr.copy(); r_eq12.index = r_eq12.index.normalize(); r_eq12 = r_eq12.groupby(level=0).sum().reindex(r_c.index.normalize()).fillna(0.0).set_axis(r_c.index)

    # Macro Equities (+ DBC + UUP + XLE)
    u_eq13 = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS + ["DBC", "UUP", "XLE"])
    r_eq13_tr = R53.simulate_dual_momentum_spot(YG.precompute_signals(u_eq13), top_k=3, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)
    r_eq13 = r_eq13_tr.copy(); r_eq13.index = r_eq13.index.normalize(); r_eq13 = r_eq13.groupby(level=0).sum().reindex(r_c.index.normalize()).fillna(0.0).set_axis(r_c.index)

    # V1 Volatility Harvesting (25% BTC, 25% ETH, 50% DTB3)
    r_v1 = simulate_v1_vol_harvest(u_c["daily"]["BTC"]["c"].reindex(r_c.index), u_c["daily"]["ETH"]["c"].reindex(r_c.index), u_c["rate"])

    r_base_blend, _ = R53.asymmetric_invvol_blend(r_c, r_eq10, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_dbc_uup_blend, _ = R53.asymmetric_invvol_blend(r_c, r_eq12, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_dbc_uup_xle_blend, _ = R53.asymmetric_invvol_blend(r_c, r_eq13, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)

    r_base_1x = MRA.apply_vol_tox_circuit_breaker(r_base_blend, mm, target_vol=0.0, max_lev=1.0)
    r_dbc_uup_1x = MRA.apply_vol_tox_circuit_breaker(r_dbc_uup_blend, mm, target_vol=0.0, max_lev=1.0)
    r_dbc_uup_v1_1x = MRA.apply_vol_tox_circuit_breaker(0.85 * r_dbc_uup_blend + 0.15 * r_v1, mm, target_vol=0.0, max_lev=1.0)

    r_base_sc = MRA.apply_vol_tox_circuit_breaker(r_base_blend, mm, target_vol=0.16, max_lev=1.8)
    r_dbc_uup_sc = MRA.apply_vol_tox_circuit_breaker(r_dbc_uup_blend, mm, target_vol=0.16, max_lev=1.8)
    r_dbc_uup_xle_sc = MRA.apply_vol_tox_circuit_breaker(r_dbc_uup_xle_blend, mm, target_vol=0.16, max_lev=1.8)
    r_dbc_uup_v1_sc = MRA.apply_vol_tox_circuit_breaker(0.85 * r_dbc_uup_blend + 0.15 * r_v1, mm, target_vol=0.16, max_lev=1.8)

    out = {
        "V1_vol_harvest_only": _eval("V1", r_v1),
        "1x_baseline": _eval("1x_base", r_base_1x),
        "1x_plus_DBC_UUP": _eval("1x_dbc_uup", r_dbc_uup_1x),
        "1x_plus_DBC_UUP_V1": _eval("1x_dbc_uup_v1", r_dbc_uup_v1_1x),
        "scaled_baseline": _eval("sc_base", r_base_sc),
        "scaled_plus_DBC_UUP": _eval("sc_dbc_uup", r_dbc_uup_sc),
        "scaled_plus_DBC_UUP_XLE": _eval("sc_dbc_uup_xle", r_dbc_uup_xle_sc),
        "scaled_plus_DBC_UUP_V1": _eval("sc_dbc_uup_v1", r_dbc_uup_v1_sc),
    }

    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    with open(OUT_PATH, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
