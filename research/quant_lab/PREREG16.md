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

## Result (run once, 2026-11-11): all three FAIL. No ledger change.

| Variant | Trades | Net/trade | t | Win rate | Periods > 0 | Stress net | Overlay on P1 (%/mo, DD) |
|---|---|---|---|---|---|---|---|
| D1 uptrend dip | 79 | +200 bps | 1.34 | 48% | 3/5 (C −373, A −94) | +92 | +8.74, −83% |
| D2 uptrend pullback | 108 | +162 bps | 1.30 | 65% | **4/5** (C −368) | +27 | +8.89, −87% |
| D3 any dip | 167 | −14 bps | −0.15 | 46% | 1/5 | −122 | +7.84, −90% |

P1 alone: +8.50%/month, −84% max drawdown.

**Reading:**
- **Buying any big drop does not work.** D3 earned +12 bps gross before costs and lost in every period
  after 2017. In crypto a big daily drop is as likely to continue as to bounce, consistent with Caporale &
  Plastun (2019).
- **Buying dips only inside an uptrend looks better, but is not statistically significant.**
  - D1 and D2 averaged +160 to +200 bps per trade.
  - With ~80–110 trades and very noisy outcomes (worst trades −32% and −52%), t ≈ 1.3, well short of 2.
  - Both lost in the 2018–20 bear.
- **As an overlay on P1** (dip trades only while P1 is flat):
  - return rises slightly (+0.2 to +0.4%/month);
  - the drawdown gets worse for D2 (−84% → −87%) and D3, and is about the same for D1 (−83%).
  - Dip-buying does not reduce the losses; it adds a little return for a little more risk.
- **D2 is the best candidate.** It is positive in 4/5 periods with a 65% win rate. Under this rule it still
  fails, and any adoption would be post hoc (forward paper only).

Trials charged: 726.

## Post-hoc decision (user, 2026-11-11): track D2 forward on paper
- **What this is:** a forward comparison on paper, chosen after seeing the results. It is not a pass.
- **Ledger:** `D2_btc_eth_pullback_1x` in the trend ledger (BTC and ETH halves at 1x, us-futures costs), beside
  P1 and R3.
- **History:** the trend state now keeps ~300 days of closes, enough for the SMA200.
- **Adoption:** it would need a fresh pre-registered test, or a long forward record. One month of paper results
  is noise at ~10 trades a year per coin.
