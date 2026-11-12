"""Synthetic tests for strategies19 (PREREG19). No real data."""
import numpy as np
import pandas as pd

import strategies19 as S


def setup(days=10, rate=0.0001, price=100.0):
    idx = pd.date_range("2021-01-01", periods=days, freq="D", tz="UTC")
    daily = pd.DataFrame({"o": price, "h": price, "l": price, "c": price}, index=idx)
    ft = pd.date_range("2021-01-01", periods=days * 3, freq="8h", tz="UTC")
    return daily, pd.Series(rate, index=ft)


def test_flat_price_collects_funding_minus_entry():
    daily, f = setup()
    b = S.coin_returns(daily, f, "C1", S.BASE)
    assert abs(b["r"].iloc[0] + (3e-4 + 7e-4) / 1.5) < 1e-12  # open the pair at the first close
    assert abs(b["r"].iloc[1] - 3 * 0.0001 / 1.5) < 1e-12      # three payments on N = E/1.5
    assert not b["held"].iloc[0] and b["held"].iloc[1]


def test_stress_haircut_only_on_received():
    daily, f = setup(rate=0.0001)
    assert abs(S.coin_returns(daily, f, "C1", S.STRESS)["r"].iloc[2] - 0.75 * 3e-4 / 1.5) < 1e-12
    daily, f = setup(rate=-0.0001)
    assert abs(S.coin_returns(daily, f, "C1", S.STRESS)["r"].iloc[2] + 3e-4 / 1.5) < 1e-12


def test_liquidation_and_rebalance_cost():
    daily, f = setup(rate=0.0)
    daily.loc[daily.index[3], ["h", "c"]] = [151.0, 110.0]
    b = S.coin_returns(daily, f, "C1", S.BASE)
    assert b["liq"].sum() == 1
    want = -(0.10 * (3e-4 + 7e-4) + 0.01 + 7e-4) / 1.5
    assert abs(b["r"].iloc[3] - want) < 1e-12


def test_c2_switches_on_trailing_rate():
    daily, f = setup(days=20, rate=0.00005)
    f[f.index >= pd.Timestamp("2021-01-10", tz="UTC")] = 0.0005
    b = S.coin_returns(daily, f, "C2", S.BASE)
    assert not b["held"].iloc[:9].any()  # 0.005% < 0.01%
    assert b["held"].iloc[-1]
    assert (b["r"] < 0).sum() >= 1       # paid to open


def test_stats_and_pass():
    idx = pd.date_range("2020-01-01", "2026-01-01", freq="D", tz="UTC")
    r = pd.Series(0.0002, index=idx)
    st = S.stats(r)
    assert abs(st["ann"] - (1.0002 ** 365 - 1)) < 1e-9 and st["max_dd"] == 0.0
    assert all(v > 0 for v in st["periods"].values())
    assert S.passes(st, 0.0)
    r.iloc[100] = -0.2
    assert not S.passes(S.stats(r), 0.0)  # -20% drawdown
    assert not S.passes(st, 1.5)
    assert np.isclose(S.portfolio([pd.DataFrame({"r": [0.1]}), pd.DataFrame({"r": [0.3]})]).iloc[0], 0.2)
