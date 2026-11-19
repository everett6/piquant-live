"""LoRA SFT fine-tuning of LFM3-1.7B for causal quantitative alpha expression discovery.

Strict anti-leakage protocol:
  - Phase 1 (`build_alpha_dataset`): Generates ~350-500 valid causal AST overlay expressions,
    validates every expression with `SafeAlphaEvaluator.validate` and `check_causality_sandbox`,
    and scores them STRICTLY on `2020-01-01 <= t < 2025-01-01` (discovery window only).
    `2025-01-01..2026-10-31` (`A_geo_monthly_pct`) is NEVER loaded or touched during
    dataset generation or training.
  - Phase 2 (`train_lora`): Fine-tunes `/home/everett/models/LFM3-1.7B` with LoRA SFT
    (`r=16, alpha=32, bf16, lr=2e-4, 3 epochs`) using the exact prompt schema of
    `auto_discover.py` (`enable_thinking=False`), then merges into `merged/`.
  - Phase 3 (`export_gguf`): Converts `merged/` to bf16 GGUF via `llama.cpp-lfm3` and
    quantizes to `Q4_K_M` for Raspberry Pi 5 deployment.

Usage:
  /home/everett/miniconda3/bin/python3 train_lfm3_alpha.py \\
    --base ~/models/LFM3-1.7B \\
    --out ~/models/piquant-alpha-lfm3-v1
"""
from __future__ import annotations

import argparse
import json
import math
import os
import random
import subprocess
import sys
import time
from typing import Any

import numpy as np
import pandas as pd

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import auto_discover as AD
import feature_store as FS

DISCOVERY_START = "2020-01-01"
DISCOVERY_END = "2025-01-01"

SYSTEM_PROMPT = (
    "You are a quantitative crypto researcher. Output ONLY a valid JSON array "
    "of expression strings using ONLY the allowed features and functions."
)

DEFAULT_BASE_MODEL = os.path.expanduser("~/models/LFM3-1.7B")
DEFAULT_OUT_DIR = os.path.expanduser("~/models/piquant-alpha-lfm3-v1")
DEFAULT_CONVERT_SCRIPT = os.path.expanduser("~/llama.cpp-lfm3/convert_hf_to_gguf.py")
DEFAULT_QUANTIZE_BIN = os.path.expanduser("~/llama.cpp-lfm3/build/bin/llama-quantize")

REGIME_HINTS: dict[str, str] = {
    "low_dd": "Focus on defensive low-drawdown overlays that sharply cut exposure during volatility spikes or trend breakdowns.",
    "low_turnover": "Focus on smooth, low-turnover trend overlays using ema() and roll_mean() with medium windows (10-30 days).",
    "vol_regime": "Focus on volatility-regime overlays using vol_ratio_5_20, btc_vol20_ann, eth_vol20_ann, and btc_vol_z20.",
    "flow": "Focus on signed volume-flow overlays combining btc_signed_flow5 and btc_vol_z20 with trend confirmation.",
    "spread": "Focus on cross-asset BTC/ETH spread overlays using pair_spread_z20, btc_minus_eth_5d, and eth_sma50_dist.",
}


def compute_composite_score(metrics: dict[str, float]) -> float:
    """Composite discovery-window score (2020-2024 only)."""
    sharpe = float(metrics.get("sharpe", 0.0))
    calmar = float(metrics.get("calmar", 0.0))
    geo_mo = float(metrics.get("geo_monthly_pct", 0.0))
    mean_dd = float(metrics.get("mean_dd_pct", 10.0))
    return float(sharpe + 0.5 * calmar + 0.3 * geo_mo - 0.1 * max(0.0, mean_dd - 8.5))


def _format_failure_reason(rec: dict[str, Any]) -> str:
    """Format a bottom-half expression's 2020-2024 metrics like auto_discover feedback."""
    reasons: list[str] = []
    if rec["geo_monthly_pct"] < AD.BENCHMARK_GEO_MO_PCT:
        reasons.append(f"Geo/mo {rec['geo_monthly_pct']:.2f}% < {AD.BENCHMARK_GEO_MO_PCT}%")
    if rec["sharpe"] <= AD.BENCHMARK_SHARPE:
        reasons.append(f"Sharpe {rec['sharpe']:.3f} <= {AD.BENCHMARK_SHARPE}")
    if rec["max_dd_pct"] < AD.BENCHMARK_MAX_DD_PCT:
        reasons.append(f"MaxDD {rec['max_dd_pct']:.1f}% < {AD.BENCHMARK_MAX_DD_PCT}%")
    if rec["mean_dd_pct"] > AD.BENCHMARK_MEAN_DD_PCT:
        reasons.append(f"MeanDD {rec['mean_dd_pct']:.1f}% > {AD.BENCHMARK_MEAN_DD_PCT}%")
    if not reasons:
        reasons.append(
            f"Geo/mo {rec['geo_monthly_pct']:.2f}%, Sharpe {rec['sharpe']:.3f}, MaxDD {rec['max_dd_pct']:.1f}%"
        )
    return f"{rec['expr']} -> FULL_GATE: {'; '.join(reasons[:3])}"


