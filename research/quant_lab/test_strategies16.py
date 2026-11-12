"""Synthetic tests for round-16 code (python test_strategies16.py)."""
import numpy as np
import pandas as pd

import lab
import strategies16 as S

NOCOST = lab.Costs("none", taker_bps=0.0, slip_bps=0.0, funding_bps_h=0.0, can_short=False)


def _daily(closes):
    idx = pd.date_range("2020-01-01", periods=len(closes), freq="D", tz="UTC")
    c = pd.Series(closes, index=idx, dtype=float)
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def _uptrend_with_crash(n=260, crash_at=240, size=0.15):
    t = np.arange(n)
    c = 100 * np.exp(0.003 * t + 0.01 * np.sin(t))  # smooth wiggle: no 2-sigma days except the crash
    c[crash_at:] *= (1 - size)
    return c


def test_d1_fires_on_crash_in_uptrend_and_exits_after_5():
    d = _daily(_uptrend_with_crash())
    tr = S.dip_trades(d, "D1")
    assert (240, 245) in tr
    assert all(x - e == 5 for e, x in tr)


def test_d1_needs_uptrend_but_d3_does_not():
    c = _uptrend_with_crash()[::-1].copy()  # downtrend
    k = np.arange(1, 61)
    c[200:] = c[199] * np.exp(-0.003 * k + 0.01 * np.sin(k))
    c[230:] *= 0.8
    d = _daily(c)
    assert not any(e == 230 for e, _ in S.dip_trades(d, "D1"))
    assert any(e == 230 for e, _ in S.dip_trades(d, "D3"))


def test_d2_exits_on_reclaiming_sma20_or_day_10():
    d = _daily(_uptrend_with_crash(size=0.2))
    tr = S.dip_trades(d, "D2")
    e, x = next(t for t in tr if t[0] >= 240)
    assert x - e <= 10
    c = d["c"]
    s20 = c.rolling(20).mean()
    assert x - e == 10 or c.iloc[x] > s20.iloc[x]


def test_no_overlap_and_trade_math():
    d = _daily(_uptrend_with_crash())
    tr = S.dip_trades(d, "D3")
    assert all(tr[i][1] < tr[i + 1][0] for i in range(len(tr) - 1))
    c = lab.Costs("c", taker_bps=6.0, slip_bps=1.0, funding_bps_h=0.1, can_short=False)
    t = S.trade_returns(d, [(240, 245)], c)
    g = d["c"].iloc[245] / d["c"].iloc[240] - 1
    assert abs(t["net"].iloc[0] - (g - 14e-4 - 0.1e-4 * 24 * 5)) < 1e-12


def test_positions_match_trade_return_without_costs():
    d = _daily(_uptrend_with_crash())
    pos = S.positions(len(d), [(240, 245)])
    r = lab.position_returns(d, pos, NOCOST)
    g = d["c"].iloc[245] / d["c"].iloc[240] - 1
    assert abs(np.prod(1 + r.iloc[241:246]) - 1 - g) < 1e-12


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
