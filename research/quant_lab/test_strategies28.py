import math

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies28 as S

IDX = pd.date_range("2021-01-01", periods=300, freq="D", tz="UTC")
FREE = S.OptCosts("free", 0.0, 0.0, 0.0)
TAU = 30 / 365


def ser(x):
    return pd.Series(np.asarray(x, dtype=float), index=IDX[:len(x)])


def frame(c):
    c = ser(c)
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


def walk(seed=5, n=300, mu=0.001, sd=0.03):
    return 100 * np.cumprod(1 + np.random.default_rng(seed).normal(mu, sd, n))


def test_fly_value_at_expiry_is_the_capped_move():
    k, kd, ku = 100.0, 80.0, 125.0
    s = np.array([100.0, 90.0, 60.0, 110.0, 200.0])
    v = S.fly_value(s, k, kd, ku, np.full(5, 0.6), 0.0)
    assert np.allclose(v, [0, 10, 20, 10, 25])
    live = S.fly_value(np.array([100.0]), k, kd, ku, np.array([0.6]), TAU)[0]
    assert 0 < live < 25


def test_no_lookahead_future_bars_do_not_change_past_returns():
    c, iv = walk(), 0.5 + 0.3 * np.random.default_rng(1).random(300)
    a = S.iron_fly_ladder(ser(c), ser(iv))
    c2, iv2 = c.copy(), iv.copy()
    c2[200:] *= 0.3
    iv2[200:] = 3.0
    b = S.iron_fly_ladder(ser(c2), ser(iv2))
    for col in a.columns:
        assert np.array_equal(a[col].to_numpy()[:200], b[col].to_numpy()[:200])
    assert np.array_equal(S5.m1_positions(frame(c), 50, 2.0)[:200], S5.m1_positions(frame(c2), 50, 2.0)[:200])


def test_flat_price_earns_the_credit():
    ov = S.iron_fly_ladder(ser(np.full(200, 100.0)), ser(np.full(200, 0.6)))
    assert (ov["paid"] == 0).all() and (ov["open"].iloc[30:] == 30).all()
    assert np.allclose(ov["ret"].iloc[60:], ov["credit"].iloc[60])  # steady state: one credit kept per day
    month = ov["ret"].iloc[60:90].sum()
    assert 0.05 < month < 0.25  # credit is a fraction of the 25% at risk


def test_zero_volatility_opens_nothing():
    ov = S.iron_fly_ladder(ser(np.full(100, 100.0)), ser(np.zeros(100)), oc=S.STRESS)
    assert (ov["ret"] == 0).all() and (ov["open"] == 0).all()


def test_crash_and_squeeze_losses_are_capped_at_f():
    for jump in (0.70, 0.40, 2.5):  # -30%, -60%, +150% in one day
        c = np.full(200, 100.0)
        c[100:] *= jump
        ov = S.iron_fly_ladder(ser(c), ser(np.full(200, 0.6)))
        hit = ov["ret"].iloc[100]
        credit = ov["credit"].iloc[70:100].sum()
        # one day can take back gains already booked on open tranches, so the day's bound is f plus their credit
        assert -(S.RISK_F + credit) <= hit < -0.05
        # the 30 tranches open at the jump (opened 70..99), from open to expiry: at most f in total
        paid = ov["paid"].iloc[100:130].sum()
        assert credit - paid >= -S.RISK_F - 1e-9
    c = np.full(200, 100.0)
    c[100:] = 250.0
    up = S.iron_fly_ladder(ser(c), ser(np.full(200, 0.6)), oc=FREE)
    assert np.isclose(up["credit"].iloc[70:100].sum() - up["paid"].iloc[100:130].sum(), -S.RISK_F, atol=1e-9)


def test_each_tranche_is_sized_to_lose_at_most_f_over_30():
    c, iv = walk(9), 0.4 + 0.5 * np.random.default_rng(2).random(300)
    ov = S.iron_fly_ladder(ser(c), ser(iv))
    v = iv * math.sqrt(TAU)
    wing = np.exp(2 * v) - 1
    credit_frac = ov["credit"].to_numpy() / ov["notional"].to_numpy()
    assert np.allclose(ov["notional"].to_numpy() * (wing - credit_frac), S.RISK_F / 30)
    assert (ov["paid"].to_numpy() <= ov["notional"].shift(30).fillna(0).to_numpy() * np.roll(wing, 30) + 1e-9)[30:].all()


def test_returns_add_up_to_credits_minus_payouts():
    c = walk(9)
    iv = 0.4 + 0.5 * np.random.default_rng(2).random(300)
    iv[269:] = 0.0  # no new tranches in the last 31 days, so every tranche has settled by the end
    for oc in (FREE, S.BASE, S.STRESS):
        ov = S.iron_fly_ladder(ser(c), ser(iv), oc=oc)
        assert (ov["open"].iloc[-1] == 0) and np.isclose(ov["ret"].sum(), ov["credit"].sum() - ov["paid"].sum(), atol=1e-9)
        assert ov["ret"].sum() >= -(S.RISK_F / 30) * (ov["credit"] > 0).sum() - 1e-9


def test_missing_volatility_opens_nothing_and_gaps_use_last_value():
    iv = np.full(300, np.nan)
    iv[100:] = 0.6
    iv[150] = np.nan
    ov = S.iron_fly_ladder(ser(walk(3)), ser(iv))
    assert (ov["ret"].iloc[:100] == 0).all() and (ov["open"].iloc[:100] == 0).all()
    assert ov["credit"].iloc[150] > 0 and np.isfinite(ov["ret"]).all()


def test_stress_costs_cut_the_credit_and_funding_only_hits_futures():
    d, iv = frame(walk(4)), ser(np.full(300, 0.6))
    free, base, stress = (S.iron_fly_ladder(d["c"], iv, oc=o) for o in (FREE, S.BASE, S.STRESS))
    assert free["ret"].sum() > base["ret"].sum() > stress["ret"].sum()
    pos = S5.m1_positions(d, 50, 2.0)
    p_b, p_s = lab.position_returns(d, pos, lab.US_FUTURES), lab.position_returns(d, pos, lab.US_FUTURES_STRESS)
    held = np.concatenate([[0.0], pos[:-1]])
    assert np.allclose(p_b - p_s, np.abs(held) * 0.9e-4 * 24)


def test_calm_market_earns_and_wild_market_loses():
    rng = np.random.default_rng(8)
    calm = 100 * np.cumprod(1 + rng.normal(0, 0.6 / math.sqrt(365) * 0.5, 300))  # realized = half of implied
    wild = 100 * np.cumprod(1 + rng.normal(0, 0.6 / math.sqrt(365) * 2.0, 300))  # realized = twice implied
    iv = ser(np.full(300, 0.6))
    assert S.iron_fly_ladder(ser(calm), iv)["ret"].sum() > 0 > S.iron_fly_ladder(ser(wild), iv)["ret"].sum()
