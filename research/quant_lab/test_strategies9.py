"""Synthetic tests for round-9 code (python test_strategies9.py)."""
import numpy as np
import pandas as pd

import strategies9 as S9


def _arr(o, h, l, c):
    t = (np.arange(len(c)) * 60 * 10**9).astype(np.int64)
    return [np.asarray(x, float) for x in (o, h, l, c)] + [t]


def test_schedule():
    s = S9.ratchet_schedule()
    assert s[0] == 65 and s[4] == 65 and s[5] == 35 and s[15] == 12 and s[25] == 0 and len(s) == 46


def test_taker_short_ratchet_tp():
    # signal at bar 0; taker sells bar-1 open 100; bar 7 (k=6) is the first low under the 35+8 bps target
    c = [100, 100, 99.9, 99.85, 99.8, 99.75, 99.6, 99.5]
    o = [100] + c[:-1]
    h = [x + 0.01 for x in np.maximum(o, c)]
    l = [x - 0.01 for x in np.minimum(o, c)]
    o, h, l, c, t = _arr(o, h, l, c)
    trig = np.zeros(8, bool); trig[0] = True
    tr = S9.sim_short(o, h, l, c, t, trig, maker_entry=False, hold=45, sched_bps=S9.ratchet_schedule())
    r = tr.iloc[0]
    p = 100 * (1 - 1e-4)
    assert r.reason == "TP" and abs(r.gross - (43e-4)) < 1e-9
    assert abs(r.net - (43e-4 - 8e-4 - 0.1e-4 * 6 / 60)) < 1e-9 and abs(p - 99.99) < 1e-9


def test_maker_short_needs_bounce_and_stops():
    c = [100, 99.9, 99.8, 99.7, 99.6, 99.5, 99.4]
    o = [100] + c[:-1]
    h = list(np.maximum(o, c)); l = list(np.minimum(o, c))
    a = _arr(o, h, l, c)
    trig = np.zeros(7, bool); trig[0] = True
    assert S9.sim_short(*a, trig, maker_entry=True, hold=45, sched_bps=S9.ratchet_schedule()).empty  # never bounced
    h2 = h.copy(); h2[1] = 100.02; h2[2] = 100.4
    a2 = _arr(o, h2, l, c)
    tr = S9.sim_short(*a2, trig, maker_entry=True, hold=45, sched_bps=S9.ratchet_schedule())
    assert tr.iloc[0].reason == "SL" and abs(tr.iloc[0].gross - (100 - 100.3 * 1.0001) / 100) < 1e-9


def test_b_signal_bear_only_and_tp_at_vwap():
    n = 1600
    idx = pd.date_range("2024-01-01", periods=n, freq="min", tz="UTC")
    rng = np.random.default_rng(0)
    c = pd.Series(100 * np.exp(np.cumsum(rng.normal(0, 2e-4, n))), index=idx)
    c.iloc[-1] = c.iloc[-2] * 1.01  # +1% pump on the last bar
    df = pd.DataFrame({"o": c.shift(1).bfill(), "h": c, "l": c, "c": c, "v": 1.0})
    trig, off = S9.b_signal(df, np.full(n, -1))
    assert trig[-1] and off[-1] > 0.003
    trig_up, _ = S9.b_signal(df, np.full(n, 1))
    assert not trig_up.any()


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
