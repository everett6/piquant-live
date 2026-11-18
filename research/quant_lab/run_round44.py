"""Run PREREG44 once: 1.0x..2.5x Volatility-Balanced Safe-QD/Dual-Speed + Buy-and-Hold Volatility Harvester
over 2020-01-01..2026-10-31. Writes results/round44.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies40 as S40
import strategies42 as S42
import strategies44 as S44
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 787


def _ann_pct(geo_mo_pct: float) -> float:
    return round(((1.0 + geo_mo_pct / 100.0) ** 12 - 1.0) * 100.0, 2)


def _full_metrics(ret: pd.Series, reg: pd.Series, mean_mult: float, mean_lev: float) -> dict:
    m = extended_metrics(ret, reg)
    m["annualized_pct"] = _ann_pct(m["geo_monthly_pct"])
    m["deflated_sharpe_787"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    m["mean_taper_mult"] = round(float(mean_mult), 4)
    m["mean_eff_lev"] = round(float(mean_lev), 4)
    m.update(S42.drawdown_diagnostics(ret))
    return m


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    qd_1x_full = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    r3_1x_full = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    dual_1x_full = {c: 0.5 * (qd_1x_full[c] + r3_1x_full[c]) for c in ("BTC", "ETH")}

    qd_1x = {c: qd_1x_full[c].reindex(btc_sub.index) for c in ("BTC", "ETH")}
    dual_1x = {c: dual_1x_full[c].reindex(btc_sub.index) for c in ("BTC", "ETH")}

    # Warmed-up 1.0x..2.5x vol-balanced positions and BH vol-harvester positions
    vb75 = {
        c: S44.vol_balanced_1x_2p5x_positions(full[c], qd_1x_full[c], vol_target_ann=0.75, min_lev=1.0, max_lev=2.5).reindex(btc_sub.index)
        for c in ("BTC", "ETH")
    }
    vb80 = {
        c: S44.vol_balanced_1x_2p5x_positions(full[c], qd_1x_full[c], vol_target_ann=0.80, min_lev=1.0, max_lev=2.5).reindex(btc_sub.index)
        for c in ("BTC", "ETH")
    }
    vb85_dual = {
        c: S44.vol_balanced_1x_2p5x_positions(full[c], dual_1x_full[c], vol_target_ann=0.85, min_lev=1.0, max_lev=2.5).reindex(btc_sub.index)
        for c in ("BTC", "ETH")
    }
    bh_harv = {
        c: S44.bh_vol_harvester_positions(full[c], base_lev=1.0, min_mult=0.65, max_mult=1.35).reindex(btc_sub.index)
        for c in ("BTC", "ETH")
    }
    zero_bh = {c: pd.Series(0.0, index=btc_sub.index) for c in ("BTC", "ETH")}

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    # (name, trend_map, bh_map, trend_share, bh_share, carry_share, dd_start, dd_full, dd_floor)
    specs = [
        ("S36_qd_carry_blend", qd_1x, zero_bh, 0.70, 0.00, 0.30, 0.10, 0.28, 0.40),
        ("Q42_dualspeed_60_40_carry", dual_1x, zero_bh, 0.60, 0.00, 0.40, 0.06, 0.20, 0.40),
        ("Q44_vol_balanced_1x_2p5x_blend", vb75, zero_bh, 0.70, 0.00, 0.30, 0.08, 0.28, 0.35),
        ("Q44_bh_vol_harvester_1x_2p5x", vb80, bh_harv, 0.55, 0.20, 0.25, 0.08, 0.30, 0.35),
        ("Q44_barbell_1x_bh_2p5x_dualspeed", vb85_dual, bh_harv, 0.50, 0.25, 0.25, 0.08, 0.28, 0.35),
    ]

    res = {
        "prereg": "PREREG44",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "regime_months": reg.value_counts().to_dict(),
        "variants": {},
        "operational_spot_margin": {},
        "checks": {},
        "pass": {},
    }

    for cname, (fut, car) in costs_map.items():
        carry_books = [S19.coin_returns(d[c], f[c], "C1", car) for c in ("BTC", "ETH")]
        c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

        for vname, t_map, b_map, ts, bs, cs, ds, df, dfl in specs:
            r_v, m_v, l_v = S44.simulate_bh_vol_balanced_portfolio(
                btc_sub, eth_sub, t_map["BTC"], t_map["ETH"], b_map["BTC"], b_map["ETH"],
                c1_ret, rate, trend_share=ts, bh_share=bs, carry_share=cs,
                taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
                spot_threshold=None, use_taper=True, dd_start=ds, dd_full=df, dd_floor=dfl,
            )
            res["variants"].setdefault(vname, {})[cname] = _full_metrics(r_v, reg, float(m_v.mean()), float(l_v.mean()))

            if cname == "base":
                r_sp, m_sp, l_sp = S44.simulate_bh_vol_balanced_portfolio(
                    btc_sub, eth_sub, t_map["BTC"], t_map["ETH"], b_map["BTC"], b_map["ETH"],
                    c1_ret, rate, trend_share=ts, bh_share=bs, carry_share=cs,
                    taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
                    spot_threshold=1.0 - cs, spot_taker_bps=lab.US_SPOT.taker_bps, spot_slip_bps=lab.US_SPOT.slip_bps,
                    use_taper=True, dd_start=ds, dd_full=df, dd_floor=dfl,
                )
                res["operational_spot_margin"][f"{vname}_spot_margin"] = _full_metrics(r_sp, reg, float(m_sp.mean()), float(l_sp.mean()))

    s36_b = res["variants"]["S36_qd_carry_blend"]["base"]
    for vname, *_ in specs[2:]:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "1_geo_ge_3p40_and_gt_s36": vb["geo_monthly_pct"] >= 3.40 and vb["geo_monthly_pct"] > s36_b["geo_monthly_pct"],
            "2_max_dd_ge_neg38": vb["max_dd_pct"] >= -38.0,
            "3_sharpe_ge_1p30_and_calmar_ge_1p25": vb["sharpe"] >= 1.30 and vb["calmar"] >= 1.25,
            "4_recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
            "5_ulcer_le_22": vb["ulcer_index_pct"] <= 22.0,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = all(ch.values())

    out_path = os.path.join(HERE, "results", "round44.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as fh:
        json.dump(res, fh, indent=1)

    for vname, *_ in specs:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        p = res["pass"].get(vname)
        print(
            f"{vname:33s}: geo={vb['geo_monthly_pct']:+.2f}%/mo ({vb['annualized_pct']:+.1f}%/yr) "
            f"maxDD={vb['max_dd_pct']:+.1f}% meanDD={vb['mean_dd_pct']:.2f}% ulcer={vb['ulcer_index_pct']:.2f}% "
            f"Sharpe={vb['sharpe']:.3f} Calmar={vb['calmar']:.2f} effLev={vb['mean_eff_lev']:.2f}x "
            f"A={vb['A_geo_monthly_pct']:+.2f} stress={vs['geo_monthly_pct']:+.2f} PASS={p}"
        )
    for sp_name, sp_m in res["operational_spot_margin"].items():
        print(
            f"  [SPOT+MARGIN] {sp_name:38s}: geo={sp_m['geo_monthly_pct']:+.2f}%/mo ({sp_m['annualized_pct']:+.1f}%/yr) "
            f"maxDD={sp_m['max_dd_pct']:+.1f}% meanDD={sp_m['mean_dd_pct']:.2f}% ulcer={sp_m['ulcer_index_pct']:.2f}% "
            f"Sharpe={sp_m['sharpe']:.3f} Calmar={sp_m['calmar']:.2f} A={sp_m['A_geo_monthly_pct']:+.2f}"
        )


if __name__ == "__main__":
    main()
