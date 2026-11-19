"""Run PREREG50 once: blend A with the trend decided weekly (L1) or monthly (L2) instead of daily.
Writes results/round50.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies40 as S40
import strategies44 as S44
import strategies50 as S50
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
COINS = ("BTC", "ETH")
SCHEDULES = {"daily_A": None, "L1_weekly": ("weekly", None), "L2_monthly": ("monthly", None),
             "shift_weekly_wed": ("weekly", 2), "shift_monthly_15th": ("monthly", 15)}


def metrics(r: pd.Series, reg: pd.Series) -> dict:
    m = extended_metrics(r, reg)
    out = {k: m[k] for k in ("geo_monthly_pct", "max_dd_pct", "sharpe", "A_geo_monthly_pct", "by_regime")}
    out["annual_pct"] = float(((1 + m["geo_monthly_pct"] / 100) ** 12 - 1) * 100)
    out["worst_day_pct"] = float(r.min() * 100)
    out["worst_month_pct"] = float(((1 + r).groupby(r.index.tz_convert(None).to_period("M")).prod() - 1).min() * 100)
    return out


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in COINS}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in COINS}
    idx = d["BTC"].index
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    fund = {c: load_funding(f"{c}USDT") for c in COINS}
    dual = {c: 0.5 * (pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index)
                      + pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index)) for c in COINS}
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), W0, W1)))

    def held(sched):
        if sched is None:
            return dual
        return {c: S50.hold_between(dual[c], S50.decision_mask(full[c].index, *sched)) for c in COINS}

    def blend(pos, fut, car):
        c1 = S19.portfolio([S19.coin_returns(d[c], fund[c], "C1", car) for c in COINS]).reindex(idx).fillna(0.0)
        zero = pd.Series(0.0, index=idx)
        r, _, _ = S44.simulate_bh_vol_balanced_portfolio(
            d["BTC"], d["ETH"], pos["BTC"].reindex(idx), pos["ETH"].reindex(idx), zero, zero, c1, rate,
            trend_share=0.60, bh_share=0.0, carry_share=0.40, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, spot_threshold=None, use_taper=True, dd_start=0.06, dd_full=0.20,
            dd_floor=0.40)
        return r

    res = {"prereg": "PREREG50", "n_trials_after": 814, "window": [W0, W1], "variants": {}, "info": {},
           "checks": {}, "pass": {}}
    for name, sched in SCHEDULES.items():
        pos = held(sched)
        res["info"][name] = {
            "trend_turnover_per_year": sum(S50.turnover_per_year(pos[c].reindex(idx)) for c in COINS),
            "trend_changes_per_year": sum(float((pos[c].reindex(idx).diff().abs() > 1e-9).sum() / (len(idx) / 365))
                                          for c in COINS)}
        for cname, (fut, car) in {"base": (lab.US_FUTURES, S19.BASE),
                                  "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}.items():
            res["variants"].setdefault(name, {})[cname] = metrics(blend(pos, fut, car), reg)
    a, ast, ta = res["variants"]["daily_A"]["base"], res["variants"]["daily_A"]["stress"], \
        res["info"]["daily_A"]["trend_turnover_per_year"]
    for name in ("L1_weekly", "L2_monthly"):
        x, s = res["variants"][name]["base"], res["variants"][name]["stress"]
        ch = {"1_return_within_0p10": x["geo_monthly_pct"] >= a["geo_monthly_pct"] - 0.10,
              "2_dd_within_1pt": x["max_dd_pct"] >= a["max_dd_pct"] - 1.0,
              "3_stress_ge_A": s["geo_monthly_pct"] >= ast["geo_monthly_pct"],
              "4_recent_positive": x["A_geo_monthly_pct"] > 0,
              "5_turnover_le_half": res["info"][name]["trend_turnover_per_year"] <= 0.5 * ta}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))
    for name in SCHEDULES:
        x, s, i = res["variants"][name]["base"], res["variants"][name]["stress"], res["info"][name]
        b = x["by_regime"]
        print(f"{name:20s} {x['geo_monthly_pct']:+.2f}%/mo ({x['annual_pct']:+.0f}%/yr) maxDD {x['max_dd_pct']:.1f}% "
              f"sharpe {x['sharpe']:.2f} recent {x['A_geo_monthly_pct']:+.2f} worst day {x['worst_day_pct']:.1f} "
              f"worst mo {x['worst_month_pct']:.1f} | bear {b['bear']:+.1f} flat {b['flat']:+.1f} bull {b['bull']:+.1f} | "
              f"stress {s['geo_monthly_pct']:+.2f} | turnover {i['trend_turnover_per_year']:.1f}/yr "
              f"changes {i['trend_changes_per_year']:.0f}/yr")
    for k in res["checks"]:
        print(k, "PASS" if res["pass"][k] else "FAIL", res["checks"][k])
    with open(os.path.join(HERE, "results", "round50.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
