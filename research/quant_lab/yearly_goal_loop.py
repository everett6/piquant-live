"""Iterative Per-Year Optimization & Bug-Diagnosis Loop targeting >= +50%/yr in EVERY calendar year.

Evaluates strictly causal strategies (t-1 signal -> t return) with realistic transaction costs
(taker + slippage + funding) across 2020..2026, diagnoses the worst-performing bottleneck year
each iteration, and mutates/optimizes strategy architecture & parameters until either:
  1) Every calendar year achieves >= +50% annualized return (both full-sample & walk-forward), OR
  2) The iteration budget is exhausted (logging the exact frontier and bottleneck).

Supports two realistic venue modes:
  - "us_spot": 1x long-only spot + DTB3 idle cash (2 bps taker + 1 bps slip)
  - "long_short_perp": Long/Short + vol-targeted leverage cap (2 bps taker + 1 bps slip + funding)
"""
from __future__ import annotations

import json
import math
import os
from dataclasses import asdict, dataclass
from typing import Any

import numpy as np
import pandas as pd

import lab
import strategies19 as S19
import strategies35 as S35
import strategies36 as S36
import strategies40 as S40
from run_round19 import END, START, load_funding
from run_round33 import DTB3

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_STATE_PATH = os.path.join(HERE, "results", "yearly_goal_loop_state.json")

ALTS = [
    "SOL", "ETH", "BTC", "AVAX", "LINK", "DOGE", "XRP",
    "ADA", "LTC", "BCH", "DOT", "ETC", "HBAR", "UNI", "XLM",
]
TRAIN_COINS = ["BTC", "ETH", "SOL", "AVAX", "LINK", "LTC", "BCH"]
HOLDOUT_COINS = ["ADA", "DOGE", "DOT", "ETC", "HBAR", "SHIB", "UNI", "XLM", "XRP"]
EQUITY_TICKERS = ["SPY", "QQQ", "IWM", "TLT", "GLD", "NVDA", "AAPL", "MSFT", "AMZN", "META"]
TRAIN_CUTOFF = "2024-01-01"


def _load_daily_csv(path: str) -> pd.DataFrame:
    df = pd.read_csv(os.path.expanduser(path))
    if "t" in df.columns:
        idx = pd.to_datetime(df["t"], utc=True)
        out = df.drop(columns=["t"]).copy()
        out.index = idx
    else:
        idx = pd.to_datetime(df["date"], utc=True)
        cols = {"c": df["close"].to_numpy(dtype=float)}
        for col_src, col_dst in (("open", "o"), ("high", "h"), ("low", "l"), ("volume", "v")):
            if col_src in df.columns:
                cols[col_dst] = df[col_src].to_numpy(dtype=float)
        out = pd.DataFrame(cols, index=idx)
    return out.sort_index()


def load_universe(coins: list[str] | None = None) -> dict[str, Any]:
    """Load requested coins (or all 15), multimodal_1D.csv, DTB3, and funding."""
    target = list(coins) if coins is not None else list(ALTS)
    daily: dict[str, pd.DataFrame] = {}
    if "BTC" in target:
        daily["BTC"] = _load_daily_csv("~/piquant-collect/daily_csv/BTC-USD_1D.csv")
    if "ETH" in target:
        daily["ETH"] = _load_daily_csv("~/piquant-collect/daily_csv/ETH-USD_1D.csv")
    for coin in target:
        if coin in ("BTC", "ETH"):
            continue
        p = os.path.expanduser(f"~/piquant-data/alts/{coin}.csv")
        if os.path.exists(p):
            daily[coin] = _load_daily_csv(p)

    mm_path = os.path.expanduser("~/piquant-collect/daily_csv/multimodal_1D.csv")
    mm = pd.read_csv(mm_path, index_col=0, parse_dates=True)
    if mm.index.tz is None:
        mm.index = mm.index.tz_localize("UTC")

    raw_rate = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(
        raw_rate["rate"].to_numpy(dtype=float),
        index=raw_rate["date"].dt.tz_localize("UTC"),
    ).dropna()

    f_btc = load_funding("BTCUSDT")
    f_eth = load_funding("ETHUSDT")

    ref_coin = "BTC" if "BTC" in daily else next(iter(daily.keys()))
    idx = daily[ref_coin].loc[(daily[ref_coin].index >= START) & (daily[ref_coin].index < END)].index
    return {
        "daily": daily,
        "mm": mm.reindex(idx).ffill().fillna(0.0),
        "rate": rate.reindex(idx, method="ffill").fillna(0.0),
        "f_btc": f_btc,
        "f_eth": f_eth,
        "idx": idx,
        "annual_days": 365,
        "has_carry": True,
    }


