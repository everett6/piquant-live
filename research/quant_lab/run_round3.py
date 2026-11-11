"""Run PREREG3 once: T0 theory matrices (discovery), S7/S8 grids on discovery, PBO, then one run per final
candidate on holdouts A, B and C. Writes results/round3.json."""
from __future__ import annotations

import json
import os
import time

import numpy as np
import pandas as pd

import lab
import strategies3 as S3

HERE = os.path.dirname(os.path.abspath(__file__))
PRIOR_TRIALS = 600
PERIODS = {
    "discovery": (lab.DATA_START, lab.DISCOVERY_END),
    "holdout_A": (lab.DISCOVERY_END, None),
    "holdout_B": (pd.Timestamp("2021-01-01", tz="UTC"), pd.Timestamp("2022-12-17", tz="UTC")),
    "holdout_C": (pd.Timestamp("2018-01-01", tz="UTC"), pd.Timestamp("2021-01-01", tz="UTC")),
}


def cut(df: pd.DataFrame, name: str) -> pd.DataFrame:
    a, b = PERIODS[name]
    m = df.index >= a
    if b is not None:
        m &= df.index < b
    return df[m]


def run_period(df, sig, p, max_hold, costs, name):
    part = cut(df.assign(sig=sig), name)
    if len(part) < 3:
        return pd.DataFrame(columns=["entry_ns", "exit_ns", "side", "gross", "net", "hold_h", "reason"]), part
    return S3.brackets(part, part["sig"].to_numpy(), p["tp"], p["sl"], max_hold, costs), part


def evaluate(name, grid, frame, sigfn, max_hold, n_trials):
    disc_daily, stats, gross = {}, {}, {}
    for i, p in enumerate(grid):
        tr, part = run_period(frame, sigfn(p), p, max_hold, lab.US_FUTURES, "discovery")
        disc_daily[i] = lab.daily_from_trades(tr, part.index[0], part.index[-1])
        stats[i] = lab.summarize(disc_daily[i], tr)
        gross[i] = stats[i].get("avg_gross_bps", 0.0)
        print(f"  {name} {i:2d} {p} sharpe={stats[i]['sharpe']:.2f} trades={stats[i].get('trades')} "
              f"win={stats[i].get('win_rate_pct', 0):.1f}% gross={gross[i]:.1f}bps", flush=True)
    mat = pd.DataFrame(disc_daily)
    sd = mat.std().replace(0, np.nan)
    var_sr = float(np.var((mat.mean() / sd).dropna()))
    best = max(stats, key=lambda k: stats[k]["sharpe"])
    p = grid[best]
    entry = {"discovery_best_params": p, "discovery_best": stats[best], "pbo": lab.pbo_cscv(mat, 10),
             "discovery_median_sharpe": float(np.median([s["sharpe"] for s in stats.values()])),
             "discovery_grid": [{**grid[i], **{k: stats[i].get(k) for k in ("sharpe", "trades", "win_rate_pct",
                                 "avg_gross_bps", "avg_win_bps", "avg_loss_bps", "total_return_pct")}}
                                for i in range(len(grid))]}
    sig = sigfn(p)
    for h in ("holdout_A", "holdout_B", "holdout_C"):
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS, lab.US_SPOT):
            tr, part = run_period(frame, sig, p, max_hold, costs, h)
            if part.empty:
                continue
            dd = lab.daily_from_trades(tr, part.index[0], part.index[-1])
            out[costs.name] = lab.summarize(dd, tr)
            if costs is lab.US_FUTURES:
                out["dsr"] = lab.deflated_sharpe(dd, n_trials, var_sr)
                out["months"] = round((part.index[-1] - part.index[0]).days / 30.4, 1)
                out["buy_hold"] = lab.summarize(lab.daily_from_bars(part["c"].pct_change().fillna(0)))
        entry[h] = out

    def ok(hh):
        f = hh.get("us-futures", {})
        return (f.get("sharpe", 0) > 1.0 and hh.get("dsr", 0) > 0.95 and f.get("total_return_pct", 0) > 0
                and f.get("trades", 0) >= 100)
    c_months = entry["holdout_C"].get("months", 0)
    entry["holdout_C_sufficient"] = bool(c_months >= 24)
    entry["pass"] = bool(entry["pbo"] < 0.30 and entry["holdout_C_sufficient"]
                         and all(ok(entry[h]) for h in ("holdout_A", "holdout_B", "holdout_C")))
    line = " | ".join(f"{h[-1]}: ret={entry[h].get('us-futures', {}).get('total_return_pct', 0):.1f}% "
                      f"sh={entry[h].get('us-futures', {}).get('sharpe', 0):.2f} "
                      f"win={entry[h].get('us-futures', {}).get('win_rate_pct', 0):.1f}%"
                      for h in ("holdout_A", "holdout_B", "holdout_C"))
    print(f"{name}: best={p} disc_sharpe={stats[best]['sharpe']:.2f} PBO={entry['pbo']:.2f} | {line} "
          f"| PASS={entry['pass']}", flush=True)
    return entry


def main():
    t0 = time.time()
    full = lab.load_candles(since=None)
    res = {"data": {"earliest": str(full.index[0]), "latest": str(full.index[-1])}}
    n_trials = PRIOR_TRIALS + len(S3.S7_GRID) + len(S3.S8_GRID)
    res["n_trials"] = n_trials
    cost_bps = 2 * (lab.US_FUTURES.taker_bps + lab.US_FUTURES.slip_bps)

    # T0 theory check: discovery only, random side whenever flat
    disc = cut(full, "discovery")
    sig = S3.t0_signal(len(disc))
    cells = []
    for tp in S3.T0_TP:
        for sl in S3.T0_SL:
            tr = S3.brackets(disc, sig, tp, sl, 1440, lab.US_FUTURES)
            cells.append(S3.t0_cell(tr, tp, sl, cost_bps))
            c = cells[-1]
            print(f"T0 tp={tp} sl={sl} win={c['win_rate']:.3f} pred={c['pred_win_rate']:.3f} "
                  f"gross={c['mean_gross_bps']:.2f} net={c['mean_net_bps']:.2f} p*={c['net_breakeven_p']:.3f}", flush=True)
    res["T0"] = {"cells": cells, "supported": S3.t0_supported(cells)}
    print("T0 supported:", res["T0"]["supported"], flush=True)

    conv = S3.s1_conv(full)
    res["S7_live_signal_wide_brackets"] = evaluate(
        "S7", S3.S7_GRID, full, lambda p: S3.s7_signal(conv, p["th"]), S3.S7_MAX_HOLD, n_trials)

    m5, h1 = lab.resample(full, "5min"), lab.resample(full, "1h")
    regimes = {}

    def s8sig(p):
        k = (p["fast"], p["slow"])
        if k not in regimes:
            regimes[k] = S3.s8_regime(h1, m5, *k)
        return regimes[k]
    res["S8_trend_regime_wide_brackets"] = evaluate("S8", S3.S8_GRID, m5, s8sig, S3.S8_MAX_HOLD, n_trials)

    res["elapsed_s"] = round(time.time() - t0, 1)
    with open(os.path.join(HERE, "results", "round3.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)
    print("wrote results/round3.json in", res["elapsed_s"], "s")


if __name__ == "__main__":
    main()
