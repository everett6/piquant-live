# Pre-registration 6: forward paper test of the trend filter (registered 2026-11-11, before the tracker runs)

## Why
Round 5's M1 (SMA50, 2x) failed the 60%-everywhere beat-rate rule: 56/52/64/62%, pooled 59.6%. Even so, it
beat buy-and-hold on total return on all four holdouts. The 1x spot version, which was reported but not
selected, was positive on all four. No untouched history remains (Coinbase 1m data starts in 2015). The
only clean test left is forward paper trading on data that does not exist yet.

## What runs
`piquant trend`, a separate paper ledger. It never touches the Pi engine.

**Variants, frozen now**

| Variant | Position | Costs | Funding |
|---|---|---|---|
| T1 spot | 1.0 x BTC when the last completed UTC daily close > SMA50 of daily closes, else flat | 2 bps fee + 1 bp slippage per unit of turnover | none |
| T2 futures | 2.0 x under the same rule | 6 bps + 1 bp per unit of turnover | 0.1 bp/h x position |
| BH benchmark | 1.0 x BTC always | none after entry | none |

- **Data:** Coinbase BTC-USD daily candles (public REST). Each decision is made once per UTC day, about 5
  minutes after the daily close. The fill is that day's open (= the previous close).

## Evaluation (no peeking rules)

**Interim at 6 complete months (2027-05-31).** Report only. No parameter changes.

**Verdict at 12 complete months (2027-11-30). A variant PASSES if all hold:**
- monthly beat rate vs BH ≥ 60%;
- net Sharpe > BH Sharpe;
- max drawdown ≤ BH max drawdown + 10 pp.

**Kill rule.** T2 is stopped early if its equity falls 50% below its peak. That is recorded as a FAIL.

**Deploy rule.** Only a passing variant may be wired into the Pi engine, and only with the user's approval.
Until then, nothing in the live engine changes because of this test.
