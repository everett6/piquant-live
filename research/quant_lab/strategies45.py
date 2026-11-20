import numpy as np
import pandas as pd
from sklearn.ensemble import HistGradientBoostingClassifier, HistGradientBoostingRegressor
from sklearn.mixture import GaussianMixture
from feature_store import build_unified_features, pick_col, purged_embargoed_splits

def walk_forward_gbdt_fee_gated_positions(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    fee_hurdle_bps: float = 20.0,
    horizon: int = 5,
    min_train_bars: int = 250,
    retrain_every: int = 60,
    purge_bars: int = 5,
    embargo_bars: int = 2
) -> tuple[pd.Series, pd.Series]:
    """
    Walk-forward strictly causal HistGradientBoostingRegressor predicting horizon-day forward BTC & ETH return in bps.
    Outputs (pos_b, pos_e) in [0.0, 1.0] where position is 1.0 ONLY when pred_bps > fee_hurdle_bps.
    """
    features = build_unified_features(btc, eth)
    
    btc_c = pick_col(btc, 'c', 'close')
    eth_c = pick_col(eth, 'c', 'close').reindex(btc.index)
    
    # Forward returns in bps: (P_{t+horizon} / P_t - 1) * 10000
    btc_fwd_bps = (btc_c.shift(-horizon) / btc_c - 1.0) * 10000.0
    eth_fwd_bps = (eth_c.shift(-horizon) / eth_c - 1.0) * 10000.0
    
    pos_b = pd.Series(0.0, index=btc.index)
    pos_e = pd.Series(0.0, index=btc.index)
    
    n = len(btc)
    model_b = None
    model_e = None
    last_retrain = -9999
    
    # Strictly causal: at bar t, training labels must only use data up to t - horizon - purge_bars
    for t in range(min_train_bars, n):
        # Check if we need to retrain
        if t - last_retrain >= retrain_every or model_b is None:
            # Training cutoff to prevent leakage: max label index allowed is t - horizon - purge_bars
            train_end_idx = t - horizon - purge_bars
            if train_end_idx < 50:
                continue
            
            X_train = features.iloc[:train_end_idx]
            y_b_train = btc_fwd_bps.iloc[:train_end_idx]
            y_e_train = eth_fwd_bps.iloc[:train_end_idx]
            
            # Drop NaN targets / features
            valid = X_train.notna().all(axis=1) & y_b_train.notna() & y_e_train.notna()
            if valid.sum() < 50:
                continue
                
            X_tr = X_train[valid]
            yb_tr = y_b_train[valid]
            ye_tr = y_e_train[valid]
            
            model_b = HistGradientBoostingRegressor(random_state=42)
            model_b.fit(X_tr, yb_tr)
            
            model_e = HistGradientBoostingRegressor(random_state=42)
            model_e.fit(X_tr, ye_tr)
            
            last_retrain = t
            
        if model_b is not None and model_e is not None:
            x_t = features.iloc[[t]]
            if x_t.notna().all(axis=1).values[0]:
                pred_b = model_b.predict(x_t)[0]
                pred_e = model_e.predict(x_t)[0]
                
                pos_b.iloc[t] = 1.0 if pred_b > fee_hurdle_bps else 0.0
                pos_e.iloc[t] = 1.0 if pred_e > fee_hurdle_bps else 0.0
                
    return pos_b, pos_e

def causal_regime_labels(
    features: pd.DataFrame,
    min_train_bars: int = 250,
    retrain_every: int = 60
) -> pd.Series:
    """
    Walk-forward causal 3-state GaussianMixture on ["btc_ret5", "btc_vol20_ann", "pair_spread_z20"],
    mapping clusters by causal training-fold stats to 'TREND', 'MEAN_REVERT', or 'CRISIS'.
    """
        
    cols = ["btc_ret5", "btc_vol20_ann", "pair_spread_z20"]
    regimes = pd.Series("TREND", index=features.index)
    
    n = len(features)
    gmm = None
    cluster_map = {}
    last_retrain = -9999
    
    for t in range(min_train_bars, n):
        if t - last_retrain >= retrain_every or gmm is None:
            train_data = features[cols].iloc[:t].dropna()
            if len(train_data) < 100:
                continue
                
            gmm = GaussianMixture(n_components=3, random_state=42)
            gmm.fit(train_data)
            
            # Map clusters based on training fold stats
            train_preds = gmm.predict(train_data)
            cluster_stats = []
            for c in range(3):
                mask = (train_preds == c)
                mean_vol = train_data.loc[mask, "btc_vol20_ann"].mean()
                mean_abs_trend = train_data.loc[mask, "btc_ret5"].abs().mean()
                cluster_stats.append((c, mean_vol, mean_abs_trend))
                
            # Highest btc_vol20_ann = 'CRISIS'
            cluster_stats.sort(key=lambda x: x[1], reverse=True)
            crisis_cluster = cluster_stats[0][0]
            
            remaining = [x for x in cluster_stats if x[0] != crisis_cluster]
            # Lowest vol & abs trend = 'MEAN_REVERT' (lowest abs trend among remaining or overall)
            remaining.sort(key=lambda x: x[2])
            mean_revert_cluster = remaining[0][0]
            trend_cluster = [x[0] for x in cluster_stats if x[0] not in (crisis_cluster, mean_revert_cluster)][0]
            
            cluster_map = {
                crisis_cluster: "CRISIS",
                mean_revert_cluster: "MEAN_REVERT",
                trend_cluster: "TREND"
            }
            last_retrain = t
            
        if gmm is not None:
            x_t = features[cols].iloc[[t]]
            if x_t.notna().all(axis=1).values[0]:
                c_id = gmm.predict(x_t)[0]
                regimes.iloc[t] = cluster_map.get(c_id, "TREND")
                
    return regimes

