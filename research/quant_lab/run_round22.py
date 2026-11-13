"""Run PREREG22 once: PREREG18's rules on Tardis.dev free days (1st of each month, 2023-01 to 2026-11).
Streams each file from datasets.tardis.dev (no key, nothing stored) and writes results/round22.json.

Usage: python run_round22.py [--out DIR] [--days N] [--synthetic]
  --synthetic runs the whole pipeline on generated data, without any network access (smoke test).
"""
from __future__ import annotations

import argparse
import json
import os
import zlib
from concurrent.futures import ProcessPoolExecutor

import numpy as np
import pandas as pd

import strategies18 as S
import strategies22 as T

N_TRIALS = 740
BASE = "https://datasets.tardis.dev/v1"
DAYS = [f"{y}-{m:02d}-01" for y in range(2023, 2027) for m in range(1, 13) if (y, m) <= (2026, 11)]
SPLIT = "2025-01-01"


def symbol(venue: str, coin: str, day: str) -> str:
    if venue == "coinbase":
        return f"{coin}-USD"
    if venue == "kraken":
        if coin == "BTC":
            return "XBT-USD" if day < "2026-07-10" else "BTC-USD"
        return "ETH-USD"
    return f"{coin}USD"


def url(venue: str, kind: str, coin: str, day: str) -> str:
    y, m, d = day.split("-")
    return f"{BASE}/{venue}/{kind}/{y}/{m}/{d}/{symbol(venue, coin, day)}.csv.gz"


