"""Run PREREG10 once: T1 session x regime drift (cells selected on discovery), T2 US opening-range breakout.
Writes results/round10.json."""
from __future__ import annotations

import json
import math
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies8 as S8
import strategies10 as S10
from run_round8 import HOLDOUTS, SPLITS, lev_for_7, ns, split_trades, stats

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 709


def judge(tr, idx_end):
    by = {h: split_trades(tr, h) for h in HOLDOUTS}
    hs = {h: stats(by[h], *SPLITS[h], idx_end) for h in HOLDOUTS}
    pooled = pd.concat(by.values())
    pn = pooled["net"].to_numpy()
    pt = float(pn.mean() / (pn.std(ddof=1) / math.sqrt(len(pn)))) if len(pn) > 1 and pn.std() > 0 else 0.0
    ms = [S5.complete_months(lab.daily_from_trades(by[h], pd.Timestamp(SPLITS[h][0], tz="UTC"),
          pd.Timestamp(SPLITS[h][1], tz="UTC") - pd.Timedelta(minutes=1) if SPLITS[h][1] else idx_end))
          for h in HOLDOUTS]
    pm = pd.concat(ms)
    pgeo = float((np.prod(1 + pm) ** (1 / len(pm)) - 1) * 100) if len(pm) else float("nan")
    req = [h for h in HOLDOUTS if not (h == "D" and hs[h]["trades"] < 100)]
    checks = {"each_holdout_positive": all(hs[h]["trades"] >= 100 and hs[h].get("mean_net_bps", -1) > 0 for h in req),
              "pooled_t_gt_2": pt > 2, "pooled_geo_positive": pgeo > 0}
    out = {"holdouts": hs, "pooled": {"trades": int(len(pn)), "mean_net_bps": float(pn.mean() * 1e4) if len(pn) else None,
           "mean_gross_bps": float(pooled["gross"].mean() * 1e4) if len(pn) else None, "t_stat": pt,
           "win_rate": float((pn > 0).mean()) if len(pn) else None, "geo_monthly_pct_1x": pgeo},
           "checks": checks, "pass": bool(all(checks.values())), "leverage_for_7pct": lev_for_7(by, idx_end)}
    for h in HOLDOUTS:
        x = hs[h]
        print(f"  {h}: {x['trades']} trades, net {x.get('mean_net_bps', float('nan')):+.2f} bps "
              f"(gross {x.get('mean_gross_bps', float('nan')):+.2f}), win {x.get('win_rate', float('nan')):.1%}, "
              f"{x.get('trades_per_month', 0):.0f}/mo, 1x {x.get('geo_monthly_pct_1x', float('nan')):+.2f}%/mo", flush=True)
    print("  pooled", out["pooled"], checks, "PASS" if out["pass"] else "FAIL", out["leverage_for_7pct"], flush=True)
    return out


def main():
    df = lab.load_candles(since=None)
    regime = S8.daily_regime_1m(lab.resample(df, "1D")["c"], 50, df.index)
    idx_end = df.index[-1]
    res = {"n_trials": N_TRIALS}
    tab = S10.session_table(df, regime)
    a, b = SPLITS["discovery"]
    disc = tab[(tab["entry_ns"] >= ns(a)) & (tab["entry_ns"] < ns(b))]
    cell_stats = []
    for (s, r), x in disc.groupby(["sess", "reg"]):
        v = x["raw"].to_numpy()
        cell_stats.append({"session": S10.SESSIONS[int(s)][0], "regime": int(r), "n": int(len(v)),
                           "mean_bps": float(v.mean() * 1e4), "t": float(v.mean() / (v.std(ddof=1) / math.sqrt(len(v))))})
    cells = S10.t1_select(disc)
    print("T1 discovery cells:", *[f"{c['session']}/{c['regime']:+d}: n={c['n']} mean {c['mean_bps']:+.1f} bps t {c['t']:+.2f}"
                                   for c in cell_stats], sep="\n  ")
    print("T1 selected:", {f"{S10.SESSIONS[s][0]}/{r:+d}": d for (s, r), d in cells.items()}, flush=True)
    res["T1"] = {"discovery_cells": cell_stats, "selected": {f"{S10.SESSIONS[s][0]}/{r}": d for (s, r), d in cells.items()}}
    if cells:
        res["T1"].update(judge(S10.t1_trades(tab, cells), idx_end))
    else:
        res["T1"]["pass"] = False
        print("  T1 FAIL: no cell with |t| > 2 on discovery")
    print("T2 opening-range breakout", flush=True)
    t2 = S10.t2_trades(df, regime)
    res["T2"] = {"discovery": stats(split_trades(t2, "discovery"), *SPLITS["discovery"], idx_end), **judge(t2, idx_end)}
    with open(os.path.join(HERE, "results", "round10.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