def generate_candidate_pool(n_pool: int = 450, seed: int = 42) -> list[dict[str, Any]]:
    """Generate a diverse pool of valid causal AST expressions over ALLOWED_FEATURES."""
    rng = np.random.default_rng(seed)

    trend_feats = ("btc_sma50_dist", "eth_sma50_dist", "btc_ret21", "eth_ret21", "btc_ret5", "eth_ret5")
    vol_feats = ("vol_ratio_5_20", "btc_vol20_ann", "eth_vol20_ann", "btc_vol_z20")
    flow_feats = ("btc_signed_flow5", "btc_vol_z20", "btc_ret1")
    spread_feats = ("pair_spread_z20", "btc_minus_eth_5d", "btc_minus_eth_1d")
    spans_fast = (3, 5, 7, 10)
    spans_slow = (10, 14, 20, 21, 30)

    def _pick(seq: tuple[Any, ...]) -> Any:
        return seq[int(rng.integers(0, len(seq)))]

    def _u(lo: float, hi: float, nd: int = 2) -> float:
        return round(float(rng.uniform(lo, hi)), nd)

    # Seed with diverse hand-crafted archetypes across all 5 regime families
    seeds: list[tuple[str, str]] = [
        ("ema(btc_sma50_dist, 10) * 3.0 - max(vol_ratio_5_20 - 1.2, 0.0) * 1.5", "low_dd"),
        ("tanh(ema(btc_ret21, 5) * 4.0) - clip(btc_vol20_ann - 0.75, 0.0, 1.0) * 2.0", "low_dd"),
        ("where(vol_ratio_5_20 < 1.35, ema(btc_sma50_dist + eth_sma50_dist, 7) * 2.5, -1.0)", "vol_regime"),
        ("ema(btc_signed_flow5, 10) * 2.0 + tanh(btc_sma50_dist * 4.0) - 0.8 * max(eth_vol20_ann - 0.85, 0.0)", "flow"),
        ("clip(ema(btc_ret21 + eth_ret21, 10) * 2.5, -2.0, 2.0) - 0.6 * abs(pair_spread_z20)", "spread"),
        ("roll_mean(btc_sma50_dist, 14) * 3.5 - 1.2 * max(vol_ratio_5_20 - 1.15, 0.0)", "low_turnover"),
        ("where(btc_vol20_ann < 0.80, 0.8 + tanh(btc_ret21 * 3.0), -0.6 * vol_ratio_5_20)", "vol_regime"),
        ("tanh(ema(btc_minus_eth_5d, 10) * 3.0) + 1.5 * tanh(btc_sma50_dist * 3.0)", "spread"),
        ("clip(roll_mean(btc_signed_flow5, 15) * 3.0 + ema(btc_ret21, 12) * 3.0, -2.0, 2.0)", "flow"),
        ("where(btc_sma50_dist > 0.0, 1.0 - max(vol_ratio_5_20 - 1.25, 0.0), -0.5 - abs(btc_ret5) * 3.0)", "low_dd"),
        ("ema(btc_sma50_dist, 21) * 4.0 - max(btc_vol20_ann - 0.70, 0.0) * 2.0", "low_turnover"),
        ("where((btc_sma50_dist > 0.0) & (vol_ratio_5_20 < 1.25), 1.5, -1.0)", "low_dd"),
    ]

    pool: list[dict[str, Any]] = []
    seen: set[str] = set()

    def _try_add(expr: str, regime: str) -> bool:
        expr = expr.strip()
        if not expr or expr in seen or len(expr) >= 160:
            return False
        try:
            AD.SafeAlphaEvaluator.validate(expr)
        except AD.UnsafeExpressionError:
            return False
        ok, _ = AD.check_causality_sandbox(expr, role="overlay")
        if not ok:
            return False
        seen.add(expr)
        pool.append({"expr": expr, "regime": regime})
        return True

    for expr, regime in seeds:
        _try_add(expr, regime)

    families = ("low_dd", "low_turnover", "vol_regime", "flow", "spread", "composite")
    attempts = 0
    max_attempts = n_pool * 12

    while len(pool) < n_pool and attempts < max_attempts:
        attempts += 1
        fam = families[attempts % len(families)]
        t1 = _pick(trend_feats)
        t2 = _pick(trend_feats)
        v1 = _pick(vol_feats)
        f1 = _pick(flow_feats)
        sp1 = _pick(spread_feats)
        sf = _pick(spans_fast)
        ss = _pick(spans_slow)
        w1 = _u(0.8, 4.5)
        w2 = _u(0.4, 2.5)
        w3 = _u(0.2, 1.5)
        thr_v = _u(0.55, 1.35)
        thr_r = _u(-0.02, 0.04)

        variant = int(rng.integers(0, 4))
        if fam == "low_dd":
            if variant == 0:
                expr = f"where({v1} < {thr_v}, tanh(ema({t1}, {sf}) * {w1}), -{w2} * abs({t2}))"
            elif variant == 1:
                expr = f"ema({t1}, {ss}) * {w1} - max({v1} - {thr_v}, 0.0) * {w2}"
            elif variant == 2:
                expr = f"where(({t1} > {thr_r}) & (vol_ratio_5_20 < {_u(1.1, 1.45)}), {w1} * tanh({t2} * 3.0), -{w2})"
            else:
                expr = f"clip(ema({t1}, {sf}) * {w1}, -1.5, 1.5) - clip(btc_vol20_ann - {_u(0.6, 0.9)}, 0.0, 1.0) * {w2}"
        elif fam == "low_turnover":
            if variant == 0:
                expr = f"roll_mean({t1}, {ss}) * {w1} - {w2} * max(roll_mean(vol_ratio_5_20, {sf}) - {_u(1.05, 1.35)}, 0.0)"
            elif variant == 1:
                expr = f"tanh(ema({t1} + {w3} * {t2}, {ss}) * {w1})"
            elif variant == 2:
                expr = f"ema(tanh({t1} * {w1}) - {w2} * max(btc_vol20_ann - {_u(0.65, 0.95)}, 0.0), {ss})"
            else:
                expr = f"clip(roll_mean({t1}, {ss}) * {w1} + ema({t2}, {ss}) * {w2}, -2.0, 2.0)"
        elif fam == "vol_regime":
            if variant == 0:
                expr = f"where(vol_ratio_5_20 < {_u(1.05, 1.45)}, {w1} * ema({t1}, {sf}), -{w2} * vol_ratio_5_20)"
            elif variant == 1:
                expr = f"sigmoid(({thr_v} - btc_vol20_ann) * {w1}) * 2.0 + tanh(ema({t1}, {sf}) * {w2}) - 1.0"
            elif variant == 2:
                expr = f"tanh(ema({t1}, {sf}) * {w1}) - {w2} * max(btc_vol20_ann - eth_vol20_ann, 0.0) - {w3} * abs(btc_vol_z20)"
            else:
                expr = f"where(btc_vol20_ann < {_u(0.60, 0.95)}, 0.5 + ema({t1}, {sf}) * {w1}, -max(vol_ratio_5_20 - 1.0, 0.0) * {w2})"
        elif fam == "flow":
            if variant == 0:
                expr = f"ema({f1}, {sf}) * {w1} + tanh(ema({t1}, {ss}) * {w2}) - {w3} * max(vol_ratio_5_20 - 1.2, 0.0)"
            elif variant == 1:
                expr = f"clip(roll_mean(btc_signed_flow5, {ss}) * {w1} + {w2} * {t1}, -2.0, 2.0)"
            elif variant == 2:
                expr = f"where(ema(btc_signed_flow5, {sf}) > 0.0, tanh({t1} * {w1}), -{w2} * max(vol_ratio_5_20 - 1.1, 0.0))"
            else:
                expr = f"tanh(ema(btc_signed_flow5, {sf}) * {w1}) + {w3} * ema(btc_vol_z20, {ss}) + {w2} * {t1}"
        elif fam == "spread":
            if variant == 0:
                expr = f"tanh(ema({sp1}, {sf}) * {w1}) + {w2} * ema({t1}, {ss}) - {w3} * abs(pair_spread_z20)"
            elif variant == 1:
                expr = f"clip(ema({t1} + {t2}, {sf}) * {w1}, -2.0, 2.0) - {w3} * max(abs(pair_spread_z20) - {_u(1.0, 2.0)}, 0.0)"
            elif variant == 2:
                expr = f"where(abs(pair_spread_z20) < {_u(1.2, 2.2)}, ema(btc_sma50_dist, {sf}) * {w1}, -{w2} * abs(btc_minus_eth_5d))"
            else:
                expr = f"ema(btc_minus_eth_5d, {sf}) * {w1} + tanh(roll_mean(eth_sma50_dist, {ss}) * {w2})"
        else:  # composite -> tag as low_dd or low_turnover based on structure
            if variant == 0:
                expr = f"tanh(ema({t1}, {sf}) * {w1} + ema({f1}, {sf}) * {w3}) - {w2} * max(vol_ratio_5_20 - {_u(1.1, 1.35)}, 0.0)"
                fam = "low_dd"
            elif variant == 1:
                expr = f"clip(ema({t1}, {ss}) * {w1} - {w3} * abs(pair_spread_z20) - {w2} * max(btc_vol20_ann - 0.8, 0.0), -2.0, 2.0)"
                fam = "low_turnover"
            elif variant == 2:
                expr = f"min(ema({t1}, {sf}) * {w1}, ema({t2}, {sf}) * {w1}) - {w2} * max(vol_ratio_5_20 - 1.2, 0.0)"
                fam = "low_dd"
            else:
                expr = f"max(ema({t1}, {ss}) * {w1}, -1.0) + {w3} * tanh(btc_signed_flow5 * 2.0) - {w2} * max(eth_vol20_ann - 0.85, 0.0)"
                fam = "flow"

        _try_add(expr, fam)

    return pool


