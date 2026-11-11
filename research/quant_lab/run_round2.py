"""Run PREREG2 once: Q1 regression, S5 and S6 grids on discovery, PBO, then one run per final candidate on
holdout A (2025-07..) and untouched holdout B (earliest data .. 2022-12-16). Writes results/round2.json."""
from __future__ import annotations

import json
import os
import time

import numpy as np
import pandas as pd

import lab
import strategies2 as S2
from run_lab import segments_as_trades

HERE = os.path.dirname(os.path.abspath(__file__))
B_END = pd.Timestamp("2022-12-17", tz="UTC")
PRIOR_TRIALS = 586


def periods(frame):
    return {"discovery": frame[(frame.index >= lab.DATA_START) & (frame.index < lab.DISCOVERY_END)],
            "holdout_A": frame[frame.index >= lab.DISCOVERY_END],
            "holdout_B": frame[frame.index < B_END]}


def main():
    t0 = time.time()
    full = lab.load_candles(since=None)
    res = {"data": {"earliest": str(full.index[0]), "latest": str(full.index[-1])}}
    b_months = (B_END - full.index[0]).days / 30.4
    res["holdout_B_months"] = round(b_months, 1)
    res["holdout_B_sufficient"] = bool(b_months >= 12)
    n_trials = PRIOR_TRIALS + len(S2.S5_GRID) + len(S2.S6_GRID)
    res["n_trials"] = n_trials

    # Q1: features are computed on the full series (causal), then split
    q = S2.q1_frame(full)
    res["Q1"] = {k: S2.q1_regression(v) for k, v in periods(q).items()}
    print("Q1", json.dumps(res["Q1"]), flush=True)

    daily = lab.resample(full, "1D")
    h1 = lab.resample(full, "1h")
    # S5 / S6 positions are computed on the full series (causal), returns evaluated per period
    for name, grid, frame, posfn, caps in (
            ("S5_vol_managed", S2.S5_GRID, daily, S2.s5_positions, True),
            ("S6_quantum_walk_regime", S2.S6_GRID, h1, S2.s6_positions, False)):
        disc_daily, stats = {}, {}
        for i, p in enumerate(grid):
            pos = posfn(frame, p, 2.0) if caps else posfn(frame, p)
            r = lab.position_returns(frame, pos, lab.US_FUTURES)
            mask = (frame.index >= lab.DATA_START) & (frame.index < lab.DISCOVERY_END)
            rd = r[mask]
            disc_daily[i] = lab.daily_from_bars(rd)
            stats[i] = lab.summarize(disc_daily[i], segments_as_trades(rd, pos[mask]))
        mat = pd.DataFrame(disc_daily)
        sd = mat.std().replace(0, np.nan)
        var_sr = float(np.var((mat.mean() / sd).dropna()))
        best = max(stats, key=lambda k: stats[k]["sharpe"])
        p = grid[best]
        entry = {"discovery_best_params": p, "discovery_best": stats[best], "pbo": lab.pbo_cscv(mat, 10),
                 "discovery_median_sharpe": float(np.median([s["sharpe"] for s in stats.values()]))}
        for hname in ("holdout_A", "holdout_B"):
            out = {}
            for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS, lab.US_SPOT):
                cap = 1.0 if costs is lab.US_SPOT else 2.0
                pos = posfn(frame, p, cap) if caps else posfn(frame, p)
                r = lab.position_returns(frame, pos, costs)
                per = periods(pd.DataFrame({"r": r, "pos": pos}, index=frame.index))[hname]
                if per.empty:
                    continue
                dd = lab.daily_from_bars(per["r"])
                out[costs.name] = lab.summarize(dd, segments_as_trades(per["r"], per["pos"].to_numpy()))
                if costs is lab.US_FUTURES:
                    out["dsr"] = lab.deflated_sharpe(dd, n_trials, var_sr)
                    bh = lab.daily_from_bars(periods(frame["c"].pct_change().fillna(0).to_frame("r"))[hname]["r"])
                    out["buy_hold"] = lab.summarize(bh)
            entry[hname] = out
        disc_bh = lab.summarize(lab.daily_from_bars(periods(frame["c"].pct_change().fillna(0).to_frame("r"))["discovery"]["r"]))
        entry["discovery_buy_hold"] = disc_bh

        def ok(h):
            f = h.get("us-futures", {})
            min_n = (f.get("days", 0) >= 365) if name.startswith("S5") else (f.get("trades", 0) >= 100)
            return f.get("sharpe", 0) > 1.0 and h.get("dsr", 0) > 0.95 and f.get("total_return_pct", 0) > 0 and min_n
        entry["pass"] = bool(entry["pbo"] < 0.30 and ok(entry["holdout_A"]) and ok(entry["holdout_B"])
                             and res["holdout_B_sufficient"])
        if name.startswith("S5"):
            entry["beats_buy_hold_sharpe_everywhere"] = bool(
                stats[best]["sharpe"] > disc_bh["sharpe"]
                and all(entry[h].get("us-futures", {}).get("sharpe", -9) > entry[h].get("buy_hold", {}).get("sharpe", 9)
                        for h in ("holdout_A", "holdout_B")))
        res[name] = entry
        a, b = entry["holdout_A"].get("us-futures", {}), entry["holdout_B"].get("us-futures", {})
        print(f"{name}: best={p} disc_sharpe={stats[best]['sharpe']:.2f} PBO={entry['pbo']:.2f} | "
              f"A: ret={a.get('total_return_pct', 0):.1f}% sharpe={a.get('sharpe', 0):.2f} (B&H {entry['holdout_A'].get('buy_hold', {}).get('sharpe', 0):.2f}) | "
              f"B: ret={b.get('total_return_pct', 0):.1f}% sharpe={b.get('sharpe', 0):.2f} (B&H {entry['holdout_B'].get('buy_hold', {}).get('sharpe', 0):.2f}) "
              f"| PASS={entry['pass']}", flush=True)
    res["elapsed_s"] = round(time.time() - t0, 1)
    with open(os.path.join(HERE, "results", "round2.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)
    print("holdout B months:", res["holdout_B_months"], "| wrote results/round2.json")


if __name__ == "__main__":
    main()
