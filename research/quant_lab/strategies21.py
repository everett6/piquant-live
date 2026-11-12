"""Round-21 (PREREG21): the PREREG9 A-taker cascade short with a VPIN filter, on real Binance aggTrades
(.pqbin from `piquant import-ticks`), plus descriptive maker markouts. Execution and exits are strategies9's,
unchanged."""
from __future__ import annotations

import numpy as np
import pandas as pd

HEADER = 64
PRICE_SCALE = 1e9
QTY_SCALE = 1e8
REAL_MARK = ord("R")  # header byte 25, set by the real-tick importer
REC = np.dtype({"names": ["px", "qty", "ts", "fl"], "formats": ["<i8", "<i8", "<u8", "u1"],
                "offsets": [8, 16, 24, 39], "itemsize": 40})
VPIN_PER_DAY = 1600
VPIN_N = 50
VPIN_MIN = 0.65
STRESS_BPS = 6.0
MARKOUT_S = (1, 10, 60, 300, 1800)
BID_MARKOUT_MIN = (1, 5, 30)
BID_COST_BPS = 2.0 + 7.0


# ---------- reading ----------

def read_pqbin(path: str) -> dict[str, np.ndarray]:
    """Trades of one real .pqbin: px, qty (float), ts (ns, int64), buy (True = buy aggressor)."""
    with open(path, "rb") as f:
        hdr = f.read(HEADER)
    if len(hdr) < HEADER or hdr[25] != REAL_MARK:
        raise ValueError(f"{path}: not a real-tick pqbin (synthetic files must never be used)")
    rec = np.memmap(path, dtype=REC, mode="r", offset=HEADER)
    return {"px": rec["px"] / PRICE_SCALE, "qty": rec["qty"] / QTY_SCALE, "ts": rec["ts"].astype(np.int64),
            "buy": (rec["fl"] & 1) == 0}


def write_pqbin(path: str, px, qty, ts, buyer_maker, real: bool = True) -> None:
    """Test helper: the same layout the Go importer writes."""
    n = len(px)
    hdr = bytearray(HEADER)
    hdr[0:6] = b"PQBIN1"
    hdr[25] = REAL_MARK if real else 0
    rec = np.zeros(n, dtype=REC)
    rec["px"] = np.round(np.asarray(px) * PRICE_SCALE)
    rec["qty"] = np.round(np.asarray(qty) * QTY_SCALE)
    rec["ts"] = ts
    rec["fl"] = np.asarray(buyer_maker, dtype=np.uint8)
    with open(path, "wb") as f:
        f.write(bytes(hdr))
        f.write(rec.tobytes())


# ---------- aggregation ----------

def minute_bars(tr: dict, start_ns: int, n_min: int) -> dict[str, np.ndarray]:
    """1m OHLCV from trades on the grid start_ns + 60 s * k. Empty minutes carry the previous close, volume 0."""
    m = (tr["ts"] - start_ns) // 60_000_000_000
    keep = (m >= 0) & (m < n_min)
    m, px, q = m[keep], tr["px"][keep], tr["qty"][keep]
    o = np.full(n_min, np.nan)
    h, lo, c, v = o.copy(), o.copy(), o.copy(), np.zeros(n_min)
    if len(m):
        cut = np.flatnonzero(np.diff(m)) + 1
        first = np.concatenate([[0], cut])
        last = np.concatenate([cut, [len(m)]]) - 1
        mm = m[first]
        o[mm], c[mm] = px[first], px[last]
        h[mm], lo[mm] = np.maximum.reduceat(px, first), np.minimum.reduceat(px, first)
        v[mm] = np.add.reduceat(q, first)
    return {"o": o, "h": h, "l": lo, "c": c, "v": v}


def fill_gaps(b: dict[str, np.ndarray]) -> dict[str, np.ndarray]:
    c = pd.Series(b["c"]).ffill().to_numpy()
    out = dict(b)
    for k in ("o", "h", "l"):
        out[k] = np.where(np.isnan(b[k]), c, b[k])
    out["c"] = c
    return out


