"""Run PREREG17 once, on or after 2026-12-09: maker bid entries on recorded Coinbase ticks with confluence,
pre-fill defense and the breakeven/time-decay ladder. Writes results/round17.json.

Data-quality exclusions (no new quotes during them): minutes with a trade_id gap, minutes with a book gap
over 5 s (how a reconnect shows up in the recording), and 10-minute windows where the clock log has no OK check
or |offset| > 250 ms."""
from __future__ import annotations

import datetime as dt
import gzip
import json
import os
import sys

import numpy as np
import pandas as pd

import lab
import strategies17 as S

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 729
COLLECT = os.path.expanduser("~/piquant-collect")
START = pd.Timestamp("2026-11-12", tz="UTC")
SPLIT = pd.Timestamp("2026-11-25", tz="UTC")
END = pd.Timestamp("2026-12-09", tz="UTC")
PRODUCTS = ("BTC-USD", "ETH-USD")


def ms(ts: pd.Timestamp) -> int:
    return int(ts.timestamp() * 1000)


def read_days(kind: str, product: str) -> list[dict]:
    rows = []
    for day in pd.date_range(START, END - pd.Timedelta(days=1), freq="D"):
        p = os.path.join(COLLECT, kind, product, day.strftime("%Y-%m-%d") + ".jsonl.gz")
        if not os.path.exists(p):
            print(f"missing {p}")
            continue
        with gzip.open(p, "rt") as f:
            rows.extend(json.loads(line) for line in f if line.strip())
    return rows


def load_trades(product: str) -> pd.DataFrame:
    df = pd.DataFrame(read_days("ticks", product))
    df = df.drop_duplicates("id").sort_values(["t", "id"]).reset_index(drop=True)
    return df[(df["t"] >= ms(START)) & (df["t"] < ms(END))]


def load_books(product: str) -> pd.DataFrame:
    out = []
    for r in read_days("book", product):
        b, a = r.get("b") or [], r.get("a") or []
        if not b or not a:
            continue
        out.append((r["t"], b[0][0], b[0][1], sum(x[1] for x in b), sum(x[1] for x in a), (b[0][0] + a[0][0]) / 2))
    df = pd.DataFrame(out, columns=["t", "bid_px", "bid_sz", "bid_tot", "ask_tot", "mid"]).sort_values("t")
    return df[(df["t"] >= ms(START)) & (df["t"] < ms(END))].reset_index(drop=True)


def bad_minutes(trades: pd.DataFrame, books: pd.DataFrame) -> set[int]:
    bad = set()
    ids, t = trades["id"].to_numpy(), trades["t"].to_numpy()
    for k in np.flatnonzero(np.diff(ids) > 1):
        bad.update(range(t[k] // 60_000, t[k + 1] // 60_000 + 1))
    bt = books["t"].to_numpy()
    for k in np.flatnonzero(np.diff(bt) > 5000):
        bad.update(range(bt[k] // 60_000, bt[k + 1] // 60_000 + 1))
    return bad


def clock_ok_windows() -> set[int]:
    """10-minute windows (t // 600000) with at least one OK check and no bad one."""
    good, badw = set(), set()
    p = os.path.join(COLLECT, "clock_offsets.jsonl")
    if os.path.exists(p):
        with open(p) as f:
            for line in f:
                r = json.loads(line)
                w = ms(pd.Timestamp(r["at"])) // 600_000
                (good if r["ok"] and abs(r["offset_ms"]) <= 250 else badw).add(w)
    return good - badw


def trend_gate(product: str):
    d = lab.resample(lab.load_candles(product=product, since=None), "1D")["c"]
    up = (d > d.rolling(50, min_periods=50).mean()).shift(1).fillna(False)  # previous day's close vs its SMA50
    table = {k.strftime("%Y-%m-%d"): bool(v) for k, v in up.items()}
    return lambda t: table.get(pd.Timestamp(t, unit="ms", tz="UTC").strftime("%Y-%m-%d"), False)


def split(trades: list[dict]) -> tuple[list[dict], list[dict]]:
    disc = [x for x in trades if x["entry_t"] < ms(SPLIT)]
    hold = [x for x in trades if x["entry_t"] >= ms(SPLIT)]
    return disc, hold


def main():
    if dt.datetime.now(dt.timezone.utc) < END.to_pydatetime():
        sys.exit("PREREG17 runs once, on or after 2026-12-09 UTC")
    clock_ok = clock_ok_windows()
    data = {}
    for p in PRODUCTS:
        tr, bk = load_trades(p), load_books(p)
        bad = bad_minutes(tr, bk)
        gate = trend_gate(p)
        excl = (lambda bad: lambda t: (t // 60_000) in bad or (t // 600_000) not in clock_ok)(bad)
        bt = bk["t"].to_numpy(np.int64)
        tt, ts, tb = tr["t"].to_numpy(np.int64), tr["s"].to_numpy(float), (tr["side"] == "b").to_numpy()
        vp = S.vpin_at(bt, tt, ts, tb, S.bucket_volumes(tt, ts))
        data[p] = (tr, bk, gate, excl, vp, len(bad))
        print(f"{p}: {len(tr)} trades, {len(bk)} books, {len(bad)} bad minutes")
    res = {"n_trials": N_TRIALS, "excluded_minutes": {p: data[p][5] for p in PRODUCTS}, "variants": {}}
    for v in S.VARIANTS:
        out = {}
        for costs in (S.BASE, S.STRESS):
            runs = {p: S.simulate(p, data[p][0], data[p][1], v, costs, data[p][2], data[p][3], vpin=data[p][4])
                    for p in PRODUCTS}
            allt = sorted((x for r in runs.values() for x in r["trades"]), key=lambda x: x["entry_t"])
            disc, hold = split(allt)
            shadow = [x for r in runs.values() for x in r["shadow_trades"]]
            out[costs.name] = {"discovery": S.summarize(disc), "holdout": S.summarize(hold),
                               "quotes": sum(r["quotes"] for r in runs.values()),
                               "fills": len(allt), "defense_cancels": sum(r["defense_cancels"] for r in runs.values()),
                               "avoided_fills": S.summarize(shadow)}
        out["pass"] = S.passes(out["base"]["holdout"], out["base"]["discovery"], out["stress"]["holdout"])
        res["variants"][v] = out
        print(v, "PASS" if out["pass"] else "FAIL", json.dumps(out["base"]["holdout"]))
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round17.json"), "w") as f:
        json.dump(res, f, indent=2)


if __name__ == "__main__":
    main()
