"""Run PREREG30 once: P1's rule on four clocks (00, 06, 12, 18 UTC) and the equal split E4, on one 6-hour grid,
against the 00:00 clock (P1 on the grid). Writes results/round30.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies30 as S30
from run_round7 import cut
from run_round24 import CORE_PERIODS, stats

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 755


def main():
    m_btc = lab.load_candles(product="BTC-USD", since=None)
    m_eth = lab.load_candles(product="ETH-USD", since=None)
    b6 = {"BTC": S30.bars_6h(m_btc), "ETH": S30.bars_6h(m_eth)}
    d_btc, d_eth = lab.resample(m_btc, "1D"), lab.resample(m_eth, "1D")
    reg = S5.regime_months(S5.complete_months(cut(d_btc["c"].pct_change().fillna(0), "2016-01-01", None)))
    held = {c: {f"clock_{h:02d}": S30.clock_held(b, h) for h in S30.CLOCKS} for c, b in b6.items()}
    for c in held:
        held[c]["E4_split"] = sum(held[c][f"clock_{h:02d}"] for h in S30.CLOCKS) / len(S30.CLOCKS)
    names = [f"clock_{h:02d}" for h in S30.CLOCKS] + ["E4_split"]
    res = {"n_trials": N_TRIALS, "candles_end": str(d_btc.index[-1].date()), "variants": {}, "extra": {}}
    daily = {}
    for costs in (lab.US_FUTURES, lab.US_FUTURES_STRESS):
        for name in names:
            r6 = S5.two_sleeve(S30.grid_returns(b6["BTC"], held["BTC"][name], costs),
                               S30.grid_returns(b6["ETH"], held["ETH"][name], costs))
            r = lab.daily_from_bars(r6)
            res["variants"].setdefault(name, {})[costs.name] = stats(r, reg)
            if costs is lab.US_FUTURES:
                daily[name] = cut(r, "2016-01-01", None)
        p1 = S5.two_sleeve(lab.position_returns(d_btc, S5.m1_positions(d_btc, 50, 2.0), costs),
                           lab.position_returns(d_eth, S5.m1_positions(d_eth, 50, 2.0), costs))
        res["variants"].setdefault("P1_published_daily", {})[costs.name] = stats(p1, reg)
    df = pd.DataFrame(daily)
    ann = np.sqrt(365)
    sh = {n: float(df[n].mean() / df[n].std() * ann) for n in names}
    clocks = names[:4]
    res["extra"] = {
        "sharpe": sh, "vol_annual": {n: float(df[n].std() * ann) for n in names},
        "clock_corr_daily": df[clocks].corr().round(4).to_dict(),
        "mean_pairwise_corr": float(df[clocks].corr().where(~np.eye(4, dtype=bool)).stack().mean()),
        "sharpe_median_clock": float(np.median([sh[n] for n in clocks])),
        "sharpe_best_clock": float(max(sh[n] for n in clocks)), "sharpe_mean_clock": float(np.mean([sh[n] for n in clocks])),
        "turnover_per_day": {n: float(np.mean([held[c][n].diff().abs().sum() / (len(held[c][n]) / 4) for c in held]))
                             for n in names}}
    v = res["variants"]
    e, p = v["E4_split"]["us-futures"], v["clock_00"]["us-futures"]
    eg, pg = e["full_2016_26"]["geo_monthly_pct"], p["full_2016_26"]["geo_monthly_pct"]
    checks = {
        "sharpe_gt_P1": sh["E4_split"] > sh["clock_00"],
        "max_dd_no_worse_than_P1": e["full_2016_26"]["max_dd_pct"] >= p["full_2016_26"]["max_dd_pct"],
        "return_ge_7_and_within_0p25_of_P1": eg >= 7.0 and eg >= pg - 0.25,
        "bear_no_worse_than_P1": e["by_regime_mean_monthly_pct"]["bear"] >= p["by_regime_mean_monthly_pct"]["bear"],
        "A_positive": e["A_2025_26"]["geo_monthly_pct"] > 0,
        "stress_full_positive": v["E4_split"]["us-futures-stress"]["full_2016_26"]["geo_monthly_pct"] > 0}
    res["checks"], res["pass"] = checks, bool(all(checks.values()))
    for name in ["P1_published_daily"] + names:
        b = v[name]["us-futures"]
        full = b["full_2016_26"]
        print(f"{name:20s} full {full['geo_monthly_pct']:+.2f}%/mo maxDD {full['max_dd_pct']:.1f}% sharpe "
              f"{sh.get(name, full['sharpe']):.3f} | "
              + " ".join(f"{q.split('_')[0]} {b[q]['geo_monthly_pct']:+.2f}" for q in CORE_PERIODS)
              + f" | 2022 {b['bear_2022']['geo_monthly_pct']:+.2f} | "
              + " ".join(f"{k} {x:+.1f}" for k, x in b["by_regime_mean_monthly_pct"].items())
              + f" | stress {v[name]['us-futures-stress']['full_2016_26']['geo_monthly_pct']:+.2f}", flush=True)
    print("checks:", checks, "PASS" if res["pass"] else "FAIL")
    print(json.dumps({k: x for k, x in res["extra"].items() if k != "clock_corr_daily"}, indent=1))
    with open(os.path.join(HERE, "results", "round30.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