def regime_switched_positions(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    qd_b: pd.Series,
    qd_e: pd.Series,
    features: pd.DataFrame,
    regimes: pd.Series,
    fee_hurdle_bps: float = 16.0
) -> tuple[pd.Series, pd.Series]:
    """
    Combines regimes with strategies:
    - TREND: uses qd_b, qd_e (1x Safe-QD)
    - MEAN_REVERT: pairs mean-reversion
    - CRISIS: 0.0 crypto trend
    """
    pos_b = pd.Series(0.0, index=btc.index)
    pos_e = pd.Series(0.0, index=btc.index)

    qb_a = qd_b.reindex(btc.index).fillna(0.0)
    qe_a = qd_e.reindex(btc.index).fillna(0.0)
    reg_a = regimes.reindex(btc.index).fillna("TREND")
    spread_z = features['pair_spread_z20'].reindex(btc.index).fillna(0.0)

    for t in range(len(btc)):
        reg = reg_a.iloc[t]
        if reg == "TREND":
            pos_b.iloc[t] = qb_a.iloc[t]
            pos_e.iloc[t] = qe_a.iloc[t]
        elif reg == "MEAN_REVERT":
            sz = spread_z.iloc[t]
            if sz < -1.5:
                # long BTC 0.6x, short ETH (or long BTC 0.6x, eth 0)
                pos_b.iloc[t] = 0.6
                pos_e.iloc[t] = 0.0
            elif sz > 1.5:
                pos_b.iloc[t] = 0.0
                pos_e.iloc[t] = 0.6
            else:
                pos_b.iloc[t] = 0.0
                pos_e.iloc[t] = 0.0
        else: # CRISIS
            pos_b.iloc[t] = 0.0
            pos_e.iloc[t] = 0.0
            
    return pos_b, pos_e

def meta_label_qd_positions(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    qd_b: pd.Series,
    qd_e: pd.Series,
    features: pd.DataFrame,
    fee_hurdle_bps: float = 18.0,
    min_train_bars: int = 250,
    retrain_every: int = 60,
    purge_bars: int = 5
) -> tuple[pd.Series, pd.Series]:
    """
    Purged walk-forward HistGradientBoostingClassifier predicting P(forward_5d_ret_bps > fee_hurdle_bps),
    scaling qd_b/qd_e by 0.50 + 0.75 * clip(p - 0.5, 0.0, 0.5) * 2.0.
    """
    horizon = 5
    btc_c = pick_col(btc, 'c', 'close')
    eth_c = pick_col(eth, 'c', 'close')
    
    btc_fwd_bps = (btc_c.shift(-horizon) / btc_c - 1.0) * 10000.0
    y_binary = (btc_fwd_bps > fee_hurdle_bps).astype(int)
    
    pos_b = pd.Series(0.0, index=btc.index)
    pos_e = pd.Series(0.0, index=btc.index)
    qb_a = qd_b.reindex(btc.index).fillna(0.0)
    qe_a = qd_e.reindex(btc.index).fillna(0.0)

    n = len(btc)
    model = None
    last_retrain = -9999

    for t in range(min_train_bars, n):
        if t - last_retrain >= retrain_every or model is None:
            train_end_idx = t - horizon - purge_bars
            if train_end_idx < 50:
                continue
                
            X_train = features.iloc[:train_end_idx]
            y_train = y_binary.iloc[:train_end_idx]
            
            valid = X_train.notna().all(axis=1) & y_train.notna()
            if valid.sum() < 50 or y_train[valid].nunique() < 2:
                continue
                
            model = HistGradientBoostingClassifier(random_state=42)
            model.fit(X_train[valid], y_train[valid])
            last_retrain = t
            
        if model is not None:
            x_t = features.iloc[[t]]
            if x_t.notna().all(axis=1).values[0]:
                probs = model.predict_proba(x_t)[0]
                # prob of class 1
                p = probs[1] if len(probs) > 1 else 0.5
                
                scale = 0.50 + 0.75 * np.clip(p - 0.5, 0.0, 0.5) * 2.0
                
                qb = qb_a.iloc[t]
                qe = qe_a.iloc[t]
                
                pos_b.iloc[t] = qb * scale if qb > 0 else 0.0
                pos_e.iloc[t] = qe * scale if qe > 0 else 0.0
                
    return pos_b, pos_e
