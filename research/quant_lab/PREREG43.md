# Pre-registration 43: Merging `PREREG42` Expected-Drawdown Control with `PREREG38` Leader Rotation & Dual-Speed Trend (`781 -> 784`, committed before reading real data)

## Why
- **`PREREG42` (`PASS 2/3`):** `Q42_dualspeed_60_40_carry` (`30% QD + 30% R3_FastExit_QD + 40% C1 + DTB3`, `6%->20%` taper) and `Q42_balanced_50_50_carry` (`50% QD + 50% C1 + DTB3`) cut everyday Expected Drawdown (`mean_dd_pct`) by **`30%–32%`** (`12.78% -> 8.73%–8.93%`), Ulcer Index by **`~31%`**, and Max DD below **`20%`** (`−19.8%`, Spot `−19.4%`), while improving Sharpe (`1.363 / 1.396`; Spot `1.483 / 1.513`) and Calmar (`1.29 / 1.28`; Spot `1.46 / 1.43`).
- **`PREREG38` (`Q38_bull_leader_blend`):** Long-only, `BTC > SMA50` gated Top-4 inverse-vol Leader Rotation (`16`-coin universe, Monday rebalance + mid-week `SMA50` stop-out into `DTB3`) beat `S36_qd_carry_blend` on full geo return (`+2.85%` vs `+2.64%`; Spot `+3.14%`), Sharpe (`1.377` vs `1.285`; Spot `1.493`), DSR (`0.663`), bull months (`+8.7%` vs `+8.0%`), and stress return (`+1.06%`), failing only the arbitrary `+2.0%/mo` bull margin.
- **Unexplored combination (`3` trials: `781 -> 784`):** Can combining **`PREREG42`'s early `6% -> 20%` taper, `R3_FastExit`, and elevated `C1` carry/DTB3 buffer** with **BTC-gated Top-4 Leaders** or **70/30 Dual-Speed Trend** recover **`+2.35%..+2.85%/mo`** compounding (`Spot +2.6%..+3.1%`) while keeping Expected Drawdown shallow (`mean_dd_pct <= 10.5%`, `max_dd_pct >= -24.5%`) and beating `Q42` on BOTH Sharpe and Calmar?

## Three Fixed Strategies (`3` trials: `781 -> 784`, window `2020-01-01..2026-10-31`)
All three use strictly causal `DTB3` on idle trend/leader cash (`max(0, 1 - carry_share - active_crypto_notional)`), `C1` BTC+ETH carry (`S19`), total portfolio gross `<= 1.0x`, and `PREREG42` soft DD taper (`dd_start = 0.06`, `dd_full = 0.20`, `dd_floor = 0.40`):

1. **`Q43_dualspeed_70_30_carry` (`35% Safe-QD + 35% R3_FastExit_QD + 30% C1 Carry + DTB3`):**
   - Exact midpoint between `S36_qd_carry_blend` (`70/30`) and `Q42_dualspeed_60_40_carry` (`60/40`), keeping `70%` trend weight via the dual-speed `50/50` mix of `QD(1x)` and `R3_FastExit_QD(1x)` with `6% -> 20%` soft DD taper.
2. **`Q43_dualspeed_leader_65_35` (`25% Safe-QD + 20% R3_FastExit_QD + 20% BTC-Gated Top-4 InvVol Leaders + 35% C1 Carry + DTB3`):**
   - Keeps `35%` `C1` carry cushion and combines `45%` asymmetric Dual-Speed `QD/R3` (`25/45` `QD`, `20/45` `R3_FastExit`) with `20%` `S38.leader_basket_weights(closes_all)`.
3. **`Q43_macro_bull_leader_barbell` (`30% Safe-QD + 30% BTC-Gated Top-4 InvVol Leaders + 40% C1 Carry + DTB3`):**
   - Replaces `R3_FastExit` in the `60/40` `PREREG42` frame with `30%` `S38.leader_basket_weights` (`100%` `DTB3` whenever `BTC <= SMA50`), preserving a full `40%` `C1` carry cushion and capping max trend+leader exposure at `60%`.

