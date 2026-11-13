"""Run PREREG28 once: a daily ladder of sold 30-day iron butterflies (f = 0.25) beside the BTC+ETH 2x core, on
2021-04-01 onward (Deribit DVOL, ~/piquant-data/dvol). Writes results/round28.json.
DO NOT RUN before PREREG28.md is approved, committed and pushed."""
from __future__ import annotations

import json
import os

import numpy as np

import lab
import strategies5 as S5
import strategies28 as S28
from run_round7 import cut
from run_round26 import W0, load_iv, stats

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 750
NAME = "H1_iron_fly_ladder"


def main():
    btc = lab.resample(lab.load_candles(product="BTC-USD", since=None), "1D")
    eth = lab.resample(lab.load_candles(product="ETH-USD", since=None), "1D")
    coins = {"BTC": btc, "ETH": eth}
    iv = {c: load_iv(c) for c in coins}
    reg = S5.regime_months(S5.complete_months(cut(btc["c"].pct_change().fillna(0), W0, None)))
    res = {"n_trials": N_TRIALS, "window_start": W0, "candles_end": str(btc.index[-1].date()), "f": S28.RISK_F,
           "variants": {}, "sleeve": {}}
    keep = {}
    for fc, oc in ((lab.US_FUTURES, S28.BASE), (lab.US_FUTURES_STRESS, S28.STRESS)):
        lin = {lev: S5.two_sleeve(*(lab.position_returns(d, S5.m1_positions(d, 50, lev), fc) for d in coins.values()))
               for lev in (1.0, 1.5, 2.0)}
        lad = {c: S28.iron_fly_ladder(d["c"], iv[c], S28.RISK_F, oc) for c, d in coins.items()}
        h = S5.two_sleeve(lad["BTC"]["ret"], lad["ETH"]["ret"])
        series = {"P1_btc_eth_2x": lin[2.0], NAME + "_alone": h, NAME + "_plus_P1": lin[2.0] + h,
                  "R1_1x": lin[1.0], NAME + "_plus_1x": lin[1.0] + h,
                  "R2_1p5x": lin[1.5], NAME + "_plus_1p5x": lin[1.5] + h}
        for name, r in series.items():
            res["variants"].setdefault(name, {})[fc.name] = stats(r, reg)
        if fc is lab.US_FUTURES:
            keep = {"series": series, "lad": lad}
    ser, lad = keep["series"], keep["lad"]
    hm = S5.complete_months(cut(ser[NAME + "_alone"], W0, None))
    pm = S5.complete_months(cut(ser["P1_btc_eth_2x"], W0, None))
    res["sleeve"] = {
        "corr_with_P1_monthly": float(np.corrcoef(hm, pm.reindex(hm.index).fillna(0))[0, 1]),
        "per_coin": {c: {"credit_pct_equity": float(cut(x["credit"], W0, None).sum() * 100),
                         "paid_pct_equity": float(cut(x["paid"], W0, None).sum() * 100),
                         "paid_over_credit": float(cut(x["paid"], W0, None).sum() / cut(x["credit"], W0, None).sum()),
                         "mean_notional": float(cut(x["notional"], W0, None).mean() * S28.TENOR_D),
                         "geo_monthly_pct_alone": stats(x["ret"], reg)["window"]["geo_monthly_pct"]}
                     for c, x in lad.items()},
        "deflated_sharpe_sleeve": lab.deflated_sharpe(lab.daily_from_bars(cut(ser[NAME + "_alone"], W0, None)), N_TRIALS),
        "deflated_sharpe_portfolio": lab.deflated_sharpe(lab.daily_from_bars(cut(ser[NAME + "_plus_P1"], W0, None)),
                                                         N_TRIALS)}
    v = res["variants"]
    a, pf, p1 = (v[n]["us-futures"] for n in (NAME + "_alone", NAME + "_plus_P1", "P1_btc_eth_2x"))
    checks = {
        "sleeve_return_positive": a["window"]["geo_monthly_pct"] > 0,
        "corr_with_P1_le_0p30": res["sleeve"]["corr_with_P1_monthly"] <= 0.30,
        "portfolio_sharpe_ge_P1_plus_0p20": pf["window"]["sharpe"] >= p1["window"]["sharpe"] + 0.20,
        "portfolio_dd_no_worse_than_P1": pf["window"]["max_dd_pct"] >= p1["window"]["max_dd_pct"],
        "sleeve_A_positive": a["A_2025_26"]["geo_monthly_pct"] > 0,
        "sleeve_stress_positive": v[NAME + "_alone"]["us-futures-stress"]["window"]["geo_monthly_pct"] > 0}
    res["checks"], res["pass"] = checks, bool(all(checks.values()))
    for name, x in v.items():
        b, w = x["us-futures"], x["us-futures"]["window"]
        print(f"{name:28s} {w['geo_monthly_pct']:+.2f}%/mo DD {w['max_dd_pct']:.0f}% sharpe {w['sharpe']:.2f} "
              f"worst day {w['worst_day_pct']:.1f}% ({w['worst_day']}) worst mo {w['worst_month_pct']:.0f}% | "
              + " ".join(f"{p.split('_')[0]} {b[p]['geo_monthly_pct']:+.2f}" for p in ("B_2021_22", "discovery", "A_2025_26"))
              + f" | 2022 {b['bear_2022']['geo_monthly_pct']:+.2f} | "
              + " ".join(f"{k} {y:+.1f}" for k, y in b["by_regime_mean_monthly_pct"].items())
              + f" | stress {x['us-futures-stress']['window']['geo_monthly_pct']:+.2f}", flush=True)
    print("checks:", checks, "PASS" if res["pass"] else "FAIL")
    print(json.dumps(res["sleeve"], indent=1))
    with open(os.path.join(HERE, "results", "round28.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
