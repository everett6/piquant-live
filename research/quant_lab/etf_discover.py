"""ETF alpha discovery: the auto_discover gates applied to a US ETF universe built around QQQ.

Same 5-gate pipeline as `auto_discover.py` (AST allowlist, future-shock causality, purged CV,
net-of-cost full backtest with deflated Sharpe, paper-only shadow queue), with:
  * an expanded ETF universe (broad, Nasdaq/tech, sectors, rates/credit, real assets),
  * causal cross-asset ETF features (`ETF_FEATURES`), written for one traded ETF (`tgt_*`, QQQ by default),
  * ETF costs (no commission, spread + slippage in bps), cash earning the 3-month T-bill rate,
  * a benchmark that moves with the target: a candidate must beat buy-and-hold of the same ETF
    on Sharpe and Calmar, not the crypto Q42 hurdles,
  * a sealed holdout: discovery reads only data before `ETF_EVAL_END`. Later data is kept for a
    pre-registered confirmation of any shadow candidate.

Every evaluated candidate is charged to the shared multiple-testing counter in the discovery state.
"""
from __future__ import annotations

import datetime as dt
import json
import os
from dataclasses import asdict
from typing import Any

import numpy as np
import pandas as pd

import auto_discover as AD
import feature_store as FS

ETF_GROUPS: dict[str, tuple[str, ...]] = {
    "broad": ("SPY", "QQQ", "IWM", "DIA", "RSP"),
    "nasdaq_tech": ("QQQ", "XLK", "SMH", "SOXX", "IGV", "XLC"),
    "sectors": ("XLF", "XLE", "XLV", "XLY", "XLP", "XLU", "XLI", "XLB", "XLRE"),
    "rates_credit": ("SHY", "IEF", "TLT", "HYG", "LQD"),
    "real_assets_fx": ("GLD", "SLV", "DBC", "UUP"),
}
ETF_UNIVERSE: tuple[str, ...] = tuple(dict.fromkeys(t for g in ETF_GROUPS.values() for t in g))
SECTOR_ETFS: tuple[str, ...] = ETF_GROUPS["sectors"] + ("XLK", "XLC")
DEFENSIVE = ("XLP", "XLU", "XLV")
CYCLICAL = ("XLY", "XLK", "XLI")

DEFAULT_TARGET = "QQQ"
DEFAULT_CACHE_DIR = "~/piquant-data/etf"
DTB3_PATH = "~/piquant-data/macro/DTB3.csv"
ETF_EVAL_START = "2008-01-01"
ETF_EVAL_END = "2025-01-01"  # exclusive; later data is the sealed holdout
ANNUAL_DAYS = 252

# Costs in bps per unit of turnover (no commission at US retail brokers)
BASE_COST_BPS = 2.0    # ~1 bp half-spread + 1 bp slippage on liquid ETFs
STRESS_COST_BPS = 10.0
MAX_DAILY_TURNOVER = 0.10
MIN_DSR = 0.80
FINANCING_SPREAD = 0.01  # annual spread over T-bills paid on exposure above 1.0

ETF_FEATURES: tuple[str, ...] = (
    "tgt_ret1",
    "tgt_ret5",
    "tgt_ret21",
    "tgt_ret63",
    "tgt_sma50_dist",
    "tgt_sma200_dist",
    "tgt_vol20_ann",
    "tgt_vol_ratio_5_20",
    "tgt_dd_252",
    "tgt_rsi14",
    "tgt_tlt_corr63",
    "spy_ret21",
    "spy_vol20_ann",
    "qqq_minus_spy_21d",
    "iwm_minus_spy_21d",
    "rsp_minus_spy_21d",
    "smh_minus_qqq_21d",
    "igv_minus_qqq_21d",
    "sector_breadth_50",
    "sector_dispersion_21",
    "defensive_minus_cyclical_21d",
    "tlt_ret21",
    "ief_minus_shy_21d",
    "hyg_minus_ief_21d",
    "gld_ret21",
    "dbc_ret21",
    "uup_ret21",
)
ETF_FEATURE_SET = frozenset(ETF_FEATURES)


