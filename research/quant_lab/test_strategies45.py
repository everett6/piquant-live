import numpy as np
import pandas as pd
import pytest

from feature_store import build_unified_features, purged_embargoed_splits
from strategies45 import (
    walk_forward_gbdt_fee_gated_positions,
    causal_regime_labels,
    meta_label_qd_positions
)

def make_dummy_data(n=400):
    np.random.seed(42)
    dates = pd.date_range("2020-01-01", periods=n, freq="D")
    btc_price = 10000.0 * np.cumprod(1.0 + np.random.normal(0.001, 0.02, n))
    eth_price = 500.0 * np.cumprod(1.0 + np.random.normal(0.001, 0.025, n))
    volume = np.random.uniform(100, 1000, n)
    
    btc = pd.DataFrame({
        'open': btc_price * 0.99,
        'high': btc_price * 1.02,
        'low': btc_price * 0.98,
        'close': btc_price,
        'volume': volume
    }, index=dates)
    
    eth = pd.DataFrame({
        'open': eth_price * 0.99,
        'high': eth_price * 1.02,
        'low': eth_price * 0.98,
        'close': eth_price,
        'volume': volume * 1.5
    }, index=dates)
    
    return btc, eth

def test_purged_embargoed_splits():
    n = 100
    purge = 5
    embargo = 2
    for train_idx, test_idx in purged_embargoed_splits(n, n_splits=5, purge_bars=purge, embargo_bars=embargo):
        test_start = test_idx[0]
        test_end = test_idx[-1]
        
        # Check that no train_idx falls within [test_start - purge, test_end + purge + embargo]
        for tr in train_idx:
            assert not (test_start - purge <= tr <= test_end + purge + embargo)

def test_causality_features_and_strategies():
    btc, eth = make_dummy_data(450)
    
    # Perturb future data at day 350+
    btc_perturbed = btc.copy()
    btc_perturbed.iloc[350:, btc_perturbed.columns.get_loc('close')] *= 2.0
    
    feats_orig = build_unified_features(btc, eth)
    feats_pert = build_unified_features(btc_perturbed, eth)
    
    # Check features at t < 320 are identical
    pd.testing.assert_frame_equal(
        feats_orig.iloc[:320],
        feats_pert.iloc[:320]
    )
    
    # Test walk-forward causality
    pos_b_orig, pos_e_orig = walk_forward_gbdt_fee_gated_positions(btc, eth, min_train_bars=250, retrain_every=50)
    pos_b_pert, pos_e_pert = walk_forward_gbdt_fee_gated_positions(btc_perturbed, eth, min_train_bars=250, retrain_every=50)
    
    # Positions before perturbation should be identical
    pd.testing.assert_series_equal(pos_b_orig.iloc[:320], pos_b_pert.iloc[:320])

def test_fee_hurdle_strict():
    btc, eth = make_dummy_data(350)
    # Extremely high fee hurdle should result in all 0 positions
    pos_b, pos_e = walk_forward_gbdt_fee_gated_positions(btc, eth, fee_hurdle_bps=10000.0, min_train_bars=250)
    assert (pos_b == 0.0).all()
    assert (pos_e == 0.0).all()
