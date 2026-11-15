"""Round-38 (PREREG38): Ping-adapted bull-market accelerators on S36_qd_carry_blend.
- Q38_kuramoto_booster: 16-coin Kuramoto breadth synchronization (r >= 0.70) unlocks 1.65x QD cap inside 70% trend sleeve,
  gated by PREREG36 10%->28% soft drawdown taper + DTB3 + 30% C1 carry.
- Q38_bull_leader_blend: 40% Safe-QD + 30% long-only BTC-gated Top-4 inverse-vol Leader Basket + 30% C1 carry + DTB3.
Pure functions; reads nothing from disk."""
from __future__ import annotations

import numpy as np
import pandas as pd

import strategies29 as S29
import strategies36 as S36

SYNC_THRESH = 0.70
BOOST_LEV = 1.65
BASE_LEV = 1.00
MIN_HIST = S29.MIN_HIST  # 60
FORM_D = 21
TOP_K = 4
SMA_N = 50
REBAL_WEEKDAY = 0  # Monday


def sma_above_matrix(closes: pd.DataFrame, sma_n: int = SMA_N, min_hist: int = MIN_HIST) -> tuple[pd.DataFrame, pd.DataFrame]:
    """Return (above_sma_bool_df, eligible_bool_df) strictly causal at each close."""
    earlier = closes.notna().cumsum() - closes.notna().astype(int)
    sma = closes.rolling(sma_n, min_periods=sma_n).mean()
    eligible = closes.notna() & sma.notna() & (earlier >= min_hist)
    above = eligible & (closes > sma)
    return above, eligible


def kuramoto_breadth(closes: pd.DataFrame, sma_n: int = SMA_N, min_hist: int = MIN_HIST) -> pd.Series:
    """Fraction r_t in [0, 1] of eligible coins strictly above their SMA50 at each close."""
    above, eligible = sma_above_matrix(closes, sma_n=sma_n, min_hist=min_hist)
    n_elig = eligible.sum(axis=1).astype(float)
    n_above = above.sum(axis=1).astype(float)
    r = pd.Series(0.0, index=closes.index, dtype=float)
    ok = n_elig > 0
    r[ok] = n_above[ok] / n_elig[ok]
    return r


def kuramoto_qd_targets(
    qd_1x_b: pd.Series,
    qd_1x_e: pd.Series,
    breadth: pd.Series,
    sync_thresh: float = SYNC_THRESH,
    base_lev: float = BASE_LEV,
    boost_lev: float = BOOST_LEV,
) -> tuple[pd.Series, pd.Series]:
    """Scale 1x QD target positions to boost_lev when breadth >= sync_thresh, else base_lev."""
    br = breadth.reindex(qd_1x_b.index).fillna(0.0)
    cap = pd.Series(np.where(br >= sync_thresh, boost_lev, base_lev), index=qd_1x_b.index, dtype=float)
    return qd_1x_b * cap, qd_1x_e.reindex(qd_1x_b.index).fillna(0.0) * cap


def leader_basket_weights(
    closes: pd.DataFrame,
    sma_n: int = SMA_N,
    min_hist: int = MIN_HIST,
    form_d: int = FORM_D,
    top_k: int = TOP_K,
    rebal_weekday: int = REBAL_WEEKDAY,
) -> pd.DataFrame:
    """Causal weekly Monday selection + daily SMA50/BTC stop-out weights in [0, 1], sum <= 1.0.

    - Macro gate: when BTC close <= BTC SMA50 (or BTC ineligible), target weights are 0.0.
    - On Mondays with BTC > SMA50: consider eligible coins with close > SMA50 and finite vol21 > 0 and ret21 > 0.
      Score = ret21 / vol21. Pick up to top_k coins weighted proportional to 1 / vol21,
      with total basket gross capped at min(top_k, len(selected)) / top_k (unused budget stays in cash/DTB3).
    - Mid-week: any held coin that closes <= its SMA50 (or disappears, or BTC <= SMA50) drops to 0.0 immediately.
    """
    cols = [c for c in S29.COINS if c in closes.columns]
    px = closes[cols]
    above, eligible = sma_above_matrix(px, sma_n=sma_n, min_hist=min_hist)
    ret1 = px.pct_change(fill_method=None)
    ret21 = px / px.shift(form_d) - 1.0
    vol21 = ret1.rolling(form_d, min_periods=form_d).std()

    n = len(px.index)
    w_arr = np.zeros((n, len(cols)), dtype=float)
    btc_idx = cols.index("BTC") if "BTC" in cols else None
    is_monday = px.index.dayofweek == rebal_weekday

    ab_arr = above.to_numpy(dtype=bool)
    el_arr = eligible.to_numpy(dtype=bool)
    r21_arr = ret21.to_numpy(dtype=float)
    v21_arr = vol21.to_numpy(dtype=float)

    cur = np.zeros(len(cols), dtype=float)
    for t in range(n):
        btc_ok = True if btc_idx is None else bool(ab_arr[t, btc_idx])
        if not btc_ok:
            cur = np.zeros(len(cols), dtype=float)
        elif bool(is_monday[t]):
            cands = []
            for j in range(len(cols)):
                if el_arr[t, j] and ab_arr[t, j]:
                    rv, vv = r21_arr[t, j], v21_arr[t, j]
                    if np.isfinite(rv) and np.isfinite(vv) and vv > 1e-8 and rv > 0.0:
                        cands.append((rv / vv, -j, j, 1.0 / vv))
            cur = np.zeros(len(cols), dtype=float)
            if cands:
                cands.sort(reverse=True)
                picked = cands[:top_k]
                inv_v = np.array([x[3] for x in picked], dtype=float)
                slot_scale = min(len(picked), top_k) / float(top_k)
                weights = (inv_v / inv_v.sum()) * slot_scale
                for k_idx, (_, _, col_j, _) in enumerate(picked):
                    cur[col_j] = weights[k_idx]
        else:
            # Mid-week daily stop-out if a held coin closes <= its SMA50 or missing
            cur = cur.copy()
            for j in range(len(cols)):
                if cur[j] > 0.0 and not ab_arr[t, j]:
                    cur[j] = 0.0
        w_arr[t] = cur

    return pd.DataFrame(w_arr, index=px.index, columns=cols)


