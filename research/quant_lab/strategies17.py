"""Round-17 (PREREG17): maker bid entries on tick data with the owner's pre-fill defense, confluence filter and
breakeven/time-decay exit ladder. Pure functions over arrays; the runner loads the recorded ticks.

Inputs per coin:
- trades: DataFrame with t (ms), p, s, side ('b' buy aggressor / 's' sell aggressor), sorted by t;
- books: DataFrame with t (ms), bid_px, bid_sz (best level), bid_tot, ask_tot (top-10 sums), mid, sorted by t;
- gate: function(t_ms) -> bool, the daily trend gate;
- excluded: function(t_ms) -> bool, data-quality exclusions (no new quotes; open positions keep running).
"""
from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np
import pandas as pd

LATENCY_MS = 100
QUOTE_TTL_MS = 60_000
LOCK_TRIGGER_BPS = 15.0
BE_BPS = 9.0  # maker entry 2 + taker exit 6 + 1 slippage
TIME_STOP_MIN = 30.0
SLIP_BPS = 1.0
SCRATCH_BPS = 2.0
VARIANTS = ("K1", "K2", "K3")


@dataclass(frozen=True)
class Costs:
    name: str
    maker_bps: float
    taker_bps: float
    queue_credit: bool  # False (stress): fills need a trade strictly below our price


BASE = Costs("base", 2.0, 6.0, True)
STRESS = Costs("stress", 4.0, 6.0, False)


def target_bps(h: float) -> float:
    """Take-profit distance after h minutes held: 55 -> 30 at 10 -> 10 at 25, then flat."""
    if h <= 10:
        return 55 - 2.5 * h
    if h <= 25:
        return 30 - (20 / 15) * (h - 10)
    return 10.0


def stop_bps(h: float) -> float:
    """Stop distance (negative = below entry): -35 -> -25 at 10 -> breakeven (+9) at 25, then flat."""
    if h <= 10:
        return -35 + 1.0 * h
    if h <= 25:
        return -25 + (BE_BPS + 25) / 15 * (h - 10)
    return BE_BPS


# ---------- signals ----------

def obi(bid_tot: np.ndarray, ask_tot: np.ndarray) -> np.ndarray:
    tot = bid_tot + ask_tot
    return np.divide(bid_tot - ask_tot, tot, out=np.zeros_like(tot, dtype=float), where=tot > 0)


def ret5s_bps(book_t: np.ndarray, mid: np.ndarray) -> np.ndarray:
    """Mid change over the last 5 s: mid now vs the latest snapshot at or before t - 5000 ms (NaN if none)."""
    j = np.searchsorted(book_t, book_t - 5000, side="right") - 1
    out = np.full(len(mid), np.nan)
    ok = j >= 0
    out[ok] = (mid[ok] / mid[j[ok]] - 1) * 1e4
    return out


def flow5s(book_t: np.ndarray, tr_t: np.ndarray, tr_s: np.ndarray, tr_buy: np.ndarray) -> np.ndarray:
    """(buy - sell) / total aggressor volume over trades in (t - 5000, t]; 0 without trades."""
    signed = np.concatenate([[0.0], np.cumsum(np.where(tr_buy, tr_s, -tr_s))])
    total = np.concatenate([[0.0], np.cumsum(tr_s)])
    hi = np.searchsorted(tr_t, book_t, side="right")
    lo = np.searchsorted(tr_t, book_t - 5000, side="right")
    vol = total[hi] - total[lo]
    return np.divide(signed[hi] - signed[lo], vol, out=np.zeros(len(book_t)), where=vol > 0)


def vpin_at(book_t: np.ndarray, tr_t: np.ndarray, tr_s: np.ndarray, tr_buy: np.ndarray,
            bucket_of_day: dict[str, float], n: int = 50) -> np.ndarray:
    """VPIN at each snapshot: mean |buy - sell| / V over the last n completed volume buckets (NaN until n exist).
    bucket_of_day maps a UTC date to its bucket volume V (1/50 of the previous day's volume). Trades are split
    across bucket boundaries."""
    close_t, imb = [], []
    buy = sell = 0.0
    days = pd.to_datetime(tr_t, unit="ms", utc=True).strftime("%Y-%m-%d")
    for t, s, b, d in zip(tr_t, tr_s, tr_buy, days):
        v = bucket_of_day[d]
        rem = float(s)
        while rem > 0:
            take = min(rem, v - (buy + sell))
            if b:
                buy += take
            else:
                sell += take
            rem -= take
            if buy + sell >= v - 1e-12:
                close_t.append(t)
                imb.append(abs(buy - sell) / v)
                buy = sell = 0.0
    close_t, imb = np.asarray(close_t), np.asarray(imb)
    out = np.full(len(book_t), np.nan)
    if len(imb) < n:
        return out
    roll = np.convolve(imb, np.ones(n) / n, mode="valid")  # roll[k] = mean of buckets k..k+n-1
    k = np.searchsorted(close_t, book_t, side="right") - 1  # last bucket closed at or before t
    ok = k >= n - 1
    out[ok] = roll[k[ok] - (n - 1)]
    return out


