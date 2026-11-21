# PREREG52: Combined Crypto (`BTC+ETH+SOL`) + US Equities/ETFs (`10` tickers) `1x` Spot Portfolio

## Hypothesis
Combining our non-overfit `1x` long-only **Crypto sleeve (`Q51`: `BTC+ETH+SOL` vol-parity dual-speed trend + `C1` carry)** with a **`1x` long-only US Equities/ETFs sleeve (`SPY, QQQ, IWM, TLT, GLD, NVDA, AAPL, MSFT, AMZN, META`)** via **causal 20-day inverse-vol parity** will:
1. Preserve strong bull-market participation (`2020`, `2021`, `2023`, `2024`),
2. Cut crypto-winter (`2022`) and crypto-chop (`2025–2026`) drawdowns without relying on fragile shorting,
3. Improve **full-period Sharpe, Calmar, and 2024–2026 OOS Sharpe** over crypto-only `Q51`.

## Variants (Strictly Causal, `t-1 -> t`)
- `B1_q51_crypto_only`: `PREREG51` (`BTC+ETH+SOL`, `US_SPOT`, `3 bps` cost)
- `B2_equities_only`: `1x` long-only `SPY, QQQ, IWM, TLT, GLD, NVDA, AAPL, MSFT, AMZN, META` (`1.5 bps` cost + `DTB3` idle cash, `top_k=3, trend=0.60, xs=0.30, mr=0.10`)
- `Q52_50_50_crypto_equities`: Fixed `50%` Crypto + `50%` Equities
- `Q52_invvol_crypto_equities`: Causal 20-day inverse-vol-parity allocation between Crypto sleeve and Equities sleeve (clamped `[0.20, 0.80]`).

## Pre-Registered Pass Gates (`Q52_invvol` vs `B1_q51_crypto_only`)
1. `sharpe_gt_crypto`: `Q52_invvol` Sharpe > `B1_q51_crypto_only` Sharpe
2. `max_dd_better_than_crypto`: `Q52_invvol` max DD > `B1_q51_crypto_only` max DD (less negative)
3. `calmar_gt_crypto`: `Q52_invvol` Calmar > `B1_q51_crypto_only` Calmar
4. `oos_2024_2026_cagr_gt_crypto`: `Q52_invvol` 2024–2026 OOS CAGR > `B1_q51_crypto_only` 2024–2026 OOS CAGR
5. `dsr_ge_0p90`: `Deflated Sharpe (n_trials=850) >= 0.90`
