# Pre-registration 10: time-of-day mechanisms with market-order entries (committed before any round-10 code runs)

## Why this round
- Rounds 8–9 failed because resting limit orders are adversely selected, and because 1m-bar triggers have
  zero gross edge.
- This round tests two mechanisms that are not about 1m-bar patterns. Both enter with taker orders: 6 bps
  fee + 1 bp slippage per side, 14 bps round trip. Holds run from minutes to hours.

## Data, splits, costs
- **Data and splits:** Coinbase BTC-USD 1m bars, same splits as PREREG8.

  | Split | Dates |
  |---|---|
  | discovery | 2022-12-17..2025-06 |
  | A | 2025-07.. |
  | B | 2021..2022-12-16 |
  | C | 2018–20 |
  | D | 2016–17 |

- **Regime:** the previous completed daily close vs SMA50. UP if above, DOWN otherwise.
- **Costs:** entry at a bar's open, exit at a bar's close or at the stop price. 1 bp slippage each side,
  6 bps fee each side. Funding 0.1 bp/h. Positions are 1x, one at a time.

## T1: session × regime drift
- **Sessions (UTC):**

  | Session | Hours |
  |---|---|
  | Asia | 00:00–08:00 |
  | Europe | 08:00–13:30 |
  | US | 13:30–21:00 |
  | Late | 21:00–24:00 |

- **Cells:** 4 sessions × 2 regimes = 8 cells. A trade enters at the session's first bar open and exits at its
  last bar close.
- **Selection on discovery:** keep every cell whose long gross session return has |t| > 2. Its direction is
  the sign of its mean: long if positive, short if negative.
- **The strategy:** the union of the selected cells. If no cell qualifies, T1 fails.
- **Trials charged:** 8.

## T2: US opening-range breakout in the regime direction
- **Range:** high and low of 13:30–14:30 UTC.
- **Entry:** after 14:30, the first 1m close above the range high in an UP regime → long at the next bar's
  open. Mirror: a close below the range low in a DOWN regime → short.
- **Stop:** the opposite side of the range (taker). Exit at the 20:59 bar close.
- **Limits:** at most one trade per day. No parameters searched.
- **Trials charged:** 1.

## Pass rule (9 trials: 700 + 9 = 709)
Each mechanism is judged on its own, with the PREREG8 rule:
1. Net mean per trade > 0 on each holdout with ≥ 100 trades (D is not required if it has fewer than 100).
2. Pooled holdout t > 2.
3. Pooled 1x geometric monthly return > 0.

Also reported: win rate, trades per month, gross bps, and the leverage (≤ 5x) needed for 7%/month.

**If one passes:** add it as a scheduled entry in the paper engine (paper only).

## Prediction (written now)
- Session drifts in the UP regime have positive gross means, but probably below 14 bps per session, so T1
  probably fails net.
- T2 is a coin flip with small expectancy.
- I expect both to fail.

One run. No variants of failed tests.
