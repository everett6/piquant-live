"""Run PREREG33 once: PREREG19's funding carry on P1's idle sleeves (I1 immediate, I2 after 5 flat closes), against
P1 and P1 with T-bills on idle sleeves, 2020-01-01 to 2026-10-31. Writes results/round33.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies33 as S33
from run_round7 import cut
from run_round19 import END, START, load_funding

HERE = os.path.dirname(os.path.abspath(__file__))
DTB3 = os.path.expanduser("~/piquant-data/macro/DTB3.csv")
N_TRIALS = 760
W0, W1, A0 = "2020-01-01", "2026-11-01", "2025-07-01"
WAITS = {"I1_immediate": 1, "I2_patient": S33.WAIT_D}
COSTS = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}


def geo(m: pd.Series) -> float:
    return float((np.expm1(np.log1p(m).mean())) * 100) if len(m) else float("nan")


def stats(r: pd.Series, reg: pd.Series) -> dict:
    m = S5.complete_months(r)
    by = m.reindex(reg.index)
    s = lab.summarize(r)
    return {"geo_monthly_pct": geo(m), "max_dd_pct": s["max_dd_pct"], "sharpe": s["sharpe"],
            "A_geo_monthly_pct": geo(S5.complete_months(cut(r, A0, W1))),
            "by_regime": {k: float(by[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}}


def total(r: pd.Series) -> float:
    return float((1 + r).prod() - 1)


def main():
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    p1_full = {c: pd.Series(S5.m1_positions(full[c], 50, 2.0), index=full[c].index) for c in full}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in full}
    p1 = {c: p1_full[c].reindex(d[c].index).to_numpy() for c in d}
    f = {c: load_funding(f"{c}USDT") for c in d}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), W0, W1)))
    res = {"n_trials": N_TRIALS, "window": [W0, W1], "regime_months": reg.value_counts().to_dict(),
           "idle_share": {c: float((p1[c] == 0).mean()) for c in d}, "variants": {}, "carry_leg": {}, "funding": {}}
    for c in d:
        pos, neg, _ = S19.daily_funding(f[c], d[c].index)
        day = (pos + neg).to_numpy()
        held_flat = np.concatenate([[False], p1[c][:-1] == 0])
        res["funding"][c] = {"mean_ann_pct_idle_days": float(day[held_flat].mean() * 365 * 100),
                             "mean_ann_pct_trend_on_days": float(day[~held_flat].mean() * 365 * 100)}
    for cname, (fut, car) in COSTS.items():
        sleeves = {c: cut(lab.position_returns(full[c], p1_full[c].to_numpy(), fut), W0, W1) for c in d}
        p1r = S5.two_sleeve(sleeves["BTC"], sleeves["ETH"])
        res["variants"].setdefault("P1_2x", {})[cname] = stats(p1r, reg)
        bills = 0.5 * sum(S33.bills_on_idle(p1[c], rate, d[c].index).reindex(p1r.index).fillna(0.0) for c in d)
        res["variants"].setdefault("P1_plus_bills", {})[cname] = stats(p1r + bills, reg)
        for name, wait in WAITS.items():
            books = {c: S33.carry_when(d[c], f[c], S33.want_idle(p1[c], wait), car) for c in d}
            leg = 0.5 * sum(books[c]["r"].reindex(p1r.index).fillna(0.0) for c in d)
            res["variants"].setdefault(name, {})[cname] = stats(p1r + leg, reg)
            yrs = {str(y): total(leg[leg.index.year == y]) * 100 for y in sorted(set(leg.index.year))}
            res["carry_leg"].setdefault(name, {})[cname] = {
                "total_pct": total(leg) * 100, "ann_pct": float(((1 + total(leg)) ** (365 / len(leg)) - 1) * 100),
                "A_total_pct": total(cut(leg, A0, W1)) * 100, "max_dd_pct": lab.summarize(leg)["max_dd_pct"],
                "by_year_pct": yrs,
                "per_coin": {c: {"time_in_market": float(b["held"].mean()), "switches": int((b["switch_cost"] > 0).sum()),
                                 "switch_cost_pct": float(b["switch_cost"].sum() * 100),
                                 "funding_pct": float(b["funding"].sum() * 100), "liquidations": int(b["liq"].sum())}
                             for c, b in books.items()}}
    v, leg = res["variants"], res["carry_leg"]
    res["checks"], res["pass"] = {}, {}
    for name in WAITS:
        x, p = v[name]["base"], v["P1_2x"]["base"]
        ch = {"return_ge_P1_plus_bills": x["geo_monthly_pct"] >= v["P1_plus_bills"]["base"]["geo_monthly_pct"],
              "max_dd_no_worse_than_P1": x["max_dd_pct"] >= p["max_dd_pct"],
              "bear_no_worse_than_P1": x["by_regime"]["bear"] >= p["by_regime"]["bear"],
              "carry_leg_A_positive": leg[name]["base"]["A_total_pct"] > 0,
              "carry_leg_stress_positive": leg[name]["stress"]["total_pct"] > 0}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))
    for name, z in v.items():
        y = z["base"]
        print(f"{name:14s} {y['geo_monthly_pct']:+.3f}%/mo maxDD {y['max_dd_pct']:.1f}% sharpe {y['sharpe']:.2f} "
              f"A {y['A_geo_monthly_pct']:+.2f} | " + " ".join(f"{k} {q:+.2f}" for k, q in y["by_regime"].items())
              + f" | stress {z['stress']['geo_monthly_pct']:+.3f}", flush=True)
    print(json.dumps({k: res[k] for k in ("idle_share", "funding", "carry_leg", "regime_months")}, indent=1))
    for name in WAITS:
        print(name, res["checks"][name], "PASS" if res["pass"][name] else "FAIL")
    with open(os.path.join(HERE, "results", "round33.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
