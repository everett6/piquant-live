"""Synthetic tests for round-12 code (python test_strategies12.py)."""
import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies12 as S12


def _daily(prices, start="2020-01-01"):
    idx = pd.date_range(start, periods=len(prices), freq="D", tz="UTC")
    c = pd.Series(prices, index=idx, dtype=float)
    return pd.DataFrame({"o": c.shift(1).fillna(c.iloc[0]), "h": c, "l": c, "c": c, "v": 1.0})


def test_brake_never_triggers_matches_two_sleeve():
    rng = np.random.default_rng(2)
    a = _daily(100 * np.exp(np.cumsum(rng.normal(0.002, 0.01, 300))))
    b = _daily(50 * np.exp(np.cumsum(rng.normal(0.002, 0.01, 300))))
    ref = S5.two_sleeve(lab.position_returns(a, S5.m1_positions(a, 20, 2.0), lab.US_FUTURES),
                        lab.position_returns(b, S5.m1_positions(b, 20, 2.0), lab.US_FUTURES))
    got = S12.braked_pair([a, b], 20, 2.0, lab.US_FUTURES, dd_on=0.99, dd_off=0.5)
    assert np.allclose(got.to_numpy(), ref.to_numpy(), atol=1e-12)


def test_brake_halves_leverage_after_drawdown():
    px = np.r_[np.linspace(100, 200, 80), np.linspace(200, 120, 20), np.linspace(120, 125, 30)]
    a = _daily(px)
    free = S12.braked_pair([a, a], 20, 2.0, lab.US_FUTURES, dd_on=0.99, dd_off=0.5)
    brk = S12.braked_pair([a, a], 20, 2.0, lab.US_FUTURES, dd_on=0.05, dd_off=0.01)
    assert (brk.iloc[:85] == free.iloc[:85]).sum() >= 80   # same until the drawdown
    assert brk.iloc[85:].abs().sum() < free.iloc[85:].abs().sum()


def test_n_sleeve_cash_before_listing():
    idx = pd.date_range("2020-01-01", periods=3, freq="D", tz="UTC")
    r = S12.n_sleeve([pd.Series(0.03, index=idx), pd.Series(0.03, index=idx), pd.Series(0.03, index=idx[2:])], idx)
    assert np.allclose(r.to_numpy(), [0.02, 0.02, 0.03])


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
