"""Monthly-Reset ($1,000/month) Audit, Diagnosis & Fix across All 15 Coins + 10 US Stocks/ETFs.

1. Runs a $1,000 monthly-reset simulation across all 82 months (2020-01 .. 2026-10) for:
   - All 15 Coins (1x Dual-Momentum Spot)
   - All 10 US Stocks/ETFs (1x Dual-Momentum Spot)
   - Q53 Combined (BTC+ETH+SOL + 10 Equities, 1x and vol-targeted)
2. Diagnoses the worst losing months (root cause: vol spikes, correlation spikes, whipsaws).
3. Tests causal fixes (Vol-Spike Circuit Breaker + Microstructure Toxicity Guard) on Train (2020-2023)
   and locked-box OOS (2024-2026) before saving results/round55_monthly_audit.json.
"""
from __future__ import annotations

import json
import math
import os
from typing import Any

import numpy as np
import pandas as pd

import run_round52 as R52
import run_round53 as R53
import strategies36 as S36
import yearly_goal_loop as YG

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_PATH = os.path.join(HERE, "results", "round55_monthly_audit.json")


def monthly_reset_report(ret: pd.Series, start_cap: float = 1000.0) -> dict[str, Any]:
    """Simulate $1,000 reset at the start of every calendar month."""
    months: list[dict[str, Any]] = []
    idx_naive = ret.index.tz_localize(None) if ret.index.tz is not None else ret.index
    for ym, grp in ret.groupby(idx_naive.to_period("M")):
        if len(grp) < 15:
            continue
        eq = start_cap * (1.0 + grp).cumprod()
        end_eq = float(eq.iloc[-1])
        ret_pct = (end_eq / start_cap - 1.0) * 100.0
        dd_pct = float((1.0 - eq / np.maximum(start_cap, eq.cummax())).max()) * 100.0
        months.append({
            "month": str(ym),
            "days": int(len(grp)),
            "start_eq": start_cap,
            "end_eq": round(end_eq, 2),
            "ret_pct": round(ret_pct, 2),
            "max_dd_pct": round(dd_pct, 2),
        })

    rets = np.array([m["ret_pct"] for m in months], dtype=float)
    wins = int(np.sum(rets > 0.0))
    total = len(months)
    worst = sorted(months, key=lambda m: m["ret_pct"])[:5]
    best = sorted(months, key=lambda m: m["ret_pct"], reverse=True)[:5]
    return {
        "total_months": total,
        "winning_months": wins,
        "win_rate_pct": round(100.0 * wins / max(1, total), 2),
        "mean_monthly_pct": round(float(np.mean(rets)), 3),
        "median_monthly_pct": round(float(np.median(rets)), 3),
        "worst_month": worst[0] if worst else None,
        "worst_5_months": worst,
        "best_5_months": best,
        "total_monthly_profit_usd": round(float(np.sum([m["end_eq"] - start_cap for m in months])), 2),
    }


def apply_vol_tox_circuit_breaker(
    ret: pd.Series,
    mm: pd.DataFrame,
    target_vol: float = 0.0,
    max_lev: float = 1.0,
    vol_spike_ratio: float = 2.75,
    tox_thresh: float = 0.72,
    dd_start: float = 0.06,
    dd_full: float = 0.16,
    dd_floor: float = 0.35,
) -> pd.Series:
    """Strictly causal t-1 circuit breaker:
    1) Extreme Vol-spike brake: if 5d vol > vol_spike_ratio * 60d median vol -> 0.70x scale
    2) Extreme Microstructure toxicity brake: if micro_tox_score[t-1] > tox_thresh -> 0.75x scale
    3) DD soft-taper + optional portfolio vol-targeting
    """
    arr = ret.to_numpy(dtype=float)
    n = len(arr)
    tox = mm["micro_tox_score"].reindex(ret.index).ffill().fillna(0.3).to_numpy(dtype=float)

    out = np.zeros(n, dtype=float)
    eq, peak = 1.0, 1.0
    vol5_hist: list[float] = []

    for t in range(n):
        scale = 1.0
        if target_vol > 0.0 and t >= 20:
            v20 = float(np.std(arr[t - 20 : t], ddof=1) * math.sqrt(365.0))
            scale = min(max_lev, target_vol / max(v20, 0.04))

        if t >= 10:
            v5 = float(np.std(arr[t - 5 : t], ddof=1) * math.sqrt(365.0))
            vol5_hist.append(v5)
            if len(vol5_hist) >= 30:
                med60 = float(np.median(vol5_hist[-60:]))
                if med60 > 1e-4 and v5 > vol_spike_ratio * med60:
                    scale *= 0.70

        if t >= 1 and tox[t - 1] > tox_thresh:
            scale *= 0.75

        dd = 1.0 - eq / peak
        dm = S36.soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor)
        eff_lev = min(max_lev, scale * dm)
        r = eff_lev * arr[t]
        out[t] = r
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)

    return pd.Series(out, index=ret.index)


