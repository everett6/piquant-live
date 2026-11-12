"""Run PREREG14 once: drawdown-first trend core (S1 stop, S2 + Grossman-Zhou, S3 + ensemble) vs P1.
Writes results/round14.json."""
from __future__ import annotations

import json
import os

import numpy as np

import lab
import strategies14 as S
from run_round7 import PERIODS, cut, geo
import strategies5 as S5

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 720
CORE_PERIODS = ("D_2016_17", "C_2018_20", "B_2021_22", "discovery", "A_2025_26")
VARIANTS = {"P1_comparator": (S.p1_signal, False, False), "S1_stop": (S.p1_signal, True, False),
            "S2_stop_gz": (S.p1_signal, True, True), "S3_stop_gz_ensemble": (S.ensemble_signal, True, True)}


def main():
    m1 = {p: lab.load_candles(product=p, since=None) for p in ("BTC-USD", "ETH-USD")}
    d1 = {p: lab.resample(df, "1D") for p, df in m1.items()}
    idx = d1["BTC-USD"].index
    res = {"n_trials": N_TRIALS, "variants": {}}
    for name, (sig_fn, use_stop, gz) in VARIANTS.items():
        sleeves = []
        for p in ("BTC-USD", "ETH-USD"):
            sl = S.build_sleeve(d1[p], m1[p], idx, sig_fn)
            if not use_stop:
                sl["stopfill"] = np.full(len(idx), np.nan)
            sleeves.append(sl)
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
            sim = S.simulate(sleeves, costs, gz)
            r = sim["r"]
            per = {}
            for pname, (a, b) in PERIODS.items():
                rr = cut(r, a, b)
                m = S5.complete_months(rr)
                s = lab.summarize(rr)
                per[pname] = {"geo_monthly_pct": geo(m), "months": int(len(m)),
                              "losing_months": float((m < 0).mean()) if len(m) else None,
                              "months_ge_7pct": float((m >= 0.07).mean()) if len(m) else None,
                              "worst_month_pct": float(m.min() * 100) if len(m) else None,
                              "max_dd_pct": s["max_dd_pct"], "sharpe": s["sharpe"]}
            full = cut(sim, "2016-01-01", None)
            eq = (1 + full["r"]).cumprod()
            dd = eq / eq.cummax() - 1
            per["extra"] = {"worst_day_pct": float(full["r"].min() * 100),
                            "stops": int(full["stop0"].sum() + full["stop1"].sum()),
                            "share_days_held": float(((full["lev0"] > 0) | (full["lev1"] > 0)).mean()),
                            "days_within_5pts_of_floor": int((dd <= -(1 - S.ALPHA) + 0.05).sum())}
            out[costs.name] = per
        res["variants"][name] = out
    for name, v in res["variants"].items():
        if name == "P1_comparator":
            continue
        f, st = v["us-futures"], v["us-futures-stress"]
        checks = {"dd_35_base": f["full_2016_26"]["max_dd_pct"] >= -35.0,
                  "dd_35_stress": st["full_2016_26"]["max_dd_pct"] >= -35.0,
                  "full_geo_ge_2p5": f["full_2016_26"]["geo_monthly_pct"] >= 2.5,
                  "positive_in_4_of_5_periods": sum(f[p]["geo_monthly_pct"] > 0 for p in CORE_PERIODS) >= 4,
                  "stress_full_positive": st["full_2016_26"]["geo_monthly_pct"] > 0}
        v["checks"], v["pass"] = checks, bool(all(checks.values()))
    passers = [n for n, v in res["variants"].items() if v.get("pass")]
    res["chosen"] = max(passers, key=lambda n: res["variants"][n]["us-futures"]["full_2016_26"]["geo_monthly_pct"]) \
        if passers else None
    for name, v in res["variants"].items():
        f = v["us-futures"]
        full, ex = f["full_2016_26"], f["extra"]
        print(f"{name:20s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% "
              f"(stress {v['us-futures-stress']['full_2016_26']['max_dd_pct']:.0f}%, "
              f"{v['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}%/mo) worst day {ex['worst_day_pct']:.1f}% "
              f"worst month {full['worst_month_pct']:.0f}% stops {ex['stops']} held {ex['share_days_held']:.0%} | "
              + " ".join(f"{p.split('_')[0]} {f[p]['geo_monthly_pct']:+.2f}" for p in CORE_PERIODS)
              + f" | {'PASS' if v.get('pass') else ('' if 'checks' not in v else 'FAIL ' + str([k for k, ok in v['checks'].items() if not ok]))}",
              flush=True)
    print("chosen:", res["chosen"])
    with open(os.path.join(HERE, "results", "round14.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
