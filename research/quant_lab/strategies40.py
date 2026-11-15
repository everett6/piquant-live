"""Round-40 (PREREG40): Multi-Clock 6h QD + C1 Carry and Fast-Exit R3 x QD + C1 Carry.
Pure functions; reads nothing from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies30 as S30
import strategies35 as S35
import strategies36 as S36

CLOCKS = S30.CLOCKS
SMA_N = 50
EXIT_SMA = 20
TAU = S35.TAU


def r3_fastexit_qd_positions(
    daily: pd.DataFrame,
    lev: float = 1.0,
    sma_n: int = SMA_N,
    exit_sma: int = EXIT_SMA,
    tau: float = TAU,
) -> np.ndarray:
    """R3 fast-exit stateful trend scaled by QD coherence age ramp 1 - exp(-age / tau).

    - Enter when not active and close > SMA(sma_n) and close >= SMA(exit_sma).
    - Stay active while close > SMA(sma_n) and close >= SMA(exit_sma).
    - Exit to 0.0 immediately on close < SMA(exit_sma) or close <= SMA(sma_n).
    Matches internal/trend/trend.go R3 entry/exit state machine strictly causally at close t.
    """
    c = daily["c"]
    sma_long = c.rolling(sma_n, min_periods=sma_n).mean().to_numpy(dtype=float)
    sma_short = c.rolling(exit_sma, min_periods=exit_sma).mean().to_numpy(dtype=float)
    px = c.to_numpy(dtype=float)

    out = np.zeros(len(px), dtype=float)
    active = False
    age = 0
    for i in range(len(px)):
        sl, ss = sma_long[i], sma_short[i]
        if not np.isfinite(sl) or not np.isfinite(ss):
            active = False
            age = 0
            out[i] = 0.0
            continue
        if not active:
            if px[i] > sl and px[i] >= ss:
                active = True
                age = 1
        else:
            if px[i] < ss or px[i] <= sl:
                active = False
                age = 0
            else:
                age += 1
        out[i] = lev * (1.0 - np.exp(-age / tau)) if active else 0.0
    return out


def clock_qd_held(b6: pd.DataFrame, hour: int, lev: float = 1.0, n: int = SMA_N, tau: float = TAU) -> pd.Series:
    """Position held over each 6h bar for clock `hour` using QD coherence on that clock's 24h closes."""
    d = S30.clock_daily(b6, hour)
    vals = S35.coherence_positions(d, lev=lev, n=n, tau=tau)
    pos = pd.Series(vals, index=d.index + pd.Timedelta(days=1))
    return pos.reindex(b6.index).ffill(limit=3).fillna(0.0)


def split_qd_held(b6: pd.DataFrame, clocks=CLOCKS, lev: float = 1.0, n: int = SMA_N, tau: float = TAU) -> pd.Series:
    """Equal split of QD across all 4 daily clocks on 6h grid (position held over bar t)."""
    return sum(clock_qd_held(b6, h, lev=lev, n=n, tau=tau) for h in clocks) / len(clocks)


def simulate_6h_qd_carry(
    b6_btc: pd.DataFrame,
    b6_eth: pd.DataFrame,
    held_b: pd.Series,
    held_e: pd.Series,
    carry_daily_ret: pd.Series,
    rate_pct: pd.Series,
    trend_share: float,
    carry_share: float,
    taker_bps: float,
    slip_bps: float,
    funding_bps_h: float,
    use_taper: bool = True,
) -> pd.Series:
    """Simulate 6h multi-clock portfolio step, aggregated back to daily UTC returns for clean regime comparison.

    carry_daily_ret is settled once per UTC day on the last 6h bar of each day (18:00 UTC) so daily variance
    is preserved faithfully. Signal held_b[t], held_e[t] is the desired target held OVER bar t before taper;
    realized taper multiplier m from end of bar t-1 multiplies the target held over bar t.
    """
    idx = b6_btc.index
    n = len(idx)
    rb = b6_btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = b6_eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    hb_raw = held_b.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    he_raw = held_e.reindex(idx).fillna(0.0).to_numpy(dtype=float)

    rf_day = rate_pct.resample("1D").last().ffill().reindex(idx.normalize(), method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0 / 4.0

    # Align daily carry return strictly once per UTC date on that date's final 6h bar present in idx
    c_map = carry_daily_ret.to_dict()
    bar_dates = idx.normalize()
    carry_6h = np.zeros(n, dtype=float)
    for t in range(n):
        is_last_of_day = (t == n - 1) or (bar_dates[t + 1] != bar_dates[t])
        if is_last_of_day:
            carry_6h[t] = float(c_map.get(bar_dates[t], 0.0))

    cost_rate = (taker_bps + slip_bps) * 1e-4
    fund_rate_6h = funding_bps_h * S30.BAR_H * 1e-4

    bar_ret = np.zeros(n, dtype=float)
    eq = 1.0
    peak = 1.0
    mult = 1.0
    prev_b, prev_e = 0.0, 0.0

    for t in range(n):
        cur_b = hb_raw[t] * mult
        cur_e = he_raw[t] * mult
        turn = abs(cur_b - prev_b) + abs(cur_e - prev_e)
        gross_trend = 0.5 * (abs(cur_b) + abs(cur_e))
        trend_net = float(trend_share) * (
            0.5 * (cur_b * rb[t] + cur_e * re[t]) - 0.5 * turn * cost_rate - gross_trend * fund_rate_6h
        )
        idle_frac = max(0.0, float(trend_share) * (1.0 - 0.5 * (cur_b + cur_e)))
        r = trend_net + float(carry_share) * carry_6h[t] + idle_frac * rf_day[t]
        bar_ret[t] = r

        eq = max(1e-12, eq * (1.0 + r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        mult = S36.soft_taper(dd) if use_taper else 1.0
        prev_b, prev_e = cur_b, cur_e

    s = pd.Series(bar_ret, index=idx)
    return (1.0 + s).groupby(s.index.normalize()).prod() - 1.0
