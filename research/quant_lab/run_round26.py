"""Run PREREG26 once: K1 (q=1) and K2 (q=2) convex leverage vs R1 (1x), R2 (1.5x) and P1 (2x) on 2021-04-01 onward.
Reads ~/piquant-data/dvol (fetch_round26.py). Writes results/round26.json."""
from __future__ import annotations

import json
import math
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies26 as S26
from run_round7 import cut, geo

HERE = os.path.dirname(os.path.abspath(__file__))
DVOL = os.path.expanduser("~/piquant-data/dvol")
N_TRIALS = 748
W0 = "2021-04-01"
PERIODS = {"window": (W0, None), "B_2021_22": (W0, "2022-12-17"), "discovery": ("2022-12-17", "2025-07-01"),
           "A_2025_26": ("2025-07-01", None), "bear_2022": ("2022-01-01", "2023-01-01")}
PRE = ("2016-01-01", "2021-03-24")


def load_iv(cur: str) -> pd.Series:
    d = pd.read_csv(os.path.join(DVOL, f"{cur}.csv"), parse_dates=["date"], index_col="date")["close"] / 100.0
    d.index = d.index.tz_localize("UTC")
    return d.sort_index()


def stats(r: pd.Series, reg: pd.Series, periods=PERIODS, reg_from=W0) -> dict:
    out = {}
    for p, (a, b) in periods.items():
        rr = cut(r, a, b)
        m = S5.complete_months(rr)
        s = lab.summarize(rr)
        out[p] = {"geo_monthly_pct": geo(m), "months": int(len(m)), "max_dd_pct": s["max_dd_pct"],
                  "sharpe": s["sharpe"], "worst_month_pct": float(m.min() * 100) if len(m) else None,
                  "worst_day_pct": float(rr.min() * 100), "worst_day": str(rr.idxmin().date()),
                  "losing_months": float((m < 0).mean()) if len(m) else None}
    if reg_from:
        m_all = S5.complete_months(cut(r, reg_from, None)).reindex(reg.index)
        out["by_regime_mean_monthly_pct"] = {k: float(m_all[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}
        out["regime_months"] = {k: int((reg == k).sum()) for k in ("bear", "flat", "bull")}
    return out


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    coins = {"BTC": (btc, load_iv("BTC")), "ETH": (eth, load_iv("ETH"))}
    reg = S5.regime_months(S5.complete_months(cut(btc["c"].pct_change().fillna(0), W0, None)))
    res = {"n_trials": N_TRIALS, "window_start": W0, "candles_end": str(btc.index[-1].date()),
           "data": json.load(open(os.path.join(DVOL, "SOURCES.json"))), "variants": {}, "overlay": {}, "pre_dvol": {}}

    def linear(lev, costs, periods=PERIODS, reg_from=W0):
        return stats(S5.two_sleeve(*(lab.position_returns(d, S5.m1_positions(d, 50, lev), costs)
                                     for d, _ in coins.values())), reg, periods, reg_from)

    for fc, oc in ((lab.US_FUTURES, S26.BASE), (lab.US_FUTURES_STRESS, S26.STRESS)):
        for name, lev in (("R1_1x", 1.0), ("R2_1p5x", 1.5), ("P1_btc_eth_2x", 2.0)):
            res["variants"].setdefault(name, {})[fc.name] = linear(lev, fc)
        for name, q in S26.Q.items():
            sl, ovs = zip(*(S26.convex_sleeve(d, iv, q, fc, oc) for d, iv in coins.values()))
            res["variants"].setdefault(name, {})[fc.name] = stats(S5.two_sleeve(*sl), reg)
            if fc is lab.US_FUTURES:
                o = {}
                for cur, ov in zip(coins, ovs):
                    w = cut(ov, W0, None)
                    o[cur] = {"premium_paid_pct_equity": float(w["premium"].sum() * 100),
                              "proceeds_pct_equity": float(w["payoff"].sum() * 100),
                              "proceeds_over_premium": float(w["payoff"].sum() / w["premium"].sum()),
                              "puts_bought": int((w["premium"] > 0).sum()), "days_held": int(w["held"].sum()),
                              "overlay_mean_monthly_pct": float(S5.complete_months(w["ret"]).mean() * 100)}
                res["overlay"][name] = o
    for cur, (d, iv) in coins.items():
        rv_next = d["c"].pct_change().rolling(30).std().shift(-30) * math.sqrt(365)
        j = pd.concat([iv, rv_next], axis=1, keys=["iv", "rv"]).dropna()
        on = pd.Series(S5.m1_positions(d, 50, 2.0) > 0, index=d.index).reindex(j.index)
        res["overlay"].setdefault("implied_vs_realized", {})[cur] = {
            "mean_dvol": float(j["iv"].mean()), "mean_next30d_realized": float(j["rv"].mean()),
            "mean_dvol_trend_on": float(j["iv"][on].mean()), "mean_next30d_realized_trend_on": float(j["rv"][on].mean())}
    # Outside the verdict: 2016-2021 with premiums from trailing realized volatility x 1.2 (a model, no market price).
    pre_p = {"pre_dvol_2016_21": PRE}
    for name, lev in (("R1_1x", 1.0), ("R2_1p5x", 1.5), ("P1_btc_eth_2x", 2.0)):
        res["pre_dvol"][name] = linear(lev, lab.US_FUTURES, pre_p, None)
    for name, q in S26.Q.items():
        sl = [S26.convex_sleeve(d, S26.realized_vol_iv(d["c"]), q, lab.US_FUTURES, S26.BASE)[0] for d, _ in coins.values()]
        res["pre_dvol"][name] = stats(S5.two_sleeve(*sl), reg, pre_p, None)

    v = res["variants"]
    base = {n: v[n]["us-futures"] for n in v}
    for name in S26.Q:
        f = base[name]
        checks = {
            "bear_months_ge_P1_plus_3": f["by_regime_mean_monthly_pct"]["bear"]
            >= base["P1_btc_eth_2x"]["by_regime_mean_monthly_pct"]["bear"] + 3.0,
            "return_ge_1p5x": f["window"]["geo_monthly_pct"] >= base["R2_1p5x"]["window"]["geo_monthly_pct"],
            "max_dd_no_worse_than_1x": f["window"]["max_dd_pct"] >= base["R1_1x"]["window"]["max_dd_pct"],
            "A_positive": f["A_2025_26"]["geo_monthly_pct"] > 0,
            "stress_positive": v[name]["us-futures-stress"]["window"]["geo_monthly_pct"] > 0}
        v[name]["checks"], v[name]["pass"] = checks, bool(all(checks.values()))
    res["passed"] = [n for n in S26.Q if v[n]["pass"]]
    for name, f in base.items():
        w = f["window"]
        print(f"{name:20s} {w['geo_monthly_pct']:+.2f}%/mo DD {w['max_dd_pct']:.0f}% sharpe {w['sharpe']:.2f} "
              f"worst day {w['worst_day_pct']:.1f}% ({w['worst_day']}) worst mo {w['worst_month_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {f[p]['geo_monthly_pct']:+.2f}/{f[p]['max_dd_pct']:.0f}"
                         for p in ("B_2021_22", "discovery", "A_2025_26"))
              + f" | 2022 {f['bear_2022']['geo_monthly_pct']:+.2f} | " + " ".join(
                  f"{k} {x:+.1f}" for k, x in f["by_regime_mean_monthly_pct"].items())
              + f" | stress {v[name]['us-futures-stress']['window']['geo_monthly_pct']:+.2f}"
              + f" | {v[name].get('checks', 'comparator')} {'PASS' if v[name].get('pass') else ''}", flush=True)
    print("regime months", base["P1_btc_eth_2x"]["regime_months"], "passed:", res["passed"])
    print(json.dumps(res["overlay"], indent=1))
    for name, f in res["pre_dvol"].items():
        p = f["pre_dvol_2016_21"]
        print(f"pre-DVOL {name:18s} {p['geo_monthly_pct']:+.2f}%/mo DD {p['max_dd_pct']:.0f}% worst day {p['worst_day_pct']:.1f}%")
    with open(os.path.join(HERE, "results", "round26.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
