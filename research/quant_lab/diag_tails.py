"""Descriptive diagnostics (no strategy is tested, no trial is charged): tail risk, volatility clustering, jumps,
Hurst exponents, gap risk for a 25% drawdown floor, and the Sharpe ratio a 7%/month + 25% max-drawdown goal needs.
Writes results/diag_tails.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies14 as S14

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
START = pd.Timestamp("2016-01-01", tz="UTC")
RNG = np.random.default_rng(19)


def load_csv(name: str) -> pd.DataFrame:
    df = pd.read_csv(os.path.join(ROOT, "data", "historical", name))
    df.index = pd.to_datetime(df["timestamp"], unit="ms", utc=True)
    return df[df.index >= START][["open", "high", "low", "close"]].rename(
        columns={"open": "o", "high": "h", "low": "l", "close": "c"})


def tails(r: np.ndarray) -> dict:
    r = r[np.isfinite(r)]
    out = {"n": int(len(r)), "std": float(r.std()), "worst": float(r.min()), "best": float(r.max()),
           "kurtosis": float(((r - r.mean()) ** 4).mean() / r.var() ** 2)}
    for name, q in (("99", 0.01), ("99.9", 0.001)):
        v = np.quantile(r, q)
        out[f"var_{name}"] = float(v)
        out[f"cvar_{name}"] = float(r[r <= v].mean())
    return out


def acf(x: np.ndarray, lag: int) -> float:
    x = x - x.mean()
    return float((x[lag:] * x[:-lag]).mean() / x.var())


def clustering(r: np.ndarray) -> dict:
    r = r[np.isfinite(r)]
    a = np.abs(r)
    return {"acf_ret_1": acf(r, 1), "acf_abs_1": acf(a, 1), "acf_abs_5": acf(a, 5), "acf_abs_20": acf(a, 20),
            "acf_abs_100": acf(a, 100)}


def jumps(r: pd.Series, window: int, per_year: float) -> dict:
    """Moves beyond 4 trailing standard deviations (sigma from the previous `window` returns)."""
    sig = r.rolling(window, min_periods=window).std().shift(1)
    z = (r / sig).dropna()
    years = len(z) / per_year
    return {"down_4sigma_per_year": float((z < -4).sum() / years), "up_4sigma_per_year": float((z > 4).sum() / years),
            "gaussian_expected_per_year_each_side": float(3.167e-5 * per_year)}


def hurst(logp: np.ndarray) -> float:
    """Aggregated-variance estimate: slope of log std(k-lag differences) on log k, k = 1..16."""
    ks = np.array([1, 2, 4, 8, 16])
    sd = np.array([np.std(logp[k:] - logp[:-k]) for k in ks])
    if (sd <= 0).any():
        return np.nan
    return float(np.polyfit(np.log(ks), np.log(sd), 1)[0])


def rolling_hurst(c: pd.Series, window: int, step: int) -> dict:
    lp = np.log(c.to_numpy())
    hs = np.array([hurst(lp[i - window:i]) for i in range(window, len(lp), step)])
    hs = hs[np.isfinite(hs)]
    return {"windows": int(len(hs)), "p10": float(np.quantile(hs, 0.1)), "median": float(np.median(hs)),
            "p90": float(np.quantile(hs, 0.9)), "share_above_0.56": float((hs > 0.56).mean()),
            "share_below_0.46": float((hs < 0.46).mean())}


def gap_risk(m1: pd.DataFrame, daily: pd.DataFrame) -> dict:
    """How far price can fall before a de-risking order could act, at several reaction horizons."""
    out = {}
    c = m1["c"]
    for name, n in (("1m", 1), ("5m", 5), ("1h", 60), ("6h", 360)):
        r = (c / c.shift(n) - 1).dropna().to_numpy()
        out[name] = {"worst": float(r.min()), "q99.9": float(np.quantile(r, 0.001)),
                     "q99.99": float(np.quantile(r, 0.0001))}
    intraday = (daily["l"] / daily["c"].shift(1) - 1).dropna().to_numpy()  # previous close to the day's low
    out["prev_close_to_low"] = {"worst": float(intraday.min()), "q99": float(np.quantile(intraday, 0.01)),
                                "q99.9": float(np.quantile(intraday, 0.001))}
    return out


def needed_sharpe(target_monthly=0.07, mdd_cap=0.25, years=5, paths=4000) -> dict:
    """For Gaussian daily returns with annual Sharpe S, leverage is set so the median path compounds at the target.
    Reports the median and 99th-percentile max drawdown for each S (best case: no fat tails, no costs)."""
    g = np.log(1 + target_monthly) * 12  # needed annual log growth
    n = 365 * years
    out = {}
    for s in (1.0, 1.5, 2.0, 3.0, 4.0, 5.0, 6.0):
        # log growth = mu - sigma^2/2 with mu = s * sigma  ->  sigma = s - sqrt(s^2 - 2g) (smaller root)
        disc = s * s - 2 * g
        if disc < 0:
            out[str(s)] = {"reachable": False}
            continue
        sigma = s - np.sqrt(disc)
        mu = s * sigma
        x = RNG.normal((mu - sigma ** 2 / 2) / 365, sigma / np.sqrt(365), size=(paths, n)).cumsum(axis=1)
        dd = 1 - np.exp(x - np.maximum.accumulate(x, axis=1))
        m = dd.max(axis=1)
        out[str(s)] = {"reachable": True, "annual_vol": float(sigma), "median_mdd": float(np.median(m)),
                       "p99_mdd": float(np.quantile(m, 0.99)), "share_paths_within_cap": float((m <= mdd_cap).mean())}
    return out


def sharpe(r: pd.Series, per_year: float) -> float:
    return float(r.mean() / r.std() * np.sqrt(per_year))


def main():
    res = {"note": "descriptive only; window 2016-01-01 onward; no strategy tested"}
    # PREREG15 data rule: drop flat 1m bars below half the prior daily close (one BTC bar at $0.06, 2017-04-15)
    m1 = {p: S14.drop_bad_prints(lab.load_candles(product=p, since=None)) for p in ("BTC-USD", "ETH-USD")}
    for coin, prod, f4, fd in (("BTC", "BTC-USD", "btc_4h_binance.csv", "btc_daily_full.csv"),
                               ("ETH", "ETH-USD", "eth_4h_binance.csv", "eth_daily_full.csv")):
        one = m1[prod][m1[prod].index >= START]
        h4, d = load_csv(f4), load_csv(fd)
        cb_daily = lab.resample(one, "1D")
        r1 = np.log(one["c"]).diff()
        r4 = np.log(h4["c"]).diff()
        rd = np.log(d["c"]).diff()
        res[coin] = {
            "tails": {"1m": tails(r1.to_numpy()), "4h": tails(r4.to_numpy()), "1d": tails(rd.to_numpy())},
            "clustering": {"1m": clustering(r1.dropna().to_numpy()), "4h": clustering(r4.dropna().to_numpy()),
                           "1d": clustering(rd.dropna().to_numpy())},
            "jumps": {"1m": jumps(r1, 1440, 525600), "4h": jumps(r4, 180, 2190), "1d": jumps(rd, 30, 365)},
            "hurst": {"4h_window_256": rolling_hurst(h4["c"], 256, 24), "1d_window_256": rolling_hurst(d["c"], 256, 7)},
            "gap_risk": gap_risk(one, cb_daily),
            "buy_hold_sharpe_daily": sharpe(d["c"].pct_change().dropna(), 365),
            "csv_vs_coinbase_daily_close_median_abs_diff": float(
                (d["c"].reindex(cb_daily.index) / cb_daily["c"] - 1).abs().median()),
        }
    res["needed_sharpe_for_7pct_month"] = needed_sharpe()
    with open(os.path.join(HERE, "results", "diag_tails.json"), "w") as f:
        json.dump(res, f, indent=2)
    print(json.dumps(res, indent=1))


if __name__ == "__main__":
    main()
