"""Microstructure Toxicity MLP & Causal News Sentiment Features for QuantLab.

Pi 5 friendly: module import and pure-NumPy inference (`infer_micro_tox`) NEVER
require `pyarrow` or `torch` at import time. `pyarrow` and `torch` are imported
lazily inside `build_daily_news_features` and `train_micro_tox_encoder`.
"""
from __future__ import annotations

import os
from typing import Any

import numpy as np
import pandas as pd

EXTRA_MULTIMODAL_COLS: tuple[str, ...] = (
    "micro_tox_score",
    "micro_imbalance_score",
    "news_sent_3d",
    "news_shock_1d",
    "news_vol_7d",
)

MICRO_TOX_INPUT_COLS: tuple[str, ...] = (
    "btc_rv_1m_ann",
    "btc_jump_ratio",
    "btc_vpin_1m",
    "eth_vpin_1m",
    "vol_ratio_5_20_proxy",
    "btc_fund_z20",
)

DEFAULT_NPZ_PATH = os.path.join(
    os.path.dirname(os.path.abspath(__file__)), "results", "micro_tox_mlp.npz"
)


def _silu(x: np.ndarray) -> np.ndarray:
    sig = 1.0 / (1.0 + np.exp(-np.clip(x, -30.0, 30.0)))
    return x * sig


def _sigmoid(x: np.ndarray) -> np.ndarray:
    return 1.0 / (1.0 + np.exp(-np.clip(x, -30.0, 30.0)))


def compute_vol_ratio_proxy(df: pd.DataFrame) -> pd.Series:
    """Strictly causal 5d/20d volatility ratio proxy from `vol_ratio_5_20` or `btc_rv_1m_ann`."""
    if "vol_ratio_5_20_proxy" in df.columns:
        return pd.to_numeric(df["vol_ratio_5_20_proxy"], errors="coerce").fillna(1.0)
    if "vol_ratio_5_20" in df.columns:
        return pd.to_numeric(df["vol_ratio_5_20"], errors="coerce").fillna(1.0)
    rv = pd.to_numeric(df.get("btc_rv_1m_ann", 0.0), errors="coerce").fillna(0.0)
    rv5 = rv.rolling(5, min_periods=1).mean()
    rv20 = rv.rolling(20, min_periods=1).mean()
    return (rv5 / (rv20 + 1e-8)).replace([np.inf, -np.inf], 1.0).fillna(1.0)


