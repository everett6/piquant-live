import numpy as np
import pandas as pd

import strategies5 as S5
import strategies31 as S31
import strategies32 as S

IDX = pd.date_range("2021-01-01", periods=200, freq="D", tz="UTC")


def test_growth_is_lagged_one_day_and_spans_thirty():
    u = pd.Series(np.arange(1.0, 201.0), index=IDX)
    g = S.supply_growth(u, IDX)
    assert g.iloc[:31].isna().all()
    assert np.isclose(g.iloc[31], u.iloc[30] / u.iloc[0] - 1) and np.isclose(g.iloc[100], u.iloc[99] / u.iloc[69] - 1)


def test_todays_and_future_supply_do_not_change_todays_gate():
    rng = np.random.default_rng(2)
    u = pd.Series(100 * np.cumprod(1 + rng.normal(0, 0.01, 200)), index=IDX)
    v = u.copy()
    v.iloc[150:] = 1e-6
    a, b = S.gate_shrinking(S.supply_growth(u, IDX)), S.gate_shrinking(S.supply_growth(v, IDX))
    assert np.array_equal(a[:151], b[:151]) and b[151]


def test_gate_is_on_when_flat_or_shrinking_and_off_without_a_signal():
    g = pd.Series([np.nan, 0.01, 0.0, -0.02], index=IDX[:4])
    assert list(S.gate_shrinking(g)) == [False, False, True, True]
    gap = pd.Series(np.linspace(100, 50, 200), index=IDX).drop(IDX[60])
    out = S.supply_growth(gap, IDX)
    assert np.isnan(out.iloc[61]) and np.isnan(out.iloc[91]) and out.iloc[62] < 0


def test_shrinking_supply_halves_a_trending_sleeve_only():
    c = pd.Series(100 * np.cumprod(np.full(200, 1.004)), index=IDX)
    d = pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})
    u = pd.Series(np.concatenate([np.linspace(100, 200, 100), np.linspace(200, 150, 100)]), index=IDX)
    gate = S.gate_shrinking(S.supply_growth(u, IDX))
    pos, p1 = S31.gated_positions(d, gate), S5.m1_positions(d, 50, 2.0)
    assert not gate[:120].any() and gate[135:].all()
    assert (pos[135:] == 1.0).all() and np.array_equal(pos[:120], p1[:120]) and (pos <= p1).all()
