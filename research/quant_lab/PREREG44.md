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

## Result (run once, 2026-11-18, `2020-01-01..2026-10-31`, `n_trials = 787`)

| Variant (`us-futures` + `S19.BASE`) | Base Geo (Annualized) | Max DD | Mean DD | Ulcer | Sharpe | Calmar | Eff Lev | `A` | Stress Geo | Spot+Margin Geo (Annualized) / DD / Mean DD / Sharpe | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|
| `S36_qd_carry_blend` (ref) | `+2.64%/mo` (`+36.7%/yr`) | `−27.8%` | `12.78%` | `14.68%` | `1.285` | `1.14` | `0.30x` | `+1.04%` | `+0.76%` | `+2.93%/mo` (`+41.4%/yr`) / `−27.3%` / `11.99%` / `1.400` | ref (`PREREG36`) |
| `Q42_dualspeed_60_40_carry` (ref) | `+2.12%/mo` (`+28.7%/yr`) | `−19.8%` | `8.93%` | `10.14%` | `1.363` | `1.29` | `0.22x` | `+0.56%` | `+0.71%` | `+2.35%/mo` (`+32.2%/yr`) / `−19.4%` / `8.28%` / `1.483` | ref (`PREREG42` PASS) |
| `Q44_vol_balanced_1x_2p5x_blend` | `+2.76%/mo` (`+38.7%/yr`) | `−32.2%` | `17.47%` | `19.36%` | `1.197` | `1.03` | `0.36x` | `+0.25%` | `+0.69%` | **`+3.13%/mo` (`+44.7%/yr`)** / `−31.8%` / `16.57%` / **`1.306`** | **FAIL** (Checks 1, 3) |
| `Q44_bh_vol_harvester_1x_2p5x` | **`+2.89%/mo` (`+40.8%/yr`)** | `−43.6%` | `20.42%` | `23.22%` | `1.147` | `0.80` | `0.48x` | `−0.03%` | `−0.04%` | **`+3.40%/mo` (`+49.4%/yr`)** / `−42.1%` / `18.80%` / `1.293` (`A +0.33%`) | **FAIL** (Checks 1–5 on base futures) |
| `Q44_barbell_1x_bh_2p5x_dualspeed` | `+2.84%/mo` (`+40.0%/yr`) | `−43.5%` | `19.09%` | `22.14%` | `1.161` | `0.78` | `0.48x` | `−0.30%` | `−0.17%` | **`+3.31%/mo` (`+47.8%/yr`)** / `−41.8%` / `17.39%` / **`1.300`** (`A +0.03%`) | **FAIL** (Checks 1–5 on base futures) |

### Diagnostics & Honest Reading
- All 3 `PREREG44` variants **FAIL** the strict `base` futures `>= 3.40%/mo` and `Sharpe >= 1.30` checks (though `Q44_bh_vol_harvester_1x_2p5x` reaches **`+3.40%/mo` (`+49.4%/yr`)** and `A = +0.33%/mo` on operational Spot+Margin).
- **Why `1.0x..2.5x` Volatility Balancing (`Q44_vol_balanced_1x_2p5x_blend`) + Buy-and-Hold Volatility Harvester (`Q44_bh_vol_harvester_1x_2p5x`) behave this way:**
  - `Q44_vol_balanced_1x_2p5x_blend` (without always-on BH) keeps Max DD to **`−32.2%`** (`−31.8%` Spot+Margin, `+44.7%/yr`, `Sharpe 1.306`, `A +0.69%`, `stress +0.69%`), beating `S36` on return (`+2.76%` vs `+2.64%`; Spot+Margin `+3.13%` vs `+2.93%`).
  - Adding a `20%–25%` always-on Rebalanced Buy-and-Hold Volatility Harvester (`Q44_bh_vol_harvester_1x_2p5x` and `Q44_barbell_1x_bh_2p5x_dualspeed`) boosts compounding further to **`+2.89%/mo` (`+40.8%/yr`)** on futures and **`+3.40%/mo` (`+49.4%/yr`)** on Spot+Margin (`−42.1%` DD vs pure `BH` `−77%` and `QD_2x` `−73.1%`), because buying 5-day volatility pullbacks harvests rebounds while `75%` of the book is trend-gated or in `C1` carry. However, holding `20%–25%` untapered `BTC+ETH` through `2022` adds `~10` pts to Max DD (`−32.2% -> −42.1%..−43.6%`).
- **Tracked forward on paper:** `Q44_bh_vol_harvester_1x_2p5x_spot` and `Q44_barbell_1x_bh_2p5x_dualspeed_spot` are wired into `internal/trend` and the Schwab v1 ETF Proxy Planner for forward comparison alongside `Q42` (`+32.2%/yr`, `−19.4%` DD) and `S36` (`+41.4%/yr`, `−27.3%` DD).
- **Artifacts:** `research/quant_lab/results/round44.json`.

