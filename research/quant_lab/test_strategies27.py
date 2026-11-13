import math

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies26 as S26
import strategies27 as S

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


def test_vector_put_matches_scalar_and_limits():
    s, k, sg = np.array([100.0, 80.0, 120.0, 100.0]), np.array([100.0, 100.0, 90.0, 100.0]), np.array([0.6, 0.9, 0.3, 0.0])
    v = S.bs_put_vec(s, k, sg, TAU)
    assert np.allclose(v, [S26.bs_put(a, b, c, TAU) for a, b, c in zip(s, k, sg)])
    assert np.array_equal(S.bs_put_vec(s, k, sg, 0.0), np.maximum(k - s, 0))
    assert np.isnan(S.bs_put_vec(np.array([100.0]), np.array([np.nan]), np.array([0.6]), TAU)[0])


def test_no_lookahead_future_bars_do_not_change_past_returns():
    c, iv = walk(), 0.5 + 0.3 * np.random.default_rng(1).random(300)
    a = S.put_spread_ladder(ser(c), ser(iv))
    c2, iv2 = c.copy(), iv.copy()
    c2[200:] *= 0.3
    iv2[200:] = 3.0
    b = S.put_spread_ladder(ser(c2), ser(iv2))
    for col in ("ret", "premium", "payoff", "open"):
        assert np.array_equal(a[col].to_numpy()[:200], b[col].to_numpy()[:200])
    d, d2 = frame(c), frame(c2)
    assert np.array_equal(S5.m1_positions(d, 50, 2.0)[:200], S5.m1_positions(d2, 50, 2.0)[:200])


def test_flat_price_steady_bleed_equals_the_premium_and_is_capped():
    c, iv = ser(np.full(200, 100.0)), ser(np.full(200, 0.6))
    ov = S.put_spread_ladder(c, iv)
    v = 0.6 * math.sqrt(TAU)
    buy, sell = S26.bs_put(100, 100, 0.66, TAU), S26.bs_put(100, 100 * math.exp(-v), 0.54, TAU)
    cost = buy + min(0.03, 0.125 * buy) - (sell - min(0.03, 0.125 * sell))
    assert np.isclose(ov["premium"].iloc[0], 0.5 / 30 * cost / 100)
    assert np.allclose(ov["ret"].iloc[60:], -0.5 / 30 * cost / 100)  # 30 tranches, each decaying to zero
    assert 0.02 < -ov["ret"].iloc[60:90].sum() < 0.035  # about q x 5-6% a month, the worst case
    assert (ov["payoff"] == 0).all() and (ov["open"].iloc[30:] == 30).all()


def test_zero_volatility_costs_nothing():
    ov = S.put_spread_ladder(ser(np.full(100, 100.0)), ser(np.zeros(100)), oc=S.STRESS)
    assert (ov["ret"] == 0).all() and (ov["premium"] == 0).all()


def test_flash_crash_pays_and_payout_is_bounded_by_the_width():
    c = np.full(200, 100.0)
    c[100:] = 70.0  # -30% in one day
    ov = S.put_spread_ladder(ser(c), ser(np.full(200, 0.6)), oc=FREE)
    width = 0.5 * (1 - math.exp(-0.6 * math.sqrt(TAU)))  # q x (K1-K2)/S, about 8% of equity
    assert 0.03 < ov["ret"].iloc[100] <= width
    life = ov["ret"].iloc[70:131].sum() + ov["premium"].iloc[70:100].sum()  # tranches opened 70..99, mark gains only
    assert life <= width + 1e-9
    assert np.isclose(ov["payoff"].iloc[101:131].sum(), width * 29 / 30, rtol=1e-9)  # 29 tranches opened 71..99
    p1 = lab.position_returns(frame(c), np.full(200, 2.0), lab.US_FUTURES)
    assert p1.iloc[100] < -0.59 and p1.iloc[100] + ov["ret"].iloc[100] > p1.iloc[100] + 0.03


