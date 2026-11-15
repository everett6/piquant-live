# Pre-registration 39: Quiet-Bull Vol-Calm Scaling + Expanded Spot/Leader Allocation on `S36_qd_carry_blend` (`771 -> 773`, committed before round-39 reads any real data)

## Why
- **`PREREG38` (`Q38_bull_leader_blend`)** beat `S36_qd_carry_blend` on geo return (`+2.85%` vs `+2.64%/mo`), Sharpe (`1.377` vs `1.285`), DSR (`0.663` vs `0.572`), flat (`+0.2%` vs `+0.0%`), bull (`+8.7%` vs `+8.0%`), and stress (`+1.06%` vs `+0.76%`), reaching **`+3.14%/mo`, Sharpe `1.493`, DSR `0.766`, DD `−29.6%`** on US Spot, **but missed the strict `+2.0%/mo` bull margin (`+8.7%` vs `+10.0%`)** because `30%` remained parked in `C1` carry (`+0.9%/mo` in bull runs) leaving only `70%` in directional sleeves.
- **Meanwhile `Q38_kuramoto_booster` proved** that boosting leverage late (`breadth >= 0.70`) amplifies blowoff-top crashes (`2021-01-21` `−18.7%`).
- **Two ping-safe daily hypotheses tested here (`2` fixed trials: `771 -> 773`):**
  1. **`Q39_volcalm_qd_carry` (`80%` Vol-Calm `QD` + `20%` Always-On `C1` Carry + `DTB3`):**
     Instead of boosting late when breadth peaks (`Q38_kuramoto_booster`) or targeting uncapped volatility (`PREREG11 P2`, `-88%` DD), scale each coin's `QD_1x` target at close `t` by a strictly causal **Vol-Calm Factor** clipped to `[0.50, 1.35]`:
     - `ann_vol20 = std_20(daily_ret) * sqrt(365)`
     - `v_mult = clip(0.55 / max(ann_vol20, 0.25), 0.50, 1.35)`
     Quiet bull climbs (`ann_vol20 <= 41%`) earn up to `1.35x` (`1.08x` portfolio weight); chaotic blowoff tops (`ann_vol20 >= 110%`) cut exposure to `0.50x` *before* `SMA50` breaks; `10%->28%` soft DD taper and `20%` always-on `C1` carry + `DTB3` guard drawdowns.
  2. **`Q39_volcalm_leader_blend` (`40%` Vol-Calm `QD` + `40%` `S38` Trend-Gated Top-4 Leader Basket + `20%` Always-On `C1` Carry + `DTB3`):**
     Splits the `80%` directional budget equally between `40%` Vol-Calm `QD` (`BTC+ETH`) and `40%` `Q38` long-only `BTC`-gated Top-4 inverse-vol Leader Basket (`16` coins, `DTB3` when `BTC <= SMA50`), plus `20%` always-on `C1` carry.

## Rules & Costs (`2` fixed trials: `771 -> 773`, window `2020-01-01..2026-10-31`)
- Same warmup, causality, and `us-futures + S19.BASE` / `us-futures-stress + S19.STRESS` cost framework as `PREREG36/38`, plus descriptive `us-spot` up to `1.0 - carry_share = 0.80` NAV (`2+1` bps, `0` funding) + `us-futures` on excess `> 0.80` NAV.

## Pass rule (`2` trials: `771 -> 773`, against `S36_qd_carry_blend` rebuilt cleanly in the same run)
A variant PASSES only if ALL FIVE hold under `base` (`us-futures + S19.BASE`) costs over `2020-01-01..2026-10-31`:
1. `max_dd_pct >= -40.0%`
2. `geo_monthly_pct >= S36_qd_carry_blend + 0.50` **AND** `bull_mean_monthly_pct >= S36_qd_carry_blend + 1.50`
3. `sharpe > S36_qd_carry_blend`
4. `bear_mean_monthly_pct >= S36_qd_carry_blend - 0.50`
5. `A_geo_monthly_pct > 0` **AND** `stress_geo_monthly_pct > 0`

## Prediction (written before reading real data)
- Shrinking from `1.0x -> 0.50x` in high-volatility tops while expanding directional spot budget (`70% -> 80%`) gives `Q39_volcalm_leader_blend` a realistic shot (`~1 in 2`) to surpass `+3.14%/mo` in base (`+3.5%/mo+` on spot) with `DD <= -35%` and Sharpe `> 1.35`, though `Q39_volcalm_qd_carry` may pay extra daily turnover/funding (`0.1 bp/h` futures) from continuous `v_mult` rebalancing (`~1 in 3`).
- One run only after synthetic tests pass.

## Result (run once, 2026-11-14): Both FAIL pre-registered pass checks (`Q39_volcalm_qd_carry` fails checks 2–3; `Q39_volcalm_leader_blend` fails checks 2 & 5 (`A < 0` on futures))

Window `2020-01-01` to `2026-10-31` (`82` complete months: `26` bear, `18` flat, `38` bull).

| Variant (`us-futures` + `S19.BASE`) | %/mo | max DD | Sharpe | Calmar | DSR | worst mo | worst day | 2022 %/mo | A (25–26) | bear / flat / bull | stress %/mo | result |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---|
| `S36_qd_carry_blend` (comparator) | +2.64 | −27.8% | 1.285 | 1.14 | 0.572 | −12.41% | −11.34% (2021-01-21) | −1.35 | +1.04 | −2.4 / +0.0 / +8.0 | +0.76 | – |
| `Q39_volcalm_qd_carry` | +2.49 | −30.1% | 1.261 | 0.99 | 0.548 | −12.07% | −9.26% (2020-09-03) | −1.57 | +0.77 | −2.5 / −0.5 / +8.0 | +0.71 | **FAIL** (checks 2, 3) |
| `Q39_volcalm_leader_blend` | +2.95 | −32.6% | 1.362 | 1.09 | 0.649 | −13.73% | −9.63% (2021-01-21) | −1.75 | −0.13 | −2.8 / −0.0 / +9.3 | +1.12 | **FAIL** (checks 2, 5: `A = −0.13`) |

Operational check (`us-spot` `2+1 bps, 0 funding` up to `0.80` NAV + `us-futures` on margin > `0.80`):
- `Q39_volcalm_qd_carry_spot_margin`: `+2.83%/mo`, max DD `−29.7%`, Sharpe `1.392`, Calmar `1.14`, `DSR 0.679`, bear/flat/bull `−2.4 / −0.2 / +8.6%`, `A +1.17%/mo`.
- `Q39_volcalm_leader_blend_spot_margin`: `+3.28%/mo`, max DD `−32.1%`, Sharpe `1.482`, Calmar `1.22`, `DSR 0.757`, bear/flat/bull `−2.7 / +0.2 / +9.9%`, `A +0.16%/mo`.

**Reading:**
- Inverse 20d realized-vol scaling (`0.50x..1.35x`) cut `QD` during volatile bull continuations and added daily turnover, losing to plain `S36_qd_carry_blend` on return (`+2.49%` vs `+2.64%`), Sharpe (`1.261` vs `1.285`), and DD (`−30.1%` vs `−27.8%`).
- Expanding the Leader Basket from `30%` to `40%` while putting Vol-Calm `QD` at `40%` pushed bull months up (`+9.3%` futures / `+9.9%` spot), **but** eroded `2025–2026` (`A = −0.13%/mo` futures, `+0.16%` spot vs `S36`'s `+1.04%`), failing checks 2 and 5. Trials charged: `771 -> 773`.

