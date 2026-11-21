"""Unit tests for micro_news_features.py, 32-feature feature_store, and Pi 5 constraints."""
from __future__ import annotations

import os
import subprocess
import sys

import numpy as np
import pandas as pd
import pytest

import auto_discover as AD
import feature_store as FS
import micro_news_features as MN


def test_no_pyarrow_required_at_import_time():
    """Pi 5 friendly rule: importing inference/feature modules must not import pyarrow."""
    code = (
        "import sys; sys.modules['pyarrow'] = None; "
        "import micro_news_features, feature_store, auto_discover; "
        "assert len(auto_discover.ALLOWED_FEATURES) == 32"
    )
    ql_dir = os.path.dirname(os.path.abspath(__file__))
    proc = subprocess.run(
        [sys.executable, "-c", code],
        cwd=ql_dir,
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 0, f"Import failed without pyarrow: {proc.stderr}"


def test_build_daily_news_features_causal_and_fallback(monkeypatch):
    rows = [
        # Day 1: teacher present
        {
            "ts": "2024-01-01T10:00:00Z",
            "teacher_direction": 0.8,
            "teacher_magnitude": 0.5,
            "jan_direction": np.nan,
            "jan_magnitude": np.nan,
            "jan_confidence": np.nan,
        },
        # Day 1: teacher NaN -> fallback to jan_*
        {
            "ts": "2024-01-01T23:59:59Z",
            "teacher_direction": np.nan,
            "teacher_magnitude": np.nan,
            "jan_direction": -0.5,
            "jan_magnitude": 0.4,
            "jan_confidence": 0.5,
        },
        # Day 2: positive shock
        {
            "ts": "2024-01-02T12:00:00Z",
            "teacher_direction": 1.0,
            "teacher_magnitude": 1.0,
            "jan_direction": np.nan,
            "jan_magnitude": np.nan,
            "jan_confidence": np.nan,
        },
    ]
    df1 = pd.DataFrame(rows)
    df2 = pd.DataFrame(rows[:-1] + [dict(rows[-1], teacher_direction=-1.0)])

    monkeypatch.setattr(pd, "read_parquet", lambda *a, **kw: df1)
    out1 = MN.build_daily_news_features("dummy.parquet")

    monkeypatch.setattr(pd, "read_parquet", lambda *a, **kw: df2)
    out2 = MN.build_daily_news_features("dummy.parquet")

    assert list(out1.columns) == ["news_shock_1d", "news_sent_3d", "news_vol_7d"]
    # Day 1: (0.8*0.5*1.0 + (-0.5)*0.4*0.5) / 2 = (0.4 - 0.1) / 2 = 0.15
    d1 = pd.Timestamp("2024-01-01", tz="UTC")
    assert np.isclose(out1.loc[d1, "news_shock_1d"], 0.15, atol=1e-9)
    # Causality: changing Day 2 headline must NOT change Day 1 features
    assert np.allclose(out1.loc[d1].to_numpy(), out2.loc[d1].to_numpy(), atol=1e-12)
    assert out1["news_shock_1d"].between(-1.0, 1.0).all()


def test_infer_micro_tox_pure_numpy_and_npz_constraints(monkeypatch):
    npz_path = MN.DEFAULT_NPZ_PATH
    assert os.path.exists(npz_path), f"Missing trained MLP weights at {npz_path}"
    assert os.path.getsize(npz_path) < 50 * 1024, "micro_tox_mlp.npz must be < 50 KB"

    weights = np.load(npz_path)
    for k in ("W1", "b1", "W2", "b2", "feat_mean", "feat_std"):
        assert k in weights

    # Block torch & pyarrow to prove pure-NumPy inference
    monkeypatch.setitem(sys.modules, "torch", None)
    monkeypatch.setitem(sys.modules, "pyarrow", None)

    idx = pd.date_range("2025-01-01", periods=30, freq="D", tz="UTC")
    rng = np.random.default_rng(7)
    df = pd.DataFrame(
        {
            "btc_rv_1m_ann": rng.uniform(0.2, 1.2, size=30),
            "btc_jump_ratio": rng.uniform(1.0, 8.0, size=30),
            "btc_vpin_1m": rng.uniform(-0.5, 0.5, size=30),
            "eth_vpin_1m": rng.uniform(-0.5, 0.5, size=30),
            "btc_fund_z20": rng.normal(0.0, 1.0, size=30),
        },
        index=idx,
    )
    out = MN.infer_micro_tox(df, npz_path=npz_path)
    assert list(out.columns) == ["micro_tox_score", "micro_imbalance_score"]
    assert out["micro_tox_score"].between(0.0, 1.0).all()
    expected_imb = df["btc_vpin_1m"].to_numpy() * (1.0 - out["micro_tox_score"].to_numpy())
    assert np.allclose(out["micro_imbalance_score"].to_numpy(), expected_imb, atol=1e-12)


def test_train_micro_tox_strict_pre2025_cutoff(tmp_path, monkeypatch):
    """Verify anti-leakage: corrupting 2025-2026 targets must produce identical weights."""
    pytest.importorskip("torch")
    dates = pd.date_range("2020-01-01", "2025-06-30", freq="D", tz="UTC")
    rng = np.random.default_rng(99)
    mm = pd.DataFrame(
        {
            "btc_rv_1m_ann": rng.uniform(0.3, 1.0, size=len(dates)),
            "btc_jump_ratio": rng.uniform(1.0, 6.0, size=len(dates)),
            "btc_vpin_1m": rng.uniform(-0.3, 0.3, size=len(dates)),
            "eth_vpin_1m": rng.uniform(-0.3, 0.3, size=len(dates)),
            "btc_fund_z20": rng.normal(0.0, 1.0, size=len(dates)),
        },
        index=dates,
    )
    mm_csv = tmp_path / "mm.csv"
    mm.to_csv(mm_csv)

    # Normal daily prices
    close1 = 10000.0 * np.exp(np.cumsum(rng.normal(0.0, 0.02, size=len(dates))))
    daily1 = pd.DataFrame(
        {"o": close1, "h": close1 * 1.02, "l": close1 * 0.98, "c": close1},
        index=dates,
    )
    # Corrupted 2025+ prices (huge crashes every day in 2025)
    daily2 = daily1.copy()
    mask_2025 = daily2.index >= pd.Timestamp("2025-01-01", tz="UTC")
    daily2.loc[mask_2025, "l"] = daily2.loc[mask_2025, "c"] * 0.50

    npz1 = str(tmp_path / "m1.npz")
    npz2 = str(tmp_path / "m2.npz")

    monkeypatch.setattr(MN, "_load_daily_ohlc_from_1m", lambda _p: daily1)
    r1 = MN.train_micro_tox_encoder("dummy.parquet", str(mm_csv), out_npz=npz1, epochs=15, seed=123)

    monkeypatch.setattr(MN, "_load_daily_ohlc_from_1m", lambda _p: daily2)
    r2 = MN.train_micro_tox_encoder("dummy.parquet", str(mm_csv), out_npz=npz2, epochs=15, seed=123)

    assert r1["train_n"] == r2["train_n"]
    w1 = np.load(npz1)
    w2 = np.load(npz2)
    for k in ("W1", "b1", "W2", "b2", "feat_mean", "feat_std"):
        assert np.allclose(w1[k], w2[k], atol=1e-7), f"2025+ data leaked into {k}!"


def test_feature_store_32_features_and_graceful_default():
    btc, eth = AD._make_synthetic_ohlcv(n=90, seed=11)
    feats_no_mm = FS.build_unified_features(btc, eth, multimodal=None)
    assert feats_no_mm.shape[1] == 32
    for col in FS.EXTRA_MULTIMODAL_COLS:
        assert col in feats_no_mm.columns
        assert (feats_no_mm[col] == 0.0).all()

    mm = pd.DataFrame(0.42, index=btc.index, columns=list(FS.ALL_MULTIMODAL_COLS))
    feats_with_mm = FS.build_unified_features(btc, eth, multimodal=mm)
    assert feats_with_mm.shape[1] == 32
    for col in FS.EXTRA_MULTIMODAL_COLS:
        assert np.allclose(feats_with_mm[col].to_numpy(), 0.42)
