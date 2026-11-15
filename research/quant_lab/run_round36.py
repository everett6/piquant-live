"""Run PREREG36 once: risk-capped QD coherence trend (<=40% DD) + DTB3 T-bills (S36_safe_qd) and always-on
70% Safe-QD + 30% C1 funding carry + DTB3 blend (S36_qd_carry_blend) over 2020-01-01..2026-10-31.
Writes results/round36.json."""
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
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import A0, DTB3, W0, W1, geo, stats

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 767


def extended_metrics(r: pd.Series, reg: pd.Series, extra_2022: pd.Series | None = None) -> dict:
    st = stats(r, reg)
    dd = abs(st["max_dd_pct"])
    st["calmar"] = float((st["geo_monthly_pct"] * 12.0) / dd) if dd > 0 else float("inf")
    m = S5.complete_months(r)
    st["months"] = int(len(m))
    st["losing_months"] = float((m < 0).mean()) if len(m) else 0.0
    st["worst_month_pct"] = float(m.min() * 100.0) if len(m) else 0.0
    st["worst_day_pct"] = float(r.min() * 100.0)
    st["worst_day"] = str(r.idxmin().date())
    st["y2022_geo_monthly_pct"] = geo(S5.complete_months(cut(r, "2022-01-01", "2023-01-01")))
    st["deflated_sharpe"] = lab.deflated_sharpe(lab.daily_from_bars(r), N_TRIALS)
    return st


