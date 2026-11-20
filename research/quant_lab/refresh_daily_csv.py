"""Raspberry Pi 5-friendly daily OHLCV & multimodal CSV refresher (zero pyarrow).

Updates `{product}_1D.csv` from local `prices/*.jsonl.gz` tick snapshots and/or
Coinbase Exchange public daily candles REST API, strictly excluding incomplete
intraday UTC bars (`day >= today_utc`), and causally extends `multimodal_1D.csv`.
"""
from __future__ import annotations

import argparse
import glob
import gzip
import json
import math
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from typing import Any

import numpy as np
import pandas as pd

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

try:
    from multimodal_features import MULTIMODAL_COLS
except ImportError:
    MULTIMODAL_COLS = (
        "btc_rv_1m_ann",
        "btc_jump_ratio",
        "btc_vpin_1m",
        "eth_vpin_1m",
        "btc_vrp",
        "dvol_z20",
        "btc_fund_z20",
        "fund_spread_be",
        "stable_flow_21d",
        "alt_breadth_20d",
        "qqq_ret21",
        "uup_ret21",
    )

OHLCV_COLS = ["o", "h", "l", "c", "v"]
COINBASE_CANDLES_URL = "https://api.exchange.coinbase.com/products/{product}/candles"
DEFAULT_DAILY_CSV_DIR = os.path.expanduser("~/piquant-collect/daily_csv")
DEFAULT_PRICES_DIR = os.path.expanduser("~/piquant-collect/prices")
_DATE_STEM_RE = re.compile(r"^(\d{4}-\d{2}-\d{2})(?:\.|$)")


def _to_utc_day(ts: pd.Timestamp | None) -> pd.Timestamp:
    if ts is None:
        t = pd.Timestamp.now(tz="UTC")
    else:
        t = pd.Timestamp(ts)
        if t.tzinfo is None:
            t = t.tz_localize("UTC")
        else:
            t = t.tz_convert("UTC")
    return t.floor("D")


def _normalize_utc_index(df: pd.DataFrame) -> pd.DataFrame:
    out = df.copy()
    idx = pd.to_datetime(out.index, utc=True)
    out.index = idx.floor("D")
    out.index.name = "t"
    return out


def _atomic_write_csv(df: pd.DataFrame, path: str, index_label: str | None = "t") -> None:
    parent = os.path.dirname(os.path.abspath(path))
    os.makedirs(parent, exist_ok=True)
    tmp_path = path + ".tmp"
    df.to_csv(tmp_path, index_label=index_label)
    os.replace(tmp_path, path)


def _read_existing_ohlcv(csv_path: str) -> pd.DataFrame:
    if not os.path.exists(csv_path):
        return pd.DataFrame(columns=OHLCV_COLS, index=pd.DatetimeIndex([], tz="UTC", name="t"))
    df = pd.read_csv(csv_path, index_col=0, parse_dates=True)
    if df.empty:
        return pd.DataFrame(columns=OHLCV_COLS, index=pd.DatetimeIndex([], tz="UTC", name="t"))
    df = _normalize_utc_index(df)
    df = df[OHLCV_COLS].astype("float64").dropna()
    df = df[~df.index.duplicated(keep="first")].sort_index()
    return df


def _parse_row_timestamp_utc(raw_t: Any) -> pd.Timestamp | None:
    if raw_t is None:
        return None
    if isinstance(raw_t, str):
        try:
            t = pd.Timestamp(raw_t)
            return t.tz_localize("UTC") if t.tzinfo is None else t.tz_convert("UTC")
        except (ValueError, TypeError):
            return None
    try:
        val = float(raw_t)
    except (ValueError, TypeError):
        return None
    if not math.isfinite(val):
        return None
    aval = abs(val)
    if aval > 1e14:
        return pd.to_datetime(int(val), unit="ns", utc=True)
    if aval > 1e11:
        return pd.to_datetime(int(val), unit="ms", utc=True)
    return pd.to_datetime(val, unit="s", utc=True)


