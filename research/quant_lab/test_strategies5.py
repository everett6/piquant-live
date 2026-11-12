"""Synthetic tests for round-5 code (python test_strategies5.py)."""
import numpy as np
import pandas as pd

import lab
import strategies5 as S5


def _daily(prices, start="2023-01-01"):
    idx = pd.date_range(start, periods=len(prices), freq="D", tz="UTC")
    c = pd.Series(prices, index=idx, dtype=float)
    return pd.DataFrame({"o": c.shift(1).fillna(c.iloc[0]), "h": c, "l": c, "c": c, "v": 1.0})


def test_positions_causal_and_gated():
    d = _daily(np.r_[np.full(10, 100.0), np.linspace(100, 120, 10), np.linspace(120, 90, 10)])
    pos = S5.m1_positions(d, 5, 2.0)
    assert pos[:4].sum() == 0               # warm-up
    assert pos[15] == 2.0 and pos[-1] == 0.0
    d2 = d.copy()
    d2.iloc[25:, d2.columns.get_loc("c")] *= 3  # future change must not alter earlier positions
    assert np.array_equal(S5.m1_positions(d2, 5, 2.0)[:25], pos[:25])


def test_complete_months_drops_partials():
    idx = pd.date_range("2023-01-15", "2023-04-10", freq="D", tz="UTC")
    m = S5.complete_months(pd.Series(0.01, index=idx))
    assert [x.month for x in m.index] == [2, 3]


def test_beat_stats_counts_strict_wins():
    idx = pd.date_range("2023-01-01", "2023-06-30", freq="D", tz="UTC")
    b = pd.Series(0.0, index=idx)
    s = pd.Series(0.0, index=idx)
    s[s.index.month.isin([1, 2, 3, 4])] = 0.001
    st = S5.beat_stats(s, b)
    assert st["months"] == 6 and st["beats"] == 4 and abs(st["beat_rate"] - 4 / 6) < 1e-12


def test_binomial():
    assert abs(S5.binom_p_one_sided(0, 10) - 1) < 1e-12
    assert abs(S5.binom_p_one_sided(10, 10) - 1 / 1024) < 1e-12
    assert S5.binom_p_one_sided(37, 60) < 0.05 < S5.binom_p_one_sided(36, 60)


def test_flat_strategy_beats_in_down_months_only():
    d = _daily(np.r_[np.linspace(100, 200, 120), np.linspace(200, 100, 120)])
    pos = np.zeros(len(d))  # always flat
    r = lab.position_returns(d, pos, lab.US_FUTURES)
    st = S5.beat_stats(r, d["c"].pct_change().fillna(0))
    assert 0.4 <= st["beat_rate"] <= 0.6


def test_ls_positions_short_below_sma():
    d = _daily(np.r_[np.full(10, 100.0), np.linspace(100, 120, 10), np.linspace(120, 80, 10)])
    pos = S5.ls_positions(d, 5, 2.0, 1.0)
    assert pos[:4].tolist() == [0, 0, 0, 0] and pos[15] == 2.0 and pos[-1] == -1.0
    r = lab.position_returns(d, pos, lab.US_FUTURES)
    assert r.iloc[-1] > 0  # short earns in the decline


def test_regime_months():
    m = pd.Series([-0.10, 0.0, 0.08], index=pd.date_range("2023-01-31", periods=3, freq="ME", tz="UTC"))
    assert S5.regime_months(m).tolist() == ["bear", "flat", "bull"]


def test_vt_positions_scale_with_vol_and_gate():
    rng = np.random.default_rng(0)
    calm = 100 * np.exp(np.cumsum(np.r_[rng.normal(0.002, 0.01, 60)]))
    d = _daily(calm)
    pos = S5.vt_positions(d, 20, 1.4, 3.0)
    assert pos[:29].sum() == 0
    vol = d["c"].pct_change().rolling(30).std().iloc[-1] * np.sqrt(365)
    if d["c"].iloc[-1] > d["c"].iloc[-20:].mean():
        assert abs(pos[-1] - min(3.0, 1.4 / vol)) < 1e-12


def test_two_sleeve_cash_before_listing():
    idx = pd.date_range("2016-01-01", periods=4, freq="D", tz="UTC")
    a = pd.Series([0.02, 0.02, 0.02, 0.02], index=idx)
    b = pd.Series([0.04, 0.04], index=idx[2:])
    r = S5.two_sleeve(a, b)
    assert r.tolist() == [0.01, 0.01, 0.03, 0.03]


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
