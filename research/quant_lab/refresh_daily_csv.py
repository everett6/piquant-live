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
import zlib
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
DERIBIT_DVOL_URL = "https://www.deribit.com/api/v2/public/get_volatility_index_data"
DEFILLAMA_STABLECOINS_URL = "https://stablecoins.llama.fi/stablecoincharts/all"
PUBLIC_HTTP_TIMEOUT_S = 8.0
DEFAULT_HTTP_HEADERS = {
    "User-Agent": "PiQuant-Research/1.0 (+https://github.com/piquant)",
    "Accept": "application/json",
}
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


def _iter_price_rows_gz(gz_path: str):
    """Yield decoded JSON dict rows from a `.jsonl.gz` snapshot, tolerating truncated tails.

    Rows the collector marked `"stale": true` are skipped: during a network outage it keeps writing the last
    known quote every second (flagged stale), and those frozen prices would read as a flat, zero-volatility
    market."""
    try:
        with gzip.open(gz_path, "rt", encoding="utf-8", errors="replace") as fh:
            try:
                for line in fh:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        row = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    if isinstance(row, dict) and row.get("stale") is not True:
                        yield row
            except (EOFError, OSError, gzip.BadGzipFile, zlib.error):
                # Preserve rows decoded before a truncated trailing gzip member
                pass
    except OSError:
        return


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

        for row in _iter_price_rows_gz(path):
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


def _fetch_json(url: str, timeout_s: float = PUBLIC_HTTP_TIMEOUT_S) -> Any:
    req = urllib.request.Request(url, headers=DEFAULT_HTTP_HEADERS, method="GET")
    with urllib.request.urlopen(req, timeout=timeout_s) as resp:
        return json.loads(resp.read().decode("utf-8"))


def _compute_1m_series_metrics(bucket: dict[int, list[Any]]) -> tuple[float, float, float] | None:
    """Compute (rv_ann, jump_ratio, vpin_1m) from minute-bucketed {min_key: [last_key, c, v_sum]}."""
    if len(bucket) < 2:
        return None
    keys = sorted(bucket.keys())
    closes = np.array([bucket[k][1] for k in keys], dtype="float64")
    vols = np.array([bucket[k][2] for k in keys], dtype="float64")
    r = np.log(closes[1:] / closes[:-1])
    vol_1m = vols[1:]
    valid = np.isfinite(r) & np.isfinite(vol_1m)
    r = r[valid]
    vol_1m = vol_1m[valid]
    if len(r) == 0:
        return None

    rv = float(np.sqrt(np.sum(r * r) * 365.0))
    jump = float(np.clip(np.max(np.abs(r)) / (np.std(r) + 1e-12), 0.0, 30.0))
    vol_sum = float(np.sum(vol_1m))
    if vol_sum > 0.0:
        vpin = float(np.clip(np.sum(np.sign(r) * vol_1m) / (vol_sum + 1e-12), -1.0, 1.0))
    else:
        nz = int(np.sum(np.abs(r) > 0.0))
        vpin = float(np.clip(np.sum(np.sign(r)) / max(1, nz), -1.0, 1.0))
    return rv, jump, vpin


def _rollup_prices_1m_for_days(
    prices_dir: str | None,
    days: list[pd.Timestamp],
) -> dict[pd.Timestamp, dict[str, float]]:
    """Compute live 1m intraday features from `prices_dir/<YYYY-MM-DD>.jsonl.gz` for new UTC days."""
    if not prices_dir or not days:
        return {}
    pdir = os.path.expanduser(prices_dir)
    if not os.path.isdir(pdir):
        return {}

    btc_syms = {"BTC-USD", "BTCUSDT", "BTCUSD"}
    eth_syms = {"ETH-USD", "ETHUSDT", "ETHUSD"}
    out: dict[pd.Timestamp, dict[str, float]] = {}

    for day in days:
        gz_path = os.path.join(pdir, f"{day.strftime('%Y-%m-%d')}.jsonl.gz")
        if not os.path.isfile(gz_path):
            continue

        btc_1m: dict[int, list[Any]] = {}
        eth_1m: dict[int, list[Any]] = {}
        seq = 0

        for row in _iter_price_rows_gz(gz_path):
            sym = str(row.get("sym") or row.get("product") or "").upper()
            if sym in btc_syms:
                target = btc_1m
            elif sym in eth_syms:
                target = eth_1m
            else:
                continue
            ts = _parse_row_timestamp_utc(row.get("t"))
            if ts is None or ts.floor("D") != day:
                continue
            ohlcv = _extract_ohlcv_from_price_row(row)
            if ohlcv is None:
                continue
            _, _, _, c, v = ohlcv
            minute_key = ts.value // 60_000_000_000
            tick_key = (ts.value, seq)
            seq += 1
            cur = target.get(minute_key)
            if cur is None:
                target[minute_key] = [tick_key, c, v]
            else:
                if tick_key > cur[0]:
                    cur[0] = tick_key
                    cur[1] = c
                cur[2] += v

        day_vals: dict[str, float] = {}
        btc_metrics = _compute_1m_series_metrics(btc_1m)
        if btc_metrics is not None:
            day_vals["btc_rv_1m_ann"], day_vals["btc_jump_ratio"], day_vals["btc_vpin_1m"] = btc_metrics
        eth_metrics = _compute_1m_series_metrics(eth_1m)
        if eth_metrics is not None:
            day_vals["eth_vpin_1m"] = eth_metrics[2]
        if day_vals:
            out[day] = day_vals

    return out