def load_equities_universe(
    tickers: list[str] | None = None,
    cache_dir: str = "~/piquant-data/equities",
) -> dict[str, Any]:
    """Load daily US equity/ETF OHLCV (cached in ~/piquant-data/equities, fetched from Yahoo if missing)."""
    import requests

    target = list(tickers) if tickers is not None else list(EQUITY_TICKERS)
    cdir = os.path.expanduser(cache_dir)
    os.makedirs(cdir, exist_ok=True)
    daily: dict[str, pd.DataFrame] = {}

    for sym in target:
        csv_p = os.path.join(cdir, f"{sym}.csv")
        if not os.path.exists(csv_p):
            url = f"https://query1.finance.yahoo.com/v8/finance/chart/{sym}?interval=1d&range=10y"
            resp = requests.get(url, headers={"User-Agent": "Mozilla/5.0"}, timeout=15)
            resp.raise_for_status()
            res = resp.json()["chart"]["result"][0]
            ts = res["timestamp"]
            q = res["indicators"]["quote"][0]
            df = pd.DataFrame({
                "date": pd.to_datetime(ts, unit="s", utc=True).normalize(),
                "open": q["open"],
                "high": q["high"],
                "low": q["low"],
                "close": q["close"],
                "volume": q["volume"],
            }).dropna(subset=["close"]).drop_duplicates(subset=["date"])
            df.to_csv(csv_p, index=False)
        daily[sym] = _load_daily_csv(csv_p)

    raw_rate = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(
        raw_rate["rate"].to_numpy(dtype=float),
        index=raw_rate["date"].dt.tz_localize("UTC"),
    ).dropna()

    ref = "SPY" if "SPY" in daily else next(iter(daily.keys()))
    idx = daily[ref].loc[(daily[ref].index >= START) & (daily[ref].index < END)].index
    zero_s = pd.Series(0.0, index=idx)
    return {
        "daily": daily,
        "mm": pd.DataFrame({"micro_tox_score": 0.3, "news_sent_3d": 0.0}, index=idx),
        "rate": rate.reindex(idx, method="ffill").fillna(0.0),
        "f_btc": zero_s,
        "f_eth": zero_s,
        "idx": idx,
        "annual_days": 252,
        "has_carry": False,
    }


def evaluate_by_year(ret: pd.Series, min_days: int = 120, annual_days: int = 365) -> dict[str, Any]:
    """Compute per-calendar-year raw & annualized return, Sharpe, and max DD."""
    years: dict[str, dict[str, float]] = {}
    ann_list: list[tuple[int, float]] = []
    for yr, grp in ret.groupby(ret.index.year):
        n = len(grp)
        if n < min_days:
            continue
        raw = float((1.0 + grp).prod() - 1.0)
        ann = float((1.0 + raw) ** (float(annual_days) / n) - 1.0) if raw > -0.999 else -0.999
        eq = (1.0 + grp).cumprod()
        dd = float((1.0 - eq / eq.cummax()).max())
        std = float(grp.std(ddof=1))
        sharpe = float((grp.mean() / std) * math.sqrt(float(annual_days))) if std > 1e-12 else 0.0
        years[str(int(yr))] = {
            "days": int(n),
            "raw_pct": round(raw * 100.0, 2),
            "ann_pct": round(ann * 100.0, 2),
            "max_dd_pct": round(dd * 100.0, 2),
            "sharpe": round(sharpe, 3),
        }
        ann_list.append((int(yr), ann * 100.0))

    if not ann_list:
        return {"years": {}, "min_ann_pct": -100.0, "worst_year": None, "years_ge_50": 0}

    worst_yr, min_ann = min(ann_list, key=lambda x: x[1])
    eq_all = (1.0 + ret).cumprod()
    total_dd = float((1.0 - eq_all / eq_all.cummax()).max()) * 100.0
    total_ann = float(((eq_all.iloc[-1]) ** (float(annual_days) / max(1, len(ret))) - 1.0) * 100.0)
    std_all = float(ret.std(ddof=1))
    sharpe_all = float((ret.mean() / std_all) * math.sqrt(float(annual_days))) if std_all > 1e-12 else 0.0

    return {
        "years": years,
        "worst_year": int(worst_yr),
        "min_ann_pct": round(min_ann, 2),
        "years_ge_50": sum(1 for _, v in ann_list if v >= 50.0),
        "total_years": len(ann_list),
        "cagr_pct": round(total_ann, 2),
        "max_dd_pct": round(total_dd, 2),
        "sharpe": round(sharpe_all, 3),
    }


