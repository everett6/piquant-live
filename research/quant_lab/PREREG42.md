# Pre-registration 42: Improving **Expected Drawdown** (`mean_dd`, Ulcer Index, CDaR95, Underwater Duration) on `S36_qd_carry_blend` (`778 -> 781`, committed before reading real data)

## Why
- **`S36_qd_carry_blend` (`PREREG36` PASS 5/5, `+2.64%/mo` base, `+2.93%/mo` US spot, max DD `−27.8%`, Sharpe `1.285`, Calmar `1.14`)** solves the hard `50%` max-drawdown ceiling, **but its soft taper only starts at `dd >= 10%`**, leaving everyday expected drawdown, Ulcer Index, and recovery time under water unoptimized.
- **`PREREG40`** proved `R3` (`SMA20` fast exit) slashes max DD to `−21.0%` and bear months to `−0.7%`, and **`PREREG19`** showed `C1` carry has `−0.6%` max DD while earning `+0.66%/mo` (`+0.41%` stress).
- **Goal (`3` fixed trials: `778 -> 781`):** Minimize **Expected Drawdown** (`mean_dd`, `ulcer_index`, `cdar_95`, `max_underwater_days`) and worst-case Max DD (`<= 23%`) simultaneously, while improving **Sharpe** and **Calmar** over `S36_qd_carry_blend` without any option premium bleed (`PREREG26` closed) or leverage creep (`<= 1.0x` gross trend notional).

## Three Fixed Strategies (`3` trials: `778 -> 781`, window `2020-01-01..2026-10-31`)
All strategies share the `PREREG36` causal `DTB3` accrual on idle trend cash (`max(0, trend_share * (1 - active_pos))`) and always-on `C1` funding carry (`S19.coin_returns(..., "C1")` across BTC+ETH).

1. **`Q42_volcap_s36_blend` (`70%` One-Sided Downside Vol-Capped Safe-QD + `30%` C1 Carry + `DTB3`):**
   - Uses `S35.coherence_positions(daily, lev=1.0, n=50, tau=5)` scaled by a strictly causal one-sided volatility cap: `sigma_20d[t] = rolling_20d_std(close_ret)[t] * sqrt(365)` (warmed up prior to `2020-01-01`), `vol_cap[t] = min(1.0, 0.45 / max(1e-6, sigma_20d[t]))`. Never exceeds `1.0x` (`PREREG39` tested `1.35x` upside levering; this is purely protective downward scaling when 20d vol > `45%/yr`).
   - Uses an earlier gentle drawdown taper on realized blend drawdown: `dd_start = 0.06`, `dd_full = 0.20`, `floor = 0.40`.

2. **`Q42_dualspeed_60_40_carry` (`60%` Dual-Speed Safe-QD (`50% QD + 50% R3_FastExit_QD`) + `40%` Always-On C1 Carry + `DTB3`):**
   - Splits the `60%` trend sleeve evenly between standard `QD(1x)` (patient trend hold) and `R3_FastExit_QD(1x)` (`SMA20` fast defense, `S40.r3_fastexit_qd_positions`), capturing half the fast bear protection without full early-exit regret in bulls, while lifting the `−0.6%` DD `C1` carry cushion from `30%` to `40%`.
   - Applies the earlier soft DD taper (`dd_start = 0.06`, `dd_full = 0.20`, `floor = 0.40`) on the `60%` trend block.

3. **`Q42_balanced_50_50_carry` (`50%` Safe-QD + `50%` Always-On C1 Carry + `DTB3`):**
   - Equal `50/50` barbell between `Safe-QD(1x)` (earlier taper `0.06 -> 0.20`, `0.40` floor) and always-on `C1` funding carry, targeting single-digit/low-teens max DD and ultra-shallow expected drawdown (`<= 2%` mean DD) with strong recovery speed.

## Drawdown Metrics & Costs (`base` futures+S19.BASE, `stress`, and operational `us-spot`)
Across `2020-01-01..2026-10-31` (`82` complete months):
- **Classic metrics:** `geo_monthly_pct`, `max_dd_pct`, `sharpe`, `calmar`, `deflated_sharpe` (`n_trials = 781`), `bear/flat/bull`, `2022`, `A (2025-07..2026-10)`, `stress`.
- **Expected Drawdown metrics (new first-class criteria):**
  - `mean_dd_pct`: average daily peak-to-trough drawdown `%` (`E[DD_t]`, `>= 0`).
  - `ulcer_index_pct`: root-mean-square daily drawdown `sqrt(E[DD_t^2])` (penalizes deep/prolonged underwater periods).
  - `dd_p95_pct` and `cdar_95_pct`: 95th-percentile daily drawdown depth and mean of worst `5%` drawdowns (`Conditional Drawdown-at-Risk`).
  - `max_underwater_days` and `mean_recovery_days`: longest streak below prior all-time peak and mean days per finished drawdown episode.

## Pass Rule (`3` fixed trials: `778 -> 781`, under `base` costs vs `S36_qd_carry_blend`)
A variant **PASSES** only if ALL FIVE hold under `base` (`us-futures + S19.BASE`) costs:
1. **Expected Drawdown improvement:** `mean_dd_pct <= 0.75 * S36_mean_dd_pct` **AND** `ulcer_index_pct <= 0.80 * S36_ulcer_index_pct`.
2. **Max Drawdown improvement:** `max_dd_pct >= -23.0%` **AND** `max_dd_pct > S36 max_dd_pct`.
3. **Efficiency improvement:** `sharpe > S36 sharpe` **AND** `calmar > S36 calmar`.
4. **Compounding & Bear defense:** `geo_monthly_pct >= 1.75%` **AND** `bear_mean_monthly_pct > S36 bear_mean_monthly_pct`.
5. **Recency & Stress:** `A_2025_26 geo_monthly_pct > 0` **AND** `stress geo_monthly_pct > 0`.

