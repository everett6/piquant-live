"""Run PREREG46 once: MFT Alpha-Decay Throttle & Causal Self-Improving Exponential-Gradient Loop
over 2020-01-01..2026-10-31. Writes results/round46.json."""
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
import strategies46 as S46
from run_round7 import cut
from run_round19 import END, START, load_funding
from run_round33 import DTB3, W0, W1
from run_round36 import extended_metrics

HERE = os.path.dirname(os.path.abspath(__file__))
N_TRIALS = 808


def _ann_pct(geo_mo_pct: float) -> float:
    return round(((1.0 + geo_mo_pct / 100.0) ** 12 - 1.0) * 100.0, 2)


def _full_metrics(ret: pd.Series, reg: pd.Series, mean_mult: float) -> dict:
    m = extended_metrics(ret, reg)
    m["annualized_pct"] = _ann_pct(m["geo_monthly_pct"])
    m["deflated_sharpe_808"] = round(lab.deflated_sharpe(ret, N_TRIALS), 4)
    m["mean_taper_mult"] = round(float(mean_mult), 4)
    m.update(S42.drawdown_diagnostics(ret))
    return m


def _simulate_exp_grad_portfolio(
    btc: pd.DataFrame,
    eth: pd.DataFrame,
    qd_b: pd.Series,
    qd_e: pd.Series,
    r3_b: pd.Series,
    r3_e: pd.Series,
    c1_ret: pd.Series,
    rate_pct: pd.Series,
    w_df: pd.DataFrame,
    taker_bps: float,
    slip_bps: float,
    funding_bps_h: float,
    dd_start: float = 0.06,
    dd_full: float = 0.20,
    dd_floor: float = 0.40,
) -> tuple[pd.Series, pd.Series]:
    """Causal portfolio simulator applying time-varying [qd, r3, carry, dtb3] weights decided at close t
    (held over t+1) with soft drawdown taper on active [qd, r3] trend legs."""
    idx = btc.index
    n = len(idx)
    rb = btc["c"].pct_change().fillna(0.0).to_numpy(dtype=float)
    re = eth["c"].reindex(idx).ffill().pct_change().fillna(0.0).to_numpy(dtype=float)
    qb = qd_b.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    qe = qd_e.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    fb = r3_b.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    fe = r3_e.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    cr = c1_ret.reindex(idx).fillna(0.0).to_numpy(dtype=float)
    rf = rate_pct.reindex(idx, method="ffill").fillna(0.0).to_numpy(dtype=float) / 100.0 / 365.0

    w_qd = w_df["qd"].reindex(idx).fillna(0.35).to_numpy(dtype=float)
    w_r3 = w_df["r3"].reindex(idx).fillna(0.25).to_numpy(dtype=float)
    w_ca = w_df["carry"].reindex(idx).fillna(0.30).to_numpy(dtype=float)

    cost_rate = (taker_bps + slip_bps) * 1e-4
    fund_rate = funding_bps_h * 24.0 * 1e-4

    out_ret = np.zeros(n, dtype=float)
    mult_arr = np.ones(n, dtype=float)
    eq, peak = 1.0, 1.0
    act_wb, act_we, act_ca, act_idle = 0.0, 0.0, 0.30, 0.70
    prev_wb, prev_we = 0.0, 0.0

    for t in range(n):
        held_wb = 0.0 if t == 0 else act_wb
        held_we = 0.0 if t == 0 else act_we
        held_ca = 0.30 if t == 0 else act_ca
        held_idle = 0.0 if t == 0 else act_idle

        turn = abs(held_wb - prev_wb) + abs(held_we - prev_we)
        gross = abs(held_wb) + abs(held_we)
        crypto_pnl = held_wb * rb[t] + held_we * re[t] - turn * cost_rate - gross * fund_rate
        bills_ret = 0.0 if t == 0 else held_idle * rf[t - 1]
        tot = crypto_pnl + held_ca * cr[t] + bills_ret
        out_ret[t] = tot

        eq = max(1e-12, eq * (1.0 + tot))
        if eq > peak:
            peak = eq
        dd = 1.0 - eq / peak
        mult = S36.soft_taper(dd, dd_start=dd_start, dd_full=dd_full, floor=dd_floor)
        mult_arr[t] = mult

        prev_wb, prev_we = held_wb, held_we
        act_wb = 0.5 * (w_qd[t] * qb[t] + w_r3[t] * fb[t]) * mult
        act_we = 0.5 * (w_qd[t] * qe[t] + w_r3[t] * fe[t]) * mult
        act_ca = w_ca[t]
        act_idle = max(0.0, (1.0 - act_ca) - (abs(act_wb) + abs(act_we)))

    return pd.Series(out_ret, index=idx), pd.Series(mult_arr, index=idx)