def second_volumes(tr: dict, start_s: int, n_s: int) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Per-second buy and sell aggressor volume, and the last trade price (NaN where none), on start_s + k."""
    s = tr["ts"] // 1_000_000_000 - start_s
    keep = (s >= 0) & (s < n_s)
    s, q, b, px = s[keep], tr["qty"][keep], tr["buy"][keep], tr["px"][keep]
    buy = np.bincount(s, weights=np.where(b, q, 0.0), minlength=n_s)
    sell = np.bincount(s, weights=np.where(b, 0.0, q), minlength=n_s)
    last = np.full(n_s, np.nan)
    last[s] = px  # trades are time-ordered, so the last write per second wins
    return buy, sell, last


# ---------- VPIN ----------

def vpin_buckets(buy: np.ndarray, sell: np.ndarray, start_s: int) -> tuple[np.ndarray, np.ndarray]:
    """Bucket completion times (unix s, end of the completing second) and imbalances |B - S| / V. Per UTC day,
    V = previous day's volume / VPIN_PER_DAY (first day: its own); buckets do not cross midnight. start_s must
    be a UTC midnight and len(buy) a whole number of days."""
    n_days = len(buy) // 86400
    tot_day = (buy + sell)[: n_days * 86400].reshape(n_days, 86400).sum(axis=1)
    times, imb = [], []
    prev = None
    for d in range(n_days):
        lo_, hi_ = d * 86400, (d + 1) * 86400
        base = prev if prev else tot_day[d]
        prev = tot_day[d] if tot_day[d] > 0 else prev
        if not base or base <= 0:
            continue
        v = base / VPIN_PER_DAY
        b, s = buy[lo_:hi_], sell[lo_:hi_]
        vol = b + s
        cum, cumb = np.cumsum(vol), np.cumsum(b)
        k = int(cum[-1] // v)
        if k == 0:
            continue
        x = v * np.arange(1, k + 1)
        idx = np.searchsorted(cum, x - 1e-12 * v, side="left")
        before = np.where(idx > 0, cum[np.maximum(idx - 1, 0)], 0.0)
        before_b = np.where(idx > 0, cumb[np.maximum(idx - 1, 0)], 0.0)
        f = np.divide(x - before, vol[idx], out=np.ones(k), where=vol[idx] > 0)
        bb = before_b + f * b[idx]
        db = np.diff(np.concatenate([[0.0], bb]))
        imb.append(np.abs(2 * db - v) / v)
        times.append(start_s + lo_ + idx + 1)
    if not times:
        return np.array([], dtype=np.int64), np.array([])
    return np.concatenate(times).astype(np.int64), np.concatenate(imb)


def vpin_at(t_s: np.ndarray, bucket_t: np.ndarray, bucket_imb: np.ndarray, n: int = VPIN_N) -> np.ndarray:
    """VPIN at each time: mean imbalance of the last n buckets completed at or before t (NaN before n exist)."""
    out = np.full(len(t_s), np.nan)
    if len(bucket_imb) < n:
        return out
    roll = np.convolve(bucket_imb, np.ones(n) / n, mode="valid")
    k = np.searchsorted(bucket_t, t_s, side="right") - 1
    ok = k >= n - 1
    out[ok] = roll[k[ok] - (n - 1)]
    return out


# ---------- descriptive maker measures ----------

def passive_markout(tr: dict, last_px: np.ndarray, start_s: int, horizons=MARKOUT_S) -> dict:
    """Volume-weighted mean signed move (bps) of the price h seconds after each trade, from the passive side:
    a sell aggressor filled a resting bid (+ move), a buy aggressor filled a resting ask (- move). Returns the
    weighted sums so months can be pooled: {h: [sum(q * bps), sum(q)]}."""
    px_f = pd.Series(last_px).ffill().to_numpy()
    s = tr["ts"] // 1_000_000_000 - start_s
    sign = np.where(tr["buy"], -1.0, 1.0)
    out = {}
    for h in horizons:
        j = s + h
        ok = (s >= 0) & (j < len(px_f))
        later = px_f[j[ok]]
        good = ~np.isnan(later)
        mv = sign[ok][good] * (later[good] / tr["px"][ok][good] - 1) * 1e4
        q = tr["qty"][ok][good]
        out[h] = [float((q * mv).sum()), float(q.sum())]
    return out


def resting_bid(tr: dict, bars: dict, last_px: np.ndarray, start_ns: int, horizons=BID_MARKOUT_MIN) -> dict:
    """At each whole minute a bid sits at the previous minute's close; it fills if a trade prints strictly below
    it within the minute. Returns counts and per-horizon sums of the move from the bid (bps) after the fill."""
    c, lo = bars["c"], bars["l"]
    n = len(c)
    px_f = pd.Series(last_px).ffill().to_numpy()
    start_s = start_ns // 1_000_000_000
    m_of = (tr["ts"] - start_ns) // 60_000_000_000
    out = {"minutes": 0, "fills": 0, **{h: [0.0, 0] for h in horizons}}
    for k in range(1, n):
        bid = c[k - 1]
        if np.isnan(bid):
            continue
        out["minutes"] += 1
        if not (lo[k] < bid):
            continue
        a, b = np.searchsorted(m_of, [k, k + 1])
        below = np.flatnonzero(tr["px"][a:b] < bid)
        if not len(below):
            continue
        out["fills"] += 1
        fs = int(tr["ts"][a + below[0]] // 1_000_000_000 - start_s)
        for h in horizons:
            j = fs + 60 * h
            if j < len(px_f) and not np.isnan(px_f[j]):
                out[h][0] += (px_f[j] / bid - 1) * 1e4
                out[h][1] += 1
    return out


# ---------- judging ----------

def summarize(tr: pd.DataFrame, stress_bps: float = 0.0) -> dict:
    if len(tr) == 0:
        return {"n": 0}
    net = tr["net"].to_numpy() * 1e4 - stress_bps
    gross = tr["gross"].to_numpy() * 1e4
    n = len(net)
    sd = net.std(ddof=1) if n > 1 else 0.0
    return {"n": n, "mean_net_bps": float(net.mean()), "mean_gross_bps": float(gross.mean()),
            "t": float(net.mean() / (sd / np.sqrt(n))) if sd > 0 else 0.0, "win_rate": float((net > 0).mean()),
            "worst_bps": float(net.min()), "exits": {k: int(v) for k, v in tr["reason"].value_counts().items()}}


def passes(holdout: dict, discovery: dict, holdout_stress: dict) -> bool:
    return (holdout.get("n", 0) >= 100 and holdout["mean_net_bps"] > 0 and holdout["t"] >= 2.0
            and discovery.get("n", 0) > 0 and discovery["mean_net_bps"] > 0
            and holdout_stress.get("n", 0) > 0 and holdout_stress["mean_net_bps"] > 0)