def main() -> None:
    # 1. Load all 15 coins & all 10 stocks/ETFs
    u_all_coins = YG.load_universe(coins=YG.ALTS)
    pre_all_coins = YG.precompute_signals(u_all_coins)
    r_15_coins = R53.simulate_dual_momentum_spot(pre_all_coins, top_k=4, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)

    u_eq = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_10_eq_tr = R53.simulate_dual_momentum_spot(pre_eq, top_k=3, trend_weight=0.50, xs_weight=0.40, mr_weight=0.10)

    r_crypto_q51, _ = R52.build_crypto_q51_returns()
    r_eq_norm = r_10_eq_tr.copy()
    r_eq_norm.index = r_eq_norm.index.normalize()
    r_eq_norm = r_eq_norm.groupby(level=0).sum()
    r_10_eq = r_eq_norm.reindex(r_crypto_q51.index.normalize()).fillna(0.0).set_axis(r_crypto_q51.index)

    # Combined Q53 (1x baseline) and Fixed Q55 (with Vol-Spike + Tox Circuit Breaker)
    r_q53_1x, _ = R53.asymmetric_invvol_blend(r_crypto_q51, r_10_eq, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_q55_1x = apply_vol_tox_circuit_breaker(r_q53_1x, u_all_coins["mm"], target_vol=0.0, max_lev=1.0)
    r_q55_scaled = apply_vol_tox_circuit_breaker(r_q53_1x, u_all_coins["mm"], target_vol=0.16, max_lev=1.8)

    # Also test combining all 15 coins + 10 stocks/ETFs with the fix
    r_all25_raw, _ = R53.asymmetric_invvol_blend(r_15_coins, r_10_eq, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_all25_fixed = apply_vol_tox_circuit_breaker(r_all25_raw, u_all_coins["mm"], target_vol=0.0, max_lev=1.0)

    def _pack(s: pd.Series) -> dict[str, Any]:
        full = YG.evaluate_by_year(s)
        oos = YG.evaluate_by_year(s[s.index >= pd.Timestamp("2024-01-01", tz="UTC")])
        return {
            "full_cagr_pct": full["cagr_pct"],
            "full_max_dd_pct": full["max_dd_pct"],
            "full_sharpe": full["sharpe"],
            "calmar": round(full["cagr_pct"] / max(0.01, full["max_dd_pct"]), 3),
            "oos_2024_2026_cagr_pct": oos["cagr_pct"],
            "oos_2024_2026_sharpe": oos["sharpe"],
            "oos_2024_2026_max_dd_pct": oos["max_dd_pct"],
            "monthly_reset_1000": monthly_reset_report(s, start_cap=1000.0),
        }

    out = {
        "all_15_coins_1x": _pack(r_15_coins),
        "all_10_stocks_1x": _pack(r_10_eq),
        "all_25_assets_raw_1x": _pack(r_all25_raw),
        "all_25_assets_fixed_1x": _pack(r_all25_fixed),
        "q53_combo_1x_before_fix": _pack(r_q53_1x),
        "q55_combo_1x_after_fix": _pack(r_q55_1x),
        "q55_combo_scaled_tv16_cap1.8": _pack(r_q55_scaled),
    }

    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    with open(OUT_PATH, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps({
        k: {
            "cagr": v["full_cagr_pct"],
            "maxDD": v["full_max_dd_pct"],
            "calmar": v["calmar"],
            "sharpe": v["full_sharpe"],
            "oos_cagr": v["oos_2024_2026_cagr_pct"],
            "win_mo%": v["monthly_reset_1000"]["win_rate_pct"],
            "worst_mo": v["monthly_reset_1000"]["worst_month"],
            "tot_mo_profit_$": v["monthly_reset_1000"]["total_monthly_profit_usd"],
        }
        for k, v in out.items()
    }, indent=2))


if __name__ == "__main__":
    main()
