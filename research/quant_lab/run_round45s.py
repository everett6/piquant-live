"""Run PREREG45 once: H21 / R21 / R30 drawdown shutoffs on the three PREREG42/44 blends (rebuilt as in run_round44),
2020-01-01 to 2026-10-31, plus a block bootstrap of one-year largest drawdowns. Writes results/round45s.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies40 as S40
import strategies44 as S44
import strategies45s as S45
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 796
RECORDED = {"A_dualspeed": 2.1228, "B_vol_balanced": 2.7610, "C_bh_harvester": 2.8914}  # PREREG44 base %/mo
RULES = {"H21": (0.21, None), "R21": (0.21, S45.RESTART_DAYS), "R30": (0.30, S45.RESTART_DAYS)}


def metrics(r: pd.Series, reg: pd.Series) -> dict:
    m = extended_metrics(r, reg)
    return {k: m[k] for k in ("geo_monthly_pct", "max_dd_pct", "sharpe", "calmar", "A_geo_monthly_pct", "by_regime")
            if k in m} | {"worst_month_pct": float(S5.complete_months(r).min() * 100),
                          "annual_pct": float(((1 + m["geo_monthly_pct"] / 100) ** 12 - 1) * 100)}


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]
    qd = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in d}
    r3 = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in d}
    dual = {c: (0.5 * (qd[c] + r3[c])).reindex(btc_sub.index) for c in d}
    vb75 = {c: S44.vol_balanced_1x_2p5x_positions(full[c], qd[c], vol_target_ann=0.75, min_lev=1.0, max_lev=2.5)
            .reindex(btc_sub.index) for c in d}
    vb80 = {c: S44.vol_balanced_1x_2p5x_positions(full[c], qd[c], vol_target_ann=0.80, min_lev=1.0, max_lev=2.5)
            .reindex(btc_sub.index) for c in d}
    harv = {c: S44.bh_vol_harvester_positions(full[c], base_lev=1.0, min_mult=0.65, max_mult=1.35)
            .reindex(btc_sub.index) for c in d}
    zero = {c: pd.Series(0.0, index=btc_sub.index) for c in d}
    f = {c: load_funding(f"{c}USDT") for c in d}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    specs = {"A_dualspeed": (dual, zero, 0.60, 0.00, 0.40, 0.06, 0.20, 0.40),
             "B_vol_balanced": (vb75, zero, 0.70, 0.00, 0.30, 0.08, 0.28, 0.35),
             "C_bh_harvester": (vb80, harv, 0.55, 0.20, 0.25, 0.08, 0.30, 0.35)}
    res = {"prereg": "PREREG45", "n_trials": N_TRIALS, "window": [W0, W1], "base": {}, "variants": {},
           "events": {}, "days_in_cash": {}, "bootstrap": {}, "checks": {}, "pass_owner_target": {},
           "pass_value_of_stop": {}, "reproduced": {}}
    base_ret = {}
    for cname, (fut, car) in {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}.items():
        c1 = S19.portfolio([S19.coin_returns(d[c], f[c], "C1", car) for c in d]).reindex(btc_sub.index).fillna(0.0)
        for name, (t, b, ts, bs, cs, ds, dfull, dfl) in specs.items():
            r, _, _ = S44.simulate_bh_vol_balanced_portfolio(
                btc_sub, eth_sub, t["BTC"], t["ETH"], b["BTC"], b["ETH"], c1, rate, trend_share=ts, bh_share=bs,
                carry_share=cs, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
                spot_threshold=None, use_taper=True, dd_start=ds, dd_full=dfull, dd_floor=dfl)
            res["base"].setdefault(name, {})[cname] = metrics(r, reg)
            if cname == "base":
                base_ret[name] = r
            for rule, (lvl, rd) in RULES.items():
                o, ev, held = S45.apply_shutoff(r, rate, lvl, rd)
                key = f"{name}_{rule}"
                res["variants"].setdefault(key, {})[cname] = metrics(o, reg)
                if cname == "base":
                    res["events"][key] = ev
                    res["days_in_cash"][key] = int((~held).sum())
    for name, r in base_ret.items():
        got = res["base"][name]["base"]["geo_monthly_pct"]
        res["reproduced"][name] = {"got": got, "recorded": RECORDED[name], "ok": abs(got - RECORDED[name]) < 0.01}
        dd = S45.bootstrap_year_drawdowns(r)
        res["bootstrap"][name] = {"mean_year_max_dd_pct": float(dd.mean() * 100), "median_pct": float(np.median(dd) * 100),
                                  "p90_pct": float(np.quantile(dd, 0.9) * 100),
                                  "share_years_ge_21pct": float((dd >= 0.21).mean()),
                                  "share_years_ge_30pct": float((dd >= 0.30).mean())}
    for key, v in res["variants"].items():
        name = key.rsplit("_", 1)[0]
        x, s, bse = v["base"], v["stress"], res["base"][name]["base"]
        ch = {"1_ge_60pct_year": x["geo_monthly_pct"] >= 3.99, "2_max_dd_ge_neg25": x["max_dd_pct"] >= -25.0,
              "3_return_ge_base": x["geo_monthly_pct"] >= bse["geo_monthly_pct"],
              "4_A_positive": x["A_geo_monthly_pct"] > 0, "5_stress_positive": s["geo_monthly_pct"] > 0}
        res["checks"][key] = ch
        res["pass_owner_target"][key] = bool(all(ch.values()))
        res["pass_value_of_stop"][key] = bool(x["geo_monthly_pct"] >= bse["geo_monthly_pct"] - 0.05
                                              and x["max_dd_pct"] >= bse["max_dd_pct"] + 2.0)
    print("reproduced:", res["reproduced"])
    for name in specs:
        rows = [(name, res["base"][name])] + [(f"{name}_{k}", res["variants"][f"{name}_{k}"]) for k in RULES]
        for key, v in rows:
            x = v["base"]
            print(f"{key:22s} {x['geo_monthly_pct']:+.2f}%/mo ({x['annual_pct']:+.0f}%/yr) maxDD {x['max_dd_pct']:.1f}% "
                  f"sharpe {x['sharpe']:.2f} calmar {x['calmar']:.2f} A {x['A_geo_monthly_pct']:+.2f} worst mo {x['worst_month_pct']:.1f} "
                  f"bear {x['by_regime']['bear']:+.1f} | stress {v['stress']['geo_monthly_pct']:+.2f}"
                  + (f" | events {res['events'][key]} cash {res['days_in_cash'][key]}d owner={res['pass_owner_target'][key]} "
                     f"value={res['pass_value_of_stop'][key]}" if key in res["events"] else ""), flush=True)
    print(json.dumps(res["bootstrap"], indent=1))
    with open(os.path.join(HERE, "results", "round45s.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
