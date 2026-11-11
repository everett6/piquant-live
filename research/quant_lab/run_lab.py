"""Run the pre-registered S0-S3 experiment (PREREG.md). Writes results/run1.json and prints a summary.

Order: discovery grids -> PBO per strategy -> walk-forward on discovery -> DSR over all configs tried ->
ONE holdout run per strategy's final candidate (chosen on the full discovery period).
"""
from __future__ import annotations

import json
import os
import sys
import time

import numpy as np
import pandas as pd

import lab
import strategies as S

HERE = os.path.dirname(os.path.abspath(__file__))


def wf_select(daily: pd.DataFrame, train_days=365, test_days=91) -> pd.Series:
    """Walk-forward: each test window uses the config with the best train-window Sharpe."""
    out = []
    t = train_days
    while t < len(daily):
        tr = daily.iloc[t - train_days:t]
        sd = tr.std().replace(0, np.nan)
        best = (tr.mean() / sd).idxmax()
        out.append(daily.iloc[t:t + test_days][best])
        t += test_days
    return pd.concat(out) if out else pd.Series(dtype=float)


def segments_as_trades(r: pd.Series, pos: np.ndarray) -> pd.DataFrame:
    """Turn a held-position return series into per-holding-period 'trades' for win-rate stats."""
    held = np.concatenate([[0.0], pos[:-1]])
    seg = np.cumsum(np.concatenate([[1], (np.diff(held) != 0).astype(int)]))
    df = pd.DataFrame({"r": r.to_numpy(), "seg": seg, "held": held, "t": r.index.asi8})
    df = df[df["held"] != 0]
    g = df.groupby("seg")
    net = np.expm1(g["r"].apply(lambda x: np.log1p(x).sum()))
    return pd.DataFrame({"net": net.to_numpy(), "gross": net.to_numpy(), "exit_ns": g["t"].max().to_numpy()})


