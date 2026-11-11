"""Generate results/REPORT.md from results/*.json (never hand-edit the report)."""
from __future__ import annotations

import json
import os

HERE = os.path.dirname(os.path.abspath(__file__))
R = os.path.join(HERE, "results")


def load(name):
    p = os.path.join(R, name)
    return json.load(open(p)) if os.path.exists(p) else None


def fmt(x, nd=2):
    return "n/a" if x is None else (f"{x:.{nd}f}" if isinstance(x, (int, float)) else str(x))


def row(cells):
    return "| " + " | ".join(cells) + " |"


def matrix(title, cells, rows, cols, rkey, ckey, vkey, nd=3):
    """TP x SL style matrix: rows = rkey values, cols = ckey values, value = vkey (mean over other params)."""
    out = [f"**{title}**", "", row([f"{rkey} \\ {ckey}"] + [str(c) for c in cols]), row(["---"] * (len(cols) + 1))]
    for r in rows:
        vals = []
        for c in cols:
            xs = [x[vkey] for x in cells if x[rkey] == r and x[ckey] == c and x.get(vkey) is not None]
            vals.append(fmt(sum(xs) / len(xs), nd) if xs else "n/a")
        out.append(row([str(r)] + vals))
    return out + [""]


def round3_section(r3):
    out = ["## Round 3 (PREREG3.md) — wider TP/SL; theory in BREAKEVEN.md", "",
           "### T0 — random entries: does bracket geometry create edge? (discovery, us-futures costs)", ""]
    cells = r3["T0"]["cells"]
    tps = sorted({c["tp"] for c in cells})
    sls = sorted({c["sl"] for c in cells})
    for title, key, nd in (("Realised gross win rate (target vs stop exits)", "win_rate", 3),
                           ("Predicted by theory S/(T+S)", "pred_win_rate", 3),
                           ("Realised minus predicted", "win_minus_pred", 3),
                           ("Mean gross bps/trade (theory: 0)", "mean_gross_bps", 2),
                           ("Mean net bps/trade", "mean_net_bps", 2),
                           ("Net breakeven win rate p* = (S+c)/(T+S)", "net_breakeven_p", 3),
                           ("Realised win rate minus p* (needs > 0 to profit)", "win_minus_breakeven", 3)):
        out += matrix(title, cells, tps, sls, "tp", "sl", key, nd)
    out += [f"Theory supported (>= 90% of cells within 3 pp and 3 bps): {r3['T0']['supported']}", ""]
    for k, label in (("S7_live_signal_wide_brackets", "S7 — live entry signal, wider brackets (1m)"),
                     ("S8_trend_regime_wide_brackets", "S8 — 1h trend regime, wide brackets (5m)")):
        e = r3.get(k)
        if not e:
            continue
        g = e["discovery_grid"]
        tps = sorted({x["tp"] for x in g})
        sls = sorted({x["sl"] for x in g})
        out += [f"### {label}", ""]
        out += matrix("Discovery net Sharpe (mean over other params)", g, tps, sls, "tp", "sl", "sharpe", 2)
        out += matrix("Discovery win rate %", g, tps, sls, "tp", "sl", "win_rate_pct", 1)
        out += matrix("Discovery mean gross bps/trade", g, tps, sls, "tp", "sl", "avg_gross_bps", 1)
        out += [row(["Period", "Months", "Ret %", "Sharpe", "DSR", "Win %", "Trades", "Gross bps", "B&H Sharpe", "Spot Sharpe"]),
                row(["---"] * 10)]
        for h in ("holdout_A", "holdout_B", "holdout_C"):
            hh = e.get(h, {})
            f = hh.get("us-futures", {})
            out.append(row([h, fmt(hh.get("months"), 1), fmt(f.get("total_return_pct"), 1), fmt(f.get("sharpe")),
                            fmt(hh.get("dsr")), fmt(f.get("win_rate_pct"), 1), str(f.get("trades", "-")),
                            fmt(f.get("avg_gross_bps"), 1), fmt(hh.get("buy_hold", {}).get("sharpe")),
                            fmt(hh.get("us-spot", {}).get("sharpe"))]))
        out += ["", f"Best discovery params {json.dumps(e['discovery_best_params'])}, discovery Sharpe "
                f"{fmt(e['discovery_best']['sharpe'])}, PBO {fmt(e['pbo'])}, C sufficient "
                f"{e['holdout_C_sufficient']} -> **{'PASS' if e['pass'] else 'FAIL'}**", ""]
    out += [f"Trials charged in total: {r3['n_trials']}", ""]
    return out