def _fetch_deribit_dvol(
    new_days: list[pd.Timestamp],
    today_utc: pd.Timestamp,
    timeout_s: float = PUBLIC_HTTP_TIMEOUT_S,
) -> tuple[pd.Series, pd.Series, str]:
    """Fetch BTC daily Deribit DVOL (~35d window) and return (close_series, z20_series, status)."""
    empty = pd.Series(dtype="float64")
    try:
        start_day = min(today_utc - pd.Timedelta(days=35), min(new_days) - pd.Timedelta(days=25))
        query = urllib.parse.urlencode(
            {
                "currency": "BTC",
                "start_timestamp": int(start_day.timestamp() * 1000),
                "end_timestamp": int(today_utc.timestamp() * 1000),
                "resolution": "1D",
            }
        )
        payload = _fetch_json(f"{DERIBIT_DVOL_URL}?{query}", timeout_s=timeout_s)
        if not isinstance(payload, dict):
            raise ValueError("invalid Deribit DVOL response")
        if payload.get("error"):
            raise ValueError(str(payload["error"]))
        result = payload.get("result")
        rows = result.get("data") if isinstance(result, dict) else None
        if not isinstance(rows, list):
            raise ValueError("missing result.data in Deribit DVOL response")

        dvol_by_day: dict[pd.Timestamp, float] = {}
        for item in rows:
            if not isinstance(item, (list, tuple)) or len(item) < 5:
                continue
            try:
                day = pd.to_datetime(int(item[0]), unit="ms", utc=True).floor("D")
                close_px = float(item[4])
            except (ValueError, TypeError):
                continue
            if day >= today_utc or not math.isfinite(close_px) or close_px <= 0.0:
                continue
            dvol_by_day[day] = close_px

        if not dvol_by_day:
            return empty, empty, "ok (0 bars)"

        dvol_s = pd.Series(dvol_by_day, dtype="float64").sort_index()
        cal_idx = pd.date_range(start=dvol_s.index.min(), end=dvol_s.index.max(), freq="D", tz="UTC")
        dvol_cal = dvol_s.reindex(cal_idx).ffill()
        mean20 = dvol_cal.rolling(20).mean()
        std20 = dvol_cal.rolling(20).std()
        z20 = ((dvol_cal - mean20) / (std20 + 1e-12)).reindex(dvol_s.index).dropna()
        return dvol_s, z20, f"ok ({len(dvol_s)} bars)"
    except Exception as exc:
        return empty, empty, f"error: {exc}"


def _fetch_defillama_stable_flow(
    new_days: list[pd.Timestamp],
    today_utc: pd.Timestamp,
    timeout_s: float = PUBLIC_HTTP_TIMEOUT_S,
) -> tuple[pd.Series, str]:
    """Fetch DeFiLlama total stablecoin USD supply and compute 21d flow for `new_days`."""
    empty = pd.Series(dtype="float64")
    try:
        payload = _fetch_json(DEFILLAMA_STABLECOINS_URL, timeout_s=timeout_s)
        if not isinstance(payload, list):
            raise ValueError("invalid DeFiLlama stablecoins response")

        usd_by_day: dict[pd.Timestamp, float] = {}
        for item in payload:
            if not isinstance(item, dict):
                continue
            raw_date = item.get("date")
            day: pd.Timestamp | None = None
            if raw_date is not None:
                try:
                    val = float(raw_date)
                    if math.isfinite(val):
                        day = _parse_row_timestamp_utc(val)
                except (ValueError, TypeError):
                    day = _parse_row_timestamp_utc(raw_date)
            if day is None:
                continue
            day = day.floor("D")
            if day >= today_utc:
                continue

            circ = item.get("totalCirculatingUSD")
            if not isinstance(circ, dict):
                continue
            usd: float | None = None
            if "peggedUSD" in circ and circ["peggedUSD"] is not None:
                try:
                    cand = float(circ["peggedUSD"])
                    if math.isfinite(cand) and cand > 0.0:
                        usd = cand
                except (ValueError, TypeError):
                    usd = None
            else:
                total = 0.0
                found = False
                for v in circ.values():
                    try:
                        fv = float(v)
                    except (ValueError, TypeError):
                        continue
                    if math.isfinite(fv):
                        total += fv
                        found = True
                if found and total > 0.0:
                    usd = total
            if usd is None:
                continue
            usd_by_day[day] = usd

        if not usd_by_day:
            return empty, "ok (0 bars)"

        usd_s = pd.Series(usd_by_day, dtype="float64").sort_index()
        flow_by_day: dict[pd.Timestamp, float] = {}
        for day in new_days:
            prev_day = day - pd.Timedelta(days=21)
            if day in usd_s.index and prev_day in usd_s.index:
                prev_usd = float(usd_s.loc[prev_day])
                if prev_usd > 0.0:
                    flow_by_day[day] = float((float(usd_s.loc[day]) / prev_usd) - 1.0)
        return pd.Series(flow_by_day, dtype="float64"), f"ok ({len(usd_s)} bars)"
    except Exception as exc:
        return empty, f"error: {exc}"