def _extract_ohlcv_from_price_row(row: dict[str, Any]) -> tuple[float, float, float, float, float] | None:
    if all(k in row for k in ("o", "h", "l", "c")):
        try:
            o = float(row["o"])
            h = float(row["h"])
            l = float(row["l"])
            c = float(row["c"])
            v = float(row.get("v", row.get("vol", 0.0)) or 0.0)
        except (ValueError, TypeError):
            return None
        if all(math.isfinite(x) for x in (o, h, l, c, v)) and o > 0 and c > 0 and h >= l and v >= 0:
            return o, h, l, c, v
        return None

    px_raw = None
    used_cb = False
    for key in ("cb", "mid", "c", "price"):
        val = row.get(key)
        if val is not None:
            try:
                fval = float(val)
            except (ValueError, TypeError):
                continue
            if math.isfinite(fval) and fval > 0:
                px_raw = fval
                used_cb = key == "cb"
                break
    if px_raw is None:
        return None

    v = 0.0
    if used_cb and "cb_vol" in row:
        try:
            cb_vol = float(row.get("cb_vol") or 0.0)
            if math.isfinite(cb_vol) and cb_vol >= 0:
                v = cb_vol / px_raw
        except (ValueError, TypeError):
            v = 0.0
    else:
        try:
            raw_v = float(row.get("v", row.get("vol", 0.0)) or 0.0)
            if math.isfinite(raw_v) and raw_v >= 0:
                v = raw_v
        except (ValueError, TypeError):
            v = 0.0

    return px_raw, px_raw, px_raw, px_raw, v


def _rollup_prices_dir(
    product: str,
    prices_dir: str | None,
    today_utc: pd.Timestamp,
    skip_days: set[pd.Timestamp] | None = None,
) -> pd.DataFrame:
    empty = pd.DataFrame(columns=OHLCV_COLS, index=pd.DatetimeIndex([], tz="UTC", name="t"))
    if not prices_dir:
        return empty
    pdir = os.path.expanduser(prices_dir)
    if not os.path.isdir(pdir):
        return empty

    base_coin = product.split("-")[0].upper()
    accepted_syms = {product.upper(), f"{base_coin}USDT", f"{base_coin}USD"}
    skip_set = skip_days or set()

    # Track running OHLCV per completed UTC day in O(days) memory
    # day -> [first_key, o, h, l, last_key, c, v_sum]
    day_agg: dict[pd.Timestamp, list[Any]] = {}
    seq = 0

    for path in sorted(glob.glob(os.path.join(pdir, "*.jsonl.gz"))):
        fname = os.path.basename(path)
        m = _DATE_STEM_RE.match(fname)
        if m:
            try:
                file_day = pd.Timestamp(m.group(1), tz="UTC").floor("D")
            except ValueError:
                file_day = None
            if file_day is not None:
                if file_day >= today_utc or file_day in skip_set:
                    continue

        try:
            with gzip.open(path, "rt", encoding="utf-8", errors="replace") as fh:
                try:
                    for line in fh:
                        line = line.strip()
                        if not line:
                            continue
                        try:
                            row = json.loads(line)
                        except json.JSONDecodeError:
                            continue
                        if not isinstance(row, dict):
                            continue
                        sym = str(row.get("sym") or row.get("product") or "").upper()
                        if sym and sym not in accepted_syms:
                            continue
                        ts = _parse_row_timestamp_utc(row.get("t"))
                        if ts is None:
                            continue
                        day = ts.floor("D")
                        if day >= today_utc or day in skip_set:
                            continue
                        ohlcv = _extract_ohlcv_from_price_row(row)
                        if ohlcv is None:
                            continue
                        o, h, l, c, v = ohlcv
                        key = (ts.value, seq)
                        seq += 1
                        cur = day_agg.get(day)
                        if cur is None:
                            day_agg[day] = [key, o, h, l, key, c, v]
                        else:
                            if key < cur[0]:
                                cur[0] = key
                                cur[1] = o
                            if h > cur[2]:
                                cur[2] = h
                            if l < cur[3]:
                                cur[3] = l
                            if key > cur[4]:
                                cur[4] = key
                                cur[5] = c
                            cur[6] += v
                except (EOFError, OSError, gzip.BadGzipFile):
                    # Preserve rows decoded before a truncated trailing gzip member
                    pass
        except OSError:
            continue

    if not day_agg:
        return empty

    days_sorted = sorted(day_agg.keys())
    records = [
        {
            "o": day_agg[d][1],
            "h": day_agg[d][2],
            "l": day_agg[d][3],
            "c": day_agg[d][5],
            "v": day_agg[d][6],
        }
        for d in days_sorted
    ]
    df = pd.DataFrame(records, index=pd.DatetimeIndex(days_sorted, tz="UTC", name="t"))
    return df[OHLCV_COLS].astype("float64")


