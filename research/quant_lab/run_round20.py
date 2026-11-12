"""Run PREREG20 once: beta-neutral BTC-ETH book, N1 always on and N2 in BTC volatility spikes only.
Writes results/round20.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies20 as S
from run_round7 import PERIODS, cut

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 736
START = pd.Timestamp("2016-06-01", tz="UTC")
CORE = ("D_2016_17", "C_2018_20", "B_2021_22", "discovery", "A_2025_26")


def main():
    d = {p: lab.resample(lab.load_candles(product=p, since=None), "1D")["c"] for p in ("BTC-USD", "ETH-USD")}
    idx = d["BTC-USD"].index.intersection(d["ETH-USD"].index)
    idx = idx[idx >= START][:-1]  # drop the current, incomplete day
    rb = d["BTC-USD"].reindex(idx).pct_change()
    re = d["ETH-USD"].reindex(idx).pct_change()
    beta = S.hedge_ratio(rb, re)
    live = beta.shift(1).notna()  # days on which a book could be held
    res = {"n_trials": N_TRIALS, "window": [str(idx[0].date()), str(idx[-1].date())],
           "beta": {"median": float(beta.median()), "p10": float(beta.quantile(0.1)), "p90": float(beta.quantile(0.9))},
           "tails": {"long_btc": S.tail(rb[live]), "long_eth": S.tail(re[live]),
                     "long_equal_weight": S.tail((0.5 * rb + 0.5 * re)[live])}, "rules": {}}
    rules = {"N1": pd.Series(True, index=idx), "N2": S.spike_flag(rb)}
    for name, on in rules.items():
        out = {}
        for label, f in (("net", 0.1), ("stress", 1.0)):
            bk = S.book_returns(rb, re, beta, on, f)
            r = bk["r"][live]
            st = S.stats(r)
            st["periods"] = {}
            for k in CORE:
                x = cut(r, *PERIODS[k])
                st["periods"][k] = float((1 + x).prod() - 1) if len(x) else None
            st["share_held"] = float(bk["held"][live].mean())
            st["tail_when_held"] = S.tail(r[bk["held"][live]]) if bk["held"][live].sum() > 10 else None
            st["btc_tail_same_days"] = S.tail(rb[live][bk["held"][live]]) if bk["held"][live].sum() > 10 else None
            out[label] = st
        pos = sum(1 for v in out["net"]["periods"].values() if v is not None and v > 0)
        t = out["net"]["tail_when_held"]
        b = out["net"]["btc_tail_same_days"]
        out["pass"] = bool(t and b and S.passes(out["net"], out["stress"], pos, t["q99.9"], b["q99.9"]))
        res["rules"][name] = out
        n = out["net"]
        print(f"{name} {'PASS' if out['pass'] else 'FAIL'} | sharpe {n['sharpe']:+.2f} (stress {out['stress']['sharpe']:+.2f})"
              f" | {n['ann_return']:+.1%}/yr vol {n['ann_vol']:.1%} dd {n['max_dd']:.1%} | held {n['share_held']:.0%} | "
              f"periods>0 {pos}/5 {dict((k, round(v, 3) if v is not None else None) for k, v in n['periods'].items())}"
              f" | book q99.9 {t['q99.9']:.2%} worst {t['worst']:.2%} vs BTC {b['q99.9']:.2%} worst {b['worst']:.2%}")
    for k, v in res["tails"].items():
        print(k, {a: (round(x, 4) if isinstance(x, float) else x) for a, x in v.items()})
    print("beta", res["beta"])
    with open(os.path.join(HERE, "results", "round20.json"), "w") as f:
        json.dump(res, f, indent=2)


if __name__ == "__main__":
    main()