def main():
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in full}
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    # Warmed-up signals on full candles, sliced cleanly to [W0, W1)
    qd_2x = {c: pd.Series(S35.coherence_positions(full[c], lev=2.0), index=full[c].index).reindex(d[c].index) for c in d}
    qd_1x = {c: 0.5 * qd_2x[c] for c in d}
    r1_1x = {c: pd.Series(S5.m1_positions(full[c], 50, 1.0), index=full[c].index).reindex(d[c].index) for c in d}

    f = {c: load_funding(f"{c}USDT") for c in d}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    zero_rate = pd.Series(0.0, index=btc_sub.index)

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    res = {
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "regime_months": reg.value_counts().to_dict(),
        "variants": {},
        "spot_operational": {},
    }

    for cname, (fut, car) in costs_map.items():
        carry_books = [S19.coin_returns(d[c], f[c], "C1", car) for c in ("BTC", "ETH")]
        c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

        r_qd2x, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_2x["BTC"], qd_2x["ETH"], c1_ret, zero_rate,
            trend_share=1.0, carry_share=0.0, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=False,
        )
        r_r1b, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, r1_1x["BTC"], r1_1x["ETH"], c1_ret, rate,
            trend_share=1.0, carry_share=0.0, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=False,
        )
        r_c1, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], c1_ret, zero_rate,
            trend_share=0.0, carry_share=1.0, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=False,
        )
        r_sqd, mult_sqd, lev_sqd = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], c1_ret, rate,
            trend_share=1.0, carry_share=0.0, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=True,
        )
        r_blend, mult_bl, lev_bl = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], c1_ret, rate,
            trend_share=0.70, carry_share=0.30, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=True,
        )

        series_map = {
            "QD_2x": r_qd2x,
            "R1_1x_plus_bills": r_r1b,
            "C1_carry_alone": r_c1,
            "S36_safe_qd": r_sqd,
            "S36_qd_carry_blend": r_blend,
        }
        for name, ser in series_map.items():
            st = extended_metrics(ser, reg)
            if cname == "base" and name == "S36_safe_qd":
                st["taper_mult_min"] = float(mult_sqd.min())
                st["taper_mult_mean"] = float(mult_sqd.mean())
                st["mean_eff_trend_lev"] = float(lev_sqd.mean())
            if cname == "base" and name == "S36_qd_carry_blend":
                st["taper_mult_min"] = float(mult_bl.min())
                st["taper_mult_mean"] = float(mult_bl.mean())
                st["mean_eff_trend_lev"] = float(lev_bl.mean())
            res["variants"].setdefault(name, {})[cname] = st

    # Operational check for S36_safe_qd on us-spot (2+1 bps, 0 funding)
    zero_carry = pd.Series(0.0, index=btc_sub.index)
    r_spot, m_spot, _ = S36.simulate_qd_carry_portfolio(
        btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], zero_carry, rate,
        trend_share=1.0, carry_share=0.0, taker_bps=lab.US_SPOT.taker_bps, slip_bps=lab.US_SPOT.slip_bps,
        funding_bps_h=lab.US_SPOT.funding_bps_h, use_taper=True,
    )
    res["spot_operational"]["S36_safe_qd_spot"] = extended_metrics(r_spot, reg)

    # Pre-registered checks (base costs)
    v = res["variants"]
    qd2 = v["QD_2x"]["base"]
    r1b = v["R1_1x_plus_bills"]["base"]
    sqd = v["S36_safe_qd"]["base"]
    sqd_s = v["S36_safe_qd"]["stress"]
    bl = v["S36_qd_carry_blend"]["base"]
    bl_s = v["S36_qd_carry_blend"]["stress"]

    res["checks"] = {
        "S36_safe_qd": {
            "max_dd_le_40pct": sqd["max_dd_pct"] >= -40.0,
            "sharpe_gt_QD2x": sqd["sharpe"] > qd2["sharpe"],
            "calmar_gt_R1_bills": sqd["calmar"] > r1b["calmar"],
            "A_positive": sqd["A_geo_monthly_pct"] > 0.0,
            "stress_positive": sqd_s["geo_monthly_pct"] > 0.0,
        },
        "S36_qd_carry_blend": {
            "max_dd_le_35pct": bl["max_dd_pct"] >= -35.0,
            "sharpe_gt_QD2x_and_safeQD": bl["sharpe"] > max(qd2["sharpe"], sqd["sharpe"]),
            "bear_gt_safeQD": bl["by_regime"]["bear"] > sqd["by_regime"]["bear"],
            "A_positive": bl["A_geo_monthly_pct"] > 0.0,
            "stress_positive": bl_s["geo_monthly_pct"] > 0.0,
        },
    }
    res["pass"] = {k: bool(all(ch.values())) for k, ch in res["checks"].items()}

    for name in ("QD_2x", "R1_1x_plus_bills", "C1_carry_alone", "S36_safe_qd", "S36_qd_carry_blend"):
        b = v[name]["base"]
        s = v[name]["stress"]
        br = b["by_regime"]
        print(
            f"{name:19s} {b['geo_monthly_pct']:+.2f}%/mo maxDD {b['max_dd_pct']:+.1f}% sharpe {b['sharpe']:.3f} "
            f"calmar {b['calmar']:.2f} | 2022 {b['y2022_geo_monthly_pct']:+.2f} A {b['A_geo_monthly_pct']:+.2f} | "
            f"bear {br['bear']:+.1f} flat {br['flat']:+.1f} bull {br['bull']:+.1f} | "
            f"worst_day {b['worst_day_pct']:+.2f}% ({b['worst_day']}) | stress {s['geo_monthly_pct']:+.2f}",
            flush=True,
        )
    sp = res["spot_operational"]["S36_safe_qd_spot"]
    print(f"S36_safe_qd_spot    {sp['geo_monthly_pct']:+.2f}%/mo maxDD {sp['max_dd_pct']:+.1f}% sharpe {sp['sharpe']:.3f} "
          f"calmar {sp['calmar']:.2f} | 2022 {sp['y2022_geo_monthly_pct']:+.2f} A {sp['A_geo_monthly_pct']:+.2f}", flush=True)
    for k in ("S36_safe_qd", "S36_qd_carry_blend"):
        print(f"{k} PASS={res['pass'][k]} checks={res['checks'][k]}", flush=True)

    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round36.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
