"""Pre-registered PREREG41 strategies (pure functions, zero disk I/O):
1) Q41_l2_4h_flow_rotation: 4h causal L2 OBI + taker CVD rotation across 16 coins
2) Q41_l2_quantum_walk: continuous-time quantum walk exp(-i tau H) flow interference
3) Q41_l2_smart_exec: causal 15-minute (4x slice) post-only L2 execution timing vs 00:00 MOC cross
"""
from __future__ import annotations

import math
from typing import Sequence

import numpy as np
from scipy.linalg import expm


def compute_obi(bid_qty: float, ask_qty: float) -> float:
    tot = bid_qty + ask_qty
    if tot <= 0:
        return 0.0
    return float((bid_qty - ask_qty) / tot)


def compute_causal_cvd_z(cvd_ratios: np.ndarray, window: int = 18, min_std: float = 0.05) -> np.ndarray:
    """Strict causal z-score over trailing `window` bars (inclusive of current bar b)."""
    n = len(cvd_ratios)
    out = np.zeros(n, dtype=float)
    for b in range(window - 1, n):
        w = cvd_ratios[b - window + 1 : b + 1]
        mu = float(np.mean(w))
        sd = float(np.std(w, ddof=1)) if len(w) > 1 else min_std
        sd = max(sd, min_std)
        out[b] = (float(cvd_ratios[b]) - mu) / sd
    return out


def compute_causal_ema(series: np.ndarray, span: int = 18) -> np.ndarray:
    """Strict causal EMA."""
    n = len(series)
    out = np.full(n, np.nan, dtype=float)
    alpha = 2.0 / (span + 1.0)
    prev = np.nan
    for i in range(n):
        v = float(series[i])
        if math.isnan(v) or v <= 0:
            out[i] = prev
            continue
        if math.isnan(prev):
            prev = v
        else:
            prev = alpha * v + (1.0 - alpha) * prev
        out[i] = prev
    return out


def quantum_walk_influx(
    log_rets_window: np.ndarray,
    obi_vec: np.ndarray,
    cvd_z_vec: np.ndarray,
    tau: float = 0.60,
) -> np.ndarray:
    """Single-step continuous-time quantum walk across N coins.

    Parameters
    ----------
    log_rets_window : shape (T, N), causal prior 4h log returns (e.g. T=36)
    obi_vec         : shape (N,), causal mean top-5 L2 imbalance in [-1, 1]
    cvd_z_vec       : shape (N,), causal signed taker flow z-score
    tau             : evolution time for unitary propagator U = expm(-1j * tau * H)

    Returns
    -------
    influx : shape (N,), constructive wave influx:
             (|psi_tau|^2 - |psi_0|^2) + Re(conj(psi_0) * psi_tau)
    """
    n_coins = obi_vec.shape[0]
    if log_rets_window.shape[0] < 6 or log_rets_window.shape[1] != n_coins:
        return np.zeros(n_coins, dtype=float)

    # Zero-variance safeguard in synthetic or quiet tests
    stds = np.std(log_rets_window, axis=0)
    valid = stds > 1e-12
    adj = np.zeros((n_coins, n_coins), dtype=float)
    if np.sum(valid) >= 2:
        sub = log_rets_window[:, valid]
        corr = np.corrcoef(sub, rowvar=False)
        corr = np.nan_to_num(corr, nan=0.0, posinf=0.0, neginf=0.0)
        corr = np.maximum(0.0, corr)
        np.fill_diagonal(corr, 0.0)
        idx = np.where(valid)[0]
        for a_i, gi in enumerate(idx):
            for a_j, gj in enumerate(idx):
                adj[gi, gj] = corr[a_i, a_j]

    deg = np.sum(adj, axis=1)
    d_inv_sqrt = np.where(deg > 1e-12, 1.0 / np.sqrt(np.maximum(deg, 1e-12)), 0.0)
    norm_adj = (d_inv_sqrt[:, None] * adj) * d_inv_sqrt[None, :]
    lap_norm = np.eye(n_coins, dtype=float) - norm_adj

    potential = -np.clip(obi_vec, -1.0, 1.0)
    ham = lap_norm + np.diag(potential)

    psi_buy_0 = np.maximum(np.clip(cvd_z_vec, -3.0, 3.0), 0.0).astype(complex)
    psi_sell_0 = np.maximum(-np.clip(cvd_z_vec, -3.0, 3.0), 0.0).astype(complex)
    if float(np.linalg.norm(psi_buy_0)) <= 1e-12 and float(np.linalg.norm(psi_sell_0)) <= 1e-12:
        return np.zeros(n_coins, dtype=float)

    unitary = expm(-1j * tau * ham)
    psi_buy_tau = unitary @ psi_buy_0
    psi_sell_tau = unitary @ psi_sell_0
    return np.asarray(np.abs(psi_buy_tau) - np.abs(psi_sell_tau), dtype=float)


