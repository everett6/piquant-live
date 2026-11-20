"""Round-37 (PREREG37): Quantum-inspired L2 order-book barrier tunneling (QT_barrier) and 16-coin Ising spin
magnetization (QM_ising) pre-fill defenses evaluated on strategies17.Book passive maker queue simulation.
Pure functions; reads nothing from disk."""
from __future__ import annotations

import math

import numpy as np
import pandas as pd

import strategies17 as S17

ZERO_MAKER = S17.Costs("zero_maker", 0.0, 2.0, True)
TUNNEL_THRESH = math.exp(-2.0)  # exp(-2 * V / K) > exp(-2) <=> K > V
ISING_MAG_THRESH = -0.50


def sell_flow5s_vol(book_t: np.ndarray, tr_t: np.ndarray, tr_s: np.ndarray, tr_buy: np.ndarray) -> np.ndarray:
    """Aggressive sell volume over (t - 5000 ms, t] at each snapshot timestamp book_t."""
    sell_sz = np.where(tr_buy, 0.0, tr_s)
    csum = np.concatenate([[0.0], np.cumsum(sell_sz, dtype=float)])
    hi = np.searchsorted(tr_t, book_t, side="right")
    lo = np.searchsorted(tr_t, book_t - 5000, side="right")
    return csum[hi] - csum[lo]


def barrier_tunnel_prob(bid_top3: np.ndarray, sell_vol5s: np.ndarray) -> np.ndarray:
    """T = exp(-2 * bid_top3 / sell_vol5s) when sell_vol5s > 0, else 0.0."""
    out = np.zeros(len(bid_top3), dtype=float)
    pos = sell_vol5s > 0.0
    ratio = np.divide(bid_top3, sell_vol5s, out=np.zeros_like(bid_top3, dtype=float), where=pos)
    out[pos] = np.exp(-2.0 * np.clip(ratio[pos], 0.0, 50.0))
    return out


def barrier_defense_hit(bid_top3: np.ndarray, book_t: np.ndarray, tr_t: np.ndarray, tr_s: np.ndarray,
                        tr_buy: np.ndarray, thresh: float = TUNNEL_THRESH) -> np.ndarray:
    """True when sell pressure tunnels through top-3 resting bid wall (T > exp(-2))."""
    k_sell = sell_flow5s_vol(book_t, tr_t, tr_s, tr_buy)
    t_prob = barrier_defense_prob = barrier_tunnel_prob(bid_top3, k_sell)
    _ = barrier_defense_prob
    return t_prob > thresh


def ising_magnetization(target_t: np.ndarray, per_coin_spins: list[tuple[np.ndarray, np.ndarray]],
                        max_stale_ms: int = 5000) -> np.ndarray:
    """Mean spin M(t) in [-1, +1] across synchronized coins at target_t timestamps.

    Each entry in per_coin_spins is (book_t_ms, spin_+1_or_-1), sorted by book_t_ms.
    Only snapshots with 0 <= target_t - book_t <= max_stale_ms are included (strictly causal).
    """
    n_t = len(target_t)
    acc = np.zeros(n_t, dtype=float)
    cnt = np.zeros(n_t, dtype=float)
    for ct, sp in per_coin_spins:
        if len(ct) == 0:
            continue
        idx = np.searchsorted(ct, target_t, side="right") - 1
        ok = (idx >= 0) & ((target_t - ct[np.maximum(idx, 0)]) <= max_stale_ms)
        acc[ok] += sp[idx[ok]]
        cnt[ok] += 1.0
    return np.divide(acc, cnt, out=np.zeros(n_t, dtype=float), where=cnt > 0.0)


