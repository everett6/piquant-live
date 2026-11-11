"""Run PREREG5 once: M1 grid on discovery (beat rate vs BTC buy-and-hold), pick, then holdouts A/B/C/D.
Writes results/round5.json."""
from __future__ import annotations

import json
import os
import time

import numpy as np
import pandas as pd

import lab
import strategies5 as S5

HERE = os.path.dirname(os.path.abspath(__file__))
PRIOR_TRIALS = 675
PERIODS = {
    "discovery": (lab.DATA_START, lab.DISCOVERY_END),
    "holdout_A": (lab.DISCOVERY_END, None),
    "holdout_B": (pd.Timestamp("2021-01-01", tz="UTC"), pd.Timestamp("2022-12-17", tz="UTC")),
    "holdout_C": (pd.Timestamp("2018-01-01", tz="UTC"), pd.Timestamp("2021-01-01", tz="UTC")),
    "holdout_D": (pd.Timestamp("2016-01-01", tz="UTC"), pd.Timestamp("2018-01-01", tz="UTC")),
}
HOLDOUTS = ("holdout_A", "holdout_B", "holdout_C", "holdout_D")


def cut(x, name):
    a, b = PERIODS[name]
    m = x.index >= a
    if b is not None:
        m &= x.index < b
    return x[m]


def strat_returns(daily, p, costs):
    lev = min(p["lev"], 1.0) if not costs.can_short and costs.funding_bps_h == 0 else p["lev"]
    return lab.position_returns(daily, S5.m1_positions(daily, p["n"], lev), costs)


def main():
    t0 = time.time()
    full = lab.load_candles(since=None)
    daily = lab.resample(full, "1D")
    bh = daily["c"].pct_change().fillna(0)
    res = {"data": {"earliest": str(daily.index[0]), "latest": str(daily.index[-1])},
           "n_trials": PRIOR_TRIALS + len(S5.M1_GRID)}
    disc, rets, excess = {}, {}, {}
    for i, p in enumerate(S5.M1_GRID):
        r = strat_returns(daily, p, lab.US_FUTURES)
        rets[i] = r
        rd, bd = cut(r, "discovery"), cut(bh, "discovery")
        st = lab.summarize(rd)
        st.update(S5.beat_stats(rd, bd))
        disc[i] = st
        sm, bm = S5.complete_months(rd), S5.complete_months(bd)
        excess[i] = (sm - bm)
        print(f"M1 {i:2d} {p} beat={st['beat_rate']:.3f} ({st['beats']}/{st['months']}) sharpe={st['sharpe']:.2f} "
              f"ret={st['total_return_pct']:.1f}%", flush=True)
    best = max(disc, key=lambda i: (disc[i]["beat_rate"], disc[i]["sharpe"]))
    pick = S5.M1_GRID[best]
    mat_daily = pd.DataFrame({i: cut(rets[i], "discovery") for i in rets})
    sd = mat_daily.std().replace(0, np.nan)
    var_sr = float(np.var((mat_daily.mean() / sd).dropna()))
    res["discovery_grid"] = [{**S5.M1_GRID[i], **disc[i]} for i in disc]
    res["pick"] = pick
    res["discovery_buy_hold"] = lab.summarize(cut(bh, "discovery"))
    res["pbo_monthly_excess"] = lab.pbo_cscv(pd.DataFrame(excess), 10)
    pooled_k = pooled_n = 0
    for h in HOLDOUTS:
        out = {}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS, lab.US_SPOT):
            r = cut(strat_returns(daily, pick, costs), h)
            b = cut(bh, h)
            if r.empty:
                continue
            st = lab.summarize(r)
            st.update(S5.beat_stats(r, b))
            out[costs.name] = st
            if costs is lab.US_FUTURES:
                out["dsr"] = lab.deflated_sharpe(r, res["n_trials"], var_sr)
                out["buy_hold"] = lab.summarize(b)
                pooled_k += st["beats"]
                pooled_n += st["months"]
        res[h] = out
    f = {h: res[h].get("us-futures", {}) for h in HOLDOUTS}
    res["holdout_D_sufficient"] = bool(f["holdout_D"].get("months", 0) >= 18)
    res["pooled"] = {"beats": pooled_k, "months": pooled_n,
                     "beat_rate": pooled_k / pooled_n if pooled_n else float("nan"),
                     "binom_p_one_sided": S5.binom_p_one_sided(pooled_k, pooled_n)}
    res["pass"] = bool(res["holdout_D_sufficient"] and all(f[h].get("beat_rate", 0) >= 0.60 for h in HOLDOUTS)
                       and res["pooled"]["binom_p_one_sided"] < 0.05)
    for h in HOLDOUTS:
        x = f[h]
        print(f"{h}: beat={x.get('beat_rate', float('nan')):.3f} ({x.get('beats')}/{x.get('months')}) "
              f"ret={x.get('total_return_pct', 0):.1f}% vs B&H {res[h].get('buy_hold', {}).get('total_return_pct', 0):.1f}% "
              f"sharpe={x.get('sharpe', 0):.2f} vs {res[h].get('buy_hold', {}).get('sharpe', 0):.2f}", flush=True)
    print(f"pick={pick} pooled={res['pooled']} D sufficient={res['holdout_D_sufficient']} PASS={res['pass']}")
    res["elapsed_s"] = round(time.time() - t0, 1)
    with open(os.path.join(HERE, "results", "round5.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)
    print("wrote results/round5.json")


if __name__ == "__main__":
    main()