def _col(closes: pd.DataFrame, sym: str) -> pd.Series:
    if sym in closes.columns:
        return closes[sym].astype(float)
    return pd.Series(np.nan, index=closes.index, dtype=float)


def build_etf_features(closes: pd.DataFrame, target: str = DEFAULT_TARGET) -> pd.DataFrame:
    """Causal daily features from adjusted closes (one column per ticker). Each value at t uses
    closes up to and including t only. Tickers absent from `closes` give NaN features."""
    if target not in closes.columns:
        raise ValueError(f"target {target} not in closes")
    ret = closes.pct_change(fill_method=None)
    r21 = closes.pct_change(21, fill_method=None)
    tgt = closes[target].astype(float)
    tr = ret[target]
    out = pd.DataFrame(index=closes.index)

    out["tgt_ret1"] = tr
    out["tgt_ret5"] = tgt.pct_change(5, fill_method=None)
    out["tgt_ret21"] = r21[target]
    out["tgt_ret63"] = tgt.pct_change(63, fill_method=None)
    out["tgt_sma50_dist"] = tgt / tgt.rolling(50).mean() - 1.0
    out["tgt_sma200_dist"] = tgt / tgt.rolling(200).mean() - 1.0
    vol5 = tr.rolling(5).std()
    vol20 = tr.rolling(20).std()
    out["tgt_vol20_ann"] = vol20 * np.sqrt(ANNUAL_DAYS)
    out["tgt_vol_ratio_5_20"] = vol5 / vol20.replace(0.0, np.nan)
    out["tgt_dd_252"] = tgt / tgt.rolling(252, min_periods=20).max() - 1.0
    gain = tr.clip(lower=0.0).ewm(alpha=1 / 14, adjust=False).mean()
    loss = (-tr.clip(upper=0.0)).ewm(alpha=1 / 14, adjust=False).mean()
    rsi = 100.0 - 100.0 / (1.0 + gain / loss.replace(0.0, np.nan))
    out["tgt_rsi14"] = (rsi - 50.0) / 50.0
    out["tgt_tlt_corr63"] = tr.rolling(63).corr(_col(ret, "TLT"))

    def rel(a: str, b: str) -> pd.Series:
        return _col(r21, a) - _col(r21, b)

    out["spy_ret21"] = _col(r21, "SPY")
    out["spy_vol20_ann"] = _col(ret, "SPY").rolling(20).std() * np.sqrt(ANNUAL_DAYS)
    out["qqq_minus_spy_21d"] = rel("QQQ", "SPY")
    out["iwm_minus_spy_21d"] = rel("IWM", "SPY")
    out["rsp_minus_spy_21d"] = rel("RSP", "SPY")
    out["smh_minus_qqq_21d"] = rel("SMH", "QQQ")
    out["igv_minus_qqq_21d"] = rel("IGV", "QQQ")

    sectors = [s for s in SECTOR_ETFS if s in closes.columns]
    if sectors:
        sc = closes[sectors]
        above = (sc > sc.rolling(50).mean()).astype(float).where(sc.rolling(50).mean().notna())
        out["sector_breadth_50"] = above.mean(axis=1, skipna=True)
        out["sector_dispersion_21"] = r21[sectors].std(axis=1, skipna=True)
    else:
        out["sector_breadth_50"] = np.nan
        out["sector_dispersion_21"] = np.nan
    dfn = [s for s in DEFENSIVE if s in closes.columns]
    cyc = [s for s in CYCLICAL if s in closes.columns]
    if dfn and cyc:
        out["defensive_minus_cyclical_21d"] = r21[dfn].mean(axis=1) - r21[cyc].mean(axis=1)
    else:
        out["defensive_minus_cyclical_21d"] = np.nan

    out["tlt_ret21"] = _col(r21, "TLT")
    out["ief_minus_shy_21d"] = rel("IEF", "SHY")
    out["hyg_minus_ief_21d"] = rel("HYG", "IEF")
    out["gld_ret21"] = _col(r21, "GLD")
    out["dbc_ret21"] = _col(r21, "DBC")
    out["uup_ret21"] = _col(r21, "UUP")
    return out[list(ETF_FEATURES)].replace([np.inf, -np.inf], np.nan)


