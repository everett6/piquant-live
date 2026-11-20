# Pre-registration 40: Multi-Clock 6h `QD + C1` (`Q40_6h_qd_carry`) & Fast-Exit `R3 × QD + C1` (`Q40_r3_fastexit_blend`) (`773 -> 775`, committed before reading real data)

## Why
- **`PREREG30` (`E4_btc_eth_4clock_2x`)** proved that executing `SMA50` across four daily clocks (`00/06/12/18 UTC` 6-hour bars) beats single-clock `00:00 UTC` daily on return (`+8.61%` vs `+8.43%`), drawdown (`−82.3%` vs `−83.5%`), and Sharpe (`1.50` vs `1.48`), eliminating 24-hour close lag while keeping turnover identical per tranche and latency requirements (`6h` interval vs `181 ms` HTTP RTT) zero-disadvantage.
- **`PREREG13` (`R3_btc_eth_fastexit_2x`)** beat `P1` on full return (`+8.58%` vs `+8.50%`), max DD (`−76%` vs `−84%`), worst month (`−36%` vs `−49%`), and bear months (`−4.2%` vs `−8.0%`) by adding `close < SMA20` fast exits and re-entering only when `close > SMA50` and `>= SMA20`.
- **Neither** has ever been combined with **`S36_qd_carry_blend`** (`70% Safe-QD + 30% Always-On C1 Carry + DTB3`, PASS 5/5: `+2.64%/mo`, max DD `−27.8%`, Sharpe `1.285`, Calmar `1.14`, bear `−2.4%`, stress `+0.76%/mo`).