def _fetch_coinbase_public_daily(
    product: str,
    start_utc: pd.Timestamp,
    today_utc: pd.Timestamp,
    timeout_s: float = 15.0,
    max_retries: int = 3,
    retry_sleep_s: float = 0.25,
) -> tuple[pd.DataFrame, str]:
    empty = pd.DataFrame(columns=OHLCV_COLS, index=pd.DatetimeIndex([], tz="UTC", name="t"))
    if start_utc >= today_utc:
        return empty, "up_to_date"

    rows_by_day: dict[pd.Timestamp, tuple[float, float, float, float, float]] = {}
    cur_start = start_utc
    last_err: str | None = None

    while cur_start < today_utc:
        chunk_end = min(cur_start + pd.Timedelta(days=250), today_utc)
        query = urllib.parse.urlencode(
            {
                "granularity": 86400,
                "start": cur_start.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "end": chunk_end.strftime("%Y-%m-%dT%H:%M:%SZ"),
            }
        )
        url = f"{COINBASE_CANDLES_URL.format(product=product)}?{query}"
        req = urllib.request.Request(
            url,
            headers={
                "User-Agent": "PiQuant-Research/1.0 (+https://github.com/piquant)",
                "Accept": "application/json",
            },
            method="GET",
        )

        payload: Any = None
        chunk_err: str | None = None
        for attempt in range(max_retries):
            try:
                with urllib.request.urlopen(req, timeout=timeout_s) as resp:
                    payload = json.loads(resp.read().decode("utf-8"))
                chunk_err = None
                break
            except (urllib.error.URLError, TimeoutError, json.JSONDecodeError, ValueError, OSError) as exc:
                chunk_err = str(exc)
                if attempt + 1 < max_retries and retry_sleep_s > 0:
                    time.sleep(retry_sleep_s * (attempt + 1))

        if chunk_err is not None:
            last_err = chunk_err
            break

        if isinstance(payload, list):
            for item in payload:
                if not isinstance(item, (list, tuple)) or len(item) < 6:
                    continue
                try:
                    # Coinbase Exchange format: [time_sec, low, high, open, close, volume]
                    ts = pd.to_datetime(int(item[0]), unit="s", utc=True).floor("D")
                    low_px = float(item[1])
                    high_px = float(item[2])
                    open_px = float(item[3])
                    close_px = float(item[4])
                    vol = float(item[5])
                except (ValueError, TypeError):
                    continue
                if ts >= today_utc:
                    continue
                if not all(math.isfinite(x) for x in (open_px, high_px, low_px, close_px, vol)):
                    continue
                if open_px <= 0 or close_px <= 0 or high_px < low_px or vol < 0:
                    continue
                rows_by_day[ts] = (open_px, high_px, low_px, close_px, vol)

        cur_start = chunk_end

    if not rows_by_day:
        if last_err is not None:
            return empty, f"error: {last_err}"
        return empty, "ok (0 bars)"

    days_sorted = sorted(rows_by_day.keys())
    df = pd.DataFrame(
        [
            {
                "o": rows_by_day[d][0],
                "h": rows_by_day[d][1],
                "l": rows_by_day[d][2],
                "c": rows_by_day[d][3],
                "v": rows_by_day[d][4],
            }
            for d in days_sorted
        ],
        index=pd.DatetimeIndex(days_sorted, tz="UTC", name="t"),
    )[OHLCV_COLS].astype("float64")
    note = f"ok ({len(df)} bars)" if last_err is None else f"partial ({len(df)} bars; error: {last_err})"
    return df, note


def refresh_ohlcv_csv(
    product: str,
    csv_path: str,
    prices_dir: str | None = None,
    fetch_public: bool = False,
    now_utc: pd.Timestamp | None = None,
) -> dict[str, Any]:
    """Refresh `{product}_1D.csv` with completed UTC bars (`< today_utc`) atomically."""
    today_utc = _to_utc_day(now_utc)
    existing = _read_existing_ohlcv(csv_path)
    prev_rows = int(len(existing))
    existing_clean = existing[existing.index < today_utc]

    parts: list[pd.DataFrame] = []
    if not existing_clean.empty:
        parts.append(existing_clean)

    fetch_note = "skipped"
    if fetch_public:
        start_utc = (
            existing_clean.index.max() + pd.Timedelta(days=1)
            if not existing_clean.empty
            else today_utc - pd.Timedelta(days=250)
        )
        rest_df, fetch_note = _fetch_coinbase_public_daily(product, start_utc=start_utc, today_utc=today_utc)
        if not rest_df.empty:
            parts.append(rest_df)

    existing_days = set(existing_clean.index)
    prices_df = _rollup_prices_dir(product, prices_dir, today_utc=today_utc, skip_days=existing_days)
    if not prices_df.empty:
        parts.append(prices_df)

    if parts:
        combined = pd.concat(parts, axis=0)
        combined = combined[combined.index < today_utc]
        combined = combined[~combined.index.duplicated(keep="first")].sort_index()
        combined = combined[OHLCV_COLS].astype("float64")
    else:
        combined = pd.DataFrame(columns=OHLCV_COLS, index=pd.DatetimeIndex([], tz="UTC", name="t"))

    combined.index.name = "t"
    _atomic_write_csv(combined, csv_path, index_label="t")

    total_rows = int(len(combined))
    added_rows = int(len(combined.index.difference(existing_clean.index)))
    last_date = combined.index.max().strftime("%Y-%m-%d") if total_rows > 0 else None
    return {
        "product": product,
        "prev_rows": prev_rows,
        "added_rows": added_rows,
        "total_rows": total_rows,
        "last_date": last_date,
        "fetch_note": fetch_note,
    }