def make_synthetic_etf_closes(n: int = 600, seed: int = 7, tickers: tuple[str, ...] = ETF_UNIVERSE) -> pd.DataFrame:
    """Correlated synthetic adjusted closes on a business-day calendar (one market factor)."""
    rng = np.random.default_rng(seed)
    idx = pd.bdate_range("2015-01-02", periods=n, tz="UTC")
    market = rng.normal(0.0004, 0.011, size=n)
    cols = {}
    for i, sym in enumerate(tickers):
        beta = -0.3 if sym in ETF_GROUPS["rates_credit"] + ("GLD", "UUP") else 0.6 + 0.08 * (i % 7)
        idio = rng.normal(0.0001, 0.006 + 0.001 * (i % 5), size=n)
        cols[sym] = 100.0 * np.exp(np.cumsum(beta * market + idio))
    return pd.DataFrame(cols, index=idx)


def check_etf_causality(
    expr: str,
    role: str = "overlay",
    target: str = DEFAULT_TARGET,
    shock_bar: int = 300,
) -> tuple[bool, str]:
    """Gate 2: shocking every ETF from `shock_bar` on must not change any signal before it."""
    c1 = make_synthetic_etf_closes(n=600, seed=11)
    c2 = c1.copy()
    rng = np.random.default_rng(3)
    c2.iloc[shock_bar:] = c2.iloc[shock_bar:] * rng.uniform(0.3, 3.0, size=c2.shape[1])
    s1 = AD.SafeAlphaEvaluator.evaluate(expr, build_etf_features(c1, target), role=role, allowed=ETF_FEATURES)
    s2 = AD.SafeAlphaEvaluator.evaluate(expr, build_etf_features(c2, target), role=role, allowed=ETF_FEATURES)
    if not np.all(np.isfinite(s1.to_numpy())):
        return False, "non-finite signal values on synthetic ETFs"
    if not np.allclose(s1.iloc[:shock_bar].to_numpy(), s2.iloc[:shock_bar].to_numpy(), atol=1e-12, rtol=1e-12):
        return False, "LOOKAHEAD DETECTED: future shock changed historical signal"
    if float(s1.iloc[220:].std()) < 1e-5:
        return False, "degenerate constant signal (std < 1e-5)"
    return True, "ok"


def simulate_etf(
    target_close: pd.Series,
    signal: pd.Series,
    role: str,
    cost_bps: float,
    rate_annual: pd.Series | None = None,
) -> tuple[pd.Series, pd.Series]:
    """Daily net returns and turnover. Overlay: exposure = signal x 100% of the target (0.40-1.25);
    standalone: exposure = signal (0-1). Exposure set at close t earns t+1. Cash earns the T-bill
    rate; exposure above 1.0 pays T-bill + FINANCING_SPREAD."""
    idx = target_close.index
    r = target_close.pct_change(fill_method=None).fillna(0.0).to_numpy(dtype=float)
    fill = 1.0 if role == "overlay" else 0.0
    w = signal.reindex(idx).fillna(fill).to_numpy(dtype=float)
    held = np.roll(w, 1)
    held[0] = 0.0
    prev = np.roll(held, 1)
    prev[0] = 0.0
    turn = np.abs(held - prev)
    rf = np.zeros(len(idx)) if rate_annual is None else (
        rate_annual.reindex(idx).ffill().fillna(0.0).to_numpy(dtype=float) / ANNUAL_DAYS
    )
    cash = np.where(held < 1.0, (1.0 - held) * rf, -(held - 1.0) * (rf + FINANCING_SPREAD / ANNUAL_DAYS))
    net = held * r + cash - turn * cost_bps * 1e-4
    return pd.Series(net, index=idx), pd.Series(turn, index=idx)


