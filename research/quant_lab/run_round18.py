"""Run PREREG18 once, on or after 2026-12-09, together with PREREG17: cross-venue lead-lag (L1, L2) and flow
momentum (F1) on recorded Coinbase and Kraken ticks. Writes results/round18.json.

Data-quality exclusions (no new entries during them), per coin: minutes with a Coinbase trade_id gap, minutes
with a book gap over 5 s on any venue the rule uses, and 10-minute windows without an OK clock check. Loading,
the window and the split are shared with run_round17."""
from __future__ import annotations

import datetime as dt
import json
import os
import sys

import numpy as np
import pandas as pd

import run_round17 as R
import strategies18 as S

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 732
COINS = {"BTC": ("BTC-USD", "kraken-BTC-USD"), "ETH": ("ETH-USD", "kraken-ETH-USD")}
DISC_DAYS = (R.SPLIT - R.START).days
HOLD_DAYS = (R.END - R.SPLIT).days


def load_books(product: str) -> pd.DataFrame:
    out = []
    for r in R.read_days("book", product):
        b, a = r.get("b") or [], r.get("a") or []
        if b and a:
            out.append((r["t"], b[0][0], a[0][0], (b[0][0] + a[0][0]) / 2))
    df = pd.DataFrame(out, columns=["t", "bid_px", "ask_px", "mid"]).drop_duplicates("t").sort_values("t")
    return df[(df["t"] >= R.ms(R.START)) & (df["t"] < R.ms(R.END))].reset_index(drop=True)


def book_gap_minutes(books: pd.DataFrame) -> set[int]:
    bad: set[int] = set()
    bt = books["t"].to_numpy()
    for k in np.flatnonzero(np.diff(bt) > 5000):
        bad.update(range(bt[k] // 60_000, bt[k + 1] // 60_000 + 1))
    return bad


def main():
    if dt.datetime.now(dt.timezone.utc) < R.END.to_pydatetime():
        sys.exit("PREREG18 runs once, on or after 2026-12-09 UTC")
    clock_ok = R.clock_ok_windows()
    sigs: dict = {r: {} for r in S.RULES}
    books: dict = {r: {} for r in S.RULES}
    excl: dict = {r: {} for r in S.RULES}
    leads: dict = {"L1": {}, "L2": {}}
    info: dict = {}
    for coin, (cb_p, kr_p) in COINS.items():
        tr, cb, kr = R.load_trades(cb_p), load_books(cb_p), load_books(kr_p)
        bad_cb = R.bad_minutes(tr, cb)                 # Coinbase trade_id gaps and book gaps
        bad_both = bad_cb | book_gap_minutes(kr)

        def make(bad):
            return lambda t: (t // 60_000) in bad or (t // 600_000) not in clock_ok
        ex_both, ex_cb = make(bad_both), make(bad_cb)
        cbt, cbm, krt, krm = (x.to_numpy() for x in (cb["t"], cb["mid"], kr["t"], kr["mid"]))
        cbt, krt = cbt.astype(np.int64), krt.astype(np.int64)
        sigs["L1"][coin] = S.lead_signals(cbt, cbm, krt, krm, long_only=True)
        sigs["L2"][coin] = S.lead_signals(krt, krm, cbt, cbm, long_only=False)
        sigs["F1"][coin] = S.flow_signals(tr["t"].to_numpy(np.int64), tr["s"].to_numpy(float),
                                          (tr["side"] == "b").to_numpy())
        books["L1"][coin], books["L2"][coin], books["F1"][coin] = kr, cb, cb
        excl["L1"][coin], excl["L2"][coin], excl["F1"][coin] = ex_both, ex_both, ex_cb
        leads["L1"][coin] = S.lead_profile(*sigs["L1"][coin], krt, krm, ex_both)
        leads["L2"][coin] = S.lead_profile(*sigs["L2"][coin], cbt, cbm, ex_both)
        info[coin] = {"trades": len(tr), "coinbase_books": len(cb), "kraken_books": len(kr),
                      "excluded_minutes_coinbase": len(bad_cb), "excluded_minutes_both": len(bad_both)}
        print(coin, json.dumps(info[coin]))
    res = {"n_trials": N_TRIALS, "data": info, "lead_measurements": leads, "rules": {}}
    for rule in S.RULES:
        hold_ms = S.FLOW_HOLD_MS if rule == "F1" else S.LAG_HOLD_MS
        out = {}
        for costs in S.COSTS[rule]:
            allt = sorted((x for coin in COINS for x in S.simulate(
                coin, *sigs[rule][coin], books[rule][coin], hold_ms, costs, excl[rule][coin])),
                key=lambda x: x["entry_t"])
            disc = [x for x in allt if x["signal_t"] < R.ms(R.SPLIT)]
            hold = [x for x in allt if x["signal_t"] >= R.ms(R.SPLIT)]
            out[costs.name] = {"discovery": S.summarize(disc, DISC_DAYS), "holdout": S.summarize(hold, HOLD_DAYS)}
        out["pass"] = S.passes(out["base"]["holdout"], out["base"]["discovery"], out["stress"]["holdout"])
        res["rules"][rule] = out
        print(rule, "PASS" if out["pass"] else "FAIL", json.dumps(out["base"]["holdout"]))
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round18.json"), "w") as f:
        json.dump(res, f, indent=2)


if __name__ == "__main__":
    main()
