"""Multimodal Feature Pipeline for QuantLab.
Builds strictly causal 12-column multimodal daily dataset from on-disk parquet candles,
DVOL, funding, stablecoins, alts, and macro datasets.
"""
from __future__ import annotations

import glob
import os
import numpy as np
import pandas as pd
import pyarrow.parquet as pq

MULTIMODAL_COLS: tuple[str, ...] = (
    "btc_rv_1m_ann",
    "btc_jump_ratio",
    "btc_vpin_1m",
    "eth_vpin_1m",
    "btc_vrp",
    "dvol_z20",
    "btc_fund_z20",
    "fund_spread_be",
    "stable_flow_21d",
    "alt_breadth_20d",
    "qqq_ret21",
    "uup_ret21",
)


def _load_1m_parquet_daily_agg(parquet_path: str) -> pd.DataFrame:
    """Reads 1m parquet candles and aggregates to daily UTC bars with strictly causal metrics."""
    pf = pq.ParquetFile(parquet_path)
    frames = []
    for i in range(pf.num_row_groups):
        df = pf.read_row_group(i).to_pandas()
        if df.index.name == 't' or not isinstance(df.index, pd.RangeIndex):
            df = df.reset_index()
        if 't' in df.columns:
            df['datetime'] = pd.to_datetime(df['t'], unit='ns', utc=True)
            df.set_index('datetime', inplace=True)
        frames.append(df)
    table = pd.concat(frames, ignore_index=False)
    table.sort_index(inplace=True)
    return table


