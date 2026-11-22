"""Unit tests for quantum_meta_alpha.py: strict causality, pre-2024 cutoff, and no pyarrow."""
from __future__ import annotations

import subprocess
import sys

import numpy as np
import pandas as pd

import quantum_meta_alpha as QMA


def _make_synthetic_data(seed: int = 7, n_days: int = 2000) -> tuple[pd.Series, pd.DataFrame, pd.Series]:
    rng = np.random.default_rng(seed)
    idx = pd.date_range("2020-01-01", periods=n_days, freq="D", tz="UTC")
    # 5 correlated assets
    common = rng.normal(0.0003, 0.01, size=n_days)
    ca = {}
    for k in range(5):
        ca[f"A{k}"] = 0.5 * common + rng.normal(0.0002, 0.012, size=n_days)
    ca_df = pd.DataFrame(ca, index=idx)
    primary_ret = pd.Series(0.6 * common + rng.normal(0.0004, 0.008, size=n_days), index=idx)
    tox = pd.Series(np.clip(0.3 + rng.normal(0.0, 0.1, size=n_days), 0.0, 1.0), index=idx)
    return primary_ret, ca_df, tox


def test_no_pyarrow_imported():
    import os

    here = os.path.dirname(os.path.abspath(__file__))
    code = "import sys, quantum_meta_alpha; assert 'pyarrow' not in sys.modules"
    proc = subprocess.run([sys.executable, "-c", code], cwd=here, capture_output=True, text=True, check=False)
    assert proc.returncode == 0, f"pyarrow was imported: {proc.stderr}"


def test_quantum_spectral_entropy_strict_causality():
    _, ca_df, _ = _make_synthetic_data()
    vn_orig = QMA.quantum_spectral_entropy(ca_df, window=30)

    # Corrupt future returns from t >= 1200 onward
    cutoff = 1200
    ca_corrupt = ca_df.copy()
    ca_corrupt.iloc[cutoff:] = -ca_corrupt.iloc[cutoff:] * 15.0 + 0.25
    vn_corr = QMA.quantum_spectral_entropy(ca_corrupt, window=30)

    # At bar cutoff (and all bars <= cutoff), spectral entropy uses only [:cutoff], so must match bit-for-bit
    pd.testing.assert_frame_equal(vn_orig.iloc[: cutoff + 1], vn_corr.iloc[: cutoff + 1])


def test_ou_and_meta_features_strict_causality():
    primary_ret, ca_df, tox = _make_synthetic_data()
    vn_orig = QMA.quantum_spectral_entropy(ca_df, window=30)
    feats_orig = QMA.build_causal_meta_features(primary_ret, vn_orig, tox_series=tox)

    cutoff = 1200
    ret_corrupt = primary_ret.copy()
    ret_corrupt.iloc[cutoff:] = -0.50
    ca_corrupt = ca_df.copy()
    ca_corrupt.iloc[cutoff:] = 0.30
    tox_corrupt = tox.copy()
    tox_corrupt.iloc[cutoff:] = 0.99

    vn_corr = QMA.quantum_spectral_entropy(ca_corrupt, window=30)
    feats_corr = QMA.build_causal_meta_features(ret_corrupt, vn_corr, tox_series=tox_corrupt)

    pd.testing.assert_frame_equal(feats_orig.iloc[: cutoff + 1], feats_corr.iloc[: cutoff + 1])


def test_pre_2024_training_cutoff_and_oos_causality():
    primary_ret, ca_df, tox = _make_synthetic_data(n_days=2200)  # spans 2020-01-01 .. 2026-01-09
    res_orig = QMA.meta_label_precision_gate(
        primary_ret, ca_df, tox_series=tox, train_start="2020-01-01", train_end="2023-12-31"
    )
    assert res_orig["model"].train_end == "2023-12-31"

    # Corrupt all OOS returns (2024-01-01 onward)
    oos_mask = primary_ret.index >= pd.Timestamp("2024-01-01", tz="UTC")
    ret_corrupt = primary_ret.copy()
    ret_corrupt.loc[oos_mask] = -0.25
    ca_corrupt = ca_df.copy()
    ca_corrupt.loc[oos_mask] = -0.25

    res_corr = QMA.meta_label_precision_gate(
        ret_corrupt, ca_corrupt, tox_series=tox, train_start="2020-01-01", train_end="2023-12-31"
    )

    # Frozen model weights must be bit-for-bit identical when 2024+ data is corrupted
    clf_orig = res_orig["model"].pipeline.named_steps["clf"]
    clf_corr = res_corr["model"].pipeline.named_steps["clf"]
    np.testing.assert_array_equal(clf_orig.coef_, clf_corr.coef_)
    np.testing.assert_array_equal(clf_orig.intercept_, clf_corr.intercept_)

    # Pre-2024 gated returns and probabilities must also be bit-for-bit identical
    pre_oos = primary_ret.index < pd.Timestamp("2024-01-01", tz="UTC")
    pd.testing.assert_series_equal(res_orig["probs"].loc[pre_oos], res_corr["probs"].loc[pre_oos])
    pd.testing.assert_series_equal(res_orig["gated_ret"].loc[pre_oos], res_corr["gated_ret"].loc[pre_oos])


def test_spectral_entropy_drops_in_correlation_crisis():
    rng = np.random.default_rng(123)
    idx = pd.date_range("2021-01-01", periods=120, freq="D", tz="UTC")
    # First 60 days: independent assets (high entropy); next 60 days: synchronized crash (low entropy)
    indep = rng.normal(0.0, 0.01, size=(60, 6))
    shock = rng.normal(-0.02, 0.02, size=(60, 1))
    crisis = shock + rng.normal(0.0, 0.001, size=(60, 6))
    ca_df = pd.DataFrame(np.vstack([indep, crisis]), index=idx, columns=[f"C{i}" for i in range(6)])

    vn = QMA.quantum_spectral_entropy(ca_df, window=30)
    assert vn["vn_entropy"].iloc[59] > vn["vn_entropy"].iloc[119] + 0.30
    assert vn["vn_purity"].iloc[119] > vn["vn_purity"].iloc[59] + 0.30