def etf_metrics(r: pd.Series, n_trials: int, recent_start: str | None = None) -> dict[str, Any]:
    import lab

    r = r.dropna()
    eq = (1.0 + r).cumprod()
    dd = eq / eq.cummax() - 1.0
    n_mo = max(1.0, len(r) / (ANNUAL_DAYS / 12.0))
    geo_mo = float((eq.iloc[-1] ** (1.0 / n_mo) - 1.0) * 100.0) if len(r) else 0.0
    std = float(r.std(ddof=1)) if len(r) > 1 else 0.0
    sharpe = float(r.mean() / std * np.sqrt(ANNUAL_DAYS)) if std > 1e-12 else 0.0
    max_dd = float(dd.min() * 100.0) if len(r) else 0.0
    ann = ((1.0 + geo_mo / 100.0) ** 12 - 1.0) * 100.0
    calmar = float(ann / abs(max_dd)) if abs(max_dd) > 1e-9 else 0.0
    out = {
        "geo_monthly_pct": round(geo_mo, 4),
        "annualized_pct": round(ann, 2),
        "sharpe": round(sharpe, 4),
        "calmar": round(calmar, 4),
        "max_dd_pct": round(max_dd, 4),
        "mean_dd_pct": round(float((-dd).mean() * 100.0), 4),
        "deflated_sharpe": round(float(lab.deflated_sharpe(r, n_trials)), 4),
    }
    if recent_start is not None:
        rec = r[r.index >= pd.Timestamp(recent_start, tz=r.index.tz)]
        if len(rec) > 20:
            n_rec = len(rec) / (ANNUAL_DAYS / 12.0)
            out["recent_geo_monthly_pct"] = round(float(((1.0 + rec).prod() ** (1.0 / n_rec) - 1.0) * 100.0), 4)
    return out


def etf_purged_cv(
    net: pd.Series,
    turn: pd.Series,
    n_splits: int = 5,
    purge_bars: int = 5,
    embargo_bars: int = 2,
) -> dict[str, Any]:
    """Gate 3: per-fold Sharpe on purged/embargoed test folds of the net-of-cost return series."""
    arr = net.to_numpy(dtype=float)
    sharpes: list[float] = []
    for _tr, te in FS.purged_embargoed_splits(len(arr), n_splits=n_splits, purge_bars=purge_bars, embargo_bars=embargo_bars):
        x = arr[te]
        sd = float(np.std(x, ddof=1)) if len(x) > 1 else 0.0
        sharpes.append(round(float(np.mean(x) / sd * np.sqrt(ANNUAL_DAYS)) if sd > 1e-12 else 0.0, 4))
    med = float(np.median(sharpes)) if sharpes else 0.0
    pos = int(sum(1 for s in sharpes if s > 0.0))
    return {
        "fold_sharpes": sharpes,
        "median_fold_sharpe": round(med, 4),
        "positive_folds": pos,
        "mean_daily_turnover": round(float(turn.mean()), 5),
        "passed": med >= 0.50 and pos >= 3,
    }


def check_etf_gates(
    base: dict[str, Any],
    stress: dict[str, Any],
    bench: dict[str, Any],
    cv: dict[str, Any],
) -> tuple[bool, str]:
    """Gate 4: beat buy-and-hold of the same ETF on Sharpe and Calmar, no deeper max drawdown,
    DSR >= MIN_DSR, positive under stress costs and in the recent window, low turnover."""
    reasons: list[str] = []
    if float(base["deflated_sharpe"]) < MIN_DSR:
        reasons.append(f"DSR {base['deflated_sharpe']} < {MIN_DSR}")
    if float(base["sharpe"]) <= float(bench["sharpe"]):
        reasons.append(f"Sharpe {base['sharpe']} <= buy-hold {bench['sharpe']}")
    if float(base["calmar"]) <= float(bench["calmar"]):
        reasons.append(f"Calmar {base['calmar']} <= buy-hold {bench['calmar']}")
    if float(base["max_dd_pct"]) < float(bench["max_dd_pct"]):
        reasons.append(f"MaxDD {base['max_dd_pct']}% deeper than buy-hold {bench['max_dd_pct']}%")
    if float(stress["geo_monthly_pct"]) <= 0.0:
        reasons.append(f"stress geo/mo {stress['geo_monthly_pct']} <= 0")
    rec = base.get("recent_geo_monthly_pct")
    if rec is None or float(rec) <= 0.0:
        reasons.append(f"recent geo/mo {rec} <= 0")
    if float(cv["mean_daily_turnover"]) > MAX_DAILY_TURNOVER:
        reasons.append(f"turnover {cv['mean_daily_turnover']} > {MAX_DAILY_TURNOVER}")
    return (False, "; ".join(reasons)) if reasons else (True, "PASS")


