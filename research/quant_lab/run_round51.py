"""Run PREREG51 once: BTC + ETH + SOL Inverse-Vol-Parity Dual-Speed Trend Rotation (US_SPOT).

Compares:
  1) Q42_dualspeed_60_40_carry_spot (BTC + ETH equal-weight 60/40 carry, US_SPOT)
  2) Q51_btc_eth_sol_volparity_spot (BTC + ETH + SOL inv-20d-vol-weighted 60/40 carry, US_SPOT)

Writes results/round51.json.
"""
from __future__ import annotations

import json
import os

import numpy as np
import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies36 as S36
import strategies40 as S40
import strategies42 as S42
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 812


def _load_daily_csv(path: str) -> pd.DataFrame:
    df = pd.read_csv(os.path.expanduser(path))
    if "t" in df.columns:
        idx = pd.to_datetime(df["t"], utc=True)
        out = df.drop(columns=["t"]).copy()
        out.index = idx
    else:
        idx = pd.to_datetime(df["date"], utc=True)
        out = pd.DataFrame({"c": df["close"].to_numpy(dtype=float)}, index=idx)
    return out.sort_index()


def simulate_multi_asset_volparity_spot(
    assets: dict[str, pd.DataFrame],
    pos_map: dict[str, pd.Series],
    carry_ret: pd.Series,
    rate_pct: pd.Series,
    trend_share: float = 0.60,
    carry_share: float = 0.40,
    taker_bps: float = 2.0,
    slip_bps: float = 1.0,
    funding_bps_h: float = 0.0,
    dd_start: float = 0.06,
    dd_full: float = 0.20,
    dd_floor: float = 0.40,
    vol_window: int = 20,
) -> tuple[pd.Series, pd.Series, pd.DataFrame]:
    """Strictly causal close-to-close multi-asset inverse-vol-parity portfolio simulation."""
    idx = assets["BTC"].index
    coins = list(assets.keys())
    n = len(idx)

    rets = {}
    pos_arr = {}
    inv_vol = {}
    for c in coins:
        px = assets[c]["c"].reindex(idx)
        r = px.pct_change().fillna(0.0)
        rets[c] = r.to_numpy(dtype=float)
        pos_arr[c] = pos_map[c].reindex(idx).fillna(0.0).to_numpy(dtype=float)
        vol = px.pct_change().rolling(vol_window, min_periods=10).std()
        valid = px.notna() & vol.notna() & (vol > 1e-6) & pos_map[c].reindex(idx).notna()
        iv = np.where(valid.to_numpy(), 1.0 / np.maximum(vol.fillna(1.0).to_numpy(dtype=float), 1e-4), 0.0)
        inv_vol[c] = iv

    # Normalize inverse-vol weights causally at each close t
    weights = {c: np.zeros(n, dtype=float) for c in coins}
    for t in range(n):
        tot = sum(inv_vol[c][t] for c in coins)
        if tot > 0.0:
            for c in coins:
                weights[c][t] = inv_vol[c][t] / tot
        else:
            # Fallback before warmup: equal weight BTC/ETH
            for c in coins:
                weights[c][t] = 0.5 if c in ("BTC", "ETH") else 0.0

    cr = carry_ret.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    rf = rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0

    cost_rate = (taker_bps + slip_bps) * 1e-4
    fund_rate = funding_bps_h * 24.0 * 1e-4

    out_ret = np.zeros(n, dtype=float)
    mult_arr = np.ones(n, dtype=float)

    eq = 1.0
    peak = 1.0
    act = {c: 0.0 for c in coins}
    prev = {c: 0.0 for c in coins}
    prev_idle = max(0.0, float(trend_share))

    for t in range(n):
        held = {c: (0.0 if t == 0 else act[c]) for c in coins}
        turn = sum(abs(held[c] - prev[c]) for c in coins)
        gross_trend = sum(abs(held[c]) for c in coins)
        trend_gross_pnl = sum(held[c] * rets[c][t] for c in coins)
        trend_net = float(trend_share) * (
            trend_gross_pnl - turn * cost_rate - gross_trend * fund_rate
        )
        bills_ret = 0.0 if t == 0 else prev_idle * rf[t - 1]
        total_r = trend_net + float(carry_share) * cr[t] + bills_ret
        out_ret[t] = total_r

        eq = max(1e-12, eq * (1.0 + total_r))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        mult = S36.soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor)
        mult_arr[t] = mult

        tot_pos = 0.0
        for c in coins:
            act[c] = weights[c][t] * pos_arr[c][t] * mult
            tot_pos += act[c]
            prev[c] = held[c]
        prev_idle = max(0.0, float(trend_share) * (1.0 - tot_pos))

    w_df = pd.DataFrame({c: weights[c] for c in coins}, index=idx)
    return pd.Series(out_ret, index=idx), pd.Series(mult_arr, index=idx), w_df


