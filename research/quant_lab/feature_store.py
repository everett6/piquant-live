import numpy as np
import pandas as pd

def pick_col(df: pd.DataFrame, short: str, long: str) -> pd.Series:
    return df[short] if short in df.columns else df[long]


def build_unified_features(btc: pd.DataFrame, eth: pd.DataFrame) -> pd.DataFrame:
    """
    Computes strictly causal features using ONLY data up to close t.
    Accepts either ('o','h','l','c','v') or ('open','high','low','close','volume').
    """
    df = pd.DataFrame(index=btc.index)
    
    # Prices & returns
    btc_c = pick_col(btc, 'c', 'close')
    eth_c = pick_col(eth, 'c', 'close')
    btc_o = pick_col(btc, 'o', 'open')
    btc_v = pick_col(btc, 'v', 'volume')
    eth_v = pick_col(eth, 'v', 'volume')

    df['btc_ret1'] = btc_c.pct_change(1)
    df['btc_ret5'] = btc_c.pct_change(5)
    df['btc_ret21'] = btc_c.pct_change(21)
    
    df['eth_ret5'] = eth_c.pct_change(5)
    df['eth_ret21'] = eth_c.pct_change(21)
    
    # Trend & momentum (SMA50 distance)
    btc_sma50 = btc_c.rolling(50).mean()
    eth_sma50 = eth_c.rolling(50).mean()
    df['btc_sma50_dist'] = (btc_c - btc_sma50) / btc_sma50
    df['eth_sma50_dist'] = (eth_c - eth_sma50) / eth_sma50
    
    # Volatility & regime
    btc_vol20 = btc_c.pct_change().rolling(20).std() * np.sqrt(365)
    eth_vol20 = eth_c.pct_change().rolling(20).std() * np.sqrt(365)
    df['btc_vol20_ann'] = btc_vol20
    df['eth_vol20_ann'] = eth_vol20
    
    vol5 = btc_c.pct_change().rolling(5).std()
    vol20 = btc_c.pct_change().rolling(20).std()
    df['vol_ratio_5_20'] = vol5 / (vol20 + 1e-12)
    
    # Volume / order-flow proxy
    btc_v_mean20 = btc_v.rolling(20).mean()
    btc_v_std20 = btc_v.rolling(20).std()
    df['btc_vol_z20'] = (btc_v - btc_v_mean20) / (btc_v_std20 + 1e-12)
    
    sign_flow = np.sign(btc_c - btc_o) * btc_v
    df['btc_signed_flow5'] = sign_flow.rolling(5).sum() / (btc_v.rolling(20).sum() + 1e-12)
    
    # Cointegration / Pairs spread
    log_spread = np.log(btc_c / eth_c)
    spread_mean20 = log_spread.rolling(20).mean()
    spread_std20 = log_spread.rolling(20).std()
    df['pair_spread_z20'] = (log_spread - spread_mean20) / (spread_std20 + 1e-12)
    
    # Cross-asset lead-lag
    eth_ret1 = eth_c.pct_change(1)
    df['btc_minus_eth_1d'] = df['btc_ret1'] - eth_ret1
    df['btc_minus_eth_5d'] = df['btc_ret5'] - eth_c.pct_change(5)
    
    return df

def purged_embargoed_splits(n_samples: int, n_splits: int = 5, purge_bars: int = 5, embargo_bars: int = 2):
    """
    Yields (train_idx, test_idx) numpy arrays implementing Marcos López de Prado
    Purged & Embargoed K-Fold CV.
    """
    indices = np.arange(n_samples)
    test_folds = np.array_split(indices, n_splits)
    
    for i in range(n_splits):
        test_idx = test_folds[i]
        test_start = test_idx[0]
        test_end = test_idx[-1]
        
        # Purge: remove purge_bars immediately before test_start
        # Embargo: remove purge_bars + embargo_bars immediately after test_end
        purge_start = test_start - purge_bars
        embargo_end = test_end + purge_bars + embargo_bars
        
        train_mask = (indices < purge_start) | (indices > embargo_end)
        # Also ensure we don't include points inside test_idx (redundant with mask if indices are contiguous, but safe)
        train_mask = train_mask & ~((indices >= test_start) & (indices <= test_end))
        
        train_idx = indices[train_mask]
        yield train_idx, test_idx
