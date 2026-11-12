"""Round-18 (PREREG18): cross-venue lead-lag (L1 Coinbase -> Kraken, L2 Kraken -> Coinbase) and 60 s trade-flow
momentum (F1). Pure functions over arrays; the runner loads the recorded ticks.

Inputs per coin:
- books: DataFrame with t (ms), bid_px, ask_px, mid, sorted by t (1 s snapshots of one venue);
- trades (F1): DataFrame with t (ms), s, side ('b' buy aggressor / 's' sell aggressor), sorted by t;
- excluded: function(t_ms) -> bool, data-quality exclusions (no new entries; open trades still exit).

Choices the pre-registration leaves open, fixed here before any real data is read:
- "1 s earlier" is the latest snapshot at or before t - 1000 ms, and it must be no older than t - 2000 ms;
- the other venue's move over "the same second" uses its latest snapshots at or before t and t - 1000 ms, each
  at most 2000 ms stale;
- an entry is skipped when the traded venue has no snapshot within 5 s of the fill time (a book gap); an exit
  takes the first snapshot at or after its fill time, however late; a trade with no exit snapshot is dropped;
- a trade is open from its decision until its exit fill;
- F1's minute ending at m covers trades in [m - 60 s, m); the daily median counts every minute of the day,
  including minutes without trades;
- the lead measurement covers every non-excluded signal, traded or not;
- a win is net > 0.
"""
from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd

FILL_DELAY_MS = 200
ENTRY_MAX_WAIT_MS = 5000
STALE_MS = 2000
LEAD_BPS = 5.0
LAG_HOLD_MS = 30_000
FLOW_MIN = 0.50
FLOW_HOLD_MS = 300_000
LEAD_HORIZONS_S = (1, 5, 30)
RULES = ("L1", "L2", "F1")


@dataclass(frozen=True)
class Costs:
    name: str
    side_bps: float  # fee plus slippage allowance per side; the spread is paid through the fills


COSTS = {"L1": (Costs("base", 40.0), Costs("stress", 45.0)),
         "L2": (Costs("base", 7.0), Costs("stress", 9.0)),
         "F1": (Costs("base", 7.0), Costs("stress", 9.0))}
LONG_ONLY = {"L1": True, "L2": False, "F1": False}


# ---------- signals ----------

def asof(t: np.ndarray, at: np.ndarray, max_stale: int | None = STALE_MS) -> np.ndarray:
    """Index of the latest element of t that is <= at (and within max_stale of it); -1 if none."""
    j = np.searchsorted(t, at, side="right") - 1
    if max_stale is not None:
        j = np.where((j >= 0) & (at - t[np.maximum(j, 0)] <= max_stale), j, -1)
    return j


def move_1s_bps(t: np.ndarray, mid: np.ndarray, at: np.ndarray) -> np.ndarray:
    """Mid change in bps from at - 1000 ms to at, using the latest snapshots at or before each (NaN if stale)."""
    a, b = asof(t, at), asof(t, at - 1000)
    out = np.full(len(at), np.nan)
    ok = (a >= 0) & (b >= 0)
    out[ok] = (mid[a[ok]] / mid[b[ok]] - 1) * 1e4
    return out


def lead_signals(lead_t: np.ndarray, lead_mid: np.ndarray, lag_t: np.ndarray, lag_mid: np.ndarray,
                 long_only: bool) -> tuple[np.ndarray, np.ndarray]:
    """Decision times (the leader's snapshot times) and directions (+1 / -1) where the leader moved >= 5 bps in
    1 s and the lagger moved less than half as much in the same direction."""
    lead = move_1s_bps(lead_t, lead_mid, lead_t)
    lag = move_1s_bps(lag_t, lag_mid, lead_t)
    d = np.sign(lead)
    with np.errstate(invalid="ignore"):
        hit = (np.abs(lead) >= LEAD_BPS) & (lag * d < np.abs(lead) / 2)
        if long_only:
            hit &= d > 0
    return lead_t[hit], d[hit].astype(int)


def lead_profile(sig_t: np.ndarray, sig_d: np.ndarray, lag_t: np.ndarray, lag_mid: np.ndarray, excluded) -> dict:
    """Mean signed move (bps) of the lagging venue's mid 1, 5 and 30 s after each non-excluded signal."""
    keep = np.array([not excluded(int(t)) for t in sig_t], dtype=bool)
    t, d = sig_t[keep], sig_d[keep]
    out: dict = {"signals": int(len(t))}
    a = asof(lag_t, t)
    for h in LEAD_HORIZONS_S:
        b = asof(lag_t, t + h * 1000)
        ok = (a >= 0) & (b >= 0)
        mv = d[ok] * (lag_mid[b[ok]] / lag_mid[a[ok]] - 1) * 1e4
        out[f"{h}s"] = {"n": int(ok.sum()), "mean_bps": float(mv.mean()) if ok.any() else None}
    return out


