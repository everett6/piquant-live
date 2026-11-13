"""Run PREREG24 once: M1 (always-on 10%/market) and M2 (idle-capital 40% x flat share) multi-market TSMOM overlays
on the BTC+ETH 2x core vs P1. Reads ~/piquant-data/macro (fetch_round24.py). Writes results/round24.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies24 as S24
from run_round7 import PERIODS, cut, geo

HERE = os.path.dirname(os.path.abspath(__file__))
MACRO = os.path.expanduser("~/piquant-data/macro")
N_TRIALS = 744
CORE_PERIODS = ("D_2016_17", "C_2018_20", "B_2021_22", "discovery", "A_2025_26")
OVERLAY_COST = {"us-futures": 2.0, "us-futures-stress": 5.0}


def load_macro():
    px = pd.concat({m: pd.read_csv(os.path.join(MACRO, f"{m}.csv"), parse_dates=["date"], index_col="date")["adj"]
                    for m in S24.MARKETS}, axis=1).sort_index()
    rf = pd.read_csv(os.path.join(MACRO, "DTB3.csv"), parse_dates=["date"], index_col="date")["rate"].sort_index()
    return px.dropna(), rf


def stats(r, reg):
    per = {}
    for p, (a, b) in PERIODS.items():
        rr = cut(r, a, b)
        m = S5.complete_months(rr)
        s = lab.summarize(rr)
        per[p] = {"geo_monthly_pct": geo(m), "months": int(len(m)),
                  "losing_months": float((m < 0).mean()) if len(m) else None,
                  "months_ge_7pct": float((m >= 0.07).mean()) if len(m) else None,
                  "worst_month_pct": float(m.min() * 100) if len(m) else None,
                  "max_dd_pct": s["max_dd_pct"], "sharpe": s["sharpe"]}
    m_all = S5.complete_months(cut(r, "2016-01-01", None)).reindex(reg.index)
    per["by_regime_mean_monthly_pct"] = {k: float(m_all[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}
    return per


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    px, rf = load_macro()
    bh_m = S5.complete_months(cut(btc["c"].pct_change().fillna(0), "2016-01-01", None))
    reg = S5.regime_months(bh_m)
    pos_b = pd.Series(S5.m1_positions(btc, 50, 2.0), index=btc.index)
    pos_e = pd.Series(S5.m1_positions(eth, 50, 2.0), index=eth.index)
    k = S24.idle_share(pos_b, pos_e, pd.DatetimeIndex(px.index).tz_localize("UTC"))
    k.index = px.index
    targets = {"M1_overlay_always_10pct": 0.10, "M2_overlay_idle_40pct": 0.40 * k}
    res = {"n_trials": N_TRIALS, "data": json.load(open(os.path.join(MACRO, "SOURCES.json"))),
           "etf_range": [str(px.index[0].date()), str(px.index[-1].date())], "variants": {}, "overlay_alone": {}}
    p1_m = None
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        p1 = S5.two_sleeve(lab.position_returns(btc, pos_b.to_numpy(), costs),
                           lab.position_returns(eth, pos_e.to_numpy(), costs))
        res["variants"].setdefault("P1_btc_eth_2x", {})[costs.name] = stats(p1, reg)
        if costs is lab.US_FUTURES:
            p1_m = S5.complete_months(cut(p1, "2016-01-01", None))
        for name, tgt in targets.items():
            ov, contrib = S24.overlay_returns(px, rf, tgt, OVERLAY_COST[costs.name])
            ovc = S24.to_crypto_days(ov, p1.index)
            res["variants"].setdefault(name, {})[costs.name] = stats(p1 + ovc, reg)
            if costs is lab.US_FUTURES:
                om = S5.complete_months(cut(ovc, "2016-01-01", None))
                om_r = om.reindex(reg.index)
                cm = {m: float(S5.complete_months(cut(S24.to_crypto_days(contrib[m], p1.index), "2016-01-01", None))
                               .mean() * 100) for m in contrib.columns}
                res["overlay_alone"][name] = {
                    "full_geo_monthly_pct": geo(om), "sharpe": lab.summarize(cut(ovc, "2016-01-01", None))["sharpe"],
                    "bear_mean_monthly_pct": float(om_r[reg == "bear"].mean() * 100),
                    "corr_with_P1_monthly": float(np.corrcoef(om.reindex(p1_m.index).fillna(0), p1_m)[0, 1]),
                    "mean_monthly_pct_by_market": cm}
    p1f = res["variants"]["P1_btc_eth_2x"]["us-futures"]
    for name in targets:
        v = res["variants"][name]
        f = v["us-futures"]
        checks = {
            "bear_months_ge_P1_plus_3": f["by_regime_mean_monthly_pct"]["bear"]
            >= p1f["by_regime_mean_monthly_pct"]["bear"] + 3.0,
            "full_geo_ge_7": f["full_2016_26"]["geo_monthly_pct"] >= 7.0,
            "max_dd_no_worse_than_P1": f["full_2016_26"]["max_dd_pct"] >= p1f["full_2016_26"]["max_dd_pct"],
            "A_positive": f["A_2025_26"]["geo_monthly_pct"] > 0,
            "stress_full_positive": v["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
        v["checks"], v["pass"] = checks, bool(all(checks.values()))
    passed = [n for n in targets if res["variants"][n]["pass"]]
    res["chosen"] = max(passed, key=lambda n: res["variants"][n]["us-futures"]["full_2016_26"]["geo_monthly_pct"]) \
        if passed else None
    print("chosen:", res["chosen"])
    for name, v in res["variants"].items():
        f = v["us-futures"]
        full = f["full_2016_26"]
        print(f"{name:24s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% "
              f"losing {full['losing_months']:.0%} worst {full['worst_month_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {f[p]['geo_monthly_pct']:+.2f}" for p in CORE_PERIODS)
              + f" | 2022 {f['bear_2022']['geo_monthly_pct']:+.2f} | bear {f['by_regime_mean_monthly_pct']['bear']:+.1f} "
              f"flat {f['by_regime_mean_monthly_pct']['flat']:+.1f} bull {f['by_regime_mean_monthly_pct']['bull']:+.1f}"
              f" | stress {v['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}"
              f" | {v.get('checks', 'comparator')} {'PASS' if v.get('pass') else ''}", flush=True)
    for name, o in res["overlay_alone"].items():
        print(name, "overlay alone:", json.dumps(o))
    with open(os.path.join(HERE, "results", "round24.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
