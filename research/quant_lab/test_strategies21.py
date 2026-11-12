"""Synthetic tests for strategies21 (PREREG21). No real data."""
import os
import tempfile

import numpy as np
import pandas as pd

import strategies21 as S

D0 = int(pd.Timestamp("2027-01-01", tz="UTC").timestamp())  # a UTC midnight, unix s
NS = 1_000_000_000


def tr(px, qty, ts_s, buy):
    return {"px": np.asarray(px, float), "qty": np.asarray(qty, float),
            "ts": (np.asarray(ts_s, float) * NS).astype(np.int64), "buy": np.asarray(buy, bool)}


def test_pqbin_roundtrip_and_synthetic_refused():
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "x.pqbin")
        ts = np.array([D0 * NS, D0 * NS + 5], dtype=np.int64)
        S.write_pqbin(p, [60000.12, 60001.5], [0.001, 2.5], ts, [True, False])
        t = S.read_pqbin(p)
        assert np.allclose(t["px"], [60000.12, 60001.5]) and np.allclose(t["qty"], [0.001, 2.5])
        assert t["ts"].tolist() == ts.tolist() and t["buy"].tolist() == [False, True]
        S.write_pqbin(p, [1.0], [1.0], ts[:1], [False], real=False)
        try:
            S.read_pqbin(p)
            raise AssertionError("synthetic file accepted")
        except ValueError:
            pass


def test_minute_bars_and_gap_fill():
    t = tr([100, 102, 99, 101, 105], [1, 1, 1, 2, 1], [D0 + 1, D0 + 10, D0 + 20, D0 + 59, D0 + 150], [1] * 5)
    b = S.minute_bars(t, D0 * NS, 3)
    assert (b["o"][0], b["h"][0], b["l"][0], b["c"][0], b["v"][0]) == (100, 102, 99, 101, 5)
    assert np.isnan(b["c"][1]) and b["c"][2] == 105
    f = S.fill_gaps(b)
    assert (f["o"][1], f["h"][1], f["l"][1], f["c"][1]) == (101, 101, 101, 101)


def test_second_volumes():
    t = tr([10, 11, 12], [1, 2, 4], [D0, D0 + 0.5, D0 + 2], [True, False, True])
    buy, sell, last = S.second_volumes(t, D0, 4)
    assert buy.tolist() == [1, 0, 4, 0] and sell.tolist() == [2, 0, 0, 0]
    assert last[0] == 11 and np.isnan(last[1]) and last[2] == 12


def test_vpin_buckets_split_and_rolling():
    n = 2 * 86400
    buy, sell = np.zeros(n), np.zeros(n)
    buy[:86400] = 1.0                       # day 1: 86400 BTC, all buys -> V = 54 per bucket
    buy[86400:] = 0.5
    sell[86400:] = 0.5                      # day 2: balanced, V = 86400 / 1600 from day 1
    bt, imb = S.vpin_buckets(buy, sell, D0)
    assert len(bt) == 3200 and np.allclose(imb[:1600], 1.0) and np.allclose(imb[1600:], 0.0)
    assert bt[0] == D0 + 54                 # first bucket completes in second 53 (0-based), stamped at its end
    v = S.vpin_at(np.array([bt[48], bt[49], bt[1600 + 24], bt[-1]]), bt, imb)
    assert np.isnan(v[0]) and v[1] == 1.0 and abs(v[2] - 25 / 50) < 1e-12 and v[3] == 0.0
    # a boundary inside a second splits that second's buy and sell in proportion
    b2, s2 = np.zeros(86400), np.zeros(86400)
    b2[0], s2[1] = 1600.0, 1600.0           # V = 3200 / 1600 = 2: second 0 fills 800 buy buckets, then sells
    bt2, im2 = S.vpin_buckets(b2, s2, D0)
    assert len(bt2) == 1600 and np.allclose(im2, 1.0) and bt2[799] == D0 + 1 and bt2[800] == D0 + 2


def test_passive_markout_sign():
    last = np.full(100, np.nan)
    last[0], last[10] = 100.0, 101.0
    t = tr([100.0, 100.0], [1.0, 3.0], [D0, D0], [False, True])  # 1 passive buy, 3 passive sells
    m = S.passive_markout(t, last, D0, horizons=(10,))
    q_bps, q = m[10]
    assert q == 4 and abs(q_bps - (1 * 100 - 3 * 100)) < 1e-6   # +100 bps for the bid, -100 for the asks


def test_resting_bid_fill_and_markout():
    # minute 0 closes at 100; minute 1 trades 99.9 at 70 s (fills the 100 bid); price 101 from 130 s
    t = tr([100, 100.5, 99.9, 101, 101], [1] * 5, [D0 + 10, D0 + 59, D0 + 70, D0 + 130, D0 + 400], [1] * 5)
    bars = S.fill_gaps(S.minute_bars(t, D0 * NS, 7))
    _, _, last = S.second_volumes(t, D0, 7 * 60)
    r = S.resting_bid(t, bars, last, D0 * NS, horizons=(1, 5))
    assert r["fills"] == 1 and r["minutes"] == 6
    assert r[1][1] == 1 and abs(r[1][0] - (101 / 100.5 - 1) * 1e4) < 1e-9  # minute 1's bid is 100.5
    assert r[5][1] == 1 and abs(r[5][0] - r[1][0]) < 1e-9                 # 370 s: still 101
    assert S.resting_bid(t, bars, last, D0 * NS, horizons=(30,))[30][1] == 0  # 70 s + 1800 s: past the data


def test_summarize_and_pass():
    rows = pd.DataFrame({"net": [4e-4, 2e-4] * 60, "gross": [1e-3] * 120, "reason": ["TP"] * 120})
    st = S.summarize(rows)
    assert st["n"] == 120 and abs(st["mean_net_bps"] - 3) < 1e-9 and st["t"] > 2 and st["exits"] == {"TP": 120}
    stress = S.summarize(rows, S.STRESS_BPS)
    assert abs(stress["mean_net_bps"] + 3) < 1e-9
    assert S.passes(st, st, st) and not S.passes(st, st, stress)
    assert not S.passes(S.summarize(rows.iloc[:99]), st, st) and S.summarize(rows.iloc[:0]) == {"n": 0}
