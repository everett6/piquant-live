# Pre-registration 12: cutting the BTC+ETH core's risk while keeping ≥7%/month (committed before any round-12 code runs or SOL data is looked at)

## Why
- **Where P1 stands (PREREG11):** BTC+ETH, 2x each above its own SMA50. It met the target at +8.50%/month
  geometric (2016–2026).
- **Its weakness:** a −84% max drawdown and −8%/month in bear months.
- **This round:** tests two standard risk tools. Their parameters are fixed here and not searched.

## Variants (daily bars, us-futures costs, long-or-flat, decided at the close)

### Q1: P1 + portfolio drawdown brake
- **Rule:** while the portfolio's equity is more than 40% below its running peak, every sleeve's leverage drops
  from 2x to 1x. Full leverage comes back once the drawdown is under 20% (hysteresis).
- **Timing:** the brake state is decided at each close from equity through that close, and applies from the
  next day.

### Q2: three sleeves, BTC + ETH + SOL
- **Rule:** one third of capital each, 2x when above that asset's own SMA50.
- **SOL data:** Coinbase SOL-USD daily candles from the public candles endpoint, granularity 86400. SOL listed
  2021-06.
- **Before SOL has 50 closes:** its third stays in cash. It is not reassigned. This lowers returns in D, C and
  early B, which is a known handicap.

## Comparator, periods, costs
- **Comparator:** P1 (BTC+ETH 2x), same run.
- **Periods:** the same as PREREG11.

  | Period | Dates |
  |---|---|
  | D | 2016–17 |
  | C | 2018–20 |
  | B | 2021-01..2022-12-16 |
  | discovery | 2022-12-17..2025-06 |
  | A | 2025-07.. |

- **Costs:** us-futures. Stress profile: 1 bp/h funding.

## Pass rule (2 trials: 711 + 2 = 713)
A variant passes only if all of these hold:
1. Full 2016–2026 geometric monthly return ≥ 7.0%.
2. Full max drawdown at least 10 points better than P1's (e.g. ≥ −74% if P1 is −84%).
3. Geometric monthly return > 0 in at least 4 of the 5 periods.
4. Stress full geometric monthly return > 0.

**Also reported:** losing months, months ≥ 7%, worst month, and bear/flat/bull mean monthly returns.

**If one passes:** it is added to the trend ledger (paper).

**If both pass:** the one with the better max drawdown is used.

## Prediction (written now)
- Q1 cuts the drawdown, but a brake after a 40% loss acts late and slows the recovery. It may fall under 7%.
- Q2 is held back by having SOL in cash before 2021. Its drawdown probably isn't 10 points better, because
  SOL is highly correlated with BTC and ETH in crashes.
- I'd guess both fail.

One run. No variants after results.
