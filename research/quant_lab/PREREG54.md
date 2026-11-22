# PREREG54: Dual-Momentum Cash Gate + Trend-Filtered MR for Crypto (`BTC + ETH + SOL`, `US_SPOT`)

## Hypothesis (First-Principles, Zero 2026 Tuning)
Apply the exact `PREREG53` mechanisms directly to the **`BTC + ETH + SOL`** `US_SPOT` `60/40` trend+carry sleeve:
1. **Absolute Momentum Cash Gate (`m > 0`):** Any coin with negative blended momentum (`7d/28d`) gets `0` trend weight, parking idle capital in `DTB3` T-bills.
2. **Top-2 Positive-Momentum Concentration (`top_k=2`, `trend_weight=0.50, xs_weight=0.40, mr_weight=0.10`):** Concentrates in trending leaders (`BTC` or `SOL`) when only 1–2 coins are in an uptrend.
3. **Trend-Filtered MR:** 5-day oversold dip-buys only when `tr > 0.25` and `m > -0.2` (zero falling-knife catches in 2022/2026).

## Pre-Registered Pass Gates (`Q54_crypto_dual_mom` vs `Q51_btc_eth_sol_volparity_spot`)
1. `sharpe_ge_q51`: Full-period Sharpe >= `Q51` (`1.670`) - `0.05`
2. `max_dd_better_than_q51`: Full-period Max DD > `Q51` (`-19.27%`, shallower)
3. `y2022_better_than_q51`: 2022 bear return > `Q51` (`-9.90%`)
4. `y2026_ann_gt_q51`: 2026 annualized return > `Q51` (`+0.21%`)
5. `dsr_ge_0p85`: `Deflated Sharpe (n_trials=870) >= 0.85`