def minute_volume_floor(tr_t: np.ndarray, tr_s: np.ndarray) -> dict[str, float]:
    """UTC date -> median 1-minute volume of the previous UTC day (the first day uses its own). Every minute of a
    day counts, including minutes without trades."""
    if len(tr_t) == 0:
        return {}
    vol = pd.Series(tr_s, index=pd.to_datetime(tr_t, unit="ms", utc=True)).resample("1min").sum()
    first, last = vol.index[0].floor("D"), vol.index[-1].floor("D")
    vol = vol.reindex(pd.date_range(first, last + pd.Timedelta(minutes=1439), freq="1min"), fill_value=0.0)
    med = vol.resample("1D").median()
    out, prev = {}, None
    for day, m in med.items():
        out[day.strftime("%Y-%m-%d")] = float(m if prev is None else prev)
        prev = m
    return out


def flow_signals(tr_t: np.ndarray, tr_s: np.ndarray, tr_buy: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Decision times (whole minutes, ms) and directions where |flow60| >= 0.50 and the minute's volume is at
    least the previous day's median 1-minute volume."""
    if len(tr_t) == 0:
        return np.array([], dtype=np.int64), np.array([], dtype=int)
    floor = minute_volume_floor(tr_t, tr_s)
    m = np.arange((tr_t[0] // 60_000 + 1) * 60_000, tr_t[-1] + 60_000, 60_000, dtype=np.int64)
    signed = np.concatenate([[0.0], np.cumsum(np.where(tr_buy, tr_s, -tr_s))])
    total = np.concatenate([[0.0], np.cumsum(tr_s)])
    hi, lo = np.searchsorted(tr_t, m, side="left"), np.searchsorted(tr_t, m - 60_000, side="left")
    vol = total[hi] - total[lo]
    flow = np.divide(signed[hi] - signed[lo], vol, out=np.zeros(len(m)), where=vol > 0)
    days = pd.to_datetime(m - 1, unit="ms", utc=True).strftime("%Y-%m-%d")  # the day the minute lies in
    need = np.array([floor.get(d, np.inf) for d in days])
    hit = (np.abs(flow) >= FLOW_MIN) & (vol > 0) & (vol >= need)
    return m[hit], np.sign(flow[hit]).astype(int)


# ---------- simulator ----------

def simulate(coin: str, sig_t: np.ndarray, sig_d: np.ndarray, books: pd.DataFrame, hold_ms: int, costs: Costs,
             excluded) -> list[dict]:
    """Take each signal in time order on the traded venue's book: enter at the first snapshot >= t + 200 ms
    (buy at the ask, sell at the bid), exit at the first snapshot >= t + hold + 200 ms. One trade at a time."""
    bt = books["t"].to_numpy(np.int64)
    bid, ask, mid = (books[c].to_numpy(float) for c in ("bid_px", "ask_px", "mid"))
    trades: list[dict] = []
    free_at = -1
    for t, d in zip(sig_t.tolist(), sig_d.tolist()):
        if t < free_at or excluded(t):
            continue
        i = int(np.searchsorted(bt, t + FILL_DELAY_MS, side="left"))
        if i >= len(bt) or bt[i] - (t + FILL_DELAY_MS) > ENTRY_MAX_WAIT_MS:
            continue
        j = int(np.searchsorted(bt, t + hold_ms + FILL_DELAY_MS, side="left"))
        if j >= len(bt):
            continue
        if not (bid[i] > 0 and ask[i] > 0 and bid[j] > 0 and ask[j] > 0):
            continue
        if d > 0:
            fill = (bid[j] / ask[i] - 1) * 1e4
        else:
            fill = (bid[i] - ask[j]) / bid[i] * 1e4
        gross = d * (mid[j] / mid[i] - 1) * 1e4
        trades.append({"coin": coin, "signal_t": t, "entry_t": int(bt[i]), "exit_t": int(bt[j]), "dir": d,
                       "gross_bps": float(gross), "spread_bps": float(gross - fill),
                       "net_bps": float(fill - 2 * costs.side_bps)})
        free_at = int(bt[j])
    return trades


def summarize(trades: list[dict], days: float) -> dict:
    if not trades:
        return {"n": 0}
    net = np.array([x["net_bps"] for x in trades])
    gross = np.array([x["gross_bps"] for x in trades])
    spread = np.array([x["spread_bps"] for x in trades])
    n = len(net)
    sd = net.std(ddof=1) if n > 1 else 0.0
    return {"n": n, "mean_net_bps": float(net.mean()), "mean_gross_bps": float(gross.mean()),
            "mean_spread_bps": float(spread.mean()),
            "spread_share_of_gross": float(spread.mean() / gross.mean()) if gross.mean() > 0 else None,
            "t": float(net.mean() / (sd / np.sqrt(n))) if sd > 0 else 0.0,
            "win_rate": float((net > 0).mean()), "trades_per_day": float(n / days) if days > 0 else None,
            "worst_bps": float(net.min())}


def passes(holdout: dict, discovery: dict, holdout_stress: dict) -> bool:
    return (holdout.get("n", 0) >= 200 and holdout["mean_net_bps"] > 0 and holdout["t"] >= 2.0
            and discovery.get("n", 0) > 0 and discovery["mean_net_bps"] > 0
            and holdout_stress.get("n", 0) > 0 and holdout_stress["mean_net_bps"] > 0)