def select_flow_rotation_weights(
    obi_vec: np.ndarray,
    cvd_z_vec: np.ndarray,
    close_vec: np.ndarray,
    ema_vec: np.ndarray,
    spread_bps_vec: np.ndarray,
    k_max: int = 3,
    obi_min: float = 0.08,
    cvd_min: float = 0.75,
    max_spread_bps: float = 6.0,
) -> np.ndarray:
    """PREREG41 Trial 1 weight selection at a single 4h close (strictly causal)."""
    n = len(obi_vec)
    w = np.zeros(n, dtype=float)
    cands = []
    for i in range(n):
        if math.isnan(close_vec[i]) or math.isnan(ema_vec[i]) or close_vec[i] <= 0:
            continue
        if (
            obi_vec[i] > obi_min
            and cvd_z_vec[i] > cvd_min
            and close_vec[i] > ema_vec[i]
            and spread_bps_vec[i] <= max_spread_bps
        ):
            cands.append((float(cvd_z_vec[i]), i))
    cands.sort(reverse=True)
    for _, idx in cands[:k_max]:
        w[idx] = 1.0 / float(k_max)
    return w


def select_quantum_walk_weights(
    influx_vec: np.ndarray,
    obi_vec: np.ndarray,
    close_vec: np.ndarray,
    ema_vec: np.ndarray,
    spread_bps_vec: np.ndarray,
    k_max: int = 3,
    influx_min: float = 0.50,
    max_spread_bps: float = 6.0,
) -> np.ndarray:
    """PREREG41 Trial 2 weight selection using continuous-time quantum walk influx."""
    n = len(influx_vec)
    w = np.zeros(n, dtype=float)
    cands = []
    for i in range(n):
        if math.isnan(close_vec[i]) or math.isnan(ema_vec[i]) or close_vec[i] <= 0:
            continue
        if (
            influx_vec[i] > influx_min
            and obi_vec[i] > 0.0
            and close_vec[i] > ema_vec[i]
            and spread_bps_vec[i] <= max_spread_bps
        ):
            cands.append((float(influx_vec[i]), i))
    cands.sort(reverse=True)
    for _, idx in cands[:k_max]:
        w[idx] = 1.0 / float(k_max)
    return w


def simulate_4h_portfolio(
    close_mat: np.ndarray,
    weight_mat: np.ndarray,
    one_way_cost_bps: float,
    funding_bps_per_bar: float,
    start_bar: int,
    end_bar: int,
) -> dict[str, float]:
    """Simulate 4h weights `weight_mat[b]` decided at close `b` and earned over `b -> b+1`.

    `end_bar` is exclusive upper bound on asset return bars (`b+1 < end_bar` up to `end_bar - 1`).
    If `b` runs from `start_bar` to `end_bar - 2`, returns are earned across `b+1` in `[start_bar+1, end_bar-1]`.
    """
    prev_w = np.zeros(close_mat.shape[1], dtype=float)
    eq = 1.0
    peak = 1.0
    max_dd = 0.0
    rets = []
    n_active_bars = 0
    turnover_sum = 0.0

    for b in range(start_bar, end_bar - 1):
        w = weight_mat[b]
        turnover = float(np.sum(np.abs(w - prev_w)))
        turnover_sum += turnover
        gross = 0.0
        for i in range(close_mat.shape[1]):
            if w[i] > 0 and close_mat[b, i] > 0 and close_mat[b + 1, i] > 0:
                gross += float(w[i]) * (float(close_mat[b + 1, i]) / float(close_mat[b, i]) - 1.0)
        gross_exposure = float(np.sum(w))
        if gross_exposure > 0:
            n_active_bars += 1
        cost = turnover * (one_way_cost_bps / 1e4) + gross_exposure * (funding_bps_per_bar / 1e4)
        net = gross - cost
        rets.append(net)
        eq *= 1.0 + net
        if eq > peak:
            peak = eq
        dd = eq / peak - 1.0
        if dd < max_dd:
            max_dd = dd
        prev_w = w

    arr = np.asarray(rets, dtype=float)
    bars_per_year = 6.0 * 365.0
    sharpe = 0.0
    if len(arr) >= 2:
        sd = float(np.std(arr, ddof=1))
        if sd > 1e-12:
            sharpe = float(np.mean(arr) / sd * math.sqrt(bars_per_year))
    return {
        "bars": int(len(arr)),
        "active_bars": int(n_active_bars),
        "cum_ret_pct": round((eq - 1.0) * 100.0, 4),
        "max_dd_pct": round(max_dd * 100.0, 4),
        "sharpe": round(sharpe, 4),
        "total_turnover": round(turnover_sum, 4),
    }