def build_daily_news_features(
    labels_path: str = "~/piquant-collect/train/labels.parquet",
) -> pd.DataFrame:
    """Aggregate headline-level sentiment labels into strictly causal daily UTC features.

    All headlines on UTC day `t` (`00:00:00..23:59:59 UTC`) are floored to day `t`
    and feed day `t` close signal for day `t+1` execution.
    """
    path = os.path.expanduser(labels_path)
    cols = [
        "ts",
        "jan_direction",
        "jan_magnitude",
        "jan_confidence",
        "teacher_direction",
        "teacher_magnitude",
    ]
    raw = pd.read_parquet(path, columns=cols)
    if raw.empty:
        return pd.DataFrame(columns=["news_shock_1d", "news_sent_3d", "news_vol_7d"])

    ts_utc = pd.to_datetime(raw["ts"], utc=True)
    day = ts_utc.dt.floor("D")

    t_dir = pd.to_numeric(raw["teacher_direction"], errors="coerce")
    t_mag = pd.to_numeric(raw["teacher_magnitude"], errors="coerce")
    j_dir = pd.to_numeric(raw["jan_direction"], errors="coerce")
    j_mag = pd.to_numeric(raw["jan_magnitude"], errors="coerce")
    j_conf = pd.to_numeric(raw["jan_confidence"], errors="coerce")

    dir_val = t_dir.fillna(j_dir).fillna(0.0)
    mag_val = t_mag.fillna(j_mag).fillna(0.0)
    # Teacher rows do not carry jan_confidence; default their confidence to 1.0
    conf_val = np.where(
        t_dir.notna() & t_mag.notna(),
        j_conf.fillna(1.0),
        j_conf.fillna(0.0),
    )
    impact = np.clip((dir_val * mag_val * conf_val).to_numpy(dtype=float), -1.0, 1.0)

    tmp = pd.DataFrame({"day": day, "impact": impact})
    grouped = tmp.groupby("day", sort=True)["impact"]
    daily_mean = grouped.mean().clip(-1.0, 1.0)
    daily_cnt = grouped.size()

    all_days = pd.date_range(
        start=daily_mean.index.min(),
        end=daily_mean.index.max(),
        freq="D",
        tz="UTC",
    )
    news_shock_1d = daily_mean.reindex(all_days).fillna(0.0).clip(-1.0, 1.0)
    news_sent_3d = news_shock_1d.ewm(span=3, adjust=False).mean().clip(-1.0, 1.0)

    cnt_series = daily_cnt.reindex(all_days).fillna(0.0)
    log_cnt_7d = np.log1p(cnt_series.rolling(7, min_periods=1).sum())
    roll_mean = log_cnt_7d.rolling(60, min_periods=5).mean()
    roll_std = log_cnt_7d.rolling(60, min_periods=5).std()
    news_vol_7d = ((log_cnt_7d - roll_mean) / (roll_std + 1e-8)).replace(
        [np.inf, -np.inf], 0.0
    ).fillna(0.0)

    return pd.DataFrame(
        {
            "news_shock_1d": news_shock_1d.astype(float),
            "news_sent_3d": news_sent_3d.astype(float),
            "news_vol_7d": news_vol_7d.astype(float),
        },
        index=all_days,
    )


def _load_daily_ohlc_from_1m(btc_1m_path: str) -> pd.DataFrame:
    """Load 1-minute BTC parquet and resample strictly to daily UTC OHLC."""
    import pyarrow.parquet as pq

    path = os.path.expanduser(btc_1m_path)
    pf = pq.ParquetFile(path)
    daily_parts = []
    for i in range(pf.num_row_groups):
        chunk = pf.read_row_group(i, columns=["t", "o", "h", "l", "c"]).to_pandas()
        if "t" in chunk.columns:
            idx = pd.to_datetime(chunk["t"], unit="ns", utc=True)
            chunk = chunk.set_index(idx)
        else:
            chunk.index = pd.to_datetime(chunk.index, utc=True)
        day_grp = chunk.groupby(chunk.index.floor("D")).agg(
            o=("o", "first"),
            h=("h", "max"),
            l=("l", "min"),
            c=("c", "last"),
        )
        daily_parts.append(day_grp)

    combined = pd.concat(daily_parts).sort_index()
    # Re-aggregate in case a day spans two row groups
    daily = combined.groupby(combined.index).agg(
        o=("o", "first"),
        h=("h", "max"),
        l=("l", "min"),
        c=("c", "last"),
    )
    return daily


