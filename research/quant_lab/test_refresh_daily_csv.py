"""Unit tests for Pi 5 daily CSV refresher (`refresh_daily_csv.py`)."""
from __future__ import annotations

import gzip
import io
import json
import os

import pandas as pd
import pytest

import multimodal_features as MF
import refresh_daily_csv as RDC


def _write_sample_ohlcv(path: str, dates: list[str], base: float = 60000.0) -> None:
    idx = pd.to_datetime(dates, utc=True)
    rows = [
        {"o": base + i, "h": base + i + 50.0, "l": base + i - 50.0, "c": base + i + 10.0, "v": 100.0 + i}
        for i in range(len(dates))
    ]
    df = pd.DataFrame(rows, index=idx)
    df.index.name = "t"
    df.to_csv(path)


def test_excludes_incomplete_today_and_preserves_existing_bars(tmp_path):
    csv_p = str(tmp_path / "BTC-USD_1D.csv")
    prices_d = tmp_path / "prices"
    prices_d.mkdir()

    _write_sample_ohlcv(csv_p, ["2026-11-16", "2026-11-17"], base=64000.0)

    # Local prices files: 2026-11-17 (already exists -> must preserve untouched),
    # 2026-11-18 (completed -> must add), 2026-11-19 (today_utc -> must exclude!)
    def _write_gz(name: str, rows: list[dict]) -> None:
        with gzip.open(str(prices_d / name), "wt", encoding="utf-8") as fh:
            for r in rows:
                fh.write(json.dumps(r) + "\n")

    t17 = int(pd.Timestamp("2026-11-17T12:00:00Z").timestamp() * 1000)
    t18_a = int(pd.Timestamp("2026-11-18T00:01:00Z").timestamp() * 1000)
    t18_b = int(pd.Timestamp("2026-11-18T23:59:00Z").timestamp() * 1000)
    t19 = int(pd.Timestamp("2026-11-19T08:00:00Z").timestamp() * 1000)

    _write_gz("2026-11-17.jsonl.gz", [{"t": t17, "sym": "BTCUSDT", "cb": 99999.0, "cb_vol": 99999.0}])
    _write_gz(
        "2026-11-18.jsonl.gz",
        [
            {"t": t18_a, "sym": "BTCUSDT", "cb": 65000.0, "cb_vol": 650000.0},
            {"t": t18_b, "sym": "BTCUSDT", "cb": 66000.0, "cb_vol": 1320000.0},
            {"t": t18_b, "sym": "ETHUSDT", "cb": 1800.0, "cb_vol": 180000.0},
        ],
    )
    _write_gz("2026-11-19.jsonl.gz", [{"t": t19, "sym": "BTCUSDT", "cb": 70000.0, "cb_vol": 700000.0}])

    res = RDC.refresh_ohlcv_csv(
        "BTC-USD",
        csv_p,
        prices_dir=str(prices_d),
        fetch_public=False,
        now_utc=pd.Timestamp("2026-11-19T15:30:00Z"),
    )
    assert res["prev_rows"] == 2
    assert res["added_rows"] == 1
    assert res["total_rows"] == 3
    assert res["last_date"] == "2026-11-18"
    assert not os.path.exists(csv_p + ".tmp")

    out = pd.read_csv(csv_p, index_col=0, parse_dates=True)
    # Existing 2026-11-17 bar preserved untouched (not overwritten by 99999.0)
    assert out.loc[pd.Timestamp("2026-11-17T00:00:00Z"), "c"] == pytest.approx(64011.0)
    # Completed 2026-11-18 rolled up accurately (o=65000, h=66000, l=65000, c=66000, v=10+20=30)
    bar18 = out.loc[pd.Timestamp("2026-11-18T00:00:00Z")]
    assert bar18["o"] == pytest.approx(65000.0)
    assert bar18["h"] == pytest.approx(66000.0)
    assert bar18["l"] == pytest.approx(65000.0)
    assert bar18["c"] == pytest.approx(66000.0)
    assert bar18["v"] == pytest.approx(30.0)
    # Today (2026-11-19) strictly excluded
    assert pd.Timestamp("2026-11-19T00:00:00Z") not in out.index


