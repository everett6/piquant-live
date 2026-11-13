"""Run PREREG21 once: the PREREG9 A-taker cascade short with a VPIN > 0.65 filter on Binance BTCUSDT 2024 ticks,
plus the unfiltered replication and the maker markouts (descriptive). Writes results/round21.json."""
from __future__ import annotations

import json
import os
import sys

import numpy as np
import pandas as pd

import strategies21 as S
import strategies8 as S8
import strategies9 as S9

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(os.path.dirname(HERE))
N_TRIALS = 737
PQBIN = os.path.expanduser("~/piquant-collect/pqbin/spot")
START = pd.Timestamp("2024-01-01", tz="UTC")
SPLIT = pd.Timestamp("2024-07-01", tz="UTC")
END = pd.Timestamp("2025-01-01", tz="UTC")


def daily_closes() -> pd.Series:
    d = pd.read_csv(os.path.join(ROOT, "data", "historical", "btc_daily_full.csv"))
    d.index = pd.to_datetime(d["timestamp"], unit="ms", utc=True)
    return d["close"][d.index < END]


def main():
    months = pd.date_range(START, END - pd.Timedelta(days=1), freq="MS")
    paths = [os.path.join(PQBIN, f"BTCUSDT-{m:%Y-%m}.pqbin") for m in months]
    missing = [p for p in paths if not os.path.exists(p)]
    if missing:
        sys.exit(f"missing tick files: {missing}")
    start_s, start_ns = int(START.timestamp()), int(START.timestamp()) * 1_000_000_000
    n_s = int((END - START).total_seconds())
    n_min = n_s // 60
    buy, sell = np.zeros(n_s), np.zeros(n_s)
    bars = {k: np.full(n_min, np.nan) for k in ("o", "h", "l", "c")}
    bars["v"] = np.zeros(n_min)
    mk: dict = {h: [0.0, 0.0] for h in S.MARKOUT_S}
    bid: dict = {"minutes": 0, "fills": 0, **{h: [0.0, 0] for h in S.BID_MARKOUT_MIN}}
    ticks = {}
    for m, p in zip(months, paths):
        tr = S.read_pqbin(p)
        ticks[f"{m:%Y-%m}"] = int(len(tr["px"]))
        m_s = int(m.timestamp())
        m_n = int(((m + pd.offsets.MonthBegin(1)) - m).total_seconds())
        b, s, last = S.second_volumes(tr, m_s, m_n)
        off = m_s - start_s
        buy[off:off + m_n], sell[off:off + m_n] = b, s
        mb = S.minute_bars(tr, m_s * 1_000_000_000, m_n // 60)
        for k in bars:
            bars[k][off // 60: off // 60 + m_n // 60] = mb[k]
        for h, (qb, q) in S.passive_markout(tr, last, m_s).items():
            mk[h][0] += qb
            mk[h][1] += q
        rb = S.resting_bid(tr, mb, last, m_s * 1_000_000_000)
        bid["minutes"] += rb["minutes"]
        bid["fills"] += rb["fills"]
        for h in S.BID_MARKOUT_MIN:
            bid[h][0] += rb[h][0]
            bid[h][1] += rb[h][1]
        print(f"{m:%Y-%m}: {len(tr['px'])} ticks", flush=True)
        del tr
    bars = S.fill_gaps(bars)
    idx = pd.date_range(START, periods=n_min, freq="1min")
    regime = S8.daily_regime_1m(daily_closes(), 50, idx)
    a_trig = S9.a_trigger(pd.Series(bars["c"], index=idx), regime)
    bt, imb = S.vpin_buckets(buy, sell, start_s)
    vp = S.vpin_at(start_s + 60 * (np.arange(n_min) + 1), bt, imb)
    v1_trig = a_trig & (np.nan_to_num(vp, nan=0.0) > S.VPIN_MIN)
    arr = [bars[k] for k in ("o", "h", "l", "c")] + [idx.as_unit("ns").asi8]
    sched = S9.ratchet_schedule()
    split_ns = SPLIT.value

    def judge(trig):
        tr = S9.sim_short(*arr, trig, False, S9.A_HOLD, sched_bps=sched)
        disc, hold = tr[tr["entry_ns"] < split_ns], tr[tr["entry_ns"] >= split_ns]
        return {"discovery": S.summarize(disc), "holdout": S.summarize(hold),
                "holdout_stress": S.summarize(hold, S.STRESS_BPS), "discovery_stress": S.summarize(disc, S.STRESS_BPS)}

    v1 = judge(v1_trig)
    v1["pass"] = S.passes(v1["holdout"], v1["discovery"], v1["holdout_stress"])
    ok = ~np.isnan(vp)
    res = {
        "n_trials": N_TRIALS, "ticks": ticks, "bear_share_of_minutes": float((regime < 0).mean()),
        "vpin": {"buckets": int(len(imb)), "minutes_defined": int(ok.sum()),
                 "share_above_0.65": float((vp[ok] > S.VPIN_MIN).mean()),
                 "percentiles": {q: float(np.quantile(vp[ok], q / 100)) for q in (10, 50, 90, 99, 99.9)},
                 "a_trigger_minutes": int(a_trig.sum()), "v1_trigger_minutes": int(v1_trig.sum()),
                 "share_of_a_triggers_above_0.65": float(v1_trig.sum() / max(a_trig.sum(), 1))},
        "V1_vpin_filtered": v1,
        "A_taker_unfiltered_replication": judge(a_trig),
        "maker_markout_passive_bps": {f"{h}s": mk[h][0] / mk[h][1] for h in S.MARKOUT_S if mk[h][1] > 0},
        "resting_bid": {"minutes": bid["minutes"], "fills": bid["fills"],
                        "fill_rate": bid["fills"] / max(bid["minutes"], 1),
                        **{f"{h}m_move_bps": bid[h][0] / bid[h][1] for h in S.BID_MARKOUT_MIN if bid[h][1]},
                        **{f"{h}m_net_bps": bid[h][0] / bid[h][1] - S.BID_COST_BPS
                           for h in S.BID_MARKOUT_MIN if bid[h][1]}},
    }
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round21.json"), "w") as f:
        json.dump(res, f, indent=2, default=str)
    print(json.dumps(res, indent=1, default=str))


if __name__ == "__main__":
    main()
