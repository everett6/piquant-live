"""Run PREREG35 once: QC entanglement, QD coherence-time and QE entropy-gate sizing on P1, each against P1 and its
constant-size line on its window. Writes results/round35.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies31 as S31
import strategies34 as S34
import strategies35 as S35
from run_round7 import cut
from run_round25 import load_alts

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 765
A0 = "2025-07-01"


def geo(m: pd.Series) -> float:
    return float((np.expm1(np.log1p(m).mean())) * 100) if len(m) else float("nan")


def stats(r: pd.Series, bh: pd.Series, w0: str) -> dict:
    r = cut(r, w0, None)
    m = S5.complete_months(r)
    reg = S5.regime_months(S5.complete_months(cut(bh, w0, None)))
    by = m.reindex(reg.index)
    out = {"geo_monthly_pct": geo(m), "months": int(len(m)), "max_dd_pct": lab.summarize(r)["max_dd_pct"],
           "sharpe": float(r.mean() / r.std() * np.sqrt(365)), "A_geo_monthly_pct": geo(S5.complete_months(cut(r, A0, None))),
           "y2022_geo_monthly_pct": geo(S5.complete_months(cut(r, "2022-01-01", "2023-01-01"))),
           "by_regime": {k: float(by[reg == k].mean() * 100) for k in ("bear", "flat", "bull")}}
    for name, (a, b) in {"D_2016_17": ("2016-01-01", "2018-01-01"), "C_2018_20": ("2018-01-01", "2021-01-01"),
                         "B_2021_22": ("2021-01-01", "2022-12-17"), "discovery": ("2022-12-17", "2025-07-01")}.items():
        if pd.Timestamp(a, tz="UTC") >= pd.Timestamp(w0, tz="UTC"):
            out[name] = geo(S5.complete_months(cut(r, a, b)))
    return out


def book(d, pos, costs):
    return S5.two_sleeve(lab.position_returns(d["BTC"], pos["BTC"], costs),
                         lab.position_returns(d["ETH"], pos["ETH"], costs))


def main():
    d = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    bh = d["BTC"]["c"].pct_change().fillna(0)
    p1 = {c: S5.m1_positions(d[c], 50, 2.0) for c in d}
    closes = load_alts(d["BTC"].index)
    closes.insert(0, "ETH", d["ETH"]["c"].reindex(d["BTC"].index))
    closes.insert(0, "BTC", d["BTC"]["c"])
    ent = S35.market_entropy(closes)
    defined = S35.gate_defined(ent)
    first = defined[defined].index[0]
    w_qe = str((first + pd.offsets.MonthBegin(0 if first.day == 1 else 1)).date())
    gate = pd.Series(S35.gate_low(ent), index=ent.index)
    var = {"QC_entanglement": ({"BTC": S35.entangled_positions(d["BTC"], d["ETH"]),
                                "ETH": S35.entangled_positions(d["ETH"], d["BTC"])}, "2016-01-01"),
           "QD_coherence": ({c: S35.coherence_positions(d[c]) for c in d}, "2016-01-01"),
           "QE_entropy_gate": ({c: S31.gated_positions(d[c], gate.reindex(d[c].index).fillna(False).to_numpy())
                                for c in d}, w_qe),
           "QB_tunnelling_ref": ({c: S34.tunnel_positions(d[c]) for c in d}, "2016-01-01")}
    res = {"n_trials": N_TRIALS, "candles_end": str(d["BTC"].index[-1].date()), "qe_window_start": w_qe,
           "variants": {}, "extra": {}, "checks": {}, "pass": {}}
    for name, (pos, w0) in var.items():
        t0 = pd.Timestamp(w0, tz="UTC")
        inw = {c: np.asarray(d[c].index >= t0) for c in d}
        k = float(np.mean([pos[c][inw[c]].mean() / p1[c][inw[c]].mean() for c in d]))
        line = {c: p1[c] * k for c in d}
        out = {"size_vs_P1": k, "window_start": w0,
               "turnover_per_day": {n: float(np.mean([np.abs(np.diff(p[c], prepend=0.0))[inw[c]].mean() for c in d]))
                                    for n, p in (("variant", pos), ("P1", p1), ("line", line))}}
        for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
            for n, p in (("variant", pos), ("line", line), ("P1", p1)):
                out.setdefault(n, {})[costs.name] = stats(book(d, p, costs), bh, w0)
        res["variants"][name] = out
        x, ln, b = out["variant"]["us-futures"], out["line"]["us-futures"], out["P1"]["us-futures"]
        ch = {"return_ge_line_plus_0p10": x["geo_monthly_pct"] >= ln["geo_monthly_pct"] + 0.10,
              "max_dd_1pt_better_than_line": x["max_dd_pct"] >= ln["max_dd_pct"] + 1.0,
              "sharpe_ge_P1_plus_0p02": x["sharpe"] >= b["sharpe"] + 0.02,
              "bear_no_worse_than_line": x["by_regime"]["bear"] >= ln["by_regime"]["bear"],
              "A_positive": x["A_geo_monthly_pct"] > 0,
              "stress_ge_line_stress": out["variant"]["us-futures-stress"]["geo_monthly_pct"]
              >= out["line"]["us-futures-stress"]["geo_monthly_pct"]}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))
        for n in ("variant", "line", "P1"):
            y = out[n]["us-futures"]
            print(f"{name:18s} {n:8s} {y['geo_monthly_pct']:+.2f}%/mo maxDD {y['max_dd_pct']:.1f}% sharpe {y['sharpe']:.3f} | "
                  + " ".join(f"{q.split('_')[0]} {y[q]:+.2f}" for q in ("D_2016_17", "C_2018_20", "B_2021_22", "discovery") if q in y)
                  + f" A {y['A_geo_monthly_pct']:+.2f} | 2022 {y['y2022_geo_monthly_pct']:+.2f} | "
                  + " ".join(f"{k2} {q:+.1f}" for k2, q in y["by_regime"].items())
                  + f" | stress {out[n]['us-futures-stress']['geo_monthly_pct']:+.2f}", flush=True)
        print(f"   size vs P1 {k:.3f} turnover {out['turnover_per_day']}", flush=True)
    t16 = {c: np.asarray(d[c].index >= pd.Timestamp("2016-01-01", tz="UTC")) for c in d}
    ex = res["extra"]
    ex["QC_share_of_trend_on_days_alone"] = {c: float((var["QC_entanglement"][0][c][t16[c] & (p1[c] > 0)] < 2.0).mean())
                                             for c in d}
    ages = {}
    for c in d:
        age, nxt = S35.trend_age(d[c]), d[c]["c"].pct_change().shift(-1).to_numpy()
        ages[c] = {lab_: {"days": int(((age >= a) & (age <= b) & t16[c]).sum()),
                          "next_day_pct": float(np.nanmean(nxt[(age >= a) & (age <= b) & t16[c]]) * 100)}
                   for lab_, (a, b) in {"1-5": (1, 5), "6-10": (6, 10), "11-20": (11, 20), "21+": (21, 10**6)}.items()}
    ex["QD_next_day_by_trend_age"] = ages
    g = gate[gate.index >= pd.Timestamp(w_qe, tz="UTC")]
    ex["QE"] = {"share_of_days_gated": float(g.mean()), "by_year": {str(y): float(v) for y, v in g.groupby(g.index.year).mean().items()},
                "first_signal": str(ent.first_valid_index().date()),
                "coins_used_last": int(closes.pct_change().iloc[-60:].notna().all().sum()),
                "entropy_mean": float(ent[ent.index >= pd.Timestamp(w_qe, tz="UTC")].mean())}
    print(json.dumps(ex, indent=1))
    for name in ("QC_entanglement", "QD_coherence", "QE_entropy_gate"):
        print(name, res["checks"][name], "PASS" if res["pass"][name] else "FAIL")
    with open(os.path.join(HERE, "results", "round35.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
