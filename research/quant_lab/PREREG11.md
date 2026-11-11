# Pre-registration 11: a better trend core — BTC+ETH and volatility targeting (committed before any round-11 code runs)

## Why
- **The goal:** 7%/month.
- **The only component with returns:** the 2x BTC SMA50 trend core. It made +7.29%/month geometric over
  2016–2026 (round 7 comparator, us-futures costs).
- **The cost of that return:** a −86% max drawdown, 43% losing months, and only +0.58%/month mean in A
  (2025-07..).
- **This round:** tests two standard, economically motivated changes that need no parameter search:
  - spreading across two trend-following assets (BTC and ETH);
  - sizing each position to a volatility target.

## Rules (daily bars, Coinbase BTC-USD and ETH-USD from 1m candles; decided at the close, held over the next day)
- **Sleeves:** each asset is a sleeve with half the capital. A sleeve is long when its own close > its own SMA50,
  else flat. Long-or-flat only; round 7 showed shorts lose.
- **Before ETH has 50 daily closes:** the ETH half stays in cash. It is not reassigned. This is conservative
  for period D.
- **Costs:** us-futures (taker 6 + slip 1 bps per unit of turnover, funding 0.1 bp/h on |position|). Stress
  profile: 1 bp/h.
- **P1 (fixed 2x):** each sleeve is 2x its half when long, so the portfolio is at most 2x.
- **P2 (vol-targeted):** a sleeve's leverage when long is min(3, 1.4 / σ), where σ is the trailing 30-day
  annualised volatility of that asset's daily returns (causal). 1.4 is about 2x BTC's long-run ~70% vol,
  so average exposure matches P1 without being fitted.
- **Comparator:** the BTC-only 2x core, same run and same costs.

## Periods

| Period | Dates |
|---|---|
| D | 2016–17 |
| C | 2018–20 |
| B | 2021-01..2022-12-16 |
| discovery | 2022-12-17..2025-06 |
| A | 2025-07.. |
| full | 2016–2026 |

## Pass rule (2 trials: 709 + 2 = 711)
A variant passes only if all of these hold:
1. Full 2016–2026 geometric monthly return ≥ 7.0%.
2. Full-history max drawdown is better than the comparator's (less negative).
3. Geometric monthly return > 0 in at least 4 of the 5 periods.
4. The stress-cost full geometric monthly return is > 0.

**Also reported:** the share of losing months, the share of months ≥ 7%, the worst month, and the monthly
return in bull, bear and flat months (the PREREG7 definition).

**If one passes:** add it as a variant to the trend ledger (internal/trend). The live in-process core switches to
it, on paper.

## Prediction (written now)
- P1 lowers drawdown somewhat, since BTC and ETH correlate about 0.8. Its return could land on either side of
  7%.
- P2 trims the worst crashes and probably lowers return.
- I'd guess at most one of them passes.

One run. No variants after results.