def build_multimodal_daily_csv(out_path: str = "/home/everett/piquant-collect/daily_csv/multimodal_1D.csv") -> pd.DataFrame:
    print("Loading BTC 1m parquet...")
    btc_1m = _load_1m_parquet_daily_agg("/home/everett/piquant-collect/train/BTC-USD_1m.parquet")
    print("Loading ETH 1m parquet...")
    eth_1m = _load_1m_parquet_daily_agg("/home/everett/piquant-collect/train/ETH-USD_1m.parquet")

    all_dates = pd.date_range(
        start=min(btc_1m.index.min(), eth_1m.index.min()).normalize(),
        end=max(btc_1m.index.max(), eth_1m.index.max()).normalize(),
        freq='D',
        tz='UTC'
    )
    df_out = pd.DataFrame(index=all_dates)

    print("Computing BTC 1m intraday features...")
    btc_groups = btc_1m.groupby(btc_1m.index.normalize())
    
    rv_list, jump_list, vpin_btc_list = [], [], []
    for day, grp in btc_groups:
        r = grp['c'].pct_change()
        r_clean = r[(r.abs() <= 1.0) & r.notna()]
        rv = np.sqrt(np.sum(r_clean**2) * 365) if len(r_clean) > 0 else 0.0
        
        r_abs = r_clean.abs()
        r_std = r_clean.std()
        jump = float(r_abs.max() / (r_std + 1e-12)) if len(r_clean) > 0 and r_std > 0 else 0.0
        jump = min(max(jump, 0.0), 30.0)
        
        sv = np.sign(grp['c'] - grp['o']) * grp['v']
        tot_v = grp['v'].sum()
        vpin = float(sv.sum() / (tot_v + 1e-12)) if tot_v > 0 else 0.0
        vpin = min(max(vpin, -1.0), 1.0)
        
        rv_list.append((day, rv))
        jump_list.append((day, jump))
        vpin_btc_list.append((day, vpin))

    df_out['btc_rv_1m_ann'] = pd.Series(dict(rv_list)).reindex(all_dates).fillna(0.0)
    df_out['btc_jump_ratio'] = pd.Series(dict(jump_list)).reindex(all_dates).fillna(0.0)
    df_out['btc_vpin_1m'] = pd.Series(dict(vpin_btc_list)).reindex(all_dates).fillna(0.0)

    print("Computing ETH 1m VPIN...")
    eth_groups = eth_1m.groupby(eth_1m.index.normalize())
    vpin_eth_list = []
    for day, grp in eth_groups:
        sv = np.sign(grp['c'] - grp['o']) * grp['v']
        tot_v = grp['v'].sum()
        vpin = float(sv.sum() / (tot_v + 1e-12)) if tot_v > 0 else 0.0
        vpin = min(max(vpin, -1.0), 1.0)
        vpin_eth_list.append((day, vpin))
    df_out['eth_vpin_1m'] = pd.Series(dict(vpin_eth_list)).reindex(all_dates).fillna(0.0)

    print("Loading DVOL...")
    dvol_df = pd.read_csv('/home/everett/piquant-data/dvol/BTC.csv')
    dvol_df['date'] = pd.to_datetime(dvol_df['date'], utc=True)
    dvol_series = dvol_df.set_index('date')['close'].reindex(all_dates)
    dvol_btc = dvol_series
    
    df_out['dvol_btc'] = dvol_btc
    df_out['btc_vrp'] = (dvol_btc / 100.0) - df_out['btc_rv_1m_ann']
    df_out['btc_vrp'] = df_out['btc_vrp'].fillna(0.0)

    dvol_rolling20_mean = dvol_btc.rolling(20).mean()
    dvol_rolling20_std = dvol_btc.rolling(20).std()
    df_out['dvol_z20'] = ((dvol_btc - dvol_rolling20_mean) / (dvol_rolling20_std + 1e-12)).fillna(0.0)

    print("Loading Funding rates...")
    def _load_funding_all(pattern: str) -> pd.Series:
        files = glob.glob(pattern)
        frames = []
        for f in sorted(files):
            try:
                temp = pd.read_csv(f)
                if 'calc_time' in temp.columns:
                    frames.append(temp[['calc_time', 'last_funding_rate']])
            except Exception:
                pass
        if not frames:
            return pd.Series(dtype=float)
        f_all = pd.concat(frames, ignore_index=True)
        f_all['date'] = pd.to_datetime(f_all['calc_time'], unit='ms', utc=True).dt.normalize()
        daily_sum = f_all.groupby('date')['last_funding_rate'].sum()
        return daily_sum

    btc_fund_daily = _load_funding_all('/home/everett/piquant-collect/funding/csv/BTCUSDT-fundingRate-*.csv')
    eth_fund_daily = _load_funding_all('/home/everett/piquant-collect/funding/csv/ETHUSDT-fundingRate-*.csv')

    btc_fund_daily = btc_fund_daily.reindex(all_dates).fillna(0.0)
    eth_fund_daily = eth_fund_daily.reindex(all_dates).fillna(0.0)

    btc_fund_sum20 = btc_fund_daily.rolling(20).sum()
    btc_fund_std20 = btc_fund_daily.rolling(20).std()
    df_out['btc_fund_z20'] = ((btc_fund_daily - btc_fund_sum20 / 20.0) / (btc_fund_std20 + 1e-12)).fillna(0.0)

    fund_diff = (btc_fund_daily - eth_fund_daily) * 10000.0
    df_out['fund_spread_be'] = fund_diff.rolling(7).mean().fillna(0.0)

    print("Loading Stablecoins...")
    stable_df = pd.read_csv('/home/everett/piquant-data/stablecoins/total.csv')
    stable_df['date'] = pd.to_datetime(stable_df['date'], utc=True)
    stable_series = stable_df.set_index('date')['usd'].reindex(all_dates).ffill()
    df_out['stable_flow_21d'] = stable_series.pct_change(21).fillna(0.0)

    print("Loading Alts...")
    alt_files = [f for f in glob.glob('/home/everett/piquant-data/alts/*.csv') if not f.endswith('SOURCES.json')]
    alt_close_frames = {}
    for f in alt_files:
        symbol = os.path.basename(f).replace('.csv', '')
        try:
            adf = pd.read_csv(f)
            adf['date'] = pd.to_datetime(adf['date'], utc=True).dt.normalize()
            alt_close_frames[symbol] = adf.set_index('date')['close'].reindex(all_dates)
        except Exception:
            pass
    
    if alt_close_frames:
        alt_closes = pd.DataFrame(alt_close_frames)
        sma20 = alt_closes.rolling(20).mean()
        breadth_list = []
        for idx_val in all_dates:
            valid_alts = []
            for col in alt_closes.columns:
                s = alt_closes.loc[:idx_val, col].dropna()
                if len(s) >= 20:
                    valid_alts.append(col)
            if not valid_alts:
                breadth_list.append(0.0)
                continue
            sub = alt_closes.loc[idx_val, valid_alts]
            sub_sma = sma20.loc[idx_val, valid_alts]
            valid_sub = sub.notna() & sub_sma.notna()
            if not valid_sub.any():
                breadth_list.append(0.0)
                continue
            frac = (sub[valid_sub] > sub_sma[valid_sub]).mean()
            breadth_list.append(float(frac - 0.5))
        df_out['alt_breadth_20d'] = pd.Series(breadth_list, index=all_dates).fillna(0.0)
    else:
        df_out['alt_breadth_20d'] = 0.0

    print("Loading Macro QQQ and UUP...")
    def _load_macro(path: str) -> pd.Series:
        mdf = pd.read_csv(path)
        mdf['date'] = pd.to_datetime(mdf['date'], utc=True).dt.normalize()
        col = 'adj' if 'adj' in mdf.columns else ('close' if 'close' in mdf.columns else 'Close')
        return mdf.set_index('date')[col].reindex(all_dates).ffill()

    qqq = _load_macro('/home/everett/piquant-data/macro/QQQ_full.csv')
    uup = _load_macro('/home/everett/piquant-data/macro/UUP.csv')

    df_out['qqq_ret21'] = qqq.pct_change(21).fillna(0.0)
    df_out['uup_ret21'] = uup.pct_change(21).fillna(0.0)

    final_df = df_out[list(MULTIMODAL_COLS)].ffill().fillna(0.0)

    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    final_df.to_csv(out_path)
    print(f"Saved multimodal daily CSV to {out_path} ({len(final_df)} rows)")
    return final_df


def load_multimodal_csv(path: str = "/home/everett/piquant-collect/daily_csv/multimodal_1D.csv") -> pd.DataFrame:
    if not os.path.isfile(path):
        return pd.DataFrame()
    df = pd.read_csv(path, index_col=0, parse_dates=True)
    if df.index.tz is None:
        df.index = pd.to_datetime(df.index, utc=True)
    else:
        df.index = pd.to_datetime(df.index, utc=True)
    return df


if __name__ == '__main__':
    build_multimodal_daily_csv()
