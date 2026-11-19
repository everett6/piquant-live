import math

import numpy as np
import pandas as pd

import strategies27 as S27
import strategies46s as S

IDX = pd.date_range("2021-01-01", periods=400, freq="D", tz="UTC")
ZERO = pd.Series([0.0], index=IDX[:1])


def ser(x):
    return pd.Series(np.asarray(x, dtype=float), index=IDX[:len(x)])


def frame(c):
    c = ser(c)
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def test_rebalancing_earns_from_swings_that_go_nowhere():
    swing = np.tile([100.0, 200.0, 100.0, 50.0], 50)  # doubles, halves, halves, doubles: ends where it started
    flat_bh = ser(swing)
    r, n = S.rebalance_mix(flat_bh, flat_bh, ZERO, cost_bps=0.0)
    assert n > 0 and (1 + r).prod() > 1.05  # buy-and-hold of this path earns 0; rebalancing earns from the swings
    calm, n2 = S.rebalance_mix(ser(np.full(200, 100.0)), ser(np.full(200, 100.0)), ZERO)
    assert n2 == 0 and (calm == 0).all()


def test_rebalancing_holds_cash_at_the_bill_rate_and_charges_turnover():
    r, n = S.rebalance_mix(ser(np.full(10, 100.0)), ser(np.full(10, 100.0)), pd.Series([3.65], index=IDX[:1]))
    assert n == 0 and np.allclose(r.iloc[1:], 0.5 * 1e-4, rtol=1e-3)  # half the book in bills at 1 bp a day
    up = np.concatenate([[100.0], np.full(9, 140.0)])  # +40% on day 1: BTC weight 0.30 > 0.25 + 0.05
    r2, n2 = S.rebalance_mix(ser(up), ser(np.full(10, 100.0)), ZERO, cost_bps=10)
    assert n2 == 1 and r2.iloc[1] < 0.25 * 0.4  # gain less the rebalancing cost


def test_bought_ladder_matches_prereg27_when_always_open():
    rng = np.random.default_rng(1)
    c = ser(100 * np.cumprod(1 + rng.normal(0, 0.03, 200)))
    iv = ser(np.full(200, 0.6))
    a = S.gated_spread_ladder(c, iv, np.ones(200, bool), +1)
    b = S27.put_spread_ladder(c, iv)
    assert np.allclose(a["ret"], b["ret"]) and np.allclose(a["premium"], b["premium"])


def test_sold_ladder_is_the_mirror_less_both_spreads():
    rng = np.random.default_rng(2)
    c = ser(100 * np.cumprod(1 + rng.normal(0, 0.03, 200)))
    iv = ser(np.full(200, 0.6))
    mask = np.ones(200, bool)
    free = S.OptCosts("free", 0.0, 0.0, 0.0)
    buy, sell = S.gated_spread_ladder(c, iv, mask, +1, oc=free), S.gated_spread_ladder(c, iv, mask, -1, oc=free)
    assert np.allclose(buy["ret"], -sell["ret"])  # with no costs the seller earns exactly what the buyer loses
    cb, cs = S.gated_spread_ladder(c, iv, mask, +1), S.gated_spread_ladder(c, iv, mask, -1)
    assert cb["ret"].sum() + cs["ret"].sum() < 0  # with costs both sides lose the spread
    assert (cs["premium"] < cb["premium"]).all() or (cs["premium"] <= cb["premium"]).all()


def test_flat_market_seller_keeps_the_credit_and_closed_gate_does_nothing():
    c, iv = ser(np.full(80, 100.0)), ser(np.full(80, 0.6))
    mask = np.zeros(80, bool)
    mask[0] = True
    sold = S.gated_spread_ladder(c, iv, mask, -1)
    assert np.isclose(sold["ret"].iloc[:31].sum(), sold["premium"].iloc[0], atol=1e-12)  # expires worthless
    none = S.gated_spread_ladder(c, iv, np.zeros(80, bool), -1)
    assert (none["ret"] == 0).all() and (none["premium"] == 0).all()


def test_vrp_gates_need_the_trend_and_the_volatility_gap():
    rng = np.random.default_rng(3)
    c = 100 * np.cumprod(1 + 0.002 + rng.choice([-0.02, 0.02], 400))  # rising, realised vol about 38%
    d = frame(c)
    sell_hi, buy_hi = S.vrp_gates(d, ser(np.full(400, 0.70)))
    sell_lo, buy_lo = S.vrp_gates(d, ser(np.full(400, 0.20)))
    assert sell_hi[100:].mean() > 0.5 and not buy_hi.any()
    assert buy_lo[100:].mean() > 0.5 and not sell_lo.any()
    down = frame(100 * np.cumprod(1 - 0.008 + rng.choice([-0.02, 0.02], 400)))
    assert not S.vrp_gates(down, ser(np.full(400, 0.70)))[0][250:].any()  # no selling below the SMA


def test_ramp_short_only_in_established_downtrends():
    # long fall (both averages above the price), a short rally above SMA50 but under SMA200, then the fall resumes
    c = np.concatenate([100 * np.cumprod(np.full(250, 1.004)), 271 * np.cumprod(np.full(100, 0.99))])
    c = np.concatenate([c, c[-1] * np.cumprod(np.full(15, 1.03)), c[-1] * 1.5 * np.cumprod(np.full(35, 0.985))])
    d = frame(c)
    pos = S.ramp_short_positions(d)
    assert (pos[:250] == 0).all() and (pos <= 0).all() and pos[349] < -0.99
    s50 = d["c"].rolling(50).mean().to_numpy()
    k = next(i for i in range(366, 400) if c[i] < s50[i])  # first close back under SMA50 after the rally
    assert pos[k - 1] == 0 and math.isclose(pos[k], -(1 - math.exp(-1 / 5)))  # the ramp restarts small
    c2 = c.copy()
    c2[300:] = c2[299] * 3  # recovery above both averages
    assert (S.ramp_short_positions(frame(c2))[300:] == 0).all()
