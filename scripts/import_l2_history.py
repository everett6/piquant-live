#!/usr/bin/env python3
"""Download and convert historical L2 order books and trades into piquant-collect JSONL.gz format.

Downloads Bybit public spot daily archives:
  - Order book (ob200 snapshot + deltas): https://quote-saver.bycsi.com/orderbook/spot/<SYM>/<DATE>_<SYM>_ob200.data.zip
  - Trades:                               https://public.bybit.com/spot/<SYM>/<SYM>_<DATE>.csv.gz

Writes into a separate target directory (default ~/piquant-collect/historical_l2) so live
PREREG17/PREREG18 recordings under ~/piquant-collect/{book,ticks} are never touched or mixed.
"""
from __future__ import annotations

import argparse
import csv
import datetime as dt
import gzip
import http.client
import io
import json
import os
import shutil
import sys
import tempfile
import time
import urllib.error
import urllib.request
import zipfile
from typing import Iterable, TextIO

BYBIT_OB_URL = "https://quote-saver.bycsi.com/orderbook/spot/{sym}/{day}_{sym}_ob200.data.zip"
BYBIT_TRADES_URL = "https://public.bybit.com/spot/{sym}/{sym}_{day}.csv.gz"


def free_gb(path: str) -> float:
    os.makedirs(path, exist_ok=True)
    return shutil.disk_usage(path).free / 1e9


def iter_days(start_day: str, end_day: str) -> list[str]:
    a = dt.date.fromisoformat(start_day)
    b = dt.date.fromisoformat(end_day)
    if b < a:
        raise ValueError(f"end {end_day} before start {start_day}")
    out = []
    cur = a
    while cur <= b:
        out.append(cur.isoformat())
        cur += dt.timedelta(days=1)
    return out


class L2BookState:
    """Reconstructs Bybit L2 snapshot + delta messages with exact level deletion and causal sampling."""

    def __init__(self, depth: int = 10) -> None:
        self.depth = depth
        self.bids: dict[float, float] = {}
        self.asks: dict[float, float] = {}
        self.ready = False
        self.upd = 0

    def apply_message(self, msg: dict) -> int:
        ts = int(msg["ts"])
        mtype = msg.get("type")
        data = msg.get("data", {})
        if mtype == "snapshot":
            self.bids.clear()
            self.asks.clear()
            for p_s, sz_s in data.get("b", ()):
                sz = float(sz_s)
                if sz > 0:
                    self.bids[float(p_s)] = sz
            for p_s, sz_s in data.get("a", ()):
                sz = float(sz_s)
                if sz > 0:
                    self.asks[float(p_s)] = sz
            self.ready = True
            self.upd += len(data.get("b", ())) + len(data.get("a", ()))
        elif mtype == "delta" and self.ready:
            bids_delta = data.get("b", ())
            asks_delta = data.get("a", ())
            for p_s, sz_s in bids_delta:
                p, sz = float(p_s), float(sz_s)
                if sz <= 0:
                    self.bids.pop(p, None)
                else:
                    self.bids[p] = sz
            for p_s, sz_s in asks_delta:
                p, sz = float(p_s), float(sz_s)
                if sz <= 0:
                    self.asks.pop(p, None)
                else:
                    self.asks[p] = sz
            self.upd += len(bids_delta) + len(asks_delta)
        return ts

    def top_n_row(self, ts_ms: int) -> dict | None:
        if not self.ready or not self.bids or not self.asks:
            return None
        top_b = sorted(self.bids.items(), key=lambda x: -x[0])[: self.depth]
        top_a = sorted(self.asks.items(), key=lambda x: x[0])[: self.depth]
        if not top_b or not top_a or top_b[0][0] >= top_a[0][0]:
            return None
        row = {
            "t": ts_ms,
            "b": [[p, s] for p, s in top_b],
            "a": [[p, s] for p, s in top_a],
            "upd": self.upd,
        }
        self.upd = 0
        return row


