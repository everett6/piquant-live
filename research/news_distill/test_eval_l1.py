"""Synthetic tests for eval_l1 scoring/verdict logic (python test_eval_l1.py)."""
import numpy as np
import pandas as pd

import eval_l1 as L


def _frame(n, informative, seed=0):
    rng = np.random.default_rng(seed)
    d = rng.uniform(-1, 1, n)
    m = rng.uniform(0, 1, n)
    out = {"teacher_direction": d, "teacher_magnitude": m}
    for h in L.HORIZONS:
        noise = rng.normal(0, 50, n)
        out[f"ret_{h}m_bps"] = (40 * d + noise) if informative else noise
    return pd.DataFrame(out), [{"direction": float(x), "magnitude": float(y)} for x, y in zip(d, m)]


def test_informative_direction_is_applicable():
    s, pred = _frame(4000, True)
    res = L.score(s, pred)
    assert res["verdict"] == "applicable_signal", res["verdict"]
    assert res["direction"]["60"]["rho"] > 0.3


def test_noise_is_scrapped():
    s, pred = _frame(4000, False)
    res = L.score(s, pred)
    assert res["verdict"] == "scrap", (res["verdict"], res["direction"], res["magnitude"])


def test_invalid_predictions_are_nan_not_zero():
    s, pred = _frame(100, False)
    pred[0] = None
    pred[1] = {"direction": "up"}
    res = L.score(s, pred)
    assert res["valid_json"] == 0.99
    assert res["direction"]["60"]["n"] == 98


def test_spearman_p_value_small_for_strong_corr():
    a = pd.Series(np.arange(500, dtype=float))
    r = L.spearman(a, a + np.random.default_rng(1).normal(0, 50, 500))
    assert r["rho"] > 0.9 and r["p_two_sided"] < 1e-10


if __name__ == "__main__":
    for k, f in list(globals().items()):
        if k.startswith("test_"):
            f()
            print("ok", k)