def refresh_multimodal_csv(
    mm_path: str,
    btc_csv_path: str,
    eth_csv_path: str,
    prices_dir: str | None = None,
    fetch_public: bool = False,
    now_utc: pd.Timestamp | None = None,
) -> dict[str, Any]:
    """Causally extend `multimodal_1D.csv` to match completed dates in `BTC-USD_1D.csv`."""
    today_utc = _to_utc_day(now_utc)
    btc_df = _read_existing_ohlcv(btc_csv_path)
    _ = _read_existing_ohlcv(eth_csv_path)  # validated alongside BTC if present
    btc_completed = btc_df[btc_df.index < today_utc]
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

    live_feeds: dict[str, Any] = {
        "prices_1m": 0,
        "deribit_dvol": "skipped",
        "defillama_stable": "skipped",
    }

    if btc_completed.empty and mm_existing.empty:
        out_df = pd.DataFrame(columns=cols, index=pd.DatetimeIndex([], tz="UTC"))
        _atomic_write_csv(out_df, mm_path, index_label="")
        return {
            "prev_rows": prev_rows,
            "added_rows": 0,
            "total_rows": 0,
            "last_date": None,
            "live_feeds": live_feeds,
        }

    target_index = mm_existing.index.union(btc_completed.index).sort_values()
    existing_set = set(mm_existing.index)
    new_days = [d for d in target_index if d not in existing_set and d < today_utc]
    new_days_set = set(new_days)

    work_df = pd.DataFrame(np.nan, index=target_index, columns=cols, dtype="float64")
    if not mm_existing.empty:
        work_df.loc[mm_existing.index, cols] = mm_existing[cols]

    if new_days:
        intraday_updates = _rollup_prices_1m_for_days(prices_dir, new_days)
        for day, vals in intraday_updates.items():
            for col, val in vals.items():
                work_df.loc[day, col] = val
        live_feeds["prices_1m"] = int(len(intraday_updates))

        if fetch_public:
            dvol_close_s, dvol_z20_s, dvol_note = _fetch_deribit_dvol(new_days, today_utc)
            live_feeds["deribit_dvol"] = dvol_note
            if not dvol_close_s.empty:
                rv_carried = work_df["btc_rv_1m_ann"].ffill().fillna(0.0)
                for day in new_days:
                    if day in dvol_close_s.index:
                        work_df.loc[day, "btc_vrp"] = float(
                            (float(dvol_close_s.loc[day]) / 100.0) - float(rv_carried.loc[day])
                        )
                    if day in dvol_z20_s.index:
                        z_val = float(dvol_z20_s.loc[day])
                        if math.isfinite(z_val):
                            work_df.loc[day, "dvol_z20"] = z_val

            stable_flow_s, stable_note = _fetch_defillama_stable_flow(new_days, today_utc)
            live_feeds["defillama_stable"] = stable_note
            for day, val in stable_flow_s.items():
                if day in new_days_set and math.isfinite(float(val)):
                    work_df.loc[day, "stable_flow_21d"] = float(val)

    # Strictly causal forward fill only (never bfill / never lookahead)
    out_df = work_df[cols].ffill().fillna(0.0).astype("float64")
    if not mm_existing.empty:
        # Guarantee historical rows already present in mm_existing are never mutated
        out_df.loc[mm_existing.index, cols] = mm_existing[cols]

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
        "live_feeds": live_feeds,
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
    mm_res = refresh_multimodal_csv(mm_csv, btc_csv, eth_csv, prices_dir=prices_dir, fetch_public=args.fetch_public)
    lf = mm_res.get("live_feeds", {})

    print(
        f"refresh_daily_csv | BTC +{btc_res['added_rows']} (total={btc_res['total_rows']}, last={btc_res['last_date']}, fetch={btc_res['fetch_note']}) | "
        f"ETH +{eth_res['added_rows']} (total={eth_res['total_rows']}, last={eth_res['last_date']}, fetch={eth_res['fetch_note']}) | "
        f"MM +{mm_res['added_rows']} (total={mm_res['total_rows']}, last={mm_res['last_date']}, "
        f"prices_1m={lf.get('prices_1m', 0)}, dvol={lf.get('deribit_dvol', 'skipped')}, stable={lf.get('defillama_stable', 'skipped')})"
    )


if __name__ == "__main__":
    main()
