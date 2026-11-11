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


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
