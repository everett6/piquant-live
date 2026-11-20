import math

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies26 as S

IDX = pd.date_range("2021-01-01", periods=200, freq="D", tz="UTC")
FREE = S.OptCosts("free", 0.0, 0.0, 0.0)


def ser(x):
    return pd.Series(np.asarray(x, dtype=float), index=IDX[:len(x)])


def frame(c):
    c = ser(c)
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def uptrend(n=200, step=0.004):
    return 100.0 * np.cumprod(np.full(n, 1 + step))


def test_bs_put_known_value_limits_and_monotone():
    tau = 30 / 365
    p = S.bs_put(100, 100, 0.6, tau)
    d1 = 0.6 * math.sqrt(tau) / 2
    assert abs(p - 100 * (2 * S._ncdf(d1) - 1)) < 1e-9 and 6.7 < p < 7.0
    assert S.bs_put(80, 100, 0.6, 0) == 20 and S.bs_put(120, 100, 0.0, tau) == 0
    assert S.bs_put(100, 100, 0.8, tau) > p > S.bs_put(100, 100, 0.4, tau)
    assert S.bs_put(50, 100, 0.6, tau) >= 50  # never below intrinsic on a futures price


def test_flat_price_loses_exactly_the_premium_and_rolls_at_expiry():
    c, iv, on = ser(np.full(100, 100.0)), ser(np.full(100, 0.6)), np.ones(100, dtype=bool)
    ov = S.put_overlay(c, iv, on, 1.0, S.BASE)
    buys = np.flatnonzero(ov["premium"].to_numpy() > 0)
    assert list(buys) == [0, 30, 60, 90]
    ask = S.bs_put(100, 100, 0.66, 30 / 365)
    cost = (ask + min(3e-4 * 100, 0.125 * ask)) / 100
    assert np.isclose(ov["premium"].iloc[0], cost)
    # days 0..30 hold exactly one full put life: bought at 0, expired worthless at 30 (plus the new purchase spread)
    spread = (ask - S.bs_put(100, 100, 0.6, 30 / 365) + min(3e-4 * 100, 0.125 * ask)) / 100
    assert np.isclose(ov["ret"].iloc[:31].sum(), -cost - spread, atol=1e-12)


def test_zero_volatility_and_no_trend_cost_nothing():
    flat = frame(np.full(200, 100.0))
    r, ov = S.convex_sleeve(flat, ser(np.full(200, 0.6)), 1.0, lab.US_FUTURES, S.BASE)
    assert (r == 0).all() and not ov["held"].any()  # a flat price is never above its SMA50
    ov0 = S.put_overlay(flat["c"], ser(np.zeros(200)), np.ones(200, dtype=bool), 2.0, S.STRESS)
    assert (ov0["ret"] == 0).all() and (ov0["premium"] == 0).all()


def test_flash_crash_loss_is_bounded_by_the_option():
    c = uptrend()
    c[120:] = c[119] * 0.40  # one-day -60% close, stays there
    d, iv = frame(c), ser(np.full(200, 0.6))
    p1 = lab.position_returns(d, S5.m1_positions(d, 50, 2.0), lab.US_FUTURES)
    k1, _ = S.convex_sleeve(d, iv, 1.0, lab.US_FUTURES, S.BASE)
    k2, ov2 = S.convex_sleeve(d, iv, 2.0, lab.US_FUTURES, S.BASE)
    assert p1.iloc[120] < -1.0  # the linear 2x sleeve is wiped out
    gap = 1 - c[109] / c[119]  # the put was bought at close 109 (49, 79, 109): gains since then are not covered
    assert ov2["premium"].iloc[109] > 0 and 0.03 < gap < 0.05
    assert -(0.60 + gap + 0.07) < k1.iloc[120] < -0.60  # 1x fall + gap to the strike + at most the put's value
    assert -(2 * gap + 2 * 0.07) < k2.iloc[120] < 0  # full call: the gap to the strike and the put's value only
    assert not ov2["held"].iloc[120] and (k2.iloc[122:] == 0).all()  # put sold at close 120; lab charges the futures exit on day 121


def test_fresh_put_caps_next_day_loss_at_the_premium():
    c = np.full(60, 100.0)
    c[31:] = 30.0  # put bought at close 30 (strike 100), -70% the next day
    on = np.ones(60, dtype=bool)
    ov = S.put_overlay(ser(c), ser(np.full(60, 0.6)), on, 2.0, FREE)
    total = 2.0 * (30.0 / 100.0 - 1) + ov["ret"].iloc[31]
    assert -2 * 0.07 - 1e-9 < total <= 0.0  # 2x futures -140% plus puts = minus the premium only


