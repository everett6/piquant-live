"""PREREG52: Backtest Combined 1x Spot Crypto (BTC+ETH+SOL) + US Equities/ETFs (10 tickers).

Runs strictly causal close-to-close backtests (2020-2026) and writes results/round52.json.
"""
from __future__ import annotations

import json
import os
from typing import Any

import numpy as np
import pandas as pd

import lab
import run_round51 as R51
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies40 as S40
import yearly_goal_loop as YG
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 850


def build_crypto_q51_returns() -> tuple[pd.Series, pd.Series]:
    """Compute strictly causal Q51 (BTC+ETH+SOL vol-parity 60/40 trend+carry, US_SPOT)."""
    daily_full = {
        "BTC": R51._load_daily_csv("~/piquant-collect/daily_csv/BTC-USD_1D.csv"),
        "ETH": R51._load_daily_csv("~/piquant-collect/daily_csv/ETH-USD_1D.csv"),
        "SOL": R51._load_daily_csv("~/piquant-data/alts/SOL.csv"),
    }
    d = {
        c: daily_full[c][(daily_full[c].index >= START) & (daily_full[c].index < END)]
        for c in ("BTC", "ETH", "SOL")
    }
    dual_1x = {}
    for c in ("BTC", "ETH", "SOL"):
        qd = pd.Series(S35.coherence_positions(daily_full[c], lev=1.0), index=daily_full[c].index)
        r3 = pd.Series(S40.r3_fastexit_qd_positions(daily_full[c], lev=1.0), index=daily_full[c].index)
        dual_1x[c] = (0.5 * (qd + r3)).reindex(d[c].index)

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    reg = S5.regime_months(S5.complete_months(cut_series(d["BTC"]["c"].pct_change().fillna(0.0), W0, W1)))
    sp = lab.US_SPOT
    c1_ret = S19.portfolio([S19.coin_returns(d[c], f[c], "C1", S19.BASE) for c in ("BTC", "ETH")]).reindex(d["BTC"].index).fillna(0.0)

    r_q51, _, _ = R51.simulate_multi_asset_volparity_spot(
        d, dual_1x, c1_ret, rate,
        trend_share=0.60, carry_share=0.40,
        taker_bps=sp.taker_bps, slip_bps=sp.slip_bps, funding_bps_h=sp.funding_bps_h,
        dd_start=0.06, dd_full=0.20, dd_floor=0.40, vol_window=20,
    )
    return r_q51, reg


def cut_series(s: pd.Series, w0: str, w1: str) -> pd.Series:
    return s[(s.index >= pd.Timestamp(w0, tz="UTC")) & (s.index < pd.Timestamp(w1, tz="UTC"))]


def build_equities_spot_returns(crypto_index: pd.DatetimeIndex) -> pd.Series:
    """Compute strictly causal 1x long-only US Equities/ETFs sleeve aligned to crypto_index."""
    eq_univ = YG.load_equities_universe(tickers=YG.EQUITY_TICKERS)
    pre_eq = YG.precompute_signals(eq_univ)
    cfg = YG.StrategyConfig(
        venue="us_spot", top_k=3, mom_fast=7, mom_slow=28,
        trend_weight=0.60, xs_weight=0.30, mr_weight=0.10,
        short_scale=0.0, tox_cut=1.0, news_boost=0.0,
        target_vol=0.0, max_lev=1.0, dd_start=0.08, dd_full=0.35, dd_floor=0.35, carry_share=0.0,
    )
    r_eq_trading = YG.simulate_config(pre_eq, cfg)
    # Normalize equity timestamps to midnight UTC and align to 365-day calendar (0.0 on market holidays/weekends)
    r_eq_norm = r_eq_trading.copy()
    r_eq_norm.index = r_eq_norm.index.normalize()
    r_eq_norm = r_eq_norm.groupby(level=0).sum()
    return r_eq_norm.reindex(crypto_index.normalize()).fillna(0.0).set_axis(crypto_index)


def invvol_blend(r_a: pd.Series, r_b: pd.Series, lookback: int = 20, min_w: float = 0.20, max_w: float = 0.80) -> tuple[pd.Series, pd.Series]:
    """Strictly causal 20-day inverse-volatility blend (t-1 vol -> t weight)."""
    vol_a = r_a.rolling(lookback, min_periods=10).std().shift(1)
    vol_b = r_b.rolling(lookback, min_periods=10).std().shift(1)
    inv_a = 1.0 / vol_a.clip(lower=1e-4)
    inv_b = 1.0 / vol_b.clip(lower=1e-4)
    w_a = (inv_a / (inv_a + inv_b)).fillna(0.5).clip(lower=min_w, upper=max_w)
    w_b = 1.0 - w_a
    return w_a * r_a + w_b * r_b, w_a


def _summarize(name: str, ret: pd.Series, reg: pd.Series) -> dict[str, Any]:
    m = extended_metrics(ret, reg)
    m[f"deflated_sharpe_{N_TRIALS}"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    by_yr = YG.evaluate_by_year(ret, min_days=120, annual_days=365)
    oos = YG.evaluate_by_year(ret[ret.index >= pd.Timestamp("2024-01-01", tz="UTC")], min_days=120, annual_days=365)
    m["per_year"] = by_yr["years"]
    m["cagr_pct"] = by_yr["cagr_pct"]
    m["worst_year"] = by_yr["worst_year"]
    m["min_ann_pct"] = by_yr["min_ann_pct"]
    m["oos_2024_2026_cagr_pct"] = oos["cagr_pct"]
    m["oos_2024_2026_sharpe"] = oos["sharpe"]
    m["oos_2024_2026_max_dd_pct"] = oos["max_dd_pct"]
    return m


def main() -> None:
    r_crypto, reg = build_crypto_q51_returns()
    r_equities = build_equities_spot_returns(r_crypto.index)
    r_50_50 = 0.5 * r_crypto + 0.5 * r_equities
    r_invvol, w_crypto = invvol_blend(r_crypto, r_equities, lookback=20, min_w=0.20, max_w=0.80)

    variants = {
        "B1_q51_crypto_only": _summarize("B1_q51_crypto_only", r_crypto, reg),
        "B2_equities_only": _summarize("B2_equities_only", r_equities, reg),
        "Q52_50_50_crypto_equities": _summarize("Q52_50_50_crypto_equities", r_50_50, reg),
        "Q52_invvol_crypto_equities": _summarize("Q52_invvol_crypto_equities", r_invvol, reg),
    }

    b1 = variants["B1_q51_crypto_only"]
    q52 = variants["Q52_invvol_crypto_equities"]
    checks = {
        "1_sharpe_gt_crypto": q52["sharpe"] > b1["sharpe"],
        "2_max_dd_better_than_crypto": q52["max_dd_pct"] > b1["max_dd_pct"],
        "3_calmar_gt_crypto": q52["calmar"] > b1["calmar"],
        "4_oos_2024_2026_cagr_gt_crypto": q52["oos_2024_2026_cagr_pct"] > b1["oos_2024_2026_cagr_pct"],
        "5_dsr_ge_0p90": q52[f"deflated_sharpe_{N_TRIALS}"] >= 0.90,
    }

    out = {
        "prereg": "PREREG52",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "mean_crypto_weight_invvol": round(float(w_crypto.mean()), 4),
        "variants": variants,
        "checks": checks,
        "overall_pass": all(checks.values()),
    }
    out_path = os.path.join(HERE, "results", "round52.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
