import numpy as np
import pandas as pd

import strategies19 as S19
import strategies33 as S

IDX = pd.date_range("2021-01-01", periods=120, freq="D", tz="UTC")
F8 = pd.date_range("2021-01-01", periods=360, freq="8h", tz="UTC")


def frame(rng=None):
    c = pd.Series(100.0, index=IDX) if rng is None else pd.Series(100 * np.cumprod(1 + rng.normal(0, 0.02, 120)), index=IDX)
    return pd.DataFrame({"o": c, "h": c * 1.01, "l": c, "c": c, "v": 1.0})


def test_always_wanted_equals_prereg19_c1():
    rng = np.random.default_rng(4)
    d, f = frame(rng), pd.Series(rng.normal(1e-4, 2e-4, 360), index=F8)
    for costs in (S19.BASE, S19.STRESS):
        a = S.carry_when(d, f, np.ones(120, bool), costs)
        b = S19.coin_returns(d, f, "C1", costs)
        assert np.allclose(a["r"], b["r"]) and np.array_equal(a["held"], b["held"])


def test_want_idle_immediate_and_patient():
    pos = np.array([2, 0, 0, 2, 0, 0, 0, 0, 0, 0, 2, 0.0])
    assert list(S.want_idle(pos)) == [p == 0 for p in pos]
    w = S.want_idle(pos, 5)
    assert list(np.flatnonzero(w)) == [8, 9]  # flat at closes 4..8, then 5..9; the trend turns on at 10


def test_carry_earns_only_on_days_after_a_wanted_close_and_pays_each_switch():
    d, f = frame(), pd.Series(3e-4, index=F8)  # flat price: no rebalancing cost; 9 bps of funding a day
    want = np.zeros(120, bool)
    want[10:20] = True
    out = S.carry_when(d, f, want, S19.BASE)
    pair = (3e-4 + 7e-4) / 1.5
    assert list(np.flatnonzero(out["held"])) == list(range(11, 21))
    assert np.isclose(out["r"].iloc[10], -pair) and np.isclose(out["r"].iloc[20], 9e-4 / 1.5 - pair)
    assert np.allclose(out["r"].iloc[11:20], 9e-4 / 1.5) and (out["r"].iloc[21:] == 0).all()
    assert np.isclose(out["switch_cost"].sum(), 2 * pair) and np.isclose(out["funding"].sum(), 10 * 9e-4 / 1.5)


def test_future_wants_and_funding_do_not_change_the_past():
    rng = np.random.default_rng(5)
    d, f = frame(rng), pd.Series(rng.normal(1e-4, 2e-4, 360), index=F8)
    want = rng.random(120) < 0.5
    w2, f2 = want.copy(), f.copy()
    w2[81:] = ~w2[81:]
    f2.iloc[243:] = 0.01  # day 81 on
    a, b = S.carry_when(d, f, want, S19.BASE), S.carry_when(d, f2, w2, S19.BASE)
    assert np.array_equal(a["r"].iloc[:81].to_numpy(), b["r"].iloc[:81].to_numpy())


def test_bills_accrue_only_on_days_held_flat():
    pos = np.array([0, 0, 2, 2, 0.0])
    rate = pd.Series([3.65], index=IDX[:1])
    out = S.bills_on_idle(pos, rate, IDX[:5])
    assert np.allclose(out.to_numpy(), [0, 1e-4, 1e-4, 0, 0])
