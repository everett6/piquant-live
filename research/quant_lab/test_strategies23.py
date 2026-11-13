"""Synthetic tests for strategies23 (PREREG23). No real data."""
import numpy as np
import pandas as pd

import strategies23 as S


def daily(close):
    idx = pd.date_range("2030-01-01", periods=len(close), freq="1D", tz="UTC")
    return pd.DataFrame({"c": np.asarray(close, dtype=float)}, index=idx)


def test_calm_uptrend_is_long_after_100_returns():
    rng = np.random.default_rng(0)
    c = 100 * np.exp(np.cumsum(0.003 + rng.normal(0, 0.01, 300)))
    for down in (False, True):
        p = S.vol_exit_positions(daily(c), 2.0, down)
        assert (p[:100] == 0).all()          # needs 100 returns
        assert p[150:].mean() > 0.8 * 2.0    # mostly long in a calm uptrend


def test_vol_spike_forces_flat_even_above_sma():
    rng = np.random.default_rng(1)
    r = 0.004 + rng.normal(0, 0.005, 300)
    r[250:255] = [-0.08, 0.09, -0.07, 0.08, 0.02]   # violent swings, price still above its SMA50
    c = 100 * np.exp(np.cumsum(r))
    d = daily(c)
    assert c[254] > d["c"].rolling(50).mean().iloc[254]
    assert S.vol_exit_positions(d, 2.0, False)[254] == 0
    assert S.gate_days(d, False) >= 1


def test_downside_ignores_up_spikes():
    rng = np.random.default_rng(2)
    r = 0.002 + rng.normal(0, 0.006, 300)
    r[250:255] = [0.08, 0.07, 0.09, 0.06, 0.08]     # a sharp rally only
    d = daily(100 * np.exp(np.cumsum(r)))
    assert S.vol_exit_positions(d, 2.0, False)[254] == 0   # total-vol gate exits the rally
    assert S.vol_exit_positions(d, 2.0, True)[254] == 2.0  # downside gate stays long


def test_zero_downside_denominator_counts_as_calm():
    c = 100 * 1.01 ** np.arange(200)                        # never a down day
    assert S.vol_exit_positions(daily(c), 2.0, True)[150] == 2.0
