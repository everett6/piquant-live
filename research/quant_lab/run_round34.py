"""Run PREREG34 once: QA interference sizing (against the four-clock split E4 on the 6-hour grid) and QB tunnelling
sizing (against P1 on daily bars), each also against a constant-size line. Writes results/round34.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies30 as S30
import strategies34 as S34
from run_round7 import cut
from run_round24 import CORE_PERIODS, stats

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 762
T0 = pd.Timestamp("2016-01-01", tz="UTC")


def mean_from(x, index) -> float:
    s = pd.Series(np.asarray(x, dtype=float), index=index)
    return float(s[s.index >= T0].mean())


def main():
    m = {c: lab.load_candles(product=f"{c}-USD", since=None) for c in ("BTC", "ETH")}
    b6 = {c: S30.bars_6h(m[c]) for c in m}
    d = {c: lab.resample(m[c], "1D") for c in m}
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), "2016-01-01", None)))
    e4 = {c: S30.split_held(b6[c]) for c in m}
    qa = {c: S34.interference_held(b6[c]) for c in m}
    p1 = {c: S5.m1_positions(d[c], 50, 2.0) for c in m}
    qb = {c: S34.tunnel_positions(d[c]) for c in m}
    k_qa = float(np.mean([mean_from(qa[c], b6[c].index) / mean_from(e4[c], b6[c].index) for c in m]))
    k_qb = float(np.mean([mean_from(qb[c], d[c].index) / mean_from(p1[c], d[c].index) for c in m]))
    grid = {"E4_split": e4, "QA_interference": qa, "QA_line": {c: e4[c] * k_qa for c in m}}
    day = {"P1_2x": p1, "QB_tunnelling": qb, "QB_line": {c: p1[c] * k_qb for c in m}}
    res = {"n_trials": N_TRIALS, "candles_end": str(d["BTC"].index[-1].date()), "variants": {}, "extra": {
        "QA": {"size_vs_E4": k_qa, "avg_leverage": {c: mean_from(qa[c], b6[c].index) for c in m},
               "share_of_bars_differing_from_E4": {c: mean_from(np.abs(qa[c] - e4[c]) > 1e-12, b6[c].index) for c in m},
               "turnover_per_day": {n: float(np.mean([mean_from(grid[n][c].diff().abs(), b6[c].index) * 4 for c in m]))
                                    for n in grid}},
        "QB": {"size_vs_P1": k_qb, "avg_leverage": {c: mean_from(qb[c], d[c].index) for c in m},
               "avg_leverage_trend_on": {c: float(qb[c][(p1[c] > 0) & (d[c].index >= T0)].mean()) for c in m},
               "turnover_per_day": {n: float(np.mean([mean_from(np.abs(np.diff(day[n][c], prepend=0.0)), d[c].index)
                                                      for c in m])) for n in day}}}}
    sharpe = {}
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        rets = {n: lab.daily_from_bars(S5.two_sleeve(S30.grid_returns(b6["BTC"], h["BTC"], costs),
                                                     S30.grid_returns(b6["ETH"], h["ETH"], costs)))
                for n, h in grid.items()}
        rets.update({n: S5.two_sleeve(lab.position_returns(d["BTC"], p["BTC"], costs),
                                      lab.position_returns(d["ETH"], p["ETH"], costs)) for n, p in day.items()})
        for n, r in rets.items():
            res["variants"].setdefault(n, {})[costs.name] = stats(r, reg)
            if costs is lab.US_FUTURES:
                x = cut(r, "2016-01-01", None)
                sharpe[n] = float(x.mean() / x.std() * np.sqrt(365))
    res["extra"]["sharpe"] = sharpe
    v, b, s = res["variants"], "us-futures", "us-futures-stress"
    res["checks"], res["pass"] = {}, {}
    for name, line, base in (("QA_interference", "QA_line", "E4_split"), ("QB_tunnelling", "QB_line", "P1_2x")):
        x, ln = v[name][b], v[line][b]
        ch = {"return_ge_line": x["full_2016_26"]["geo_monthly_pct"] >= ln["full_2016_26"]["geo_monthly_pct"],
              "max_dd_no_worse_than_line": x["full_2016_26"]["max_dd_pct"] >= ln["full_2016_26"]["max_dd_pct"],
              "sharpe_gt_base": sharpe[name] > sharpe[base],
              "bear_no_worse_than_line": x["by_regime_mean_monthly_pct"]["bear"] >= ln["by_regime_mean_monthly_pct"]["bear"],
              "A_positive": x["A_2025_26"]["geo_monthly_pct"] > 0,
              "stress_full_positive": v[name][s]["full_2016_26"]["geo_monthly_pct"] > 0}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))
    for name, z in v.items():
        y = z[b]
        full = y["full_2016_26"]
        print(f"{name:16s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.1f}% sharpe {sharpe[name]:.3f} | "
              + " ".join(f"{q.split('_')[0]} {y[q]['geo_monthly_pct']:+.2f}" for q in CORE_PERIODS)
              + f" | 2022 {y['bear_2022']['geo_monthly_pct']:+.2f} | "
              + " ".join(f"{k} {q:+.1f}" for k, q in y["by_regime_mean_monthly_pct"].items())
              + f" | stress {z[s]['full_2016_26']['geo_monthly_pct']:+.2f}", flush=True)
    print(json.dumps(res["extra"], indent=1))
    for name in res["checks"]:
        print(name, res["checks"][name], "PASS" if res["pass"][name] else "FAIL")
    with open(os.path.join(HERE, "results", "round34.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
