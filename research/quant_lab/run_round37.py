"""Run PREREG37 once on 50 consecutive frozen days of 16-coin historical_l2 (2026-09-25..2026-11-13):
Discovery = 2026-09-25..2026-10-19 (25 days)
Holdout   = 2026-10-20..2026-11-13 (25 days)
Evaluates K2_base, K3_flow, QT_barrier (order-book tunneling defense), and QM_ising (16-coin spin magnetization).
Writes results/round37.json."""
from __future__ import annotations

import concurrent.futures as cf
import datetime as dt
import gzip
import json
import os
import time

import numpy as np
import pandas as pd

import strategies17 as S17
import strategies29 as S29
import strategies37 as S37

HERE = os.path.dirname(os.path.abspath(__file__))
L2_ROOT = os.path.expanduser("~/piquant-collect/historical_l2")
N_TRIALS = 769
DISC_DAYS = [d.strftime("%Y-%m-%d") for d in pd.date_range("2026-09-25", "2026-10-19", freq="D")]
HOLD_DAYS = [d.strftime("%Y-%m-%d") for d in pd.date_range("2026-10-20", "2026-11-13", freq="D")]
TRADE_SYMS = ("BTCUSDT", "ETHUSDT")
SPIN_SYMS = tuple(f"{c}USDT" for c in S29.COINS)


def _load_book_full(path: str) -> dict[str, np.ndarray]:
    ts, bpx, bsz, b3, btot, atot, mid = [], [], [], [], [], [], []
    with gzip.open(path, "rb") as f:
        for line in f:
            if not line.strip():
                continue
            r = json.loads(line)
            b = r.get("b") or []
            a = r.get("a") or []
            if not b or not a:
                continue
            bp0, bs0 = float(b[0][0]), float(b[0][1])
            ap0 = float(a[0][0])
            if bp0 <= 0 or ap0 <= bp0:
                continue
            ts.append(int(r["t"]))
            bpx.append(bp0)
            bsz.append(bs0)
            b3.append(sum(float(x[1]) for x in b[:3]))
            btot.append(sum(float(x[1]) for x in b))
            atot.append(sum(float(x[1]) for x in a))
            mid.append(0.5 * (bp0 + ap0))
    return {
        "t": np.asarray(ts, dtype=np.int64),
        "bid_px": np.asarray(bpx, dtype=float),
        "bid_sz": np.asarray(bsz, dtype=float),
        "bid_top3": np.asarray(b3, dtype=float),
        "bid_tot": np.asarray(btot, dtype=float),
        "ask_tot": np.asarray(atot, dtype=float),
        "mid": np.asarray(mid, dtype=float),
    }


def _load_book_spin(path: str) -> tuple[np.ndarray, np.ndarray]:
    ts, sp = [], []
    with gzip.open(path, "rb") as f:
        for line in f:
            if not line.strip():
                continue
            r = json.loads(line)
            b = r.get("b") or []
            a = r.get("a") or []
            if not b or not a:
                continue
            bt = sum(float(x[1]) for x in b)
            at = sum(float(x[1]) for x in a)
            ts.append(int(r["t"]))
            sp.append(1.0 if bt >= at else -1.0)
    return np.asarray(ts, dtype=np.int64), np.asarray(sp, dtype=float)


def _load_trades(path: str) -> dict[str, np.ndarray]:
    ts, px, sz, side = [], [], [], []
    with gzip.open(path, "rb") as f:
        for line in f:
            if not line.strip():
                continue
            r = json.loads(line)
            ts.append(int(r["t"]))
            px.append(float(r["p"]))
            sz.append(float(r["s"]))
            side.append(r["side"])
    return {
        "t": np.asarray(ts, dtype=np.int64),
        "p": np.asarray(px, dtype=float),
        "s": np.asarray(sz, dtype=float),
        "side": np.asarray(side),
    }


