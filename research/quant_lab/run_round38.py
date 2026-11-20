"""Run PREREG38 once: ping-adapted bull accelerators on S36_qd_carry_blend
(Q38_kuramoto_booster and Q38_bull_leader_blend) over 2020-01-01..2026-10-31.
Writes results/round38.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies29 as S29
import strategies35 as S35
import strategies36 as S36
import strategies38 as S38
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round25 import load_alts
from run_round33 import DTB3, W0, W1, stats
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 771


def main():
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    closes_all = load_alts(full["BTC"].index)
    closes_all.insert(0, "ETH", full["ETH"]["c"].reindex(full["BTC"].index))
    closes_all.insert(0, "BTC", full["BTC"]["c"])
    closes_all = closes_all[S29.COINS]

    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in full}
    btc_sub, eth_sub = d["BTC"], d["ETH"]
    closes_sub = closes_all.reindex(btc_sub.index)

    # Warmed-up causal signals before slicing to [W0, W1)
    qd_1x_full = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    qd_1x = {c: qd_1x_full[c].reindex(btc_sub.index) for c in ("BTC", "ETH")}
    breadth_full = S38.kuramoto_breadth(closes_all)
    k_b_full, k_e_full = S38.kuramoto_qd_targets(qd_1x_full["BTC"], qd_1x_full["ETH"], breadth_full)
    k_b = k_b_full.reindex(btc_sub.index)
    k_e = k_e_full.reindex(btc_sub.index)
    breadth_sub = breadth_full.reindex(btc_sub.index)

    leader_w_full = S38.leader_basket_weights(closes_all)
    leader_w_sub = leader_w_full.reindex(btc_sub.index).fillna(0.0)

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    res = {
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "regime_months": reg.value_counts().to_dict(),
        "signal_diagnostics": {
            "share_days_breadth_ge_0p70": float((breadth_sub >= S38.SYNC_THRESH).mean()),
            "mean_breadth": float(breadth_sub.mean()),
            "leader_mean_gross": float(leader_w_sub.sum(axis=1).mean()),
            "leader_active_days_pct": float((leader_w_sub.sum(axis=1) > 0).mean() * 100.0),
        },
        "variants": {},
        "operational_spot_margin": {},
        "checks": {},
        "pass": {},
    }

    empty_w = pd.DataFrame(0.0, index=btc_sub.index, columns=["BTC"])

    for cname, (fut, car) in costs_map.items():
        carry_books = [S19.coin_returns(d[c], f[c], "C1", car) for c in ("BTC", "ETH")]
        c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

        r_s36, _, lev_s36 = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], c1_ret, rate,
            trend_share=0.70, carry_share=0.30, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=True,
        )
        r_kura, mult_k, lev_k = S38.simulate_multi_sleeve_portfolio(
            btc_sub, eth_sub, k_b, k_e, closes_sub[["BTC"]], empty_w, c1_ret, rate,
            trend_share=0.70, leader_share=0.0, carry_share=0.30,
            taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
            use_taper=True,
        )
        r_lead, mult_l, lev_l = S38.simulate_multi_sleeve_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], closes_sub, leader_w_sub, c1_ret, rate,
            trend_share=0.40, leader_share=0.30, carry_share=0.30,
            taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
            use_taper=True,
        )

        for vname, ret, lev_s, mult_s in (
            ("S36_qd_carry_blend", r_s36, lev_s36, None),
            ("Q38_kuramoto_booster", r_kura, lev_k, mult_k),
            ("Q38_bull_leader_blend", r_lead, lev_l, mult_l),
        ):
            m = extended_metrics(ret, reg)
            m["mean_eff_trend_lev"] = float(lev_s.mean())
            m["max_eff_trend_lev"] = float(lev_s.max())
            if mult_s is not None:
                m["mean_taper_mult"] = float(mult_s.mean())
            res["variants"].setdefault(vname, {})[cname] = m

        if cname == "base":
            # Operational US Spot (up to 0.70 NAV, 2+1 bps, 0 funding) + Futures margin (>0.70 NAV)
            r_kura_op, _, _ = S38.simulate_multi_sleeve_portfolio(
                btc_sub, eth_sub, k_b, k_e, closes_sub[["BTC"]], empty_w, c1_ret, rate,
                trend_share=0.70, leader_share=0.0, carry_share=0.30,
                taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
                spot_threshold=0.70, spot_taker_bps=lab.US_SPOT.taker_bps, spot_slip_bps=lab.US_SPOT.slip_bps,
                use_taper=True,
            )
            r_lead_op, _, _ = S38.simulate_multi_sleeve_portfolio(
                btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], closes_sub, leader_w_sub, c1_ret, rate,
                trend_share=0.40, leader_share=0.30, carry_share=0.30,
                taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
                spot_threshold=0.70, spot_taker_bps=lab.US_SPOT.taker_bps, spot_slip_bps=lab.US_SPOT.slip_bps,
                use_taper=True,
            )
            res["operational_spot_margin"]["Q38_kuramoto_booster_spot_margin"] = extended_metrics(r_kura_op, reg)
            res["operational_spot_margin"]["Q38_bull_leader_blend_spot"] = extended_metrics(r_lead_op, reg)

    s36_b = res["variants"]["S36_qd_carry_blend"]["base"]
    for vname in ("Q38_kuramoto_booster", "Q38_bull_leader_blend"):
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "max_dd_ge_minus_40": vb["max_dd_pct"] >= -40.0,
            "geo_gt_s36_and_bull_plus_2": (
                vb["geo_monthly_pct"] > s36_b["geo_monthly_pct"]
                and vb["by_regime"]["bull"] >= s36_b["by_regime"]["bull"] + 2.0
            ),
            "sharpe_gt_s36": vb["sharpe"] > s36_b["sharpe"],
            "bear_ge_s36_minus_0p5": vb["by_regime"]["bear"] >= s36_b["by_regime"]["bear"] - 0.50,
            "recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = bool(all(ch.values()))

    for vname in ("S36_qd_carry_blend", "Q38_kuramoto_booster", "Q38_bull_leader_blend"):
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
    for k, vm in res["operational_spot_margin"].items():
        br = vm["by_regime"]
        print(
            f"  [OP] {k:34s}: geo={vm['geo_monthly_pct']:+.2f}%/mo DD={vm['max_dd_pct']:+.1f}% "
            f"Sharpe={vm['sharpe']:.3f} Calmar={vm['calmar']:.2f} bull={br['bull']:+.1f}% "
            f"A={vm['A_geo_monthly_pct']:+.2f} DSR={vm['deflated_sharpe']:.3f}"
        )

    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round38.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
