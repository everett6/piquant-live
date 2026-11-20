"""Run PREREG42 once: Expected Drawdown optimization on S36_qd_carry_blend over 2020-01-01..2026-10-31.
Evaluates:
  1) Q42_volcap_s36_blend     (70% vol-capped Safe-QD + 30% C1 + DTB3, 6%->20% taper)
  2) Q42_dualspeed_60_40_carry (30% Safe-QD + 30% R3-FastExit + 40% C1 + DTB3, 6%->20% taper)
  3) Q42_balanced_50_50_carry  (50% Safe-QD + 50% C1 + DTB3, 6%->20% taper)
Writes results/round42.json."""
from __future__ import annotations

import json
import os

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
N_TRIALS = 781


def _full_metrics(ret: pd.Series, reg: pd.Series, mean_mult: float) -> dict:
    m = extended_metrics(ret, reg)
    m["deflated_sharpe_781"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    m["mean_taper_mult"] = round(float(mean_mult), 4)
    m.update(S42.drawdown_diagnostics(ret))
    return m


def main() -> None:
    raw_1m = {c: lab.load_candles(product=f"{c}-USD", since=None) for c in ("BTC", "ETH")}
    daily_full = {c: lab.resample(raw_1m[c], "1D") for c in ("BTC", "ETH")}
    d = {c: daily_full[c][(daily_full[c].index >= START) & (daily_full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    qd_1x = {
        c: pd.Series(S35.coherence_positions(daily_full[c], lev=1.0), index=daily_full[c].index).reindex(d[c].index)
        for c in ("BTC", "ETH")
    }
    r3_1x = {
        c: pd.Series(S40.r3_fastexit_qd_positions(daily_full[c], lev=1.0), index=daily_full[c].index).reindex(d[c].index)
        for c in ("BTC", "ETH")
    }
    dual_1x = {c: 0.5 * (qd_1x[c] + r3_1x[c]) for c in ("BTC", "ETH")}
    volcap_1x = {
        c: S42.downside_vol_cap_positions(daily_full[c], pd.Series(S35.coherence_positions(daily_full[c], lev=1.0), index=daily_full[c].index), vol_target_ann=0.45, vol_window=20).reindex(d[c].index)
        for c in ("BTC", "ETH")
    }

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    res = {
        "prereg": "PREREG42",
        "n_trials": N_TRIALS,
        "window": [W0, W1],
        "regime_months": reg.value_counts().to_dict(),
        "variants": {},
        "operational_spot": {},
        "checks": {},
        "pass": {},
    }

    specs = [
        ("S36_qd_carry_blend", qd_1x, 0.70, 0.30, 0.10, 0.28),
        ("Q42_volcap_s36_blend", volcap_1x, 0.70, 0.30, 0.06, 0.20),
        ("Q42_dualspeed_60_40_carry", dual_1x, 0.60, 0.40, 0.06, 0.20),
        ("Q42_balanced_50_50_carry", qd_1x, 0.50, 0.50, 0.06, 0.20),
    ]

    for cname, (fut, car) in costs_map.items():
        carry_books = [S19.coin_returns(d[c], f[c], "C1", car) for c in ("BTC", "ETH")]
        c1_ret = S19.portfolio(carry_books).reindex(btc_sub.index).fillna(0.0)

        for vname, pos_map, ts, cs, ds, df in specs:
            r_v, m_v, _ = S36.simulate_qd_carry_portfolio(
                btc_sub, eth_sub, pos_map["BTC"], pos_map["ETH"], c1_ret, rate,
                trend_share=ts, carry_share=cs, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
                funding_bps_h=fut.funding_bps_h, use_taper=True, dd_start=ds, dd_full=df, dd_floor=0.40,
            )
            res["variants"].setdefault(vname, {})[cname] = _full_metrics(r_v, reg, float(m_v.mean()))

            if cname == "base":
                sp = lab.US_SPOT
                r_sp, m_sp, _ = S36.simulate_qd_carry_portfolio(
                    btc_sub, eth_sub, pos_map["BTC"], pos_map["ETH"], c1_ret, rate,
                    trend_share=ts, carry_share=cs, taker_bps=sp.taker_bps, slip_bps=sp.slip_bps,
                    funding_bps_h=sp.funding_bps_h, use_taper=True, dd_start=ds, dd_full=df, dd_floor=0.40,
                )
                res["operational_spot"][f"{vname}_spot"] = _full_metrics(r_sp, reg, float(m_sp.mean()))

    s36_b = res["variants"]["S36_qd_carry_blend"]["base"]
    for vname, _, _, _, _, _ in specs[1:]:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "1_expected_dd_and_ulcer_cut": (
                vb["mean_dd_pct"] <= 0.75 * s36_b["mean_dd_pct"]
                and vb["ulcer_index_pct"] <= 0.80 * s36_b["ulcer_index_pct"]
            ),
            "2_max_dd_ge_neg23_and_beats_s36": vb["max_dd_pct"] >= -23.0 and vb["max_dd_pct"] > s36_b["max_dd_pct"],
            "3_sharpe_and_calmar_gt_s36": vb["sharpe"] > s36_b["sharpe"] and vb["calmar"] > s36_b["calmar"],
            "4_geo_ge_1p75_and_bear_gt_s36": (
                vb["geo_monthly_pct"] >= 1.75 and vb["by_regime"]["bear"] > s36_b["by_regime"]["bear"]
            ),
            "5_recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = bool(all(ch.values()))

    for vname, _, _, _, _, _ in specs:
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        br = vb["by_regime"]
        print(
            f"{vname:25s}: geo={vb['geo_monthly_pct']:+.2f}%/mo maxDD={vb['max_dd_pct']:+.1f}% "
            f"meanDD={vb['mean_dd_pct']:.2f}% ulcer={vb['ulcer_index_pct']:.2f}% cdar95={vb['cdar_95_pct']:.1f}% "
            f"maxUW={vb['max_underwater_days']}d Sharpe={vb['sharpe']:.3f} Calmar={vb['calmar']:.2f} "
            f"bear={br['bear']:+.1f} stress={vs['geo_monthly_pct']:+.2f} PASS={res['pass'].get(vname)}"
        )
    for k, vm in res["operational_spot"].items():
        br = vm["by_regime"]
        print(
            f"  [SPOT] {k:29s}: geo={vm['geo_monthly_pct']:+.2f}%/mo maxDD={vm['max_dd_pct']:+.1f}% "
            f"meanDD={vm['mean_dd_pct']:.2f}% ulcer={vm['ulcer_index_pct']:.2f}% cdar95={vm['cdar_95_pct']:.1f}% "
            f"maxUW={vm['max_underwater_days']}d Sharpe={vm['sharpe']:.3f} Calmar={vm['calmar']:.2f} A={vm['A_geo_monthly_pct']:+.2f}"
        )

    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round42.json"), "w") as fh:
        json.dump(res, fh, indent=1, default=str)


if __name__ == "__main__":
    main()
