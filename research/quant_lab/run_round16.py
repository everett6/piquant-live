"""Run PREREG16 once: daily dip-buying (D1 uptrend dip, D2 uptrend pullback, D3 any dip). Writes
results/round16.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies16 as S
from run_round7 import PERIODS, cut, geo

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 726
CORE_PERIODS = ("D_2016_17", "C_2018_20", "B_2021_22", "discovery", "A_2025_26")


def tstat(x):
    return float(x.mean() / (x.std(ddof=1) / np.sqrt(len(x)))) if len(x) > 1 and x.std(ddof=1) > 0 else 0.0


def main():
    d = {p: lab.resample(lab.load_candles(product=p, since=None), "1D") for p in ("BTC-USD", "ETH-USD")}
    start = pd.Timestamp("2016-01-01", tz="UTC")
    p1 = {}
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        p1[costs.name] = S5.two_sleeve(*[lab.position_returns(d[p], S5.m1_positions(d[p], 50, 2.0), costs)
                                         for p in ("BTC-USD", "ETH-USD")])
    res = {"n_trials": N_TRIALS, "variants": {}}
    for v in ("D1", "D2", "D3"):
        trades = {p: S.dip_trades(d[p], v) for p in d}
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
            tr = pd.concat([S.trade_returns(d[p], trades[p], costs) for p in d]).sort_index()
            tr = tr[tr.index >= start]
            per = {"all": {"n": int(len(tr)), "mean_net_bps": float(tr["net"].mean() * 1e4) if len(tr) else None,
                           "mean_gross_bps": float(tr["gross"].mean() * 1e4) if len(tr) else None,
                           "t": tstat(tr["net"]), "win_rate": float((tr["net"] > 0).mean()) if len(tr) else None,
                           "worst_trade_pct": float(tr["net"].min() * 100) if len(tr) else None}}
            for pname in CORE_PERIODS:
                a, b = PERIODS[pname]
                x = cut(tr["net"], a, b)
                per[pname] = {"n": int(len(x)), "mean_net_bps": float(x.mean() * 1e4) if len(x) else None}
            # Overlay on P1: dip sleeve only while that coin's P1 sleeve is flat, 1x on the same half.
            sleeves = []
            for p in ("BTC-USD", "ETH-USD"):
                p1pos = S5.m1_positions(d[p], 50, 2.0)
                dpos = S.positions(len(d[p]), trades[p])
                sleeves.append(lab.position_returns(d[p], np.where(p1pos > 0, p1pos, dpos), costs))
            comb = cut(S5.two_sleeve(*sleeves), "2016-01-01", None)
            base = cut(p1[costs.name], "2016-01-01", None)
            per["overlay_vs_p1"] = {"overlay_geo_monthly_pct": geo(S5.complete_months(comb)),
                                    "overlay_max_dd_pct": lab.summarize(comb)["max_dd_pct"],
                                    "p1_geo_monthly_pct": geo(S5.complete_months(base)),
                                    "p1_max_dd_pct": lab.summarize(base)["max_dd_pct"]}
            out[costs.name] = per
        f, st = out["us-futures"], out["us-futures-stress"]
        checks = {"n_ge_60": f["all"]["n"] >= 60,
                  "mean_pos_t_ge_2": (f["all"]["mean_net_bps"] or -1) > 0 and f["all"]["t"] >= 2.0,
                  "pos_in_4_of_5_periods": sum((f[p]["mean_net_bps"] or -1) > 0 for p in CORE_PERIODS) >= 4,
                  "stress_mean_pos": (st["all"]["mean_net_bps"] or -1) > 0}
        out["checks"], out["pass"] = checks, bool(all(checks.values()))
        res["variants"][v] = out
        a, o = f["all"], f["overlay_vs_p1"]
        print(f"{v}: n {a['n']} net {a['mean_net_bps']:+.0f} bps (gross {a['mean_gross_bps']:+.0f}) t {a['t']:.2f} "
              f"win {a['win_rate']:.0%} worst {a['worst_trade_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {f[p]['n']}:{(f[p]['mean_net_bps'] or 0):+.0f}" for p in CORE_PERIODS)
              + f" | stress {st['all']['mean_net_bps']:+.0f} | overlay {o['overlay_geo_monthly_pct']:+.2f}%/mo "
              f"DD {o['overlay_max_dd_pct']:.0f}% vs P1 {o['p1_geo_monthly_pct']:+.2f}% DD {o['p1_max_dd_pct']:.0f}% | "
              + ("PASS" if out["pass"] else "FAIL " + str([k for k, ok in checks.items() if not ok])), flush=True)
    passers = [v for v in res["variants"] if res["variants"][v]["pass"]]
    res["chosen"] = max(passers, key=lambda v: res["variants"][v]["us-futures"]["all"]["t"]) if passers else None
    print("chosen:", res["chosen"])
    with open(os.path.join(HERE, "results", "round16.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