def main() -> None:
    full = {c: lab.resample(lab.load_candles(product=f"{c}-USD", since=None), "1D") for c in ("BTC", "ETH")}
    d = {c: full[c][(full[c].index >= START) & (full[c].index < END)] for c in ("BTC", "ETH")}
    btc_sub, eth_sub = d["BTC"], d["ETH"]

    qd_1x_full = {c: pd.Series(S35.coherence_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}
    r3_1x_full = {c: pd.Series(S40.r3_fastexit_qd_positions(full[c], lev=1.0), index=full[c].index) for c in ("BTC", "ETH")}

    qd_1x = {c: qd_1x_full[c].reindex(btc_sub.index) for c in ("BTC", "ETH")}
    r3_1x = {c: r3_1x_full[c].reindex(btc_sub.index) for c in ("BTC", "ETH")}
    dual_1x = {c: 0.5 * (qd_1x[c] + r3_1x[c]) for c in ("BTC", "ETH")}

    f = {c: load_funding(f"{c}USDT") for c in ("BTC", "ETH")}
    raw = pd.read_csv(DTB3, parse_dates=["date"])
    rate = pd.Series(raw["rate"].to_numpy(dtype=float), index=raw["date"].dt.tz_localize("UTC")).dropna()
    rf_daily = (rate.reindex(btc_sub.index, method="ffill").fillna(0.0) / 100.0 / 365.0)

    reg = S5.regime_months(S5.complete_months(cut(btc_sub["c"].pct_change().fillna(0), W0, W1)))
    costs_map = {"base": (lab.US_FUTURES, S19.BASE), "stress": (lab.US_FUTURES_STRESS, S19.STRESS)}

    res = {
        "prereg": "PREREG46",
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

        # Standalone net sleeve streams for causal alpha-decay measurement
        qd_net, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], pd.Series(0.0, index=btc_sub.index), rate,
            trend_share=1.0, carry_share=0.0, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=False,
        )
        r3_net, _, _ = S36.simulate_qd_carry_portfolio(
            btc_sub, eth_sub, r3_1x["BTC"], r3_1x["ETH"], pd.Series(0.0, index=btc_sub.index), rate,
            trend_share=1.0, carry_share=0.0, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
            funding_bps_h=fut.funding_bps_h, use_taper=False,
        )

        th_qd = S46.rolling_alpha_decay_throttle(qd_net, lookback=60, floor_mult=0.35)
        th_r3 = S46.rolling_alpha_decay_throttle(r3_net, lookback=60, floor_mult=0.35)

        qd_th = {c: qd_1x[c] * th_qd for c in ("BTC", "ETH")}
        dual_th = {c: 0.5 * (qd_1x[c] * th_qd + r3_1x[c] * th_r3) for c in ("BTC", "ETH")}

        sleeve_df = pd.DataFrame({"qd": qd_net, "r3": r3_net, "carry": c1_ret, "dtb3": rf_daily}, index=btc_sub.index)
        w_eg = S46.self_improving_exp_grad_weights(sleeve_df, eta=0.15, min_carry=0.25, max_trend=0.70, lookback=45)

        specs = [
            ("S36_qd_carry_blend", qd_1x, 0.70, 0.30, 0.10, 0.28),
            ("Q42_dualspeed_60_40_carry", dual_1x, 0.60, 0.40, 0.06, 0.20),
            ("Q46_ic_decay_throttled_s36", qd_th, 0.70, 0.30, 0.10, 0.28),
            ("Q46_ic_decay_throttled_q42", dual_th, 0.60, 0.40, 0.06, 0.20),
        ]
        for vname, pmap, ts, cs, ds, df in specs:
            rv, mv, _ = S36.simulate_qd_carry_portfolio(
                btc_sub, eth_sub, pmap["BTC"], pmap["ETH"], c1_ret, rate,
                trend_share=ts, carry_share=cs, taker_bps=fut.taker_bps, slip_bps=fut.slip_bps,
                funding_bps_h=fut.funding_bps_h, use_taper=True, dd_start=ds, dd_full=df, dd_floor=0.40,
            )
            res["variants"].setdefault(vname, {})[cname] = _full_metrics(rv, reg, float(mv.mean()))
            if cname == "base":
                sp = lab.US_SPOT
                rsp, msp, _ = S36.simulate_qd_carry_portfolio(
                    btc_sub, eth_sub, pmap["BTC"], pmap["ETH"], c1_ret, rate,
                    trend_share=ts, carry_share=cs, taker_bps=sp.taker_bps, slip_bps=sp.slip_bps,
                    funding_bps_h=sp.funding_bps_h, use_taper=True, dd_start=ds, dd_full=df, dd_floor=0.40,
                )
                res["operational_spot"][f"{vname}_spot"] = _full_metrics(rsp, reg, float(msp.mean()))

        r_eg, m_eg = _simulate_exp_grad_portfolio(
            btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], r3_1x["BTC"], r3_1x["ETH"],
            c1_ret, rate, w_eg, fut.taker_bps, fut.slip_bps, fut.funding_bps_h,
        )
        res["variants"].setdefault("Q46_exp_grad_self_improving", {})[cname] = _full_metrics(r_eg, reg, float(m_eg.mean()))
        if cname == "base":
            sp = lab.US_SPOT
            r_eg_sp, m_eg_sp = _simulate_exp_grad_portfolio(
                btc_sub, eth_sub, qd_1x["BTC"], qd_1x["ETH"], r3_1x["BTC"], r3_1x["ETH"],
                c1_ret, rate, w_eg, sp.taker_bps, sp.slip_bps, sp.funding_bps_h,
            )
            res["operational_spot"]["Q46_exp_grad_self_improving_spot"] = _full_metrics(r_eg_sp, reg, float(m_eg_sp.mean()))
            res["final_self_improving_weights"] = {k: round(float(w_eg[k].iloc[-1]), 4) for k in ("qd", "r3", "carry", "dtb3")}

    for vname in ("Q46_ic_decay_throttled_s36", "Q46_ic_decay_throttled_q42", "Q46_exp_grad_self_improving"):
        vb = res["variants"][vname]["base"]
        vs = res["variants"][vname]["stress"]
        ch = {
            "1_sharpe_and_calmar_gt_q42": vb["sharpe"] > 1.363 and vb["calmar"] > 1.286,
            "2_max_dd_ge_neg23_and_mean_dd_le_9p5": vb["max_dd_pct"] >= -23.0 and vb["mean_dd_pct"] <= 9.50,
            "3_geo_ge_2p15": vb["geo_monthly_pct"] >= 2.15,
            "4_recent_a_and_stress_pos": vb["A_geo_monthly_pct"] > 0.0 and vs["geo_monthly_pct"] > 0.0,
            "5_ulcer_lt_q42": vb["ulcer_index_pct"] < 10.14,
        }
        res["checks"][vname] = ch
        res["pass"][vname] = all(ch.values())

    out_path = os.path.join(HERE, "results", "round46.json")
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w") as fh:
        json.dump(res, fh, indent=1)

    for vname in ("S36_qd_carry_blend", "Q42_dualspeed_60_40_carry", "Q46_ic_decay_throttled_s36", "Q46_ic_decay_throttled_q42", "Q46_exp_grad_self_improving"):
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
            f"  [SPOT] {sp_name:34s}: geo={sp_m['geo_monthly_pct']:+.2f}%/mo ({sp_m['annualized_pct']:+.1f}%/yr) "
            f"maxDD={sp_m['max_dd_pct']:+.1f}% meanDD={sp_m['mean_dd_pct']:.2f}% Sharpe={sp_m['sharpe']:.3f} A={sp_m['A_geo_monthly_pct']:+.2f}"
        )


if __name__ == "__main__":
    main()
