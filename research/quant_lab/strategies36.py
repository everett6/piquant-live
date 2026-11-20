"""Round-36 (PREREG36): risk-capped QD coherence trend with non-locking drawdown taper, idle DTB3 T-bills,
and dedicated always-on C1 funding carry sleeve. Pure functions; reads nothing from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies19 as S19
import strategies35 as S35

DD_START = 0.10
DD_FULL = 0.28
DD_FLOOR = 0.40


def soft_taper(dd: float, dd_start: float = DD_START, dd_full: float = DD_FULL,
               floor: float = DD_FLOOR) -> float:
    """Non-locking linear drawdown taper: 1.0 up to dd_start, tapering linearly to floor at dd_full."""
    if dd <= dd_start:
        return 1.0
    if dd >= dd_full:
        return float(floor)
    frac = (dd - dd_start) / (dd_full - dd_start)
    return float(1.0 - (1.0 - floor) * frac)


def simulate_qd_carry_portfolio(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    pos_b_base: pd.Series,
    pos_e_base: pd.Series,
    carry_ret: pd.Series,
    rate_pct: pd.Series,
    trend_share: float,
    carry_share: float,
    taker_bps: float,
    slip_bps: float,
    funding_bps_h: float,
    use_taper: bool = True,
    dd_start: float = DD_START,
    dd_full: float = DD_FULL,
    dd_floor: float = DD_FLOOR,
) -> tuple[pd.Series, pd.Series, pd.Series]:
    """Causal close-to-close portfolio simulation on btc.index.

    pos_b_base / pos_e_base: target sleeve leverages in [0, lev] decided at close t (warmed up beforehand).
    Over day t:
      - held_b, held_e from close t-1 earn asset return minus turnover & funding, scaled by 0.5 * trend_share.
      - idle_cash from close t-1 = max(0, trend_share * (1 - 0.5 * (held_b + held_e))) earns T-bill rate[t-1].
      - carry_share earns carry_ret[t] (already net of carry costs/funding).
    At close t, portfolio equity E[t] updates dd[t] and taper multiplier m[t] -> decides t+1 trend holdings.
    Returns (net_daily_returns, taper_mult_series, effective_trend_leverage_series).
    """
    idx = btc.index
    n = len(idx)
    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    pb = pos_b_base.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    pe = pos_e_base.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    cr = carry_ret.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    rf = rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0

    cost_rate = (taker_bps + slip_bps) * 1e-4
    fund_rate = funding_bps_h * 24.0 * 1e-4

    out_ret = np.zeros(n, dtype=float)
    mult_arr = np.ones(n, dtype=float)
    eff_lev = np.zeros(n, dtype=float)

    eq = 1.0
    peak = 1.0
    act_b, act_e = 0.0, 0.0
    prev_b, prev_e = 0.0, 0.0
    prev_idle = max(0.0, float(trend_share))

    for t in range(n):
        held_b = 0.0 if t == 0 else act_b
        held_e = 0.0 if t == 0 else act_e

        turn = abs(held_b - prev_b) + abs(held_e - prev_e)
        gross_trend = 0.5 * (abs(held_b) + abs(held_e))
        trend_gross_pnl = 0.5 * (held_b * rb[t] + held_e * re[t])
        trend_net = float(trend_share) * (
            trend_gross_pnl - 0.5 * turn * cost_rate - gross_trend * fund_rate
        )
        bills_ret = 0.0 if t == 0 else prev_idle * rf[t - 1]
        total_r = trend_net + float(carry_share) * cr[t] + bills_ret
        out_ret[t] = total_r

        eq = max(1e-12, eq * (1.0 + total_r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        mult = soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor) if use_taper else 1.0
        mult_arr[t] = mult

        act_b = pb[t] * mult
        act_e = pe[t] * mult
        mean_pos = 0.5 * (act_b + act_e)
        eff_lev[t] = float(trend_share) * mean_pos
        prev_idle = max(0.0, float(trend_share) * (1.0 - mean_pos))
        prev_b, prev_e = held_b, held_e

    return (
        pd.Series(out_ret, index=idx),
        pd.Series(mult_arr, index=idx),
        pd.Series(eff_lev, index=idx),
    )
