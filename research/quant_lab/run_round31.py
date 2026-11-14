"""Run PREREG31 once: the funding crowding gate on P1 (G1 fixed level, G2 relative level) on 2021-01-01 to
2026-10-31, against P1, 1x, 1.5x and a constant leverage of the same average size. Writes results/round31.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies31 as S31
from run_round7 import cut
from run_round19 import load_funding

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 757
W0, W1, A0 = "2021-01-01", "2026-11-01", "2025-07-01"


def geo(m: pd.Series) -> float:
    return float((np.expm1(np.log1p(m).mean())) * 100) if len(m) else float("nan")


def stats(r: pd.Series, reg: pd.Series) -> dict:
    r = cut(r, W0, W1)
    m = S5.complete_months(r)
    by = m.reindex(reg.index)
    return {"geo_monthly_pct": geo(m), "months": int(len(m)), "max_dd_pct": lab.summarize(r)["max_dd_pct"],
            "sharpe": lab.summarize(r)["sharpe"], "worst_day_pct": float(r.min() * 100),
            "A_geo_monthly_pct": geo(S5.complete_months(cut(r, A0, W1))),
            "y2022_geo_monthly_pct": geo(S5.complete_months(cut(r, "2022-01-01", "2023-01-01"))),
            "by_regime": {k: float(by[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}}


def main():
    d = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    sig = {c: S31.crowd_signal(load_funding(f"{c}USDT"), d[c].index) for c in d}
    gates = {"G1_fixed_level": {c: S31.gate_abs(sig[c]) for c in d},
             "G2_relative_level": {c: S31.gate_rel(sig[c]) for c in d}}
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), W0, W1)))
    win = {c: np.asarray((d[c].index >= pd.Timestamp(W0, tz="UTC")) & (d[c].index < pd.Timestamp(W1, tz="UTC")))
           for c in d}
    p1 = {c: S5.m1_positions(d[c], 50, 2.0) for c in d}
    pos = {"P1_2x": p1, "R1_1x": {c: p1[c] / 2 for c in d}, "R2_1p5x": {c: p1[c] * 0.75 for c in d}}
    res = {"n_trials": N_TRIALS, "window": [W0, W1], "regime_months": reg.value_counts().to_dict(),
           "variants": {}, "gate": {}}
    for name, g in gates.items():
        gp = {c: S31.gated_positions(d[c], g[c]) for c in d}
        on = {c: win[c] & (p1[c] > 0) for c in d}
        lbar = float(np.mean([gp[c][on[c]].mean() for c in d]))
        pos[name], pos[name + "_line"] = gp, {c: p1[c] / 2 * lbar for c in d}
        info = {"avg_leverage_trend_on": lbar}
        for c in d:
            ret = d[c]["c"].pct_change()
            nxt, nxt7 = ret.shift(-1), d[c]["c"].shift(-7) / d[c]["c"] - 1
            gd, ug = on[c] & g[c], on[c] & ~g[c]
            info[c] = {"share_of_trend_on_days_gated": float(g[c][on[c]].mean()), "gated_days": int(gd.sum()),
                       "next_day_pct": {"gated": float(nxt[gd].mean() * 100), "ungated": float(nxt[ug].mean() * 100)},
                       "next_7d_pct": {"gated": float(nxt7[gd].mean() * 100), "ungated": float(nxt7[ug].mean() * 100)}}
        res["gate"][name] = info
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        for name, p in pos.items():
            r = S5.two_sleeve(lab.position_returns(d["BTC"], p["BTC"], costs),
                              lab.position_returns(d["ETH"], p["ETH"], costs))
            res["variants"].setdefault(name, {})[costs.name] = stats(r, reg)
    v, res["checks"], res["pass"] = res["variants"], {}, {}
    b, s = "us-futures", "us-futures-stress"
    for name in gates:
        x, line = v[name][b], v[name + "_line"][b]
        ch = {"bear_ge_P1_plus_3": x["by_regime"]["bear"] >= v["P1_2x"][b]["by_regime"]["bear"] + 3.0,
              "return_ge_line": x["geo_monthly_pct"] >= line["geo_monthly_pct"],
              "max_dd_no_worse_than_line": x["max_dd_pct"] >= line["max_dd_pct"],
              "A_positive": x["A_geo_monthly_pct"] > 0,
              "stress_ge_P1_stress": v[name][s]["geo_monthly_pct"] >= v["P1_2x"][s]["geo_monthly_pct"]}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))
    for name, x in v.items():
        y = x[b]
        print(f"{name:24s} {y['geo_monthly_pct']:+.2f}%/mo maxDD {y['max_dd_pct']:.1f}% sharpe {y['sharpe']:.2f} "
              f"worst day {y['worst_day_pct']:.1f}% | A {y['A_geo_monthly_pct']:+.2f} 2022 {y['y2022_geo_monthly_pct']:+.2f} | "
              + " ".join(f"{k} {z:+.1f}" for k, z in y["by_regime"].items())
              + f" | stress {x[s]['geo_monthly_pct']:+.2f}", flush=True)
    print(json.dumps(res["gate"], indent=1))
    print("regime months", res["regime_months"])
    for name in gates:
        print(name, res["checks"][name], "PASS" if res["pass"][name] else "FAIL")
    with open(os.path.join(HERE, "results", "round31.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
