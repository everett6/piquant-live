"""Run PREREG27 once: an always-on ladder of 30-day put spreads (q = 0.5) beside the BTC+ETH 2x core, against P1.
Implied volatility: Deribit DVOL from 2021-03-24 (~/piquant-data/dvol), the PREREG26 stand-in before.
Writes results/round27.json. DO NOT RUN before PREREG27.md is approved, committed and pushed."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies27 as S27
from run_round7 import cut, geo
from run_round24 import CORE_PERIODS, stats
from run_round26 import load_iv

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 749
W0 = "2021-04-01"  # months priced from real implied volatility
NAME = "S1_put_spread_ladder"


def regime_means(r: pd.Series, reg: pd.Series, start: str) -> dict:
    m = S5.complete_months(cut(r, start, None)).reindex(reg.index)
    keep = reg.index >= pd.Timestamp(start, tz="UTC")
    return {k: float(m[(reg == k) & keep].mean() * 100) for k in ("bear", "flat", "bull")}


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    coins = {"BTC": btc, "ETH": eth}
    iv = {c: S27.splice_iv(d["c"], load_iv(c)) for c, d in coins.items()}
    reg = S5.regime_months(S5.complete_months(cut(btc["c"].pct_change().fillna(0), "2016-01-01", None)))
    res = {"n_trials": N_TRIALS, "candles_end": str(btc.index[-1].date()), "q": S27.Q, "variants": {}, "sleeve": {}}
    keep = {}
    for fc, oc in ((lab.US_FUTURES, S27.BASE), (lab.US_FUTURES_STRESS, S27.STRESS)):
        p1 = S5.two_sleeve(*(lab.position_returns(d, S5.m1_positions(d, 50, 2.0), fc) for d in coins.values()))
        lad = {c: S27.put_spread_ladder(d["c"], iv[c], S27.Q, oc) for c, d in coins.items()}
        ov = S5.two_sleeve(lad["BTC"]["ret"], lad["ETH"]["ret"])
        tot = p1 + ov
        for name, r in (("P1_btc_eth_2x", p1), (NAME, tot)):
            st = stats(r, reg)
            st["by_regime_from_2021_04"] = regime_means(r, reg, W0)
            res["variants"].setdefault(name, {})[fc.name] = st
        if fc is lab.US_FUTURES:
            keep = {"p1": p1, "ov": ov, "tot": tot, "lad": lad}
    p1, ov, tot, lad = keep["p1"], keep["ov"], keep["tot"], keep["lad"]
    full_ov = cut(ov, "2016-01-01", None)
    om, pm = S5.complete_months(full_ov), S5.complete_months(cut(p1, "2016-01-01", None))
    s_ov = lab.summarize(full_ov)
    res["sleeve"] = {
        "full": stats(ov, reg), "by_regime_from_2021_04": regime_means(ov, reg, W0),
        "geo_monthly_pct": geo(om), "sharpe": s_ov["sharpe"], "max_dd_pct": s_ov["max_dd_pct"],
        "worst_day_pct": float(full_ov.min() * 100), "worst_day": str(full_ov.idxmin().date()),
        "worst_month_pct": float(om.min() * 100),
        "corr_with_P1_monthly": float(np.corrcoef(om.reindex(pm.index).fillna(0), pm)[0, 1]),
        "per_coin": {c: {"premium_pct_equity": float(x["premium"].sum() * 100),
                         "paid_back_pct_equity": float(x["payoff"].sum() * 100),
                         "paid_back_over_premium": float(x["payoff"].sum() / x["premium"].sum()),
                         "from_2021_04_paid_back_over_premium":
                             float(cut(x["payoff"], W0, None).sum() / cut(x["premium"], W0, None).sum())}
                     for c, x in lad.items()},
        "deflated_sharpe_sleeve": lab.deflated_sharpe(lab.daily_from_bars(full_ov), N_TRIALS),
        "deflated_sharpe_portfolio": lab.deflated_sharpe(lab.daily_from_bars(cut(tot, "2016-01-01", None)), N_TRIALS)}

    v = res["variants"]
    f, p = v[NAME]["us-futures"], v["P1_btc_eth_2x"]["us-futures"]
    checks = {
        "bear_ge_P1_plus_3_full": f["by_regime_mean_monthly_pct"]["bear"] >= p["by_regime_mean_monthly_pct"]["bear"] + 3.0,
        "bear_ge_P1_plus_3_from_2021_04": f["by_regime_from_2021_04"]["bear"] >= p["by_regime_from_2021_04"]["bear"] + 3.0,
        "full_geo_ge_7": f["full_2016_26"]["geo_monthly_pct"] >= 7.0,
        "max_dd_no_worse_than_P1": f["full_2016_26"]["max_dd_pct"] >= p["full_2016_26"]["max_dd_pct"],
        "A_positive": f["A_2025_26"]["geo_monthly_pct"] > 0,
        "stress_full_positive": v[NAME]["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
    v[NAME]["checks"], v[NAME]["pass"] = checks, bool(all(checks.values()))
    for name in ("P1_btc_eth_2x", NAME):
        b = v[name]["us-futures"]
        full = b["full_2016_26"]
        print(f"{name:22s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% sharpe {full['sharpe']:.2f} | "
              + " ".join(f"{q.split('_')[0]} {b[q]['geo_monthly_pct']:+.2f}" for q in CORE_PERIODS)
              + f" | 2022 {b['bear_2022']['geo_monthly_pct']:+.2f} | full " + " ".join(
                  f"{k} {x:+.1f}" for k, x in b["by_regime_mean_monthly_pct"].items())
              + " | 2021-04 on " + " ".join(f"{k} {x:+.1f}" for k, x in b["by_regime_from_2021_04"].items())
              + f" | stress {v[name]['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}", flush=True)
    print("checks:", checks, "PASS" if v[NAME]["pass"] else "FAIL")
    sl = res["sleeve"]
    print("sleeve alone: full regimes", sl["full"]["by_regime_mean_monthly_pct"], "2021-04 on", sl["by_regime_from_2021_04"])
    print(json.dumps({k: sl[k] for k in sl if k not in ("full", "by_regime_from_2021_04")}, indent=1))
    with open(os.path.join(HERE, "results", "round27.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
