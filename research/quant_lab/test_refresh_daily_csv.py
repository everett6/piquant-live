"""Unit tests for Pi 5 daily CSV refresher (`refresh_daily_csv.py`)."""
from __future__ import annotations

import gzip
import io
import json
import math
import os

import numpy as np
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


def _write_gz_rows(path, rows: list[dict], trailing_garbage: bytes = b"") -> None:
    buf = io.BytesIO()
    with gzip.GzipFile(fileobj=buf, mode="wb") as gz:
        for r in rows:
            gz.write((json.dumps(r) + "\n").encode("utf-8"))
    path.write_bytes(buf.getvalue() + trailing_garbage)


def test_refresh_multimodal_live_prices_1m_and_historical_immutability(tmp_path):
    btc_csv = str(tmp_path / "BTC-USD_1D.csv")
    eth_csv = str(tmp_path / "ETH-USD_1D.csv")
    mm_csv = str(tmp_path / "multimodal_1D.csv")
    prices_d = tmp_path / "prices"
    prices_d.mkdir()

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

    # Historical day file must NOT mutate existing 2026-11-17 row in mm_csv
    t17 = int(pd.Timestamp("2026-11-17T01:00:00Z").timestamp() * 1000)
    _write_gz_rows(
        prices_d / "2026-11-17.jsonl.gz",
        [
            {"t": t17, "sym": "BTCUSDT", "cb": 50000.0, "cb_vol": 500000.0},
            {"t": t17 + 60_000, "sym": "BTCUSDT", "cb": 90000.0, "cb_vol": 900000.0},
        ],
    )

    # Completed new day 2026-11-18 with multiple ticks inside minute 0 and 4 minutes total,
    # plus truncated trailing gzip garbage to ensure corrupt tail recovery.
    t18 = int(pd.Timestamp("2026-11-18T00:00:00Z").timestamp() * 1000)
    rows18 = [
        # Minute 0: two BTC ticks -> last price is 100.0, sum volume = 1.0 + 1.0 = 2.0
        {"t": t18 + 10_000, "sym": "BTCUSDT", "cb": 99.0, "cb_vol": 99.0},
        {"t": t18 + 50_000, "sym": "BTCUSDT", "cb": 100.0, "cb_vol": 200.0},
        # Minute 1: BTC up to 105.0, vol = 10.0
        {"t": t18 + 60_000, "sym": "BTCUSDT", "cb": 105.0, "cb_vol": 1050.0},
        # Minute 2: BTC down to 102.0, vol = 4.0
        {"t": t18 + 120_000, "sym": "BTCUSDT", "cb": 102.0, "cb_vol": 408.0},
        # Minute 3: BTC up to 110.0, vol = 6.0
        {"t": t18 + 180_000, "sym": "BTCUSDT", "cb": 110.0, "cb_vol": 660.0},
        # ETH 1-minute series: 2000 -> 1980 (vol 10) -> 1960 (vol 5) -> 1990 (vol 5)
        {"t": t18 + 10_000, "sym": "ETHUSDT", "cb": 2000.0, "cb_vol": 2000.0},
        {"t": t18 + 60_000, "sym": "ETHUSDT", "cb": 1980.0, "cb_vol": 19800.0},
        {"t": t18 + 120_000, "sym": "ETHUSDT", "cb": 1960.0, "cb_vol": 9800.0},
        {"t": t18 + 180_000, "sym": "ETHUSDT", "cb": 1990.0, "cb_vol": 9950.0},
    ]
    _write_gz_rows(prices_d / "2026-11-18.jsonl.gz", rows18, trailing_garbage=b"\x1f\x8b\x08\x00broken")

    # Incomplete today 2026-11-19 must be excluded when now_utc=2026-11-19T12:00Z
    t19 = int(pd.Timestamp("2026-11-19T01:00:00Z").timestamp() * 1000)
    _write_gz_rows(
        prices_d / "2026-11-19.jsonl.gz",
        [
            {"t": t19, "sym": "BTCUSDT", "cb": 70000.0, "cb_vol": 700000.0},
            {"t": t19 + 60_000, "sym": "BTCUSDT", "cb": 71000.0, "cb_vol": 710000.0},
        ],
    )

    res = RDC.refresh_multimodal_csv(
        mm_csv,
        btc_csv,
        eth_csv,
        prices_dir=str(prices_d),
        fetch_public=False,
        now_utc=pd.Timestamp("2026-11-19T12:00:00Z"),
    )
    assert res["prev_rows"] == 2
    assert res["added_rows"] == 1
    assert res["total_rows"] == 3
    assert res["last_date"] == "2026-11-18"
    assert res["live_feeds"] == {
        "prices_1m": 1,
        "deribit_dvol": "skipped",
        "defillama_stable": "skipped",
    }

    out = MF.load_multimodal_csv(mm_csv)
    assert len(out) == 3
    assert pd.Timestamp("2026-11-19T00:00:00Z") not in out.index

    # Historical rows strictly immutable
    pd.testing.assert_frame_equal(out.loc[mm_init.index], mm_init, check_names=False)

    # Hand-computed expectations for 2026-11-18
    btc_r = np.log(np.array([105.0 / 100.0, 102.0 / 105.0, 110.0 / 102.0]))
    btc_v = np.array([10.0, 4.0, 6.0])
    exp_rv = math.sqrt(float(np.sum(btc_r**2)) * 365.0)
    exp_jump = float(np.clip(np.max(np.abs(btc_r)) / (np.std(btc_r) + 1e-12), 0.0, 30.0))
    exp_btc_vpin = float(np.sum(np.sign(btc_r) * btc_v) / (np.sum(btc_v) + 1e-12))

    eth_r = np.log(np.array([1980.0 / 2000.0, 1960.0 / 1980.0, 1990.0 / 1960.0]))
    eth_v = np.array([10.0, 5.0, 5.0])
    exp_eth_vpin = float(np.sum(np.sign(eth_r) * eth_v) / (np.sum(eth_v) + 1e-12))

    row18 = out.loc[pd.Timestamp("2026-11-18T00:00:00Z")]
    assert row18["btc_rv_1m_ann"] == pytest.approx(exp_rv)
    assert row18["btc_jump_ratio"] == pytest.approx(exp_jump)
    assert row18["btc_vpin_1m"] == pytest.approx(exp_btc_vpin)
    assert row18["eth_vpin_1m"] == pytest.approx(exp_eth_vpin)
    # Other columns causally forward-filled from 2026-11-17
    for col in MF.MULTIMODAL_COLS[4:]:
        assert row18[col] == pytest.approx(mm_init.loc[pd.Timestamp("2026-11-17T00:00:00Z"), col])


