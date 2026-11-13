"""Run PREREG29 once: cross-sectional momentum book (16 coins, weekly, top/bottom third by 21-day return) as an
overlay on the BTC+ETH 2x core vs P1. Reads ~/piquant-data/alts and ~/piquant-data/macro/DTB3.csv.
Writes results/round29.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies29 as S29
from run_round7 import cut, geo
from run_round24 import CORE_PERIODS, stats
from run_round25 import load_alts

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 751
NAME = "X1_xs_momentum"


def reg_means(r, reg):
    m = S5.complete_months(cut(r, "2016-01-01", None)).reindex(reg.index)
    return {k: float(m[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    closes = load_alts(btc.index)
    closes.insert(0, "ETH", eth["c"].reindex(btc.index))
    closes.insert(0, "BTC", btc["c"])
    closes = closes[S29.COINS]
    reg = S5.regime_months(S5.complete_months(cut(btc["c"].pct_change().fillna(0), "2016-01-01", None)))
    pos = {c: S5.m1_positions(d, 50, 2.0) for c, d in (("BTC", btc), ("ETH", eth))}
    w = S29.target_weights(closes)
    per_leg = (w > 0).sum(axis=1)
    res = {"n_trials": N_TRIALS, "candles_end": str(btc.index[-1].date()),
           "first_active_day": str(w.index[(w != 0).any(axis=1)][0].date()),
           "coins_per_leg_by_year": {str(y): float(v) for y, v in per_leg.groupby(per_leg.index.year).mean().items()},
           "variants": {}, "book": {}}
    keep = {}
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        p1 = S5.two_sleeve(lab.position_returns(btc, pos["BTC"], costs), lab.position_returns(eth, pos["ETH"], costs))
        bk = S29.book_returns(closes, w, costs.taker_bps, costs.slip_bps, costs.funding_bps_h)
        res["variants"].setdefault("P1_btc_eth_2x", {})[costs.name] = stats(p1, reg)
        res["variants"].setdefault(NAME, {})[costs.name] = stats(p1 + bk["total"], reg)
        res["book"][costs.name] = {"stats": stats(bk["total"], reg), "by_regime": reg_means(bk["total"], reg)}
        if costs is lab.US_FUTURES:
            keep = {"p1": p1, "bk": bk}
    p1, bk = keep["p1"], keep["bk"]
    b16 = cut(bk["total"], "2016-01-01", None)
    bm, pm = S5.complete_months(b16), S5.complete_months(cut(p1, "2016-01-01", None))
    s = lab.summarize(b16)
    yearly = np.expm1(np.log1p(b16).groupby(b16.index.year).sum()) * 100
    res["book"]["alone"] = {
        "geo_monthly_pct": geo(bm), "sharpe": s["sharpe"], "max_dd_pct": s["max_dd_pct"],
        "worst_day_pct": float(b16.min() * 100), "worst_day": str(b16.idxmin().date()),
        "worst_month_pct": float(bm.min() * 100), "corr_with_P1_monthly": float(np.corrcoef(bm, pm.reindex(bm.index))[0, 1]),
        "long_leg_by_regime": reg_means(bk["long"], reg), "short_leg_by_regime": reg_means(bk["short"], reg),
        "mean_daily_cost_bps": float(cut(bk["cost"], res["first_active_day"], None).mean() * 1e4),
        "by_year_pct": {str(y): float(v) for y, v in yearly.items()},
        "deflated_sharpe": lab.deflated_sharpe(lab.daily_from_bars(cut(bk["total"], res["first_active_day"], None)), N_TRIALS)}
    # Bookkeeping only (not a strategy, not a trial): Treasury-bill interest on the half of equity each flat sleeve frees.
    rf = pd.read_csv(os.path.expanduser("~/piquant-data/macro/DTB3.csv"), parse_dates=["date"], index_col="date")["rate"]
    rf.index = rf.index.tz_localize("UTC")
    rf_d = rf.reindex(btc.index).ffill().shift(1).fillna(0) / 100 / 365
    idle = 0.5 * (pd.Series(pos["BTC"], index=btc.index).shift(1).fillna(0) == 0) + \
        0.5 * (pd.Series(pos["ETH"], index=eth.index).reindex(btc.index).shift(1).fillna(0) == 0)
    res["cash_interest_on_idle_sleeves"] = {"by_regime_mean_monthly_pct": reg_means(idle * rf_d, reg),
                                            "mean_idle_share": float(cut(idle, "2016-01-01", None).mean())}

    v = res["variants"]
    f, p = v[NAME]["us-futures"], v["P1_btc_eth_2x"]["us-futures"]
    checks = {
        "bear_months_ge_P1_plus_3": f["by_regime_mean_monthly_pct"]["bear"] >= p["by_regime_mean_monthly_pct"]["bear"] + 3.0,
        "full_geo_ge_7": f["full_2016_26"]["geo_monthly_pct"] >= 7.0,
        "max_dd_no_worse_than_P1": f["full_2016_26"]["max_dd_pct"] >= p["full_2016_26"]["max_dd_pct"],
        "A_positive": f["A_2025_26"]["geo_monthly_pct"] > 0,
        "stress_full_positive": v[NAME]["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
    v[NAME]["checks"], v[NAME]["pass"] = checks, bool(all(checks.values()))
    for name in ("P1_btc_eth_2x", NAME):
        b = v[name]["us-futures"]
        full = b["full_2016_26"]
        print(f"{name:16s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% sharpe {full['sharpe']:.2f} | "
              + " ".join(f"{q.split('_')[0]} {b[q]['geo_monthly_pct']:+.2f}" for q in CORE_PERIODS)
              + f" | 2022 {b['bear_2022']['geo_monthly_pct']:+.2f} | "
              + " ".join(f"{k} {x:+.1f}" for k, x in b["by_regime_mean_monthly_pct"].items())
              + f" | stress {v[name]['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}", flush=True)
    print("checks:", checks, "PASS" if v[NAME]["pass"] else "FAIL")
    print("book alone by regime:", res["book"]["us-futures"]["by_regime"], "stress:", res["book"]["us-futures-stress"]["by_regime"])
    print(json.dumps(res["book"]["alone"], indent=1))
    print("first active", res["first_active_day"], "coins per leg", res["coins_per_leg_by_year"])
    print("cash interest:", res["cash_interest_on_idle_sleeves"])
    with open(os.path.join(HERE, "results", "round29.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