def test_no_lookahead_future_data_does_not_change_past_returns():
    rng = np.random.default_rng(7)
    c = 100 * np.cumprod(1 + rng.normal(0.002, 0.03, 200))
    iv = 0.5 + 0.2 * rng.random(200)
    a, _ = S.convex_sleeve(frame(c), ser(iv), 1.0, lab.US_FUTURES, S.BASE)
    c2, iv2 = c.copy(), iv.copy()
    c2[150:] *= 0.5
    iv2[150:] = 2.0
    b, _ = S.convex_sleeve(frame(c2), ser(iv2), 1.0, lab.US_FUTURES, S.BASE)
    assert np.array_equal(a.iloc[:150].to_numpy(), b.iloc[:150].to_numpy())


def test_decision_at_close_t_earns_from_day_t_plus_1():
    c = np.full(40, 100.0)
    c[11] = 90.0
    on = np.zeros(40, dtype=bool)
    on[10:] = True
    ov = S.put_overlay(ser(c), ser(np.full(40, 0.6)), on, 1.0, FREE)
    assert (ov["ret"].iloc[:11] == 0).all()  # bought at close 10 at mid: no return on day 10
    assert ov["ret"].iloc[11] > 0.03  # the fall on day 11 is the first day it earns


def test_trend_off_sells_the_put_and_stops():
    c = uptrend(100)
    on = np.ones(100, dtype=bool)
    on[40:] = False
    ov = S.put_overlay(ser(c), ser(np.full(100, 0.6)), on, 1.0, S.BASE)
    assert ov["held"].iloc[39] and not ov["held"].iloc[40:].any()
    assert ov["payoff"].iloc[40] > 0 and (ov["ret"].iloc[41:] == 0).all()
    free = S.put_overlay(ser(c), ser(np.full(100, 0.6)), on, 1.0, FREE)
    assert ov["ret"].iloc[40] < free["ret"].iloc[40]  # selling pays the spread


def test_q_zero_is_p1_and_missing_iv_means_no_put():
    d = frame(uptrend())
    r0, _ = S.convex_sleeve(d, ser(np.full(200, 0.6)), 0.0, lab.US_FUTURES, S.BASE)
    p1 = lab.position_returns(d, S5.m1_positions(d, 50, 2.0), lab.US_FUTURES)
    assert np.array_equal(r0.to_numpy(), p1.to_numpy())
    iv = np.full(200, np.nan)
    iv[100:] = 0.6
    iv[150] = np.nan  # a later gap uses the last value
    _, ov = S.convex_sleeve(d, ser(iv), 1.0, lab.US_FUTURES, S.BASE)
    assert not ov["held"].iloc[:100].any() and ov["held"].iloc[100:].all()
    assert np.isfinite(ov["ret"]).all()


def test_stress_costs_more_and_funding_spike_only_hits_futures():
    rng = np.random.default_rng(3)
    d = frame(100 * np.cumprod(1 + rng.normal(0.003, 0.02, 200)))
    iv = ser(np.full(200, 0.6))
    base, ovb = S.convex_sleeve(d, iv, 1.0, lab.US_FUTURES, S.BASE)
    fund, ovf = S.convex_sleeve(d, iv, 1.0, lab.US_FUTURES_STRESS, S.BASE)
    opt, ovs = S.convex_sleeve(d, iv, 1.0, lab.US_FUTURES, S.STRESS)
    assert np.array_equal(ovb["ret"].to_numpy(), ovf["ret"].to_numpy())
    assert fund.sum() < base.sum() and opt.sum() < base.sum()
    assert ovs["premium"].sum() > ovb["premium"].sum()


def test_rally_keeps_the_upside_less_the_premium():
    d = frame(uptrend(200, 0.01))
    iv = ser(np.full(200, 0.6))
    p1 = lab.position_returns(d, S5.m1_positions(d, 50, 2.0), lab.US_FUTURES)
    k1, ov = S.convex_sleeve(d, iv, 1.0, lab.US_FUTURES, S.BASE)
    drag = p1.sum() - k1.sum()
    assert 0 < drag <= ov["premium"].sum() + 1e-12  # never loses more than the premiums paid
    assert (ov["ret"] > -0.08).all()


def test_realized_vol_stand_in():
    c = ser(100 * np.cumprod(1 + np.tile([0.02, -0.02], 100)))
    v = S.realized_vol_iv(c)
    assert v.iloc[:30].isna().all() and np.isclose(v.iloc[60], c.pct_change().iloc[31:61].std() * math.sqrt(365) * 1.2)