def simulate_with_defense(
    coin: str,
    trades: pd.DataFrame,
    books: pd.DataFrame,
    costs: S17.Costs,
    defense_arr: np.ndarray | None,
    use_k2_filter: bool = True,
    track_shadows: bool = True,
) -> dict:
    """Fast bit-exact port of strategies17.simulate parameterized by boolean defense_arr[i].

    Skips trade advancement in O(log N) whenever main.pos is None, main.quote is None, and shadows is empty.
    """
    tr_t = trades["t"].to_numpy(np.int64)
    tr_p = trades["p"].to_numpy(float)
    tr_s = trades["s"].to_numpy(float)
    tr_buy = (trades["side"] == "b").to_numpy()

    bt = books["t"].to_numpy(np.int64)
    ob = S17.obi(books["bid_tot"].to_numpy(float), books["ask_tot"].to_numpy(float))
    vpin = S17.vpin_at(bt, tr_t, tr_s, tr_buy, S17.bucket_volumes(tr_t, tr_s)) if use_k2_filter else \
        np.full(len(bt), np.nan)
    bid_px = books["bid_px"].to_numpy(float)
    bid_sz = books["bid_sz"].to_numpy(float)

    k2_ok = (ob > 0.25) & np.isfinite(vpin) & (vpin < 0.50) if use_k2_filter else np.ones(len(bt), dtype=bool)
    def_hit = np.zeros(len(bt), dtype=bool) if defense_arr is None else np.asarray(defense_arr, dtype=bool)

    variant_tag = "K3" if defense_arr is not None else ("K2" if use_k2_filter else "K1")
    main = S17.Book(variant_tag, costs)
    shadows: list[S17.Book] = []
    done_shadows: list[S17.Book] = []
    ti = 0
    n_tr = len(tr_t)

    for i in range(len(bt)):
        t = int(bt[i])
        if main.pos is None and main.quote is None and not shadows:
            while ti < n_tr and tr_t[ti] <= t:
                ti += 1
        else:
            while ti < n_tr and tr_t[ti] <= t:
                tp, tpx, tsz, tb = int(tr_t[ti]), tr_p[ti], tr_s[ti], bool(tr_buy[ti])
                main.on_trade(tp, tpx, tsz, tb, coin)
                for sh in shadows:
                    sh.on_trade(tp, tpx, tsz, tb, coin)
                ti += 1

        if shadows:
            still = []
            for sh in shadows:
                if sh.quote is not None and sh.quote.cancel_at is not None and t >= sh.quote.cancel_at:
                    sh.quote = None
                if sh.pos is None and sh.quote is None:
                    done_shadows.append(sh)
                    continue
                if sh.quote is not None and sh.pos is None and (t >= sh.quote.expire or not bool(k2_ok[i])):
                    sh.cancel(t, "k2")
                still.append(sh)
            shadows = still

        if main.pos is not None:
            continue

        q = main.quote
        if q is not None and q.cancel_at is not None and t >= q.cancel_at:
            main.quote = q = None

        if q is not None:
            if q.cancel_at is not None:
                continue
            if t >= q.expire:
                main.cancel(t, "timeout")
            elif use_k2_filter and not bool(k2_ok[i]):
                main.cancel(t, "condition")
            elif bool(def_hit[i]):
                main.cancel(t, "defense")
                main.defense_cancels += 1
                if track_shadows:
                    shadows.append(S17.Book("K2", costs, quote=S17.Quote(q.px, q.queue, q.live_at, q.expire)))
            continue

        if bid_px[i] <= 0 or np.isnan(bid_px[i]):
            continue
        if bool(k2_ok[i]) and not bool(def_hit[i]):
            main.post(t, bid_px[i], bid_sz[i])

    shadow_trades = [x for sh in done_shadows + shadows for x in sh.trades]
    return {
        "trades": main.trades,
        "quotes": main.quotes,
        "defense_cancels": main.defense_cancels,
        "shadow_trades": shadow_trades,
    }


def post_fill_markouts(trades: list[dict], book_t: np.ndarray, mid: np.ndarray,
                       horizons_s: tuple[int, ...] = (5, 30, 60, 300)) -> dict[str, float]:
    """Mean mid-price move in bps at +H seconds after fill timestamp vs entry price (before fees)."""
    if not trades:
        return {f"m{h}s_bps": float("nan") for h in horizons_s}
    ft = np.array([x["entry_t"] for x in trades], dtype=np.int64)
    ep = np.array([x["net_bps"] * 0 + 1.0 for x in trades], dtype=float)
    _ = ep
    # Lookup entry price from book mid or store markout against fill_mid at fill_t
    j0 = np.clip(np.searchsorted(book_t, ft, side="right") - 1, 0, len(mid) - 1)
    m0 = mid[j0]
    out = {}
    for h in horizons_s:
        jh = np.searchsorted(book_t, ft + h * 1000, side="left")
        ok = jh < len(mid)
        if ok.any():
            vals = (mid[jh[ok]] / m0[ok] - 1.0) * 1e4
            out[f"m{h}s_bps"] = float(vals.mean())
        else:
            out[f"m{h}s_bps"] = float("nan")
    return out
