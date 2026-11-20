"""Run PREREG32 once: the stablecoin supply gate on P1 (H1) against P1, 1x, 1.5x and a constant leverage of the same
average size. Writes results/round32.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies31 as S31
import strategies32 as S32
from run_round7 import cut

HERE = os.path.dirname(os.path.abspath(__file__))
SUPPLY = os.path.expanduser("~/piquant-data/stablecoins/total.csv")
N_TRIALS = 758
W0_REG, W1, A0 = "2019-01-01", "2026-11-01", "2025-07-01"
NAME = "H1_supply_gate"


def geo(m: pd.Series) -> float:
    return float((np.expm1(np.log1p(m).mean())) * 100) if len(m) else float("nan")


def stats(r: pd.Series, reg: pd.Series, w0: str) -> dict:
    r = cut(r, w0, W1)
    m = S5.complete_months(r)
    by = m.reindex(reg.index)
    s = lab.summarize(r)
    return {"geo_monthly_pct": geo(m), "months": int(len(m)), "max_dd_pct": s["max_dd_pct"], "sharpe": s["sharpe"],
            "worst_day_pct": float(r.min() * 100), "A_geo_monthly_pct": geo(S5.complete_months(cut(r, A0, W1))),
            "y2022_geo_monthly_pct": geo(S5.complete_months(cut(r, "2022-01-01", "2023-01-01"))),
            "by_regime": {k: float(by[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}}


def main():
    d = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    raw = pd.read_csv(SUPPLY)
    supply = pd.Series(raw["usd"].to_numpy(dtype=float), index=pd.to_datetime(raw["date"], utc=True))
    growth = {c: S32.supply_growth(supply, d[c].index) for c in d}
    first = growth["BTC"].first_valid_index()
    w0 = W0_REG
    if first > pd.Timestamp(W0_REG, tz="UTC"):  # the one permitted change: start at the first full month with a signal
        w0 = str((first + pd.offsets.MonthBegin(0 if first.day == 1 else 1)).date())
    g = {c: S32.gate_shrinking(growth[c]) for c in d}
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), w0, W1)))
    win = {c: np.asarray((d[c].index >= pd.Timestamp(w0, tz="UTC")) & (d[c].index < pd.Timestamp(W1, tz="UTC")))
           for c in d}
    p1 = {c: S5.m1_positions(d[c], 50, 2.0) for c in d}
    gp = {c: S31.gated_positions(d[c], g[c]) for c in d}
    on = {c: win[c] & (p1[c] > 0) for c in d}
    lbar = float(np.mean([gp[c][on[c]].mean() for c in d]))
    pos = {"P1_2x": p1, "R1_1x": {c: p1[c] / 2 for c in d}, "R2_1p5x": {c: p1[c] * 0.75 for c in d},
           NAME: gp, NAME + "_line": {c: p1[c] / 2 * lbar for c in d}}
    gs = pd.Series(g["BTC"], index=d["BTC"].index)[win["BTC"]]
    sw = gs[gs != gs.shift(1)].iloc[1:]
    res = {"n_trials": N_TRIALS, "window": [w0, W1], "supply_first_signal": str(first.date()),
           "supply_rows": int(len(supply)), "supply_missing_days_in_window": int(growth["BTC"][win["BTC"]].isna().sum()),
           "regime_months": reg.value_counts().to_dict(), "variants": {},
           "gate": {"avg_leverage_trend_on": lbar, "share_of_all_days_gated": float(gs.mean()),
                    "switches": [[str(t.date()), "on" if v else "off"] for t, v in sw.items()]}}
    for c in d:
        ret = d[c]["c"].pct_change()
        nxt, nxt7 = ret.shift(-1), d[c]["c"].shift(-7) / d[c]["c"] - 1
        gd, ug = on[c] & g[c], on[c] & ~g[c]
        res["gate"][c] = {"share_of_trend_on_days_gated": float(g[c][on[c]].mean()), "gated_days": int(gd.sum()),
                          "next_day_pct": {"gated": float(nxt[gd].mean() * 100), "ungated": float(nxt[ug].mean() * 100)},
                          "next_7d_pct": {"gated": float(nxt7[gd].mean() * 100), "ungated": float(nxt7[ug].mean() * 100)}}
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        for name, p in pos.items():
            r = S5.two_sleeve(lab.position_returns(d["BTC"], p["BTC"], costs),
                              lab.position_returns(d["ETH"], p["ETH"], costs))
            res["variants"].setdefault(name, {})[costs.name] = stats(r, reg, w0)
    v, b, s = res["variants"], "us-futures", "us-futures-stress"
    x, line = v[NAME][b], v[NAME + "_line"][b]
    ch = {"bear_ge_P1_plus_3": x["by_regime"]["bear"] >= v["P1_2x"][b]["by_regime"]["bear"] + 3.0,
          "return_ge_line": x["geo_monthly_pct"] >= line["geo_monthly_pct"],
          "max_dd_no_worse_than_line": x["max_dd_pct"] >= line["max_dd_pct"],
          "A_positive": x["A_geo_monthly_pct"] > 0,
          "stress_ge_P1_stress": v[NAME][s]["geo_monthly_pct"] >= v["P1_2x"][s]["geo_monthly_pct"]}
    res["checks"], res["pass"] = ch, bool(all(ch.values()))
    for name, z in v.items():
        y = z[b]
        print(f"{name:22s} {y['geo_monthly_pct']:+.2f}%/mo maxDD {y['max_dd_pct']:.1f}% sharpe {y['sharpe']:.2f} "
              f"worst day {y['worst_day_pct']:.1f}% | A {y['A_geo_monthly_pct']:+.2f} 2022 {y['y2022_geo_monthly_pct']:+.2f} | "
              + " ".join(f"{k} {q:+.1f}" for k, q in y["by_regime"].items())
              + f" | stress {z[s]['geo_monthly_pct']:+.2f}", flush=True)
    print(json.dumps({k: q for k, q in res.items() if k not in ("variants",)}, indent=1, default=str))
    print("PASS" if res["pass"] else "FAIL")
    with open(os.path.join(HERE, "results", "round32.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
