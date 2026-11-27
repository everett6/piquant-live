"""PREREG59: Q51 inverse-vol dual-speed trend rotation on ETFs (E1 multi-asset, E2 equity-only).

Run once:  python3 run_round59.py   (writes results/round59.json)
"""
from __future__ import annotations

import json
import os
from typing import Any

import numpy as np
import pandas as pd

import etf_discover as ED
import lab
import strategies35 as S35
import strategies36 as S36
import strategies40 as S40

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, "results", "round59.json")
START, END = "2008-01-01", "2025-01-01"  # END exclusive: holdout never read
RECENT = "2023-01-01"
N_TRIALS = 840
BASE_BPS, STRESS_BPS = 2.0, 10.0
DD_START, DD_FULL, DD_FLOOR = 0.06, 0.20, 0.40
VOL_WIN, VOL_MIN = 20, 10
DAYS = 252

E1 = tuple(t for t in ED.ETF_UNIVERSE if t != "SHY")
E2 = ED.ETF_GROUPS["broad"] + tuple(t for t in ED.ETF_GROUPS["nasdaq_tech"] if t != "QQQ") + ED.ETF_GROUPS["sectors"]


def dual_speed(close: pd.Series) -> pd.Series:
    """0.5 QD coherence + 0.5 R3 fast-exit (both 1x) on one ETF's own history; NaN where it has no data."""
    s = close.dropna()
    d = pd.DataFrame({"c": s})
    pos = 0.5 * (S35.coherence_positions(d, lev=1.0) + S40.r3_fastexit_qd_positions(d, lev=1.0))
    return pd.Series(pos, index=s.index).reindex(close.index)


def simulate(closes: pd.DataFrame, tickers: tuple[str, ...], rate_annual: pd.Series | None,
             cost_bps: float) -> tuple[pd.Series, pd.DataFrame]:
    """Q51 loop (run_round51.simulate_multi_asset_volparity_spot) with trend_share=1, no carry,
    trading-day T-bill accrual. Returns daily net returns and the exposures held each day."""
    idx = closes.index
    n = len(idx)
    cols = [t for t in tickers if t in closes.columns]
    px = closes[cols]
    rets = px.pct_change(fill_method=None).fillna(0.0).to_numpy(dtype=float)
    pos_df = pd.DataFrame({t: dual_speed(px[t]) for t in cols}, index=idx)
    vol = px.pct_change(fill_method=None).rolling(VOL_WIN, min_periods=VOL_MIN).std()
    valid = px.notna() & vol.notna() & (vol > 1e-6) & pos_df.notna()
    iv = np.where(valid.to_numpy(), 1.0 / np.maximum(vol.fillna(1.0).to_numpy(dtype=float), 1e-4), 0.0)
    tot = iv.sum(axis=1, keepdims=True)
    w = np.divide(iv, tot, out=np.zeros_like(iv), where=tot > 0)
    pos = pos_df.fillna(0.0).to_numpy(dtype=float)
    rf = np.zeros(n) if rate_annual is None else (
        rate_annual.reindex(idx).ffill().fillna(0.0).to_numpy(dtype=float) / DAYS
    )
    cost = cost_bps * 1e-4

    out = np.zeros(n)
    held_hist = np.zeros((n, len(cols)))
    act = np.zeros(len(cols))
    prev = np.zeros(len(cols))
    eq = peak = 1.0
    for t in range(n):
        held = act.copy() if t > 0 else np.zeros(len(cols))
        turn = float(np.abs(held - prev).sum())
        idle = max(0.0, 1.0 - float(held.sum()))
        r = float(held @ rets[t]) - turn * cost + (idle * rf[t - 1] if t > 0 else 0.0)
        out[t] = r
        held_hist[t] = held
        eq = max(1e-12, eq * (1.0 + r))
        peak = max(peak, eq)
        mult = S36.soft_taper(1.0 - eq / peak, dd_start=DD_START, dd_full=DD_FULL, floor=DD_FLOOR)
        act = w[t] * pos[t] * mult
        prev = held
    return pd.Series(out, index=idx), pd.DataFrame(held_hist, index=idx, columns=cols)


def benchmark(closes: pd.DataFrame, mix: dict[str, float]) -> pd.Series:
    r = closes[list(mix)].pct_change(fill_method=None).fillna(0.0)
    return sum(r[k] * v for k, v in mix.items())