def fetch_quotes(venue: str, coin: str, day: str, sym: str | None = None) -> tuple:
    u = url(venue, "quotes", coin, day)
    if sym:
        u = u.rsplit("/", 1)[0] + f"/{sym}.csv.gz"
    df = pd.read_csv(u, compression="gzip", usecols=["local_timestamp", "bid_price", "ask_price"])
    if df.empty:
        raise ValueError("empty quotes file")
    t = (df["local_timestamp"].to_numpy(np.int64) // 1000)
    o = np.argsort(t, kind="stable")
    return t[o], df["bid_price"].to_numpy(float)[o], df["ask_price"].to_numpy(float)[o]


def fetch_binance_us(coin: str, day: str) -> dict:
    """Binance.US feeds only the lead matrix. Its USD pairs are empty from mid-2023 (it lost USD banking), so
    fall back to the USDT pair; if both fail, the day runs without Binance.US (amendment, PREREG22)."""
    for sym in (f"{coin}USD", f"{coin}USDT"):
        try:
            return {"binance-us": fetch_quotes("binance-us", coin, day, sym)}
        except Exception as e:  # noqa: BLE001 - empty or missing file
            print(day, coin, "binance-us", sym, "unavailable:", repr(e)[:80], flush=True)
    return {}


def fetch_trades(coin: str, day: str) -> pd.DataFrame:
    df = pd.read_csv(url("coinbase", "trades", coin, day), compression="gzip",
                     usecols=["local_timestamp", "id", "side", "amount"])
    df["t"] = df["local_timestamp"].to_numpy(np.int64) // 1000
    return T.clean_trades(df)


def synthetic(venue: str, coin: str, day: str, seed: int) -> tuple:
    """A random walk per day; Kraken and Binance.US copy Coinbase with a 1.5 s and 0.8 s delay."""
    rng = np.random.default_rng(seed)
    d0 = T.day_start_ms(day)
    t = d0 + np.sort(rng.integers(0, T.DAY_MS, 200_000)).astype(np.int64)
    mid = 30_000 * np.exp(np.cumsum(rng.normal(0, 2e-4, len(t))))
    lag = {"coinbase": 0, "kraken": 1500, "binance-us": 800}[venue]
    return t + lag, mid * (1 - 1e-4), mid * (1 + 1e-4)


def synthetic_trades(day: str, seed: int) -> pd.DataFrame:
    rng = np.random.default_rng(seed + 7)
    d0 = T.day_start_ms(day)
    n = 100_000
    t = d0 + np.sort(rng.integers(0, T.DAY_MS, n)).astype(np.int64)
    return T.clean_trades(pd.DataFrame({"t": t, "id": np.arange(n), "side": rng.choice(["buy", "sell"], n),
                                        "amount": rng.exponential(0.1, n)}))


def one(args: tuple) -> tuple:
    day, coin, synth = args
    seed = zlib.crc32(f"{day} {coin}".encode())
    try:
        if synth:
            q = {v: synthetic(v, coin, day, seed) for v in T.VENUES}
            tr = synthetic_trades(day, seed)
        else:
            q = {v: fetch_quotes(v, coin, day) for v in ("coinbase", "kraken")}
            q.update(fetch_binance_us(coin, day))
            tr = fetch_trades(coin, day)
        res = T.run_day(day, coin, q, tr)
        print(day, coin, json.dumps(res["data"]), flush=True)
        return day, coin, res, None
    except Exception as e:  # a missing or broken file is recorded, not fatal
        print(day, coin, "ERROR", repr(e), flush=True)
        return day, coin, None, repr(e)


def main() -> None:
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=os.path.join(os.path.dirname(os.path.abspath(__file__)), "results"))
    ap.add_argument("--days", type=int, default=None, help="first N days only (smoke tests)")
    ap.add_argument("--synthetic", action="store_true")
    ap.add_argument("--workers", type=int, default=4)
    a = ap.parse_args()
    if a.days and not a.synthetic:
        ap.error("--days is for synthetic smoke tests only; the real run covers every day once")
    days = DAYS[: a.days] if a.days else DAYS
    jobs = [(d, c, a.synthetic) for d in days for c in ("BTC", "ETH")]
    with ProcessPoolExecutor(a.workers) as pool:
        got = list(pool.map(one, jobs))
    errors = {f"{d} {c}": e for d, c, r, e in got if e}
    ok = [(d, c, r) for d, c, r, e in got if r is not None]
    disc_days = len({d for d, _, _ in ok if d < SPLIT})
    hold_days = len({d for d, _, _ in ok if d >= SPLIT})
    res: dict = {"n_trials": N_TRIALS, "synthetic": a.synthetic, "days_run": len({d for d, _, _ in ok}),
                 "discovery_days": disc_days, "holdout_days": hold_days, "errors": errors,
                 "data": {f"{d} {c}": r["data"] for d, c, r in ok}, "lead_matrix": {}, "rules": {}}
    pairs = sorted({p for _, _, r in ok for p in r["leads"]})
    for coin in ("BTC", "ETH"):
        res["lead_matrix"][coin] = {p: T.pool_leads([r["leads"][p] for _, c, r in ok if c == coin
                                                     and p in r["leads"]]) for p in pairs}
    for rule in S.RULES:
        out: dict = {}
        for costs in S.COSTS[rule]:
            allt = sorted((x for _, _, r in ok for x in r["trades"][(rule, costs.name)]),
                          key=lambda x: x["entry_t"])
            disc = [x for x in allt if x["day"] < SPLIT]
            hold = [x for x in allt if x["day"] >= SPLIT]
            out[costs.name] = {"discovery": S.summarize(disc, disc_days), "holdout": S.summarize(hold, hold_days),
                               "by_year": {y: S.summarize([x for x in allt if x["day"][:4] == y],
                                                          len({d for d, _, _ in ok if d[:4] == y}))
                                           for y in sorted({x["day"][:4] for x in allt})}}
        out["pass"] = S.passes(out["base"]["holdout"], out["base"]["discovery"], out["stress"]["holdout"])
        res["rules"][rule] = out
        print(rule, "PASS" if out["pass"] else "FAIL", json.dumps(out["base"]["holdout"]), flush=True)
    os.makedirs(a.out, exist_ok=True)
    name = "round22_synthetic.json" if a.synthetic else "round22.json"
    with open(os.path.join(a.out, name), "w") as f:
        json.dump(res, f, indent=2)
    print("wrote", os.path.join(a.out, name), "errors:", len(errors))


if __name__ == "__main__":
    main()
