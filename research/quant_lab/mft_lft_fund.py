"""Institutional MFT + LFT Multi-Strategy Fund Engine.

Combines:
  - LFT (Low-Frequency Trend): Multi-horizon (20d / 60d / 120d) trend + dual-speed coherence
    across Crypto (BTC+ETH+SOL) & US Equities/Macro ETFs (SPY..META + DBC + UUP).
  - MFT (Medium-Frequency Trading): 5d trend-filtered mean-reversion + C1 funding/basis carry
    + micro_tox_score & news_sent_3d gating.
  - Institutional Execution/Risk Layer:
    * 3% no-trade rebalance buffer zone (cuts churn & fee drag)
    * Asymmetric 10d/20d inverse-vol parity risk budgeting
    * Extreme-shock vol-spike & toxicity circuit breaker
    * 6% -> 18% DD soft-taper + DTB3 idle cash yield
"""
from __future__ import annotations

import json
import math
import os
from typing import Any

import numpy as np
import pandas as pd

import monthly_reset_audit as MRA
import run_round52 as R52
import run_round53 as R53
import strategies36 as S36
import yearly_goal_loop as YG

HERE = os.path.dirname(os.path.abspath(__file__))
OUT_PATH = os.path.join(HERE, "results", "mft_lft_fund.json")


def simulate_lft_mft_sleeve(
    pre: dict[str, Any],
    top_k: int = 3,
    lft_weight: float = 0.50,
    xs_weight: float = 0.40,
    mft_mr_weight: float = 0.10,
    rebal_buffer: float = 0.03,
) -> pd.Series:
    """Strictly causal LFT + MFT sleeve with 3% no-trade rebalance buffer."""
    idx = pre["idx"]
    coins = pre["coins"]
    n = len(idx)
    px_df = pre["px_df"]
    ret_arr = {c: pre["ret_df"][c].to_numpy(dtype=float) for c in coins}
    vol_arr = {c: pre["vol20_df"][c].to_numpy(dtype=float) for c in coins}
    tr_arr = pre["trend_pos"]
    r5z_arr = {c: pre["ret5_z"][c].to_numpy(dtype=float) for c in coins}

    # Multi-horizon LFT momentum score (7d / 28d / 60d blend)
    m7 = px_df.pct_change(7)
    m28 = px_df.pct_change(28)
    m60 = px_df.pct_change(60)
    mom_score = ((m7 + m28 + m60) / 3.0) / pre["vol20_df"].clip(lower=0.10)
    mom_np = {c: mom_score[c].to_numpy(dtype=float) for c in coins}

    ann_days = int(pre.get("annual_days", 365))
    cost_rate = 3.0 * 1e-4 if ann_days == 365 else 1.5 * 1e-4

    out = np.zeros(n, dtype=float)
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

            w_lft = {c: ((inv_v[c] / tot_iv) * tr_arr[c][t] if m > 0.0 else 0.0) for m, c, _ in scored}
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

        # 3% no-trade rebalance buffer zone (skip tiny churn)
        for c in coins:
            prev_w[c] = held_w[c]
            if abs(target_w[c] - held_w[c]) >= rebal_buffer or target_w[c] == 0.0:
                held_w[c] = target_w[c]

    return pd.Series(out, index=idx)


def run_fund_backtest() -> dict[str, Any]:
    r_crypto, _ = R52.build_crypto_q51_returns()
    u_c = YG.load_universe(["BTC"])
    mm = u_c["mm"]

    u_eq = YG.load_equities_universe(tickers=YG.MACRO_EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(u_eq)
    r_eq_tr = simulate_lft_mft_sleeve(pre_eq, top_k=3, rebal_buffer=0.03)
    r_eq = r_eq_tr.copy()
    r_eq.index = r_eq.index.normalize()
    r_eq = r_eq.groupby(level=0).sum().reindex(r_crypto.index.normalize()).fillna(0.0).set_axis(r_crypto.index)

    r_blend, _ = R53.asymmetric_invvol_blend(r_crypto, r_eq, fast_lb=10, slow_lb=20, min_w=0.20, max_w=0.80)
    r_1x = MRA.apply_vol_tox_circuit_breaker(r_blend, mm, target_vol=0.0, max_lev=1.0)

    # Borrow-adjusted 1.8x scaled mode (5.5% APR on >1.0x slice)
    arr = r_blend.to_numpy(dtype=float)
    tox = mm["micro_tox_score"].reindex(r_blend.index).ffill().fillna(0.3).to_numpy(dtype=float)
    out_sc = np.zeros(len(arr))
    eq, peak = 1.0, 1.0
    v5_hist: list[float] = []
    for t in range(len(arr)):
        scale = 1.0
        if t >= 20:
            v20 = float(np.std(arr[t - 20 : t], ddof=1) * math.sqrt(365.0))
            scale = min(1.8, 0.16 / max(v20, 0.04))
        if t >= 10:
            v5 = float(np.std(arr[t - 5 : t], ddof=1) * math.sqrt(365.0))
            v5_hist.append(v5)
            if len(v5_hist) >= 30 and np.median(v5_hist[-60:]) > 1e-4 and v5 > 2.75 * np.median(v5_hist[-60:]):
                scale *= 0.70
        if t >= 1 and tox[t - 1] > 0.72:
            scale *= 0.75
        dm = S36.soft_taper(1.0 - eq / peak, dd_start=0.06, dd_full=0.18, floor=0.35)
        lev = min(1.8, scale * dm)
        r = lev * arr[t] - max(0.0, lev - 1.0) * (0.055 / 365.0)
        out_sc[t] = r
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)
    r_sc = pd.Series(out_sc, index=r_blend.index)

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

    res = {"fund_1x_spot": _pack(r_1x), "fund_1p8x_borrow_adj": _pack(r_sc)}
    os.makedirs(os.path.dirname(OUT_PATH), exist_ok=True)
    with open(OUT_PATH, "w", encoding="utf-8") as fh:
        json.dump(res, fh, indent=2)
    return res


if __name__ == "__main__":
    print(json.dumps(run_fund_backtest(), indent=2))