def metrics(r: pd.Series, n_trials: int = N_TRIALS) -> dict[str, Any]:
    eq = (1.0 + r).cumprod()
    yrs = len(r) / DAYS
    sd = float(r.std(ddof=1))
    rec = r[r.index >= pd.Timestamp(RECENT, tz=r.index.tz)]
    return {
        "cagr_pct": round(float((eq.iloc[-1] ** (1.0 / yrs) - 1.0) * 100.0), 3),
        "sharpe": round(float(r.mean() / sd * np.sqrt(DAYS)) if sd > 0 else 0.0, 4),
        "max_dd_pct": round(float((eq / eq.cummax() - 1.0).min() * 100.0), 3),
        "deflated_sharpe": round(float(lab.deflated_sharpe(r, n_trials)), 4),
        "recent_return_pct": round(float(((1.0 + rec).prod() - 1.0) * 100.0), 3),
    }


def check(s: dict[str, Any], stress: dict[str, Any], b: dict[str, Any]) -> dict[str, bool]:
    return {
        "1_sharpe_edge_0.10": s["sharpe"] >= b["sharpe"] + 0.10,
        "2_shallower_dd": s["max_dd_pct"] > b["max_dd_pct"],
        "3_cagr_within_2pts": s["cagr_pct"] >= b["cagr_pct"] - 2.0,
        "4_dsr_0.80": s["deflated_sharpe"] >= 0.80,
        "5_stress_and_recent_positive": stress["cagr_pct"] > 0.0 and s["recent_return_pct"] > 0.0,
    }


def report(r: pd.Series, held: pd.DataFrame, bench: pd.Series) -> dict[str, Any]:
    groups = {g: [t for t in ts if t in held.columns] for g, ts in ED.ETF_GROUPS.items()}
    gross = held.sum(axis=1)
    by_year = (1.0 + r).groupby(r.index.year).prod() - 1.0
    b_year = (1.0 + bench).groupby(bench.index.year).prod() - 1.0
    return {
        "mean_gross_exposure": round(float(gross.mean()), 4),
        "share_days_any_position": round(float((gross > 1e-6).mean()), 4),
        "trades_per_year_turnover": round(float(held.diff().abs().sum(axis=1).sum() / (len(r) / DAYS)), 3),
        "mean_exposure_by_group": {g: round(float(held[c].sum(axis=1).mean()), 4) for g, c in groups.items() if c},
        "year_returns_pct": {int(y): round(float(v) * 100, 2) for y, v in by_year.items()},
        "bench_year_returns_pct": {int(y): round(float(v) * 100, 2) for y, v in b_year.items()},
        "corr_with_bench": round(float(r.corr(bench)), 4),
    }


def run_trial(closes: pd.DataFrame, tickers: tuple[str, ...], mix: dict[str, float],
              rate: pd.Series | None) -> dict[str, Any]:
    r, held = simulate(closes, tickers, rate, BASE_BPS)
    rs, _ = simulate(closes, tickers, rate, STRESS_BPS)
    win = closes.index >= pd.Timestamp(START, tz="UTC")
    r, rs, held = r[win], rs[win], held[win]
    bench = benchmark(closes, mix)[win]
    s, st, b = metrics(r), metrics(rs), metrics(bench)
    c = check(s, st, b)
    return {"tickers": list(tickers), "benchmark_mix": mix, "strategy": s, "stress": st, "benchmark": b,
            "checks": c, "pass": all(c.values()), "report": report(r, held, bench)}


def main() -> None:
    closes = ED.load_etf_closes(end=END)  # full history before END for warm-up; scored from START
    rate = ED.load_tbill_rate()
    res = {
        "prereg": "PREREG59", "window": [START, END], "n_trials": N_TRIALS,
        "E1_multi_asset": run_trial(closes, E1, {"SPY": 0.6, "IEF": 0.4}, rate),
        "E2_equity_only": run_trial(closes, E2, {"SPY": 1.0}, rate),
    }
    with open(OUT, "w", encoding="utf-8") as fh:
        json.dump(res, fh, indent=2)
    for k in ("E1_multi_asset", "E2_equity_only"):
        t = res[k]
        print(f"{k}: {'PASS' if t['pass'] else 'FAIL'}  strat={t['strategy']}  stress_cagr={t['stress']['cagr_pct']}")
        print(f"    bench={t['benchmark']}  checks={t['checks']}")


if __name__ == "__main__":
    main()
