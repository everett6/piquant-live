"""Run PREREG43 once: combining PREREG42 Expected-Drawdown control (6%->20% taper, R3 fast-exit,
higher C1 carry share) with PREREG38 BTC-gated Top-4 InvVol Leaders over 2020-01-01..2026-10-31.
Writes results/round43.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import lab
import strategies5 as S5
import strategies19 as S19
import strategies29 as S29
import strategies35 as S35
import strategies38 as S38
import strategies40 as S40
import strategies43 as S43
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round25 import load_alts
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 784


def _full_metrics(ret: pd.Series, reg: pd.Series, mean_mult: float, diag: dict[str, float]) -> dict:
    m = extended_metrics(ret, reg)
    m["deflated_sharpe_784"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    m["mean_taper_mult"] = round(float(mean_mult), 4)
    m.update(diag)
    return m


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    closes_all = load_alts(full["BTC"].index)
    closes_all.insert(0, "ETH", full["ETH"]["c"].reindex(full["BTC"].index))
    closes_all.insert(0, "BTC", full["BTC"]["c"])
    closes_all = closes_all[S29.COINS]

    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]
    closes_sub = closes_all.reindex(btc_sub.index)

    qd_1x = {
        c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index).reindex(btc_sub.index)
        for c in ("BTC", "ETH")
    }
    r3_1x = {
        c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index).reindex(btc_sub.index)
        for c in ("BTC", "ETH")
    }
    dual50_1x = {c: S43.asymmetric_dualspeed_positions(qd_1x[c], r3_1x[c], 0.50) for c in ("BTC", "ETH")}
    dual25_20_1x = {c: S43.asymmetric_dualspeed_positions(qd_1x[c], r3_1x[c], 25.0 / 45.0) for c in ("BTC", "ETH")}

    leader_w_sub = S38.leader_basket_weights(closes_all).reindex(btc_sub.index).fillna(0.0)
    empty_w = pd.DataFrame(0.0, index=btc_sub.index, columns=["BTC"])

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    # (name, pos_map, leader_w, trend_share, leader_share, carry_share, dd_start, dd_full)
    specs = [
        ("S36_qd_carry_blend", qd_1x, empty_w, 0.70, 0.00, 0.30, 0.10, 0.28),
        ("Q42_dualspeed_60_40_carry", dual50_1x, empty_w, 0.60, 0.00, 0.40, 0.06, 0.20),
        ("Q43_dualspeed_70_30_carry", dual50_1x, empty_w, 0.70, 0.00, 0.30, 0.06, 0.20),
        ("Q43_dualspeed_leader_65_35", dual25_20_1x, leader_w_sub, 0.45, 0.20, 0.35, 0.06, 0.20),
        ("Q43_macro_bull_leader_barbell", qd_1x, leader_w_sub, 0.30, 0.30, 0.40, 0.06, 0.20),
    ]

    res = {
        "prereg": "PREREG43",
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

        for vname, pos_map, lw, ts, ls, cs, ds, df in specs:
            ret_v, mult_v, diag_v = S43.evaluate_prereg43_variant(
                btc_sub, eth_sub, pos_map["BTC"], pos_map["ETH"], closes_sub, lw, c1_ret, rate,
                trend_share=ts, leader_share=ls, carry_share=cs,
                taker_bps=fut.taker_bps, slip_bps=fut.slip_bps, funding_bps_h=fut.funding_bps_h,
                spot_threshold=None, dd_start=ds, dd_full=df, dd_floor=0.40,
            )
            res["variants"].setdefault(vname, {})[cname] = _full_metrics(ret_v, reg, float(mult_v.mean()), diag_v)

            if cname == "base":
                sp = lab.US_SPOT
                ret_sp, mult_sp, diag_sp = S43.evaluate_prereg43_variant(
                    btc_sub, eth_sub, pos_map["BTC"], pos_map["ETH"], closes_sub, lw, c1_ret, rate,
                    trend_share=ts, leader_share=ls, carry_share=cs,
                    taker_bps=sp.taker_bps, slip_bps=sp.slip_bps, funding_bps_h=sp.funding_bps_h,
                    spot_threshold=1.0 - cs, dd_start=ds, dd_full=df, dd_floor=0.40,
                )
                res["operational_spot"][f"{vname}_spot"] = _full_metrics(ret_sp, reg, float(mult_sp.mean()), diag_sp)

    q42_b = res["variants"]["Q42_dualspeed_60_40_carry"]["base"]
    for vname, *_ in specs[2:]:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "1_geo_ge_2p35": vb["geo_monthly_pct"] >= 2.35,
            "2_max_dd_ge_neg24p5": vb["max_dd_pct"] >= -24.5,
            "3_expected_dd_and_ulcer_kept": vb["mean_dd_pct"] <= 10.50 and vb["ulcer_index_pct"] <= 12.20,
            "4_sharpe_and_calmar_gt_q42": vb["sharpe"] > q42_b["sharpe"] and vb["calmar"] > q42_b["calmar"],
            "5_recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = all(ch.values())

    out_path = os.path.join(HERE, "results", "round43.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as fh:
        json.dump(res, fh, indent=1)

    for vname, *_ in specs:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        p = res["pass"].get(vname)
        print(
            f"{vname:30s}: geo={vb['geo_monthly_pct']:+.2f}%/mo maxDD={vb['max_dd_pct']:+.1f}% "
            f"meanDD={vb['mean_dd_pct']:.2f}% ulcer={vb['ulcer_index_pct']:.2f}% cdar95={vb['cdar_95_pct']:.1f}% "
            f"Sharpe={vb['sharpe']:.3f} Calmar={vb['calmar']:.2f} bear={vb['by_regime']['bear']:+.1f} "
            f"A={vb['A_geo_monthly_pct']:+.2f} stress={vs['geo_monthly_pct']:+.2f} PASS={p}"
        )
    for sp_name, sp_m in res["operational_spot"].items():
        print(
            f"  [SPOT] {sp_name:32s}: geo={sp_m['geo_monthly_pct']:+.2f}%/mo maxDD={sp_m['max_dd_pct']:+.1f}% "
            f"meanDD={sp_m['mean_dd_pct']:.2f}% ulcer={sp_m['ulcer_index_pct']:.2f}% "
            f"Sharpe={sp_m['sharpe']:.3f} Calmar={sp_m['calmar']:.2f} A={sp_m['A_geo_monthly_pct']:+.2f}"
        )


if __name__ == "__main__":
    main()
