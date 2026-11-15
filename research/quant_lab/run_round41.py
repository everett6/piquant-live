"""Run PREREG41 once on 91 consecutive frozen days of 16-coin historical_l2 (2026-08-15..2026-11-13):
Discovery = 2026-08-15..2026-09-28 (45 days, 270 4h bars)
Holdout   = 2026-09-29..2026-11-13 (46 days, 276 4h bars)
Evaluates:
  1) Q41_l2_4h_flow_rotation
  2) Q41_l2_quantum_walk
  3) Q41_l2_smart_exec
Writes results/round41.json."""
from __future__ import annotations

import concurrent.futures as cf
import datetime as dt
import gzip
import json
import math
import os
import time

import numpy as np
import pandas as pd

import strategies29 as S29
import strategies41 as S41

HERE = os.path.dirname(os.path.abspath(__file__))
L2_ROOT = os.path.expanduser("~/piquant-collect/historical_l2")
N_TRIALS_BEFORE = 775
N_TRIALS_NEW = 3
N_TRIALS_AFTER = N_TRIALS_BEFORE + N_TRIALS_NEW

ALL_DAYS = [d.strftime("%Y-%m-%d") for d in pd.date_range("2026-08-15", "2026-11-13", freq="D")]
DISC_DAYS = ALL_DAYS[:45]  # 2026-08-15 .. 2026-09-28
HOLD_DAYS = ALL_DAYS[45:]  # 2026-09-29 .. 2026-11-13
DISC_SET = set(DISC_DAYS)
HOLD_SET = set(HOLD_DAYS)
SYMS = tuple(f"{c}USDT" for c in S29.COINS)


def _day_start_ms(day_str: str) -> int:
    y, m, d = (int(x) for x in day_str.split("-"))
    return int(dt.datetime(y, m, d, tzinfo=dt.timezone.utc).timestamp() * 1000)