def refresh_multimodal_csv(mm_path: str, btc_csv_path: str, eth_csv_path: str) -> dict[str, Any]:
    """Causally extend `multimodal_1D.csv` to match completed dates in `BTC-USD_1D.csv`."""
    btc_df = _read_existing_ohlcv(btc_csv_path)
    _ = _read_existing_ohlcv(eth_csv_path)  # validated alongside BTC if present
    cols = list(MULTIMODAL_COLS)

    if os.path.exists(mm_path):
        mm_raw = pd.read_csv(mm_path, index_col=0, parse_dates=True)
        prev_rows = int(len(mm_raw))
        if not mm_raw.empty:
            mm_raw = _normalize_utc_index(mm_raw)
            mm_raw = mm_raw[~mm_raw.index.duplicated(keep="first")].sort_index()
            for col in cols:
                if col not in mm_raw.columns:
                    mm_raw[col] = 0.0
            mm_existing = mm_raw[cols].astype("float64")
        else:
            mm_existing = pd.DataFrame(columns=cols, index=pd.DatetimeIndex([], tz="UTC"))
    else:
        prev_rows = 0
        mm_existing = pd.DataFrame(columns=cols, index=pd.DatetimeIndex([], tz="UTC"))

    if btc_df.empty and mm_existing.empty:
        out_df = pd.DataFrame(columns=cols, index=pd.DatetimeIndex([], tz="UTC"))
        _atomic_write_csv(out_df, mm_path, index_label="")
        return {"prev_rows": prev_rows, "added_rows": 0, "total_rows": 0, "last_date": None}

    target_index = mm_existing.index.union(btc_df.index).sort_values()
    if mm_existing.empty:
        out_df = pd.DataFrame(0.0, index=target_index, columns=cols, dtype="float64")
    else:
        # Strictly causal forward fill only (never bfill / never lookahead)
        out_df = mm_existing[cols].reindex(target_index).ffill().fillna(0.0).astype("float64")

    out_df.index.name = None
    _atomic_write_csv(out_df, mm_path, index_label="")

    total_rows = int(len(out_df))
    added_rows = int(len(out_df.index.difference(mm_existing.index)))
    last_date = out_df.index.max().strftime("%Y-%m-%d") if total_rows > 0 else None
    return {
        "prev_rows": prev_rows,
        "added_rows": added_rows,
        "total_rows": total_rows,
        "last_date": last_date,
    }


def main() -> None:
    p = argparse.ArgumentParser(description="Refresh daily OHLCV and multimodal CSVs for QuantLab.")
    p.add_argument("--daily-csv-dir", default=DEFAULT_DAILY_CSV_DIR)
    p.add_argument("--prices-dir", default=DEFAULT_PRICES_DIR)
    p.add_argument("--fetch-public", action="store_true")
    args = p.parse_args()

    daily_dir = os.path.expanduser(args.daily_csv_dir)
    prices_dir = os.path.expanduser(args.prices_dir) if args.prices_dir else None

    btc_csv = os.path.join(daily_dir, "BTC-USD_1D.csv")
    eth_csv = os.path.join(daily_dir, "ETH-USD_1D.csv")
    mm_csv = os.path.join(daily_dir, "multimodal_1D.csv")

    btc_res = refresh_ohlcv_csv("BTC-USD", btc_csv, prices_dir=prices_dir, fetch_public=args.fetch_public)
    eth_res = refresh_ohlcv_csv("ETH-USD", eth_csv, prices_dir=prices_dir, fetch_public=args.fetch_public)
    mm_res = refresh_multimodal_csv(mm_csv, btc_csv, eth_csv)

    print(
        f"refresh_daily_csv | BTC +{btc_res['added_rows']} (total={btc_res['total_rows']}, last={btc_res['last_date']}, fetch={btc_res['fetch_note']}) | "
        f"ETH +{eth_res['added_rows']} (total={eth_res['total_rows']}, last={eth_res['last_date']}, fetch={eth_res['fetch_note']}) | "
        f"MM +{mm_res['added_rows']} (total={mm_res['total_rows']}, last={mm_res['last_date']})"
    )


if __name__ == "__main__":
    main()
