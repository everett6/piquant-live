# PREREG51 — BTC + ETH + SOL Inverse-Vol-Parity Dual-Speed Trend Rotation (US Spot)

**Date:** 2026-11-21
**Window:** `2020-01-01 .. 2026-11-01` (`SOL.csv` enters causally after `2021-06-17` + 50-day SMA warmup)
**Venue / Costs:** Strictly `US_SPOT` (`taker_bps = 2.0`, `slip_bps = 1.0`, `funding_bps_h = 0.0`)
**Multiple-testing budget:** `n_trials = 812` (cumulative prior `810` + `2` variants evaluated here)

## Hypothesis
Adding `SOL` as a third trend asset via causal inverse-20-day-volatility weighting (`BTC + ETH + SOL`) inside the `60%` dual-speed trend sleeve (`50% Safe-QD + 50% R3-FastExit-QD`) paired with `40%` always-on `C1` carry (`DTB3` on idle cash, `dd_start=0.06, dd_full=0.20, dd_floor=0.40`) improves geometric monthly return and Sharpe over `Q42_dualspeed_60_40_carry_spot` (`BTC + ETH`) without inflating max drawdown.

## Variants
1. **`Q42_dualspeed_60_40_carry_spot` (Benchmark):** `60%` equal-weight `(BTC, ETH)` dual-speed trend + `40%` `C1` carry + `DTB3`, `US_SPOT`.
2. **`Q51_btc_eth_sol_volparity_spot` (Candidate):** `60%` inverse-20d-vol-weighted `(BTC, ETH, SOL)` dual-speed trend + `40%` `C1` carry + `DTB3`, `US_SPOT`. Before `SOL` has 50 daily closes (`2021-06-17` + 50d warmup), weights normalize across `BTC` and `ETH`.

## Pre-Registered Pass Bar (All 5 Must Hold)
1. `Q51.geo_monthly_pct >= Q42.geo_monthly_pct + 0.10`
2. `Q51.sharpe > Q42.sharpe`
3. `Q51.max_dd_pct >= Q42.max_dd_pct - 1.0`
4. `Q51.A_geo_monthly_pct > 0.0` (2025–2026 unseen recent regime positive)
5. `Q51.deflated_sharpe_812 >= 0.80`

## Results — **PASS (5/5)**

Executed via `run_round51.py` (`results/round51.json`), `sol_active_days = 1953`, mean weights when SOL active: `BTC = 0.4318`, `ETH = 0.3249`, `SOL = 0.2432`.

| Variant | Geo/mo | Max DD | Mean DD | Ulcer | Sharpe | Calmar | A (2025-26) | DSR (812) | Verdict |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `Q42_dualspeed_60_40_carry_spot` | `+2.351%` | `-19.37%` | `8.28%` | `9.51%` | `1.483` | `1.457` | `+0.764%` | `0.7552` | ref |
| `Q51_btc_eth_sol_volparity_spot` | **`+2.641%`** | **`-19.27%`** | **`7.83%`** | **`9.13%`** | **`1.670`** | **`1.645`** | `+0.305%` | **`0.8843`** | **PASS (5/5)** |

All 5 pre-registered checks passed:
1. `geo_monthly_pct >= Q42 + 0.10%`: `+2.641% >= +2.451%` (`PASS`)
2. `sharpe > Q42`: `1.670 > 1.483` (`PASS`)
3. `max_dd_pct >= Q42 - 1.0%`: `-19.27% >= -20.37%` (`PASS`)
4. `A_geo_monthly_pct > 0.0`: `+0.305% > 0.0` (`PASS`)
5. `deflated_sharpe_812 >= 0.80`: `0.8843 >= 0.80` (`PASS`)
