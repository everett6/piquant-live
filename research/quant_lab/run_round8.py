"""Run PREREG8 once: select one cell per mechanism on discovery, judge it on holdouts A-D. Writes
results/round8.json."""
from __future__ import annotations

import json
import math
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies8 as S8

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 697
SPLITS = {"discovery": ("2022-12-17", "2025-07-01"), "A": ("2025-07-01", None), "B": ("2021-01-01", "2022-12-17"),
          "C": ("2018-01-01", "2021-01-01"), "D": ("2016-01-01", "2018-01-01")}
HOLDOUTS = ("A", "B", "C", "D")


def ns(x):
    return pd.Timestamp(x, tz="UTC").value if x else np.iinfo(np.int64).max


def split_trades(tr, name):
    a, b = SPLITS[name]
    return tr[(tr["entry_ns"] >= ns(a)) & (tr["entry_ns"] < ns(b))]


def stats(tr, a, b, idx_end):
    out = {"trades": int(len(tr))}
    if len(tr) < 2:
        return out
    net = tr["net"].to_numpy()
    start = pd.Timestamp(a, tz="UTC")
    end = pd.Timestamp(b, tz="UTC") - pd.Timedelta(minutes=1) if b else idx_end
    months = max((end - start).days / 30.44, 1e-9)
    daily = lab.daily_from_trades(tr, start, end)
    m = S5.complete_months(daily)
    out.update({"mean_net_bps": float(net.mean() * 1e4), "mean_gross_bps": float(tr["gross"].mean() * 1e4),
                "t_stat": float(net.mean() / (net.std(ddof=1) / math.sqrt(len(net)))) if net.std() > 0 else 0.0,
                "win_rate": float((net > 0).mean()), "trades_per_month": float(len(tr) / months),
                "exit_mix": tr["reason"].value_counts(normalize=True).round(3).to_dict(),
                "geo_monthly_pct_1x": float((np.prod(1 + m) ** (1 / len(m)) - 1) * 100) if len(m) else float("nan")})
    return out


def lev_for_7(tr_by_split, idx_end):
    """Smallest leverage (0.25 steps, <=5x) whose pooled holdout geometric monthly return >= 7%."""
    best = None
    for lev in np.arange(0.25, 5.0001, 0.25):
        ms, dds = [], []
        for name in HOLDOUTS:
            a, b = SPLITS[name]
            t = tr_by_split[name].assign(net=lambda x: x["net"] * lev)
            start = pd.Timestamp(a, tz="UTC")
            end = pd.Timestamp(b, tz="UTC") - pd.Timedelta(minutes=1) if b else idx_end
            d = lab.daily_from_trades(t, start, end)
            ms.append(S5.complete_months(d))
            dds.append(lab.summarize(d)["max_dd_pct"])
        m = pd.concat(ms)
        g = float((np.prod(1 + m.clip(lower=-0.999)) ** (1 / len(m)) - 1) * 100)
        if g >= 7.0:
            return {"leverage": float(lev), "geo_monthly_pct": g, "worst_split_max_dd_pct": float(min(dds))}
        best = {"leverage": None, "geo_monthly_pct_at_5x": g, "worst_split_max_dd_pct_at_5x": float(min(dds))}
    return best


def main():
    df = lab.load_candles(since=None)
    c = df["c"]
    arr = [df[k].to_numpy() for k in ("o", "h", "l", "c")] + [df.index.as_unit("ns").asi8]
    regime = S8.daily_regime_1m(lab.resample(df, "1D")["c"], 50, df.index)
    idx_end = df.index[-1]
    res = {"n_trials": N_TRIALS, "mechanisms": {}}
    for mech, grid, make in (("M1_shock_fade", S8.M1_GRID, lambda p: S8.m1_signal(c, p["k"])),
                             ("M2_trend_pullback", S8.M2_GRID, lambda p: S8.m2_signal(c, regime, p["q"]))):
        cells = []
        for p in grid:
            side, tp, sl = make(p)
            tr = S8.sim_maker(*arr, side, tp, sl, hold=p["hold"])
            disc = stats(split_trades(tr, "discovery"), *SPLITS["discovery"], idx_end)
            cells.append({"params": p, "discovery": disc, "_tr": tr})
            print(mech, p, {k: disc.get(k) for k in ("trades", "mean_net_bps", "mean_gross_bps", "win_rate")}, flush=True)
        ok = [x for x in cells if x["discovery"]["trades"] >= 300]
        out = {"cells": [{k: v for k, v in x.items() if k != "_tr"} for x in cells]}
        if not ok:
            out["selected"] = None
            out["pass"] = False
            res["mechanisms"][mech] = out
            continue
        sel = max(ok, key=lambda x: x["discovery"]["mean_net_bps"])
        tr = sel["_tr"]
        by = {h: split_trades(tr, h) for h in HOLDOUTS}
        hs = {h: stats(by[h], *SPLITS[h], idx_end) for h in HOLDOUTS}
        pooled = pd.concat(by.values())
        pn = pooled["net"].to_numpy()
        pooled_t = float(pn.mean() / (pn.std(ddof=1) / math.sqrt(len(pn)))) if len(pn) > 1 and pn.std() > 0 else 0.0
        ms = [S5.complete_months(lab.daily_from_trades(by[h], pd.Timestamp(SPLITS[h][0], tz="UTC"),
              pd.Timestamp(SPLITS[h][1], tz="UTC") - pd.Timedelta(minutes=1) if SPLITS[h][1] else idx_end)) for h in HOLDOUTS]
        pm = pd.concat(ms)
        pooled_geo = float((np.prod(1 + pm) ** (1 / len(pm)) - 1) * 100)
        req = [h for h in HOLDOUTS if not (h == "D" and hs[h]["trades"] < 100)]
        c1 = all(hs[h]["trades"] >= 100 and hs[h].get("mean_net_bps", -1) > 0 for h in req)
        checks = {"each_holdout_positive": c1, "pooled_t_gt_2": pooled_t > 2, "pooled_geo_positive": pooled_geo > 0}
        out.update({"selected": sel["params"], "holdouts": hs, "pooled": {"trades": int(len(pn)),
                    "mean_net_bps": float(pn.mean() * 1e4), "t_stat": pooled_t, "win_rate": float((pn > 0).mean()),
                    "geo_monthly_pct_1x": pooled_geo}, "checks": checks, "pass": bool(all(checks.values())),
                    "leverage_for_7pct": lev_for_7(by, idx_end)})
        res["mechanisms"][mech] = out
        print(mech, "SELECTED", sel["params"], flush=True)
        for h in HOLDOUTS:
            x = hs[h]
            print(f"  {h}: {x['trades']} trades, net {x.get('mean_net_bps', float('nan')):+.2f} bps "
                  f"(gross {x.get('mean_gross_bps', float('nan')):+.2f}), win {x.get('win_rate', float('nan')):.1%}, "
                  f"{x.get('trades_per_month', 0):.0f}/mo, 1x {x.get('geo_monthly_pct_1x', float('nan')):+.2f}%/mo", flush=True)
        print("  pooled", out["pooled"], checks, "PASS" if out["pass"] else "FAIL", out["leverage_for_7pct"], flush=True)
    with open(os.path.join(HERE, "results", "round8.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