def round4_section(r4):
    out = ["## Round 4 (PREREG4.md) — G1: >=55% win-rate geometry on the live entry", ""]
    g = r4["discovery_grid"]
    tps = sorted({x["tp"] for x in g})
    sls = sorted({x["sl"] for x in g})
    out += matrix("Discovery win rate % (mean over th)", g, tps, sls, "tp", "sl", "win_rate_pct", 1)
    out += matrix("Zero-edge prediction 100*S/(T+S)", g, tps, sls, "tp", "sl", "pred_zero_edge_win_pct", 1)
    out += matrix("Discovery mean net bps/trade", g, tps, sls, "tp", "sl", "mean_net_bps", 1)
    if not r4.get("pick"):
        return out + [f"No pick: {r4['adoption'].get('reason')}", ""]
    out += [f"Pick {json.dumps(r4['pick'])} vs live comparator {json.dumps(r4['comparator'])}, PBO {fmt(r4['pbo'])}", "",
            row(["Holdout", "G1 win %", "G1 net bps/trade", "G1 ret %", "G1 Sharpe", "Live win %", "Live net bps/trade",
                 "Live ret %"]), row(["---"] * 8)]
    for h in ("holdout_A", "holdout_B", "holdout_C"):
        a, b = r4["g1_holdouts"][h], r4["live_holdouts"][h]
        out.append(row([h, fmt(a["win_rate_pct"], 1), fmt(a["mean_net_bps"], 1), fmt(a["total_return_pct"], 1),
                        fmt(a["sharpe"]), fmt(b["win_rate_pct"], 1), fmt(b["mean_net_bps"], 1),
                        fmt(b["total_return_pct"], 1)]))
    ad = r4["adoption"]
    out += ["", f"H-win (>=55% on A, B, C): {ad['h_win']}; net bps better on {ad['net_bps_better_count']}/3; "
            f"return better on {ad['return_better_count']}/3 -> adopt: **{ad['adopt']}**; "
            f"H-profit pass: **{r4['h_profit_pass']}**", f"Trials charged in total: {r4['n_trials']}", ""]
    return out


