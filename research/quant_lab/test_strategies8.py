"""Synthetic tests for round-8 code (python test_strategies8.py)."""
import numpy as np
import pandas as pd

import strategies8 as S8


def _bars(o, h, l, c):
    n = len(c)
    t = (np.arange(n) * 60 * 10**9).astype(np.int64)
    return [np.asarray(x, float) for x in (o, h, l, c)] + [t]


def test_unfilled_limit_is_no_trade():
    o, h, l, c, t = _bars([100] * 10, [101] * 10, [100] * 10, [100] * 10)  # low never trades through 100
    side = np.zeros(10, int); side[0] = 1
    tr = S8.sim_maker(o, h, l, c, t, side, np.full(10, 0.005), np.full(10, 0.01), hold=5)
    assert tr.empty


def test_tp_maker_fees():
    o = [100, 100, 99.9, 100.2, 100.6]
    h = [100, 100, 100.0, 100.4, 100.7]
    l = [100, 99.9, 99.8, 100.1, 100.5]
    c = [100, 99.95, 99.9, 100.3, 100.6]
    o, h, l, c, t = _bars(o, h, l, c)
    side = np.zeros(5, int); side[0] = 1
    tr = S8.sim_maker(o, h, l, c, t, side, np.full(5, 0.005), np.full(5, 0.01), hold=10)
    r = tr.iloc[0]
    assert r.reason == "TP" and abs(r.gross - 0.005) < 1e-12
    assert abs(r.net - (0.005 - 4e-4 - 0.1e-4 * 3 / 60)) < 1e-9


def test_same_bar_counts_as_stop_and_gap_fills_at_open():
    o = [100, 100, 100, 98.5]
    h = [100, 100, 100.4, 98.6]
    l = [100, 99.9, 99.5, 98.0]
    c = [100, 99.95, 100, 98.2]
    o, h, l, c, t = _bars(o, h, l, c)
    side = np.zeros(4, int); side[0] = 1
    tr = S8.sim_maker(o, h, l, c, t, side, np.full(4, 0.005), np.full(4, 0.01), hold=10)
    assert tr.iloc[0].reason == "SL"      # bar 2 touched neither, bar 3 gapped through the 99 stop
    assert abs(tr.iloc[0].gross - (98.5 * (1 - 1e-4) - 100) / 100) < 1e-12
    h2, l2 = h.copy(), l.copy()
    h2[2], l2[2] = 100.6, 98.9          # both TP and SL inside one bar -> stop
    tr2 = S8.sim_maker(o, h2, l2, c, t, side, np.full(4, 0.005), np.full(4, 0.01), hold=10)
    assert tr2.iloc[0].reason == "SL" and abs(tr2.iloc[0].gross - (99 * (1 - 1e-4) - 100) / 100) < 1e-12


def test_m1_fades_shock_causally():
    rng = np.random.default_rng(0)
    c = pd.Series(100 * np.exp(np.cumsum(rng.normal(0, 1e-4, 3000))))
    c.iloc[2000:] *= 0.98  # a -2% shock at bar 2000
    side, tp, sl = S8.m1_signal(c, 6.0)
    assert side[2000] == 1 and side[:1441].sum() == 0
    assert abs(sl[2000] - abs(np.log(0.98))) < 1e-3 and abs(tp[2000] - sl[2000] / 2) < 1e-12


def test_regime_uses_previous_day():
    d = pd.Series(np.r_[np.full(5, 100.0), 110.0], index=pd.date_range("2024-01-01", periods=6, freq="D", tz="UTC"))
    idx = pd.date_range("2024-01-06", periods=2 * 1440, freq="min", tz="UTC")
    reg = S8.daily_regime_1m(d, 3, idx)
    assert reg[0] == -1 and reg[-1] == 1  # day-6 close (110 > SMA) is usable only from day 7


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