def bucket_volumes(tr_t: np.ndarray, tr_s: np.ndarray) -> dict[str, float]:
    """1/50 of the previous UTC day's volume (the first day uses its own)."""
    days = pd.Series(tr_s, index=pd.to_datetime(tr_t, unit="ms", utc=True)).resample("1D").sum()
    out, prev = {}, None
    for d, v in days.items():
        base = prev if prev else v
        out[d.strftime("%Y-%m-%d")] = max(base, 1e-12) / 50
        prev = v if v > 0 else prev
    return out


# ---------- simulator ----------

@dataclass
class Quote:
    px: float
    queue: float
    live_at: int
    expire: int
    cancel_at: int | None = None
    reason: str = ""


@dataclass
class Position:
    entry: float
    fill_t: int
    peak_bps: float = 0.0


@dataclass
class Book:
    """One quote-then-position lifecycle machine. A shadow machine (K3's avoided fills) runs a K2 quote alone."""
    variant: str
    costs: Costs
    quote: Quote | None = None
    pos: Position | None = None
    trades: list = field(default_factory=list)
    quotes: int = 0
    defense_cancels: int = 0

    def on_trade(self, t: int, p: float, s: float, buy: bool, coin: str) -> None:
        q = self.quote
        if q is not None and self.pos is None:
            if q.cancel_at is not None and t >= q.cancel_at:
                self.quote = None
            elif t >= q.live_at:
                if p < q.px:
                    self._fill(t, q.px)
                elif self.costs.queue_credit and p == q.px and not buy:
                    q.queue -= s
                    if q.queue <= 0:
                        self._fill(t, q.px)
            return
        if self.pos is None:
            return
        ps = self.pos
        h = (t - ps.fill_t) / 60_000
        move = (p / ps.entry - 1) * 1e4
        ps.peak_bps = max(ps.peak_bps, move)
        stop = stop_bps(h)
        if ps.peak_bps >= LOCK_TRIGGER_BPS:
            stop = max(stop, BE_BPS)
        if h >= TIME_STOP_MIN:
            self._exit(t, p * (1 - SLIP_BPS * 1e-4), "time", taker=True, coin=coin)
        elif move <= stop:
            self._exit(t, p * (1 - SLIP_BPS * 1e-4), "breakeven" if stop >= BE_BPS else "stop", taker=True, coin=coin)
        elif move >= target_bps(h):
            self._exit(t, ps.entry * (1 + target_bps(h) * 1e-4), "target", taker=False, coin=coin)

    def _fill(self, t: int, px: float) -> None:
        self.quote = None
        self.pos = Position(entry=px, fill_t=t)

    def _exit(self, t: int, px: float, reason: str, taker: bool, coin: str) -> None:
        ps = self.pos
        gross = (px / ps.entry - 1) * 1e4
        fees = self.costs.maker_bps + (self.costs.taker_bps if taker else self.costs.maker_bps)
        self.trades.append({"coin": coin, "entry_t": ps.fill_t, "exit_t": t, "reason": reason,
                            "gross_bps": gross, "net_bps": gross - fees})
        self.pos = None

    def post(self, t: int, bid_px: float, bid_sz: float) -> None:
        self.quote = Quote(px=bid_px, queue=bid_sz, live_at=t + LATENCY_MS, expire=t + QUOTE_TTL_MS)
        self.quotes += 1

    def cancel(self, t: int, reason: str) -> None:
        if self.quote is not None and self.quote.cancel_at is None:
            self.quote.cancel_at = t + LATENCY_MS
            self.quote.reason = reason


def entry_ok(variant: str, gate: bool, obi_v: float, vpin_v: float) -> bool:
    if not gate:
        return False
    if variant == "K1":
        return True
    return obi_v > 0.25 and not np.isnan(vpin_v) and vpin_v < 0.50


def defense_hit(r5: float, f5: float) -> bool:
    return (not np.isnan(r5) and r5 < -3.0) or f5 < -0.60


