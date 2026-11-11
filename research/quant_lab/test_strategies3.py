"""Synthetic tests for round-3 code (run: python test_strategies3.py)."""
import numpy as np
import pandas as pd

import lab
import strategies3 as S3


def _walk(n, drift=0.0, vol=0.0008, seed=0, freq="1min"):
    rng = np.random.default_rng(seed)
    lr = drift + vol * rng.standard_normal(n)
    c = 30000 * np.exp(np.cumsum(lr))
    o = np.concatenate([[c[0]], c[:-1]])
    wig = np.abs(vol * 0.3 * rng.standard_normal(n))
    h = np.maximum(o, c) * (1 + wig)
    lo = np.minimum(o, c) * (1 - wig)
    idx = pd.date_range("2023-01-01", periods=n, freq=freq, tz="UTC")
    return pd.DataFrame({"o": o, "h": h, "l": lo, "c": c, "v": 1.0}, index=idx)


ZERO = lab.Costs("zero", taker_bps=0.0, slip_bps=0.0, funding_bps_h=0.0, can_short=True)


def test_t0_martingale_matches_theory():
    df = _walk(300_000)
    tr = S3.brackets(df, S3.t0_signal(len(df)), 60, 30, 1440, ZERO)
    cell = S3.t0_cell(tr, 60, 30, 14.0)
    assert abs(cell["win_minus_pred"]) < 0.03, cell
    assert abs(cell["mean_gross_bps"]) < 3.0, cell
    assert abs(cell["net_breakeven_p"] - 44 / 90) < 1e-12


def test_t0_drift_breaks_theory_in_right_direction():
    df = _walk(300_000, drift=2e-5)  # strong up drift
    long_only = np.ones(len(df), dtype=int)
    cell = S3.t0_cell(S3.brackets(df, long_only, 60, 30, 1440, ZERO), 60, 30, 14.0)
    assert cell["win_minus_pred"] > 0.03 and cell["mean_gross_bps"] > 0, cell


def test_t0_supported_rule():
    good = {"win_minus_pred": 0.01, "mean_gross_bps": 1.0}
    bad = {"win_minus_pred": 0.10, "mean_gross_bps": 1.0}
    assert S3.t0_supported([good] * 9 + [bad])
    assert not S3.t0_supported([good] * 8 + [bad] * 2)


def test_s8_regime_is_causal():
    m1 = _walk(60 * 24 * 30, seed=1)
    h1, m5 = lab.resample(m1, "1h"), lab.resample(m1, "5min")
    reg = S3.s8_regime(h1, m5, 3, 10)
    # perturb the future: closes after day 20 must not change the regime before day 20
    m1b = m1.copy()
    cut = m1.index[0] + pd.Timedelta(days=20)
    m1b.loc[m1b.index >= cut, ["o", "h", "l", "c"]] *= 1.5
    reg_b = S3.s8_regime(lab.resample(m1b, "1h"), lab.resample(m1b, "5min"), 3, 10)
    before = (m5.index + pd.Timedelta(minutes=5)) <= cut
    assert np.array_equal(reg[before], reg_b[before])
    assert reg[:10 * 12].sum() == 0  # warm-up (slow=10 hours) stays flat


def test_s7_signal_thresholds():
    conv = np.array([0.8, 0.5, 0.2, np.nan])
    assert S3.s7_signal(conv, 0.7).tolist() == [1, 0, -1, 0]


def test_grid_sizes():
    assert len(S3.S7_GRID) == 24 and len(S3.S8_GRID) == 36 and len(S3.T0_TP) * len(S3.T0_SL) == 24


if __name__ == "__main__":
    n = 0
    for name, fn in list(globals().items()):
        if name.startswith("test_"):
            fn()
            n += 1
            print("ok", name)
    print(n, "tests passed")