def build_etf_prompt(
    n_candidates: int,
    recent_feedback: list[str] | None = None,
    latency_profile: dict[str, Any] | None = None,
    target: str = DEFAULT_TARGET,
) -> str:
    del latency_profile  # daily ETF signals are not latency-bound
    fb = ""
    if recent_feedback:
        fb = "\nRecent rejected attempts (avoid these failure modes):\n- " + "\n- ".join(recent_feedback[-5:])
    return (
        f"Generate exactly {n_candidates} distinct daily risk-overlay expressions for the US ETF {target} "
        f"as a JSON array of strings.\n"
        f"Features prefixed tgt_ describe {target} itself. Allowed feature names ONLY: {', '.join(ETF_FEATURES)}.\n"
        f"Allowed functions ONLY: abs(x), tanh(x), sign(x), sigmoid(x), clip(x,lo,hi), "
        f"where(cond,a,b), min(a,b), max(a,b), ema(x,span), roll_mean(x,win).\n"
        f"Allowed operators: + - * / < <= > >= & | ~.\n"
        f"Example valid expressions:\n"
        f"- tanh(tgt_sma200_dist * 8.0) - 2.0 * max(tgt_vol20_ann - 0.30, 0.0)\n"
        f"- ema(hyg_minus_ief_21d, 10) * 20.0 + 2.0 * (sector_breadth_50 - 0.5)\n"
        f"Goal: hold {target} through calm uptrends and cut exposure before or during drawdowns, so that "
        f"Sharpe and Calmar beat buy-and-hold with turnover under {MAX_DAILY_TURNOVER:.0%} a day. Use "
        f"cross-asset context (credit, rates, breadth, semis leadership, dollar) not only {target}'s own trend. "
        f"Keep expressions under 160 chars.{fb}\n"
        f"Return ONLY a JSON array of {n_candidates} expression strings, nothing else."
    )


ETF_TEMPLATES: tuple[str, ...] = (
    "tanh(tgt_sma200_dist * 8.0) - 2.0 * max(tgt_vol20_ann - 0.30, 0.0)",
    "where(tgt_sma200_dist > 0.0, 1.5 - 4.0 * max(-tgt_dd_252 - 0.10, 0.0), -1.0)",
    "ema(hyg_minus_ief_21d, 10) * 20.0 + 2.0 * (sector_breadth_50 - 0.5)",
    "tanh(ema(tgt_ret63, 10) * 4.0) - 3.0 * max(tgt_vol_ratio_5_20 - 1.4, 0.0)",
    "2.0 * (sector_breadth_50 - 0.4) - 10.0 * max(defensive_minus_cyclical_21d, 0.0)",
    "tanh(ema(smh_minus_qqq_21d, 10) * 10.0) + tanh(tgt_sma50_dist * 10.0)",
    "where(spy_vol20_ann < 0.25, 1.0 + tanh(tgt_ret21 * 5.0), -0.5 - 5.0 * max(spy_vol20_ann - 0.25, 0.0))",
    "tanh(tgt_sma200_dist * 6.0) - 2.0 * max(tgt_tlt_corr63, 0.0) * max(-tlt_ret21, 0.0) * 10.0",
    "ema(iwm_minus_spy_21d + rsp_minus_spy_21d, 10) * 10.0 + tanh(tgt_sma200_dist * 5.0)",
    "1.0 - 8.0 * max(uup_ret21 - 0.02, 0.0) - 6.0 * max(-hyg_minus_ief_21d - 0.01, 0.0)",
    "roll_mean(sector_breadth_50, 10) * 3.0 - 1.5 - 4.0 * max(sector_dispersion_21 - 0.05, 0.0)",
    "where(tgt_rsi14 < -0.4, 1.5, tanh(tgt_sma200_dist * 8.0)) - max(tgt_vol20_ann - 0.35, 0.0) * 3.0",
)


