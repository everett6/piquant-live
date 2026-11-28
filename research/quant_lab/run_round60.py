"""Run PREREG60 once: synthetic calibration of the CUSUM decay detector against the old health rule.

Synthetic Student-t paths only; reads no market data and calls no register(). Writes results/round60.json.
"""
from __future__ import annotations

import json
import os

import numpy as np

import decay_cusum as DC

HERE = os.path.dirname(os.path.abspath(__file__))
SEED = 60
PATHS = 2000
REF_N, LIVE_N = 1095, 730
SIGMA_D = 0.0094
REF_SHARPE = 1.5
SCENARIOS = {"H": 0.75, "Z": 0.0, "N": -1.0, "C": -3.0}
DOF = 3


def draws(rng: np.random.Generator, n: int, sharpe: float) -> np.ndarray:
    t = rng.standard_t(DOF, size=n) / np.sqrt(DOF / (DOF - 2.0))  # unit variance
    return sharpe * SIGMA_D / np.sqrt(365.0) + SIGMA_D * t


def drawdown_pct(r: np.ndarray) -> np.ndarray:
    eq = np.cumprod(np.maximum(1.0 + r, 1e-12))
    peak = np.maximum(1.0, np.maximum.accumulate(eq))
    return (eq / peak - 1.0) * 100.0


def old_kill_day(r: np.ndarray) -> int | None:
    """First live day (1-based) where the current classify_health returns KILLED."""
    n = np.arange(1, len(r) + 1)
    dd_run_min = np.minimum.accumulate(drawdown_pct(r))  # classify uses max drawdown so far
    s1, s2 = np.cumsum(r), np.cumsum(r * r)
    with np.errstate(invalid="ignore", divide="ignore"):
        var = (s2 - s1 * s1 / n) / (n - 1)
        sharpe = np.where(var > 1e-24, (s1 / n) / np.sqrt(var) * np.sqrt(365.0), 0.0)
    kill = (n >= 14) & ((dd_run_min < -15.0) | ((n >= 30) & (sharpe < -0.5)))
    hit = np.flatnonzero(kill)
    return int(hit[0]) + 1 if len(hit) else None


def new_kill_day(ref: np.ndarray, r: np.ndarray) -> int | None:
    p = DC.reference_params(ref)
    dd_hit = np.flatnonzero(np.minimum.accumulate(drawdown_pct(r)) < -15.0)
    first = [int(dd_hit[0]) + 1] if len(dd_hit) else []
    if p is not None:
        al = np.flatnonzero(DC.cusum_path(r, p) >= p["h"])
        if len(al):
            first.append(int(al[0]) + 1)
    return min(first) if first else None


def summarize(days: list[int | None]) -> dict:
    d = np.array([x if x is not None else np.inf for x in days], dtype=float)
    return {
        "kill_365": round(float(np.mean(d <= 365)), 4),
        "kill_730": round(float(np.mean(d <= 730)), 4),
        "median_days": float(np.median(d)) if np.isfinite(np.median(d)) else None,
    }


def main() -> None:
    rng = np.random.default_rng(SEED)
    res: dict = {"paths": PATHS, "seed": SEED, "scenarios": {}}
    for name, live_sharpe in SCENARIOS.items():
        old, new, hs = [], [], []
        for _ in range(PATHS):
            ref = draws(rng, REF_N, REF_SHARPE)
            live = draws(rng, LIVE_N, live_sharpe)
            old.append(old_kill_day(live))
            new.append(new_kill_day(ref, live))
            p = DC.reference_params(ref)
            hs.append(p["h"] if p else np.nan)
        res["scenarios"][name] = {"live_sharpe": live_sharpe, "old": summarize(old), "new": summarize(new),
                                  "median_h": round(float(np.nanmedian(hs)), 3)}
        print(name, json.dumps(res["scenarios"][name]))
    s = res["scenarios"]
    rules = {
        "1_H_new_kill365_le_20pct": s["H"]["new"]["kill_365"] <= 0.20,
        "2_H_new_lt_old": s["H"]["new"]["kill_365"] < s["H"]["old"]["kill_365"],
        "3_N_new_kill730_ge_70pct": s["N"]["new"]["kill_730"] >= 0.70,
        "4_C_new_median_le_120d": (s["C"]["new"]["median_days"] or np.inf) <= 120,
    }
    res["rules"] = rules
    res["pass"] = all(rules.values())
    print("PASS" if res["pass"] else "FAIL", rules)
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round60.json"), "w") as f:
        json.dump(res, f, indent=2)


if __name__ == "__main__":
    main()