def test_refresh_multimodal_zero_volume_vpin_fallback(tmp_path):
    btc_csv = str(tmp_path / "BTC-USD_1D.csv")
    eth_csv = str(tmp_path / "ETH-USD_1D.csv")
    mm_csv = str(tmp_path / "multimodal_1D.csv")
    prices_d = tmp_path / "prices"
    prices_d.mkdir()

    _write_sample_ohlcv(btc_csv, ["2026-11-18"], base=64000.0)
    _write_sample_ohlcv(eth_csv, ["2026-11-18"], base=1800.0)

    t18 = int(pd.Timestamp("2026-11-18T00:00:00Z").timestamp() * 1000)
    _write_gz_rows(
        prices_d / "2026-11-18.jsonl.gz",
        [
            {"t": t18, "sym": "BTCUSDT", "mid": 100.0},
            {"t": t18 + 60_000, "sym": "BTCUSDT", "mid": 101.0},
            {"t": t18 + 120_000, "sym": "BTCUSDT", "mid": 102.0},
            {"t": t18 + 180_000, "sym": "BTCUSDT", "mid": 101.0},
            {"t": t18, "sym": "ETHUSDT", "mid": 200.0},
            {"t": t18 + 60_000, "sym": "ETHUSDT", "mid": 198.0},
            {"t": t18 + 120_000, "sym": "ETHUSDT", "mid": 196.0},
        ],
    )

    res = RDC.refresh_multimodal_csv(
        mm_csv,
        btc_csv,
        eth_csv,
        prices_dir=str(prices_d),
        fetch_public=False,
        now_utc=pd.Timestamp("2026-11-19T00:05:00Z"),
    )
    assert res["live_feeds"]["prices_1m"] == 1
    out = MF.load_multimodal_csv(mm_csv)
    row18 = out.loc[pd.Timestamp("2026-11-18T00:00:00Z")]
    # BTC signs: +1, +1, -1 -> sum=1 / count(|r|>0)=3
    assert row18["btc_vpin_1m"] == pytest.approx(1.0 / 3.0)
    # ETH signs: -1, -1 -> -1.0
    assert row18["eth_vpin_1m"] == pytest.approx(-1.0)


