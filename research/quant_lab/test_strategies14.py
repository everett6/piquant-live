"""Synthetic tests for round-14 code (python test_strategies14.py)."""
import numpy as np
import pandas as pd

import lab
import strategies14 as S

NOCOST = lab.Costs("none", taker_bps=0.0, slip_bps=0.0, funding_bps_h=0.0, can_short=False)


def _daily(closes, start="2023-01-01"):
    idx = pd.date_range(start, periods=len(closes), freq="D", tz="UTC")
    c = pd.Series(closes, index=idx, dtype=float)
    return pd.DataFrame({"o": c.shift(1).fillna(c.iloc[0]), "h": c, "l": c, "c": c, "v": 1.0})


def _min1(days, rows):
    """rows: {day_index: [(minute, o, l), ...]} -> 1m frame."""
    recs = []
    for di, bars in rows.items():
        for mnt, o, lo in bars:
            recs.append((days[di] + pd.Timedelta(minutes=mnt), o, max(o, lo), lo, o))
    df = pd.DataFrame(recs, columns=["t", "o", "h", "l", "c"]).set_index("t")
    return df


def test_first_touch_fill_and_gap():
    days = pd.date_range("2023-01-01", periods=3, freq="D", tz="UTC")
    m = _min1(days, {0: [(0, 100, 99), (5, 99, 94)], 1: [(0, 90, 89), (3, 95, 80)], 2: [(0, 100, 99)]})
    f = S.first_touch(m, days, np.array([95.0, 92.0, np.nan]))
    assert f[0] == 95.0          # touched mid-bar: fill at level
    assert f[1] == 90.0          # first bar opens below level (gap through): fill at its open
    assert np.isnan(f[2])        # NaN level never triggers


def test_stop_fraction_clipped_and_causal():
    rng = np.random.default_rng(1)
    d = _daily(100 * np.exp(np.cumsum(rng.normal(0, 0.2, 60))))
    s = S.stop_fraction(d)
    assert np.isnan(s[:30]).all() and (s[30:] == 0.12).all()
    calm = _daily(100 * np.exp(np.cumsum(rng.normal(0, 0.001, 60))))
    assert (S.stop_fraction(calm)[30:] == 0.03).all()


def test_gz_cap():
    assert S.gz_cap(1.0, 1.0) == 2.0
    assert abs(S.gz_cap(0.85, 1.0) - S.M_GZ * (1 - 0.7 / 0.85)) < 1e-12
    assert S.gz_cap(0.69, 1.0) == 0.0


def test_ensemble_signal():
    d = _daily(np.r_[np.full(250, 100.0), np.linspace(100, 130, 30)])
    sig = S.ensemble_signal(d)
    assert sig[:49].sum() == 0 and sig[-1] == 1.0
    d2 = _daily(np.r_[np.linspace(100, 200, 250), np.linspace(200, 185, 15)])
    assert 0 < S.ensemble_signal(d2)[-1] < 1


def test_simulate_stop_caps_crash_day():
    closes = np.r_[np.full(10, 100.0), [70.0, 70.0]]
    d = _daily(closes)
    sig = np.ones(len(d))
    stopfill = np.full(len(d), np.nan)
    stopfill[10] = 95.0
    a = S.simulate([{"daily": d, "signal": sig, "stopfill": stopfill}], NOCOST, gz=False)
    b = S.simulate([{"daily": d, "signal": sig, "stopfill": np.full(len(d), np.nan)}], NOCOST, gz=False)
    assert abs(a["r"].iloc[10] - 2 * (95 / 100 - 1)) < 1e-12 and a["stop0"].iloc[10]
    assert abs(b["r"].iloc[10] - 2 * (70 / 100 - 1)) < 1e-12


def test_gz_keeps_drawdown_above_floor_without_gaps():
    rng = np.random.default_rng(3)
    d = _daily(100 * np.exp(np.cumsum(rng.normal(-0.004, 0.04, 800))))
    sl = {"daily": d, "signal": np.ones(len(d)), "stopfill": np.full(len(d), np.nan)}
    out = S.simulate([sl], NOCOST, gz=True)
    eq = (1 + out["r"]).cumprod()
    dd = (eq / eq.cummax() - 1).min()
    raw = S.simulate([sl], NOCOST, gz=False)
    eq2 = (1 + raw["r"]).cumprod()
    assert dd > (eq2 / eq2.cummax() - 1).min()
    assert dd > -0.40   # daily moves of ~4% at most 2x keep it near the 30% floor


def test_costs_charged_on_entry_and_stop():
    d = _daily(np.full(5, 100.0))
    c = lab.Costs("c", taker_bps=10.0, slip_bps=0.0, funding_bps_h=0.0, can_short=False)
    stopfill = np.full(5, np.nan)
    stopfill[2] = 100.0
    out = S.simulate([{"daily": d, "signal": np.ones(5), "stopfill": stopfill}], c, gz=False)
    assert abs(out["r"].iloc[0] + 2 * 1e-3) < 1e-12 and out["r"].iloc[1] == 0  # enter 2x at day 0's close
    assert abs(out["r"].iloc[2] + 2 * 1e-3 + 2 * 1e-3) < 1e-12  # stop exit + re-entry at close


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