## Two fixed variants (`2` trials: `773 -> 775`, window `2020-01-01..2026-10-31`)
1. **`Q40_6h_qd_carry`:**
   `70%` `4-clock 6h Safe-QD` (`lev = 1.0`, average of `QD(n=50, tau=5)` evaluated on each of the four daily clocks `00/06/12/18 UTC` on 6-hour candles, modulated by `PREREG36`'s `10%->28%` soft DD taper down to `0.40` floor) + `30%` Always-On `C1` funding carry (settled once per UTC day so carry variance is not smoothed across 6h bars) + `DTB3` on idle trend cash (`rate / 100 / 365 / 4` per 6h bar).
2. **`Q40_r3_fastexit_blend`:**
   `70%` `R3 Fast-Exit QD` (`1.0x`, `enter when close > SMA50`, `exit when close < SMA20 or close < SMA50`, `re-enter only when close > SMA50 and close >= SMA20`, ramped by `(1 - exp(-active_age / 5.0))`, modulated by `PREREG36` `10%->28%` soft DD taper) + `30%` Always-On `C1` funding carry + `DTB3` on idle trend cash.

## Rules & Costs (`2` trials: `773 -> 775`)
- Signals warmed up on full candle history before `2020-01-01`.
- **Base cost model:** `us-futures` (`6+1` bps turnover, `0.1 bp/h` funding = `0.6 bps/6h` on active `6h` bar) + `S19.BASE`.
- **Stress cost model:** `us-futures-stress` (`6+1` bps, `1.0 bp/h` funding) + `S19.STRESS`.
- **Operational US Spot check (descriptive only):** `us-spot` (`2+1` bps, `0` funding) on the `70%` Trend sleeve.

## Pass rule (`2` fixed trials: `773 -> 775`, against `S36_qd_carry_blend` rebuilt cleanly)
A variant PASSES only if ALL FIVE hold under `base` (`us-futures + S19.BASE`) costs over `2020-01-01..2026-10-31`:
1. `max_dd_pct >= -32.0%`
2. `geo_monthly_pct > S36_qd_carry_blend geo_monthly_pct` **AND** `calmar > S36_qd_carry_blend calmar`
3. `sharpe > S36_qd_carry_blend sharpe`
4. `bear_mean_monthly_pct >= S36_qd_carry_blend bear_mean_monthly_pct`
5. `A_geo_monthly_pct > 0` **AND** `stress_geo_monthly_pct > 0`

## Prediction (written before reading real data)
- `Q40_6h_qd_carry` should react 4x faster on intra-day trend breaks than `00:00 UTC`-only `S36_qd_carry_blend`, trimming `max_dd` by `~1–3` pts and improving bear months (`~-2.0%` vs `-2.4%`) while preserving or edging up Sharpe (`~1.30–1.38`), though margins over `S36` will be modest (`~1 in 2` pass).
- `Q40_r3_fastexit_blend` should trim bear/worst-day drops via `SMA20` exits, **but** higher whipsaw turnover (`6+1` bps per flip) in sideways months (`2022–2023`, `A 2025-26`) may cost nearly as much as it saves (`~1 in 3` pass).
- One run only after synthetic tests pass.

## Result (run once, 2026-11-14): Both FAIL checks 2–3 against `S36_qd_carry_blend` (`Q40_6h_qd_carry` virtually ties `S36` with slightly lower DD and higher stress; `Q40_r3_fastexit_blend` cuts max DD to −21.0% and bear to −0.7% at the cost of bull upside)

Window `2020-01-01` to `2026-10-31` (`82` complete months: `26` bear, `18` flat, `38` bull).

| Variant (`us-futures` + `S19.BASE`) | %/mo | max DD | Sharpe | Calmar | DSR | worst mo | worst day | 2022 %/mo | A (25–26) | bear / flat / bull | stress %/mo | result |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---|
| `S36_qd_carry_blend` (comparator) | +2.64 | −27.8% | 1.285 | 1.14 | 0.572 | −12.41% | −11.34% (2021-01-21) | −1.35 | +1.04 | −2.4 / +0.0 / +8.0 | +0.76 | – |
| `Q40_6h_qd_carry` | +2.60 | −27.4% | 1.282 | 1.14 | 0.568 | −11.47% | −11.45% (2021-01-21) | −1.38 | +1.01 | −2.2 / −0.3 / +7.9 | +0.81 | **FAIL** (checks 2, 3) |
| `Q40_r3_fastexit_blend` | +2.10 | −21.0% | 1.254 | 1.20 | 0.541 | −6.84% | −6.97% (2021-01-11) | −1.10 | +0.25 | −0.7 / −1.2 / +6.1 | +0.74 | **FAIL** (checks 2, 3) |

Operational check (`us-spot` `2+1 bps, 0 funding` on `70%` Trend + `30%` `S19.BASE` C1 Carry):
- **`S36_qd_carry_blend_spot`:** **`+2.93%/mo`**, max DD **`−27.3%`**, Sharpe **`1.400`**, Calmar `1.29`, `DSR 0.685`, bear/flat/bull `−2.3 / +0.2 / +8.5%`, `A +1.30%/mo`.
- `Q40_6h_qd_carry_spot`: `+2.89%/mo`, max DD `−26.9%`, Sharpe `1.397`, Calmar `1.29`, `DSR 0.682`, bear/flat/bull `−2.1 / −0.1 / +8.3%`, `A +1.27%/mo`.
- `Q40_r3_fastexit_blend_spot`: `+2.34%/mo`, max DD `−20.2%`, Sharpe `1.375`, Calmar `1.39`, `DSR 0.665`, bear/flat/bull `−0.7 / −1.1 / +6.5%`, `A +0.46%/mo`.

**Reading:**
- Moving `S36_qd_carry_blend` to 6-hour 4-clock staggered rebalancing (`Q40_6h_qd_carry`) modestly trimmed worst month (`−12.41% -> −11.47%`), max DD (`−27.8% -> −27.4%`), bear months (`−2.4% -> −2.2%`), and lifted stress (`+0.76% -> +0.81%`) at essentially identical Sharpe (`1.282` vs `1.285`), **confirming clock-independence cleanly** while missing `S36` on geo return by `-0.04%/mo`.
- Combining `R3` (`SMA20` fast exit) with `QD` in `Q40_r3_fastexit_blend` slashed max DD to `−21.0%` (`−20.2%` spot) and bear bleed to `−0.7%/mo`, **but** surrendered `~2%/mo` of bull upside (`+6.1%` vs `+8.0%`) due to early exits and whipsaw re-entries.
- **US Spot confirmation:** simply running `S36_qd_carry_blend`'s `70%` Trend leg on US Spot (`0` funding, `3 bps/side`) rather than paying `0.1 bp/h` futures funding raised return from `+2.64% -> +2.93%/mo`, Sharpe `1.285 -> 1.400`, Calmar `1.14 -> 1.29`, and `A` `+1.04% -> +1.30%/mo` with `−27.3%` max DD. Trials charged: `773 -> 775`.

