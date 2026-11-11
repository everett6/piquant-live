# Pre-registration 16: buy the dip at the daily scale (committed before any round-16 code runs)

## Why
The owner asked how to make money from dips instead of losing on them. Ideas already tested and failed:
- PREREG8: minute-scale shock fades (maker entries were adversely selected).
- PREREG7: shorting below SMA50.
- PREREG9: shorting cascades and fading rallies.

Not yet tested: buying a large daily drop and holding for days. Two sources inform this:
- **Mean reversion after overreaction.** Literature on equities: De Bondt & Thaler 1985; Lehmann 1990.
- **Continuation.** Caporale & Plastun (2019) found crypto prices tend to continue in the direction of an
  abnormal daily move on the next day. So holds are multi-day, and the trend context is tested separately.

P1 is flat whenever close < SMA50, so dip trades would use idle capital.

## Variants (BTC and ETH separately, trades pooled; long only, 1x, us-futures costs; 3 fixed variants)
**Common rules:**
- σ30 = std of the last 30 daily close-to-close returns, through day t.
- Entry is at day t's close; the signal uses closes through t.
- One open trade per coin; signals during a trade are ignored.
- Costs: taker 6 + slip 1 bps per side, funding 0.1 bp/h held (stress: 1 bp/h).

| Variant | Entry signal at close t | Exit |
|---|---|---|
| D1 uptrend dip | close > SMA200 and day-t return ≤ −2·σ30 | close of day t+5 |
| D2 uptrend pullback | close > SMA200 and close ≤ 0.85 × the highest close of the last 20 days | first close > SMA20, or day t+10 |
| D3 any dip | day-t return ≤ −2·σ30 (no trend filter) | close of day t+5 |

## Pass rule (3 trials: 723 + 3 = 726)
A variant passes only if all of these hold, over 2016-01-01..now:
1. At least 60 pooled trades.
2. Mean net return per trade > 0, with a one-sided t-stat ≥ 2.0 (trades treated as independent).
3. Mean net return per trade > 0 in at least 4 of the 5 periods: D 2016–17, C 2018–20, B 2021..2022-12-16,
   discovery 2022-12-17..2025-06, A 2025-07... A period with no trades counts as not > 0.
4. Stress-cost mean net per trade > 0.

**Also reported:**
- Win rate, mean gross and net per trade, and the worst trade.
- Combined with P1: the dip sleeve trades only when that coin's P1 sleeve is flat, on the same half of
  capital at 1x. Report the full-history monthly return and max drawdown against P1 alone.

**If one passes:** the best by t-stat is added to the trend ledger as a paper overlay beside P1. P1 is
unchanged.

**If none pass:** daily dip-buying with these rules is closed.

## Prediction (written now)
- D1: small positive mean (~+50 to +150 bps per trade), t below 2 because there are few trades. Likely fails
  check 2.
- D2: fewer trades, mixed.
- D3: negative in bear periods (falling knives), fails check 3.
- I expect none to pass.
