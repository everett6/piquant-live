# Pre-registration 5: a ≥60% monthly market-beat rate (committed before any M1 code runs)

The user's target: beat BTC buy-and-hold in at least 60% of months.

**Definition.** Beat rate = the share of complete calendar months in which the strategy's compounded net
return is strictly greater than BTC-USD buy-and-hold over the same month. This is not the same as beating
buy-and-hold overall, so total return, Sharpe and max drawdown versus buy-and-hold are reported next to it.

## Strategy family M1: trend-gated leverage (daily bars)

- **Signal:** at each daily close, trend = close > SMA(n) of daily closes, today included.
- **Position:** the next day's position is L if trend holds, else 0 (flat). Long only.
  - Intuition: in up-trending months L > 1 can beat buy-and-hold. In down-trending months being flat can
    beat it. Whipsaw months lose.
- **Costs:** us-futures (taker + slippage on turnover; funding 0.1 bp/h scaled by |position|).
  us-futures-stress and us-spot (L capped at 1, no funding) are reported for the pick.
- **Grid:** n in {20, 50, 100, 200} days x L in {1.0, 1.5, 2.0} = 12 configs. Charged: 675 + 12 = 687 trials.

## Data and splits (Coinbase BTC-USD 1m, resampled to daily)

| Split | Dates | Status |
|---|---|---|
| Discovery | 2022-12-17 .. 2025-06-30 | selection happens here only |
| A | 2025-07-01 .. end | seen |
| B | 2021-01-01 .. 2022-12-16 | seen |
| C | 2018-01-01 .. 2020-12-31 | used by rounds 3–4 before this runs |
| D | 2016-01-01 .. 2017-12-31 | untouched, primary |

- D was backfilled after this file was written and was never loaded by any test.
- If fewer than 18 complete months of D are available, D is insufficient and M1 FAILS. No replacement window
  is used.
- Positions are computed causally on the full series. Each period is then evaluated on its own months.

## Selection and pass rule

- **Pick:** the config with the highest discovery beat rate. Ties go to the higher discovery net Sharpe.
- **PASS (">= 60% market beat rate")** requires all three:
  - beat rate ≥ 60% on D;
  - beat rate ≥ 60% on each of A, B and C;
  - a one-sided binomial test of the pooled A+B+C+D months against 50% gives p < 0.05.
- **Also reported:** PBO across the 12 configs (on monthly excess return), and DSR of the pick's daily
  returns on each holdout.
- **Prediction, written now:** beat rates land around 50–60%. Clearing 60% on all four holdouts is unlikely
  because whipsaw months and leverage drag in bear markets both work against it.
- One run. A failure is recorded. No variants of M1 are tested on these holdouts.