def train_micro_tox_encoder(
    btc_1m_path: str = "~/piquant-collect/train/BTC-USD_1m.parquet",
    mm_csv_path: str = "~/piquant-collect/daily_csv/multimodal_1D.csv",
    out_npz: str = DEFAULT_NPZ_PATH,
    epochs: int = 120,
    lr: float = 1e-2,
    seed: int = 42,
) -> dict[str, Any]:
    """Train a 2-layer PyTorch MLP (6 -> 24 -> 1, SiLU + Sigmoid) strictly on
    `2020-01-01 <= t < 2025-01-01` and export weights to a <50 KB `.npz` file.

    Returns a summary dict with train_auc (pre-2025) and oos_auc (2025-2026).
    """
    import torch
    import torch.nn as nn
    from sklearn.metrics import roc_auc_score

    mm_path = os.path.expanduser(mm_csv_path)
    mm = pd.read_csv(mm_path, index_col=0, parse_dates=True)
    if mm.index.tz is None:
        mm.index = mm.index.tz_localize("UTC")
    else:
        mm.index = mm.index.tz_convert("UTC")
    mm = mm.sort_index()
    mm["vol_ratio_5_20_proxy"] = compute_vol_ratio_proxy(mm)

    daily = _load_daily_ohlc_from_1m(btc_1m_path)
    # Next-day adverse tail risk target (t+1 return < -2.5% or t+1 intraday range > 6.0%)
    next_ret = daily["c"].shift(-1) / daily["c"] - 1.0
    next_range = (daily["h"].shift(-1) - daily["l"].shift(-1)) / daily["c"].shift(-1)
    target = ((next_ret < -0.025) | (next_range > 0.060)).astype(float)

    aligned = mm[list(MICRO_TOX_INPUT_COLS)].copy()
    aligned["target"] = target.reindex(aligned.index)
    aligned = aligned.dropna(subset=["target"])

    # STRICT ANTI-LEAKAGE: train strictly on 2020-01-01 <= t < 2024-12-31
    # (so even t+1 target never touches 2025-01-01)
    train_mask = (aligned.index >= pd.Timestamp("2020-01-01", tz="UTC")) & (
        aligned.index < pd.Timestamp("2024-12-31", tz="UTC")
    )
    oos_mask = aligned.index >= pd.Timestamp("2025-01-01", tz="UTC")

    train_df = aligned.loc[train_mask]
    oos_df = aligned.loc[oos_mask]

    X_train_raw = train_df[list(MICRO_TOX_INPUT_COLS)].fillna(0.0).to_numpy(dtype=np.float32)
    y_train = train_df["target"].to_numpy(dtype=np.float32)

    feat_mean = X_train_raw.mean(axis=0)
    feat_std = np.where(X_train_raw.std(axis=0) > 1e-6, X_train_raw.std(axis=0), 1.0).astype(
        np.float32
    )
    X_train = np.clip((X_train_raw - feat_mean) / feat_std, -6.0, 6.0)

    torch.manual_seed(seed)
    np.random.seed(seed)

    model = nn.Sequential(
        nn.Linear(len(MICRO_TOX_INPUT_COLS), 24),
        nn.SiLU(),
        nn.Linear(24, 1),
        nn.Sigmoid(),
    )
    pos_rate = float(y_train.mean()) if len(y_train) > 0 else 0.2
    pos_weight = (1.0 - pos_rate) / max(pos_rate, 1e-4)

    opt = torch.optim.AdamW(model.parameters(), lr=lr, weight_decay=1e-2)
    X_t = torch.from_numpy(X_train)
    y_t = torch.from_numpy(y_train).unsqueeze(1)
    weights_t = torch.where(y_t > 0.5, pos_weight, 1.0)
    bce = nn.BCELoss(weight=weights_t)

    model.train()
    for _ in range(epochs):
        opt.zero_grad()
        pred = model(X_t)
        loss = bce(pred, y_t)
        loss.backward()
        opt.step()

    model.eval()
    with torch.no_grad():
        train_probs = model(X_t).squeeze(1).numpy()

    # Export weights for pure-NumPy inference: X @ W1 + b1 -> SiLU -> @ W2 + b2 -> Sigmoid
    l1, _, l2, _ = model
    W1 = l1.weight.detach().numpy().T.astype(np.float32)  # (6, 24)
    b1 = l1.bias.detach().numpy().astype(np.float32)      # (24,)
    W2 = l2.weight.detach().numpy().T.astype(np.float32)  # (24, 1)
    b2 = l2.bias.detach().numpy().astype(np.float32)      # (1,)

    os.makedirs(os.path.dirname(os.path.abspath(out_npz)), exist_ok=True)
    np.savez(
        out_npz,
        W1=W1,
        b1=b1,
        W2=W2,
        b2=b2,
        feat_mean=feat_mean.astype(np.float32),
        feat_std=feat_std.astype(np.float32),
    )

    train_auc = float(roc_auc_score(y_train, train_probs)) if len(np.unique(y_train)) > 1 else 0.5
    oos_auc = None
    oos_n = int(len(oos_df))
    if oos_n > 0 and len(np.unique(oos_df["target"].to_numpy())) > 1:
        oos_scores = infer_micro_tox(oos_df, npz_path=out_npz)["micro_tox_score"].to_numpy()
        oos_auc = float(roc_auc_score(oos_df["target"].to_numpy(), oos_scores))

    return {
        "npz_path": os.path.abspath(out_npz),
        "npz_bytes": os.path.getsize(out_npz),
        "train_n": int(len(train_df)),
        "train_pos_rate": round(pos_rate, 4),
        "train_auc": round(train_auc, 4),
        "oos_n": oos_n,
        "oos_pos_rate": round(float(oos_df["target"].mean()), 4) if oos_n > 0 else None,
        "oos_auc": round(oos_auc, 4) if oos_auc is not None else None,
    }