## Pass Rule (`3` fixed trials: `781 -> 784`, under `base` (`us-futures + S19.BASE`) costs)
A variant **PASSES** only if ALL FIVE hold under `base` costs over `2020-01-01..2026-10-31`:
1. **Return lift over `Q42`:** `geo_monthly_pct >= 2.35%` (clearly lifts over `Q42_dualspeed_60_40_carry` `+2.12%/mo` toward `S36`).
2. **Hard & Owner Max DD ceiling:** `max_dd_pct >= -24.5%` (strictly inside `25%` owner ceiling and beats `S36` `−27.8%`).
3. **Expected Drawdown preservation:** `mean_dd_pct <= 10.50%` **AND** `ulcer_index_pct <= 12.20%` (maintains `>= 17–18%` cut below `S36`'s `12.78%` / `14.68%`).
4. **Efficiency beats `Q42_dualspeed_60_40_carry`:** `sharpe > 1.363` **AND** `calmar > 1.286`.
5. **Recency & Stress:** `A_2025_26 geo_monthly_pct > 0` **AND** `stress geo_monthly_pct > 0`.

## Prediction (written before running on real data)
- `Q43_dualspeed_70_30_carry`: raising trend allocation from `60%` to `70%` (`35% QD + 35% R3`) should restore geo return to `~+2.35%..+2.50%/mo` (`Spot ~+2.6%`), with max DD around `−21%..−23.5%` and `mean_dd_pct` near `~9.8%..10.6%`; Calmar and `mean_dd_pct <= 10.5%` will be tight (`~1 in 3`).
- `Q43_dualspeed_leader_65_35`: adding `20%` Monday-rotated InvVol Leaders into a `65/35` Dual-Speed+Carry frame should add cross-sectional bull return (`+2.45%..+2.70%/mo`, Spot `~+2.75%..+3.0%`) with minimal bear leakage because leaders exit to `DTB3` whenever `BTC <= SMA50` (`mean_dd` `~9.2%..10.2%`, max DD `~−21%..−23.5%`, Sharpe `~1.42–1.55`); strongest overall candidate (`~1 in 2`).
- `Q43_macro_bull_leader_barbell`: `40%` `C1` carry + `30% Safe-QD + 30% Leaders` (`60%` trend+leader cap) could achieve both high Sharpe (`~1.45+`) and shallow drawdown (`−20%..−24%`, `mean_dd ~9.5%`), though higher single-name leader volatility than `R3_FastExit` may push `2021-01` or `2022` DD slightly above `Q42_dualspeed` (`~1 in 3`).
- One run only after synthetic unit tests pass.

## Result (run once, 2026-11-17, `2020-01-01..2026-10-31`, `n_trials = 784`)

| Variant (`us-futures` + `S19.BASE`) | Base Geo | Max DD | Mean DD | Ulcer | CDaR95 | Sharpe | Calmar | DSR784 | Bear | `A` | Stress Geo | Spot Geo / DD / Mean DD / Sharpe | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|
| `S36_qd_carry_blend` (ref) | `+2.64%/mo` | `−27.8%` | `12.78%` | `14.68%` | `26.2%` | `1.285` | `1.14` | `0.570` | `−2.45%` | `+1.04%` | `+0.76%` | `+2.93%` / `−27.3%` / `11.99%` / `1.400` | ref (`PREREG36`) |
| `Q42_dualspeed_60_40_carry` (ref) | `+2.12%/mo` | `−19.8%` | `8.93%` | `10.14%` | `18.2%` | `1.363` | `1.29` | `0.649` | `−1.20%` | `+0.56%` | `+0.71%` | `+2.35%` / `−19.4%` / `8.28%` / `1.483` | ref (`PREREG42` PASS) |
| `Q43_dualspeed_70_30_carry` | `+2.24%/mo` | `−21.6%` | `10.47%` | `11.72%` | `20.0%` | `1.290` | `1.25` | `0.576` | `−1.33%` | `+0.55%` | `+0.69%` | `+2.51%` / `−21.2%` / `9.72%` / `1.412` | **FAIL** (Checks 1, 4) |
| `Q43_dualspeed_leader_65_35` | `+2.43%/mo` | `−22.9%` | `11.41%` | `12.93%` | `21.5%` | `1.413` | `1.27` | `0.695` | `−1.63%` | `+0.19%` | `+0.92%` | `+2.68%` / `−22.6%` / `10.91%` / `1.532` | **FAIL** (Checks 3, 4b) |
| `Q43_macro_bull_leader_barbell` | `+2.53%/mo` | `−24.2%` | `12.22%` | `13.84%` | `22.8%` | `1.438` | `1.26` | `0.717` | `−1.97%` | `+0.07%` | `+1.00%` | `+2.78%` / `−23.8%` / `11.74%` / `1.552` (`0.808` DSR) | **FAIL** (Checks 3, 4b) |

### Diagnostics & Honest Reading
- All 3 `PREREG43` trials **FAIL** strict joint pass criteria and remain research-only (`PREREG43` closed):
  - `Q43_dualspeed_70_30_carry` held expected DD (`10.47%`) and Ulcer (`11.72%`) under ceiling, but missed `>= +2.35%/mo` (`+2.24%`) and `Q42` Sharpe/Calmar (`1.290 < 1.363`, `1.248 < 1.287`).
  - `Q43_dualspeed_leader_65_35` (`+2.43%/mo`, Spot `+2.68%`, `maxDD −22.9%`) and `Q43_macro_bull_leader_barbell` (`+2.53%/mo`, Spot `+2.78%`, `maxDD −24.2%`) both beat `Q42` on geo return, **Sharpe** (`1.413 / 1.438`; Spot `1.532 / 1.552`), `DSR_784` (`0.695 / 0.717`; Spot **`0.808`**), and **stress** (`+0.92% / +1.00%`), while capping Max DD under `25%` — **but** cross-sectional alt dispersion kept `2022–2023` underwater depths higher (`mean_dd_pct = 11.41% / 12.22% > 10.50%`), trimming Calmar to `1.27 / 1.26` (just below `Q42`'s `1.29`).
- **Pareto conclusion:** `Q42_dualspeed_60_40_carry` and `Q42_balanced_50_50_carry` remain undefeated for **Expected Drawdown** (`8.73%–8.93%` mean DD, `−19.8%` Max DD, `1.29` Calmar), while `S36_qd_carry_blend` (`+2.64%` / Spot `+2.93%`) remains undefeated for full `PREREG36` compounding under strict `2025–26` (`A = +1.04%` / Spot `+1.30%`) persistence.
- **Artifacts:** `research/quant_lab/results/round43.json`.

