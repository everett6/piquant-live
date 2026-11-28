"""Run PREREG61 once: synthetic calibration of the vol-scaled drawdown kill (+ PREREG60 CUSUM) vs the old rule.

Synthetic Student-t paths only; reads no market data and calls no register(). Writes results/round61.json.
"""
from __future__ import annotations

import json
import os

import numpy as np

import decay_cusum as DC
import run_round60 as R60

HERE = os.path.dirname(os.path.abspath(__file__))
SEED = 61


def first_day(mask: np.ndarray) -> int | None:
    hit = np.flatnonzero(mask)
    return int(hit[0]) + 1 if len(hit) else None


def main() -> None:
    rng = np.random.default_rng(SEED)
    res: dict = {"paths": R60.PATHS, "seed": SEED, "scenarios": {}}
    for name, live_sharpe in R60.SCENARIOS.items():
        old, new, dd_only, limits = [], [], [], []
        for _ in range(R60.PATHS):
            ref = R60.draws(rng, R60.REF_N, R60.REF_SHARPE)
            live = R60.draws(rng, R60.LIVE_N, live_sharpe)
            p = DC.reference_params(ref)
            lim = DC.dd_limit_pct(ref)
            limits.append(lim)
            run_dd = np.minimum.accumulate(R60.drawdown_pct(live))
            d_dd = first_day(run_dd < lim)
            d_cu = first_day(DC.cusum_path(live, p) >= p["h"])
            new.append(min([d for d in (d_dd, d_cu) if d is not None], default=None))
            dd_only.append(d_dd)
            old.append(R60.old_kill_day(live))
        res["scenarios"][name] = {"live_sharpe": live_sharpe, "old": R60.summarize(old),
                                  "new": R60.summarize(new), "dd_limit_only": R60.summarize(dd_only),
                                  "median_dd_limit_pct": round(float(np.median(limits)), 3)}
        print(name, json.dumps(res["scenarios"][name]))
    s = res["scenarios"]
    rules = {
        "1_H_new_kill365_le_20pct": s["H"]["new"]["kill_365"] <= 0.20,
        "2_H_ddlimit_kill730_le_7pct": s["H"]["dd_limit_only"]["kill_730"] <= 0.07,
        "3_N_new_kill730_ge_70pct": s["N"]["new"]["kill_730"] >= 0.70,
        "4_C_new_median_le_120d": (s["C"]["new"]["median_days"] or np.inf) <= 120,
    }
    res["rules"] = rules
    res["pass"] = all(rules.values())
    print("PASS" if res["pass"] else "FAIL", rules)
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", "round61.json"), "w") as f:
        json.dump(res, f, indent=2)


if __name__ == "__main__":
    main()
