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
