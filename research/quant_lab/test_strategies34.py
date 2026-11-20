import numpy as np
import pandas as pd

import strategies5 as S5
import strategies30 as S30
import strategies34 as S

D = pd.date_range("2021-01-01", periods=200, freq="D", tz="UTC")
H = pd.date_range("2021-01-01", periods=1200, freq="6h", tz="UTC")


def daily(c):
    c = pd.Series(np.asarray(c, dtype=float), index=D[:len(c)])
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def bars(c):
    c = pd.Series(np.asarray(c, dtype=float), index=H[:len(c)])
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def test_interference_squares_the_vote_and_matches_the_split_at_full_agreement():
    rng = np.random.default_rng(3)
    b = bars(100 * np.cumprod(1 + rng.normal(0.0003, 0.01, 1200)))
    k, qa, e4 = S.clocks_on(b), S.interference_held(b), S30.split_held(b)
    assert set(np.unique(k)) <= {0.0, 1.0, 2.0, 3.0, 4.0} and len(np.unique(k)) >= 4
    assert np.allclose(qa, 2.0 * (k / 4) ** 2) and np.allclose(e4, 2.0 * k / 4)
    assert (qa <= e4 + 1e-12).all() and np.allclose(qa[k == 4], 2.0) and np.allclose(qa[k == 2], 0.5)


def test_interference_uses_nothing_after_the_bar_starts():
    rng = np.random.default_rng(4)
    c = 100 * np.cumprod(1 + rng.normal(0.0003, 0.01, 1200))
    c2 = c.copy()
    c2[900:] *= 0.3
    a, b = S.interference_held(bars(c)), S.interference_held(bars(c2))
    assert np.array_equal(a.iloc[:901].to_numpy(), b.iloc[:901].to_numpy())


def test_tunnel_formula_is_zero_below_the_sma_and_never_above_p1():
    rng = np.random.default_rng(5)
    d = daily(100 * np.cumprod(1 + rng.normal(0.002, 0.03, 200)))
    pos, p1 = S.tunnel_positions(d), S5.m1_positions(d, 50, 2.0)
    assert np.array_equal(pos > 0, p1 > 0) and (pos <= p1).all()
    i = int(np.flatnonzero(pos > 0)[-1])
    c = d["c"]
    gap = np.log(c.iloc[i] / c.iloc[i - 49:i + 1].mean())
    sig = c.pct_change().iloc[i - 29:i + 1].std()
    assert np.isclose(pos[i], 2.0 * (1 - np.exp(-2.0 * gap / sig)))


def test_tunnel_holds_more_the_further_above_the_line():
    base = np.full(200, 100.0)
    base[1::2] = 101.0  # steady noise so sigma is the same in both cases
    near, far = base.copy(), base.copy()
    near[-1], far[-1] = 101.0, 110.0
    pn, pf = S.tunnel_positions(daily(near))[-1], S.tunnel_positions(daily(far))[-1]
    assert 0 < pn < 1.5 and 1.99 < pf <= 2.0 and pn < pf


def test_tunnel_uses_nothing_after_the_close():
    rng = np.random.default_rng(6)
    c = 100 * np.cumprod(1 + rng.normal(0.002, 0.03, 200))
    c2 = c.copy()
    c2[150:] *= 2
    assert np.array_equal(S.tunnel_positions(daily(c))[:150], S.tunnel_positions(daily(c2))[:150])