def test_refresh_multimodal_deribit_dvol_and_defillama_mocked(tmp_path, monkeypatch):
    btc_csv = str(tmp_path / "BTC-USD_1D.csv")
    eth_csv = str(tmp_path / "ETH-USD_1D.csv")
    mm_csv = str(tmp_path / "multimodal_1D.csv")

    _write_sample_ohlcv(btc_csv, ["2026-11-17", "2026-11-18"], base=64000.0)
    _write_sample_ohlcv(eth_csv, ["2026-11-17", "2026-11-18"], base=1800.0)

    mm_init = pd.DataFrame(
        [{col: 0.45 if col == "btc_rv_1m_ann" else 0.77 for col in MF.MULTIMODAL_COLS}],
        index=pd.to_datetime(["2026-11-17"], utc=True),
    )
    mm_init.to_csv(mm_csv)

    # Build 25 daily Deribit DVOL candles up through 2026-11-18 (plus an incomplete 2026-11-19 bar to exclude)
    dvol_days = pd.date_range("2026-10-25", "2026-11-19", freq="D", tz="UTC")
    dvol_vals = [40.0 + (i % 5) for i in range(len(dvol_days) - 1)] + [99.0]
    dvol_payload = {
        "result": {
            "data": [
                [int(d.timestamp() * 1000), v - 1.0, v + 1.0, v - 2.0, v]
                for d, v in zip(dvol_days, dvol_vals)
            ]
        }
    }

    # DeFiLlama stablecoins with string epoch seconds and peggedUSD (plus fallback check on earlier days)
    d_target = pd.Timestamp("2026-11-18T00:00:00Z")
    d_prev21 = d_target - pd.Timedelta(days=21)
    llama_payload = [
        {"date": str(int(d_prev21.timestamp())), "totalCirculatingUSD": {"peggedUSD": 200_000_000_000.0}},
        {"date": int(d_target.timestamp()), "totalCirculatingUSD": {"peggedEUR": 5_000_000_000.0, "peggedUSD_alt": 205_000_000_000.0}},
    ]

    captured_urls: list[str] = []

    class _FakeResp:
        def __init__(self, obj):
            self._raw = json.dumps(obj).encode("utf-8")

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def read(self):
            return self._raw

    def _fake_urlopen(req, timeout=None):
        captured_urls.append(req.full_url)
        assert timeout == pytest.approx(8.0)
        if "deribit.com" in req.full_url:
            return _FakeResp(dvol_payload)
        if "stablecoins.llama.fi" in req.full_url:
            return _FakeResp(llama_payload)
        raise AssertionError(f"unexpected URL: {req.full_url}")

    monkeypatch.setattr(RDC.urllib.request, "urlopen", _fake_urlopen)

    res = RDC.refresh_multimodal_csv(
        mm_csv,
        btc_csv,
        eth_csv,
        prices_dir=None,
        fetch_public=True,
        now_utc=pd.Timestamp("2026-11-19T08:00:00Z"),
    )
    assert len(captured_urls) == 2
    assert any("get_volatility_index_data" in u and "currency=BTC" in u and "resolution=1D" in u for u in captured_urls)
    assert any("stablecoins.llama.fi/stablecoincharts/all" in u for u in captured_urls)
    assert res["added_rows"] == 1
    assert res["live_feeds"]["prices_1m"] == 0
    assert res["live_feeds"]["deribit_dvol"].startswith("ok (")
    assert res["live_feeds"]["defillama_stable"].startswith("ok (")

    out = MF.load_multimodal_csv(mm_csv)
    # Historical row untouched
    pd.testing.assert_frame_equal(out.loc[mm_init.index], mm_init, check_names=False)

    # Expected DVOL computations using only completed days up to 2026-11-18
    dvol_s = pd.Series(dvol_vals[:-1], index=dvol_days[:-1], dtype="float64")
    dvol_18 = float(dvol_s.loc[d_target])
    win20 = dvol_s.iloc[-20:]
    exp_z20 = float((dvol_18 - win20.mean()) / (win20.std() + 1e-12))
    # Prior carried btc_rv_1m_ann is 0.45 because prices_dir=None
    exp_vrp = (dvol_18 / 100.0) - 0.45
    exp_stable = (210_000_000_000.0 / 200_000_000_000.0) - 1.0

    row18 = out.loc[d_target]
    assert row18["btc_vrp"] == pytest.approx(exp_vrp)
    assert row18["dvol_z20"] == pytest.approx(exp_z20)
    assert row18["stable_flow_21d"] == pytest.approx(exp_stable)
    # Unfetched columns causally forward-filled from 2026-11-17
    assert row18["btc_fund_z20"] == pytest.approx(0.77)
    assert row18["qqq_ret21"] == pytest.approx(0.77)