def generate_etf_symbolic_candidates(n_candidates: int, seed: int = 42) -> list[str]:
    """Deterministic ETF expressions: curated templates, then random trend-minus-risk combinations."""
    rng = np.random.default_rng(seed)
    out: list[str] = []
    for i in rng.permutation(len(ETF_TEMPLATES)):
        out.append(ETF_TEMPLATES[int(i)])
        if len(out) >= n_candidates:
            return out
    trend = ("tgt_sma200_dist", "tgt_sma50_dist", "tgt_ret63", "sector_breadth_50", "hyg_minus_ief_21d")
    risk = ("tgt_vol20_ann", "spy_vol20_ann", "tgt_vol_ratio_5_20", "defensive_minus_cyclical_21d", "uup_ret21")
    seen = set(out)
    attempts = 0
    while len(out) < n_candidates and attempts < n_candidates * 10:
        attempts += 1
        tf, rf = str(rng.choice(trend)), str(rng.choice(risk))
        span = int(rng.choice([5, 10, 21]))
        w1 = round(float(rng.uniform(2.0, 10.0)), 2)
        w2 = round(float(rng.uniform(1.0, 5.0)), 2)
        thr = round(float(rng.uniform(0.0, 0.4)), 2)
        expr = f"tanh(ema({tf}, {span}) * {w1}) - {w2} * max({rf} - {thr}, 0.0)"
        if expr not in seen:
            seen.add(expr)
            out.append(expr)
    return out


def load_etf_closes(
    tickers: tuple[str, ...] = ETF_UNIVERSE,
    cache_dir: str = DEFAULT_CACHE_DIR,
    end: str = ETF_EVAL_END,
    refresh: bool = False,
) -> pd.DataFrame:
    """Dividend-adjusted daily closes from Yahoo's free chart API, cached as CSV per ticker.
    Returns only rows before `end`, so the holdout is never read into discovery."""
    import requests

    cdir = os.path.expanduser(cache_dir)
    os.makedirs(cdir, exist_ok=True)
    series: dict[str, pd.Series] = {}
    for sym in tickers:
        path = os.path.join(cdir, f"{sym}.csv")
        if refresh or not os.path.exists(path):
            # range=max silently returns monthly bars; an explicit period keeps interval=1d
            now = int(dt.datetime.now(dt.timezone.utc).timestamp())
            url = (f"https://query1.finance.yahoo.com/v8/finance/chart/{sym}"
                   f"?interval=1d&period1=0&period2={now}&events=div,split")
            resp = requests.get(url, headers={"User-Agent": "Mozilla/5.0"}, timeout=20)
            resp.raise_for_status()
            res = resp.json()["chart"]["result"][0]
            adj = (res["indicators"].get("adjclose") or [{}])[0].get("adjclose")
            close = adj if adj is not None else res["indicators"]["quote"][0]["close"]
            df = pd.DataFrame({
                "date": pd.to_datetime(res["timestamp"], unit="s", utc=True).normalize(),
                "adj_close": close,
            }).dropna().drop_duplicates(subset=["date"], keep="last")
            df.to_csv(path, index=False)
        df = pd.read_csv(path)
        s = pd.Series(df["adj_close"].to_numpy(dtype=float), index=pd.to_datetime(df["date"], utc=True))
        s = s.sort_index()
        _check_daily(s, sym)
        series[sym] = s
    closes = pd.DataFrame(series).sort_index()
    closes = closes[closes.index < pd.Timestamp(end, tz="UTC")]
    return closes.dropna(how="all")