def load_discovery_context(
    daily_csv_dir: str | None = None,
    start_date: str = DISCOVERY_START,
    end_date: str = DISCOVERY_END,
) -> dict[str, Any]:
    """Load real BTC/ETH data STRICTLY truncated to `< end_date` (default 2025-01-01).

    Anti-leakage guarantee: every time series (candles, funding, DTB3, features, base sleeves)
    is truncated to `< end_date` BEFORE any indicator or simulation runs.
    """
    import lab
    import strategies5 as S5
    import strategies19 as S19
    import strategies35 as S35
    import strategies36 as S36
    import strategies40 as S40
    import strategies42 as S42
    from run_round19 import load_funding
    from run_round33 import DTB3, geo

    end_ts = pd.Timestamp(end_date, tz="UTC")
    start_ts = pd.Timestamp(start_date, tz="UTC")

    full_raw = {c: AD._load_daily_candles(f"{c}-USD", lab, daily_csv_dir=daily_csv_dir) for c in ("BTC", "ETH")}
    # STRICT ANTI-LEAKAGE TRUNCATION before computing ANY indicators or features
    full = {c: df[df.index < end_ts].copy() for c, df in full_raw.items()}
    d = {c: full[c][(full[c].index >= start_ts) & (full[c].index < end_ts)].copy() for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]
    assert len(btc_sub) > 0 and btc_sub.index.max() < end_ts, "Discovery window breached end_date!"
    eth_aligned = eth_sub.reindex(btc_sub.index).ffill()

    qd_full = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    r3_full = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    dual_1x = {
        c: (0.5 * (qd_full[c] + r3_full[c])).reindex(btc_sub.index).fillna(0.0)
        for c in ("BTC", "ETH")
    }

    full_eth_aligned = full["ETH"].reindex(full["BTC"].index).ffill()
    features_full = FS.build_unified_features(full["BTC"], full_eth_aligned)
    features_sub = features_full.reindex(btc_sub.index)

    f_raw = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    f = {c: df[df.index < end_ts].copy() for c, df in f_raw.items()}

    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate_all = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    rate = rate_all[rate_all.index < end_ts].copy()

    c1_base = S19.portfolio([S19.coin_returns(d[c], f[c], "C1", S19.BASE) for c in ("BTC", "ETH")]).reindex(
        btc_sub.index
    ).fillna(0.0)

    return {
        "btc": btc_sub,
        "eth": eth_aligned,
        "features": features_sub,
        "dual_1x": dual_1x,
        "c1_base": c1_base,
        "rate": rate,
        "lab": lab,
        "S5": S5,
        "S36": S36,
        "S42": S42,
        "geo": geo,
        "start_date": start_date,
        "end_date": end_date,
    }