def test_refresh_multimodal_offline_rest_fallback(tmp_path, monkeypatch):
    btc_csv = str(tmp_path / "BTC-USD_1D.csv")
    eth_csv = str(tmp_path / "ETH-USD_1D.csv")
    mm_csv = str(tmp_path / "multimodal_1D.csv")
    prices_d = tmp_path / "prices"
    prices_d.mkdir()

    _write_sample_ohlcv(btc_csv, ["2026-11-17", "2026-11-18"], base=64000.0)
    _write_sample_ohlcv(eth_csv, ["2026-11-17", "2026-11-18"], base=1800.0)

    mm_init = pd.DataFrame(
        [{col: 0.33 for col in MF.MULTIMODAL_COLS}],
        index=pd.to_datetime(["2026-11-17"], utc=True),
    )
    mm_init.to_csv(mm_csv)

    t18 = int(pd.Timestamp("2026-11-18T00:00:00Z").timestamp() * 1000)
    _write_gz_rows(
        prices_d / "2026-11-18.jsonl.gz",
        [
            {"t": t18, "sym": "BTCUSDT", "cb": 64000.0, "cb_vol": 64000.0},
            {"t": t18 + 60_000, "sym": "BTCUSDT", "cb": 64640.0, "cb_vol": 64640.0},
        ],
    )

    def _fail_urlopen(req, timeout=None):
        raise RDC.urllib.error.URLError("offline")

    monkeypatch.setattr(RDC.urllib.request, "urlopen", _fail_urlopen)

    res = RDC.refresh_multimodal_csv(
        mm_csv,
        btc_csv,
        eth_csv,
        prices_dir=str(prices_d),
        fetch_public=True,
        now_utc=pd.Timestamp("2026-11-19T02:00:00Z"),
    )
    assert res["added_rows"] == 1
    assert res["live_feeds"]["prices_1m"] == 1
    assert res["live_feeds"]["deribit_dvol"].startswith("error:")
    assert res["live_feeds"]["defillama_stable"].startswith("error:")

    out = MF.load_multimodal_csv(mm_csv)
    row18 = out.loc[pd.Timestamp("2026-11-18T00:00:00Z")]
    # Local 1m BTC features computed; missing ETH and failed REST columns forward-filled causally
    assert row18["btc_rv_1m_ann"] > 0.0
    assert row18["eth_vpin_1m"] == pytest.approx(0.33)
    assert row18["btc_vrp"] == pytest.approx(0.33)
    assert row18["dvol_z20"] == pytest.approx(0.33)
    assert row18["stable_flow_21d"] == pytest.approx(0.33)


def test_main_threads_prices_dir_and_fetch_public(tmp_path, monkeypatch, capsys):
    daily_d = tmp_path / "daily_csv"
    prices_d = tmp_path / "prices"
    daily_d.mkdir()
    prices_d.mkdir()

    btc_csv = str(daily_d / "BTC-USD_1D.csv")
    eth_csv = str(daily_d / "ETH-USD_1D.csv")
    mm_csv = str(daily_d / "multimodal_1D.csv")
    _write_sample_ohlcv(btc_csv, ["2026-11-17"], base=64000.0)
    _write_sample_ohlcv(eth_csv, ["2026-11-17"], base=1800.0)
    pd.DataFrame(
        [{col: 0.1 for col in MF.MULTIMODAL_COLS}],
        index=pd.to_datetime(["2026-11-17"], utc=True),
    ).to_csv(mm_csv)

    t18 = int(pd.Timestamp("2026-11-18T00:00:00Z").timestamp() * 1000)
    _write_gz_rows(
        prices_d / "2026-11-18.jsonl.gz",
        [
            {"t": t18, "sym": "BTCUSDT", "cb": 64000.0, "cb_vol": 64000.0},
            {"t": t18 + 60_000, "sym": "BTCUSDT", "cb": 64640.0, "cb_vol": 64640.0},
            {"t": t18, "sym": "ETHUSDT", "cb": 1800.0, "cb_vol": 18000.0},
            {"t": t18 + 60_000, "sym": "ETHUSDT", "cb": 1818.0, "cb_vol": 18180.0},
        ],
    )

    def _fail_urlopen(req, timeout=None):
        raise RDC.urllib.error.URLError("offline")

    monkeypatch.setattr(RDC.urllib.request, "urlopen", _fail_urlopen)
    monkeypatch.setattr(RDC.time, "sleep", lambda _s: None)
    monkeypatch.setattr(
        "sys.argv",
        [
            "refresh_daily_csv.py",
            "--daily-csv-dir",
            str(daily_d),
            "--prices-dir",
            str(prices_d),
            "--fetch-public",
        ],
    )

    RDC.main()
    stdout = capsys.readouterr().out
    assert "refresh_daily_csv |" in stdout
    assert "prices_1m=" in stdout
    assert "dvol=" in stdout
    assert "stable=" in stdout


def test_iter_price_rows_skips_stale_outage_rows(tmp_path):
    p = tmp_path / "2026-01-02.jsonl.gz"
    t0 = 1767312000000
    rows = [{"t": t0, "sym": "BTCUSDT", "cb": 100.0},
            {"t": t0 + 1000, "sym": "BTCUSDT", "cb": 100.0, "stale": True},
            {"t": t0 + 2000, "sym": "BTCUSDT", "cb": 101.0, "stale": False}]
    _write_gz_rows(p, rows)
    got = list(RDC._iter_price_rows_gz(str(p)))
    assert [r["t"] for r in got] == [t0, t0 + 2000]
