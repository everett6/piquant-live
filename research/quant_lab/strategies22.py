"""Round-22 (PREREG22): PREREG18's rules (strategies18, unchanged) on Tardis.dev free days. This module only turns
Tardis quotes and trades into the inputs strategies18 expects, builds the exclusions, and runs one day.

Choices the pre-registration leaves open, fixed here before any real data is read:
- the 1 s grid runs from 00:00:00 to 23:59:59 of the day (86 400 points); a grid point gets the latest quote at
  or before it, and no snapshot if there is none yet or if bid <= 0 or bid >= ask;
- a quote gap is any stretch over 60 s without an update, including from 00:00 to the first quote and from the
  last quote to 24:00; every minute it touches is excluded;
- a Coinbase trade-ID gap excludes the minutes from the trade before the gap to the trade after it (as
  run_round17.bad_minutes); duplicate IDs are dropped first;
- lead-matrix moves are pooled across days and coins as trade-count-weighted means of strategies18.lead_profile.
"""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies18 as S

DAY_MS = 86_400_000
QUOTE_GAP_MS = 60_000
VENUES = ("coinbase", "kraken", "binance-us")


def day_start_ms(day: str) -> int:
    return int(pd.Timestamp(day, tz="UTC").value // 1_000_000)


def quotes_to_1s(t: np.ndarray, bid: np.ndarray, ask: np.ndarray, day0: int) -> pd.DataFrame:
    """1 s snapshots (t, bid_px, ask_px, mid) from change-only quotes sorted by t (ms)."""
    grid = day0 + 1000 * np.arange(DAY_MS // 1000, dtype=np.int64)
    j = np.searchsorted(t, grid, side="right") - 1
    ok = j >= 0
    jj = np.maximum(j, 0)
    b, a = bid[jj], ask[jj]
    ok &= (b > 0) & (a > b)
    df = pd.DataFrame({"t": grid[ok], "bid_px": b[ok], "ask_px": a[ok]})
    df["mid"] = (df["bid_px"] + df["ask_px"]) / 2
    return df.reset_index(drop=True)


def quote_gap_minutes(t: np.ndarray, day0: int) -> set[int]:
    edges = np.concatenate([[day0], t[(t >= day0) & (t < day0 + DAY_MS)], [day0 + DAY_MS]])
    bad: set[int] = set()
    for k in np.flatnonzero(np.diff(edges) > QUOTE_GAP_MS):
        bad.update(range(int(edges[k]) // 60_000, int(edges[k + 1] - 1) // 60_000 + 1))
    return bad


def trade_id_gap_minutes(ids: np.ndarray, t: np.ndarray) -> set[int]:
    bad: set[int] = set()
    for k in np.flatnonzero(np.diff(ids) > 1):
        bad.update(range(int(t[k]) // 60_000, int(t[k + 1]) // 60_000 + 1))
    return bad


def clean_trades(tr: pd.DataFrame) -> pd.DataFrame:
    """tr has t (ms), id, side ('buy'/'sell'), amount. Returns t, s, side ('b'/'s'), id sorted by id."""
    tr = tr.dropna(subset=["id"]).drop_duplicates("id").copy()
    tr["id"] = tr["id"].astype(np.int64)
    tr = tr.sort_values("id")
    out = pd.DataFrame({"t": tr["t"].astype(np.int64).to_numpy(), "s": tr["amount"].astype(float).to_numpy(),
                        "side": np.where(tr["side"].to_numpy() == "buy", "b", "s"),
                        "id": tr["id"].to_numpy()})
    return out


def _arr(b: pd.DataFrame):
    return b["t"].to_numpy(np.int64), b["mid"].to_numpy(float)


def run_day(day: str, coin: str, quotes: dict[str, tuple], trades: pd.DataFrame) -> dict:
    """quotes: venue -> (t, bid, ask) raw arrays sorted by t (ms). trades: Coinbase, as clean_trades returns.
    Returns per-rule trade lists (base and stress), per-pair lead profiles and data counts for one day."""
    day0 = day_start_ms(day)
    books = {v: quotes_to_1s(*quotes[v], day0) for v in VENUES if v in quotes}
    gaps = {v: quote_gap_minutes(quotes[v][0], day0) for v in books}
    tr = trades[(trades["t"] >= day0) & (trades["t"] < day0 + DAY_MS)].reset_index(drop=True)
    id_gaps = trade_id_gap_minutes(tr["id"].to_numpy(), tr["t"].to_numpy()) if len(tr) else set()

    def ex(bad):
        return lambda t: (t // 60_000) in bad

    bad_ck = gaps["coinbase"] | gaps["kraken"]
    ex_ck, ex_f = ex(bad_ck), ex(gaps["coinbase"] | id_gaps)
    cbt, cbm = _arr(books["coinbase"])
    krt, krm = _arr(books["kraken"])
    sigs = {"L1": S.lead_signals(cbt, cbm, krt, krm, long_only=True),
            "L2": S.lead_signals(krt, krm, cbt, cbm, long_only=False),
            "F1": S.flow_signals(tr["t"].to_numpy(np.int64), tr["s"].to_numpy(float),
                                 (tr["side"] == "b").to_numpy())}
    traded = {"L1": books["kraken"], "L2": books["coinbase"], "F1": books["coinbase"]}
    excl = {"L1": ex_ck, "L2": ex_ck, "F1": ex_f}
    out: dict = {"trades": {}, "leads": {}, "data": {}}
    for rule in S.RULES:
        hold = S.FLOW_HOLD_MS if rule == "F1" else S.LAG_HOLD_MS
        for costs in S.COSTS[rule]:
            ts = S.simulate(coin, *sigs[rule], traded[rule], hold, costs, excl[rule])
            for x in ts:
                x["day"] = day
            out["trades"][(rule, costs.name)] = ts
    for lead in books:
        for lag in books:
            if lead == lag:
                continue
            lt, lm = _arr(books[lead])
            gt, gm = _arr(books[lag])
            st, sd = S.lead_signals(lt, lm, gt, gm, long_only=False)
            out["leads"][f"{lead}->{lag}"] = S.lead_profile(st, sd, gt, gm, ex(gaps[lead] | gaps[lag]))
    out["data"] = {"trades": int(len(tr)), "id_gap_minutes": len(id_gaps),
                   **{f"{v}_snapshots": int(len(b)) for v, b in books.items()},
                   **{f"{v}_gap_minutes": len(g) for v, g in gaps.items()}}
    return out


def pool_leads(profiles: list[dict]) -> dict:
    """Trade-count-weighted pooling of strategies18.lead_profile outputs."""
    out: dict = {"signals": int(sum(p["signals"] for p in profiles))}
    for h in S.LEAD_HORIZONS_S:
        k = f"{h}s"
        n = sum(p[k]["n"] for p in profiles)
        s = sum(p[k]["n"] * p[k]["mean_bps"] for p in profiles if p[k]["n"])
        out[k] = {"n": int(n), "mean_bps": float(s / n) if n else None}
    return out
