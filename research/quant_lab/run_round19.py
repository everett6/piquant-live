"""Run PREREG19 once: funding carry C1 (always on) and C2 (high funding only). Writes results/round19.json."""
from __future__ import annotations

import glob
import json
import os

import pandas as pd

import lab
import strategies19 as S

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 734
FUND_DIR = os.path.expanduser("~/piquant-collect/funding/csv")
START = pd.Timestamp("2020-01-01", tz="UTC")
END = pd.Timestamp("2026-11-01", tz="UTC")  # end of the latest complete funding month (2026-10)


def load_funding(sym: str) -> pd.Series:
    parts = []
    for p in sorted(glob.glob(os.path.join(FUND_DIR, f"{sym}-fundingRate-*.csv"))):
        df = pd.read_csv(p)
        if "calc_time" not in df.columns:  # some early files have no header
            df = pd.read_csv(p, header=None, names=["calc_time", "funding_interval_hours", "last_funding_rate"])
        parts.append(df)
    df = pd.concat(parts).drop_duplicates("calc_time")
    s = pd.Series(df["last_funding_rate"].astype(float).to_numpy(),
                  index=pd.to_datetime(df["calc_time"].astype("int64"), unit="ms", utc=True)).sort_index()
    return s[(s.index >= START) & (s.index < END)]


def main():
    coins = {"BTC": ("BTC-USD", "BTCUSDT"), "ETH": ("ETH-USD", "ETHUSDT")}
    data = {}
    for k, (prod, sym) in coins.items():
        d = lab.resample(lab.load_candles(product=prod, since=None), "1D")
        d = d[(d.index >= START) & (d.index < END)]
        data[k] = (d, load_funding(sym))
    years = (END - START).days / 365.25
    res = {"n_trials": N_TRIALS, "window": [str(START.date()), str((END - pd.Timedelta(days=1)).date())],
           "funding_negative_share": {k: float((f < 0).mean()) for k, (_, f) in data.items()},
           "funding_mean_ann": {k: float(f.mean() * 3 * 365) for k, (_, f) in data.items()}, "rules": {}}
    for rule in ("C1", "C2"):
        out = {}
        for costs in (S.BASE, S.STRESS):
            books = {k: S.coin_returns(d, f, rule, costs) for k, (d, f) in data.items()}
            st = S.stats(S.portfolio(list(books.values())))
            st["liquidations"] = {k: int(b["liq"].sum()) for k, b in books.items()}
            st["time_in_market"] = {k: float(b["held"].mean()) for k, b in books.items()}
            out[costs.name] = st
        liq_rate = max(out["stress"]["liquidations"].values()) / years
        out["pass"] = S.passes(out["stress"], liq_rate)
        res["rules"][rule] = out
        b, s = out["base"], out["stress"]
        print(f"{rule} {'PASS' if out['pass'] else 'FAIL'} | base {b['ann']:+.2%}/yr dd {b['max_dd']:.2%} | "
              f"stress {s['ann']:+.2%}/yr dd {s['max_dd']:.2%} monthly {s['monthly']:+.3%} | periods "
              + ", ".join(f"{k} {v:+.2%}" for k, v in s["periods"].items()) + f" | liq {s['liquidations']} "
              f"| in-market {s['time_in_market']}")
    print("funding negative share", res["funding_negative_share"], "mean ann", res["funding_mean_ann"])
    with open(os.path.join(HERE, "results", "round19.json"), "w") as f:
        json.dump(res, f, indent=2)


if __name__ == "__main__":
    main()