def _load_synthetic_discovery_context(seed: int = 42) -> dict[str, Any]:
    """Fast synthetic discovery context strictly within 2020-2024 for `--dry-run-dataset`."""
    btc, eth = AD._make_synthetic_ohlcv(n=600, seed=seed)
    idx = pd.date_range("2022-01-01", periods=len(btc), freq="1D", tz="UTC")
    btc.index = idx
    eth.index = idx
    features = FS.build_unified_features(btc, eth)
    return {
        "synthetic": True,
        "btc": btc,
        "eth": eth,
        "features": features,
        "start_date": DISCOVERY_START,
        "end_date": DISCOVERY_END,
    }


def score_expression_discovery(expr: str, ctx: dict[str, Any]) -> dict[str, float]:
    """Score one causal overlay expression strictly on the 2020-2024 discovery window."""
    btc_sub = ctx["btc"]
    eth_sub = ctx["eth"]
    sig = AD.SafeAlphaEvaluator.evaluate(expr, ctx["features"], role="overlay")
    turnover = float(sig.diff().abs().mean()) if len(sig) > 1 else 0.0

    if ctx.get("synthetic"):
        rb = btc_sub["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
        re = eth_sub["c"].reindex(btc_sub.index).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
        w = 0.5 * sig.to_numpy(dtype=float)
        held = np.roll(w, 1)
        held[0] = 0.0
        prev = np.roll(held, 1)
        prev[0] = 0.0
        turn = 2.0 * np.abs(held - prev)
        rv = pd.Series(held * (rb + re) - turn * 0.0015, index=btc_sub.index)
        eq = (1.0 + rv).cumprod()
        dd = eq / eq.cummax() - 1.0
        n_mo = max(1.0, len(rv) / 30.4375)
        geo_mo = float(((eq.iloc[-1] ** (1.0 / n_mo)) - 1.0) * 100.0)
        std = float(rv.std(ddof=1))
        sharpe = float((rv.mean() / std) * np.sqrt(365.0)) if std > 1e-12 else 0.0
        max_dd = float(dd.min() * 100.0)
        mean_dd = float((-dd).mean() * 100.0)
        calmar = float((geo_mo * 12.0) / abs(max_dd)) if abs(max_dd) > 1e-9 else 0.0
    else:
        lab = ctx["lab"]
        S5 = ctx["S5"]
        S36 = ctx["S36"]
        S42 = ctx["S42"]
        geo_fn = ctx["geo"]

        pos_b = ctx["dual_1x"]["BTC"] * sig.reindex(btc_sub.index).fillna(1.0)
        pos_e = ctx["dual_1x"]["ETH"] * sig.reindex(btc_sub.index).fillna(1.0)
        cost_cfg = lab.US_FUTURES
        rv, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, pos_b, pos_e, ctx["c1_base"], ctx["rate"],
            trend_share=0.60, carry_share=0.40,
            taker_bps=cost_cfg.taker_bps, slip_bps=cost_cfg.slip_bps,
            funding_bps_h=cost_cfg.funding_bps_h,
            use_taper=True, dd_start=0.06, dd_full=0.20, dd_floor=0.40,
        )
        m_series = S5.complete_months(rv)
        geo_mo = float(geo_fn(m_series))
        summ = lab.summarize(rv)
        sharpe = float(summ["sharpe"])
        max_dd = float(summ["max_dd_pct"])
        dd_diag = S42.drawdown_diagnostics(rv)
        mean_dd = float(dd_diag["mean_dd_pct"])
        calmar = float((geo_mo * 12.0) / abs(max_dd)) if abs(max_dd) > 1e-9 else 0.0

    metrics = {
        "geo_monthly_pct": round(geo_mo, 4),
        "sharpe": round(sharpe, 4),
        "calmar": round(calmar, 4),
        "max_dd_pct": round(max_dd, 4),
        "mean_dd_pct": round(mean_dd, 4),
        "turnover": round(turnover, 5),
    }
    metrics["score"] = round(compute_composite_score(metrics), 5)
    return metrics


