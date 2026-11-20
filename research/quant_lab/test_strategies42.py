"""Synthetic unit tests for PREREG42 expected-drawdown diagnostics and vol capping."""
import math
import numpy as np
import pandas as pd
import pytest

import strategies42 as S42


def test_drawdown_diagnostics_exact_and_causal():
    idx = pd.date_range("2025-01-01", periods=6, freq="D", tz="UTC")
    # Day0:+10% (eq 1.10, dd 0)
    # Day1:-10% (eq 0.99, dd 10%)
    # Day2:+0%  (eq 0.99, dd 10%)
    # Day3:+11.111111% (eq 1.10, dd 0 -> recovered in 2d streak)
    # Day4:-5%  (eq 1.045, dd 5%)
    # Day5:-5%  (eq 0.99275, dd 9.75%)
    r = pd.Series([0.10, -0.10, 0.0, 1.10 / 0.99 - 1.0, -0.05, -0.05], index=idx)
    d = S42.drawdown_diagnostics(r)
    expected_dds = np.array([0.0, 10.0, 10.0, 0.0, 5.0, 9.75])
    assert d["mean_dd_pct"] == pytest.approx(float(np.mean(expected_dds)), abs=1e-3)
    assert d["ulcer_index_pct"] == pytest.approx(float(np.sqrt(np.mean(expected_dds**2))), abs=1e-3)
    assert d["max_underwater_days"] == 2
    assert d["mean_recovery_days"] == pytest.approx(2.0)


def test_downside_vol_cap_is_causal_and_never_exceeds_one():
    idx = pd.date_range("2025-01-01", periods=50, freq="D", tz="UTC")
    rng = np.random.default_rng(9)
    rets = rng.normal(0.0, 0.04, size=50)  # ~76% annualized vol > 45% target
    px = 100.0 * np.exp(np.cumsum(rets))
    df = pd.DataFrame({"c": px}, index=idx)
    base = pd.Series(1.0, index=idx)

    out1 = S42.downside_vol_cap_positions(df, base, vol_target_ann=0.45, vol_window=20)
    assert float(out1.max()) <= 1.0 + 1e-12
    # High-vol period -> noticeably scaled down below 0.85
    assert float(out1.iloc[25]) < 0.85

    # Future shock at index 35 cannot change values at indices <= 34
    df2 = df.copy()
    df2.iloc[35:, 0] *= 3.0
    out2 = S42.downside_vol_cap_positions(df2, base, vol_target_ann=0.45, vol_window=20)
    np.testing.assert_allclose(out1.iloc[:35].to_numpy(), out2.iloc[:35].to_numpy())
