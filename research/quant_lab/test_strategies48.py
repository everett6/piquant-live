import numpy as np
import pandas as pd

import strategies48 as S


def _close(vals, start="2020-01-02"):
    return pd.Series(vals, index=pd.bdate_range(start, periods=len(vals)), dtype=float)


def test_positions_long_flat_and_ramp():
    up = _close(100 * 1.01 ** np.arange(120))
    p = S.dual_positions(up)
    assert ((p >= 0) & (p <= 1)).all()
    assert p.iloc[:49].eq(0).all() and p.iloc[-1] > 0.95


def test_flat_earns_tbills_over_weekend():
    c = _close([100.0] * 6)  # Thu..Thu, flat prices
    rate = pd.Series(3.65, index=c.index)
    r = S.etf_sleeve(c, pd.Series(0.0, index=c.index), rate, 2.0)
    gaps = c.index.to_series().diff().dt.days.fillna(1).to_numpy()
    assert np.allclose(r.to_numpy(), 0.0365 / 365 * gaps)


def test_long_gets_etf_return_and_pays_cost_once():
    c = _close([100, 100, 110, 121])
    pos = pd.Series([1.0, 1.0, 1.0, 1.0], index=c.index)
    r = S.etf_sleeve(c, pos, pd.Series(0.0, index=c.index), 10.0)
    assert np.isclose(r.iloc[1], -0.001)  # bought at first close, cost lands on the next day
    assert np.isclose(r.iloc[2], 0.10) and np.isclose(r.iloc[3], 0.10)


def test_on_calendar_zero_weekend():
    c = _close([1.0, 2.0, 3.0], start="2020-01-02")
    idx = pd.date_range("2020-01-01", "2020-01-07", freq="D", tz="UTC")
    s = S.on_calendar(pd.Series([0.1, 0.2, 0.3], index=c.index), idx)
    assert s["2020-01-04"] == 0 and s["2020-01-06"] == 0.3 and np.isclose(s.sum(), 0.6)
