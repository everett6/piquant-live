import numpy as np
import pandas as pd

import strategies5 as S5
import strategies35 as S

D = pd.date_range("2021-01-01", periods=600, freq="D", tz="UTC")


def daily(c, start=0):
    c = pd.Series(np.asarray(c, dtype=float), index=D[start:start + len(c)])
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0})


UP = 100 * np.cumprod(np.full(200, 1.004))
DOWN = 100 * np.cumprod(np.full(200, 0.996))


def test_entangled_is_two_when_both_on_one_when_alone_zero_when_off():
    i = daily(np.concatenate([UP, UP[-1] * np.cumprod(np.full(200, 1.004))]))
    j = daily(np.concatenate([UP, UP[-1] * np.cumprod(np.full(200, 0.996))]))
    p = S.entangled_positions(i, j)
    assert (p[60:200] == 2.0).all() and (p[260:] == 1.0).all()
    assert (S.entangled_positions(j, i)[260:] == 0.0).all()
    assert (p <= S5.m1_positions(i, 50, 2.0)).all()


def test_entangled_is_p1_until_the_other_coin_has_an_sma():
    i = daily(UP)
    j = daily(DOWN[:100], start=100)  # starts 100 days later and falls
    p, p1 = S.entangled_positions(i, j), S5.m1_positions(i, 50, 2.0)
    assert np.array_equal(p[:149], p1[:149]) and (p[149:] == 1.0).all()


def test_trend_age_counts_consecutive_closes_and_resets():
    c = np.concatenate([UP[:100], np.full(5, 1.0), UP[:100] * 3])
    age = S.trend_age(daily(c))
    assert age[48] == 0 and age[49] == 1 and age[99] == 51 and age[100] == 0
    k = int(np.flatnonzero(age[105:] > 0)[0]) + 105
    assert age[k] == 1 and age[k + 4] == 5


def test_coherence_ramp_values_and_bound():
    d = daily(UP)
    p, age = S.coherence_positions(d), S.trend_age(d)
    assert np.allclose(p, 2 * (1 - np.exp(-age / 5.0))) and (p <= S5.m1_positions(d, 50, 2.0)).all()
    assert np.isclose(p[49], 2 * (1 - np.exp(-0.2))) and p[-1] > 1.999


def test_entropy_is_zero_for_one_block_and_one_for_independent_coins():
    rng = np.random.default_rng(8)
    common = rng.normal(0, 0.02, 400)
    block = pd.DataFrame({f"c{k}": 100 * np.cumprod(1 + common * (1 + 0.5 * k)) for k in range(10)}, index=D[:400])
    indep = pd.DataFrame({f"c{k}": 100 * np.cumprod(1 + rng.normal(0, 0.02, 400)) for k in range(10)}, index=D[:400])
    eb, ei = S.market_entropy(block), S.market_entropy(indep)
    assert eb.iloc[:60].isna().all() and eb.iloc[60:].max() < 0.02
    assert 0.85 < ei.iloc[60:].min() and ei.iloc[60:].max() <= 1.0 + 1e-9
    assert S.market_entropy(indep.iloc[:, :7]).isna().all()  # fewer than 8 coins


def test_entropy_skips_coins_without_history_and_ignores_the_future():
    rng = np.random.default_rng(9)
    x = pd.DataFrame({f"c{k}": 100 * np.cumprod(1 + rng.normal(0, 0.02, 400)) for k in range(10)}, index=D[:400])
    late = x.copy()
    late.iloc[:300, 9] = np.nan
    assert late.pipe(S.market_entropy).iloc[60:].notna().all()  # 9 coins are enough
    y = x.copy()
    y.iloc[250:] *= rng.normal(1, 0.2, (150, 10))
    assert np.array_equal(S.market_entropy(x).iloc[:250].to_numpy(), S.market_entropy(y).iloc[:250].to_numpy(),
                          equal_nan=True)


def test_gate_low_uses_the_previous_year_only():
    sig = pd.Series(np.tile(np.arange(10.0), 60), index=D)  # 20th percentile of the last year is between 1 and 2
    g, late = S.gate_low(sig), sig.iloc[360:].to_numpy()
    assert not g[:350].any() and g[360:][late == 0].all() and not g[360:][late >= 2].any()
    low = pd.Series(5.0, index=D)
    low.iloc[400] = 1.0
    assert S.gate_low(low)[400] and not S.gate_low(low)[401]  # today's value is not in its own threshold
    assert not S.gate_low(pd.Series(1.0, index=D)).any()
    assert np.array_equal(S.gate_defined(sig).to_numpy(), np.arange(600) >= 350)
