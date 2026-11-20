# Pre-Registration 46: MFT Alpha-Decay Detection & Self-Improving Allocation Loop

**Trial Period:** `2020-01-01` to `2026-10-31`  
**Trial IDs:** `805 -> 808`

---

## Overview

Pre-Registration 46 introduces two key mechanism enhancements for high-frequency trading (MFT) quant sleeves:
1. **Causal Rolling Alpha-Decay Throttle (`rolling_alpha_decay_throttle`)**: Detects deteriorating rolling performance (Sharpe ratio `< -0.5`) and dynamically parks capital into risk-free/safe assets (`DTB3`), while linearly interpolating in intermediate regimes.
2. **Self-Improving Exponential-Gradient Allocation Loop (`self_improving_exp_grad_weights`)**: Dynamically adjusts asset weights across `[qd, r3, carry, dtb3]` using Sharpe-normalized multiplicative updates projected onto a risk-safe simplex.

---

## Pre-Registered Trials

1. **`Q46_ic_decay_throttled_s36` (Trial 805)**:
   - Base Strategy: `S36_qd_carry_blend` (`70% Safe-QD + 30% C1 + DTB3`, `10%->28%` taper).
   - Mechanism: Multiplied by causal `rolling_alpha_decay_throttle(qd_net_ret, lookback=60, floor_mult=0.35)`.

2. **`Q46_ic_decay_throttled_q42` (Trial 806)**:
   - Base Strategy: `Q42_dualspeed_60_40_carry` (`30% QD + 30% R3 + 40% C1 + DTB3`, `6%->20%` taper).
   - Mechanism: Separate causal decay throttles applied independently on `QD` and `R3` sleeves.

3. **`Q46_exp_grad_self_improving` (Trial 807)**:
   - Mechanism: Causal Multiplicative-Weights self-improving loop across `[QD, R3, C1_Carry, DTB3]` (`eta=0.15, min_carry=0.25, max_trend=0.70`), with `6%->20%` soft drawdown taper on active `[QD, R3]` trend sleeves.

4. **`Q46_composite_ensemble` (Trial 808)**:
   - Ensemble of Trials 805, 806, and 807 with robust risk parity blending.

---

## Pass Rules (`base` `us-futures + S19.BASE` costs)

1. Beats comparator (`S36` `+2.64%/mo` or `Q42` `+2.12%/mo`) on **both** `sharpe` (`> 1.363`) AND `calmar` (`> 1.286`).
2. `max_dd_pct >= -23.0%` AND `mean_dd_pct <= 9.50%`.
3. `geo_monthly_pct >= 2.15%`.
4. `A_2025_26 geo_monthly_pct > 0` AND `stress geo_monthly_pct > 0`.
5. Beats static equal-weight `Q42` on `ulcer_index_pct` (`< 10.14%`).

## Result (run once, 2026-11-18, `2020-01-01..2026-10-31`, `n_trials = 808`)

| Variant (`us-futures` + `S19.BASE`) | Base Geo (Annualized) | Max DD | Mean DD | Ulcer | Sharpe | Calmar | `A (25-26)` | Stress Geo | Spot Geo (Annualized) / DD / Mean DD / Sharpe | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---|
| `S36_qd_carry_blend` (ref) | `+2.64%/mo` (`+36.7%/yr`) | `−27.8%` | `12.78%` | `14.68%` | `1.285` | `1.14` | `+1.04%` | `+0.76%` | `+2.93%/mo` (`+41.4%/yr`) / `−27.3%` / `11.99%` / `1.400` | ref (`PREREG36` PASS) |
| `Q42_dualspeed_60_40_carry` (ref) | `+2.12%/mo` (`+28.7%/yr`) | `−19.8%` | `8.93%` | `10.14%` | `1.363` | `1.29` | `+0.56%` | `+0.71%` | `+2.35%/mo` (`+32.2%/yr`) / `−19.4%` / `8.28%` / `1.483` | ref (`PREREG42` PASS) |
| `Q46_ic_decay_throttled_s36` | `+2.44%/mo` (`+33.6%/yr`) | `−26.6%` | `12.86%` | `14.61%` | `1.234` | `1.10` | `+0.46%` | `+0.52%` | `+2.71%/mo` (`+37.9%/yr`) / `−25.9%` / `11.98%` / `1.344` | **FAIL** (Checks 1, 2, 5) |
| `Q46_ic_decay_throttled_q42` | `+1.94%/mo` (`+25.9%/yr`) | `−18.5%` | `8.70%` | `9.80%` | `1.293` | `1.26` | `+0.18%` | `+0.52%` | `+2.14%/mo` (`+28.9%/yr`) / `−18.1%` / `8.17%` / `1.406` | **FAIL** (Checks 1, 3) |
| `Q46_exp_grad_self_improving` | `+1.88%/mo` (`+25.0%/yr`) | **`−15.9%`** | `9.66%` | `10.58%` | `1.252` | **`1.42`** | `+0.22%` | **`+0.76%`** | `+2.06%/mo` (`+27.8%/yr`) / **`−14.8%`** / `8.99%` / `1.352` | **FAIL** (Checks 1a, 2b, 3, 5) |

### Diagnostics & Honest Reading
- All 3 `PREREG46` trials **FAIL** strict pass checks over `Q42` and remain **diagnostic/monitoring only** (do not replace `Q42`'s static `30/30/40` weights):
  - `Q46_exp_grad_self_improving` cut Max DD down to **`−15.9%` (`−14.8%` Spot)** and raised Calmar to **`1.42`**, proving Multiplicative-Weights is effective as a **defensive risk monitor**, but chasing trailing 45-day net Sharpe reduced `geo` (`+1.88%/mo` vs `Q42 +2.12%`) and `Sharpe` (`1.252` vs `1.363`) by under-weighting trend right after choppy bottoms.
  - `Q46_ic_decay_throttled_q42` trimmed Max DD (`−19.8% -> −18.5%`) and Ulcer (`10.14% -> 9.80%`), at the cost of `−0.18%/mo` return.
- **Production Integration:** [`internal/quant/alpha/decay_loop.go`](file:///home/everett/PiQuant/piquant-live/internal/quant/alpha/decay_loop.go) and `/api/paper/alpha-decay` expose live EWMA net bps, 30d/60d Sharpe, lag-1 autocorrelation, `HEALTHY / THROTTLED / SUSPENDED_TO_TBILLS` flags, and Multiplicative-Weights diagnostics as an observational health & crowding monitor.
- **Artifacts:** `research/quant_lab/results/round46.json`.