def simulate_multi_sleeve_portfolio(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    pos_b_base: pd.Series,
    pos_e_base: pd.Series,
    leader_closes: pd.DataFrame,
    leader_w: pd.DataFrame,
    carry_ret: pd.Series,
    rate_pct: pd.Series,
    trend_share: float,
    leader_share: float,
    carry_share: float,
    taker_bps: float,
    slip_bps: float,
    funding_bps_h: float,
    spot_threshold: float | None = None,
    spot_taker_bps: float = 2.0,
    spot_slip_bps: float = 1.0,
    use_taper: bool = True,
    dd_start: float = S36.DD_START,
    dd_full: float = S36.DD_FULL,
    dd_floor: float = S36.DD_FLOOR,
) -> tuple[pd.Series, pd.Series, pd.Series]:
    """Causal portfolio simulator supporting QD trend + optional 16-coin leader basket + C1 carry + DTB3.

    When leader_share == 0.0 and spot_threshold is None, matches strategies36.simulate_qd_carry_portfolio cleanly.
    If spot_threshold is set (e.g., 1.0 - carry_share), portfolio crypto notional up to spot_threshold pays
    spot_taker_bps + spot_slip_bps with 0 funding; only excess notional above spot_threshold pays futures fees/funding.
    """
    idx = btc.index
    n = len(idx)
    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    pb = pos_b_base.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    pe = pos_e_base.reindex(idx).fillna(0.0).to_numpy(dtype=float)

    l_cols = list(leader_w.columns)
    l_ret = leader_closes[l_cols].reindex(idx).pct_change(fill_method=None).fillna(0.0).to_numpy(dtype=float) if l_cols else np.zeros((n, 0))
    lw = leader_w.reindex(idx).fillna(0.0).to_numpy(dtype=float) if l_cols else np.zeros((n, 0))

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
    act_b, act_e = 0.0, 0.0
    prev_b, prev_e = 0.0, 0.0
    act_l = np.zeros(len(l_cols), dtype=float)
    prev_l = np.zeros(len(l_cols), dtype=float)
    prev_idle = max(0.0, float(trend_share) + float(leader_share))

    for t in range(n):
        held_b = 0.0 if t == 0 else act_b
        held_e = 0.0 if t == 0 else act_e
        held_l = np.zeros(len(l_cols), dtype=float) if t == 0 else act_l

        # Portfolio-weighted leg holdings & turnover for trend + leader sleeves
        gross_trend = float(trend_share) * 0.5 * (abs(held_b) + abs(held_e))
        gross_leader = float(leader_share) * float(np.abs(held_l).sum())
        tot_crypto = gross_trend + gross_leader

        turn_trend = float(trend_share) * 0.5 * (abs(held_b - prev_b) + abs(held_e - prev_e))
        turn_leader = float(leader_share) * float(np.abs(held_l - prev_l).sum())
        tot_turn = turn_trend + turn_leader

        if spot_threshold is None:
            tot_cost = tot_turn * fut_cost + tot_crypto * fut_fund
        else:
            prev_tot_crypto = float(trend_share) * 0.5 * (abs(prev_b) + abs(prev_e)) + float(leader_share) * float(np.abs(prev_l).sum())
            fut_notional = max(0.0, tot_crypto - spot_threshold)
            spot_turn_share = 1.0 if max(tot_crypto, prev_tot_crypto) <= spot_threshold else (
                min(1.0, spot_threshold / max(1e-12, max(tot_crypto, prev_tot_crypto)))
            )
            tot_cost = tot_turn * (spot_turn_share * sp_cost + (1.0 - spot_turn_share) * fut_cost) + fut_notional * fut_fund

        trend_pnl = float(trend_share) * 0.5 * (held_b * rb[t] + held_e * re[t])
        leader_pnl = float(leader_share) * float(np.dot(held_l, l_ret[t])) if len(l_cols) else 0.0
        bills_ret = 0.0 if t == 0 else prev_idle * rf[t - 1]
        total_r = trend_pnl + leader_pnl - tot_cost + float(carry_share) * cr[t] + bills_ret
        out_ret[t] = total_r

        eq = max(1e-12, eq * (1.0 + total_r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        mult = S36.soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor) if use_taper else 1.0
        mult_arr[t] = mult

        prev_b, prev_e = held_b, held_e
        prev_l = held_l.copy()

        act_b = pb[t] * mult
        act_e = pe[t] * mult
        act_l = lw[t] * mult
        active_notional = float(trend_share) * 0.5 * (act_b + act_e) + float(leader_share) * float(act_l.sum())
        eff_lev[t] = active_notional
        prev_idle = max(0.0, (float(trend_share) + float(leader_share)) - active_notional)

    return (
        pd.Series(out_ret, index=idx),
        pd.Series(mult_arr, index=idx),
        pd.Series(eff_lev, index=idx),
    )
