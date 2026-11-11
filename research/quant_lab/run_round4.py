"""Run PREREG4 G1 once: live-engine entry (S1 score, mode +1) with win-rate-first brackets; pick on discovery
(win >= 55%, best net Sharpe), compare with the current live geometry on holdouts A/B/C, apply the
pre-registered adoption rule. Writes results/round4.json."""
from __future__ import annotations

import json
import os
import time

import numpy as np
import pandas as pd

import lab
import strategies3 as S3
from run_round3 import cut

HERE = os.path.dirname(os.path.abspath(__file__))
PRIOR_TRIALS = 660
PAIRS = [(20, 40), (20, 60), (20, 80), (30, 60), (30, 80), (40, 60), (40, 80)]
G1_GRID = [dict(th=th, tp=t, sl=s) for th in (0.65, 0.75) for t, s in PAIRS]
LIVE_TP, LIVE_SL = 39, 30          # live: 35 bps net target + 4 bps maker fees; 30 bps stop
MAX_HOLD, LOSER_STOP = 240, 45     # same exits as round-1 S1
HOLDOUTS = ("holdout_A", "holdout_B", "holdout_C")


def run(df, conv, p, costs, period):
    part = cut(df.assign(conv=conv), period)
    sig = S3.s7_signal(part["conv"].to_numpy(), p["th"])
    n = len(part)
    tr = lab.simulate_brackets(part["o"].to_numpy(), part["h"].to_numpy(), part["l"].to_numpy(),
                               part["c"].to_numpy(), part.index.as_unit("ns").asi8, sig,
                               np.full(n, float(p["tp"])), np.full(n, float(p["sl"])),
                               max_hold=MAX_HOLD, loser_stop=LOSER_STOP, costs=costs)
    dd = lab.daily_from_trades(tr, part.index[0], part.index[-1])
    s = lab.summarize(dd, tr)
    s["mean_net_bps"] = float(tr["net"].mean() * 1e4) if len(tr) else float("nan")
    s["pred_zero_edge_win_pct"] = 100 * p["sl"] / (p["tp"] + p["sl"])
    s["months"] = round((part.index[-1] - part.index[0]).days / 30.4, 1)
    return s, dd


def adopt(g1: dict, live: dict) -> dict:
    win_ok = all(g1[h]["win_rate_pct"] >= 55 for h in HOLDOUTS)
    net_better = sum(g1[h]["mean_net_bps"] >= live[h]["mean_net_bps"] for h in HOLDOUTS)
    ret_better = sum(g1[h]["total_return_pct"] >= live[h]["total_return_pct"] for h in HOLDOUTS)
    return {"h_win": bool(win_ok), "net_bps_better_count": int(net_better), "return_better_count": int(ret_better),
            "adopt": bool(win_ok and net_better >= 2 and ret_better >= 2)}


def main():
    t0 = time.time()
    full = lab.load_candles(since=None)
    conv = S3.s1_conv(full)
    n_trials = PRIOR_TRIALS + len(G1_GRID) + 1
    res = {"data": {"earliest": str(full.index[0]), "latest": str(full.index[-1])}, "n_trials": n_trials}
    disc, dailies = {}, {}
    for i, p in enumerate(G1_GRID):
        disc[i], dailies[i] = run(full, conv, p, lab.US_FUTURES, "discovery")
        print(f"G1 {i:2d} {p} sharpe={disc[i]['sharpe']:.2f} win={disc[i]['win_rate_pct']:.1f}% "
              f"(pred {disc[i]['pred_zero_edge_win_pct']:.1f}) net={disc[i]['mean_net_bps']:.1f}bps "
              f"trades={disc[i]['trades']}", flush=True)
    mat = pd.DataFrame(dailies)
    sd = mat.std().replace(0, np.nan)
    var_sr = float(np.var((mat.mean() / sd).dropna()))
    eligible = [i for i in disc if disc[i]["win_rate_pct"] >= 55]
    res["discovery_grid"] = [{**G1_GRID[i], **disc[i]} for i in disc]
    res["pbo"] = lab.pbo_cscv(mat, 10)
    if not eligible:
        res["pick"] = None
        res["adoption"] = {"adopt": False, "reason": "no discovery config reached 55% win rate"}
    else:
        best = max(eligible, key=lambda i: disc[i]["sharpe"])
        p = G1_GRID[best]
        live_p = dict(th=p["th"], tp=LIVE_TP, sl=LIVE_SL)
        res["pick"], res["comparator"] = p, live_p
        g1h, liveh = {}, {}
        for h in HOLDOUTS:
            g1h[h], dd = run(full, conv, p, lab.US_FUTURES, h)
            g1h[h]["dsr"] = lab.deflated_sharpe(dd, n_trials, var_sr)
            g1h[h]["spot"] = run(full, conv, p, lab.US_SPOT, h)[0]
            liveh[h] = run(full, conv, live_p, lab.US_FUTURES, h)[0]
        res["g1_holdouts"], res["live_holdouts"] = g1h, liveh
        res["adoption"] = adopt(g1h, liveh)
        res["h_profit_pass"] = bool(res["pbo"] < 0.30 and all(
            g1h[h]["sharpe"] > 1 and g1h[h]["dsr"] > 0.95 and g1h[h]["total_return_pct"] > 0
            and g1h[h]["trades"] >= 100 for h in HOLDOUTS))
        for h in HOLDOUTS:
            print(f"{h}: G1 win={g1h[h]['win_rate_pct']:.1f}% net={g1h[h]['mean_net_bps']:.1f}bps "
                  f"ret={g1h[h]['total_return_pct']:.1f}% | live win={liveh[h]['win_rate_pct']:.1f}% "
                  f"net={liveh[h]['mean_net_bps']:.1f}bps ret={liveh[h]['total_return_pct']:.1f}%", flush=True)
        print("adoption:", res["adoption"], "| H-profit pass:", res["h_profit_pass"], flush=True)
    res["elapsed_s"] = round(time.time() - t0, 1)
    with open(os.path.join(HERE, "results", "round4.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)
    print("wrote results/round4.json")


if __name__ == "__main__":
    main()