@dataclass
class StrategyConfig:
    venue: str = "us_spot"           # "us_spot" or "long_short_perp"
    top_k: int = 3                   # top-K / bottom-K coins
    mom_fast: int = 7                # fast momentum lookback (days)
    mom_slow: int = 28               # slow momentum lookback (days)
    trend_weight: float = 0.50       # directional dual-speed coherence trend
    xs_weight: float = 0.35          # cross-sectional top-K / bottom-K spread
    mr_weight: float = 0.15          # short-term oversold/overbought mean-reversion
    short_scale: float = 1.0         # 0.0 for long-only; >0 for shorting
    tox_cut: float = 0.65            # scale down directional when micro_tox_score > tox_cut
    news_boost: float = 0.20         # tilt from news_sent_3d
    target_vol: float = 0.35         # 0 = no vol-targeting; >0 = target annualized vol
    max_lev: float = 2.0             # max gross leverage
    dd_start: float = 0.12
    dd_full: float = 0.30
    dd_floor: float = 0.45
    carry_share: float = 0.20        # optional carry sleeve (0..0.4)


def precompute_signals(universe: dict[str, Any]) -> dict[str, Any]:
    """Precompute causal per-coin/asset returns, trend positions, and RSI/vol arrays."""
    daily = universe["daily"]
    idx = universe["idx"]
    coins = list(daily.keys())
    ann_days = int(universe.get("annual_days", 365))

    px_df = pd.DataFrame({c: daily[c]["c"].reindex(idx) for c in coins}, index=idx)
    ret_df = px_df.pct_change().fillna(0.0)
    vol20_df = ret_df.rolling(20, min_periods=10).std() * math.sqrt(float(ann_days))

    # Dual-speed coherence trend (strictly causal: computed on full daily, reindexed)
    trend_pos: dict[str, np.ndarray] = {}
    for c in coins:
        full_c = daily[c]
        qd = pd.Series(S35.coherence_positions(full_c, lev=1.0), index=full_c.index)
        r3 = pd.Series(S40.r3_fastexit_qd_positions(full_c, lev=1.0), index=full_c.index)
        dual = (0.5 * (qd + r3)).reindex(idx).fillna(0.0)
        trend_pos[c] = dual.to_numpy(dtype=float)

    # Causal 5-day RSI-style z-score for mean-reversion dip/rip detection
    ret5 = px_df.pct_change(5)
    ret5_z = (ret5 - ret5.rolling(60, min_periods=20).mean()) / (
        ret5.rolling(60, min_periods=20).std().clip(lower=1e-4)
    )

    # Carry sleeve (BTC + ETH) only when crypto carry is available and BTC/ETH are in universe
    has_carry = bool(universe.get("has_carry", True)) and ("BTC" in daily) and ("ETH" in daily)
    if has_carry:
        c1_ret = S19.portfolio([
            S19.coin_returns(daily["BTC"].reindex(idx).ffill(), universe["f_btc"], "C1", S19.BASE),
            S19.coin_returns(daily["ETH"].reindex(idx).ffill(), universe["f_eth"], "C1", S19.BASE),
        ]).reindex(idx).fillna(0.0).to_numpy(dtype=float)
    else:
        c1_ret = np.zeros(len(idx), dtype=float)

    return {
        "idx": idx,
        "coins": coins,
        "px_df": px_df,
        "ret_df": ret_df,
        "vol20_df": vol20_df,
        "trend_pos": trend_pos,
        "ret5_z": ret5_z.fillna(0.0),
        "c1_ret": c1_ret,
        "has_carry": has_carry,
        "annual_days": ann_days,
        "mm": universe["mm"],
        "rate": universe["rate"].to_numpy(dtype=float) / 100.0 / float(ann_days),
    }


