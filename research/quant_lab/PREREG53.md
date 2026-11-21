# PREREG53: Dual-Momentum Cash Gate + Trend-Filtered MR + Asymmetric Fast-Risk-Off InvVol (`Crypto + Equities`)

## Hypothesis (First-Principles, Zero 2026 Parameter Tuning)
1. **Absolute Momentum Cash Gate:** In `1x` long-only sleeves, never hold an asset in `w_tr` when its blended momentum is negative (`m <= 0`); let idle capital earn `DTB3` T-bills instead of bleeding on lagging trend exits.
2. **No Falling-Knife MR:** Restrict 5-day oversold mean-reversion (`w_mr`) strictly to assets in an established uptrend (`close > SMA50` / `tr > 0.25`) so dip-buying never catches a falling knife in bear markets (2022 crypto, 2026 NVDA/GLD).
3. **Asymmetric Fast-Risk-Off Inv-Vol Blend:** Use `max(vol_10d, vol_20d)` (shifted by 1 day, strictly causal) when blending the Crypto (`Q51`) and US Equities (`10` tickers) sleeves, so capital exits volatility spikes faster.

## Pre-Registered Pass Gates (`Q53` vs `Q52_invvol_crypto_equities`)
1. `sharpe_gt_q52`: Full-period Sharpe > `Q52_invvol` (`1.974`)
2. `max_dd_ge_q52`: Full-period Max DD >= `Q52_invvol` (`-11.96%`, i.e. shallower or equal)
3. `y2026_ann_gt_q52`: 2026 annualized return > `Q52_invvol` (`+5.76%`)
4. `oos_2024_2026_sharpe_gt_q52`: 2024–2026 OOS Sharpe > `Q52_invvol` (`1.981`)
5. `dsr_ge_0p95`: `Deflated Sharpe (n_trials=860) >= 0.95`
