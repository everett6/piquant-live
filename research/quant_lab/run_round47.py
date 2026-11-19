"""Run PREREG47 once: X1 boosted-tree forecaster (long/flat) and X2 boosted-tree meta-label filter on blend A's trend
sleeve, walk-forward from 2020-01-01. Writes results/round47.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd
from sklearn.ensemble import HistGradientBoostingClassifier
from sklearn.inspection import permutation_importance

import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies40 as S40
import strategies44 as S44
import strategies47 as S47
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 802
COINS = ("BTC", "ETH")


def metrics(r: pd.Series, reg: pd.Series) -> dict:
    m = extended_metrics(r, reg)
    out = {k: m[k] for k in ("geo_monthly_pct", "max_dd_pct", "sharpe", "calmar", "A_geo_monthly_pct", "by_regime")}
    out["deflated_sharpe_802"] = float(lab.deflated_sharpe(r, N_TRIALS))
    out["annual_pct"] = float(((1 + m["geo_monthly_pct"] / 100) ** 12 - 1) * 100)
    return out


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in COINS}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in COINS}
    idx = d["BTC"].index
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    fund = {c: load_funding(f"{c}USDT") for c in COINS}
    qd = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in COINS}
    r3 = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in COINS}
    dual = {c: 0.5 * (qd[c] + r3[c]) for c in COINS}
    reg = S5.regime_months(S5.complete_months(cut(d["BTC"]["c"].pct_change().fillna(0), W0, W1)))

    # pooled feature table (BTC rows then ETH rows)
    parts = []
    for i, (c, o) in enumerate((("BTC", "ETH"), ("ETH", "BTC"))):
        f = S47.features(full[c], full[o], i)
        fwd = full[c]["c"].shift(-S47.HORIZON) / full[c]["c"] - 1
        f["_y1"] = S47.forward_up(full[c]["c"])
        tr = dual[c] * fwd
        f["_y2"] = (tr > 0).astype(float).where(fwd.notna() & (dual[c] != 0))
        f["_date"], f["_coin"] = f.index, c
        parts.append(f)
    tab = pd.concat(parts, ignore_index=True)
    X = tab.drop(columns=["_y1", "_y2", "_date", "_coin"])
    dates = tab["_date"]
    p1 = S47.walk_forward_proba(X, tab["_y1"], dates, W0)
    p2 = S47.walk_forward_proba(X, tab["_y2"], dates, W0, train_mask=tab["_y2"].notna())

    def per_coin(p, c):
        k = tab["_coin"] == c
        return pd.Series(p[k].to_numpy(), index=pd.DatetimeIndex(dates[k]))

    scored = (dates >= pd.Timestamp(W0, tz="UTC")) & (dates < pd.Timestamp(W1, tz="UTC"))
    res = {"prereg": "PREREG47", "n_trials": N_TRIALS, "window": [W0, W1], "params": S47.PARAMS,
           "auc": {"X1_pooled": S47.auc(tab["_y1"][scored], p1[scored]),
                   "X2_pooled": S47.auc(tab["_y2"][scored], p2[scored])},
           "variants": {}, "reference": {}, "info": {}, "checks": {}, "pass": {}}
    for c in COINS:
        k = scored & (tab["_coin"] == c)
        res["auc"][f"X1_{c}"] = S47.auc(tab["_y1"][k], p1[k])
        res["auc"][f"X2_{c}"] = S47.auc(tab["_y2"][k], p2[k])

    x1pos = {c: S47.threshold_position(per_coin(p1, c)).reindex(full[c].index).fillna(0.0) for c in COINS}
    x2dual = {c: S47.meta_filter(dual[c], per_coin(p2, c)) for c in COINS}
    win = (x1pos["BTC"].index >= pd.Timestamp(W0, tz="UTC")) & (x1pos["BTC"].index < pd.Timestamp(W1, tz="UTC"))
    res["info"]["X1_share_long"] = {c: float(x1pos[c][win[: len(x1pos[c])]].mean()) for c in COINS}
    res["info"]["X1_turnover_per_year"] = {c: float(x1pos[c].reindex(idx).diff().abs().sum() / (len(idx) / 365))
                                           for c in COINS}
    res["info"]["X2_share_cut"] = {c: float(((x2dual[c] != dual[c]).reindex(idx)).mean()) for c in COINS}

    def sleeve(pos, fut):
        legs = [cut(lab.position_returns(full[c], pos[c].reindex(full[c].index).fillna(0).to_numpy(), fut), W0, None)
                .reindex(idx).fillna(0.0) for c in COINS]
        return S5.two_sleeve(legs[0], legs[1])

    def blend(dpos, fut, car):
        c1 = S19.portfolio([S19.coin_returns(d[c], fund[c], "C1", car) for c in COINS]).reindex(idx).fillna(0.0)
        zero = pd.Series(0.0, index=idx)
        r, _, _ = S44.simulate_bh_vol_balanced_portfolio(
            d["BTC"], d["ETH"], dpos["BTC"].reindex(idx), dpos["ETH"].reindex(idx), zero, zero, c1, rate,
            trend_share=0.60, bh_share=0.0, carry_share=0.40, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, spot_threshold=None, use_taper=True, dd_start=0.06, dd_full=0.20,
            dd_floor=0.40)
        return r

    for cname, (fut, car) in {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}.items():
        res["reference"].setdefault("QD_1x_sleeve", {})[cname] = metrics(sleeve(qd, fut), reg)
        res["reference"].setdefault("blend_A", {})[cname] = metrics(blend(dual, fut, car), reg)
        res["variants"].setdefault("X1_forecaster", {})[cname] = metrics(sleeve(x1pos, fut), reg)
        res["variants"].setdefault("X2_meta_filter", {})[cname] = metrics(blend(x2dual, fut, car), reg)

    # permutation importance: model fitted before the last 365 days, scored on them (X1 label)
    cut_d = pd.Timestamp(W1, tz="UTC") - pd.Timedelta(days=365)
    ok = X.notna().all(axis=1)
    tr = ok & tab["_y1"].notna() & (dates < cut_d - pd.Timedelta(days=S47.HORIZON))
    te = ok & tab["_y1"].notna() & (dates >= cut_d) & (dates < pd.Timestamp(W1, tz="UTC"))
    m = HistGradientBoostingClassifier(**S47.PARAMS).fit(X[tr].to_numpy(), tab["_y1"][tr].to_numpy())
    pi = permutation_importance(m, X[te].to_numpy(), tab["_y1"][te].to_numpy(), scoring="roc_auc", n_repeats=10,
                                random_state=0)
    res["info"]["X1_perm_importance_last_year"] = dict(sorted(zip(X.columns, pi.importances_mean.round(4).tolist()),
                                                              key=lambda kv: -kv[1]))

    x1, x1s, qdb = res["variants"]["X1_forecaster"]["base"], res["variants"]["X1_forecaster"]["stress"], \
        res["reference"]["QD_1x_sleeve"]["base"]
    res["checks"]["X1_forecaster"] = {
        "1_auc_ge_0p53": (res["auc"]["X1_pooled"] or 0) >= 0.53, "2_sharpe_gt_qd": x1["sharpe"] > qdb["sharpe"],
        "3_recent_positive": x1["A_geo_monthly_pct"] > 0, "4_stress_positive": x1s["geo_monthly_pct"] > 0,
        "5_dsr_ge_0p50": x1["deflated_sharpe_802"] >= 0.50}
    x2, x2s = res["variants"]["X2_meta_filter"]["base"], res["variants"]["X2_meta_filter"]["stress"]
    ab, ast = res["reference"]["blend_A"]["base"], res["reference"]["blend_A"]["stress"]
    res["checks"]["X2_meta_filter"] = {
        "1_dd_2pt_better": x2["max_dd_pct"] >= ab["max_dd_pct"] + 2.0,
        "2_return_within_0p10": x2["geo_monthly_pct"] >= ab["geo_monthly_pct"] - 0.10,
        "3_sharpe_gt_A": x2["sharpe"] > ab["sharpe"],
        "4_stress_within_0p10": x2s["geo_monthly_pct"] >= ast["geo_monthly_pct"] - 0.10,
        "5_recent_positive": x2["A_geo_monthly_pct"] > 0}
    for k, ch in res["checks"].items():
        res["pass"][k] = bool(all(ch.values()))

    print("AUC", {k: round(v, 4) if v else v for k, v in res["auc"].items()})
    for grp in ("reference", "variants"):
        for name, v in res[grp].items():
            x, s = v["base"], v["stress"]
            b = x["by_regime"]
            print(f"{name:16s} {x['geo_monthly_pct']:+.2f}%/mo ({x['annual_pct']:+.0f}%/yr) maxDD {x['max_dd_pct']:.1f}% "
                  f"sharpe {x['sharpe']:.2f} DSR {x['deflated_sharpe_802']:.2f} A {x['A_geo_monthly_pct']:+.2f} | "
                  f"bear {b['bear']:+.1f} flat {b['flat']:+.1f} bull {b['bull']:+.1f} | stress {s['geo_monthly_pct']:+.2f}")
    print("info", res["info"])
    for k in res["checks"]:
        print(k, "PASS" if res["pass"][k] else "FAIL", res["checks"][k])
    with open(os.path.join(HERE, "results", "round47.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
