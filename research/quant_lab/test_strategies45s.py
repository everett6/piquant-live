import numpy as np
import pandas as pd

import strategies45s as S

IDX = pd.date_range("2021-01-01", periods=200, freq="D", tz="UTC")
RATE = pd.Series(3.65, index=IDX[:1])  # 1 bp a day


def ser(x):
    return pd.Series(np.asarray(x, dtype=float), index=IDX[:len(x)])


def test_no_stop_when_the_drawdown_never_reaches_the_level():
    rng = np.random.default_rng(1)
    r = ser(rng.normal(0.001, 0.005, 200))
    out, ev, held = S.apply_shutoff(r, RATE, 0.21)
    assert ev == [] and held.all() and np.array_equal(out.to_numpy(), r.to_numpy())


def test_hard_stop_fires_at_the_level_pays_the_cost_then_earns_bills_for_good():
    r = np.zeros(200)
    r[10] = 0.10
    r[20:23] = -0.076  # 0.924^3 = 0.789 -> drawdown 21.1% from the peak on day 22 (14.6% on day 21)
    r[50:] = 0.02      # a rally the stopped book misses
    out, ev, held = S.apply_shutoff(ser(r), RATE, 0.21)
    assert ev == [("stop", str(IDX[22].date()))]
    assert np.isclose(out.iloc[22], (1 - 0.076) * (1 - 0.0015) - 1)
    assert held[:23].all() and not held[23:].any()
    assert np.allclose(out.iloc[23:], 1e-4)


def test_restart_after_thirty_days_resets_the_peak():
    r = np.zeros(200)
    r[5] = -0.25
    r[60:65] = -0.03  # 14% below the old peak's level would not matter: only the restart peak counts
    out, ev, held = S.apply_shutoff(ser(r), RATE, 0.21, restart_days=30)
    assert ev == [("stop", str(IDX[5].date())), ("restart", str(IDX[35].date()))]
    assert not held[6:36].any() and held[36:].all()
    assert np.isclose(out.iloc[35], (1 + 1e-4) * (1 - 0.0015) - 1)


def test_restart_peak_reset_lets_a_second_stop_fire_from_the_new_peak():
    r = np.zeros(200)
    r[5] = -0.25
    r[60:68] = -0.03  # 0.97^8 = 0.784 -> 21.6% below the restart peak on day 67
    out, ev, _ = S.apply_shutoff(ser(r), RATE, 0.21, restart_days=30)
    assert [e[0] for e in ev[:3]] == ["stop", "restart", "stop"] and ev[2][1] == str(IDX[67].date())
    assert ev[3] == ("restart", str(IDX[97].date()))


def test_overlay_uses_nothing_after_the_close():
    rng = np.random.default_rng(2)
    r = rng.normal(0.0, 0.03, 200)
    r2 = r.copy()
    r2[120:] = -0.2
    a, _, _ = S.apply_shutoff(ser(r), RATE, 0.21, restart_days=30)
    b, _, _ = S.apply_shutoff(ser(r2), RATE, 0.21, restart_days=30)
    assert np.array_equal(a.iloc[:120].to_numpy(), b.iloc[:120].to_numpy())


def test_bootstrap_is_reproducible_and_scales_with_volatility():
    rng = np.random.default_rng(3)
    calm = ser(rng.normal(0.001, 0.005, 200))
    wild = calm * 4
    a, b = S.bootstrap_year_drawdowns(calm, 300), S.bootstrap_year_drawdowns(calm, 300)
    assert np.array_equal(a, b) and len(a) == 300 and (a >= 0).all()
    assert S.bootstrap_year_drawdowns(wild, 300).mean() > 2.5 * a.mean()
    assert S.max_drawdown(np.array([0.1, -0.5, 0.2])) == 0.5
