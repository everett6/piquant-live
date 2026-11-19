"""
Unit tests for Strategies 46 (Alpha-Decay Throttle & Self-Improving Exp-Grad Allocation).
"""

import numpy as np
import pandas as pd
import pytest

from strategies46 import rolling_alpha_decay_throttle, self_improving_exp_grad_weights, project_simplex


def test_rolling_alpha_decay_throttle_bounds():
    np.random.seed(42)
    rets = pd.Series(np.random.normal(0.001, 0.01, 200))
    throttle = rolling_alpha_decay_throttle(rets, lookback=60, floor_mult=0.35)

    assert len(throttle) == len(rets)
    assert (throttle >= 0.35).all()
    assert (throttle <= 1.0).all()


def test_rolling_alpha_decay_throttle_causality():
    np.random.seed(123)
    rets1 = pd.Series(np.random.normal(0.0005, 0.02, 150))
    rets2 = rets1.copy()
    # Perturb future bars (t >= 80)
    rets2.iloc[80:] += 0.05

    t1 = rolling_alpha_decay_throttle(rets1, lookback=40, floor_mult=0.3)
    t2 = rolling_alpha_decay_throttle(rets2, lookback=40, floor_mult=0.3)

    # Past values up to t=79 must be identical (strictly causal)
    pd.testing.assert_series_equal(t1.iloc[:80], t2.iloc[:80])


def test_self_improving_exp_grad_weights_simplex():
    np.random.seed(42)
    n = 100
    df = pd.DataFrame(
        {
            "qd": np.random.normal(-0.002, 0.01, n),
            "r3": np.random.normal(0.001, 0.01, n),
            "carry": np.random.normal(0.0008, 0.005, n),
            "dtb3": np.random.normal(0.0001, 0.001, n),
        }
    )

    weights_df = self_improving_exp_grad_weights(df, eta=0.15, min_carry=0.25, max_trend=0.70, lookback=30)

    assert len(weights_df) == n
    for i, row in weights_df.iterrows():
        w = row.values
        # sum == 1.0
        assert pytest.approx(w.sum(), abs=1e-6) == 1.0
        # w >= 0
        assert (w >= -1e-7).all()
        # w_carry >= min_carry (index 2)
        assert w[2] >= 0.25 - 1e-6
        # w_qd + w_r3 <= max_trend (indices 0, 1)
        assert w[0] + w[1] <= 0.70 + 1e-6


def test_self_improving_causality():
    np.random.seed(42)
    n = 120
    df1 = pd.DataFrame(
        {
            "qd": np.random.normal(0.001, 0.01, n),
            "r3": np.random.normal(0.0, 0.01, n),
            "carry": np.random.normal(0.0005, 0.005, n),
            "dtb3": np.random.normal(0.0001, 0.001, n),
        }
    )
    df2 = df1.copy()
    # Perturb future (t >= 75)
    df2.iloc[75:, 0] += 0.1

    w1 = self_improving_exp_grad_weights(df1, lookback=30)
    w2 = self_improving_exp_grad_weights(df2, lookback=30)

    # Weights decided at t and held over t+1. Up to t=74, decisions must match.
    pd.testing.assert_frame_equal(w1.iloc[:75], w2.iloc[:75])


def test_project_simplex():
    raw = np.array([0.5, 0.5, 0.1, 0.1])
    w = project_simplex(raw, min_carry=0.25, max_trend=0.70)
    assert pytest.approx(w.sum(), abs=1e-6) == 1.0
    assert w[2] >= 0.25 - 1e-6
    assert w[0] + w[1] <= 0.70 + 1e-6