def _full_metrics(ret: pd.Series, reg: pd.Series, mean_mult: float) -> dict:
    m = extended_metrics(ret, reg)
    m[f"deflated_sharpe_{N_TRIALS}"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    m["mean_taper_mult"] = round(float(mean_mult), 4)
    m.update(S42.drawdown_diagnostics(ret))
    return m


def main() -> None:
    daily_full = {
        "BTC": _load_daily_csv("~/piquant-collect/daily_csv/BTC-USD_1D.csv"),
        "ETH": _load_daily_csv("~/piquant-collect/daily_csv/ETH-USD_1D.csv"),
        "SOL": _load_daily_csv("~/piquant-data/alts/SOL.csv"),
    }
    d = {
        c: daily_full[c][(daily_full[c].index >= START) & (daily_full[c].index < END)]
        for c in ("BTC", "ETH", "SOL")
    }
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    dual_1x = {}
    for c in ("BTC", "ETH", "SOL"):
        qd = pd.Series(S35.coherence_positions(daily_full[c], lev=1.0), index=daily_full[c].index)
        r3 = pd.Series(S40.r3_fastexit_qd_positions(daily_full[c], lev=1.0), index=daily_full[c].index)
        dual_1x[c] = (0.5 * (qd + r3)).reindex(d[c].index)

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    sp = lab.US_SPOT
    carry_books = [S19.coin_returns(d[c], f[c], "C1", S19.BASE) for c in ("BTC", "ETH")]
    c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

    r_q42, m_q42, _ = S36.simulate_qd_carry_portfolio(
        btc_sub, eth_sub, dual_1x["BTC"], dual_1x["ETH"], c1_ret, rate,
        trend_share=0.60, carry_share=0.40,
        taker_bps=sp.taker_bps, slip_bps=sp.slip_bps, funding_bps_h=sp.funding_bps_h,
        use_taper=True, dd_start=0.06, dd_full=0.20, dd_floor=0.40,
    )
    q42_metrics = _full_metrics(r_q42, reg, float(m_q42.mean()))

    r_q51, m_q51, w_df = simulate_multi_asset_volparity_spot(
        d, dual_1x, c1_ret, rate,
        trend_share=0.60, carry_share=0.40,
        taker_bps=sp.taker_bps, slip_bps=sp.slip_bps, funding_bps_h=sp.funding_bps_h,
        dd_start=0.06, dd_full=0.20, dd_floor=0.40, vol_window=20,
    )
    q51_metrics = _full_metrics(r_q51, reg, float(m_q51.mean()))

    checks = {
        "1_geo_lift_ge_0p10": q51_metrics["geo_monthly_pct"] >= q42_metrics["geo_monthly_pct"] + 0.10,
        "2_sharpe_gt_q42": q51_metrics["sharpe"] > q42_metrics["sharpe"],
        "3_max_dd_ge_q42_minus_1pct": q51_metrics["max_dd_pct"] >= q42_metrics["max_dd_pct"] - 1.0,
        "4_A_geo_monthly_pos": q51_metrics["A_geo_monthly_pct"] > 0.0,
        "5_dsr_ge_0p80": q51_metrics[f"deflated_sharpe_{N_TRIALS}"] >= 0.80,
    }
    overall_pass = all(checks.values())

    sol_active_mask = w_df["SOL"] > 0.0
    out = {
        "prereg": "PREREG51",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "venue": "US_SPOT",
        "sol_active_days": int(sol_active_mask.sum()),
        "mean_weights_when_sol_active": {
            c: round(float(w_df.loc[sol_active_mask, c].mean()), 4) for c in ("BTC", "ETH", "SOL")
        },
        "variants": {
            "Q42_dualspeed_60_40_carry_spot": q42_metrics,
            "Q51_btc_eth_sol_volparity_spot": q51_metrics,
        },
        "checks": checks,
        "overall_pass": overall_pass,
    }

    out_path = os.path.join(HERE, "results", "round51.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as fh:
        json.dump(out, fh, indent=2)
    print(json.dumps(out, indent=2))


if __name__ == "__main__":
    main()
