# Pre-Registration 45: MFT Infrastructure & Advanced Machine Learning Overlays

- **ID**: `PREREG45` (`787 -> 790`)
- **Period**: `2020-01-01 .. 2026-10-31`
- **Baseline Benchmarks**:
  - `S36_qd_carry_blend`: `+2.64%/mo`, `−27.8%` DD, `Sharpe 1.285`
  - `Q42_dualspeed_60_40_carry`: `+2.12%/mo`, `−19.8%` DD, `Sharpe 1.363`

## Strategies Evaluated
1. `Q45_gbdt_fee_gated`: Walk-forward HGBRegressor with rigorous purged cross-validation & transaction fee hurdle gating (`>20 bps`).
2. `Q45_hmm_regime_qd_carry`: Walk-forward Causal GMM 3-state regime classification (`TREND`, `MEAN_REVERT`, `CRISIS`) dynamically switching execution logic.
3. `Q45_ml_metalabel_qd_carry`: Meta-labeling classifier scaling base Safe-QD positions via probability of clearing fee hurdles.

## Pass Rules (`base` `us-futures + S19.BASE` costs)
1. `geo_monthly_pct >= 2.25%` AND `max_dd_pct >= -26.0%`.
2. `sharpe > 1.363` AND `calmar > 1.286`.
3. `mean_dd_pct <= 11.50%`.
4. `A_2025_26 geo_monthly_pct > 0` AND `stress geo_monthly_pct > 0`.
5. Beats naive `0-bps` un-gated baseline on net Sharpe.

## Result (run once, 2026-11-18, `2020-01-01..2026-10-31`, `n_trials = 790`)

| Variant (`us-futures` + `S19.BASE`) | Base Geo (Annualized) | Max DD | Mean DD | Ulcer | Sharpe | Calmar | `A (25-26)` | Stress Geo | Spot Geo (Annualized) / DD / Mean DD / Sharpe | Verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---:|---|---|
| `S36_qd_carry_blend` (ref) | `+2.64%/mo` (`+36.7%/yr`) | `−27.8%` | `12.78%` | `14.68%` | `1.285` | `1.14` | `+1.04%` | `+0.76%` | `+2.93%/mo` (`+41.4%/yr`) / `−27.3%` / `11.99%` / `1.400` | ref (`PREREG36` PASS) |
| `Q45_gbdt_ungated_0bps_ref` | `+2.10%/mo` (`+28.3%/yr`) | `−36.1%` | `17.02%` | `19.78%` | `1.121` | `0.70` | `−0.37%` | `+0.66%` | `+2.56%/mo` (`+35.4%/yr`) / `−34.1%` / `15.37%` / `1.307` | ref (0-bps un-gated) |
| `Q45_gbdt_fee_gated` | `+2.00%/mo` (`+26.8%/yr`) | `−34.4%` | `16.11%` | `18.63%` | `1.085` | `0.70` | `−0.56%` | `+0.62%` | `+2.41%/mo` (`+33.1%/yr`) / `−32.0%` / `14.39%` / `1.257` | **FAIL** (Checks 1–5) |
| `Q45_hmm_regime_qd_carry` | `+1.11%/mo` (`+14.2%/yr`) | `−18.6%` | `8.18%` | `9.90%` | `0.944` | `0.72` | `−0.27%` | `+0.51%` | `+1.24%/mo` (`+15.9%/yr`) / `−18.1%` / `7.19%` / `1.029` | **FAIL** (Checks 1, 2, 4, 5) |
| `Q45_ml_metalabel_qd_carry` | `+1.87%/mo` (`+24.9%/yr`) | `−19.8%` | `9.14%` | `10.46%` | `1.251` | `1.14` | `+1.11%` | `+0.54%` | `+2.14%/mo` (`+28.9%/yr`) / `−19.4%` / `8.43%` / `1.395` (`A +1.32%`) | **FAIL** (Checks 1, 2) |

### Diagnostics & Honest Reading
- **Why Checking Fees & Purged CV First Protected Capital:** All 3 ML/MFT overlays **FAIL** against `S36` and `Q42` and are **not promoted to live trading**:
  - **GBDT (`Q45_gbdt_fee_gated`):** Under strict purged walk-forward training, GBDT return forecasts on daily OHLC/flow/pair features underperformed `Safe-QD` (`+2.00%/mo`, `Sharpe 1.085`, `A −0.56%` vs `S36 +2.64%`, `1.285`, `+1.04%`).
  - **GMM Regime + Pairs (`Q45_hmm_regime_qd_carry`):** Classified `1,576` of `2,496` days as `MEAN_REVERT` (`450` `TREND`, `470` `CRISIS`), throttling `Safe-QD` out of bull trends (`+1.11%/mo`, `A −0.27%`) and proving `BTC/ETH` pairs mean-reversion loses money vs `Safe-QD` after fees.
  - **Meta-Labeling (`Q45_ml_metalabel_qd_carry`):** Best of the three (`−19.8%` DD, `A = +1.11%` / Spot `+1.32%`), yet still trails `Q42_dualspeed_60_40_carry` (`+2.12%/mo`, `−19.8%` DD, `Sharpe 1.363` / Spot `1.483`).
- **Kept in Production Infrastructure:** `feature_store.py` (`build_unified_features`, `purged_embargoed_splits`) and Go [`CheckFeeHurdle`](file:///home/everett/PiQuant/piquant-live/internal/quant/guardrails/guardrail.go#L417-L441) (`ErrInsufficientNetEdge`) remain active as strict pre-trade fee/leakage guardrails.
- **Artifacts:** `research/quant_lab/results/round45.json`.