def _check_daily(s: pd.Series, sym: str) -> None:
    """Refuse non-daily bars (e.g. Yahoo's monthly fallback): median gap must be <= 4 days."""
    if len(s) < 3:
        return
    gap = float(pd.Series(s.index).diff().dt.days.median())
    if gap > 4.0:
        raise ValueError(f"{sym}: median bar gap {gap:.0f} days, not daily data; re-download with refresh")


def load_tbill_rate(path: str = DTB3_PATH) -> pd.Series | None:
    p = os.path.expanduser(path)
    if not os.path.exists(p):
        return None
    raw = pd.read_csv(p, parse_dates=["date"])
    return pd.Series(raw["rate"].to_numpy(dtype=float) / 100.0, index=raw["date"].dt.tz_localize("UTC")).dropna()


def _load_etf_feedback(log_path: str, limit: int = 5) -> list[str]:
    if not os.path.exists(log_path):
        return []
    lines: list[str] = []
    with open(log_path, "r", encoding="utf-8") as fh:
        for line in fh:
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            if rec.get("asset_class") != "etf" or rec.get("accepted"):
                continue
            reason = str(rec.get("reject_reason") or "failed")
            if reason.startswith("AST_GATE"):
                continue
            lines.append(f'expr="{AD._sanitize_expr(str(rec.get("expr", "")))}" (reason: {reason[:157]})')
    return lines[-limit:]