def main():
    t0 = time.time()
    df = lab.load_candles()
    disc = df[df.index < lab.DISCOVERY_END]
    hold = df[df.index >= lab.DISCOVERY_END]
    h1 = lab.resample(df, "1h")
    h1_disc, h1_hold = h1[h1.index < lab.DISCOVERY_END], h1[h1.index >= lab.DISCOVERY_END]
    d0, d1 = disc.index[0], disc.index[-1]
    res = {"data": {"start": str(df.index[0]), "end": str(df.index[-1]), "bars_1m": len(df),
                    "discovery_end": str(lab.DISCOVERY_END)}, "strategies": {}}
    n_trials = 0

    # S0 buy and hold
    for name, part in (("discovery", disc), ("holdout", hold)):
        r = lab.daily_from_bars(part["c"].pct_change().fillna(0))
        res["strategies"].setdefault("S0_buy_hold", {})[name] = lab.summarize(r)

    # S1 grid
    f = S.s1_features(df)
    fd = f[f.index < lab.DISCOVERY_END]
    s1_daily, s1_stats = {}, {}
    for i, p in enumerate(S.S1_GRID):
        tr = S.s1_run(disc, fd, p, lab.US_FUTURES)
        s1_daily[i] = lab.daily_from_trades(tr, d0, d1)
        s1_stats[i] = lab.summarize(s1_daily[i], tr)
    n_trials += len(S.S1_GRID)
    print(f"S1 grid done {time.time() - t0:.0f}s", flush=True)

    # S2 grid
    s2_daily, s2_stats = {}, {}
    for i, p in enumerate(S.S2_GRID):
        pos = S.s2_positions(h1_disc, p)
        r = lab.position_returns(h1_disc, pos, lab.US_FUTURES)
        s2_daily[i] = lab.daily_from_bars(r)
        s2_stats[i] = lab.summarize(s2_daily[i], segments_as_trades(r, pos))
    n_trials += len(S.S2_GRID)
    print(f"S2 grid done {time.time() - t0:.0f}s", flush=True)

    # S3 grid
    levels = {w: S.s3_levels(df, w) for w in (20, 60)}
    s3_daily, s3_stats = {}, {}
    for i, p in enumerate(S.S3_GRID):
        tr = S.s3_run(disc, h1_disc, levels[p["window"]], p, lab.US_FUTURES)
        s3_daily[i] = lab.daily_from_trades(tr, d0, d1)
        s3_stats[i] = lab.summarize(s3_daily[i], tr)
    n_trials += len(S.S3_GRID)
    print(f"S3 grid done {time.time() - t0:.0f}s", flush=True)

    grids = {"S1_engine_momentum": (S.S1_GRID, s1_daily, s1_stats),
             "S2_trend_1h": (S.S2_GRID, s2_daily, s2_stats),
             "S3_qho_levels": (S.S3_GRID, s3_daily, s3_stats)}
    all_sharpes_daily = []
    for name, (grid, daily, stats) in grids.items():
        mat = pd.DataFrame(daily)
        sd = mat.std().replace(0, np.nan)
        all_sharpes_daily += list((mat.mean() / sd).dropna())
    var_sr = float(np.var(all_sharpes_daily)) if all_sharpes_daily else None

    for name, (grid, daily, stats) in grids.items():
        mat = pd.DataFrame(daily)
        best = max(stats, key=lambda k: stats[k]["sharpe"])
        wf = wf_select(mat)
        entry = res["strategies"].setdefault(name, {})
        entry["grid_size"] = len(grid)
        entry["discovery_best_params"] = grid[best]
        entry["discovery_best"] = stats[best]
        entry["discovery_median_sharpe"] = float(np.median([s["sharpe"] for s in stats.values()]))
        entry["discovery_share_positive"] = float(np.mean([s["total_return_pct"] > 0 for s in stats.values()]))
        entry["pbo"] = lab.pbo_cscv(mat, 10)
        entry["walk_forward_oos"] = lab.summarize(wf) if len(wf) else {}
        entry["dsr_best_discovery"] = lab.deflated_sharpe(mat[best], n_trials, var_sr)
        # ONE holdout run for the final candidate (best on full discovery), in each cost profile
        p = grid[best]
        hold_out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS, lab.US_SPOT):
            if name.startswith("S1"):
                fh = f[f.index >= lab.DISCOVERY_END]
                tr = S.s1_run(hold, fh, p, costs)
                dd = lab.daily_from_trades(tr, hold.index[0], hold.index[-1])
                hold_out[costs.name] = lab.summarize(dd, tr)
            elif name.startswith("S2"):
                pos = S.s2_positions(h1_hold, p)
                r = lab.position_returns(h1_hold, pos, costs)
                dd = lab.daily_from_bars(r)
                hold_out[costs.name] = lab.summarize(dd, segments_as_trades(r, pos))
            else:
                tr = S.s3_run(hold, h1_hold, levels[p["window"]], p, costs)
                dd = lab.daily_from_trades(tr, hold.index[0], hold.index[-1])
                hold_out[costs.name] = lab.summarize(dd, tr)
            if costs is lab.US_FUTURES:
                entry["holdout_dsr"] = lab.deflated_sharpe(dd, n_trials, var_sr)
        entry["holdout"] = hold_out
        h = hold_out[lab.US_FUTURES.name]
        entry["pass"] = bool(h["sharpe"] > 1.0 and entry["holdout_dsr"] > 0.95 and entry["pbo"] < 0.30
                             and h.get("trades", 0) >= 100 and h["total_return_pct"] > 0)
    res["n_trials"] = n_trials
    res["elapsed_s"] = round(time.time() - t0, 1)
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    out = os.path.join(HERE, "results", sys.argv[1] if len(sys.argv) > 1 else "run1.json")
    with open(out, "w") as fh:
        json.dump(res, fh, indent=1, default=str)
    for name, e in res["strategies"].items():
        if name.startswith("S0"):
            print(name, "disc", round(e["discovery"]["total_return_pct"], 1), "% hold", round(e["holdout"]["total_return_pct"], 1), "%")
            continue
        h = e["holdout"]["us-futures"]
        print(f"{name}: best={e['discovery_best_params']} disc_sharpe={e['discovery_best']['sharpe']:.2f} "
              f"median={e['discovery_median_sharpe']:.2f} PBO={e['pbo']:.2f} WF_sharpe={e['walk_forward_oos'].get('sharpe', 0):.2f} "
              f"| HOLDOUT sharpe={h['sharpe']:.2f} ret={h['total_return_pct']:.1f}% win={h.get('win_rate_pct', 0):.1f}% "
              f"trades={h.get('trades', 0)} DSR={e['holdout_dsr']:.2f} PASS={e['pass']}")
    print("wrote", out, f"in {res['elapsed_s']}s")


if __name__ == "__main__":
    main()