def _load_day_worker(day: str) -> tuple[str, dict, dict]:
    coins = {}
    for sym in TRADE_SYMS:
        b_path = os.path.join(L2_ROOT, "book", f"bybit-{sym}", f"{day}.jsonl.gz")
        t_path = os.path.join(L2_ROOT, "ticks", f"bybit-{sym}", f"{day}.jsonl.gz")
        bk = _load_book_full(b_path)
        tr = _load_trades(t_path)
        coins[sym] = (bk, tr)
    spins = {}
    for sym in SPIN_SYMS:
        if sym in coins:
            bt = coins[sym][0]["t"]
            sp = np.where(coins[sym][0]["bid_tot"] >= coins[sym][0]["ask_tot"], 1.0, -1.0)
            spins[sym] = (bt, sp)
        else:
            b_path = os.path.join(L2_ROOT, "book", f"bybit-{sym}", f"{day}.jsonl.gz")
            spins[sym] = _load_book_spin(b_path)
    return day, coins, spins


def _concat_split(days: list[str], cache: dict) -> tuple[dict, list[tuple[np.ndarray, np.ndarray]]]:
    merged = {}
    for sym in TRADE_SYMS:
        b_list = [cache[d][0][sym][0] for d in days]
        t_list = [cache[d][0][sym][1] for d in days]
        bk_df = pd.DataFrame({k: np.concatenate([x[k] for x in b_list]) for k in b_list[0]})
        tr_df = pd.DataFrame({k: np.concatenate([x[k] for x in t_list]) for k in t_list[0]})
        bk_df = bk_df.sort_values("t").drop_duplicates("t").reset_index(drop=True)
        tr_df = tr_df.sort_values("t").reset_index(drop=True)
        merged[sym] = (bk_df, tr_df)
    spins = []
    for sym in SPIN_SYMS:
        ct = np.concatenate([cache[d][1][sym][0] for d in days])
        sp = np.concatenate([cache[d][1][sym][1] for d in days])
        order = np.argsort(ct, kind="mergesort")
        spins.append((ct[order], sp[order]))
    return merged, spins


def _eval_split(split_name: str, days: list[str], cache: dict) -> dict:
    merged, spins = _concat_split(days, cache)
    defenses = {sym: {} for sym in TRADE_SYMS}
    for sym in TRADE_SYMS:
        bk_df, tr_df = merged[sym]
        bt = bk_df["t"].to_numpy(np.int64)
        tr_t = tr_df["t"].to_numpy(np.int64)
        tr_s = tr_df["s"].to_numpy(float)
        tr_buy = (tr_df["side"] == "b").to_numpy()
        r5 = S17.ret5s_bps(bt, bk_df["mid"].to_numpy(float))
        f5 = S17.flow5s(bt, tr_t, tr_s, tr_buy)
        mag = S37.ising_magnetization(bt, spins, max_stale_ms=5000)

        defenses[sym]["K2_base"] = None
        defenses[sym]["K3_flow"] = ((~np.isnan(r5)) & (r5 < -3.0)) | (f5 < -0.60)
        defenses[sym]["QT_barrier"] = S37.barrier_defense_hit(
            bk_df["bid_top3"].to_numpy(float), bt, tr_t, tr_s, tr_buy
        )
        defenses[sym]["QM_ising"] = mag <= S37.ISING_MAG_THRESH

    out = {"days": len(days), "variants": {}}
    cost_profiles = (S17.BASE, S37.ZERO_MAKER, S17.STRESS)
    for vname in ("K2_base", "K3_flow", "QT_barrier", "QM_ising"):
        out["variants"][vname] = {}
        for costs in cost_profiles:
            pooled_trades, pooled_shadows = [], []
            quotes_tot, cancels_tot = 0, 0
            per_coin_markout = {}
            for sym in TRADE_SYMS:
                bk_df, tr_df = merged[sym]
                r = S37.simulate_with_defense(sym, tr_df, bk_df, costs, defenses[sym][vname],
                                              use_k2_filter=True, track_shadows=(vname != "K2_base"))
                pooled_trades.extend(r["trades"])
                pooled_shadows.extend(r["shadow_trades"])
                quotes_tot += r["quotes"]
                cancels_tot += r["defense_cancels"]
                if costs is S17.BASE:
                    per_coin_markout[sym] = S37.post_fill_markouts(
                        r["trades"], bk_df["t"].to_numpy(np.int64), bk_df["mid"].to_numpy(float)
                    )
            st = S17.summarize(pooled_trades)
            st["quotes"] = quotes_tot
            st["defense_cancels"] = cancels_tot
            if vname != "K2_base":
                sh = S17.summarize(pooled_shadows)
                st["avoided_shadow"] = sh
            if costs is S17.BASE:
                st["markouts_bps"] = per_coin_markout
            out["variants"][vname][costs.name] = st
    return out