def run_etf_discovery(
    n_candidates: int = 8,
    mode: str = "auto",
    target: str = DEFAULT_TARGET,
    role: str = "overlay",
    seed: int = 42,
    dry_run_synth: bool = False,
    closes: pd.DataFrame | None = None,
    cache_dir: str = DEFAULT_CACHE_DIR,
    eval_start: str = ETF_EVAL_START,
    eval_end: str = ETF_EVAL_END,
    state_path: str = AD.DEFAULT_STATE_PATH,
    shadow_path: str = AD.DEFAULT_SHADOW_PATH,
    log_path: str = AD.DEFAULT_LOG_PATH,
    **gen_kwargs: Any,
) -> dict[str, Any]:
    """One bounded ETF discovery round. `closes` overrides data loading (tests)."""
    if target not in ETF_UNIVERSE:
        raise ValueError(f"target {target} not in ETF universe")
    state = AD._load_state(state_path)
    feedback = _load_etf_feedback(log_path)

    expr_pairs, notes = AD.generate_candidates(
        n_candidates=n_candidates, mode=mode, dry_run_synth=dry_run_synth, seed=seed,
        recent_feedback=feedback,
        prompt_fn=lambda n, fb, lp: build_etf_prompt(n, fb, lp, target=target),
        symbolic_fn=generate_etf_symbolic_candidates,
        **gen_kwargs,
    )

    if closes is None:
        closes = make_synthetic_etf_closes(n=900, seed=seed + 5) if dry_run_synth else load_etf_closes(
            cache_dir=cache_dir, end=eval_end
        )
    closes = closes[closes.index < pd.Timestamp(eval_end, tz="UTC")]
    rate = None if dry_run_synth else load_tbill_rate()
    features = build_etf_features(closes, target=target)
    start_ts = pd.Timestamp(eval_start, tz="UTC")
    if dry_run_synth:
        start_ts = closes.index[min(220, len(closes) - 1)]
    win = closes.index[closes.index >= start_ts]
    if len(win) < 260:
        raise ValueError(f"evaluation window too short: {len(win)} days")
    tgt_close = closes[target].reindex(win)
    recent_start = str(win[-1] - pd.Timedelta(days=730))[:10]

    bench_r, _ = simulate_etf(tgt_close, pd.Series(1.0, index=win), "standalone", 0.0, rate)
    bench = etf_metrics(bench_r, state["n_trials"], recent_start=recent_start)

    shadow: list[dict[str, Any]] = []
    if os.path.exists(shadow_path):
        try:
            with open(shadow_path, "r", encoding="utf-8") as fh:
                loaded = json.load(fh)
            shadow = loaded if isinstance(loaded, list) else []
        except json.JSONDecodeError:
            shadow = []
    keys = {(str(x.get("expr")), str(x.get("venue")), str(x.get("role"))) for x in shadow if isinstance(x, dict)}
    venue = f"etf:{target}"
    now = dt.datetime.now(dt.timezone.utc).isoformat()
    results: list[dict[str, Any]] = []

    def record(res: AD.CandidateResult, extra: dict[str, Any] | None = None) -> dict[str, Any]:
        rec = asdict(res)
        rec["asset_class"] = "etf"
        rec["target"] = target
        rec["benchmark_metrics"] = bench
        if extra:
            rec.update(extra)
        results.append(rec)
        AD._append_jsonl(log_path, rec)
        return rec

    for raw, src in expr_pairs:
        expr = AD._sanitize_expr(raw) or raw

        def reject(reason: str, trials: int, **kw: Any) -> None:
            record(AD.CandidateResult(expr=expr, source=src, role=role, n_trials_at_eval=trials, accepted=False,
                                      reject_reason=reason, venue=venue, timestamp_utc=now, **kw))
            state["rejected"] += 1

        try:
            AD.SafeAlphaEvaluator.validate(expr, allowed=ETF_FEATURE_SET)
        except AD.UnsafeExpressionError as exc:
            reject(f"AST_GATE: {exc}", state["n_trials"])
            continue
        ok, msg = check_etf_causality(expr, role=role, target=target)
        if not ok:
            reject(f"CAUSALITY_GATE: {msg}", state["n_trials"])
            continue

        sig = AD.SafeAlphaEvaluator.evaluate(expr, features, role=role, allowed=ETF_FEATURES).reindex(win)
        state["n_trials"] += 1
        trials = state["n_trials"]
        net, turn = simulate_etf(tgt_close, sig, role, BASE_COST_BPS, rate)
        cv = etf_purged_cv(net, turn)
        if not cv["passed"]:
            reject(f"PURGED_CV_GATE: median_sharpe={cv['median_fold_sharpe']}, pos_folds={cv['positive_folds']}/5",
                   trials, cv_summary=cv)
            continue
        stress_net, _ = simulate_etf(tgt_close, sig, role, STRESS_COST_BPS, rate)
        base_m = etf_metrics(net, trials, recent_start=recent_start)
        stress_m = etf_metrics(stress_net, trials, recent_start=recent_start)
        ok_full, why = check_etf_gates(base_m, stress_m, bench, cv)
        rec = record(AD.CandidateResult(
            expr=expr, source=src, role=role, n_trials_at_eval=trials, accepted=ok_full,
            reject_reason="" if ok_full else f"FULL_GATE: {why}", venue=venue, cv_summary=cv,
            base_metrics=base_m, stress_metrics=stress_m, timestamp_utc=now,
        ))
        if ok_full:
            state["accepted"] += 1
            if (expr, venue, role) not in keys:
                keys.add((expr, venue, role))
                shadow.append({**rec, "status": "SHADOW_PAPER_ONLY", "holdout_from": eval_end})
        else:
            state["rejected"] += 1

    state["runs"] += 1
    state["last_run_utc"] = now
    AD._save_json(state_path, state)
    AD._save_json(shadow_path, shadow)
    return {
        "mode": mode,
        "venue": venue,
        "dry_run_synth": dry_run_synth,
        "window": [str(win[0])[:10], str(win[-1])[:10]],
        "benchmark": bench,
        "notes": notes,
        "evaluated": len(results),
        "accepted": sum(1 for r in results if r["accepted"]),
        "rejected": sum(1 for r in results if not r["accepted"]),
        "n_trials_total": state["n_trials"],
        "shadow_queue_len": len(shadow),
        "results": results,
    }
