# Pre-registration 7: long/short trend filter, so bear markets can pay too (committed before any code runs)

## Honest limits
- Every Coinbase BTC holdout (2016–2026) has already been seen by earlier rounds. No untouched history is
  left.
- To keep selection bias minimal, the rule is fixed in advance from round 5: SMA50, 2x long. Only two
  short-side sizes are tested. No search.
- A pass here is weaker evidence than a pass on untouched data. Forward paper results (PREREG6) remain the
  real test.

## Rule (daily bars, decided at the daily close, held over the next day)
- **Long side:** 2.0x long when close > SMA50.
- **Short side when close ≤ SMA50:**
  - L1 (primary): 1.0x short.
  - L2 (secondary): 2.0x short.
- **Costs:** us-futures: taker 6 bps + 1 bp slippage per unit of turnover. Funding is charged at 0.1 bp/h on
  |position| on both sides. That is conservative, because shorts often receive funding. Stress profile:
  1 bp/h.
- **Comparator:** the long-or-flat core (2x / 0x), same costs.

## Periods and regimes

| Period | Dates |
|---|---|
| D | 2016–17 |
| C | 2018–20 |
| B | 2021-01 .. 2022-12-16 |
| discovery | 2022-12-17 .. 2025-06 |
| A | 2025-07 .. now |

- **Bear regime months:** calendar months in which BTC buy-and-hold lost more than 5%.
- **Bull regime months:** months in which it gained more than 5%.
- **Flat months:** the rest. They are reported separately.

## Pass rule (each of L1 and L2 judged separately; both are charged: 687 + 2 = 689 trials)
A variant passes only if all of these hold:
1. Mean return over all bear-regime months 2016–2026 > 0. This is the requirement that bear markets pay.
2. Mean return in both bear holdouts > 0: A (2025-07..) and calendar 2022.
3. Full 2016–2026 geometric monthly return ≥ the long-or-flat core's, measured in the same run.
4. Max drawdown 2016–2026 no worse than −90%.

**7% target.** The report also states, for each variant, the share of months ≥ 7% and the full-history
geometric monthly return. "7% every month" is not expected and not claimed.

## Prediction (written now)
L1 improves bear months. It probably loses some return in choppy markets, through short squeezes and
whipsaw costs. L2 has larger drawdowns. Whether either one beats the long-or-flat core over the full
history is genuinely uncertain.

One run. Results are recorded as they come out.