def infer_micro_tox(
    df: pd.DataFrame,
    npz_path: str = DEFAULT_NPZ_PATH,
) -> pd.DataFrame:
    """Pure-NumPy inference for `micro_tox_score` in [0, 1] and `micro_imbalance_score`."""
    weights = np.load(os.path.expanduser(npz_path))
    W1 = weights["W1"]
    b1 = weights["b1"]
    W2 = weights["W2"]
    b2 = weights["b2"]
    feat_mean = weights["feat_mean"]
    feat_std = weights["feat_std"]

    work = df.copy()
    if "vol_ratio_5_20_proxy" not in work.columns:
        work["vol_ratio_5_20_proxy"] = compute_vol_ratio_proxy(work)

    cols_mat = []
    for col in MICRO_TOX_INPUT_COLS:
        if col in work.columns:
            s = pd.to_numeric(work[col], errors="coerce").fillna(0.0).to_numpy(dtype=np.float32)
        else:
            s = np.zeros(len(work), dtype=np.float32)
        cols_mat.append(s)
    X_raw = np.column_stack(cols_mat) if len(work) > 0 else np.zeros((0, len(MICRO_TOX_INPUT_COLS)), dtype=np.float32)
    X_norm = np.clip((X_raw - feat_mean) / (feat_std + 1e-8), -6.0, 6.0)

    h = _silu(X_norm @ W1 + b1)
    tox_score = _sigmoid(h @ W2 + b2).ravel()
    vpin = work["btc_vpin_1m"].fillna(0.0).to_numpy(dtype=float) if "btc_vpin_1m" in work.columns else np.zeros(len(work))
    imbalance = vpin * (1.0 - tox_score)

    return pd.DataFrame(
        {
            "micro_tox_score": tox_score.astype(float),
            "micro_imbalance_score": imbalance.astype(float),
        },
        index=df.index,
    )


def append_micro_news_to_multimodal(
    mm_path: str = "~/piquant-collect/daily_csv/multimodal_1D.csv",
    labels_path: str = "~/piquant-collect/train/labels.parquet",
    npz_path: str = DEFAULT_NPZ_PATH,
) -> pd.DataFrame:
    """Causally merge the 5 micro_tox + news_sentiment columns into `multimodal_1D.csv`,
    keeping all 12 existing multimodal columns intact.
    """
    mm_file = os.path.expanduser(mm_path)
    mm = pd.read_csv(mm_file, index_col=0, parse_dates=True)
    if mm.index.tz is None:
        mm.index = mm.index.tz_localize("UTC")
    else:
        mm.index = mm.index.tz_convert("UTC")
    mm = mm.sort_index()

    micro_df = infer_micro_tox(mm, npz_path=npz_path)
    news_df = build_daily_news_features(labels_path=labels_path)
    news_aligned = news_df.reindex(mm.index).ffill().fillna(0.0)

    for col in ("micro_tox_score", "micro_imbalance_score"):
        mm[col] = micro_df[col].ffill().fillna(0.0)
    for col in ("news_sent_3d", "news_shock_1d", "news_vol_7d"):
        mm[col] = news_aligned[col].ffill().fillna(0.0)

    tmp_file = mm_file + ".tmp"
    mm.to_csv(tmp_file)
    os.replace(tmp_file, mm_file)
    return mm
