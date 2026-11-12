# Tranche isolation versus a drawdown floor: the math, and whether it can be tested (2026-11-11)

Analysis only. No data was run and no trial was charged (still 736).

## The idea
Replace the continuous floor (CPPI / Grossman–Zhou, which locked into cash after one gap in PREREG14) with fixed
risk budgets:
- At the start of each window (a day, week or month), post a fraction **b** of current equity W as the only money
  at risk, at leverage **L**. Exposure is b·L·W.
- If the position loses its whole budget inside the window (a fall of 1/L from entry), it is closed and stays flat
  until the next window.
- At the next window a new budget b·W is posted, whatever happened before.

## What it guarantees
- **Loss per window ≤ b**, if the isolation holds (see the caveat below).
- After n windows, in the worst case, W_n ≥ W_0·(1 − b)^n.
- After k losing windows in a row, the drawdown from the peak is 1 − (1 − b)^k. To keep that within D:

  **b ≤ 1 − (1 − D)^(1/k)**

| Windows lost in a row (k) | Largest budget b for D = 25% |
|---|---|
| 6 | 4.68% |
| 12 | 2.37% |
| 24 | 1.19% |
| 52 | 0.55% |

## What it cannot guarantee
- **There is no floor.** (1 − b)^n falls toward 0 as n grows. The 25% bound holds only for the k you assumed. A
  bear market with more losing windows than that breaks it.
- **A floor and "never locks" exclude each other.**
  - Any rule that guarantees W ≥ 0.75 × the high-water mark for ever must risk nothing when W reaches that level.
    Any risk there could breach it.
  - Risking nothing at the floor is the cash lock.
  - Tranches avoid the lock only by giving up the floor.

## What it earns
The budget only changes the result when it is used up. Two cases:

- **Low leverage, short windows** (for example daily windows at L = 4, liquidation at −25% in a day):
  - The budget is almost never used up: the 99.9% worst fall from one close to the next day's low is −23.5% for
    BTC.
  - The tranche is then plain exposure of b·L held all the time. Its drawdown is the trend core's own drawdown,
    scaled.
  - From the measured results (P1 at 1x: about +5%/month with −57%; at 2x: +8.5% with −84%), holding the drawdown
    near 25% needs roughly 0.3–0.4x exposure, which earns about 1.5–2%/month.
  - This is the same return-versus-drawdown line as before.
- **High leverage, long windows** (for example monthly windows at L = 20, liquidation at −5%):
  - The budget is used up within days in most months: a 4-hour fall of 4.4% happens 1% of the time, about 22 times
    a year.
  - The book then sits flat for the rest of the window and misses the trend it was meant to hold.
  - This is a stop-loss with a scheduled re-entry.

Either way the Sharpe ratio is unchanged or lower. Phase 1 showed that 7%/month inside 25% needs a Sharpe ratio
near 4. Tranches change how losses are taken, not how much edge there is.

## Does testing it break the pre-registration rules?
Yes, as described.
- PREREG14 S1 was the trend core with a fixed-distance intraday stop and re-entry at the next close. It had no
  high-water-mark term. PREREG14 states: "If none pass: results are recorded and no variant of these rules is
  retried." PREREG15 reran the same rules with one data fix and closed them (S1: +4.73%/month, −91%).
- A tranche is the same mechanism with two parameters changed: the stop distance becomes 1/L and the re-entry
  interval becomes the window length. That is a variant of S1.
- PREREG12 Q1 (step brake) and PREREG14/15 S2–S3 (floor sizing) are closed for the same reason. PREREG16's rule on
  daily dip-buying is not touched by this.
- A lower-leverage version of the trend core is not a new test either. Its result follows from the P1 numbers by
  scaling.

So tranche isolation is not run. The table above is what the rule would do by construction.

## Caveat on "isolated"
- The per-window cap assumes the venue closes the position at the budget and absorbs any shortfall.
- Crypto-native venues with isolated margin work that way.
- US-regulated futures (the only leveraged route for a US retail account) do not: the customer owes any deficit
  after a gap. On the worst observed 1-minute falls (BTC −19.6%, ETH −34%) a position at L ≥ 5 would lose more
  than its budget.
