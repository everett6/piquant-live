"""PREREG47: gradient-boosted trees (HistGradientBoostingClassifier) on daily BTC/ETH, walk-forward, no look-ahead."""
from __future__ import annotations

import numpy as np
import pandas as pd
from sklearn.ensemble import HistGradientBoostingClassifier
from sklearn.metrics import roc_auc_score

PARAMS = dict(max_depth=3, learning_rate=0.05, max_iter=200, min_samples_leaf=50, l2_regularization=1.0,
              random_state=0, early_stopping=False)
HORIZON = 5


def features(daily: pd.DataFrame, other: pd.DataFrame, coin_flag: int) -> pd.DataFrame:
    """Features at close t from data up to and including t."""
    c, v = daily["c"], daily["v"]
    lr = np.log(c).diff()
    f = pd.DataFrame(index=daily.index)
    for n in (1, 3, 7, 14, 30, 60, 120):
        f[f"ret_{n}"] = np.log(c / c.shift(n))
    f["vol_7"] = lr.rolling(7).std()
    f["vol_30"] = lr.rolling(30).std()
    f["vol_ratio"] = f["vol_7"] / f["vol_30"]
    f["sma50"] = c / c.rolling(50).mean() - 1
    f["sma200"] = c / c.rolling(200).mean() - 1
    f["dd_90"] = c / c.rolling(90).max() - 1
    f["volume_ratio"] = v.rolling(30).mean() / v.rolling(180).mean()
    f["dow"] = daily.index.dayofweek
    oc = other["c"].reindex(daily.index)
    f["other_ret_7"] = np.log(oc / oc.shift(7))
    f["other_ret_30"] = np.log(oc / oc.shift(30))
    f["coin"] = coin_flag
    return f.replace([np.inf, -np.inf], np.nan)


def forward_up(close: pd.Series, h: int = HORIZON) -> pd.Series:
    """1.0 if close[t+h] > close[t], NaN where t+h is not yet known."""
    fwd = close.shift(-h) / close - 1
    return (fwd > 0).astype(float).where(fwd.notna())


def walk_forward_proba(X: pd.DataFrame, y: pd.Series, dates: pd.Series, start: str, refit: int = 63,
                       embargo: int = HORIZON) -> pd.Series:
    """Out-of-sample P(y=1) for rows dated >= start. Refit every `refit` unique dates on rows dated more than
    `embargo` days before the block start whose label is known; each model only predicts its own block."""
    ok = X.notna().all(axis=1)
    trainable = ok & y.notna()
    t0 = pd.Timestamp(start, tz=dates.dt.tz)
    test_dates = np.sort(dates[(dates >= t0) & ok].unique())
    out = pd.Series(np.nan, index=X.index)
    for i in range(0, len(test_dates), refit):
        block = test_dates[i:i + refit]
        b0 = pd.Timestamp(block[0])
        tr = trainable & (dates < b0 - pd.Timedelta(days=embargo))
        te = ok & dates.isin(block)
        if tr.sum() < 200 or y[tr].nunique() < 2:
            continue
        m = HistGradientBoostingClassifier(**PARAMS).fit(X[tr].to_numpy(), y[tr].to_numpy())
        out[te] = m.predict_proba(X[te].to_numpy())[:, 1]
    return out


def auc(y: pd.Series, p: pd.Series) -> float | None:
    k = y.notna() & p.notna()
    return float(roc_auc_score(y[k], p[k])) if k.sum() > 10 and y[k].nunique() == 2 else None


def threshold_position(p: pd.Series, thr: float = 0.55) -> pd.Series:
    """X1: long 1x when P >= thr, flat otherwise (and flat where no prediction)."""
    return (p >= thr).astype(float).where(p.notna(), 0.0)


def meta_filter(pos: pd.Series, p: pd.Series, thr: float = 0.45, cut: float = 0.5) -> pd.Series:
    """X2: scale the trend position by `cut` where P < thr; unchanged where P is missing."""
    return pos * np.where(p.reindex(pos.index).fillna(1.0) < thr, cut, 1.0)
