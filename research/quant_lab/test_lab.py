"""Self-checking tests for lab.py (run: python test_lab.py). Synthetic data only."""
import numpy as np
import pandas as pd

import lab

NOCOST = lab.Costs("none", 0, 0, 0, True)


def bars(prices_o, prices_h, prices_l, prices_c):
    n = len(prices_o)
    t = (np.arange(n) * 60e9).astype("int64")
    return [np.asarray(x, float) for x in (prices_o, prices_h, prices_l, prices_c)] + [t]


def test_next_bar_fill_and_target():
    o, h, l, c, t = bars([100, 101, 101, 101, 101], [100, 101, 103, 101, 101], [100, 101, 100.5, 101, 101], [100, 101, 102, 101, 101])
    sig = np.array([1, 0, 0, 0, 0])
    tr = lab.simulate_brackets(o, h, l, c, t, sig, np.full(5, 100.0), np.full(5, 100.0), 10, 10, NOCOST)
    assert len(tr) == 1 and tr.reason[0] == "target"
    assert abs(tr.gross[0] - (101 * 1.01 / 101 - 1)) < 1e-12, "entry must be next bar open (101), target +1%"


def test_stop_first_when_both_touched():
    o, h, l, c, t = bars([100, 100, 100, 100], [100, 100, 102, 100], [100, 100, 98, 100], [100, 100, 100, 100])
    tr = lab.simulate_brackets(o, h, l, c, t, np.array([1, 0, 0, 0]), np.full(4, 100.0), np.full(4, 100.0), 10, 10, NOCOST)
    assert tr.reason[0] == "stop" and tr.gross[0] < 0


def test_costs_and_no_short_on_spot():
    o, h, l, c, t = bars([100] * 6, [100, 100, 100, 100, 100, 100], [100, 100, 99, 100, 100, 100], [100] * 6)
    tr = lab.simulate_brackets(o, h, l, c, t, np.array([-1, 0, 0, 0, 0, 0]), np.full(6, 50.0), np.full(6, 50.0), 2, 99,
                               lab.US_SPOT)
    assert tr.empty, "spot profile cannot short"
    tr = lab.simulate_brackets(o, h, l, c, t, np.array([1, 0, 0, 0, 0, 0]), np.full(6, 500.0), np.full(6, 500.0), 2, 99,
                               lab.US_FUTURES)
    assert tr.reason[0] == "max_hold" and abs(tr.net[0] - (-14e-4 - 0.1e-4 * tr.hold_h[0])) < 1e-9


def test_position_returns_lag_and_turnover():
    idx = pd.date_range("2024-01-01", periods=4, freq="h", tz="UTC")
    df = pd.DataFrame({"o": 1.0, "h": 1.0, "l": 1.0, "c": [100, 110, 121, 121]}, index=idx)
    r = lab.position_returns(df, np.array([1, 1, 0, 0]), NOCOST)
    assert np.allclose(r.to_numpy(), [0, 0.10, 0.10, 0.0]), "position decided at close t earns bar t+1"


def test_pbo_noise_and_signal():
    rng = np.random.default_rng(0)
    noise = pd.DataFrame(rng.normal(0, 0.01, (600, 20)))
    p = lab.pbo_cscv(noise, 10)
    assert 0.25 < p < 0.8, p
    strong = noise.copy()
    strong[0] += 0.004
    assert lab.pbo_cscv(strong, 10) < 0.1


def test_deflated_sharpe_penalizes_trials():
    rng = np.random.default_rng(1)
    d = pd.Series(rng.normal(0.001, 0.01, 1000))
    assert lab.deflated_sharpe(d, 2) > lab.deflated_sharpe(d, 400)


if __name__ == "__main__":
    n = 0
    for name, fn in list(globals().items()):
        if name.startswith("test_"):
            fn()
            n += 1
            print("ok", name)
    print(f"{n} tests passed")
