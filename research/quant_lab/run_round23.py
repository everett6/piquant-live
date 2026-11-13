"""Run PREREG23 once: volatility-expansion exits V1 (total) and V2 (downside) on the BTC+ETH 2x core vs P1.
Writes results/round23.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies5 as S5
import strategies23 as S23
from run_round7 import PERIODS, cut, geo

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 742
CORE_PERIODS = ("D_2016_17", "C_2018_20", "B_2021_22", "discovery", "A_2025_26")


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    bh_m = S5.complete_months(cut(btc["c"].pct_change().fillna(0), "2016-01-01", None))
    reg = S5.regime_months(bh_m)
    res = {"n_trials": N_TRIALS, "variants": {}}

    def pair(fn, c):
        return S5.two_sleeve(lab.position_returns(btc, fn(btc), c), lab.position_returns(eth, fn(eth), c))

    builders = {
        "V1_vol_exit_2x": lambda c: pair(lambda d: S23.vol_exit_positions(d, 2.0, False), c),
        "V2_downside_vol_exit_2x": lambda c: pair(lambda d: S23.vol_exit_positions(d, 2.0, True), c),
        "P1_btc_eth_2x": lambda c: pair(lambda d: S5.m1_positions(d, 50, 2.0), c),
    }
    for name, build in builders.items():
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
            r = build(costs)
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
            out[costs.name] = per
        res["variants"][name] = out
    p1 = res["variants"]["P1_btc_eth_2x"]["us-futures"]
    for name, down in (("V1_vol_exit_2x", False), ("V2_downside_vol_exit_2x", True)):
        v = res["variants"][name]
        f = v["us-futures"]
        checks = {
            "bear_months_ge_P1_plus_3": f["by_regime_mean_monthly_pct"]["bear"]
            >= p1["by_regime_mean_monthly_pct"]["bear"] + 3.0,
            "full_geo_ge_7": f["full_2016_26"]["geo_monthly_pct"] >= 7.0,
            "max_dd_no_worse_than_P1": f["full_2016_26"]["max_dd_pct"] >= p1["full_2016_26"]["max_dd_pct"],
            "A_positive": f["A_2025_26"]["geo_monthly_pct"] > 0,
            "stress_full_positive": v["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
        v["checks"], v["pass"] = checks, bool(all(checks.values()))
        v["gate_flat_days"] = {"BTC": S23.gate_days(btc, down), "ETH": S23.gate_days(eth, down)}
    passed = [n for n, v in res["variants"].items() if v.get("pass")]
    res["chosen"] = max(passed, key=lambda n: res["variants"][n]["us-futures"]["full_2016_26"]["geo_monthly_pct"]) if passed else None
    print("chosen:", res["chosen"])
    for name, v in res["variants"].items():
        f = v["us-futures"]
        full = f["full_2016_26"]
        print(f"{name:20s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% losing {full['losing_months']:.0%} "
              f">=7% {full['months_ge_7pct']:.0%} worst {full['worst_month_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {f[p]['geo_monthly_pct']:+.2f}" for p in CORE_PERIODS)
              + f" | bear {f['by_regime_mean_monthly_pct']['bear']:+.1f} flat {f['by_regime_mean_monthly_pct']['flat']:+.1f} "
              f"bull {f['by_regime_mean_monthly_pct']['bull']:+.1f} | stress {v['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}"
              f" | {v.get('checks', 'comparator')} {'PASS' if v.get('pass') else ''}", flush=True)
    with open(os.path.join(HERE, "results", "round23.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
