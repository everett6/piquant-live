"""Synthetic-only check that the streaming round-17/18 loaders return exactly what the old list-of-dicts
loaders did (no real tick data is read)."""
from __future__ import annotations

import gzip
import json
import os

import pandas as pd

import run_round17 as R
import run_round18 as R18


def _write(path, rows):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with gzip.open(path, "wt") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")


def _old_load_trades(rows):
    df = pd.DataFrame(rows)
    df = df.drop_duplicates("id").sort_values(["t", "id"]).reset_index(drop=True)
    return df[(df["t"] >= R.ms(R.START)) & (df["t"] < R.ms(R.END))]


def test_streaming_loaders_match_old(tmp_path, monkeypatch):
    monkeypatch.setattr(R, "COLLECT", str(tmp_path))
    monkeypatch.setattr(R, "START", pd.Timestamp("2026-01-01", tz="UTC"))
    monkeypatch.setattr(R, "END", pd.Timestamp("2026-01-04", tz="UTC"))
    d0 = R.ms(R.START)
    day = 86_400_000
    trades = {
        "2026-01-01": [{"t": d0 - 5, "p": 1.0, "s": 1.0, "side": "b", "id": 1},          # before START
                       {"t": d0 + 10, "p": 2.0, "s": 0.5, "side": "s", "id": 3},
                       {"t": d0 + 10, "p": 2.1, "s": 0.4, "side": "b", "id": 2}],
        # 2026-01-02 missing on purpose
        "2026-01-03": [{"t": d0 + 10, "p": 9.9, "s": 9.9, "side": "b", "id": 3},          # duplicate id
                       {"t": d0 + 2 * day + 7, "p": 3.0, "s": 0.2, "side": "b", "id": 7},
                       {"t": d0 + 3 * day + 1, "p": 4.0, "s": 0.1, "side": "s", "id": 8}],  # at END
    }
    books = {
        "2026-01-01": [{"t": d0 + 1, "b": [[1.0, 2.0], [0.9, 1.0]], "a": [[1.1, 3.0]]},
                       {"t": d0 + 2, "b": [], "a": [[1.1, 3.0]]}],
        "2026-01-03": [{"t": d0 + 2 * day, "b": [[2.0, 1.0]], "a": [[2.2, 1.5], [2.3, 1.0]]}],
    }
    all_trades, all_books = [], []
    for d, rows in trades.items():
        _write(tmp_path / "ticks" / "BTC-USD" / f"{d}.jsonl.gz", rows)
        all_trades += rows
    for d, rows in books.items():
        _write(tmp_path / "book" / "BTC-USD" / f"{d}.jsonl.gz", rows)
        all_books += rows

    pd.testing.assert_frame_equal(R.load_trades("BTC-USD"), _old_load_trades(all_trades))
    assert R.read_days("book", "BTC-USD") == all_books
    bk = R.load_books("BTC-USD")
    assert bk["t"].tolist() == [d0 + 1, d0 + 2 * day]
    assert bk["bid_tot"].tolist() == [3.0, 1.0] and bk["ask_tot"].tolist() == [3.0, 2.5]
    b18 = R18.load_books("BTC-USD")
    assert b18["mid"].tolist() == [1.05, 2.1]


def test_load_trades_no_files(tmp_path, monkeypatch):
    monkeypatch.setattr(R, "COLLECT", str(tmp_path))
    assert R.load_trades("BTC-USD").empty


def test_rows_in_clock_wrong_windows_are_dropped(tmp_path, monkeypatch):
    monkeypatch.setattr(R, "COLLECT", str(tmp_path))
    monkeypatch.setattr(R, "START", pd.Timestamp("2026-01-01", tz="UTC"))
    monkeypatch.setattr(R, "END", pd.Timestamp("2026-01-02", tz="UTC"))
    d0 = R.ms(R.START)
    with open(tmp_path / "clock_offsets.jsonl", "w") as f:
        f.write(json.dumps({"at": "2026-01-01T00:20:03Z", "offset_ms": 1.5e8, "ok": False}) + "\n")  # wrong clock
        f.write(json.dumps({"at": "2026-01-01T00:40:00Z", "offset_ms": 0, "ok": False}) + "\n")      # failed, unmeasured
        f.write(json.dumps({"at": "2026-01-01T00:50:00Z", "offset_ms": 3.0, "ok": True}) + "\n")
    assert R.clock_wrong_windows() == {d0 // 600_000 + 2}
    tick = lambda t, i: {"t": t, "p": 1.0, "s": 1.0, "side": "b", "id": i}
    _write(tmp_path / "ticks" / "BTC-USD" / "2026-01-01.jsonl.gz",
           [tick(d0 + 19 * 60_000, 1), tick(d0 + 20 * 60_000 + 5_000, 2), tick(d0 + 41 * 60_000, 3)])
    _write(tmp_path / "book" / "BTC-USD" / "2026-01-01.jsonl.gz",
           [{"t": d0 + 25 * 60_000, "b": [[1.0, 1.0]], "a": [[1.1, 1.0]]},
            {"t": d0 + 31 * 60_000, "b": [[1.0, 1.0]], "a": [[1.1, 1.0]]}])
    assert R.load_trades("BTC-USD")["id"].tolist() == [1, 3]
    assert R.load_books("BTC-USD")["t"].tolist() == [d0 + 31 * 60_000]
    assert R18.load_books("BTC-USD")["t"].tolist() == [d0 + 31 * 60_000]