def build_alpha_dataset(
    n_pool: int = 450,
    n_examples: int = 360,
    seed: int = 42,
    daily_csv_dir: str | None = None,
    dry_run_synth: bool = False,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    """Phase 1: Generate, score (strictly 2020-2024), rank, and build SFT prompt/response pairs."""
    t0 = time.time()
    pool = generate_candidate_pool(n_pool=n_pool, seed=seed)
    ctx = _load_synthetic_discovery_context(seed=seed) if dry_run_synth else load_discovery_context(
        daily_csv_dir=daily_csv_dir, start_date=DISCOVERY_START, end_date=DISCOVERY_END
    )

    scored: list[dict[str, Any]] = []
    for item in pool:
        m = score_expression_discovery(item["expr"], ctx)
        scored.append({"expr": item["expr"], "regime": item["regime"], **m})

    scored.sort(key=lambda r: r["score"], reverse=True)
    n_total = len(scored)
    top_q_cut = max(12, n_total // 4)
    bot_h_cut = n_total // 2

    top_quartile = scored[:top_q_cut]
    bottom_half = scored[bot_h_cut:]

    # Build regime-specific top pools (top 40% of each regime, or filtered by metric)
    by_regime: dict[str, list[dict[str, Any]]] = {}
    for reg_name in REGIME_HINTS:
        if reg_name == "low_dd":
            subset = sorted(scored[: max(24, n_total // 2)], key=lambda r: (r["max_dd_pct"], r["score"]), reverse=True)
            by_regime[reg_name] = subset[: max(10, len(subset) // 3)]
        elif reg_name == "low_turnover":
            subset = sorted(scored[: max(24, n_total // 2)], key=lambda r: (r["turnover"], -r["score"]))
            by_regime[reg_name] = subset[: max(10, len(subset) // 3)]
        else:
            subset = [r for r in scored if r["regime"] == reg_name]
            subset.sort(key=lambda r: r["score"], reverse=True)
            by_regime[reg_name] = subset[: max(10, len(subset) // 3)] if len(subset) >= 8 else top_quartile

    rng = random.Random(seed + 101)
    sft_examples: list[dict[str, Any]] = []
    regime_names = list(REGIME_HINTS.keys())

    for i in range(n_examples):
        # Mix of n_candidates: 70% = 4, 20% = 6, 10% = 8 so model generalizes across request sizes
        r_n = rng.random()
        if r_n < 0.70:
            n_cand = 4
        elif r_n < 0.90:
            n_cand = 6
        else:
            n_cand = 8

        r_mode = rng.random()
        if r_mode < 0.55:
            # Rejection-feedback generation (exact auto_discover._build_alpha_prompt format)
            k_fb = rng.randint(3, 5)
            fb_recs = rng.sample(bottom_half, min(k_fb, len(bottom_half)))
            fb_lines = [_format_failure_reason(r) for r in fb_recs]
            prompt = AD._build_alpha_prompt(n_cand, recent_feedback=fb_lines)
            targets = [r["expr"] for r in rng.sample(top_quartile, min(n_cand, len(top_quartile)))]
            ptype = "rejection_feedback"
        elif r_mode < 0.65:
            # Cold-start generation (no prior feedback, exact auto_discover format on first run)
            prompt = AD._build_alpha_prompt(n_cand, recent_feedback=None)
            targets = [r["expr"] for r in rng.sample(top_quartile, min(n_cand, len(top_quartile)))]
            ptype = "cold_start"
        else:
            # Regime-targeted generation
            reg_name = regime_names[i % len(regime_names)]
            k_fb = rng.randint(2, 4)
            fb_recs = rng.sample(bottom_half, min(k_fb, len(bottom_half)))
            fb_lines = [_format_failure_reason(r) for r in fb_recs]
            base_prompt = AD._build_alpha_prompt(n_cand, recent_feedback=fb_lines)
            prompt = base_prompt.replace(
                "Goal: low-turnover",
                f"Regime focus: {REGIME_HINTS[reg_name]}\nGoal: low-turnover",
            )
            reg_pool = by_regime[reg_name]
            targets = [r["expr"] for r in rng.sample(reg_pool, min(n_cand, len(reg_pool)))]
            ptype = f"regime_{reg_name}"

        response = json.dumps(targets)
        sft_examples.append({
            "system": SYSTEM_PROMPT,
            "prompt": prompt,
            "response": response,
            "target_exprs": targets,
            "prompt_type": ptype,
            "n_candidates": len(targets),
            "discovery_window": [DISCOVERY_START, DISCOVERY_END],
        })

    dt_s = time.time() - t0
    print(
        f"[Phase 1] Built {len(sft_examples)} SFT pairs from {len(scored)} valid causal expressions "
        f"(window={DISCOVERY_START}..{DISCOVERY_END}, top score={scored[0]['score']:.3f}, "
        f"median score={scored[n_total // 2]['score']:.3f}) in {dt_s:.1f}s",
        flush=True,
    )
    return sft_examples, scored


def _prompt_ids(tok: Any, system: str, prompt: str) -> list[int]:
    msgs = [{"role": "system", "content": system}, {"role": "user", "content": prompt}]
    ids = tok.apply_chat_template(msgs, add_generation_prompt=True, enable_thinking=False, tokenize=True)
    if hasattr(ids, "keys"):
        ids = ids["input_ids"]
    return list(ids)


def _encode_example(tok: Any, ex: dict[str, Any], max_len: int) -> tuple[list[int], list[int]]:
    p = _prompt_ids(tok, ex["system"], ex["prompt"])
    a = tok(ex["response"] + tok.eos_token, add_special_tokens=False)["input_ids"]
    ids = (p + a)[:max_len]
    labels = ([-100] * len(p) + a)[:max_len]
    return ids, labels


def _patch_lfm3_rope_compat() -> None:
    """Compat shim for transformers>=5.17 where standardize_rope_params indexes non-attention layer_types."""
    from transformers.configuration_utils import PretrainedConfig

    orig = getattr(PretrainedConfig, "standardize_rope_params", None)
    if orig is None or getattr(orig, "_lfm3_patched", False):
        return

    def _wrapped(self: Any) -> Any:
        rp = getattr(self, "rope_parameters", None)
        lts = getattr(self, "layer_types", None)
        added: list[str] = []
        if isinstance(rp, dict) and lts and not set(rp.keys()).isdisjoint(lts):
            for k in [k for k, v in list(rp.items()) if not isinstance(v, dict) and v is not None]:
                rp.pop(k, None)
            for lt in set(lts):
                if lt not in rp:
                    rp[lt] = None
                    added.append(lt)
        try:
            return orig(self)
        finally:
            if isinstance(rp, dict):
                for lt in added:
                    if rp.get(lt) is None:
                        rp.pop(lt, None)

    _wrapped._lfm3_patched = True  # type: ignore[attr-defined]
    PretrainedConfig.standardize_rope_params = _wrapped  # type: ignore[method-assign]


def train_lora(
    sft_examples: list[dict[str, Any]],
    base_model: str = DEFAULT_BASE_MODEL,
    out_dir: str = DEFAULT_OUT_DIR,
    epochs: int = 3,
    batch: int = 16,
    micro_batch: int = 4,
    lr: float = 2e-4,
    rank: int = 16,
    max_len: int = 1024,
    merge: bool = True,
) -> dict[str, Any]:
    """Phase 2: LoRA SFT fine-tuning of LFM3-1.7B on local GPU + optional merge."""
    import torch
    from peft import LoraConfig, get_peft_model
    from transformers import AutoModelForCausalLM, AutoTokenizer

    _patch_lfm3_rope_compat()
    t0 = time.time()
    tok = AutoTokenizer.from_pretrained(base_model, trust_remote_code=True)
    if tok.pad_token_id is None:
        tok.pad_token_id = tok.eos_token_id

    rows = [_encode_example(tok, ex, max_len=max_len) for ex in sft_examples]
    mean_toks = float(np.mean([len(i) for i, _ in rows]))
    max_toks = int(max(len(i) for i, _ in rows))
    print(f"[Phase 2] SFT rows={len(rows)} mean_tokens={mean_toks:.0f} max_tokens={max_toks}", flush=True)

    model = AutoModelForCausalLM.from_pretrained(
        base_model, dtype=torch.bfloat16, trust_remote_code=True, device_map="cuda"
    )
    # Restore "conv" layer_type if transformers>=5.17 renamed it to "linear_attention"
    if getattr(model.config, "layer_types", None):
        model.config.layer_types = [
            "conv" if lt == "linear_attention" else lt for lt in model.config.layer_types
        ]
    model.gradient_checkpointing_enable()
    model.enable_input_require_grads()

    targets = sorted({
        n.split(".")[-1]
        for n, m in model.named_modules()
        if isinstance(m, torch.nn.Linear) and not n.endswith("lm_head")
    })
    lora_cfg = LoraConfig(
        r=rank,
        lora_alpha=2 * rank,
        lora_dropout=0.05,
        target_modules=targets,
        task_type="CAUSAL_LM",
    )
    model = get_peft_model(model, lora_cfg)
    model.print_trainable_parameters()

    opt = torch.optim.AdamW([p for p in model.parameters() if p.requires_grad], lr=lr, weight_decay=0.0)
    steps = math.ceil(len(rows) / batch) * epochs
    warmup = max(1, min(10, steps // 8))
    sched = torch.optim.lr_scheduler.LambdaLR(
        opt,
        lambda s: min(1.0, (s + 1) / warmup) * max(0.05, 0.5 * (1.0 + math.cos(math.pi * s / max(1, steps)))),
    )

    pad = tok.pad_token_id
    model.train()
    step = 0
    loss_history: list[float] = []

    for ep in range(epochs):
        random.Random(ep + 7).shuffle(rows)
        for b in range(0, len(rows), batch):
            chunk = rows[b : b + batch]
            n_tok = sum(sum(1 for x in l if x != -100) for _, l in chunk)
            if n_tok == 0:
                continue
            loss_sum = 0.0
            for m in range(0, len(chunk), micro_batch):
                part = chunk[m : m + micro_batch]
                L = max(len(i) for i, _ in part)
                ids = torch.tensor([i + [pad] * (L - len(i)) for i, _ in part], device="cuda")
                lab = torch.tensor([l + [-100] * (L - len(l)) for _, l in part], device="cuda")
                lens = torch.tensor([len(i) for i, _ in part], device="cuda")
                att = (torch.arange(L, device="cuda")[None, :] < lens[:, None]).long()
                part_tok = sum(sum(1 for x in l if x != -100) for _, l in part)
                if part_tok == 0:
                    continue
                out = model(input_ids=ids, attention_mask=att, labels=lab)
                (out.loss * (part_tok / n_tok)).backward()
                loss_sum += float(out.loss.item()) * (part_tok / n_tok)

            torch.nn.utils.clip_grad_norm_(model.parameters(), 1.0)
            opt.step()
            sched.step()
            opt.zero_grad(set_to_none=True)
            step += 1
            loss_history.append(round(loss_sum, 4))
            if step == 1 or step % 10 == 0 or step == steps:
                print(
                    f"epoch {ep + 1}/{epochs} step {step}/{steps} loss={loss_sum:.4f} "
                    f"lr={sched.get_last_lr()[0]:.2e} elapsed={time.time() - t0:.0f}s",
                    flush=True,
                )

    os.makedirs(out_dir, exist_ok=True)
    adapter_dir = os.path.join(out_dir, "adapter")
    model.save_pretrained(adapter_dir)

    merged_dir = os.path.join(out_dir, "merged")
    if merge:
        merged = model.merge_and_unload()
        merged.save_pretrained(merged_dir)
        tok.save_pretrained(merged_dir)
        for fname in ("config.json", "configuration_lfm3.py", "modeling_lfm3.py", "lfm3_conv.py", "chat_template.jinja"):
            src = os.path.join(base_model, fname)
            if os.path.exists(src):
                with open(src, "rb") as fin, open(os.path.join(merged_dir, fname), "wb") as fout:
                    fout.write(fin.read())
        print(f"[Phase 2] Merged weights saved to {merged_dir}", flush=True)

    train_time_s = round(time.time() - t0, 1)
    # Free VRAM cleanly before GGUF export
    del model
    torch.cuda.empty_cache()

    return {
        "n_examples": len(rows),
        "epochs": epochs,
        "steps": step,
        "initial_loss": loss_history[0] if loss_history else None,
        "final_loss": loss_history[-1] if loss_history else None,
        "train_time_s": train_time_s,
        "adapter_dir": adapter_dir,
        "merged_dir": merged_dir if merge else None,
    }


def export_gguf(
    merged_dir: str,
    out_dir: str,
    model_name: str = "piquant-alpha-lfm3-v1",
    convert_script: str = DEFAULT_CONVERT_SCRIPT,
    quantize_bin: str = DEFAULT_QUANTIZE_BIN,
    quant_type: str = "Q4_K_M",
    keep_bf16: bool = False,
) -> dict[str, Any]:
    """Phase 3: Convert merged HF weights to bf16 GGUF and quantize to Q4_K_M."""
    t0 = time.time()
    bf16_path = os.path.join(out_dir, f"{model_name}-bf16.gguf")
    quant_path = os.path.join(out_dir, f"{model_name}-{quant_type}.gguf")

    cmd_convert = [
        sys.executable,
        os.path.expanduser(convert_script),
        merged_dir,
        "--outtype",
        "bf16",
        "--outfile",
        bf16_path,
    ]
    print(f"[Phase 3] Converting to bf16 GGUF: {' '.join(cmd_convert)}", flush=True)
    subprocess.run(cmd_convert, stdin=subprocess.DEVNULL, check=True)

    cmd_quant = [
        os.path.expanduser(quantize_bin),
        bf16_path,
        quant_path,
        quant_type,
    ]
    print(f"[Phase 3] Quantizing to {quant_type}: {' '.join(cmd_quant)}", flush=True)
    subprocess.run(cmd_quant, check=True)

    if not os.path.isfile(quant_path) or os.path.getsize(quant_path) < 100_000_000:
        raise RuntimeError(f"Quantized GGUF missing or unexpectedly small: {quant_path}")

    quant_size_mb = round(os.path.getsize(quant_path) / (1024 * 1024), 1)
    if not keep_bf16 and os.path.exists(bf16_path):
        os.remove(bf16_path)
        print(f"[Phase 3] Removed intermediate {bf16_path} to save disk space", flush=True)

    dt_s = round(time.time() - t0, 1)
    print(f"[Phase 3] Exported {quant_path} ({quant_size_mb} MB) in {dt_s}s", flush=True)
    return {
        "gguf_path": quant_path,
        "gguf_size_mb": quant_size_mb,
        "export_time_s": dt_s,
    }


def main() -> None:
    ap = argparse.ArgumentParser(description="Fine-tune LFM3-1.7B with LoRA SFT for causal alpha discovery.")
    ap.add_argument("--base", default=DEFAULT_BASE_MODEL)
    ap.add_argument("--out", default=DEFAULT_OUT_DIR)
    ap.add_argument("--daily-csv-dir", default=None)
    ap.add_argument("--n-pool", type=int, default=450)
    ap.add_argument("--n-examples", type=int, default=360)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--epochs", type=int, default=3)
    ap.add_argument("--batch", type=int, default=16)
    ap.add_argument("--micro-batch", type=int, default=4)
    ap.add_argument("--lr", type=float, default=2e-4)
    ap.add_argument("--rank", type=int, default=16)
    ap.add_argument("--max-len", type=int, default=1024)
    ap.add_argument("--convert-script", default=DEFAULT_CONVERT_SCRIPT)
    ap.add_argument("--quantize-bin", default=DEFAULT_QUANTIZE_BIN)
    ap.add_argument("--quant-type", default="Q4_K_M")
    ap.add_argument("--keep-bf16", action="store_true")
    ap.add_argument("--skip-gguf", action="store_true")
    ap.add_argument("--dry-run-dataset", action="store_true", help="Fast synthetic dataset build only (no GPU training)")
    args = ap.parse_args()

    out_dir = os.path.expanduser(args.out)
    os.makedirs(out_dir, exist_ok=True)

    sft_examples, scored_pool = build_alpha_dataset(
        n_pool=40 if args.dry_run_dataset else args.n_pool,
        n_examples=20 if args.dry_run_dataset else args.n_examples,
        seed=args.seed,
        daily_csv_dir=args.daily_csv_dir,
        dry_run_synth=args.dry_run_dataset,
    )

    dataset_path = os.path.join(out_dir, "train_dataset.jsonl")
    with open(dataset_path, "w", encoding="utf-8") as fh:
        for ex in sft_examples:
            fh.write(json.dumps(ex) + "\n")

    pool_path = os.path.join(out_dir, "scored_pool.json")
    with open(pool_path, "w", encoding="utf-8") as fh:
        json.dump(scored_pool, fh, indent=2)

    if args.dry_run_dataset:
        print(f"[Dry-Run] Saved {len(sft_examples)} examples to {dataset_path} and exited.")
        return

    train_info = train_lora(
        sft_examples=sft_examples,
        base_model=os.path.expanduser(args.base),
        out_dir=out_dir,
        epochs=args.epochs,
        batch=args.batch,
        micro_batch=args.micro_batch,
        lr=args.lr,
        rank=args.rank,
        max_len=args.max_len,
        merge=True,
    )

    report: dict[str, Any] = {
        "discovery_window": [DISCOVERY_START, DISCOVERY_END],
        "n_pool": len(scored_pool),
        "n_examples": len(sft_examples),
        "top5_discovery_pool": scored_pool[:5],
        "train": train_info,
    }

    if not args.skip_gguf and train_info.get("merged_dir"):
        gguf_info = export_gguf(
            merged_dir=str(train_info["merged_dir"]),
            out_dir=out_dir,
            model_name=os.path.basename(out_dir.rstrip("/")) or "piquant-alpha-lfm3-v1",
            convert_script=args.convert_script,
            quantize_bin=args.quantize_bin,
            quant_type=args.quant_type,
            keep_bf16=args.keep_bf16,
        )
        report["gguf"] = gguf_info

    report_path = os.path.join(out_dir, "train_report.json")
    with open(report_path, "w", encoding="utf-8") as fh:
        json.dump(report, fh, indent=2)
    print(f"[Done] Report written to {report_path}", flush=True)


if __name__ == "__main__":
    main()
