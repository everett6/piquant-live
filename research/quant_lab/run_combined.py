"""Diagnostic (no parameter search): 1-minute replay of the configuration deployed in bc92bc6.
  core: 2x BTC while the last completed daily close > SMA50 (else flat), rebalanced at the next day's open,
        us-futures costs incl. funding; marked to market every minute.
  MFT:  the live-entry replica (round-1 S1 score, th 0.65, live geometry TP 39 / SL 30 gross bps, max hold 240 m,
        loser stop 45 m), longs only, and only while the daily regime is UP; 1x notional per trade, taker costs.
Reports average and per-month returns per period. Writes results/combined.json."""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies3 as S3
import strategies5 as S5
from run_round5 import PERIODS, cut

HERE = os.path.dirname(os.path.abspath(__file__))
CORE = dict(n=50, lev=2.0)
MFT = dict(th=0.65, tp=39.0, sl=30.0)


def main():
    full = lab.load_candles(since=None)
    daily = lab.resample(full, "1D")
    core_daily = lab.position_returns(daily, S5.m1_positions(daily, CORE["n"], CORE["lev"]), lab.US_FUTURES)
    # regime known at each daily close, usable from the next day: map to 1m bars causally
    up = pd.Series(S5.m1_positions(daily, CORE["n"], 1.0) > 0, index=daily.index + pd.Timedelta(days=1))
    up_1m = up.reindex(up.index.union(full.index)).ffill().reindex(full.index).fillna(False).to_numpy()
    conv = S3.s1_conv(full)
    sig = np.where(up_1m, np.where(S3.s7_signal(conv, MFT["th"]) > 0, 1, 0), 0)
    n = len(full)
    tr = lab.simulate_brackets(full["o"].to_numpy(), full["h"].to_numpy(), full["l"].to_numpy(), full["c"].to_numpy(),
                               full.index.as_unit("ns").asi8, sig, np.full(n, MFT["tp"]), np.full(n, MFT["sl"]),
                               max_hold=240, loser_stop=45, costs=lab.US_FUTURES)
    mft_daily = lab.daily_from_trades(tr, full.index[0], full.index[-1]).reindex(core_daily.index, fill_value=0.0)
    combo = (1 + core_daily) * (1 + mft_daily) - 1
    bh = daily["c"].pct_change().fillna(0)
    res = {"config": {"core": CORE, "mft": MFT}, "periods": {}}
    for name in PERIODS:
        parts = {k: cut(v, name) for k, v in (("core", core_daily), ("mft", mft_daily), ("combined", combo), ("bh", bh))}
        if parts["combined"].empty:
            continue
        out = {}
        for k, r in parts.items():
            m = S5.complete_months(r)
            out[k] = {"avg_monthly_pct": float(m.mean() * 100) if len(m) else float("nan"),
                      "geo_monthly_pct": float((np.prod(1 + m) ** (1 / len(m)) - 1) * 100) if len(m) else float("nan"),
                      "months_ge_7pct": float((m >= 0.07).mean()) if len(m) else float("nan"),
                      "months": int(len(m)), **lab.summarize(r)}
        ptr = tr[(tr["entry_ns"] >= cut(full.iloc[:, :1], name).index[0].value)
                 & (tr["entry_ns"] <= cut(full.iloc[:, :1], name).index[-1].value)]
        out["mft_trades"] = int(len(ptr))
        out["mft_mean_net_bps"] = float(ptr["net"].mean() * 1e4) if len(ptr) else float("nan")
        out["mft_win_rate_pct"] = float((ptr["net"] > 0).mean() * 100) if len(ptr) else float("nan")
        res["periods"][name] = out
        print(f"{name:10s} core {out['core']['geo_monthly_pct']:+6.2f}%/mo | MFT {out['mft']['geo_monthly_pct']:+6.2f}%/mo "
              f"({out['mft_trades']} trades, {out['mft_mean_net_bps']:+.1f} bps, win {out['mft_win_rate_pct']:.1f}%) | "
              f"combined {out['combined']['geo_monthly_pct']:+6.2f}%/mo, months>=7% {out['combined']['months_ge_7pct']:.0%}, "
              f"maxDD {out['combined']['max_dd_pct']:.0f}% | BH {out['bh']['geo_monthly_pct']:+6.2f}%/mo", flush=True)
    with open(os.path.join(HERE, "results", "combined.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
