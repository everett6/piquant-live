"""Run PREREG25 once: B1 (BTC-downtrend-gated) and B2 (always-on) long-BTC / short-alt-basket overlays on the
BTC+ETH 2x core vs P1. Reads ~/piquant-data/alts (fetch_round25.py). Writes results/round25.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies25 as S25
from run_round7 import cut, geo
from run_round24 import CORE_PERIODS, stats

HERE = os.path.dirname(os.path.abspath(__file__))
ALTS = os.path.expanduser("~/piquant-data/alts")
N_TRIALS = 746


def load_alts(index):
    cols = {}
    for c in S25.COINS:
        s = pd.read_csv(os.path.join(ALTS, f"{c}.csv"), parse_dates=["date"], index_col="date")["close"]
        s.index = s.index.tz_localize("UTC")
        cols[c] = s
    return pd.DataFrame(cols).reindex(index)


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    alts = load_alts(btc.index)
    bh_m = S5.complete_months(cut(btc["c"].pct_change().fillna(0), "2016-01-01", None))
    reg = S5.regime_months(bh_m)
    pos_b = S5.m1_positions(btc, 50, 2.0)
    pos_e = S5.m1_positions(eth, 50, 2.0)
    gates = {"B1_pair_in_btc_downtrend": S25.btc_downtrend(btc["c"]),
             "B2_pair_always_on": pd.Series(True, index=btc.index)}
    w = S25.basket_weights(alts)
    nb = (w > 0).sum(axis=1)
    res = {"n_trials": N_TRIALS, "data": json.load(open(os.path.join(ALTS, "SOURCES.json"))),
           "first_close": {c: str(alts[c].first_valid_index()) for c in S25.COINS},
           "basket_size_by_year": {str(y): float(v) for y, v in nb.groupby(nb.index.year).mean().items()},
           "variants": {}, "pair_alone": {}}
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        p1 = S5.two_sleeve(lab.position_returns(btc, pos_b, costs), lab.position_returns(eth, pos_e, costs))
        res["variants"].setdefault("P1_btc_eth_2x", {})[costs.name] = stats(p1, reg)
        for name, g in gates.items():
            pr, contrib, pos = S25.pair_returns(btc["c"], alts, g, costs.taker_bps, costs.slip_bps, costs.funding_bps_h)
            res["variants"].setdefault(name, {})[costs.name] = stats(p1 + pr, reg)
            if costs is lab.US_FUTURES:
                p1m = S5.complete_months(cut(p1, "2016-01-01", None))
                pm = S5.complete_months(cut(pr, "2016-01-01", None))
                pmr = pm.reindex(reg.index)
                pr16 = cut(pr, "2016-01-01", None)
                res["pair_alone"][name] = {
                    "full_geo_monthly_pct": geo(pm), "sharpe": lab.summarize(pr16)["sharpe"],
                    "max_dd_pct": lab.summarize(pr16)["max_dd_pct"],
                    "by_regime_mean_monthly_pct": {k: float(pmr[reg == k].mean() * 100) for k in ("bear", "flat", "bull")},
                    "worst_day_pct": float(pr16.min() * 100), "worst_day": str(pr16.idxmin()),
                    "best_day_pct": float(pr16.max() * 100),
                    "days_on": int((pos["BTC"].shift(1).fillna(0) > 0).sum()),
                    "corr_with_P1_monthly": float(np.corrcoef(pm.reindex(p1m.index).fillna(0), p1m)[0, 1]),
                    "mean_daily_bps_by_asset": {k: float(v * 1e4) for k, v in cut(contrib, "2016-01-01", None)
                                                .mean().items()}}
    p1f = res["variants"]["P1_btc_eth_2x"]["us-futures"]
    for name in gates:
        v = res["variants"][name]
        f = v["us-futures"]
        checks = {
            "bear_months_ge_P1_plus_3": f["by_regime_mean_monthly_pct"]["bear"]
            >= p1f["by_regime_mean_monthly_pct"]["bear"] + 3.0,
            "full_geo_ge_7": f["full_2016_26"]["geo_monthly_pct"] >= 7.0,
            "max_dd_no_worse_than_P1": f["full_2016_26"]["max_dd_pct"] >= p1f["full_2016_26"]["max_dd_pct"],
            "A_positive": f["A_2025_26"]["geo_monthly_pct"] > 0,
            "stress_full_positive": v["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
        v["checks"], v["pass"] = checks, bool(all(checks.values()))
    passed = [n for n in gates if res["variants"][n]["pass"]]
    res["chosen"] = max(passed, key=lambda n: res["variants"][n]["us-futures"]["full_2016_26"]["geo_monthly_pct"]) \
        if passed else None
    print("chosen:", res["chosen"])
    for name, v in res["variants"].items():
        f = v["us-futures"]
        full = f["full_2016_26"]
        print(f"{name:26s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.0f}% "
              f"worst {full['worst_month_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {f[p]['geo_monthly_pct']:+.2f}" for p in CORE_PERIODS)
              + f" | 2022 {f['bear_2022']['geo_monthly_pct']:+.2f} | bear {f['by_regime_mean_monthly_pct']['bear']:+.1f} "
              f"flat {f['by_regime_mean_monthly_pct']['flat']:+.1f} bull {f['by_regime_mean_monthly_pct']['bull']:+.1f}"
              f" | stress {v['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}"
              f" | {v.get('checks', 'comparator')} {'PASS' if v.get('pass') else ''}", flush=True)
    for name, o in res["pair_alone"].items():
        print(name, "pair alone:", json.dumps(o))
    print("basket size by year:", res["basket_size_by_year"])
    with open(os.path.join(HERE, "results", "round25.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
