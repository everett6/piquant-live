"""S4 (PREREG): simulated quantum annealing vs random search as parameter optimizers for 1h trend following.

Space: fast EMA (16 values) x slow EMA (16 values) x mode (long/short, long/flat) = 512 configs, 9 bits.
Each method gets 100 distinct evaluations of the discovery-period Sharpe (us-futures costs). Both methods are
also run inside walk-forward (re-optimised on every 12-month train window, tested on the next 3 months), and
each method's full-discovery pick is run once on the holdout. Writes results/s4.json.
"""
from __future__ import annotations

import json
import os
import time

import numpy as np
import pandas as pd

import lab
import quantum_inspired as Q
import strategies as S

HERE = os.path.dirname(os.path.abspath(__file__))
FAST = [4, 5, 6, 8, 10, 12, 14, 16, 20, 24, 30, 36, 44, 54, 66, 96]
SLOW = [48, 60, 72, 90, 110, 130, 168, 200, 240, 290, 336, 400, 480, 560, 640, 720]
FIELDS = [("fast", FAST), ("slow", SLOW), ("mode", ["ls", "lf"])]
BUDGET = 100


def daily_for(h1, p, costs=lab.US_FUTURES):
    if p["fast"] >= p["slow"]:
        return None
    return lab.daily_from_bars(lab.position_returns(h1, S.s2_positions(h1, p), costs))


def energy_fn(h1):
    def energy(bits):
        d = daily_for(h1, Q.decode(bits, FIELDS))
        if d is None or d.std() == 0:
            return 5.0  # invalid (fast >= slow) or flat: worst energy
        return -float(d.mean() / d.std() * np.sqrt(365))
    return energy


def optimise(method, h1, seed):
    obj = Q.BudgetedObjective(energy_fn(h1), BUDGET)
    bits, e = (Q.sqa(obj, 9, seed=seed, temperature=0.1, sweeps=400) if method == "sqa"
               else Q.random_search(obj, 9, seed=seed))
    return Q.decode(bits, FIELDS), -e, obj


def main():
    t0 = time.time()
    df = lab.load_candles()
    h1 = lab.resample(df, "1h")
    disc, hold = h1[h1.index < lab.DISCOVERY_END], h1[h1.index >= lab.DISCOVERY_END]
    res, seen = {}, set()
    for method in ("sqa", "random"):
        picks = []
        for seed in range(3):  # 3 seeds per method to show optimiser variance
            p, sharpe, obj = optimise(method, disc, seed)
            seen |= set(obj.cache)
            picks.append({"seed": seed, "params": p, "discovery_sharpe": sharpe})
        # walk-forward: re-optimise per window (seed 0), test on the next quarter
        days = pd.date_range(disc.index[0].floor("D"), disc.index[-1].floor("D"), freq="D", tz="UTC")
        oos = []
        t = 365
        while t < len(days):
            tr = disc[(disc.index >= days[t - 365]) & (disc.index < days[t])]
            te_end = days[min(t + 91, len(days) - 1)]
            p, _, obj = optimise(method, tr, 0)
            seen |= set(obj.cache)
            te = h1[(h1.index >= days[t] - pd.Timedelta(days=30)) & (h1.index < te_end)]  # 30d warm-up for EMAs
            d = daily_for(te, p)
            if d is not None:
                oos.append(d[d.index >= days[t]])
            t += 91
        wf = pd.concat(oos) if oos else pd.Series(dtype=float)
        best = max(picks, key=lambda x: x["discovery_sharpe"])
        warm = h1[h1.index >= lab.DISCOVERY_END - pd.Timedelta(days=30)]
        dh = daily_for(warm, best["params"])
        dh = dh[dh.index >= lab.DISCOVERY_END]
        res[method] = {"picks": picks, "walk_forward_oos": lab.summarize(wf),
                       "holdout_pick": best["params"], "holdout": lab.summarize(dh)}
        print(method, "picks", [(x["params"], round(x["discovery_sharpe"], 2)) for x in picks],
              "| WF sharpe", round(res[method]["walk_forward_oos"]["sharpe"], 2),
              "| HOLDOUT", best["params"], "ret", round(res[method]["holdout"]["total_return_pct"], 1), "% sharpe",
              round(res[method]["holdout"]["sharpe"], 2), flush=True)
    res["distinct_configs_evaluated"] = len(seen)
    res["elapsed_s"] = round(time.time() - t0, 1)
    with open(os.path.join(HERE, "results", "s4.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)
    print("distinct configs evaluated (trials charged):", len(seen), f"in {res['elapsed_s']}s")


if __name__ == "__main__":
    main()
