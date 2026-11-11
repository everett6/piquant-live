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

## Result (run once, 2026-11-11): both FAIL. P1 stays.

| | full geo %/mo | max DD | losing | ≥7% | worst month | D | C | B | discovery | A | bear/flat/bull | stress |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| Q1 DD brake | +5.88 | **−70%** | 42% | 36% | −42% | +21.45 | +4.06 | +1.28 | +2.61 | +1.70 | −4.9/−3.1/+25.3 | +2.13 |
| Q2 BTC/ETH/SOL | +7.78 | −81% | 44% | 38% | **−35%** | +19.37 | +5.14 | +5.03 | +8.33 | +1.06 | −7.5/−4.6/+32.1 | +1.82 |
| P1 (comparator) | +8.50 | −84% | 43% | 39% | −49% | +27.99 | +6.24 | +0.77 | +6.25 | +3.32 | −8.0/−3.9/+38.2 | +1.19 |

- **Q1:** cut the drawdown by 14 points, but the return fell to +5.88%/month. It failed check 1.
- **Q2:** kept ≥7% and had the mildest worst month, but its drawdown was only 3 points better. It failed
  check 2.
- **The trade-off is real:** in this data, every unit of drawdown removed also removes return. Leveraged
  long-or-flat trend reaches 7%/month only with drawdowns of about −80% or worse.
- **Next step:** no ledger change. P1 remains the live paper variant.
