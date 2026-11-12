"""Run PREREG7 once: long/short SMA50 trend (L1: 2x/1x, L2: 2x/2x) vs the long-or-flat core. Writes
results/round7.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 689
VARIANTS = {"L1_long2_short1": (2.0, 1.0), "L2_long2_short2": (2.0, 2.0), "core_long2_flat": (2.0, 0.0)}
PERIODS = {"D_2016_17": ("2016-01-01", "2018-01-01"), "C_2018_20": ("2018-01-01", "2021-01-01"),
           "B_2021_22": ("2021-01-01", "2022-12-17"), "discovery": ("2022-12-17", "2025-07-01"),
           "A_2025_26": ("2025-07-01", None), "bear_2022": ("2022-01-01", "2023-01-01"),
           "full_2016_26": ("2016-01-01", None)}


def cut(x, a, b):
    m = x.index >= pd.Timestamp(a, tz="UTC")
    if b is not None:
        m &= x.index < pd.Timestamp(b, tz="UTC")
    return x[m]


def geo(m):
    return float((np.prod(1 + m) ** (1 / len(m)) - 1) * 100) if len(m) else float("nan")


def main():
    daily = lab.resample(lab.load_candles(since=None), "1D")
    bh = daily["c"].pct_change().fillna(0)
    bh_m = S5.complete_months(cut(bh, "2016-01-01", None))
    reg = S5.regime_months(bh_m)
    res = {"n_trials": N_TRIALS, "regime_counts": reg.value_counts().to_dict(), "variants": {}}
    for name, (ll, ls) in VARIANTS.items():
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
            r = lab.position_returns(daily, S5.ls_positions(daily, 50, ll, ls), costs)
            per = {}
            for p, (a, b) in PERIODS.items():
                rr = cut(r, a, b)
                m = S5.complete_months(rr)
                s = lab.summarize(rr)
                per[p] = {"geo_monthly_pct": geo(m), "mean_monthly_pct": float(m.mean() * 100) if len(m) else float("nan"),
                          "months": int(len(m)), "months_ge_7pct": float((m >= 0.07).mean()) if len(m) else float("nan"),
                          "losing_months": float((m < 0).mean()) if len(m) else float("nan"),
                          "sharpe": s["sharpe"], "max_dd_pct": s["max_dd_pct"], "total_return_pct": s["total_return_pct"]}
            m_all = S5.complete_months(cut(r, "2016-01-01", None)).reindex(reg.index)
            per["by_regime_mean_monthly_pct"] = {k: float(m_all[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}
            out[costs.name] = per
        f = out["us-futures"]
        out["checks"] = {"bear_months_positive": f["by_regime_mean_monthly_pct"]["bear"] > 0,
                         "bear_holdouts_positive": f["A_2025_26"]["mean_monthly_pct"] > 0 and f["bear_2022"]["mean_monthly_pct"] > 0,
                         "max_dd_ok": f["full_2016_26"]["max_dd_pct"] > -90}
        res["variants"][name] = out
    core_geo = res["variants"]["core_long2_flat"]["us-futures"]["full_2016_26"]["geo_monthly_pct"]
    for name in ("L1_long2_short1", "L2_long2_short2"):
        c = res["variants"][name]["checks"]
        c["beats_core_full_history"] = res["variants"][name]["us-futures"]["full_2016_26"]["geo_monthly_pct"] >= core_geo
        c["pass"] = bool(all(c.values()))
    for name, v in res["variants"].items():
        f = v["us-futures"]
        print(f"{name:17s} full {f['full_2016_26']['geo_monthly_pct']:+.2f}%/mo (>=7%: {f['full_2016_26']['months_ge_7pct']:.0%}, "
              f"losing {f['full_2016_26']['losing_months']:.0%}, maxDD {f['full_2016_26']['max_dd_pct']:.0f}%) | "
              f"bear months {f['by_regime_mean_monthly_pct']['bear']:+.2f}% flat {f['by_regime_mean_monthly_pct']['flat']:+.2f}% "
              f"bull {f['by_regime_mean_monthly_pct']['bull']:+.2f}% | 2022 {f['bear_2022']['mean_monthly_pct']:+.2f}%/mo "
              f"A {f['A_2025_26']['mean_monthly_pct']:+.2f}%/mo | stress full {v['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}%/mo "
              f"| {v.get('checks')}", flush=True)
    print("regime months:", res["regime_counts"])
    with open(os.path.join(HERE, "results", "round7.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
