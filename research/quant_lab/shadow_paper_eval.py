"""Zero-hindsight shadow paper evaluator for quarantined `SHADOW_PAPER_ONLY` alphas.

Evaluates accepted expressions from `results/shadow_candidates.json` strictly on
completed UTC daily bars AFTER each candidate's admission date (`bar_day > entry_day`),
net of venue-appropriate costs (`US_SPOT` vs `US_FUTURES`), and writes
`results/shadow_paper_state.json` atomically. Zero `pyarrow` dependency.
"""
from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
from typing import Any

import numpy as np
import pandas as pd

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import auto_discover as AD
import feature_store as FS
import lab
import multimodal_features as MF
import strategies35 as S35
import strategies40 as S40

DEFAULT_DAILY_CSV_DIR = os.path.expanduser("~/piquant-collect/daily_csv")
DEFAULT_SHADOW_PATH = os.path.join(HERE, "results", "shadow_candidates.json")
DEFAULT_OUT_PATH = os.path.join(HERE, "results", "shadow_paper_state.json")


def _to_utc_day(ts_val: Any) -> pd.Timestamp:
    ts = pd.Timestamp(ts_val)
    if ts.tzinfo is None:
        return ts.tz_localize("UTC").floor("D")
    return ts.tz_convert("UTC").floor("D")


def _normalize_daily_df(df: pd.DataFrame) -> pd.DataFrame:
    out = df.copy()
    if out.index.tz is None:
        out.index = out.index.tz_localize("UTC")
    else:
        out.index = out.index.tz_convert("UTC")
    out.index = out.index.floor("D")
    out = out[~out.index.duplicated(keep="first")].sort_index()
    return out


def classify_health(live_days: int, live_max_dd_pct: float, live_sharpe: float) -> str:
    """Classify forward paper health state from OOS days, max drawdown %, and Sharpe."""
    if live_days < 14:
        return "WARMUP"
    if live_max_dd_pct < -15.0 or (live_days >= 30 and live_sharpe < -0.5):
        return "KILLED"
    if live_days >= 30 and live_sharpe < 0.3:
        return "DECAYING"
    return "HEALTHY"


def compute_live_metrics(oos_ret: pd.Series) -> dict[str, Any]:
    """Compute strictly forward OOS return, max drawdown, Sharpe, and health state."""
    r = oos_ret.dropna().to_numpy(dtype=float)
    live_days = int(len(r))
    if live_days == 0:
        return {
            "live_days": 0,
            "live_return_pct": 0.0,
            "live_max_dd_pct": 0.0,
            "live_sharpe": 0.0,
            "health": "WARMUP",
        }
    eq = np.cumprod(np.maximum(1.0 + r, 1e-12))
    live_return_pct = float((eq[-1] - 1.0) * 100.0)
    peak = np.maximum(1.0, np.maximum.accumulate(eq))
    dd = eq / peak - 1.0
    live_max_dd_pct = float(np.min(dd) * 100.0)
    std = float(np.std(r, ddof=1)) if live_days > 1 else 0.0
    live_sharpe = float((np.mean(r) / std) * np.sqrt(365.0)) if std > 1e-12 else 0.0
    health = classify_health(live_days, live_max_dd_pct, live_sharpe)
    return {
        "live_days": live_days,
        "live_return_pct": round(live_return_pct, 4),
        "live_max_dd_pct": round(live_max_dd_pct, 4),
        "live_sharpe": round(live_sharpe, 4),
        "health": health,
    }


