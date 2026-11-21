"""Unit tests for PREREG53 (dual-momentum cash gate & asymmetric invvol causality)."""
from __future__ import annotations

import numpy as np
import pandas as pd

import run_round53 as R53


def test_asymmetric_invvol_blend_strictly_causal():
    idx = pd.date_range("2023-01-01", "2024-06-30", freq="D", tz="UTC")
    rng = np.random.default_rng(19)
    r_a = pd.Series(rng.normal(0.001, 0.03, size=len(idx)), index=idx)
    r_b = pd.Series(rng.normal(0.0005, 0.01, size=len(idx)), index=idx)

    b1, w1 = R53.asymmetric_invvol_blend(r_a, r_b)
    cut = pd.Timestamp("2024-01-01", tz="UTC")
    r_a2, r_b2 = r_a.copy(), r_b.copy()
    r_a2[r_a2.index >= cut] = -0.30
    r_b2[r_b2.index >= cut] = 0.30
    b2, w2 = R53.asymmetric_invvol_blend(r_a2, r_b2)

    before = idx < cut
    assert np.allclose(w1[before].to_numpy(), w2[before].to_numpy(), atol=1e-12)
    assert np.allclose(b1[before].to_numpy(), b2[before].to_numpy(), atol=1e-12)
