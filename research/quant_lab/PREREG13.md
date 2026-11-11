# Pre-registration 13: drawdown first — make the BTC+ETH trend core lose much less (committed before any round-13 code runs)

## Why
- **The new goal (the user's request, 2026-11-11):** "improve the drop so it doesn't lose a lot".
- **Where P1 stands:** BTC+ETH, 2x each above its own SMA50. It made +8.50%/month with a −84% max drawdown.
- **What this round changes:** the drawdown becomes the primary target. Return is maximised only among the
  variants whose drawdown is acceptable.

## Variants (BTC and ETH halves, daily bars, us-futures costs, long-or-flat; 4 fixed variants, no search)

| Variant | Rule |
|---|---|
| R1 | P1 at 1.0x per sleeve |
| R2 | P1 at 1.5x per sleeve |
| R3 (fast exit) | 2x. Enter when close > SMA50. Exit when close < SMA20 or < SMA50. Re-enter only when close > SMA50 again and close ≥ SMA20. |
| R4 (trailing stop) | 2x on the P1 rule. Exit the sleeve when its close is 15% below the highest close since entry. After a stop, re-enter only after the close has gone back to ≤ SMA50 and then crossed above it. |

## Pass rule (4 trials: 713 + 4 = 717)
A variant passes only if all of these hold:
1. Full 2016–2026 max drawdown no worse than −50%.
2. Geometric monthly return > 0 in at least 4 of the 5 periods.

   | Period | Dates |
   |---|---|
   | D | 2016–17 |
   | C | 2018–20 |
   | B | 2021-01..2022-12-16 |
   | discovery | 2022-12-17..2025-06 |
   | A | 2025-07.. |

3. Stress (1 bp/h funding) full geometric monthly return > 0.

**Choosing among passers:** the one with the highest full-history geometric monthly return.

**Also reported:** each variant's full geometric %/month, its distance from 7%, losing months, the worst
month, and bear/flat/bull means.

**If one passes:** it is added to the trend ledger as a further paper variant, and it becomes the recommended
core. P1 stays tracked for comparison.

## Prediction (written now)
- R1's drawdown is roughly −55% to −65%. It may miss −50%.
- R2 misses −50%.
- R3 and R4 cut drawdowns more, at the cost of whipsaw.
- I expect at most one variant under −50%, earning around 3–5%/month: well short of 7%. That gap is the price
  of a smaller drop.

One run. No variants after results.