def _compute_candidate_net_returns(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    features: pd.DataFrame,
    dual_b: pd.Series,
    dual_e: pd.Series,
    expr: str,
    role: str,
    venue: str,
) -> pd.Series:
    """Compute causal close-to-close daily net returns on btc.index."""
    idx = btc.index
    sig = AD.SafeAlphaEvaluator.evaluate(expr, features, role=role).reindex(idx)
    sig = sig.fillna(1.0 if role == "overlay" else 0.0).to_numpy(dtype=float)

    if role == "overlay":
        wb = 0.5 * dual_b.reindex(idx).fillna(0.0).to_numpy(dtype=float) * sig
        we = 0.5 * dual_e.reindex(idx).fillna(0.0).to_numpy(dtype=float) * sig
    elif role == "standalone":
        wb = 0.5 * sig
        we = 0.5 * sig
    else:
        raise ValueError(f"unknown role: {role}")

    costs = lab.US_SPOT if venue == "spot" else lab.US_FUTURES
    if not costs.can_short:
        wb = np.clip(wb, 0.0, None)
        we = np.clip(we, 0.0, None)

    # Strict 1-bar causal execution lag: weights decided at close t earn return on t+1
    held_wb = np.roll(wb, 1)
    held_we = np.roll(we, 1)
    held_wb[0] = 0.0
    held_we[0] = 0.0

    prev_wb = np.roll(held_wb, 1)
    prev_we = np.roll(held_we, 1)
    prev_wb[0] = 0.0
    prev_we[0] = 0.0

    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)

    cost_rate = (costs.taker_bps + costs.slip_bps) * 1e-4
    fund_rate = costs.funding_bps_h * 24.0 * 1e-4
    turn = np.abs(held_wb - prev_wb) + np.abs(held_we - prev_we)
    gross = np.abs(held_wb) + np.abs(held_we)
    net_ret = held_wb * rb + held_we * re - turn * cost_rate - gross * fund_rate
    return pd.Series(net_ret, index=idx, dtype=float)


def _load_shadow_entries(shadow_path: str) -> list[dict[str, Any]]:
    if not os.path.exists(shadow_path):
        return []
    try:
        with open(shadow_path, "r", encoding="utf-8") as fh:
            data = json.load(fh)
    except (json.JSONDecodeError, OSError):
        return []
    if not isinstance(data, list):
        return []
    return [
        item
        for item in data
        if isinstance(item, dict)
        and item.get("status") == "SHADOW_PAPER_ONLY"
        and isinstance(item.get("expr"), str)
        and item.get("expr", "").strip()
    ]


def _make_synthetic_market(entry_days: list[pd.Timestamp], seed: int = 42) -> tuple[pd.DataFrame, pd.DataFrame, pd.DataFrame]:
    n = 400
    btc, eth = AD._make_synthetic_ohlcv(n=n, seed=seed)
    if entry_days:
        end_day = max(entry_days) + pd.Timedelta(days=45)
        idx = pd.date_range(end=end_day, periods=n, freq="1D", tz="UTC")
        btc.index = idx
        eth.index = idx
    rng = np.random.default_rng(seed + 7)
    mm = pd.DataFrame(
        rng.normal(0.0, 1.0, size=(len(btc), len(MF.MULTIMODAL_COLS))),
        index=btc.index,
        columns=list(MF.MULTIMODAL_COLS),
    )
    return btc, eth, mm


