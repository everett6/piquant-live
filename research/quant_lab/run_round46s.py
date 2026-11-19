"""Run PREREG46 once: V1 rebalancing, V2 expensive-option put-spread selling, V3 cheap-option put-spread buying and
V4 ramped downtrend short, each as a 20% sleeve beside blend A (Q42 dual-speed). Writes results/round46s.json."""
from __future__ import annotations

import dataclasses
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
import strategies46s as S46
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round26 import load_iv
from run_round33 import DTB3, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 800
W_ALL, W_OPT = "2020-01-01", "2021-04-01"
SLEEVE = 0.20
COINS = ("BTC", "ETH")


def metrics(r: pd.Series, reg: pd.Series) -> dict:
    m = extended_metrics(r, reg)
    out = {k: m[k] for k in ("geo_monthly_pct", "max_dd_pct", "sharpe", "calmar", "A_geo_monthly_pct", "by_regime")}
    out["worst_day_pct"] = float(r.min() * 100)
    out["annual_pct"] = float(((1 + m["geo_monthly_pct"] / 100) ** 12 - 1) * 100)
    return out


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in COINS}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    fund = {c: load_funding(f"{c}USDT") for c in COINS}
    iv = {c: load_iv(c) for c in COINS}
    qd = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in COINS}
    r3 = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in COINS}
    gates = {c: S46.vrp_gates(full[c], iv[c]) for c in COINS}
    v4pos = {c: S46.ramp_short_positions(full[c]) for c in COINS}

    def window(w0):
        t0 = pd.Timestamp(w0, tz="UTC")
        return {c: full[c][(full[c].index >= t0) & (full[c].index < END)] for c in COINS}

    def blend_a(d, fut, car):
        idx = d["BTC"].index
        c1 = S19.portfolio([S19.coin_returns(d[c], fund[c], "C1", car) for c in COINS]).reindex(idx).fillna(0.0)
        dual = {c: (0.5 * (qd[c] + r3[c])).reindex(idx) for c in COINS}
        zero = pd.Series(0.0, index=idx)
        r, _, _ = S44.simulate_bh_vol_balanced_portfolio(
            d["BTC"], d["ETH"], dual["BTC"], dual["ETH"], zero, zero, c1, rate, trend_share=0.60, bh_share=0.0,
            carry_share=0.40, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
            spot_threshold=None, use_taper=True, dd_start=0.06, dd_full=0.20, dd_floor=0.40)
        return r

    def v4_sleeve(d, fut, car):
        idx = d["BTC"].index
        legs = []
        for c in COINS:
            p = pd.Series(v4pos[c], index=full[c].index)
            no_fund = dataclasses.replace(fut, funding_bps_h=fut.funding_bps_h if fut is lab.US_FUTURES_STRESS else 0.0)
            r = cut(lab.position_returns(full[c], p.to_numpy(), no_fund), str(idx[0].date()), None).reindex(idx).fillna(0.0)
            pos_f, neg_f, _ = S19.daily_funding(fund[c], idx)
            held = p.shift(1).reindex(idx).fillna(0.0)  # position held over each day
            received = np.where(held < 0, -held * (pos_f * car.funding_haircut + neg_f), -held * (pos_f + neg_f))
            legs.append(r + pd.Series(received, index=idx))
        return S5.two_sleeve(legs[0], legs[1]), {c: int((v4pos[c] < 0)[full[c].index >= idx[0]].sum()) for c in COINS}

    res = {"prereg": "PREREG46", "n_trials": N_TRIALS, "windows": {"V1_V4": [W_ALL, W1], "V2_V3": [W_OPT, W1]},
           "blend_A": {}, "variants": {}, "sleeve_alone": {}, "info": {}, "checks": {}, "pass": {}}
    for w0, names in ((W_ALL, ("V1_rebalance", "V4_ramp_short")), (W_OPT, ("V2_sell_expensive", "V3_buy_cheap"))):
        d = window(w0)
        idx = d["BTC"].index
        reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), w0, W1)))
        for cname, (fut, car, oc, spot_bps) in {"base": (lab.US_FUTURES, S19.BASE, S46.BASE, 3.0),
                                                "stress": (lab.US_FUTURES_STRESS, S19.STRESS, S46.STRESS, 6.0)}.items():
            a = blend_a(d, fut, car)
            res["blend_A"].setdefault(w0, {})[cname] = metrics(a, reg)
            for name in names:
                info = {}
                if name == "V1_rebalance":
                    sl, info["rebalances"] = S46.rebalance_mix(d["BTC"]["c"], d["ETH"]["c"], rate, cost_bps=spot_bps)
                elif name == "V4_ramp_short":
                    sl, info["days_short"] = v4_sleeve(d, fut, car)
                else:
                    gi, side = (0, -1) if name == "V2_sell_expensive" else (1, +1)
                    legs, gate_share, prem, paid = [], {}, {}, {}
                    for c in COINS:
                        mask = pd.Series(gates[c][gi], index=full[c].index).reindex(idx).fillna(False).to_numpy()
                        lad = S46.gated_spread_ladder(d[c]["c"], iv[c], mask, side, oc=oc)
                        legs.append(lad["ret"])
                        gate_share[c] = float(mask.mean())
                        prem[c] = float(lad["premium"].sum())
                        paid[c] = float(lad["payoff"].sum())
                    sl = S5.two_sleeve(legs[0], legs[1])
                    info.update({"gate_open_share": gate_share, "premium_sum": prem, "settled_value_sum": paid,
                                 "settled_over_premium": {c: paid[c] / prem[c] if prem[c] else None for c in COINS}})
                sl = sl.reindex(idx).fillna(0.0)
                port = (1 - SLEEVE) * a + SLEEVE * sl
                res["variants"].setdefault(name, {})[cname] = metrics(port, reg)
                res["sleeve_alone"].setdefault(name, {})[cname] = metrics(sl, reg) | {
                    "corr_with_A": float(np.corrcoef(sl.to_numpy(), a.to_numpy())[0, 1])}
                if cname == "base":
                    res["info"][name] = info
    for name, v in res["variants"].items():
        w0 = W_ALL if name in ("V1_rebalance", "V4_ramp_short") else W_OPT
        x, s, ab, ast = v["base"], v["stress"], res["blend_A"][w0]["base"], res["blend_A"][w0]["stress"]
        ch = {"1_bear_1pt_better": x["by_regime"]["bear"] >= ab["by_regime"]["bear"] + 1.0,
              "2_max_dd_no_worse": x["max_dd_pct"] >= ab["max_dd_pct"],
              "3_return_within_0p10": x["geo_monthly_pct"] >= ab["geo_monthly_pct"] - 0.10,
              "4_A_positive": x["A_geo_monthly_pct"] > 0,
              "5_stress_within_0p10": s["geo_monthly_pct"] >= ast["geo_monthly_pct"] - 0.10}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))

    def line(label, x, s):
        b = x["by_regime"]
        return (f"{label:30s} {x['geo_monthly_pct']:+.2f}%/mo ({x['annual_pct']:+.0f}%/yr) maxDD {x['max_dd_pct']:.1f}% "
                f"sharpe {x['sharpe']:.2f} A {x['A_geo_monthly_pct']:+.2f} worst day {x['worst_day_pct']:.1f} | "
                f"bear {b['bear']:+.1f} flat {b['flat']:+.1f} bull {b['bull']:+.1f} | stress {s['geo_monthly_pct']:+.2f}")
    for w0 in (W_ALL, W_OPT):
        print(line(f"blend A ({w0[:7]} on)", res["blend_A"][w0]["base"], res["blend_A"][w0]["stress"]))
    for name in res["variants"]:
        print(line(f"80% A + 20% {name}", res["variants"][name]["base"], res["variants"][name]["stress"]))
        sa = res["sleeve_alone"][name]
        print(line(f"   {name} alone", sa["base"], sa["stress"]) + f" | corr with A {sa['base']['corr_with_A']:+.2f}")
        print("   ", res["info"][name], "PASS" if res["pass"][name] else "FAIL", res["checks"][name])
    with open(os.path.join(HERE, "results", "round46s.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