def test_coinbase_rest_mock_descending_and_atomic_write(tmp_path, monkeypatch):
    csv_p = str(tmp_path / "ETH-USD_1D.csv")
    _write_sample_ohlcv(csv_p, ["2026-11-16", "2026-11-17"], base=1800.0)

    captured_headers: dict[str, str] = {}
    captured_urls: list[str] = []

    t17 = int(pd.Timestamp("2026-11-17T00:00:00Z").timestamp())
    t18 = int(pd.Timestamp("2026-11-18T00:00:00Z").timestamp())
    t19 = int(pd.Timestamp("2026-11-19T00:00:00Z").timestamp())

    # Coinbase returns [time_sec, low, high, open, close, volume] descending,
    # including incomplete today's candle t19 which must be excluded!
    mock_rows = [
        [t19, 1900.0, 1950.0, 1910.0, 1940.0, 5000.0],
        [t18, 1810.0, 1890.0, 1820.0, 1875.0, 12345.6],
        [t17, 9999.0, 9999.0, 9999.0, 9999.0, 9999.0],
    ]

    class _FakeResp:
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return json.dumps(mock_rows).encode("utf-8")

    def _fake_urlopen(req, timeout=None):
        captured_urls.append(req.full_url)
        captured_headers.update({k.lower(): v for k, v in req.header_items()})
        return _FakeResp()

    monkeypatch.setattr(RDC.urllib.request, "urlopen", _fake_urlopen)

    res = RDC.refresh_ohlcv_csv(
        "ETH-USD",
        csv_p,
        prices_dir=None,
        fetch_public=True,
        now_utc=pd.Timestamp("2026-11-19T06:00:00Z"),
    )
    assert len(captured_urls) == 1
    assert "granularity=86400" in captured_urls[0]
    assert "user-agent" in captured_headers
    assert res["prev_rows"] == 2
    assert res["added_rows"] == 1
    assert res["total_rows"] == 3
    assert res["last_date"] == "2026-11-18"
    assert res["fetch_note"] == "ok (2 bars)"

    out = pd.read_csv(csv_p, index_col=0, parse_dates=True)
    # Preserved existing 2026-11-17
    assert out.loc[pd.Timestamp("2026-11-17T00:00:00Z"), "c"] == pytest.approx(1811.0)
    # Parsed [time, low, high, open, close, volume] accurately for 2026-11-18
    bar18 = out.loc[pd.Timestamp("2026-11-18T00:00:00Z")]
    assert bar18["o"] == pytest.approx(1820.0)
    assert bar18["h"] == pytest.approx(1890.0)
    assert bar18["l"] == pytest.approx(1810.0)
    assert bar18["c"] == pytest.approx(1875.0)
    assert bar18["v"] == pytest.approx(12345.6)
    assert pd.Timestamp("2026-11-19T00:00:00Z") not in out.index


def test_rest_failure_falls_back_to_prices_dir_and_handles_truncated_gz(tmp_path, monkeypatch):
    csv_p = str(tmp_path / "BTC-USD_1D.csv")
    prices_d = tmp_path / "prices"
    prices_d.mkdir()
    _write_sample_ohlcv(csv_p, ["2026-11-17"], base=64000.0)

    # Write a gzip file with valid row followed by truncated garbage tail
    t18 = int(pd.Timestamp("2026-11-18T12:00:00Z").timestamp() * 1000)
    buf = io.BytesIO()
    with gzip.GzipFile(fileobj=buf, mode="wb") as gz:
        gz.write((json.dumps({"t": t18, "sym": "BTCUSDT", "cb": 65500.0, "cb_vol": 655000.0}) + "\n").encode("utf-8"))
    raw_gz = buf.getvalue()
    (prices_d / "2026-11-18.jsonl.gz").write_bytes(raw_gz + b"\x1f\x8b\x08\x00broken")

    def _fail_urlopen(req, timeout=None):
        raise RDC.urllib.error.URLError("offline")

    monkeypatch.setattr(RDC.urllib.request, "urlopen", _fail_urlopen)
    monkeypatch.setattr(RDC.time, "sleep", lambda _s: None)

    res = RDC.refresh_ohlcv_csv(
        "BTC-USD",
        csv_p,
        prices_dir=str(prices_d),
        fetch_public=True,
        now_utc=pd.Timestamp("2026-11-19T02:00:00Z"),
    )
    assert res["fetch_note"].startswith("error:")
    assert res["added_rows"] == 1
    assert res["total_rows"] == 2
    assert res["last_date"] == "2026-11-18"


def test_refresh_multimodal_csv_causal_extension(tmp_path):
    btc_csv = str(tmp_path / "BTC-USD_1D.csv")
    eth_csv = str(tmp_path / "ETH-USD_1D.csv")
    mm_csv = str(tmp_path / "multimodal_1D.csv")

    _write_sample_ohlcv(btc_csv, ["2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19"], base=64000.0)
    _write_sample_ohlcv(eth_csv, ["2026-11-16", "2026-11-17", "2026-11-18", "2026-11-19"], base=1800.0)

    mm_init = pd.DataFrame(
        [
            {col: 0.1 * (i + 1) for i, col in enumerate(MF.MULTIMODAL_COLS)},
            {col: 0.2 * (i + 1) for i, col in enumerate(MF.MULTIMODAL_COLS)},
        ],
        index=pd.to_datetime(["2026-11-16", "2026-11-17"], utc=True),
    )
    mm_init.to_csv(mm_csv)

    res = RDC.refresh_multimodal_csv(mm_csv, btc_csv, eth_csv)
    assert res["prev_rows"] == 2
    assert res["added_rows"] == 2
    assert res["total_rows"] == 4
    assert res["last_date"] == "2026-11-19"
    assert not os.path.exists(mm_csv + ".tmp")

    out = MF.load_multimodal_csv(mm_csv)
    assert list(out.columns) == list(MF.MULTIMODAL_COLS)
    assert len(out) == 4
    # Existing rows preserved; new days forward-filled causally from 2026-11-17
    pd.testing.assert_series_equal(
        out.loc[pd.Timestamp("2026-11-18T00:00:00Z")],
        out.loc[pd.Timestamp("2026-11-17T00:00:00Z")],
        check_names=False,
    )
    pd.testing.assert_series_equal(
        out.loc[pd.Timestamp("2026-11-19T00:00:00Z")],
        out.loc[pd.Timestamp("2026-11-17T00:00:00Z")],
        check_names=False,
    )
