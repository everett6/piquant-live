import numpy as np
import pandas as pd

import strategies50 as S


def _idx(n=70, start="2020-01-01"):
    return pd.date_range(start, periods=n, freq="D", tz="UTC")


def test_weekly_mask_sundays():
    idx = _idx()
    m = S.decision_mask(idx, "weekly")
    assert all(d.dayofweek == 6 for d in idx[m]) and m.sum() == 10


def test_monthly_mask_last_day():
    idx = _idx()
    m = S.decision_mask(idx, "monthly")
    assert [str(d.date()) for d in idx[m]] == ["2020-01-31", "2020-02-29", "2020-03-10"]


def test_hold_between_carries_forward_and_ignores_later_changes():
    idx = _idx(14)
    pos = pd.Series(np.arange(14, dtype=float), index=idx)
    m = S.decision_mask(idx, "weekly")  # 2020-01-05 and 2020-01-12 are Sundays
    h = S.hold_between(pos, m)
    assert h.iloc[:4].eq(0).all() and h["2020-01-05":"2020-01-11"].eq(4).all() and h.iloc[-2:].eq(11).all()


def test_turnover():
    idx = _idx(365)
    pos = pd.Series(0.0, index=idx)
    pos.iloc[100:200] = 1.0
    assert np.isclose(S.turnover_per_year(pos), 2.0)
