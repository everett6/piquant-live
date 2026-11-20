"""Run PREREG40 once: Multi-Clock 6h QD+C1 (Q40_6h_qd_carry) and Fast-Exit R3 x QD+C1 (Q40_r3_fastexit_blend)
over 2020-01-01..2026-10-31. Writes results/round40.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies30 as S30
import strategies35 as S35
import strategies36 as S36
import strategies40 as S40
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 775


def main():
    raw_1m = {c: lab.load_candles(product=f"{c}-USD", since=None) for c in ("BTC", "ETH")}
    b6_full = {c: S30.bars_6h(raw_1m[c]) for c in ("BTC", "ETH")}
    daily_full = {c: lab.resample(raw_1m[c], "1D") for c in ("BTC", "ETH")}

    d = {c: daily_full[c][(daily_full[c].index >= START) & (daily_full[c].index < END)] for c in ("BTC", "ETH")}
    b6 = {c: b6_full[c][(b6_full[c].index >= START) & (b6_full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    qd_1x = {c: pd.Series(S35.coherence_positions(daily_full[c], lev=1.0), index=daily_full[c].index).reindex(d[c].index) for c in d}
    r3_1x = {c: pd.Series(S40.r3_fastexit_qd_positions(daily_full[c], lev=1.0), index=daily_full[c].index).reindex(d[c].index) for c in d}
    qd_6h = {c: S40.split_qd_held(b6_full[c], lev=1.0).reindex(b6[c].index).fillna(0.0) for c in d}

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    res = {
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "regime_months": reg.value_counts().to_dict(),
        "variants": {},
        "operational_spot": {},
        "checks": {},
        "pass": {},
    }

    for cname, (fut, car) in costs_map.items():
        carry_books = [S19.coin_returns(d[c], f[c], "C1", car) for c in ("BTC", "ETH")]
        c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

        r_s36, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], c1_ret, rate,
            trend_share=0.70, carry_share=0.30, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=True,
        )
        r_r3, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, r3_1x["BTC"], r3_1x["ETH"], c1_ret, rate,
            trend_share=0.70, carry_share=0.30, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=True,
        )
        r_6h = S40.simulate_6h_qd_carry(
            b6["BTC"], b6["ETH"], qd_6h["BTC"], qd_6h["ETH"], c1_ret, rate,
            trend_share=0.70, carry_share=0.30, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=True,
        ).reindex(btc_sub.index).fillna(0.0)

        for vname, ret in (
            ("S36_qd_carry_blend", r_s36),
            ("Q40_6h_qd_carry", r_6h),
            ("Q40_r3_fastexit_blend", r_r3),
        ):
            res["variants"].setdefault(vname, {})[cname] = extended_metrics(ret, reg)

        if cname == "base":
            sp = lab.US_SPOT
            r_s36_sp, _, _ = S36.simulate_qd_carry_portfolio(
                btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], c1_ret, rate,
                trend_share=0.70, carry_share=0.30, taker_bps=sp.taker_bps, slip_bps=sp.slip_bps,
                funding_bps_h=sp.funding_bps_h, use_taper=True,
            )
            r_6h_sp = S40.simulate_6h_qd_carry(
                b6["BTC"], b6["ETH"], qd_6h["BTC"], qd_6h["ETH"], c1_ret, rate,
                trend_share=0.70, carry_share=0.30, taker_bps=sp.taker_bps, slip_bps=sp.slip_bps,
                funding_bps_h=sp.funding_bps_h, use_taper=True,
            ).reindex(btc_sub.index).fillna(0.0)
            r_r3_sp, _, _ = S36.simulate_qd_carry_portfolio(
                btc_sub, eth_sub, r3_1x["BTC"], r3_1x["ETH"], c1_ret, rate,
                trend_share=0.70, carry_share=0.30, taker_bps=sp.taker_bps, slip_bps=sp.slip_bps,
                funding_bps_h=sp.funding_bps_h, use_taper=True,
            )
            res["operational_spot"]["S36_qd_carry_blend_spot"] = extended_metrics(r_s36_sp, reg)
            res["operational_spot"]["Q40_6h_qd_carry_spot"] = extended_metrics(r_6h_sp, reg)
            res["operational_spot"]["Q40_r3_fastexit_blend_spot"] = extended_metrics(r_r3_sp, reg)

    s36_b = res["variants"]["S36_qd_carry_blend"]["base"]
    for vname in ("Q40_6h_qd_carry", "Q40_r3_fastexit_blend"):
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "max_dd_ge_minus_32": vb["max_dd_pct"] >= -32.0,
            "geo_and_calmar_gt_s36": vb["geo_monthly_pct"] > s36_b["geo_monthly_pct"] and vb["calmar"] > s36_b["calmar"],
            "sharpe_gt_s36": vb["sharpe"] > s36_b["sharpe"],
            "bear_ge_s36": vb["by_regime"]["bear"] >= s36_b["by_regime"]["bear"],
            "recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = bool(all(ch.values()))

    for vname in ("S36_qd_carry_blend", "Q40_6h_qd_carry", "Q40_r3_fastexit_blend"):
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        br = vb["by_regime"]
        print(
            f"{vname:22s}: geo={vb['geo_monthly_pct']:+.2f}%/mo DD={vb['max_dd_pct']:+.1f}% "
            f"Sharpe={vb['sharpe']:.3f} Calmar={vb['calmar']:.2f} "
            f"bear/flat/bull={br['bear']:+.1f}/{br['flat']:+.1f}/{br['bull']:+.1f} "
            f"2022={vb['y2022_geo_monthly_pct']:+.2f} A={vb['A_geo_monthly_pct']:+.2f} stress={vs['geo_monthly_pct']:+.2f} "
            f"PASS={res['pass'].get(vname)}"
        )
    for k, vm in res["operational_spot"].items():
        br = vm["by_regime"]
        print(
            f"  [SPOT] {k:28s}: geo={vm['geo_monthly_pct']:+.2f}%/mo DD={vm['max_dd_pct']:+.1f}% "
            f"Sharpe={vm['sharpe']:.3f} Calmar={vm['calmar']:.2f} bear/flat/bull={br['bear']:+.1f}/{br['flat']:+.1f}/{br['bull']:+.1f} "
            f"A={vm['A_geo_monthly_pct']:+.2f} DSR={vm['deflated_sharpe']:.3f}"
        )

    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round40.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
