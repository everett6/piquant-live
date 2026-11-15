"""Synthetic unit tests for scripts/import_l2_history.py (zero network)."""
from __future__ import annotations

import gzip
import io
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import import_l2_history as L2


def test_l2_snapshot_delta_and_causal_sampling(tmp_path):
    lines = [
        json.dumps({
            "ts": 1000,
            "type": "snapshot",
            "data": {"b": [["100.0", "2.0"], ["99.5", "1.0"]], "a": [["101.0", "1.5"], ["102.0", "3.0"]]},
        }),
        # Update at 1500ms: delete bid 100.0, add bid 100.2 -> must NOT leak into t=1000 snapshot
        json.dumps({
            "ts": 1500,
            "type": "delta",
            "data": {"b": [["100.0", "0"], ["100.2", "4.0"]], "a": []},
        }),
        # Message at 2100ms crosses t=2000ms boundary -> t=2000 snapshot must reflect t=1500 state
        json.dumps({
            "ts": 2100,
            "type": "delta",
            "data": {"b": [], "a": [["101.0", "0"], ["100.5", "2.0"]]},
        }),
    ]
    out_gz = str(tmp_path / "book.jsonl.gz")
    n = L2.convert_bybit_ob_lines(lines, out_gz, sample_ms=1000, depth=10)
    assert n == 3

    with gzip.open(out_gz, "rt") as f:
        rows = [json.loads(line) for line in f]

    assert [r["t"] for r in rows] == [1000, 2000, 3000]
    # t=1000: snapshot bids [100.0, 99.5]
    assert rows[0]["b"] == [[100.0, 2.0], [99.5, 1.0]]
    assert rows[0]["a"] == [[101.0, 1.5], [102.0, 3.0]]
    # t=2000: reflects t=1500 delta (100.0 removed, 100.2 added), NOT t=2100 ask 100.5
    assert rows[1]["b"] == [[100.2, 4.0], [99.5, 1.0]]
    assert rows[1]["a"] == [[101.0, 1.5], [102.0, 3.0]]
    # t=3000: reflects t=2100 ask update (101.0 removed, 100.5 added)
    assert rows[2]["a"] == [[100.5, 2.0], [102.0, 3.0]]


def test_crossed_book_is_skipped(tmp_path):
    lines = [
        json.dumps({
            "ts": 1000,
            "type": "snapshot",
            "data": {"b": [["105.0", "1.0"]], "a": [["104.0", "1.0"]]},
        }),
    ]
    out_gz = str(tmp_path / "crossed.jsonl.gz")
    n = L2.convert_bybit_ob_lines(lines, out_gz, sample_ms=1000, depth=10)
    assert n == 0


def test_bybit_trades_conversion(tmp_path):
    csv_text = "id,timestamp,price,volume,side,rpi\n10,1794441600381,74013.7,0.5,buy,0\n11,1794441601172,74013.6,0.25,sell,0\n"
    out_gz = str(tmp_path / "ticks.jsonl.gz")
    n = L2.convert_bybit_trade_rows(io.StringIO(csv_text), out_gz)
    assert n == 2

    with gzip.open(out_gz, "rt") as f:
        rows = [json.loads(line) for line in f]
    assert rows[0] == {"t": 1794441600381, "p": 74013.7, "s": 0.5, "side": "b", "id": 10}
    assert rows[1] == {"t": 1794441601172, "p": 74013.6, "s": 0.25, "side": "s", "id": 11}


def test_download_retries_transient_error(monkeypatch):
    import urllib.error

    calls = {"n": 0}
    sleeps = []

    class FakeResp(io.BytesIO):
        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

    def fake_urlopen(req, timeout=120):
        calls["n"] += 1
        if calls["n"] < 3:
            raise urllib.error.URLError("Temporary failure in name resolution")
        return FakeResp(b"hello-l2")

    monkeypatch.setattr(L2.urllib.request, "urlopen", fake_urlopen)
    path = L2.download_to_temp("https://example.test/file.gz", retries=3, retry_delay=1.5, sleep_fn=sleeps.append)
    try:
        with open(path, "rb") as f:
            assert f.read() == b"hello-l2"
        assert calls["n"] == 3
        assert sleeps == [1.5, 3.0]
    finally:
        if os.path.exists(path):
            os.unlink(path)


def test_download_does_not_retry_404(monkeypatch):
    import pytest
    import urllib.error

    calls = {"n": 0}
    sleeps = []

    def fake_urlopen(req, timeout=120):
        calls["n"] += 1
        raise urllib.error.HTTPError("https://example.test/404", 404, "Not Found", {}, None)

    monkeypatch.setattr(L2.urllib.request, "urlopen", fake_urlopen)
    with pytest.raises(urllib.error.HTTPError):
        L2.download_to_temp("https://example.test/404", retries=3, retry_delay=1.0, sleep_fn=sleeps.append)
    assert calls["n"] == 1
    assert sleeps == []
