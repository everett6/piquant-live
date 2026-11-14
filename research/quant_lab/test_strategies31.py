import numpy as np
import pandas as pd

import strategies5 as S5
import strategies31 as S

IDX = pd.date_range("2021-01-01", periods=500, freq="D", tz="UTC")
F8 = pd.date_range("2021-01-01", periods=1500, freq="8h", tz="UTC")


def frame(c):
    c = pd.Series(np.asarray(c, dtype=float), index=IDX[:len(c)])
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def test_signal_is_seven_day_mean_of_daily_means_and_needs_seven_days():
    f = pd.Series(np.arange(1500, dtype=float), index=F8)
    s = S.crowd_signal(f, IDX)
    assert s.iloc[:6].isna().all()
    assert np.isclose(s.iloc[6], f.iloc[:21].mean()) and np.isclose(s.iloc[10], f.iloc[12:33].mean())


def test_signal_uses_nothing_after_the_close():
    rng = np.random.default_rng(1)
    f = pd.Series(rng.normal(1e-4, 1e-4, 1500), index=F8)
    g = f.copy()
    g.iloc[900:] = 1.0  # day 300 on
    a, b = S.crowd_signal(f, IDX), S.crowd_signal(g, IDX)
    assert np.array_equal(a.iloc[:300].to_numpy(), b.iloc[:300].to_numpy(), equal_nan=True)
    assert np.array_equal(S.gate_rel(a)[:300], S.gate_rel(b)[:300])


def test_absolute_gate_is_strict_and_missing_signal_is_not_gated():
    sig = pd.Series([np.nan, 1e-4, 2e-4, 2.1e-4], index=IDX[:4])
    assert list(S.gate_abs(sig)) == [False, False, False, True]


def test_relative_gate_compares_with_the_previous_year_only():
    sig = pd.Series(np.tile(np.arange(10.0), 50), index=IDX)  # 0..9 repeating: 80th percentile is 7.2
    g = S.gate_rel(sig)
    assert not g[:350].any()  # fewer than 350 earlier values
    late = sig.iloc[360:].to_numpy()
    assert np.array_equal(g[360:], late > 7.2)
    assert not S.gate_rel(pd.Series(1.0, index=IDX)).any()  # a constant never exceeds its own past
    spike = pd.Series(1.0, index=IDX)
    spike.iloc[400] = 5.0
    assert S.gate_rel(spike)[400] and not S.gate_rel(spike)[401]


def test_gate_cuts_two_to_one_only_while_the_trend_is_on_and_never_raises():
    c = np.concatenate([100 * np.cumprod(np.full(250, 1.004)), 100 * np.cumprod(np.full(250, 0.996))])
    d = frame(c)
    p1 = S5.m1_positions(d, 50, 2.0)
    gate = np.zeros(500, dtype=bool)
    gate[100:120] = True
    gate[400:420] = True  # trend is off here
    g = S.gated_positions(d, gate)
    assert (g[100:120] == 1.0).all() and (p1[100:120] == 2.0).all()
    assert (g[400:420] == 0.0).all() and (g <= p1).all()
    rest = np.ones(500, dtype=bool)
    rest[100:120] = False
    assert np.array_equal(g[rest], p1[rest])


def test_no_funding_data_is_exactly_p1():
    d = frame(100 * np.cumprod(np.full(500, 1.003)))
    sig = S.crowd_signal(pd.Series(dtype=float, index=pd.DatetimeIndex([], tz="UTC")), d.index)
    for gate in (S.gate_abs(sig), S.gate_rel(sig)):
        assert np.array_equal(S.gated_positions(d, gate), S5.m1_positions(d, 50, 2.0))
