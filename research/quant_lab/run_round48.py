"""Run PREREG48 once: blend A's dual-speed trend rule on SPY (E1) and QQQ (E2), long/flat 1x with idle T-bills, each
as a 20% sleeve beside blend A. Fetches full free Yahoo history first. Writes results/round48.json."""
from __future__ import annotations

import datetime as dt
import json
import os

import numpy as np
import pandas as pd

import fetch_round24 as F
import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies40 as S40
import strategies44 as S44
import strategies48 as S48
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 804
COINS = ("BTC", "ETH")
ETFS = {"E1_SPY": "SPY", "E2_QQQ": "QQQ"}


def fetch(sym: str) -> pd.Series:
    path = os.path.join(F.OUT, f"{sym}_full.csv")
    if not os.path.exists(path):
        F.START = int(dt.datetime(1993, 1, 1, tzinfo=dt.timezone.utc).timestamp())
        pd.DataFrame(F.yahoo(sym), columns=["date", "adj"]).to_csv(path, index=False)
    s = pd.read_csv(path, parse_dates=["date"]).set_index("date")["adj"].astype(float)
    return s[~s.index.duplicated()].sort_index()


def metrics(r: pd.Series, reg: pd.Series | None) -> dict:
    if reg is None:  # long-history stats without regimes
        eq = (1 + r).cumprod()
        yrs = len(r) / 252
        return {"annual_pct": float((eq.iloc[-1] ** (1 / yrs) - 1) * 100),
                "max_dd_pct": float((eq / eq.cummax() - 1).min() * 100),
                "sharpe": float(r.mean() / r.std() * np.sqrt(252)), "start": str(r.index[0].date())}
    m = extended_metrics(r, reg)
    out = {k: m[k] for k in ("geo_monthly_pct", "max_dd_pct", "sharpe", "A_geo_monthly_pct", "by_regime")}
    out["annual_pct"] = float(((1 + m["geo_monthly_pct"] / 100) ** 12 - 1) * 100)
    return out


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in COINS}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in COINS}
    idx = d["BTC"].index
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"]).dropna()
    rate_utc = rate.copy()
    rate_utc.index = rate_utc.index.tz_localize("UTC")
    fund = {c: load_funding(f"{c}USDT") for c in COINS}
    dual = {c: 0.5 * (pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index)
                      + pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index)) for c in COINS}
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), W0, W1)))

    def blend(fut, car):
        c1 = S19.portfolio([S19.coin_returns(d[c], fund[c], "C1", car) for c in COINS]).reindex(idx).fillna(0.0)
        zero = pd.Series(0.0, index=idx)
        r, _, _ = S44.simulate_bh_vol_balanced_portfolio(
            d["BTC"], d["ETH"], dual["BTC"].reindex(idx), dual["ETH"].reindex(idx), zero, zero, c1, rate_utc,
            trend_share=0.60, bh_share=0.0, carry_share=0.40, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, spot_threshold=None, use_taper=True, dd_start=0.06, dd_full=0.20,
            dd_floor=0.40)
        return r

    a = {"base": blend(lab.US_FUTURES, S19.BASE), "stress": blend(lab.US_FUTURES_STRESS, S19.STRESS)}
    res = {"prereg": "PREREG48", "n_trials": N_TRIALS, "window": [W0, W1],
           "blend_A": {k: metrics(v, reg) for k, v in a.items()}, "variants": {}, "with_buy_hold": {},
           "sleeve_alone": {}, "long_history": {}, "info": {}, "checks": {}, "pass": {}}
    w0, w1 = pd.Timestamp(W0), pd.Timestamp(W1)
    for name, sym in ETFS.items():
        px = fetch(sym)
        pos = S48.dual_positions(px)
        bh_ret = px.pct_change().fillna(0.0)
        inwin = (px.index >= w0) & (px.index < w1)
        res["info"][name] = {"data_start": str(px.index[0].date()), "share_long": float(pos[inwin].mean()),
                             "trades_per_year": float(pos[inwin].diff().abs().sum() / (inwin.sum() / 252)),
                             "data_end": str(px.index[-1].date())}
        for cname, bps in (("base", 2.0), ("stress", 5.0)):
            sl = S48.etf_sleeve(px, pos, rate, bps)
            sl_cal = S48.on_calendar(sl[inwin], idx)
            bh_cal = S48.on_calendar(bh_ret[inwin], idx)
            res["variants"].setdefault(name, {})[cname] = metrics(0.8 * a[cname] + 0.2 * sl_cal, reg)
            res["with_buy_hold"].setdefault(name, {})[cname] = metrics(0.8 * a[cname] + 0.2 * bh_cal, reg)
            if cname == "base":
                res["sleeve_alone"][name] = {
                    "trend": metrics(sl_cal, reg), "buy_hold": metrics(bh_cal, reg),
                    "corr_with_A": float(np.corrcoef(sl_cal.to_numpy(), a["base"].to_numpy())[0, 1])}
                lh = px.index < w1
                res["long_history"][name] = {"trend": metrics(sl[lh].iloc[1:], None),
                                             "buy_hold": metrics(bh_ret[lh].iloc[1:], None)}
        x, s = res["variants"][name]["base"], res["variants"][name]["stress"]
        ab, ast = res["blend_A"]["base"], res["blend_A"]["stress"]
        sa = res["sleeve_alone"][name]
        ch = {"1_dd_1pt_better": x["max_dd_pct"] >= ab["max_dd_pct"] + 1.0,
              "2_return_within_0p10": x["geo_monthly_pct"] >= ab["geo_monthly_pct"] - 0.10,
              "3_sharpe_gt_A": x["sharpe"] > ab["sharpe"],
              "4_stress_within_0p10": s["geo_monthly_pct"] >= ast["geo_monthly_pct"] - 0.10,
              "5_recent_positive": x["A_geo_monthly_pct"] > 0,
              "6_sleeve_dd_lt_buy_hold": sa["trend"]["max_dd_pct"] > sa["buy_hold"]["max_dd_pct"]}
        res["checks"][name], res["pass"][name] = ch, bool(all(ch.values()))

    def line(label, x):
        b = x["by_regime"]
        return (f"{label:30s} {x['geo_monthly_pct']:+.2f}%/mo ({x['annual_pct']:+.0f}%/yr) maxDD {x['max_dd_pct']:.1f}% "
                f"sharpe {x['sharpe']:.2f} recent {x['A_geo_monthly_pct']:+.2f} | bear {b['bear']:+.1f} "
                f"flat {b['flat']:+.1f} bull {b['bull']:+.1f}")
    print(line("blend A", res["blend_A"]["base"]), "stress", round(res["blend_A"]["stress"]["geo_monthly_pct"], 2))
    for name in ETFS:
        print(line(f"80% A + 20% {name} trend", res["variants"][name]["base"]),
              "stress", round(res["variants"][name]["stress"]["geo_monthly_pct"], 2))
        print(line(f"80% A + 20% {name} buy&hold", res["with_buy_hold"][name]["base"]))
        sa = res["sleeve_alone"][name]
        print(line(f"   {name} trend alone", sa["trend"]), f"corr {sa['corr_with_A']:+.2f}")
        print(line(f"   {name} buy&hold alone", sa["buy_hold"]))
        print("   long history", res["long_history"][name], res["info"][name])
        print("  ", "PASS" if res["pass"][name] else "FAIL", res["checks"][name])
    with open(os.path.join(HERE, "results", "round48.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
