import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies30 as S

N_DAYS = 200


def minute_frame(seed=1, days=N_DAYS, mu=0.0004, sd=0.012):
    """Hourly synthetic bars (stand in for 1-minute data; the functions only need bars finer than 6 hours)."""
    idx = pd.date_range("2021-01-01", periods=days * 24, freq="h", tz="UTC")
    c = 100 * np.cumprod(1 + np.random.default_rng(seed).normal(mu, sd, len(idx)))
    return pd.DataFrame({"o": c, "h": c, "l": c, "c": c, "v": 1.0}, index=idx)


def test_clock_daily_closes_at_its_hour():
    b6 = S.bars_6h(minute_frame())
    for h in S.CLOCKS:
        d = S.clock_daily(b6, h)
        assert (d.index.hour == h).all()
        last_bar = d.index[10] + pd.Timedelta(hours=18)
        assert d["c"].iloc[10] == b6.loc[last_bar, "c"]
    assert len(S.clock_daily(b6, 6)) == N_DAYS - 1  # the partial first and last days are dropped


def test_clock_zero_matches_p1_on_daily_bars():
    m = minute_frame()
    b6, daily = S.bars_6h(m), lab.resample(m, "1D")
    held = S.clock_held(b6, 0)
    p1 = pd.Series(S5.m1_positions(daily, 50, 2.0), index=daily.index)
    at_midnight = held[held.index.hour == 0]
    assert np.array_equal(at_midnight.iloc[1:].to_numpy(), p1.iloc[:-1].to_numpy())  # held from the next midnight
    for h in (6, 12, 18):  # and held unchanged for the whole 24 hours
        assert np.array_equal(held[held.index.hour == h].to_numpy(), at_midnight.to_numpy()[:len(held[held.index.hour == h])])


def test_no_lookahead_future_bars_do_not_change_positions():
    m = minute_frame(3)
    b6 = S.bars_6h(m)
    m2 = m.copy()
    cut = 150 * 24
    m2.iloc[cut:, :4] *= 0.3
    b6b = S.bars_6h(m2)
    k = cut // 6
    for h in S.CLOCKS:
        assert np.array_equal(S.clock_held(b6, h).to_numpy()[:k + 1], S.clock_held(b6b, h).to_numpy()[:k + 1])
    assert np.array_equal(S.split_held(b6).to_numpy()[:k + 1], S.split_held(b6b).to_numpy()[:k + 1])
    a, b = (S.grid_returns(x, S.split_held(x), lab.US_FUTURES) for x in (b6, b6b))
    assert np.array_equal(a.to_numpy()[:k], b.to_numpy()[:k])


def test_split_is_the_average_and_takes_quarter_steps():
    b6 = S.bars_6h(minute_frame(5))
    e = S.split_held(b6)
    assert set(np.unique(e)) <= {0.0, 0.5, 1.0, 1.5, 2.0}
    assert np.allclose(e, sum(S.clock_held(b6, h) for h in S.CLOCKS) / 4)
    r = [S.grid_returns(b6, S.clock_held(b6, h), lab.Costs("free", 0, 0, 0, True)) for h in S.CLOCKS]
    assert np.allclose(S.grid_returns(b6, e, lab.Costs("free", 0, 0, 0, True)), sum(r) / 4)  # no costs: exact average


def test_split_is_never_riskier_than_the_average_clock():
    for seed in range(4):
        b6 = S.bars_6h(minute_frame(seed, mu=0.0002))
        free = lab.Costs("free", 0, 0, 0, True)
        sds = [S.grid_returns(b6, S.clock_held(b6, h), free).std() for h in S.CLOCKS]
        assert S.grid_returns(b6, S.split_held(b6), free).std() <= np.mean(sds) + 1e-15


def test_flash_crash_hits_each_clock_by_its_position():
    m = minute_frame(7, mu=0.0008)
    t = 180 * 24 + 9  # 09:00 on day 180
    m.iloc[t:, :4] *= 0.70
    b6 = S.bars_6h(m)
    bar = b6.index[b6.index.get_indexer([m.index[t]], method="ffill")[0]]
    e = S.split_held(b6)
    r = S.grid_returns(b6, e, lab.Costs("free", 0, 0, 0, True))
    assert np.isclose(r.loc[bar], e.loc[bar] * (b6.loc[bar, "c"] / b6["c"].shift(1).loc[bar] - 1))
    assert r.loc[bar] >= 2.0 * (b6.loc[bar, "c"] / b6["c"].shift(1).loc[bar] - 1) - 1e-12  # never worse than full 2x


def test_flat_market_takes_no_position_and_costs_nothing():
    idx = pd.date_range("2021-01-01", periods=120 * 24, freq="h", tz="UTC")
    flat = pd.DataFrame({"o": 100.0, "h": 100.0, "l": 100.0, "c": 100.0, "v": 1.0}, index=idx)
    b6 = S.bars_6h(flat)
    assert (S.split_held(b6) == 0).all()
    assert (S.grid_returns(b6, S.split_held(b6), lab.US_FUTURES_STRESS) == 0).all()


def test_costs_turnover_and_stress_funding_accounting():
    b6 = S.bars_6h(minute_frame(9, mu=0.0006))
    held = S.split_held(b6)
    ret = b6["c"].pct_change().fillna(0)
    turn = held.diff().abs().fillna(held.abs())
    base, stress = (S.grid_returns(b6, held, c) for c in (lab.US_FUTURES, lab.US_FUTURES_STRESS))
    assert np.allclose(base, held * ret - turn * 7e-4 - held * 0.1e-4 * 6)
    assert np.allclose(base - stress, held * 0.9e-4 * 6)
    one = S.clock_held(b6, 0)
    assert turn.sum() <= one.diff().abs().fillna(one.abs()).sum() * 1.5  # the split does not multiply trading