def convert_bybit_ob_lines(lines: Iterable[str], out_gz_path: str, sample_ms: int = 1000, depth: int = 10) -> int:
    """Convert raw Bybit ob200 JSON lines into piquant BookRow .jsonl.gz causally sampled every sample_ms."""
    os.makedirs(os.path.dirname(out_gz_path), exist_ok=True)
    tmp_path = out_gz_path + ".tmp"
    book = L2BookState(depth=depth)
    next_snap: int | None = None
    written = 0

    with gzip.open(tmp_path, "wt", encoding="utf-8", compresslevel=5) as out:
        for line in lines:
            line = line.strip()
            if not line:
                continue
            msg = json.loads(line)
            msg_ts = int(msg["ts"])

            if next_snap is None:
                # Align first snapshot strictly at or after the first message boundary
                next_snap = ((msg_ts + sample_ms - 1) // sample_ms) * sample_ms

            # Emit causal snapshots for boundaries passed BEFORE applying the newer message
            while msg_ts > next_snap:
                row = book.top_n_row(next_snap)
                if row is not None:
                    out.write(json.dumps(row, separators=(",", ":")) + "\n")
                    written += 1
                next_snap += sample_ms

            book.apply_message(msg)

            if msg_ts == next_snap:
                row = book.top_n_row(next_snap)
                if row is not None:
                    out.write(json.dumps(row, separators=(",", ":")) + "\n")
                    written += 1
                next_snap += sample_ms

        # Emit final snapshot at the last boundary if book was updated after previous snap
        if next_snap is not None and book.ready and book.upd > 0:
            row = book.top_n_row(next_snap)
            if row is not None:
                out.write(json.dumps(row, separators=(",", ":")) + "\n")
                written += 1

    os.replace(tmp_path, out_gz_path)
    return written


def convert_bybit_trade_rows(csv_stream: TextIO, out_gz_path: str) -> int:
    """Convert raw Bybit spot trades CSV into piquant TradeRow .jsonl.gz."""
    os.makedirs(os.path.dirname(out_gz_path), exist_ok=True)
    tmp_path = out_gz_path + ".tmp"
    reader = csv.DictReader(csv_stream)
    written = 0
    with gzip.open(tmp_path, "wt", encoding="utf-8", compresslevel=5) as out:
        for r in reader:
            ts_raw = r.get("timestamp") or r.get("time") or ""
            if not ts_raw:
                continue
            ts_val = int(float(ts_raw))
            if ts_val < 1_000_000_000_000:
                ts_val *= 1000
            side_raw = (r.get("side") or "").strip().lower()
            side = "b" if side_raw.startswith("b") else "s"
            row = {
                "t": ts_val,
                "p": float(r["price"]),
                "s": float(r.get("volume") or r.get("size") or r.get("qty")),
                "side": side,
                "id": int(r.get("id") or r.get("trdMatchID") or 0),
            }
            out.write(json.dumps(row, separators=(",", ":")) + "\n")
            written += 1
    os.replace(tmp_path, out_gz_path)
    return written


def _is_retryable(exc: Exception) -> bool:
    if isinstance(exc, urllib.error.HTTPError):
        return exc.code in (429, 500, 502, 503, 504)
    return isinstance(exc, (urllib.error.URLError, TimeoutError, http.client.HTTPException, OSError))


def download_to_temp(
    url: str,
    timeout: int = 120,
    retries: int = 4,
    retry_delay: float = 3.0,
    sleep_fn=time.sleep,
) -> str:
    """Download URL to a temp file, retrying transient DNS/network/5xx failures with exponential backoff."""
    attempts = max(1, retries + 1)
    for attempt in range(attempts):
        fd, tmp_path = tempfile.mkstemp(prefix="l2dl_")
        os.close(fd)
        try:
            req = urllib.request.Request(url, headers={"User-Agent": "piquant-l2-importer/1.0"})
            with urllib.request.urlopen(req, timeout=timeout) as resp, open(tmp_path, "wb") as f:
                shutil.copyfileobj(resp, f, length=1024 * 1024)
            return tmp_path
        except Exception as exc:
            if os.path.exists(tmp_path):
                os.unlink(tmp_path)
            if attempt + 1 >= attempts or not _is_retryable(exc):
                raise
            delay = retry_delay * (2 ** attempt)
            print(f"[L2-IMPORT] retry {attempt + 1}/{retries} in {delay:.1f}s for {url}: {exc}", file=sys.stderr, flush=True)
            sleep_fn(delay)
    raise RuntimeError("unreachable")


def process_day(
    sym: str,
    day: str,
    out_dir: str,
    sample_ms: int,
    depth: int,
    min_free_gb: float,
    retries: int = 4,
    retry_delay: float = 3.0,
) -> tuple[int, int]:
    feed = f"bybit-{sym}"
    book_out = os.path.join(out_dir, "book", feed, f"{day}.jsonl.gz")
    ticks_out = os.path.join(out_dir, "ticks", feed, f"{day}.jsonl.gz")

    n_book = -1
    n_ticks = -1

    if not os.path.exists(book_out):
        if free_gb(out_dir) < min_free_gb:
            raise RuntimeError(f"free disk below {min_free_gb:.1f} GB at {out_dir}")
        ob_url = BYBIT_OB_URL.format(sym=sym, day=day)
        zip_tmp = download_to_temp(ob_url, retries=retries, retry_delay=retry_delay)
        try:
            with zipfile.ZipFile(zip_tmp) as zf:
                entry = zf.namelist()[0]
                with zf.open(entry) as raw_f:
                    txt_f = io.TextIOWrapper(raw_f, encoding="utf-8", errors="replace")
                    n_book = convert_bybit_ob_lines(txt_f, book_out, sample_ms=sample_ms, depth=depth)
        finally:
            if os.path.exists(zip_tmp):
                os.unlink(zip_tmp)

    if not os.path.exists(ticks_out):
        if free_gb(out_dir) < min_free_gb:
            raise RuntimeError(f"free disk below {min_free_gb:.1f} GB at {out_dir}")
        tr_url = BYBIT_TRADES_URL.format(sym=sym, day=day)
        gz_tmp = download_to_temp(tr_url, retries=retries, retry_delay=retry_delay)
        try:
            with gzip.open(gz_tmp, "rt", encoding="utf-8", errors="replace") as txt_f:
                n_ticks = convert_bybit_trade_rows(txt_f, ticks_out)
        finally:
            if os.path.exists(gz_tmp):
                os.unlink(gz_tmp)

    return n_book, n_ticks


def main() -> int:
    default_out = os.path.expanduser("~/piquant-collect/historical_l2")
    default_end = (dt.datetime.now(dt.timezone.utc).date() - dt.timedelta(days=1)).isoformat()
    default_start = (dt.datetime.now(dt.timezone.utc).date() - dt.timedelta(days=90)).isoformat()

    p = argparse.ArgumentParser(description="Backfill historical L2 order books + trades from Bybit public archives.")
    p.add_argument("--symbols", default="BTCUSDT,ETHUSDT", help="Comma-separated spot symbols")
    p.add_argument("--from", dest="start", default=default_start, help="Start UTC date YYYY-MM-DD")
    p.add_argument("--to", dest="end", default=default_end, help="End UTC date YYYY-MM-DD")
    p.add_argument("--out", default=default_out, help="Target root dir (must NOT be ~/piquant-collect directly)")
    p.add_argument("--sample-ms", type=int, default=1000, help="L2 snapshot interval in ms (default 1000)")
    p.add_argument("--depth", type=int, default=10, help="Top N book levels per side (default 10)")
    p.add_argument("--min-free-gb", type=float, default=25.0, help="Pause/stop if free disk drops below this GB")
    p.add_argument("--retries", type=int, default=4, help="Retry count on transient DNS/network/5xx errors")
    p.add_argument("--retry-delay", type=float, default=3.0, help="Initial backoff delay in seconds")
    args = p.parse_args()

    out_abs = os.path.abspath(os.path.expanduser(args.out))
    live_abs = os.path.abspath(os.path.expanduser("~/piquant-collect"))
    if out_abs == live_abs:
        print("Refusing to write directly into ~/piquant-collect (PREREG17/18 live recording dir).", file=sys.stderr)
        return 2

    days = list(reversed(iter_days(args.start, args.end)))  # newest first so recent data lands immediately
    symbols = [s.strip().upper() for s in args.symbols.split(",") if s.strip()]
    print(f"[L2-IMPORT] out={out_abs} symbols={symbols} days={len(days)} ({args.start}..{args.end}) sample_ms={args.sample_ms}", flush=True)

    for day in days:
        for sym in symbols:
            try:
                nb, nt = process_day(
                    sym, day, out_abs, args.sample_ms, args.depth, args.min_free_gb,
                    retries=args.retries, retry_delay=args.retry_delay,
                )
                if nb >= 0 or nt >= 0:
                    print(f"[L2-IMPORT] {day} {sym}: book_rows={nb if nb>=0 else 'skip'} trades={nt if nt>=0 else 'skip'} free={free_gb(out_abs):.1f}G", flush=True)
            except urllib.error.HTTPError as e:
                print(f"[L2-IMPORT] {day} {sym}: HTTP {e.code} (skipping)", file=sys.stderr, flush=True)
            except Exception as e:
                print(f"[L2-IMPORT] {day} {sym}: error {e}", file=sys.stderr, flush=True)
                if "free disk below" in str(e):
                    return 1
    print("[L2-IMPORT] complete", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