def test_squeeze_loss_is_capped_at_the_premium_paid():
    c = np.full(200, 100.0)
    c[100:] = 250.0  # +150% in one day: a short would lose 150%
    ov = S.put_spread_ladder(ser(c), ser(np.full(200, 0.6)))
    open_premium = ov["premium"].iloc[70:100].sum()
    assert -open_premium <= ov["ret"].iloc[100] < 0
    assert ov["ret"].iloc[100] > -0.03
    for a in range(0, 170):
        assert ov["ret"].iloc[a:a + 30].sum() >= -ov["premium"].iloc[max(a - 30, 0):a + 30].sum() - 1e-12


def test_returns_add_up_to_payoffs_minus_premiums():
    c = walk(9)
    c[260:] = c[259] * 50  # final spike: every spread still open is worth nothing, so nothing is left unsettled
    iv = 0.4 + 0.5 * np.random.default_rng(2).random(300)
    iv[260:] = 0.0  # and tranches opened after the spike cost nothing
    for oc in (FREE, S.BASE, S.STRESS):
        ov = S.put_spread_ladder(ser(c), ser(iv), oc=oc)
        assert np.isclose(ov["ret"].sum(), ov["payoff"].sum() - ov["premium"].sum(), atol=1e-9)
        assert ov["ret"].sum() >= -ov["premium"].sum()  # the ladder can never lose more than it paid
        assert (ov["payoff"] >= 0).all() and (ov["premium"] >= 0).all()


def test_missing_volatility_opens_nothing_and_gaps_use_last_value():
    c = walk(3)
    iv = np.full(300, np.nan)
    iv[100:] = 0.6
    iv[150] = np.nan
    ov = S.put_spread_ladder(ser(c), ser(iv))
    assert (ov["ret"].iloc[:100] == 0).all() and (ov["open"].iloc[:100] == 0).all()
    assert ov["premium"].iloc[150] > 0 and np.isfinite(ov["ret"]).all()


def test_stress_costs_more_and_funding_only_hits_the_futures_leg():
    c = walk(4)
    d, iv = frame(c), ser(np.full(300, 0.6))
    base, stress = S.put_spread_ladder(d["c"], iv, oc=S.BASE), S.put_spread_ladder(d["c"], iv, oc=S.STRESS)
    assert stress["premium"].sum() > base["premium"].sum() and stress["ret"].sum() < base["ret"].sum()
    pos = S5.m1_positions(d, 50, 2.0)
    p_b, p_s = lab.position_returns(d, pos, lab.US_FUTURES), lab.position_returns(d, pos, lab.US_FUTURES_STRESS)
    held = np.concatenate([[0.0], pos[:-1]])
    assert np.allclose(p_b - p_s, np.abs(held) * (1.0 - 0.1) * 1e-4 * 24)  # 1 bp/h against 0.1 bp/h, per day held


def test_lab_turnover_cost_and_funding_accounting():
    c = np.array([100.0, 100.0, 110.0, 99.0, 99.0, 99.0])
    d = frame(c)
    pos = np.array([2.0, 2.0, 0.0, 1.0, 1.0, 0.0])
    r = lab.position_returns(d, pos, lab.US_FUTURES)
    held = np.array([0.0, 2.0, 2.0, 0.0, 1.0, 1.0])
    turn = np.array([0.0, 2.0, 0.0, 2.0, 1.0, 0.0])
    ret = np.array([0.0, 0.0, 0.10, -0.10, 0.0, 0.0])
    assert np.allclose(r, held * ret - turn * 7e-4 - held * 0.1e-4 * 24)


def test_splice_uses_stand_in_before_dvol_and_dvol_after():
    c = ser(walk(6))
    dvol = pd.Series(0.55, index=IDX[150:])
    iv = S.splice_iv(c, dvol)
    stand = S26.realized_vol_iv(c)
    assert iv.iloc[:30].isna().all()
    assert np.allclose(iv.iloc[30:150], stand.iloc[30:150]) and (iv.iloc[150:] == 0.55).all()