## Prediction (written before running on real data)
- `Q42_volcap_s36_blend`: capping 20d volatility > `45%/yr` will trim post-peak whipsaws (`mean_dd` `-15%` to `-25%` better than `S36`, max DD around `~−21%..−25%`), though in `2020–21` early high-vol breakouts (`vol > 45%`) it may trim some bull return (`~+2.1%..+2.5%/mo`). Roughly `~1 in 3` to clear both the `25%` `mean_dd` cut and `>= -23%` max DD.
- `Q42_dualspeed_60_40_carry`: blending `30% QD + 30% R3_FastExit + 40% C1` is structurally built for this exact target: expected drawdown (`mean_dd` & `ulcer_index`) should drop **`30–40%`**, max DD lands around **`−16%..−20%`**, Calmar jumps into `1.3–1.6`, and bear months stay near `−1.0%/mo` while keeping `+1.9%..+2.2%/mo` base (`~+2.2%..+2.4%/mo` Spot). Strong candidate (`~1 in 2`).
- `Q42_balanced_50_50_carry`: lowest expected drawdown and highest Sharpe (`~1.6–1.9`), likely max DD around **`−16%..−19%`**, with geo return around **`+1.8%..+2.1%/mo`** base (`+2.0%..+2.3%/mo` Spot) — watch check 4 (`>= 1.75%`) under futures funding (`~1 in 2`).
- One run only after synthetic unit tests pass.

## Result (run once, 2026-11-15, `2020-01-01..2026-10-31`, `n_trials = 781`)

| Variant | Base Geo | Max DD | Mean DD | Ulcer | DD P95 | CDaR95 | Max UW | Mean Rec | Sharpe | Calmar | DSR781 | Bear | `A` | Stress Geo | Spot Geo / DD / Sharpe | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|
| `S36_qd_carry_blend` (ref) | `+2.64%/mo` | `−27.8%` | `12.78%` | `14.68%` | `23.89%` | `26.21%` | `792d` | `39.6d` | `1.285` | `1.14` | `0.570` | `−2.45%` | `+1.04%` | `+0.76%` | `+2.93%` / `−27.3%` / `1.400` | ref (`PREREG36` PASS) |
| `Q42_volcap_s36_blend` | `+1.94%/mo` | `−21.5%` | `9.51%` | `11.06%` | `19.01%` | `20.35%` | `756d` | `36.9d` | `1.359` | `1.09` | `0.645` | `−1.54%` | `+0.71%` | `+0.60%` | `+2.18%` / `−21.1%` / `1.489` | **FAIL** (Check 3: Calmar `1.09 < 1.14`) |
| `Q42_dualspeed_60_40_carry` | `+2.12%/mo` | `−19.8%` | `8.93%` | `10.14%` | `15.77%` | `18.20%` | `831d` | `39.5d` | `1.363` | `1.29` | `0.650` | `−1.20%` | `+0.56%` | `+0.71%` | `+2.35%` / `−19.4%` / `1.483` | **PASS (5/5)** |
| `Q42_balanced_50_50_carry` | `+2.12%/mo` | `−19.9%` | `8.73%` | `10.11%` | `16.81%` | `18.69%` | `783d` | `38.1d` | `1.396` | `1.28` | `0.680` | `−1.58%` | `+0.82%` | `+0.68%` | `+2.33%` / `−19.5%` / `1.513` | **PASS (5/5)** |

### Diagnostics & Honest Reading
- **`Q42_dualspeed_60_40_carry` and `Q42_balanced_50_50_carry` both PASS all 5 pre-registered checks**, cutting Expected Drawdown (`mean_dd_pct`) by **`30.1%–31.7%`** (`12.78% -> 8.93% / 8.73%`), Ulcer Index by **`~31%`** (`14.68% -> 10.14% / 10.11%`), CDaR95 from **`26.2%` to `18.2%–18.7%`**, and worst-case Max Drawdown below `20%` (`−19.8%` / `−19.9%` futures, `−19.4%` / `−19.5%` spot) while improving both Sharpe (`1.285 -> 1.363 / 1.396`; spot `1.400 -> 1.483 / 1.513`) and Calmar (`1.14 -> 1.29 / 1.28`; spot `1.29 -> 1.46 / 1.43`).
- **`Q42_dualspeed_60_40_carry` vs `Q42_balanced_50_50_carry`:**
  - `Q42_dualspeed_60_40_carry` gives stronger bear & tail-month cushioning (`worst_month −6.47%` vs `S36 −12.41%` and `balanced −8.64%`; bear mean `−1.20%/mo`, stress `+0.71%/mo`, Calmar `1.29` / spot `1.46`).
  - `Q42_balanced_50_50_carry` gives the lowest expected drawdown (`8.73%` mean DD, `10.11%` Ulcer, `8.22%` spot mean DD), highest Sharpe (`1.396`, spot `1.513`), positive flat months (`+0.19%/mo`, spot `+0.34%`), and stronger recent `A (2025–26)` compounding (`+0.82%/mo`, spot `+1.00%/mo`).
- **`Q42_volcap_s36_blend` fails Check 3** despite cutting max DD to `−21.5%` and Sharpe to `1.359`: capping 20d volatility at `45%/yr` clips upside breakouts enough (`+2.64% -> +1.94%/mo`) that Calmar drops slightly below `S36` (`1.09 < 1.14`).
- **Artifacts:** `research/quant_lab/results/round42.json`.