def evaluate_shadow_candidates(
    shadow_path: str = DEFAULT_SHADOW_PATH,
    out_path: str = DEFAULT_OUT_PATH,
    daily_csv_dir: str | None = None,
    dry_run_synth: bool = False,
    now_utc: pd.Timestamp | None = None,
) -> dict[str, Any]:
    """Evaluate all SHADOW_PAPER_ONLY entries strictly forward of admission date."""
    eval_ts = (
        pd.Timestamp.now(tz="UTC")
        if now_utc is None
        else (pd.Timestamp(now_utc).tz_localize("UTC") if pd.Timestamp(now_utc).tzinfo is None else pd.Timestamp(now_utc).tz_convert("UTC"))
    )
    entries = _load_shadow_entries(shadow_path)

    evaluated: list[dict[str, Any]] = []
    if entries:
        entry_days: list[pd.Timestamp] = []
        for entry in entries:
            ts_raw = entry.get("timestamp_utc")
            entry_days.append(_to_utc_day(ts_raw) if ts_raw else eval_ts.floor("D"))

        if dry_run_synth:
            btc, eth, mm = _make_synthetic_market(entry_days)
        else:
            csv_dir = os.path.expanduser(daily_csv_dir) if daily_csv_dir else DEFAULT_DAILY_CSV_DIR
            btc = _normalize_daily_df(AD._load_daily_candles("BTC-USD", lab, daily_csv_dir=csv_dir))
            eth_raw = _normalize_daily_df(AD._load_daily_candles("ETH-USD", lab, daily_csv_dir=csv_dir))
            eth = eth_raw.reindex(btc.index).ffill()
            mm_path = os.path.join(csv_dir, "multimodal_1D.csv")
            mm = MF.load_multimodal_csv(mm_path)
            if not mm.empty:
                mm = _normalize_daily_df(mm)

        features = FS.build_unified_features(btc, eth, multimodal=mm)
        qd_b = pd.Series(S35.coherence_positions(btc, lev=1.0), index=btc.index)
        qd_e = pd.Series(S35.coherence_positions(eth, lev=1.0), index=btc.index)
        r3_b = pd.Series(S40.r3_fastexit_qd_positions(btc, lev=1.0), index=btc.index)
        r3_e = pd.Series(S40.r3_fastexit_qd_positions(eth, lev=1.0), index=btc.index)
        dual_b = 0.5 * (qd_b + r3_b)
        dual_e = 0.5 * (qd_e + r3_e)

        bar_days = btc.index.floor("D")

        for entry, entry_day in zip(entries, entry_days):
            expr = str(entry["expr"]).strip()
            role = str(entry.get("role", "overlay"))
            venue = str(entry.get("venue", "futures"))
            rec: dict[str, Any] = {
                "expr": expr,
                "role": role,
                "venue": venue,
                "source": str(entry.get("source", "")),
                "status": "SHADOW_PAPER_ONLY",
                "admitted_at_utc": str(entry.get("timestamp_utc", "")),
                "entry_day_utc": entry_day.strftime("%Y-%m-%d"),
            }
            try:
                net_ret = _compute_candidate_net_returns(
                    btc, eth, features, dual_b, dual_e, expr=expr, role=role, venue=venue
                )
                oos_mask = bar_days > entry_day
                oos_ret = net_ret[oos_mask]
                metrics = compute_live_metrics(oos_ret)
                rec.update(metrics)
                if metrics["live_days"] > 0:
                    rec["first_oos_day_utc"] = oos_ret.index.min().strftime("%Y-%m-%d")
                    rec["last_oos_day_utc"] = oos_ret.index.max().strftime("%Y-%m-%d")
                else:
                    rec["first_oos_day_utc"] = None
                    rec["last_oos_day_utc"] = None
            except Exception as exc:
                rec.update(
                    {
                        "live_days": 0,
                        "live_return_pct": 0.0,
                        "live_max_dd_pct": 0.0,
                        "live_sharpe": 0.0,
                        "health": "WARMUP",
                        "first_oos_day_utc": None,
                        "last_oos_day_utc": None,
                        "error": str(exc),
                    }
                )
            evaluated.append(rec)

    health_counts = {"WARMUP": 0, "HEALTHY": 0, "DECAYING": 0, "KILLED": 0}
    for c in evaluated:
        h = str(c.get("health", "WARMUP"))
        if h in health_counts:
            health_counts[h] += 1

    state = {
        "evaluated_at_utc": eval_ts.isoformat(),
        "dry_run_synth": bool(dry_run_synth),
        "count": len(evaluated),
        "health_counts": health_counts,
        "candidates": evaluated,
    }

    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    tmp_path = out_path + ".tmp"
    with open(tmp_path, "w", encoding="utf-8") as fh:
        json.dump(state, fh, indent=2)
    os.replace(tmp_path, out_path)
    return state


def main() -> None:
    p = argparse.ArgumentParser(description="Evaluate SHADOW_PAPER_ONLY alpha candidates strictly forward OOS.")
    p.add_argument("--dry-run-synth", action="store_true")
    p.add_argument("--daily-csv-dir", default=None)
    p.add_argument("--shadow-path", default=DEFAULT_SHADOW_PATH)
    p.add_argument("--out-path", default=DEFAULT_OUT_PATH)
    args = p.parse_args()

    state = evaluate_shadow_candidates(
        shadow_path=args.shadow_path,
        out_path=args.out_path,
        daily_csv_dir=args.daily_csv_dir,
        dry_run_synth=args.dry_run_synth,
    )
    print(
        f"shadow_paper_eval | count={state['count']} synth={state['dry_run_synth']} "
        f"health={state['health_counts']}"
    )
    for c in state["candidates"]:
        print(
            f"  [{c['venue']}/{c['role']}] {c['health']} | days={c['live_days']} "
            f"ret={c['live_return_pct']:+.2f}% maxDD={c['live_max_dd_pct']:.2f}% "
            f"sharpe={c['live_sharpe']:.3f} | expr={c['expr']}"
        )


if __name__ == "__main__":
    main()
