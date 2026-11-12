"""Run PREREG9 once: three fixed bear-regime short variants judged on holdouts A-D. Writes results/round9.json."""
from __future__ import annotations

import json
import math
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies8 as S8
import strategies9 as S9
from run_round8 import HOLDOUTS, SPLITS, lev_for_7, split_trades, stats

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 700


def main():
    df = lab.load_candles(since=None)
    arr = [df[k].to_numpy() for k in ("o", "h", "l", "c")] + [df.index.as_unit("ns").asi8]
    regime = S8.daily_regime_1m(lab.resample(df, "1D")["c"], 50, df.index)
    idx_end = df.index[-1]
    a_trig = S9.a_trigger(df["c"], regime)
    b_trig, b_off = S9.b_signal(df, regime)
    sched = S9.ratchet_schedule()
    variants = {
        "A_maker": lambda: S9.sim_short(*arr, a_trig, True, S9.A_HOLD, sched_bps=sched),
        "A_taker": lambda: S9.sim_short(*arr, a_trig, False, S9.A_HOLD, sched_bps=sched),
        "B_rally_fade": lambda: S9.sim_short(*arr, b_trig, True, S9.B_HOLD, tp_off=b_off),
    }
    res = {"n_trials": N_TRIALS, "bear_share_of_minutes": float((regime < 0).mean()), "variants": {}}
    for name, run in variants.items():
        tr = run()
        by = {h: split_trades(tr, h) for h in HOLDOUTS}
        hs = {h: stats(by[h], *SPLITS[h], idx_end) for h in HOLDOUTS}
        disc = stats(split_trades(tr, "discovery"), *SPLITS["discovery"], idx_end)
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
        out = {"discovery": disc, "holdouts": hs,
               "pooled": {"trades": int(len(pn)), "mean_net_bps": float(pn.mean() * 1e4) if len(pn) else float("nan"),
                          "mean_gross_bps": float(pooled["gross"].mean() * 1e4) if len(pn) else float("nan"),
                          "t_stat": pt, "win_rate": float((pn > 0).mean()) if len(pn) else float("nan"),
                          "exit_mix": pooled["reason"].value_counts(normalize=True).round(3).to_dict(),
                          "geo_monthly_pct_1x": pgeo},
               "checks": checks, "pass": bool(all(checks.values())), "leverage_for_7pct": lev_for_7(by, idx_end)}
        res["variants"][name] = out
        print(name, "discovery", {k: disc.get(k) for k in ("trades", "mean_net_bps", "mean_gross_bps", "win_rate")}, flush=True)
        for h in HOLDOUTS:
            x = hs[h]
            print(f"  {h}: {x['trades']} trades, net {x.get('mean_net_bps', float('nan')):+.2f} bps "
                  f"(gross {x.get('mean_gross_bps', float('nan')):+.2f}), win {x.get('win_rate', float('nan')):.1%}, "
                  f"{x.get('trades_per_month', 0):.0f}/mo, 1x {x.get('geo_monthly_pct_1x', float('nan')):+.2f}%/mo", flush=True)
        print("  pooled", out["pooled"], checks, "PASS" if out["pass"] else "FAIL", out["leverage_for_7pct"], flush=True)
    with open(os.path.join(HERE, "results", "round9.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
