"""Run PREREG49 once: 80% blend A + 20% SPY (H1) or QQQ (H2) held, rebalanced monthly; main window 2020-01..2026-10
with blend A, early window 2016-08..2019-12 with blend A0 (carry share in T-bills). Writes results/round49.json."""
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
import strategies48 as S48
import strategies49 as S49
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics
from run_round48 import fetch

HERE = os.path.dirname(os.path.abspath(__file__))
COINS = ("BTC", "ETH")
ETFS = {"H1_SPY": "SPY", "H2_QQQ": "QQQ"}
E0, E1 = "2016-08-01", "2020-01-01"


def metrics(r: pd.Series, reg: pd.Series) -> dict:
    m = extended_metrics(r, reg)
    out = {k: m[k] for k in ("geo_monthly_pct", "max_dd_pct", "sharpe", "A_geo_monthly_pct", "by_regime")}
    out["annual_pct"] = float(((1 + m["geo_monthly_pct"] / 100) ** 12 - 1) * 100)
    mo = (1 + r).groupby(r.index.tz_convert(None).to_period("M")).prod() - 1
    out["worst_month_pct"] = float(mo.min() * 100)
    return out


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in COINS}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    fund = {c: load_funding(f"{c}USDT") for c in COINS}
    dual = {c: 0.5 * (pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index)
                      + pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index)) for c in COINS}
    etf = {n: fetch(s) for n, s in ETFS.items()}

    def blend(d, idx, carry, fut):
        zero = pd.Series(0.0, index=idx)
        r, _, _ = S44.simulate_bh_vol_balanced_portfolio(
            d["BTC"], d["ETH"], dual["BTC"].reindex(idx), dual["ETH"].reindex(idx), zero, zero, carry, rate,
            trend_share=0.60, bh_share=0.0, carry_share=0.40, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, spot_threshold=None, use_taper=True, dd_start=0.06, dd_full=0.20,
            dd_floor=0.40)
        return r

    res = {"prereg": "PREREG49", "n_trials_after": 812, "windows": {"main": [W0, W1], "early": [E0, E1]},
           "blend": {}, "variants": {}, "sizing": {}, "info": {}, "checks": {}, "pass": {}}
    for wname, (t0, t1) in {"main": (START, END), "early": (pd.Timestamp(E0, tz="UTC"),
                                                            pd.Timestamp(E1, tz="UTC"))}.items():
        d = {c: full[c][(full[c].index >= t0) & (full[c].index < t1)] for c in COINS}
        idx = d["BTC"].index
        reg = S5.regime_months(S5.complete_months(d["BTC"]["c"].pct_change().fillna(0)))
        rf_daily = rate.reindex(idx, method="ffill").fillna(0.0) / 100 / 365
        for cname, (fut, car, bps) in {"base": (lab.US_FUTURES, S19.BASE, 3.0),
                                       "stress": (lab.US_FUTURES_STRESS, S19.STRESS, 8.0)}.items():
            carry = (S19.portfolio([S19.coin_returns(d[c], fund[c], "C1", car) for c in COINS]).reindex(idx)
                     .fillna(0.0) if wname == "main" else rf_daily)
            a = blend(d, idx, carry, fut)
            res["blend"].setdefault(wname, {})[cname] = metrics(a, reg)
            for name, px in etf.items():
                r_etf = px.pct_change().fillna(0.0)
                r_etf = S48.on_calendar(r_etf[(r_etf.index >= t0.tz_convert(None)) & (r_etf.index < t1.tz_convert(None))],
                                        idx)
                p, n_reb = S49.monthly_rebalanced(a, r_etf, 0.20, bps)
                res["variants"].setdefault(name, {}).setdefault(wname, {})[cname] = metrics(p, reg)
                if cname == "base":
                    res["info"].setdefault(name, {})[wname] = {
                        "rebalances": n_reb, "corr_with_blend": float(np.corrcoef(a.to_numpy(), r_etf.to_numpy())[0, 1])}
                    for w in (0.10, 0.30):
                        res["sizing"].setdefault(name, {}).setdefault(wname, {})[f"{int(w * 100)}pct"] = metrics(
                            S49.monthly_rebalanced(a, r_etf, w, bps)[0], reg)
    for name in ETFS:
        v, b = res["variants"][name], res["blend"]
        ch = {"1_sharpe_both": all(v[w]["base"]["sharpe"] > b[w]["base"]["sharpe"] for w in ("main", "early")),
              "2_return_within_0p15_both": all(v[w]["base"]["geo_monthly_pct"] >= b[w]["base"]["geo_monthly_pct"] - 0.15
                                               for w in ("main", "early")),
              "3_dd_within_1pt_both": all(v[w]["base"]["max_dd_pct"] >= b[w]["base"]["max_dd_pct"] - 1.0
                                          for w in ("main", "early")),
              "4_stress_within_0p15": v["main"]["stress"]["geo_monthly_pct"]
              >= b["main"]["stress"]["geo_monthly_pct"] - 0.15,
              "5_recent_positive": v["main"]["base"]["A_geo_monthly_pct"] > 0}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))

    def line(label, x):
        g = x["by_regime"]
        return (f"{label:34s} {x['geo_monthly_pct']:+.2f}%/mo ({x['annual_pct']:+.0f}%/yr) maxDD {x['max_dd_pct']:.1f}% "
                f"sharpe {x['sharpe']:.2f} worst mo {x['worst_month_pct']:.1f} | bear {g.get('bear', 0):+.1f} "
                f"bull {g.get('bull', 0):+.1f}")
    for w in ("main", "early"):
        print(line(f"[{w}] blend alone", res["blend"][w]["base"]), "stress",
              round(res["blend"][w]["stress"]["geo_monthly_pct"], 2), "recent", round(res["blend"][w]["base"]["A_geo_monthly_pct"], 2))
        for name in ETFS:
            x = res["variants"][name][w]
            print(line(f"[{w}] 80/20 {name}", x["base"]), "stress", round(x["stress"]["geo_monthly_pct"], 2),
                  "recent", round(x["base"]["A_geo_monthly_pct"], 2), res["info"][name][w])
            for k, s in res["sizing"][name][w].items():
                print(line(f"      ({k} sleeve)", s))
    for name in ETFS:
        print(name, "PASS" if res["pass"][name] else "FAIL", res["checks"][name])
    with open(os.path.join(HERE, "results", "round49.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
