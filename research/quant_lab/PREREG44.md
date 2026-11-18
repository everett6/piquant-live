# Pre-registration 44: Balancing 1.0x and 2.5x Leverage with Buy-and-Hold Volatility Harvesting for the 60%/yr (+3.99%/mo) Target (784 -> 787, committed before reading real data)

## Overview
- **Author**: Quant Lab
- **Date**: 2026-11-18
- **Scope**: Pre-Registration 44 (Trials 784 -> 787)
- **Data Period**: `2020-01-01` to `2026-10-31`
- **Total Trials**: `n_trials = 787`

## Fixed Trials
1. `Q44_vol_balanced_1x_2p5x_blend`:
   - `70%` Volatility-Balanced `1.0x..2.5x` Safe-QD (`lev_t = clip(0.75 / sigma_20d[t], 1.0, 2.5)` multiplying `1x` QD coherence position when `close > SMA50`, `0` otherwise)
   - `30%` Always-On C1 Carry + `DTB3` on idle cash
   - Soft DD taper (`dd_start=0.08, dd_full=0.28, dd_floor=0.35`)

2. `Q44_bh_vol_harvester_1x_2p5x`:
   - `20%` Always-On Rebalanced Buy-and-Hold (`50/50 BTC+ETH`) Volatility Harvester (daily Shannon's Demon rebalancing against portfolio equity + counter-volatility dip multiplier `bh_mult_t = clip(1.0 + 0.5 * tanh(-ret_5d / max(sigma_20d_daily, 1e-4)), 0.65, 1.35)`)
   - `55%` Vol-Balanced `1.0x..2.5x` Safe-QD (`sigma_target = 0.80`)
   - `25%` C1 Carry + `DTB3`
   - Soft DD taper (`dd_start=0.08, dd_full=0.30, dd_floor=0.35`) applied to active QD sleeve while `20%` BH Harvester stays untapered

3. `Q44_barbell_1x_bh_2p5x_dualspeed`:
   - `25%` `1.0x` Buy-and-Hold Volatility Harvester (`bh_mult_t` in `[0.65, 1.35]`)
   - `50%` `1.0x..2.5x` Vol-Balanced Dual-Speed (`0.5 * QD + 0.5 * R3_FastExit`, `sigma_target = 0.85`)
   - `25%` C1 Carry + `DTB3`
   - Soft DD taper (`dd_start=0.08, dd_full=0.28, dd_floor=0.35`) on the `2.5x` Dual-Speed sleeve

## Pass Rules
A variant PASSES if ALL FIVE hold under `base` costs:
1. `geo_monthly_pct >= 3.40%` (`>= +49%/yr` futures, targeting `>= +3.99%/mo` / `+60%/yr` on Spot+Margin) AND `geo_monthly_pct > S36_qd_carry_blend geo_monthly_pct` (`+2.64%/mo`).
2. `max_dd_pct >= -38.0%` (more than `35` percentage points safer than `QD_2x`'s `−73.1%` and `BH`'s `−77%`, and well inside the `40%` dry-run breaker).
3. `sharpe >= 1.30` AND `calmar >= 1.25` (beats `S36` on both Sharpe `1.285` and Calmar `1.14`).
4. `A_2025_26 geo_monthly_pct > 0` AND `stress geo_monthly_pct > 0`.
5. `ulcer_index_pct <= 22.0%`.