def main():
    t0 = time.time()
    all_days = DISC_DAYS + HOLD_DAYS
    cache = {}
    with cf.ProcessPoolExecutor(max_workers=6) as ex:
        for day, coins, spins in ex.map(_load_day_worker, all_days):
            cache[day] = (coins, spins)
    print(f"[PREREG37] Loaded {len(all_days)} days across 16 coins in {time.time() - t0:.1f}s", flush=True)

    res = {
        "n_trials": N_TRIALS,
        "discovery_range": [DISC_DAYS[0], DISC_DAYS[-1]],
        "holdout_range": [HOLD_DAYS[0], HOLD_DAYS[-1]],
        "discovery": _eval_split("discovery", DISC_DAYS, cache),
        "holdout": _eval_split("holdout", HOLD_DAYS, cache),
        "checks": {},
        "pass": {},
    }

    disc_v = res["discovery"]["variants"]
    hold_v = res["holdout"]["variants"]
    k2_hold_net = hold_v["K2_base"]["base"]["mean_net_bps"]

    for vname in ("QT_barrier", "QM_ising"):
        hb = hold_v[vname]["base"]
        hs = hold_v[vname]["stress"]
        db = disc_v[vname]["base"]
        sh = hb.get("avoided_shadow", {})
        ch = {
            "improves_over_K2_plus_0p5": hb.get("mean_net_bps", -999) > k2_hold_net + 0.5,
            "avoided_worse_than_kept": sh.get("n", 0) > 0 and sh.get("mean_net_bps", 999) < hb.get("mean_net_bps", -999),
            "holdout_n_ge_200": hb.get("n", 0) >= 200,
            "holdout_base_pos": hb.get("mean_net_bps", -999) > 0 and hb.get("t", -999) >= 2.0,
            "discovery_base_pos": db.get("n", 0) >= 200 and db.get("mean_net_bps", -999) > 0,
            "holdout_stress_pos": hs.get("n", 0) > 0 and hs.get("mean_net_bps", -999) > 0,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = bool(all(ch.values()))

    for sp_name in ("discovery", "holdout"):
        print(f"=== {sp_name.upper()} ===", flush=True)
        for vname in ("K2_base", "K3_flow", "QT_barrier", "QM_ising"):
            for cname in ("base", "zero_maker", "stress"):
                st = res[sp_name]["variants"][vname][cname]
                sh = st.get("avoided_shadow", {})
                sh_str = f" | avoided n={sh.get('n', 0)} net={sh.get('mean_net_bps', float('nan')):+.2f}" if sh else ""
                print(
                    f"  {vname:10s} [{cname:10s}] n={st.get('n', 0):5d} net={st.get('mean_net_bps', 0):+.2f}bps "
                    f"gross={st.get('mean_gross_bps', 0):+.2f} t={st.get('t', 0):+.2f} win={st.get('win_rate', 0)*100:4.1f}% "
                    f"cancels={st.get('defense_cancels', 0):5d}{sh_str}",
                    flush=True,
                )
    for vname in ("QT_barrier", "QM_ising"):
        print(f"{vname} PASS={res['pass'][vname]} checks={res['checks'][vname]}", flush=True)

    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round37.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