def simulate_smart_exec_window(
    book_ts_ms: np.ndarray,
    bid_px: np.ndarray,
    ask_px: np.ndarray,
    obi3: np.ndarray,
    trade_ts_ms: np.ndarray,
    trade_px: np.ndarray,
    side: int,
    t0_ms: int,
    spread_med_1h_bps: float,
    latency_ms: int = 100,
    n_slices: int = 4,
    slice_ms: int = 225_000,
) -> dict[str, float]:
    """Compare immediate 00:00 MOC cross vs 4x ping-safe L2 patient post-only slices.

    `side = +1` for BUY, `-1` for SELL.
    Returns baseline_shortfall_bps, smart_shortfall_bps, bps_saved, and passive_fill_frac.
    Shortfall is always measured relative to benchmark arrival `mid(t0)` in bps (lower is better!).
    """
    if len(book_ts_ms) == 0 or side not in (1, -1):
        return {"valid": 0.0}

    i0 = int(np.searchsorted(book_ts_ms, t0_ms))
    if i0 >= len(book_ts_ms) or book_ts_ms[i0] > t0_ms + 15_000:
        return {"valid": 0.0}

    mid0 = 0.5 * (float(bid_px[i0]) + float(ask_px[i0]))
    if mid0 <= 0:
        return {"valid": 0.0}

    if side == 1:
        base_px = float(ask_px[i0])
        base_sf = (base_px - mid0) / mid0 * 1e4
    else:
        base_px = float(bid_px[i0])
        base_sf = (mid0 - base_px) / mid0 * 1e4

    slice_sfs = []
    passive_fills = 0
    thresh_spread = 0.85 * spread_med_1h_bps if spread_med_1h_bps > 0 else 1e9

    for s in range(n_slices):
        ws = t0_ms + s * slice_ms
        we = ws + slice_ms
        bi_start = int(np.searchsorted(book_ts_ms, ws))
        bi_end = int(np.searchsorted(book_ts_ms, we, side="right"))
        if bi_start >= bi_end:
            # No snapshots in slice -> fallback to latest prior known quote
            fallback_idx = max(0, bi_start - 1)
            f_px = float(ask_px[fallback_idx] if side == 1 else bid_px[fallback_idx])
            sf = ((f_px - mid0) / mid0 * 1e4) if side == 1 else ((mid0 - f_px) / mid0 * 1e4)
            slice_sfs.append(sf)
            continue

        # Look for first causal 1s state inside the slice meeting spread compression OR favorable OBI3
        posted = False
        post_t = 0
        post_px = 0.0
        for bi in range(bi_start, bi_end):
            bp = float(bid_px[bi])
            ap = float(ask_px[bi])
            m = 0.5 * (bp + ap)
            if m <= 0 or ap <= bp:
                continue
            sp_bps = (ap - bp) / m * 1e4
            fav_obi = float(obi3[bi]) >= 0.10 if side == 1 else float(obi3[bi]) <= -0.10
            if sp_bps <= thresh_spread or fav_obi:
                posted = True
                post_t = int(book_ts_ms[bi]) + latency_ms
                post_px = bp if side == 1 else ap
                break

        filled_passive = False
        if posted and post_t < we and len(trade_ts_ms) > 0:
            ti0 = int(np.searchsorted(trade_ts_ms, post_t))
            ti1 = int(np.searchsorted(trade_ts_ms, we, side="right"))
            if ti0 < ti1:
                win_px = trade_px[ti0:ti1]
                if (side == 1 and float(np.min(win_px)) <= post_px) or (
                    side == -1 and float(np.max(win_px)) >= post_px
                ):
                    filled_passive = True

        if filled_passive:
            passive_fills += 1
            sf = ((post_px - mid0) / mid0 * 1e4) if side == 1 else ((mid0 - post_px) / mid0 * 1e4)
        else:
            # Market cross at slice deadline
            cross_idx = bi_end - 1
            c_px = float(ask_px[cross_idx] if side == 1 else bid_px[cross_idx])
            sf = ((c_px - mid0) / mid0 * 1e4) if side == 1 else ((mid0 - c_px) / mid0 * 1e4)
        slice_sfs.append(sf)

    smart_sf = float(np.mean(slice_sfs))
    return {
        "valid": 1.0,
        "base_sf_bps": base_sf,
        "smart_sf_bps": smart_sf,
        "bps_saved": base_sf - smart_sf,
        "passive_fill_frac": float(passive_fills) / float(n_slices),
    }