def _process_coin(sym: str) -> dict:
    """Process one coin sequentially over all 91 days cleanly on a worker core."""
    book_dir = os.path.join(L2_ROOT, "book", f"bybit-{sym}")
    tick_dir = os.path.join(L2_ROOT, "ticks", f"bybit-{sym}")

    n_days = len(ALL_DAYS)
    n_bars = n_days * 6
    close_mid = np.full(n_bars, np.nan, dtype=float)
    obi_4h = np.zeros(n_bars, dtype=float)
    spread_4h = np.zeros(n_bars, dtype=float)
    cvd_4h = np.zeros(n_bars, dtype=float)

    exec_records = []
    prev_last_hour_spread_med = -1.0

    for d_idx, day in enumerate(ALL_DAYS):
        t_day0 = _day_start_ms(day)
        t_exec_end = t_day0 + 915_000        # first 15m15s for smart exec
        t_last_hour = t_day0 + 86_400_000 - 3_600_000  # last 1h for tomorrow's trailing spread

        bpath = os.path.join(book_dir, f"{day}.jsonl.gz")
        tpath = os.path.join(tick_dir, f"{day}.jsonl.gz")

        bar_obi_sum = [0.0] * 6
        bar_sp_sum = [0.0] * 6
        bar_cnt = [0] * 6
        bar_last_mid = [np.nan] * 6
        bar_buy_usd = [0.0] * 6
        bar_sell_usd = [0.0] * 6

        win_ts, win_bid, win_ask, win_obi3 = [], [], [], []
        last_hour_spreads = []

        if os.path.exists(bpath):
            with gzip.open(bpath, "rb") as f:
                for line in f:
                    if not line.strip():
                        continue
                    r = json.loads(line)
                    t_ms = int(r["t"])
                    b = r.get("b") or []
                    a = r.get("a") or []
                    if not b or not a:
                        continue
                    bp0 = float(b[0][0])
                    ap0 = float(a[0][0])
                    if bp0 <= 0 or ap0 <= bp0:
                        continue
                    mid = 0.5 * (bp0 + ap0)
                    sp_bps = (ap0 - bp0) / mid * 1e4

                    b5 = sum(float(x[1]) for x in b[:5])
                    a5 = sum(float(x[1]) for x in a[:5])
                    obi5 = S41.compute_obi(b5, a5)

                    slot = int((t_ms - t_day0) // 14_400_000)
                    if 0 <= slot < 6:
                        bar_obi_sum[slot] += obi5
                        bar_sp_sum[slot] += sp_bps
                        bar_cnt[slot] += 1
                        bar_last_mid[slot] = mid

                    if t_ms <= t_exec_end:
                        b3 = sum(float(x[1]) for x in b[:3])
                        a3 = sum(float(x[1]) for x in a[:3])
                        win_ts.append(t_ms)
                        win_bid.append(bp0)
                        win_ask.append(ap0)
                        win_obi3.append(S41.compute_obi(b3, a3))
                    if t_ms >= t_last_hour:
                        last_hour_spreads.append(sp_bps)

        tr_ts, tr_px = [], []
        if os.path.exists(tpath):
            with gzip.open(tpath, "rb") as f:
                for line in f:
                    if not line.strip():
                        continue
                    r = json.loads(line)
                    t_ms = int(r["t"])
                    px = float(r["p"])
                    sz = float(r["s"])
                    side_s = r.get("side", "")
                    usd = px * sz
                    slot = int((t_ms - t_day0) // 14_400_000)
                    if 0 <= slot < 6:
                        if side_s == "b":
                            bar_buy_usd[slot] += usd
                        elif side_s == "s":
                            bar_sell_usd[slot] += usd
                    if t_ms <= t_exec_end:
                        tr_ts.append(t_ms)
                        tr_px.append(px)

        for slot in range(6):
            gb = d_idx * 6 + slot
            close_mid[gb] = bar_last_mid[slot]
            if bar_cnt[slot] > 0:
                obi_4h[gb] = bar_obi_sum[slot] / bar_cnt[slot]
                spread_4h[gb] = bar_sp_sum[slot] / bar_cnt[slot]
            tot_usd = bar_buy_usd[slot] + bar_sell_usd[slot]
            if tot_usd > 0:
                cvd_4h[gb] = (bar_buy_usd[slot] - bar_sell_usd[slot]) / tot_usd

        # Smart execution evaluation on [00:00:00, 00:15:00] using ONLY previous day's trailing 1h median spread
        if d_idx >= 1 and prev_last_hour_spread_med > 0 and win_ts:
            b_ts = np.asarray(win_ts, dtype=np.int64)
            b_bid = np.asarray(win_bid, dtype=float)
            b_ask = np.asarray(win_ask, dtype=float)
            b_obi3 = np.asarray(win_obi3, dtype=float)
            t_ts = np.asarray(tr_ts, dtype=np.int64)
            t_px = np.asarray(tr_px, dtype=float)
            for side in (1, -1):
                res = S41.simulate_smart_exec_window(
                    b_ts,
                    b_bid,
                    b_ask,
                    b_obi3,
                    t_ts,
                    t_px,
                    side=side,
                    t0_ms=t_day0,
                    spread_med_1h_bps=prev_last_hour_spread_med,
                    latency_ms=100,
                )
                if res.get("valid", 0.0) == 1.0:
                    exec_records.append(
                        {
                            "day": day,
                            "sym": sym,
                            "side": side,
                            "base_sf_bps": res["base_sf_bps"],
                            "smart_sf_bps": res["smart_sf_bps"],
                            "bps_saved": res["bps_saved"],
                            "passive_fill_frac": res["passive_fill_frac"],
                        }
                    )

        prev_last_hour_spread_med = float(np.median(last_hour_spreads)) if last_hour_spreads else -1.0

    return {
        "sym": sym,
        "close_mid": close_mid,
        "obi_4h": obi_4h,
        "spread_4h": spread_4h,
        "cvd_4h": cvd_4h,
        "exec_records": exec_records,
    }


def _agg_exec(rows: list[dict]) -> dict[str, float]:
    if not rows:
        return {"n": 0, "base_sf_bps": 0.0, "smart_sf_bps": 0.0, "bps_saved": 0.0, "t_stat": 0.0, "passive_fill_pct": 0.0}
    base = np.asarray([r["base_sf_bps"] for r in rows], dtype=float)
    smart = np.asarray([r["smart_sf_bps"] for r in rows], dtype=float)
    saved = np.asarray([r["bps_saved"] for r in rows], dtype=float)
    fills = np.asarray([r["passive_fill_frac"] for r in rows], dtype=float)
    n = len(saved)
    mu = float(np.mean(saved))
    sd = float(np.std(saved, ddof=1)) if n > 1 else 0.0
    t_stat = (mu / (sd / math.sqrt(n))) if (n > 1 and sd > 1e-12) else 0.0
    return {
        "n": int(n),
        "base_sf_bps": round(float(np.mean(base)), 4),
        "smart_sf_bps": round(float(np.mean(smart)), 4),
        "bps_saved": round(mu, 4),
        "t_stat": round(t_stat, 3),
        "passive_fill_pct": round(float(np.mean(fills)) * 100.0, 2),
    }


def main() -> None:
    t0 = time.time()
    n_coins = len(SYMS)
    n_bars = len(ALL_DAYS) * 6

    close_mat = np.full((n_bars, n_coins), np.nan, dtype=float)
    obi_mat = np.zeros((n_bars, n_coins), dtype=float)
    spread_mat = np.zeros((n_bars, n_coins), dtype=float)
    cvd_mat = np.zeros((n_bars, n_coins), dtype=float)
    all_exec: list[dict] = []

    with cf.ProcessPoolExecutor(max_workers=min(16, os.cpu_count() or 8)) as ex:
        results = list(ex.map(_process_coin, SYMS))

    sym_to_idx = {s: i for i, s in enumerate(SYMS)}
    for r in results:
        idx = sym_to_idx[r["sym"]]
        close_mat[:, idx] = r["close_mid"]
        obi_mat[:, idx] = r["obi_4h"]
        spread_mat[:, idx] = r["spread_4h"]
        cvd_mat[:, idx] = r["cvd_4h"]
        all_exec.extend(r["exec_records"])

    # Causal rolling matrices
    cvd_z_mat = np.zeros_like(cvd_mat)
    ema_mat = np.full_like(close_mat, np.nan)
    for i in range(n_coins):
        cvd_z_mat[:, i] = S41.compute_causal_cvd_z(cvd_mat[:, i], window=18, min_std=0.05)
        ema_mat[:, i] = S41.compute_causal_ema(close_mat[:, i], span=18)

    log_ret_mat = np.zeros_like(close_mat)
    for b in range(1, n_bars):
        for i in range(n_coins):
            if close_mat[b, i] > 0 and close_mat[b - 1, i] > 0:
                log_ret_mat[b, i] = math.log(close_mat[b, i] / close_mat[b - 1, i])

    w_flow = np.zeros((n_bars, n_coins), dtype=float)
    w_qwalk = np.zeros((n_bars, n_coins), dtype=float)
    w_bh = np.full((n_bars, n_coins), 1.0 / float(n_coins), dtype=float)

    for b in range(36, n_bars):
        w_flow[b] = S41.select_flow_rotation_weights(
            obi_mat[b], cvd_z_mat[b], close_mat[b], ema_mat[b], spread_mat[b]
        )
        influx = S41.quantum_walk_influx(
            log_ret_mat[b - 35 : b + 1], obi_mat[b], cvd_z_mat[b], tau=0.60
        )
        w_qwalk[b] = S41.select_quantum_walk_weights(
            influx, obi_mat[b], close_mat[b], ema_mat[b], spread_mat[b]
        )

    # Bar split:
    # Discovery decision bars: [36 .. 268] -> earns bar 37..269 (ends 2026-09-28 23:59)
    # Holdout decision bars: [269 .. n_bars-2] -> earns bar 270..545 (2026-09-29 00:00 .. 2026-11-13 23:59)
    splits = {
        "discovery": (36, 270),
        "holdout": (269, n_bars),
    }

    dir_out = {}
    for name, w_mat in (
        ("Q41_l2_4h_flow_rotation", w_flow),
        ("Q41_l2_quantum_walk", w_qwalk),
        ("EW_BuyHold_16coin", w_bh),
    ):
        dir_out[name] = {}
        for sp_name, (sb, eb) in splits.items():
            dir_out[name][sp_name] = {
                "us_spot": S41.simulate_4h_portfolio(close_mat, w_mat, one_way_cost_bps=3.0, funding_bps_per_bar=0.0, start_bar=sb, end_bar=eb),
                "us_fut": S41.simulate_4h_portfolio(close_mat, w_mat, one_way_cost_bps=7.0, funding_bps_per_bar=0.4, start_bar=sb, end_bar=eb),
            }

    # Execution summary
    disc_exec = [x for x in all_exec if x["day"] in DISC_SET]
    hold_exec = [x for x in all_exec if x["day"] in HOLD_SET]
    exec_out = {
        "discovery_all16": _agg_exec(disc_exec),
        "holdout_all16": _agg_exec(hold_exec),
        "discovery_btc_eth": _agg_exec([x for x in disc_exec if x["sym"] in ("BTCUSDT", "ETHUSDT")]),
        "holdout_btc_eth": _agg_exec([x for x in hold_exec if x["sym"] in ("BTCUSDT", "ETHUSDT")]),
        "holdout_buys_all16": _agg_exec([x for x in hold_exec if x["side"] == 1]),
        "holdout_sells_all16": _agg_exec([x for x in hold_exec if x["side"] == -1]),
    }

    # Evaluative pass verdicts against PREREG41 exact rules
    bh_hold_sharpe = dir_out["EW_BuyHold_16coin"]["holdout"]["us_spot"]["sharpe"]
    verdicts = {}
    for name in ("Q41_l2_4h_flow_rotation", "Q41_l2_quantum_walk"):
        ds = dir_out[name]["discovery"]["us_spot"]
        hs = dir_out[name]["holdout"]["us_spot"]
        hf = dir_out[name]["holdout"]["us_fut"]
        checks = {
            "1_holdout_ret_gt_0_and_sharpe_beats_bh": hs["cum_ret_pct"] > 0.0 and hs["sharpe"] > bh_hold_sharpe,
            "2_holdout_sharpe_ge_1_and_dd_ge_neg12": hs["sharpe"] >= 1.0 and hs["max_dd_pct"] >= -12.0,
            "3_disc_sharpe_ge_1_and_ret_gt_0": ds["sharpe"] >= 1.0 and ds["cum_ret_pct"] > 0.0,
            "4_holdout_fut_ret_gt_0": hf["cum_ret_pct"] > 0.0,
        }
        verdicts[name] = {"pass": all(checks.values()), "checks": checks}

    exec_checks = {
        "1_holdout_saved_gt_0p25_and_t_ge_3": exec_out["holdout_all16"]["bps_saved"] > 0.25 and exec_out["holdout_all16"]["t_stat"] >= 3.0,
        "2_disc_saved_gt_0p25_and_t_ge_3": exec_out["discovery_all16"]["bps_saved"] > 0.25 and exec_out["discovery_all16"]["t_stat"] >= 3.0,
        "3_holdout_btc_eth_saved_gt_0p10": exec_out["holdout_btc_eth"]["bps_saved"] > 0.10,
    }
    verdicts["Q41_l2_smart_exec"] = {"pass": all(exec_checks.values()), "checks": exec_checks}

    payload = {
        "prereg": "PREREG41",
        "trials_before": N_TRIALS_BEFORE,
        "trials_new": N_TRIALS_NEW,
        "trials_after": N_TRIALS_AFTER,
        "window": "2026-08-15..2026-11-13 (91 days, 546 4h bars)",
        "discovery": f"{DISC_DAYS[0]}..{DISC_DAYS[-1]} (45d)",
        "holdout": f"{HOLD_DAYS[0]}..{HOLD_DAYS[-1]} (46d)",
        "elapsed_s": round(time.time() - t0, 2),
        "directional_4h": dir_out,
        "smart_exec": exec_out,
        "verdicts": verdicts,
    }

    out_path = os.path.join(HERE, "results", "round41.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as f:
        json.dump(payload, f, indent=2)
    print(json.dumps(payload, indent=2))


if __name__ == "__main__":
    main()
