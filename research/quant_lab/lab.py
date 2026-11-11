"""Honest backtest harness for PiQuant research (see PREREG.md for the rules this implements).

- Data: Coinbase BTC-USD 1m candles from `piquant collect` (gzip JSONL per UTC day).
- Fills: signal on bar close t -> entry at open of bar t+1. Stops/targets on later bars' high/low; when both are
  touched in one bar the stop is assumed first. Costs per profile (fees, slippage, funding per hour held).
- Metrics: daily-P&L Sharpe, drawdown, win rate, profit factor, months >= 7%; Deflated Sharpe Ratio; PBO (CSCV).
"""
from __future__ import annotations

import glob
import gzip
import itertools
import json
import math
import os
from dataclasses import dataclass

import numpy as np
import pandas as pd

DATA_START = pd.Timestamp("2022-12-17", tz="UTC")  # PREREG data start (older backfilled days are ignored)
DISCOVERY_END = pd.Timestamp("2025-07-01", tz="UTC")  # holdout starts here (PREREG)


@dataclass(frozen=True)
class Costs:
    name: str
    taker_bps: float
    slip_bps: float
    funding_bps_h: float
    can_short: bool


US_FUTURES = Costs("us-futures", taker_bps=6.0, slip_bps=1.0, funding_bps_h=0.1, can_short=True)
US_FUTURES_STRESS = Costs("us-futures-stress", taker_bps=6.0, slip_bps=1.0, funding_bps_h=1.0, can_short=True)
US_SPOT = Costs("us-spot", taker_bps=2.0, slip_bps=1.0, funding_bps_h=0.0, can_short=False)


def load_candles(collect_dir: str = "~/piquant-collect", product: str = "BTC-USD", cache: bool = True,
                 since: pd.Timestamp | None = DATA_START) -> pd.DataFrame:
    """1-minute OHLCV indexed by UTC time. Cached to parquet; the cache is rebuilt when new day files appear."""
    collect_dir = os.path.expanduser(collect_dir)
    files = sorted(glob.glob(os.path.join(collect_dir, "candles", product, "*.jsonl.gz")))
    cache_path = os.path.join(collect_dir, "train", f"{product}_1m.parquet")
    if cache and os.path.exists(cache_path):
        df = pd.read_parquet(cache_path)
        if "n_files" in df and len(files) == int(df["n_files"].iloc[0]):
            df = df.drop(columns=["n_files"])
            return df if since is None else df[df.index >= since]
    rows = []
    for p in files:
        try:
            with gzip.open(p, "rt") as f:
                rows += [json.loads(line) for line in f if line.strip()]
        except (EOFError, OSError):
            pass
    df = pd.DataFrame(rows).drop_duplicates("t").sort_values("t")
    df.index = pd.to_datetime(df["t"], unit="s", utc=True)
    df = df[["o", "h", "l", "c", "v"]].astype("float64")
    df = df[(df["c"] > 0) & (df["h"] >= df["l"])]
    if cache:
        os.makedirs(os.path.dirname(cache_path), exist_ok=True)
        out = df.copy()
        out["n_files"] = len(files)
        out.to_parquet(cache_path)
    return df if since is None else df[df.index >= since]


def resample(df: pd.DataFrame, rule: str) -> pd.DataFrame:
    return df.resample(rule, label="left", closed="left").agg(
        {"o": "first", "h": "max", "l": "min", "c": "last", "v": "sum"}).dropna()