def main():
    out = ["# Quant lab results (generated by make_report.py)", ""]
    r1 = load("run1.json")
    if r1:
        out += [f"## Round 1 (PREREG.md) — data {r1['data']['start'][:10]} .. {r1['data']['end'][:10]}, "
                f"holdout from {r1['data']['discovery_end'][:10]}", "",
                row(["Strategy", "Best params (discovery)", "Disc. Sharpe", "Median cfg Sharpe", "PBO", "WF Sharpe",
                     "Holdout ret %", "Holdout Sharpe", "Win %", "Trades", "Gross bps/trade", "PASS"]),
                row(["---"] * 12)]
        for k, e in r1["strategies"].items():
            if k.startswith("S0"):
                out.append(row([k, "-", fmt(e["discovery"]["sharpe"]), "-", "-", "-",
                                fmt(e["holdout"]["total_return_pct"], 1), fmt(e["holdout"]["sharpe"]), "-", "-", "-", "-"]))
                continue
            h = e["holdout"]["us-futures"]
            out.append(row([k, json.dumps(e["discovery_best_params"]), fmt(e["discovery_best"]["sharpe"]),
                            fmt(e["discovery_median_sharpe"]), fmt(e["pbo"]), fmt(e["walk_forward_oos"].get("sharpe")),
                            fmt(h["total_return_pct"], 1), fmt(h["sharpe"]), fmt(h.get("win_rate_pct"), 1),
                            str(h.get("trades", "-")), fmt(h.get("avg_gross_bps")), "PASS" if e["pass"] else "FAIL"]))
        out += ["", f"Trials charged so far: {r1['n_trials']}", ""]
    s4 = load("s4.json")
    if s4:
        out += ["## S4 — simulated quantum annealing vs random search (1h trend space, 512 configs)", "",
                row(["Method", "Discovery Sharpe (3 seeds)", "Walk-forward Sharpe", "Holdout pick", "Holdout ret %", "Holdout Sharpe"]),
                row(["---"] * 6)]
        for m in ("sqa", "random"):
            e = s4[m]
            out.append(row([m, ", ".join(fmt(p["discovery_sharpe"]) for p in e["picks"]), fmt(e["walk_forward_oos"]["sharpe"]),
                            json.dumps(e["holdout_pick"]), fmt(e["holdout"]["total_return_pct"], 1), fmt(e["holdout"]["sharpe"])]))
        out += ["", f"Distinct configurations evaluated: {s4['distinct_configs_evaluated']} "
                "(pushes total trials to 586, over the pre-registered 400; DSR uses the true count).", ""]
    r2 = load("round2.json")
    if r2:
        out += [f"## Round 2 (PREREG2.md) — untouched holdout B: {r2['data']['earliest'][:10]} .. 2022-12-16 "
                f"({r2['holdout_B_months']} months)", "", "### Q1 — QHO anharmonic coefficient as a volatility feature", "",
                row(["Period", "n", "R² base", "R² with λ", "R² gain", "λ t-stat"]), row(["---"] * 6)]
        for per, q in r2["Q1"].items():
            out.append(row([per, str(q.get("n")), fmt(q.get("r2_base"), 3), fmt(q.get("r2_with_lambda"), 3),
                            fmt(q.get("r2_gain"), 3), fmt(q.get("lambda_t"))]))
        out += ["", row(["Strategy", "Best params", "Disc. Sharpe", "PBO", "A ret %", "A Sharpe", "A B&H Sharpe",
                         "B ret %", "B Sharpe", "B B&H Sharpe", "PASS"]), row(["---"] * 11)]
        for k in ("S5_vol_managed", "S6_quantum_walk_regime"):
            e = r2.get(k)
            if not e:
                continue
            a, b = e["holdout_A"], e["holdout_B"]
            fa, fb = a.get("us-futures", {}), b.get("us-futures", {})
            out.append(row([k, json.dumps(e["discovery_best_params"]), fmt(e["discovery_best"]["sharpe"]), fmt(e["pbo"]),
                            fmt(fa.get("total_return_pct"), 1), fmt(fa.get("sharpe")), fmt(a.get("buy_hold", {}).get("sharpe")),
                            fmt(fb.get("total_return_pct"), 1), fmt(fb.get("sharpe")), fmt(b.get("buy_hold", {}).get("sharpe")),
                            "PASS" if e["pass"] else "FAIL"]))
        if "S5_vol_managed" in r2:
            out += ["", f"S5 beats buy-and-hold Sharpe on discovery, A and B: "
                    f"{r2['S5_vol_managed'].get('beats_buy_hold_sharpe_everywhere')}"]
        out += ["", f"Trials charged in total: {r2['n_trials']}", ""]
    r3 = load("round3.json")
    if r3:
        out += round3_section(r3)
    r4 = load("round4.json")
    if r4:
        out += round4_section(r4)
    with open(os.path.join(R, "REPORT.md"), "w") as fh:
        fh.write("\n".join(out) + "\n")
    print("\n".join(out))


if __name__ == "__main__":
    main()
