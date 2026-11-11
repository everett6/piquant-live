"""Self-checks for strategies2 (synthetic data, no market data). python test_strategies2.py"""
import numpy as np
import pandas as pd

import strategies2 as S2


def test_s6_trending_vs_meanreverting():
    idx = pd.date_range("2024-01-01", periods=24 * 60, freq="h", tz="UTC")
    rng = np.random.default_rng(0)
    # persistent drift segments -> superdiffusive; alternating -> subdiffusive
    trend = np.cumsum(np.repeat(rng.choice([-1, 1], 60), 24) * 0.002 + rng.normal(0, 0.0005, 24 * 60))
    alt = np.cumsum(np.where(np.arange(24 * 60) % 2 == 0, 0.003, -0.003) + rng.normal(0, 0.0005, 24 * 60))
    p = dict(delta=0.1, window_d=14, k=12)
    pt = S2.s6_positions(pd.DataFrame({"c": 100 * np.exp(trend)}, index=idx), p)
    pa = S2.s6_positions(pd.DataFrame({"c": 100 * np.exp(alt)}, index=idx), p)
    # trending series: positions follow the last 12h move; alternating: fade (or flat)
    rk_t = np.sign(trend[12:] - trend[:-12])[-200:]
    assert (pt[-200:] == rk_t).mean() > 0.6
    assert (pa[-200:] != np.sign(alt[12:] - alt[:-12])[-200:]).mean() > 0.6


def test_s5_caps_and_scales():
    idx = pd.date_range("2024-01-01", periods=200, freq="D", tz="UTC")
    c = 100 * np.exp(np.cumsum(np.random.default_rng(1).normal(0, 0.04, 200)))  # ~76% annual vol
    w = S2.s5_positions(pd.DataFrame({"c": c}, index=idx), dict(target=0.5, lookback=20), cap=1.0)
    assert np.nanmax(w) <= 1.0 and 0.4 < np.nanmedian(w[30:]) < 0.9


def test_q1_lambda_finite():
    r = np.random.default_rng(2).standard_t(4, 60) * 0.02
    assert np.isfinite(S2.qho_lambda(r))


if __name__ == "__main__":
    for name, fn in list(globals().items()):
        if name.startswith("test_"):
            fn()
            print("ok", name)