# ----------------------------------------------------------------------------------------------------------
# Trade simulator for bracket (TP/SL/time-stop) strategies
# ----------------------------------------------------------------------------------------------------------
def simulate_brackets(o, h, l, c, times_ns, signal, tp_bps, sl_bps, max_hold, loser_stop, costs: Costs,
                      target_px=None):
    """One position at a time. signal[t] in {-1,0,+1} decided at close of bar t -> fill at o[t+1].

    tp_bps / sl_bps: arrays (per signal bar) of target/stop distance in bps (vol scaling done by the caller).
    target_px: optional array of absolute target prices (overrides tp_bps when finite).
    max_hold: bars; loser_stop: bars after which a losing position is closed at the bar close (time stop).
    Returns a DataFrame of trades with net returns (fraction of notional) and hold hours.
    """
    n = len(o)
    if n >= 2 and (int(times_ns[1]) - int(times_ns[0])) < 10**9:
        raise ValueError("times_ns must be epoch nanoseconds (use index.as_unit('ns').asi8)")
    trades = []
    i = 0
    round_trip_cost = 2 * (costs.taker_bps + costs.slip_bps) * 1e-4
    while i < n - 2:
        s = signal[i]
        if s == 0 or (s < 0 and not costs.can_short) or not np.isfinite(tp_bps[i]) or not np.isfinite(sl_bps[i]):
            i += 1
            continue
        e = i + 1
        entry = o[e]
        if s > 0:
            stop = entry * (1 - sl_bps[i] * 1e-4)
            tgt = target_px[i] if target_px is not None and np.isfinite(target_px[i]) else entry * (1 + tp_bps[i] * 1e-4)
            if tgt <= entry:
                i += 1
                continue
        else:
            stop = entry * (1 + sl_bps[i] * 1e-4)
            tgt = target_px[i] if target_px is not None and np.isfinite(target_px[i]) else entry * (1 - tp_bps[i] * 1e-4)
            if tgt >= entry:
                i += 1
                continue
        exit_px, j, reason = None, e, ""
        last = min(n - 1, e + max_hold)
        while j <= last:
            hit_stop = l[j] <= stop if s > 0 else h[j] >= stop
            hit_tgt = h[j] >= tgt if s > 0 else l[j] <= tgt
            if hit_stop:  # pessimistic: stop first when both touched
                gap = o[j] < stop if s > 0 else o[j] > stop
                exit_px, reason = (o[j] if gap else stop), "stop"
                break
            if hit_tgt:
                gap = o[j] > tgt if s > 0 else o[j] < tgt
                exit_px, reason = (o[j] if gap else tgt), "target"
                break
            if j - e >= loser_stop and (c[j] - entry) * s < 0:
                exit_px, reason = c[j], "time"
                break
            j += 1
        if exit_px is None:
            j = last
            exit_px, reason = c[j], "max_hold"
        gross = (exit_px / entry - 1) * s
        hold_h = (times_ns[j] - times_ns[e]) / 3.6e12 + 1 / 60
        net = gross - round_trip_cost - costs.funding_bps_h * 1e-4 * hold_h
        trades.append((times_ns[e], times_ns[j], s, gross, net, hold_h, reason))
        i = j + 1  # flat after exit; next signal evaluated from the following bar's close
    return pd.DataFrame(trades, columns=["entry_ns", "exit_ns", "side", "gross", "net", "hold_h", "reason"])


def position_returns(df: pd.DataFrame, pos: np.ndarray, costs: Costs) -> pd.Series:
    """Bar returns of a target-position strategy: pos[t] decided at close t is held over bar t+1."""
    ret = df["c"].pct_change().fillna(0).to_numpy()
    p = np.asarray(pos, dtype=float)
    if not costs.can_short:
        p = np.clip(p, 0, None)
    held = np.concatenate([[0.0], p[:-1]])
    turnover = np.abs(np.diff(np.concatenate([[0.0], held])))
    bar_h = (df.index[1] - df.index[0]).total_seconds() / 3600 if len(df) > 1 else 1.0
    r = held * ret - turnover * (costs.taker_bps + costs.slip_bps) * 1e-4 - np.abs(held) * costs.funding_bps_h * 1e-4 * bar_h
    return pd.Series(r, index=df.index)


# ----------------------------------------------------------------------------------------------------------
# Metrics
# ----------------------------------------------------------------------------------------------------------
def daily_from_trades(tr: pd.DataFrame, start, end) -> pd.Series:
    idx = pd.date_range(pd.Timestamp(start).floor("D"), pd.Timestamp(end).floor("D"), freq="D", tz="UTC").as_unit("ns")
    if tr.empty:
        return pd.Series(0.0, index=idx)
    d = pd.DatetimeIndex(pd.to_datetime(tr["exit_ns"].to_numpy(), unit="ns", utc=True)).floor("D").as_unit("ns")
    g = pd.Series(np.log1p(tr["net"].to_numpy()), index=d).groupby(level=0).sum()
    return np.expm1(g.reindex(idx, fill_value=0.0))


def daily_from_bars(r: pd.Series) -> pd.Series:
    return np.expm1(np.log1p(r).resample("D").sum())


def summarize(daily: pd.Series, trades: pd.DataFrame | None = None) -> dict:
    daily = daily.fillna(0)
    eq = (1 + daily).cumprod()
    years = max(len(daily) / 365.0, 1e-9)
    sd = daily.std()
    out = {
        "total_return_pct": float((eq.iloc[-1] - 1) * 100) if len(eq) else 0.0,
        "cagr_pct": float((eq.iloc[-1] ** (1 / years) - 1) * 100) if len(eq) and eq.iloc[-1] > 0 else -100.0,
        "sharpe": float(daily.mean() / sd * math.sqrt(365)) if sd > 0 else 0.0,
        "max_dd_pct": float(((eq / eq.cummax()) - 1).min() * 100) if len(eq) else 0.0,
        "months_ge_7pct": float((np.expm1(np.log1p(daily).resample("ME").sum()) >= 0.07).mean()) if len(daily) > 31 else 0.0,
        "days": int(len(daily)),
    }
    if trades is not None:
        n = len(trades)
        wins = trades["net"] > 0
        out.update({
            "trades": int(n),
            "win_rate_pct": float(wins.mean() * 100) if n else 0.0,
            "avg_win_bps": float(trades.loc[wins, "net"].mean() * 1e4) if wins.any() else 0.0,
            "avg_loss_bps": float(trades.loc[~wins, "net"].mean() * 1e4) if (~wins).any() else 0.0,
            "profit_factor": float(trades.loc[wins, "net"].sum() / -trades.loc[~wins, "net"].sum())
            if (~wins).any() and trades.loc[~wins, "net"].sum() < 0 else float("inf"),
            "avg_gross_bps": float(trades["gross"].mean() * 1e4) if n else 0.0,
        })
    return out


