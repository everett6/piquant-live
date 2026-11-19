"""Run PREREG45 once: Unified Feature Store, Purged-CV GBDT (fee-gated vs un-gated),
Causal 3-State Regime Switching + Pairs Mean Reversion, and Meta-Labeling Safe-QD over 2020-01-01..2026-10-31.
Writes results/round45.json."""
from __future__ import annotations

import json
import os

import pandas as pd

import feature_store as FS
import lab
import strategies5 as S5
import strategies19 as S19
import strategies35 as S35
import strategies36 as S36
import strategies42 as S42
import strategies45 as S45
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 790


def _ann_pct(geo_mo_pct: float) -> float:
    return round(((1.0 + geo_mo_pct / 100.0) ** 12 - 1.0) * 100.0, 2)


def _full_metrics(ret: pd.Series, reg: pd.Series, mean_mult: float) -> dict:
    m = extended_metrics(ret, reg)
    m["annualized_pct"] = _ann_pct(m["geo_monthly_pct"])
    m["deflated_sharpe_790"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    m["mean_taper_mult"] = round(float(mean_mult), 4)
    m.update(S42.drawdown_diagnostics(ret))
    return m


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    qd_1x_full = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    qd_1x = {c: qd_1x_full[c].reindex(btc_sub.index) for c in ("BTC", "ETH")}

    features_full = FS.build_unified_features(full["BTC"], full["ETH"])
    regimes_full = S45.causal_regime_labels(features_full)

    # 1) Fee-gated GBDT (20 bps hurdle) vs naive 0-bps un-gated comparator (for Check 5)
    gbdt_fee_b, gbdt_fee_e = S45.walk_forward_gbdt_fee_gated_positions(full["BTC"], full["ETH"], fee_hurdle_bps=20.0)
    gbdt_0bps_b, gbdt_0bps_e = S45.walk_forward_gbdt_fee_gated_positions(full["BTC"], full["ETH"], fee_hurdle_bps=0.0)

    # 2) Causal 3-state GMM regime switching (Trend=Safe-QD, MeanRevert=fee-gated pairs, Crisis=0)
    reg_b, reg_e = S45.regime_switched_positions(
        full["BTC"], full["ETH"], qd_1x_full["BTC"], qd_1x_full["ETH"], features_full, regimes_full, fee_hurdle_bps=16.0
    )

    # 3) Purged Meta-Labeling overlay on Safe-QD
    meta_b, meta_e = S45.meta_label_qd_positions(
        full["BTC"], full["ETH"], qd_1x_full["BTC"], qd_1x_full["ETH"], features_full, fee_hurdle_bps=18.0
    )

    pos_maps = {
        "S36_qd_carry_blend": (qd_1x["BTC"], qd_1x["ETH"], 0.70, 0.30, 0.10, 0.28),
        "Q45_gbdt_ungated_0bps_ref": (gbdt_0bps_b.reindex(btc_sub.index), gbdt_0bps_e.reindex(btc_sub.index), 0.70, 0.30, 0.06, 0.20),
        "Q45_gbdt_fee_gated": (gbdt_fee_b.reindex(btc_sub.index), gbdt_fee_e.reindex(btc_sub.index), 0.70, 0.30, 0.06, 0.20),
        "Q45_hmm_regime_qd_carry": (reg_b.reindex(btc_sub.index), reg_e.reindex(btc_sub.index), 0.70, 0.30, 0.06, 0.20),
        "Q45_ml_metalabel_qd_carry": (meta_b.reindex(btc_sub.index), meta_e.reindex(btc_sub.index), 0.70, 0.30, 0.06, 0.20),
    }

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    reg_counts = regimes_full.reindex(btc_sub.index).value_counts().to_dict()
    res = {
        "prereg": "PREREG45",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "regime_months": reg.value_counts().to_dict(),
        "gmm_regime_days": {str(k): int(v) for k, v in reg_counts.items()},
        "variants": {},
        "operational_spot": {},
        "checks": {},
        "pass": {},
    }

    for cname, (fut, car) in costs_map.items():
        carry_books = [S19.coin_returns(d[c], f[c], "C1", car) for c in ("BTC", "ETH")]
        c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

        for vname, (pb, pe, ts, cs, ds, df) in pos_maps.items():
            r_v, m_v, _ = S36.simulate_qd_carry_portfolio(
                btc_sub, eth_sub, pb, pe, c1_ret, rate,
                trend_share=ts, carry_share=cs, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
                funding_bps_h=fut.funding_bps_h, use_taper=True, dd_start=ds, dd_full=df, dd_floor=0.40,
            )
            res["variants"].setdefault(vname, {})[cname] = _full_metrics(r_v, reg, float(m_v.mean()))

            if cname == "base":
                sp = lab.US_SPOT
                r_sp, m_sp, _ = S36.simulate_qd_carry_portfolio(
                    btc_sub, eth_sub, pb, pe, c1_ret, rate,
                    trend_share=ts, carry_share=cs, taker_bps=sp.taker_bps, slip_bps=sp.slip_bps,
                    funding_bps_h=sp.funding_bps_h, use_taper=True, dd_start=ds, dd_full=df, dd_floor=0.40,
                )
                res["operational_spot"][f"{vname}_spot"] = _full_metrics(r_sp, reg, float(m_sp.mean()))

    naive_sh = res["variants"]["Q45_gbdt_ungated_0bps_ref"]["base"]["sharpe"]
    for vname in ("Q45_gbdt_fee_gated", "Q45_hmm_regime_qd_carry", "Q45_ml_metalabel_qd_carry"):
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "1_geo_ge_2p25_and_max_dd_ge_neg26": vb["geo_monthly_pct"] >= 2.25 and vb["max_dd_pct"] >= -26.0,
            "2_sharpe_and_calmar_gt_q42": vb["sharpe"] > 1.363 and vb["calmar"] > 1.286,
            "3_mean_dd_le_11p50": vb["mean_dd_pct"] <= 11.50,
            "4_recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
            "5_beats_ungated_0bps_sharpe": vb["sharpe"] > naive_sh,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = all(ch.values())

    out_path = os.path.join(HERE, "results", "round45.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as fh:
        json.dump(res, fh, indent=1)

    print("GMM regime days:", res["gmm_regime_days"])
    for vname in pos_maps:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        p = res["pass"].get(vname)
        print(
            f"{vname:28s}: geo={vb['geo_monthly_pct']:+.2f}%/mo ({vb['annualized_pct']:+.1f}%/yr) "
            f"maxDD={vb['max_dd_pct']:+.1f}% meanDD={vb['mean_dd_pct']:.2f}% ulcer={vb['ulcer_index_pct']:.2f}% "
            f"Sharpe={vb['sharpe']:.3f} Calmar={vb['calmar']:.2f} A={vb['A_geo_monthly_pct']:+.2f} "
            f"stress={vs['geo_monthly_pct']:+.2f} PASS={p}"
        )
    for sp_name, sp_m in res["operational_spot"].items():
        print(
            f"  [SPOT] {sp_name:33s}: geo={sp_m['geo_monthly_pct']:+.2f}%/mo ({sp_m['annualized_pct']:+.1f}%/yr) "
            f"maxDD={sp_m['max_dd_pct']:+.1f}% meanDD={sp_m['mean_dd_pct']:.2f}% Sharpe={sp_m['sharpe']:.3f} A={sp_m['A_geo_monthly_pct']:+.2f}"
        )


if __name__ == "__main__":
    main()
