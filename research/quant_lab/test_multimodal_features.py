"""Tests for Multimodal Features and Feature Store Integration.
"""
from __future__ import annotations

import sys
import os
import numpy as np
import pandas as pd

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)) + "/../../")
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import multimodal_features as MF
import feature_store as FS
import auto_discover as AD


def test_multimodal_cols_presence():
    assert len(MF.MULTIMODAL_COLS) == 12
    for col in MF.MULTIMODAL_COLS:
        assert col in AD.ALLOWED_FEATURE_SET


def test_build_unified_features_multimodal():
    btc = pd.DataFrame({
        'c': np.linspace(30000, 31000, 50),
        'o': np.linspace(29900, 30900, 50),
        'v': np.ones(50) * 1000
    }, index=pd.date_range('2023-01-01', periods=50, freq='D', tz='UTC'))
    eth = pd.DataFrame({
        'c': np.linspace(2000, 2100, 50),
        'o': np.linspace(1990, 2090, 50),
        'v': np.ones(50) * 5000
    }, index=pd.date_range('2023-01-01', periods=50, freq='D', tz='UTC'))
    
    mm = pd.DataFrame(1.23, index=btc.index, columns=MF.MULTIMODAL_COLS)
    
    feats = FS.build_unified_features(btc, eth, multimodal=mm)
    assert len(feats.columns) == 27
    for col in MF.MULTIMODAL_COLS:
        assert col in feats.columns
        assert np.all(np.isfinite(feats[col].to_numpy()))


def test_causality_sandbox_multimodal_expression():
    expr = "tanh(btc_vrp * 3.0) + ema(stable_flow_21d, 10) * 2.0 - max(btc_fund_z20 - 1.5, 0.0)"
    ok, msg = AD.check_causality_sandbox(expr)
    assert ok, f"Multimodal causality sandbox failed: {msg}"


if __name__ == "__main__":
    test_multimodal_cols_presence()
    test_build_unified_features_multimodal()
    test_causality_sandbox_multimodal_expression()
    print("All multimodal feature tests passed successfully!")