def _norm_cdf(x):
    return 0.5 * (1 + math.erf(x / math.sqrt(2)))


def _norm_ppf(p):
    # Acklam's rational approximation (good to ~1e-9), avoids a scipy dependency.
    a = [-3.969683028665376e01, 2.209460984245205e02, -2.759285104469687e02, 1.383577518672690e02,
         -3.066479806614716e01, 2.506628277459239e00]
    b = [-5.447609879822406e01, 1.615858368580409e02, -1.556989798598866e02, 6.680131188771972e01, -1.328068155288572e01]
    cc = [-7.784894002430293e-03, -3.223964580411365e-01, -2.400758277161838e00, -2.549732539343734e00,
          4.374664141464968e00, 2.938163982698783e00]
    d = [7.784695709041462e-03, 3.224671290700398e-01, 2.445134137142996e00, 3.754408661907416e00]
    pl = 0.02425
    if p < pl:
        q = math.sqrt(-2 * math.log(p))
        return (((((cc[0] * q + cc[1]) * q + cc[2]) * q + cc[3]) * q + cc[4]) * q + cc[5]) / ((((d[0] * q + d[1]) * q + d[2]) * q + d[3]) * q + 1)
    if p > 1 - pl:
        return -_norm_ppf(1 - p)
    q = p - 0.5
    r = q * q
    return (((((a[0] * r + a[1]) * r + a[2]) * r + a[3]) * r + a[4]) * r + a[5]) * q / (((((b[0] * r + b[1]) * r + b[2]) * r + b[3]) * r + b[4]) * r + 1)


def deflated_sharpe(daily: pd.Series, n_trials: int, trial_sharpes_daily_var: float | None = None) -> float:
    """Probability that the true (daily) Sharpe > 0 after selecting the best of n_trials (Bailey & Lopez de Prado).
    trial_sharpes_daily_var: variance of the *daily* Sharpe ratios across trials (defaults to 1/T)."""
    r = daily.dropna().to_numpy()
    T = len(r)
    if T < 30 or r.std() == 0:
        return 0.0
    sr = r.mean() / r.std()
    skew = float(((r - r.mean()) ** 3).mean() / r.std() ** 3)
    kurt = float(((r - r.mean()) ** 4).mean() / r.std() ** 4)
    var_sr = trial_sharpes_daily_var if trial_sharpes_daily_var else 1.0 / T
    emc = 0.5772156649
    n = max(int(n_trials), 2)
    sr0 = math.sqrt(var_sr) * ((1 - emc) * _norm_ppf(1 - 1 / n) + emc * _norm_ppf(1 - 1 / (n * math.e)))
    denom = math.sqrt(max(1e-12, 1 - skew * sr + (kurt - 1) / 4 * sr * sr))
    return _norm_cdf((sr - sr0) * math.sqrt(T - 1) / denom)


def pbo_cscv(perf: pd.DataFrame, n_blocks: int = 10) -> float:
    """Probability of Backtest Overfitting (CSCV). perf: rows = time (daily returns), cols = configurations."""
    m = perf.fillna(0).to_numpy()
    T, N = m.shape
    if N < 2 or T < n_blocks * 5:
        return float("nan")
    blocks = np.array_split(np.arange(T), n_blocks)
    logits = []
    for combo in itertools.combinations(range(n_blocks), n_blocks // 2):
        is_idx = np.concatenate([blocks[k] for k in combo])
        oos_idx = np.concatenate([blocks[k] for k in range(n_blocks) if k not in combo])

        def sharpe(x):
            sd = x.std(axis=0)
            return np.where(sd > 0, x.mean(axis=0) / np.where(sd > 0, sd, 1), -np.inf)
        best = int(np.argmax(sharpe(m[is_idx])))
        oos = sharpe(m[oos_idx])
        rank = (oos < oos[best]).sum() + 0.5 * ((oos == oos[best]).sum() - 1)
        w = (rank + 1) / (N + 1)
        logits.append(math.log(w / (1 - w)))
    return float(np.mean(np.array(logits) <= 0))
