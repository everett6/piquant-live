"""Round-3 algorithms from PREREG3.md: T0 random-entry bracket grid (theory check), S7 live-signal entries with
wider brackets, S8 trend-regime entries with wide brackets. Causal features only."""
from __future__ import annotations

import numpy as np
import pandas as pd

import lab
import strategies as S

T0_TP = (20, 35, 60, 100, 150, 250)
T0_SL = (20, 30, 50, 100)
S7_GRID = [dict(th=th, tp=tp, sl=sl) for th in (0.65, 0.75) for tp in (60, 100, 150, 250) for sl in (20, 30, 50)]
S8_GRID = [dict(fast=fa, slow=sw, tp=tp, sl=sl)
           for fa in (12, 24) for sw in (168, 336) for tp in (150, 300, 600) for sl in (50, 100, 200)]
S7_MAX_HOLD = 1440      # 1m bars = 1 day
S8_MAX_HOLD = 14 * 288  # 5m bars = 14 days


def brackets(df: pd.DataFrame, signal: np.ndarray, tp: float, sl: float, max_hold: int, costs: lab.Costs):
    n = len(df)
    return lab.simulate_brackets(df["o"].to_numpy(), df["h"].to_numpy(), df["l"].to_numpy(), df["c"].to_numpy(),
                                 df.index.as_unit("ns").asi8, signal, np.full(n, float(tp)), np.full(n, float(sl)),
                                 max_hold=max_hold, loser_stop=max_hold, costs=costs)


# ---------------------------------------------------------------------------------------------------------
# T0: random side entered whenever flat
# ---------------------------------------------------------------------------------------------------------
def t0_signal(n: int, seed: int = 3) -> np.ndarray:
    return np.random.default_rng(seed).choice(np.array([-1, 1]), size=n)


def t0_cell(tr: pd.DataFrame, tp: float, sl: float, cost_bps: float) -> dict:
    two = tr[tr["reason"].isin(["target", "stop"])]
    win = float((two["reason"] == "target").mean()) if len(two) else float("nan")
    pred = sl / (tp + sl)
    p_star = (sl + cost_bps) / (tp + sl)
    return {"tp": tp, "sl": sl, "trades": int(len(tr)), "maxhold_frac": float((tr["reason"] == "max_hold").mean()) if len(tr) else 0.0,
            "win_rate": win, "pred_win_rate": pred, "win_minus_pred": win - pred,
            "mean_gross_bps": float(tr["gross"].mean() * 1e4) if len(tr) else float("nan"),
            "mean_net_bps": float(tr["net"].mean() * 1e4) if len(tr) else float("nan"),
            "net_breakeven_p": p_star, "win_minus_breakeven": win - p_star}


def t0_supported(cells: list[dict]) -> bool:
    ok = [abs(c["win_minus_pred"]) <= 0.03 and abs(c["mean_gross_bps"]) <= 3.0 for c in cells]
    return bool(np.mean(ok) >= 0.90)


# ---------------------------------------------------------------------------------------------------------
# S7: live-engine momentum score, wide fixed-bps brackets
# ---------------------------------------------------------------------------------------------------------
def s7_signal(conv: np.ndarray, th: float) -> np.ndarray:
    sig = np.where(conv > th, 1, np.where(conv < 1 - th, -1, 0))
    return np.where(np.isfinite(conv), sig, 0)


# ---------------------------------------------------------------------------------------------------------
# S8: 1h EMA trend regime (last completed hour) mapped onto 5m bars, long only
# ---------------------------------------------------------------------------------------------------------
def s8_regime(h1: pd.DataFrame, m5: pd.DataFrame, fast: int, slow: int) -> np.ndarray:
    ef = h1["c"].ewm(span=fast, adjust=False).mean()
    es = h1["c"].ewm(span=slow, adjust=False).mean()
    up = (ef > es).astype(float)
    up.iloc[:slow] = 0.0
    up.index = up.index + pd.Timedelta(hours=1)          # hour H's close is known at H+1h
    bar_close = m5.index + pd.Timedelta(minutes=5)       # 5m bar t's signal is decided at t+5m
    reg = up.reindex(up.index.union(bar_close)).ffill().reindex(bar_close).fillna(0.0)
    return reg.to_numpy().astype(int)


def s1_conv(df: pd.DataFrame) -> np.ndarray:
    return S.s1_features(df)["conv"].to_numpy()