def simulate(coin: str, trades: pd.DataFrame, books: pd.DataFrame, variant: str, costs: Costs, gate, excluded,
             vpin: np.ndarray | None = None) -> dict:
    """Replay one coin. Returns trades, quote count, defense cancels and K3's shadow (avoided) trades."""
    tr_t = trades["t"].to_numpy(np.int64)
    tr_p = trades["p"].to_numpy(float)
    tr_s = trades["s"].to_numpy(float)
    tr_buy = (trades["side"] == "b").to_numpy()
    bt = books["t"].to_numpy(np.int64)
    ob = obi(books["bid_tot"].to_numpy(float), books["ask_tot"].to_numpy(float))
    r5 = ret5s_bps(bt, books["mid"].to_numpy(float))
    f5 = flow5s(bt, tr_t, tr_s, tr_buy)
    if vpin is None:
        vpin = vpin_at(bt, tr_t, tr_s, tr_buy, bucket_volumes(tr_t, tr_s)) if variant != "K1" else \
            np.full(len(bt), np.nan)
    bid_px = books["bid_px"].to_numpy(float)
    bid_sz = books["bid_sz"].to_numpy(float)

    main = Book(variant, costs)
    shadows: list[Book] = []
    done_shadows: list[Book] = []
    ti = 0
    for i in range(len(bt)):
        t = int(bt[i])
        while ti < len(tr_t) and tr_t[ti] <= t:
            for bk in [main, *shadows]:
                bk.on_trade(int(tr_t[ti]), tr_p[ti], tr_s[ti], bool(tr_buy[ti]), coin)
            ti += 1
        still = []
        for sh in shadows:
            if sh.quote is not None and sh.quote.cancel_at is not None and t >= sh.quote.cancel_at:
                sh.quote = None
            if sh.pos is None and sh.quote is None:
                done_shadows.append(sh)
                continue
            if sh.quote is not None and sh.pos is None and (t >= sh.quote.expire or
                                                            not entry_ok("K2", gate(t), ob[i], vpin[i])):
                sh.cancel(t, "k2")
            still.append(sh)
        shadows = still
        if main.pos is not None:
            continue
        q = main.quote
        if q is not None and q.cancel_at is not None and t >= q.cancel_at:
            main.quote = q = None
        if q is not None:
            if q.cancel_at is not None:
                continue
            if t >= q.expire:
                main.cancel(t, "timeout")
            elif variant != "K1" and not entry_ok(variant, gate(t), ob[i], vpin[i]):
                main.cancel(t, "condition")
            elif variant == "K3" and defense_hit(r5[i], f5[i]):
                main.cancel(t, "defense")
                main.defense_cancels += 1
                sh = Book("K2", costs, quote=Quote(q.px, q.queue, q.live_at, q.expire))
                shadows.append(sh)
            continue
        if excluded(t) or bid_px[i] <= 0 or np.isnan(bid_px[i]):
            continue
        if entry_ok(variant, gate(t), ob[i], vpin[i]) and not (variant == "K3" and defense_hit(r5[i], f5[i])):
            main.post(t, bid_px[i], bid_sz[i])
    shadow_trades = [x for sh in done_shadows + shadows for x in sh.trades]
    return {"trades": main.trades, "quotes": main.quotes, "defense_cancels": main.defense_cancels,
            "shadow_trades": shadow_trades}


def summarize(trades: list[dict]) -> dict:
    if not trades:
        return {"n": 0}
    net = np.array([x["net_bps"] for x in trades])
    gross = np.array([x["gross_bps"] for x in trades])
    n = len(net)
    sd = net.std(ddof=1) if n > 1 else 0.0
    reasons: dict[str, int] = {}
    for x in trades:
        reasons[x["reason"]] = reasons.get(x["reason"], 0) + 1
    return {"n": n, "mean_net_bps": float(net.mean()), "mean_gross_bps": float(gross.mean()),
            "t": float(net.mean() / (sd / np.sqrt(n))) if sd > 0 else 0.0,
            "win_rate": float((net >= SCRATCH_BPS).mean()), "scratch_rate": float((np.abs(net) < SCRATCH_BPS).mean()),
            "loss_rate": float((net <= -SCRATCH_BPS).mean()), "worst_bps": float(net.min()), "reasons": reasons}


def passes(holdout: dict, discovery: dict, holdout_stress: dict) -> bool:
    return (holdout.get("n", 0) >= 200 and holdout["mean_net_bps"] > 0 and holdout["t"] >= 2.0
            and discovery.get("n", 0) > 0 and discovery["mean_net_bps"] > 0
            and holdout_stress.get("n", 0) > 0 and holdout_stress["mean_net_bps"] > 0)
