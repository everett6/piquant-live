import numpy as np
import pandas as pd

import strategies49 as S


def test_no_drift_equals_weighted_sum():
    idx = pd.date_range("2020-01-01", periods=90, freq="D", tz="UTC")
    ra = pd.Series(0.001, index=idx)
    r, _ = S.monthly_rebalanced(ra, ra.copy(), 0.2, 3.0)
    assert np.allclose(r, 0.001)  # identical assets never drift, so no cost


def test_drift_within_month_and_reset():
    idx = pd.date_range("2020-01-30", periods=4, freq="D", tz="UTC")  # Jan 30, 31, Feb 1, 2
    ra = pd.Series([0.0, 0.0, 0.0, 0.0], index=idx)
    rb = pd.Series([1.0, 0.0, 0.0, 0.0], index=idx)  # B doubles on day 0
    r, n = S.monthly_rebalanced(ra, rb, 0.5, 0.0)
    assert np.isclose(r.iloc[0], 0.5) and n == 1
    rb2 = pd.Series([1.0, 0.0, 1.0, 0.0], index=idx)
    r2, _ = S.monthly_rebalanced(ra, rb2, 0.5, 0.0)
    assert np.isclose(r2.iloc[2], 0.5)  # weights reset to 50/50 on Feb 1, so B's doubling adds half


def test_cost_charged_on_rebalance():
    idx = pd.date_range("2020-01-31", periods=2, freq="D", tz="UTC")
    ra = pd.Series([0.0, 0.0], index=idx)
    rb = pd.Series([1.0, 0.0], index=idx)
    r, _ = S.monthly_rebalanced(ra, rb, 0.5, 100.0)
    # value 1.5 split 0.5/1.0 -> 0.75/0.75 moves 0.5 = 1/3 of value; cost 1% of moved
    assert np.isclose(r.iloc[1], -(0.5 * 0.01) / 1.5)
