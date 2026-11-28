"""One-sided CUSUM decay detector (PREREG60).

Tests live daily returns against the candidate's own pre-admission returns: the expected live mean is half
the reference mean, the decayed mean is 0, and the alarm threshold is set so a candidate performing as
expected raises a false alarm about once per ARL0 days. Constants are fixed by PREREG60; do not tune.
"""
from __future__ import annotations

import math
from typing import Any

import numpy as np

REF_DAYS = 1095
MIN_REF_DAYS = 180
HAIRCUT = 0.5
ARL0_DAYS = 1825.0
Z_CLIP = 4.0
K_FLOOR = 0.01
WINSOR_Q = 0.005


def siegmund_arl0(k: float, h: float) -> float:
    """Siegmund's approximation of the in-control average run length of a CUSUM with drift k."""
    b = h + 1.166
    x = 2.0 * k * b
    if x > 700:
        return math.inf
    return (math.exp(x) - 1.0 - x) / (2.0 * k * k)


def solve_threshold(k: float, arl0: float = ARL0_DAYS) -> float:
    """Smallest h in [0, 1e4] with siegmund_arl0(k, h) >= arl0, by bisection."""
    lo, hi = 0.0, 1e4
    if siegmund_arl0(k, lo) >= arl0:
        return lo
    for _ in range(200):
        mid = 0.5 * (lo + hi)
        if siegmund_arl0(k, mid) >= arl0:
            hi = mid
        else:
            lo = mid
    return hi


def reference_params(ref: np.ndarray) -> dict[str, float] | None:
    """mu0, sigma, k, h from the pre-admission returns, or None when there are too few days or no scale."""
    r = np.asarray(ref, dtype=float)
    r = r[np.isfinite(r)][-REF_DAYS:]
    if len(r) < MIN_REF_DAYS:
        return None
    lo, hi = np.quantile(r, [WINSOR_Q, 1.0 - WINSOR_Q])
    sigma = float(np.std(np.clip(r, lo, hi), ddof=1))
    if not sigma > 1e-12:
        return None
    mu0 = HAIRCUT * float(np.mean(r))
    k = mu0 / (2.0 * sigma) if mu0 > 0 else K_FLOOR
    return {"mu0": mu0, "sigma": sigma, "k": k, "h": solve_threshold(k), "ref_days": float(len(r))}


def cusum_path(live: np.ndarray, p: dict[str, float]) -> np.ndarray:
    """S_t for every live day."""
    r = np.asarray(live, dtype=float)
    r = r[np.isfinite(r)]
    z = np.clip((p["mu0"] - r) / p["sigma"], -Z_CLIP, Z_CLIP) - p["k"]
    out = np.empty(len(z))
    s = 0.0
    for i, step in enumerate(z):
        s = max(0.0, s + step)
        out[i] = s
    return out


def cusum_decay(ref: np.ndarray, live: np.ndarray) -> dict[str, Any] | None:
    """Detector state after the full live series; None when the reference is unusable."""
    p = reference_params(ref)
    if p is None:
        return None
    path = cusum_path(live, p)
    hit = np.flatnonzero(path >= p["h"])
    return {
        "s": round(float(path[-1]), 4) if len(path) else 0.0,
        "h": round(p["h"], 4),
        "k": round(p["k"], 6),
        "mu0_bps": round(p["mu0"] * 1e4, 4),
        "sigma_bps": round(p["sigma"] * 1e4, 4),
        "ref_days": int(p["ref_days"]),
        "alarm": bool(len(hit)),
        "alarm_day": int(hit[0]) + 1 if len(hit) else None,
        "frac_of_h": round(float(path[-1]) / p["h"], 4) if len(path) and p["h"] > 0 else 0.0,
    }


def classify_health_cusum(live_days: int, live_max_dd_pct: float, cusum: dict[str, Any]) -> str:
    """PREREG60 health rule (used only once the calibration has passed)."""
    if live_max_dd_pct < -15.0 or cusum["alarm"]:
        return "KILLED"
    if live_days < 14:
        return "WARMUP"
    if cusum["s"] >= 0.5 * cusum["h"]:
        return "DECAYING"
    return "HEALTHY"
