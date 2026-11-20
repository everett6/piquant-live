"""Round-44 (PREREG44): Balancing 1.0x and 2.5x leverage with Buy-and-Hold Volatility Harvesting.
Pure functions; zero disk I/O."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

import strategies36 as S36


def vol_balanced_1x_2p5x_positions(
    daily: pd.DataFrame,
    base_pos_1x: pd.Series,
    vol_target_ann: float = 0.75,
    min_lev: float = 1.0,
    max_lev: float = 2.5,
    vol_window: int = 20,
) -> pd.Series:
    """Strictly causal 20-day inverse-volatility leverage balancer between min_lev (1.0x) and max_lev (2.5x).
    When base_pos_1x[t] == 0, output is 0.0."""
    ann_vol = (
        daily["c"]
        .pct_change()
        .fillna(0.0)
        .rolling(vol_window, min_periods=vol_window)
        .std(ddof=1)
        * math.sqrt(365.0)
    )
    lev = (vol_target_ann / ann_vol.clip(lower=1e-6)).clip(lower=min_lev, upper=max_lev).fillna(min_lev)
    base_aligned = base_pos_1x.reindex(daily.index).fillna(0.0)
    return base_aligned * lev


def bh_vol_harvester_positions(
    daily: pd.DataFrame,
    base_lev: float = 1.0,
    min_mult: float = 0.65,
    max_mult: float = 1.35,
    vol_window: int = 20,
) -> pd.Series:
    """Strictly causal Buy-and-Hold Volatility Harvester (Shannon's Demon + counter-volatility dip scaling).
    Scales BH exposure up toward max_mult (1.35x) on sharp 5-day volatility pullbacks and trims toward
    min_mult (0.65x) on overextended 5-day spikes."""
    ret1 = daily["c"].pct_change().fillna(0.0)
    ret5 = daily["c"].pct_change(5).fillna(0.0)
    vol_d = ret1.rolling(vol_window, min_periods=vol_window).std(ddof=1).clip(lower=1e-4).fillna(0.02)
    z = (-ret5 / (vol_d * math.sqrt(5.0))).clip(-3.0, 3.0)
    mult = (1.0 + 0.35 * np.tanh(z)).clip(min_mult, max_mult)
    return pd.Series(base_lev * mult, index=daily.index, dtype=float)


def simulate_bh_vol_balanced_portfolio(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    trend_pos_b: pd.Series,
    trend_pos_e: pd.Series,
    bh_pos_b: pd.Series,
    bh_pos_e: pd.Series,
    carry_ret: pd.Series,
    rate_pct: pd.Series,
    trend_share: float,
    bh_share: float,
    carry_share: float,
    taker_bps: float,
    slip_bps: float,
    funding_bps_h: float,
    spot_threshold: float | None = None,
    spot_taker_bps: float = 2.0,
    spot_slip_bps: float = 1.0,
    use_taper: bool = True,
    dd_start: float = 0.08,
    dd_full: float = 0.28,
    dd_floor: float = 0.35,
) -> tuple[pd.Series, pd.Series, pd.Series]:
    """Strictly causal daily portfolio simulator combining:
    - trend_share allocated to 1.0x..2.5x vol-balanced Safe-QD / Dual-Speed trend (tapered by soft_taper(dd_t)),
    - bh_share allocated to always-on rebalanced Buy-and-Hold Volatility Harvester (untapered to buy bottoms),
    - carry_share allocated to C1 funding carry,
    - DTB3 risk-free interest on uninvested cash max(0, (trend_share + bh_share) - active_crypto_notional).
    At close t, portfolio equity E[t] updates dd[t] and decides t+1 holdings (held over day t+1).
    Returns (net_daily_returns, taper_mult_series, effective_crypto_notional_series).
    """
    idx = btc.index
    n = len(idx)
    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    tb = trend_pos_b.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    te = trend_pos_e.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    bb = bh_pos_b.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    be = bh_pos_e.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    cr = carry_ret.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    rf = rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0

    fut_cost = (taker_bps + slip_bps) * 1e-4
    fut_fund = funding_bps_h * 24.0 * 1e-4
    sp_cost = (spot_taker_bps + spot_slip_bps) * 1e-4

    out_ret = np.zeros(n, dtype=float)
    mult_arr = np.ones(n, dtype=float)
    eff_lev = np.zeros(n, dtype=float)

    eq = 1.0
    peak = 1.0
    act_wb, act_we = 0.0, 0.0
    prev_wb, prev_we = 0.0, 0.0
    prev_idle = max(0.0, float(trend_share) + float(bh_share))

    for t in range(n):
        held_wb = 0.0 if t == 0 else act_wb
        held_we = 0.0 if t == 0 else act_we

        tot_crypto = abs(held_wb) + abs(held_we)
        tot_turn = abs(held_wb - prev_wb) + abs(held_we - prev_we)

        if spot_threshold is None:
            tot_cost = tot_turn * fut_cost + tot_crypto * fut_fund
        else:
            prev_tot_crypto = abs(prev_wb) + abs(prev_we)
            fut_notional = max(0.0, tot_crypto - spot_threshold)
            max_c = max(tot_crypto, prev_tot_crypto)
            spot_turn_share = 1.0 if max_c <= spot_threshold else min(1.0, spot_threshold / max(1e-12, max_c))
            tot_cost = tot_turn * (spot_turn_share * sp_cost + (1.0 - spot_turn_share) * fut_cost) + fut_notional * fut_fund

        crypto_pnl = held_wb * rb[t] + held_we * re[t]
        bills_ret = 0.0 if t == 0 else prev_idle * rf[t - 1]
        total_r = crypto_pnl - tot_cost + float(carry_share) * cr[t] + bills_ret
        out_ret[t] = total_r

        eq = max(1e-12, eq * (1.0 + total_r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        mult = S36.soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor) if use_taper else 1.0
        mult_arr[t] = mult

        prev_wb, prev_we = held_wb, held_we

        act_wb = 0.5 * (float(trend_share) * tb[t] * mult + float(bh_share) * bb[t])
        act_we = 0.5 * (float(trend_share) * te[t] * mult + float(bh_share) * be[t])
        active_notional = abs(act_wb) + abs(act_we)
        eff_lev[t] = active_notional
        prev_idle = max(0.0, (float(trend_share) + float(bh_share)) - active_notional)

    return (
        pd.Series(out_ret, index=idx),
        pd.Series(mult_arr, index=idx),
        pd.Series(eff_lev, index=idx),
    )