def simulate_config(pre: dict[str, Any], cfg: StrategyConfig) -> pd.Series:
    """Strictly causal close-to-close simulation of StrategyConfig."""
    idx = pre["idx"]
    coins = pre["coins"]
    n = len(idx)
    ann_days = int(pre.get("annual_days", 365))
    has_carry = bool(pre.get("has_carry", True))
    px_df = pre["px_df"]
    ret_arr = {c: pre["ret_df"][c].to_numpy(dtype=float) for c in coins}
    vol_arr = {c: pre["vol20_df"][c].to_numpy(dtype=float) for c in coins}
    tr_arr = pre["trend_pos"]
    r5z_arr = {c: pre["ret5_z"][c].to_numpy(dtype=float) for c in coins}

    # Causal blended momentum score at close t (used for t -> t+1)
    mom_f = px_df.pct_change(cfg.mom_fast)
    mom_s = px_df.pct_change(cfg.mom_slow)
    mom_score = (0.5 * mom_f + 0.5 * mom_s) / (pre["vol20_df"].clip(lower=0.10))
    mom_np = {c: mom_score[c].to_numpy(dtype=float) for c in coins}

    mm = pre["mm"]
    tox = mm["micro_tox_score"].to_numpy(dtype=float) if "micro_tox_score" in mm.columns else np.full(n, 0.3)
    nsent = mm["news_sent_3d"].to_numpy(dtype=float) if "news_sent_3d" in mm.columns else np.zeros(n)

    cost_rate = 3.0 * 1e-4 if ann_days == 365 else 1.5 * 1e-4  # 3 bps crypto; 1.5 bps US equities/ETFs
    fund_rate = (0.0001 if ann_days == 365 else 0.00005) if cfg.venue == "long_short_perp" else 0.0

    out_ret = np.zeros(n, dtype=float)
    eq = 1.0
    peak = 1.0
    prev_w = {c: 0.0 for c in coins}
    held_w = {c: 0.0 for c in coins}
    port_vol_buf: list[float] = []
    eff_carry = cfg.carry_share if has_carry else 0.0
    trend_share = 1.0 - eff_carry

    for t in range(n):
        # Realize return from t-1 -> t using held_w decided at t-1
        turn = sum(abs(held_w[c] - prev_w[c]) for c in coins)
        gross = sum(abs(held_w[c]) for c in coins)
        pnl = sum(held_w[c] * ret_arr[c][t] for c in coins)
        net_trend = trend_share * (pnl - turn * cost_rate - gross * fund_rate)
        idle = max(0.0, trend_share * (1.0 - gross))
        bills = 0.0 if t == 0 else idle * pre["rate"][t - 1]
        total_r = net_trend + eff_carry * pre["c1_ret"][t] + bills
        out_ret[t] = total_r

        port_vol_buf.append(total_r)
        eq = max(1e-12, eq * (1.0 + total_r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        dd_mult = S36.soft_taper(dd, dd_start=cfg.dd_start, dd_full=cfg.dd_full, floor=cfg.dd_floor)

        # Decide target weights at close t for t -> t+1
        # Rank valid coins by mom_score
        scored = []
        for c in coins:
            m = mom_np[c][t]
            v = vol_arr[c][t]
            if np.isfinite(m) and np.isfinite(v) and v > 0.05:
                scored.append((m, c, v))
        scored.sort(reverse=True)

        raw_w = {c: 0.0 for c in coins}
        active_coins = [c for _, c, _ in scored]
        if active_coins:
            inv_v = {c: 1.0 / max(vol_arr[c][t], 0.15) for c in active_coins}
            tot_iv = sum(inv_v.values())
            breadth = sum(1 for c in active_coins if tr_arr[c][t] > 0.25) / len(active_coins)
            bear_regime = (cfg.venue == "long_short_perp") and (cfg.short_scale > 0.0) and (breadth < 0.35)

            # Sleeve 1: Directional dual-speed coherence trend (inverse-vol weighted)
            w_tr = {c: (inv_v[c] / tot_iv) * tr_arr[c][t] for c in active_coins}
            if bear_regime:
                # Only short in confirmed broad bear breadth (<35% uptrending) & negative momentum
                for m, c, _ in scored:
                    if tr_arr[c][t] <= 0.0 and m < 0.0:
                        w_tr[c] = -(inv_v[c] / tot_iv) * cfg.short_scale
            tr_gross = sum(abs(v) for v in w_tr.values())
            if tr_gross > 1.0:
                for c in active_coins:
                    w_tr[c] /= tr_gross

            # Sleeve 2: Cross-sectional Top-K momentum (with Bottom-K short leg ONLY in bear_regime)
            w_xs = {c: 0.0 for c in active_coins}
            k = min(cfg.top_k, len(scored) // 2) if bear_regime else min(cfg.top_k, len(scored))
            if k >= 1:
                long_c = [c for m, c, _ in scored[:k] if m > 0.0]
                short_c = [c for m, c, _ in scored[-k:] if m < 0.0] if bear_regime else []
                if long_c:
                    iv_l = sum(inv_v[c] for c in long_c)
                    long_budget = 0.5 if short_c else 1.0
                    for c in long_c:
                        w_xs[c] += long_budget * (inv_v[c] / iv_l)
                if short_c:
                    iv_s = sum(inv_v[c] for c in short_c)
                    short_budget = 0.5 if long_c else min(1.0, cfg.short_scale)
                    for c in short_c:
                        w_xs[c] -= short_budget * (inv_v[c] / iv_s)

            # Sleeve 3: Short-term 5d mean-reversion (oversold bounce; overbought fade only in bear_regime)
            w_mr = {c: 0.0 for c in active_coins}
            if cfg.mr_weight > 0.0:
                mr_long = [c for c in active_coins if r5z_arr[c][t] < -1.5]
                mr_short = [c for c in active_coins if r5z_arr[c][t] > 2.0] if bear_regime else []
                for c in mr_long:
                    w_mr[c] += 1.0 / max(1, len(mr_long) + len(mr_short))
                for c in mr_short:
                    w_mr[c] -= 1.0 / max(1, len(mr_long) + len(mr_short))

            for c in active_coins:
                comb = cfg.trend_weight * w_tr[c] + cfg.xs_weight * w_xs[c] + cfg.mr_weight * w_mr[c]
                if cfg.venue == "us_spot":
                    comb = max(0.0, comb)
                raw_w[c] = comb

        # Microstructure toxicity & news tilt
        tox_mult = 0.5 if tox[t] > cfg.tox_cut else 1.0
        news_mult = max(0.6, min(1.3, 1.0 + cfg.news_boost * nsent[t]))

        # Causal portfolio vol-targeting
        vol_mult = 1.0
        if cfg.target_vol > 0.0 and len(port_vol_buf) >= 20:
            realized_ann = float(np.std(port_vol_buf[-20:], ddof=1) * math.sqrt(float(ann_days)))
            if realized_ann > 0.05:
                vol_mult = min(cfg.max_lev, cfg.target_vol / realized_ann)

        scale = min(cfg.max_lev, dd_mult * tox_mult * news_mult * vol_mult)
        for c in coins:
            prev_w[c] = held_w[c]
            held_w[c] = raw_w[c] * scale

    return pd.Series(out_ret, index=idx)


def mutate_towards_goal(cfg: StrategyConfig, stats: dict[str, Any], step: int, rng: np.random.Generator) -> tuple[StrategyConfig, str]:
    """Diagnose the worst bottleneck year and propose a targeted mutation."""
    worst_yr = stats["worst_year"]
    d = asdict(cfg)
    reason = f"step={step} bottleneck={worst_yr} ({stats['min_ann_pct']}%)"

    if cfg.venue == "us_spot":
        # Spot mode: cannot short; push top-K concentration, MR dip-buying, and faster momentum
        choice = step % 4
        if choice == 0:
            d["top_k"] = int(rng.choice([2, 3, 4, 5]))
            d["mom_fast"] = int(rng.choice([5, 7, 10, 14]))
            d["mom_slow"] = int(rng.choice([21, 28, 35, 45]))
            reason += " -> tune top-K & dual momentum"
        elif choice == 1:
            d["mr_weight"] = float(rng.choice([0.15, 0.25, 0.35]))
            d["trend_weight"] = float(rng.choice([0.45, 0.55, 0.65]))
            d["xs_weight"] = round(1.0 - d["mr_weight"] - d["trend_weight"] * 0.5, 2)
            reason += " -> boost oversold MR dip-buying"
        elif choice == 2:
            d["tox_cut"] = float(rng.choice([0.45, 0.55, 0.65, 1.0]))
            d["news_boost"] = float(rng.choice([0.0, 0.15, 0.30]))
            reason += " -> tune micro_tox & news_sent_3d"
        else:
            d["dd_start"] = float(rng.choice([0.08, 0.12, 0.15]))
            d["dd_full"] = float(rng.choice([0.25, 0.35]))
            d["dd_floor"] = float(rng.choice([0.35, 0.50, 0.65]))
            d["carry_share"] = float(rng.choice([0.0, 0.20, 0.30, 0.40]))
            reason += " -> tune DD taper & carry_share"
    else:
        # Long/Short vol-targeted mode
        choice = step % 4
        if choice == 0:
            d["top_k"] = int(rng.choice([2, 3, 4]))
            d["mom_fast"] = int(rng.choice([5, 7, 10, 14]))
            d["mom_slow"] = int(rng.choice([21, 28, 35]))
            reason += " -> tune L/S top-K & momentum"
        elif choice == 1:
            d["short_scale"] = float(rng.choice([0.5, 0.75, 1.0, 1.25]))
            d["xs_weight"] = float(rng.choice([0.30, 0.45, 0.60]))
            d["trend_weight"] = float(rng.choice([0.30, 0.45, 0.60]))
            d["carry_share"] = float(rng.choice([0.0, 0.15, 0.25, 0.35]))
            reason += " -> tune short sleeve & carry_share"
        elif choice == 2:
            d["target_vol"] = float(rng.choice([0.28, 0.35, 0.45, 0.55]))
            d["max_lev"] = float(rng.choice([1.5, 2.0, 2.5, 3.0]))
            d["dd_start"] = float(rng.choice([0.10, 0.15]))
            d["dd_floor"] = float(rng.choice([0.40, 0.60]))
            reason += " -> tune causal vol-target & lev cap"
        else:
            d["tox_cut"] = float(rng.choice([0.45, 0.55, 0.65, 1.0]))
            d["news_boost"] = float(rng.choice([0.0, 0.2, 0.4]))
            d["mr_weight"] = float(rng.choice([0.0, 0.15, 0.25]))
            reason += " -> tune micro/news + MR"

    return StrategyConfig(**d), reason


def _robust_train_score(train_stats: dict[str, Any], cfg: StrategyConfig) -> float:
    """Sharpe-regularized score on TRAIN ONLY (2020-2023) to prevent leverage/year overfitting."""
    lev_pen = 0.15 * max(0.0, cfg.max_lev - 2.0)
    dd_pen = 0.02 * max(0.0, train_stats.get("max_dd_pct", 0.0) - 20.0)
    return float(train_stats.get("sharpe", 0.0)) + 0.01 * float(train_stats.get("min_ann_pct", -100.0)) - lev_pen - dd_pen


def run_optimization_loop(
    venue: str = "us_spot",
    goal_ann_pct: float = 50.0,
    max_iters: int = 40,
    seed: int = 42,
    out_path: str = DEFAULT_STATE_PATH,
    verify_cross_domain: bool = True,
) -> dict[str, Any]:
    """Optimize strictly on TRAIN_COINS (2020-2023), then evaluate untouched on:
      1) Locked-box Time OOS (2024-2026 on TRAIN_COINS)
      2) Untested Holdout Coins (2020-2026 on HOLDOUT_COINS)
      3) US Equities & ETFs (2020-2026 on EQUITY_TICKERS, if verify_cross_domain=True)
    """
    train_univ = load_universe(coins=TRAIN_COINS)
    pre_train = precompute_signals(train_univ)
    rng = np.random.default_rng(seed)
    cutoff_ts = pd.Timestamp(TRAIN_CUTOFF, tz="UTC")

    if venue == "us_spot":
        best_cfg = StrategyConfig(
            venue="us_spot", top_k=3, trend_weight=0.60, xs_weight=0.30,
            mr_weight=0.10, short_scale=0.0, target_vol=0.0, max_lev=1.0, carry_share=0.25,
        )
    else:
        best_cfg = StrategyConfig(
            venue="long_short_perp", top_k=3, trend_weight=0.45, xs_weight=0.45,
            mr_weight=0.10, short_scale=1.0, target_vol=0.40, max_lev=2.5, carry_share=0.20,
        )

    best_ret_full = simulate_config(pre_train, best_cfg)
    best_train_stats = evaluate_by_year(best_ret_full[best_ret_full.index < cutoff_ts])
    best_score = _robust_train_score(best_train_stats, best_cfg)
    history: list[dict[str, Any]] = [
        {"iter": 0, "reason": "initial_baseline", "train_score": round(best_score, 4), "train_min_ann_pct": best_train_stats["min_ann_pct"], "train_sharpe": best_train_stats["sharpe"]}
    ]

    for step in range(1, max_iters + 1):
        if best_train_stats["min_ann_pct"] >= goal_ann_pct:
            break
        cand_cfg, reason = mutate_towards_goal(best_cfg, best_train_stats, step, rng)
        cand_ret_full = simulate_config(pre_train, cand_cfg)
        cand_train_stats = evaluate_by_year(cand_ret_full[cand_ret_full.index < cutoff_ts])
        cand_score = _robust_train_score(cand_train_stats, cand_cfg)

        improved = cand_score > best_score + 1e-6
        if improved:
            best_cfg = cand_cfg
            best_train_stats = cand_train_stats
            best_ret_full = cand_ret_full
            best_score = cand_score

        history.append({
            "iter": step,
            "reason": reason,
            "improved": improved,
            "cand_train_score": round(cand_score, 4),
            "best_train_score": round(best_score, 4),
            "best_train_min_ann_pct": best_train_stats["min_ann_pct"],
            "best_train_sharpe": best_train_stats["sharpe"],
        })

    # 1. Locked-box Time OOS (2024-2026 on TRAIN_COINS)
    oos_time_stats = evaluate_by_year(best_ret_full[best_ret_full.index >= cutoff_ts])

    # 2. Untested Holdout Coins (2020-2026 on 9 coins never seen during optimization)
    holdout_univ = load_universe(coins=HOLDOUT_COINS)
    pre_holdout = precompute_signals(holdout_univ)
    holdout_ret = simulate_config(pre_holdout, best_cfg)
    holdout_stats = evaluate_by_year(holdout_ret)

    # 3. Cross-domain US Equities & ETFs (2020-2026, 252 trading days/yr, zero param tuning)
    equities_stats: dict[str, Any] | None = None
    if verify_cross_domain:
        eq_univ = load_equities_universe(tickers=EQUITY_TICKERS)
        pre_eq = precompute_signals(eq_univ)
        eq_ret = simulate_config(pre_eq, best_cfg)
        equities_stats = evaluate_by_year(eq_ret, min_days=80, annual_days=252)

    result = {
        "venue": venue,
        "goal_ann_pct": goal_ann_pct,
        "train_cutoff": TRAIN_CUTOFF,
        "train_coins": TRAIN_COINS,
        "holdout_coins": HOLDOUT_COINS,
        "iters_run": len(history) - 1,
        "best_config": asdict(best_cfg),
        "train_2020_2023": best_train_stats,
        "oos_2024_2026": oos_time_stats,
        "holdout_untested_coins_2020_2026": holdout_stats,
        "equities_etfs_2020_2026": equities_stats,
        "history": history,
    }
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(result, fh, indent=2)
    return result


def main() -> None:
    import argparse
    ap = argparse.ArgumentParser()
    ap.add_argument("--venue", choices=["us_spot", "long_short_perp"], default="long_short_perp")
    ap.add_argument("--goal", type=float, default=50.0)
    ap.add_argument("--iters", type=int, default=60)
    ap.add_argument("--no-equities", action="store_true")
    args = ap.parse_args()
    res = run_optimization_loop(
        venue=args.venue,
        goal_ann_pct=args.goal,
        max_iters=args.iters,
        verify_cross_domain=not args.no_equities,
    )
    print(json.dumps({k: v for k, v in res.items() if k != "history"}, indent=2))


if __name__ == "__main__":
    main()
