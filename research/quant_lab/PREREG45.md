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
