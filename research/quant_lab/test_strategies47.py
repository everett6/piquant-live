import numpy as np
import pandas as pd

import strategies47 as S


def _daily(n=900, seed=0, drift=0.0):
    rng = np.random.default_rng(seed)
    idx = pd.date_range("2016-01-01", periods=n, freq="D", tz="UTC")
    c = 100 * np.exp(np.cumsum(drift + 0.03 * rng.standard_normal(n)))
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1 + rng.random(n)}, index=idx)


def test_features_use_only_past():
    d, o = _daily(), _daily(seed=1)
    f1 = S.features(d, o, 0)
    d2 = d.copy()
    d2.iloc[600:, d2.columns.get_loc("c")] *= 3  # change the future
    f2 = S.features(d2, o, 0)
    pd.testing.assert_frame_equal(f1.iloc[:600], f2.iloc[:600])


def test_forward_label_nan_at_end():
    y = S.forward_up(_daily()["c"])
    assert y.iloc[-S.HORIZON:].isna().all() and y.iloc[:-S.HORIZON].notna().all()


def test_walk_forward_no_future_labels():
    d, o = _daily(), _daily(seed=1)
    X = S.features(d, o, 0)
    y = S.forward_up(d["c"])
    dates = pd.Series(X.index, index=X.index)
    p1 = S.walk_forward_proba(X, y, dates, "2017-06-01")
    y2 = y.copy()
    y2[y2.index >= "2017-06-01"] = 1 - y2[y2.index >= "2017-06-01"]  # flip every label in the test period
    p2 = S.walk_forward_proba(X, y2, dates, "2017-06-01")
    first_block = p1.dropna().index[:63]
    assert np.allclose(p1[first_block], p2[first_block])  # first block never sees test-period labels
    assert p1[p1.index < "2017-06-01"].isna().all()


def test_learns_planted_signal():
    d, o = _daily(n=1500), _daily(seed=1, n=1500)
    X = S.features(d, o, 0)
    rng = np.random.default_rng(3)
    y = ((X["ret_7"] > 0) ^ (rng.random(len(X)) < 0.1)).astype(float).where(X["ret_7"].notna())
    dates = pd.Series(X.index, index=X.index)
    p = S.walk_forward_proba(X, y, dates, "2018-01-01")
    assert S.auc(y, p) > 0.8


def test_positions():
    p = pd.Series([0.6, 0.5, np.nan, 0.55])
    assert S.threshold_position(p).tolist() == [1.0, 0.0, 0.0, 1.0]
    pos = pd.Series([1.0, 1.0, -1.0, 0.5])
    assert S.meta_filter(pos, pd.Series([0.4, 0.5, 0.3, np.nan])).tolist() == [0.5, 1.0, -0.5, 0.5]
