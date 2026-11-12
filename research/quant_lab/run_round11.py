"""Run PREREG11 once: BTC+ETH trend core (P1 fixed 2x, P2 vol-targeted) vs BTC-only 2x core. Writes
results/round11.json."""
from __future__ import annotations

import json
import os

import numpy as np

import lab
import strategies5 as S5
from run_round7 import PERIODS, cut, geo

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 711
CORE_PERIODS = ("D_2016_17", "C_2018_20", "B_2021_22", "discovery", "A_2025_26")


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    bh_m = S5.complete_months(cut(btc["c"].pct_change().fillna(0), "2016-01-01", None))
    reg = S5.regime_months(bh_m)
    res = {"n_trials": N_TRIALS, "eth_first_day": str(eth.index[0]), "variants": {}}
    for name in ("P1_btc_eth_2x", "P2_btc_eth_voltarget", "core_btc_2x"):
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
            if name == "core_btc_2x":
                r = lab.position_returns(btc, S5.m1_positions(btc, 50, 2.0), costs)
            elif name == "P1_btc_eth_2x":
                r = S5.two_sleeve(lab.position_returns(btc, S5.m1_positions(btc, 50, 2.0), costs),
                                  lab.position_returns(eth, S5.m1_positions(eth, 50, 2.0), costs))
            else:
                r = S5.two_sleeve(lab.position_returns(btc, S5.vt_positions(btc, 50, 1.4, 3.0), costs),
                                  lab.position_returns(eth, S5.vt_positions(eth, 50, 1.4, 3.0), costs))
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
    core = res["variants"]["core_btc_2x"]["us-futures"]["full_2016_26"]
    for name in ("P1_btc_eth_2x", "P2_btc_eth_voltarget"):
        v = res["variants"][name]
        f = v["us-futures"]
        checks = {"full_geo_ge_7": f["full_2016_26"]["geo_monthly_pct"] >= 7.0,
                  "dd_better_than_core": f["full_2016_26"]["max_dd_pct"] > core["max_dd_pct"],
                  "positive_in_4_of_5_periods": sum(f[p]["geo_monthly_pct"] > 0 for p in CORE_PERIODS) >= 4,
                  "stress_full_positive": v["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
        v["checks"], v["pass"] = checks, bool(all(checks.values()))
    for name, v in res["variants"].items():
        f = v["us-futures"]
        full = f["full_2016_26"]
        print(f"{name:21s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% losing {full['losing_months']:.0%} "
              f">=7% {full['months_ge_7pct']:.0%} worst {full['worst_month_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {f[p]['geo_monthly_pct']:+.2f}" for p in CORE_PERIODS)
              + f" | bear {f['by_regime_mean_monthly_pct']['bear']:+.1f} flat {f['by_regime_mean_monthly_pct']['flat']:+.1f} "
              f"bull {f['by_regime_mean_monthly_pct']['bull']:+.1f} | stress {v['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}"
              f" | {v.get('checks', 'comparator')} {'PASS' if v.get('pass') else ''}", flush=True)
    with open(os.path.join(HERE, "results", "round11.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
