# Pre-registration 9: bear-market minutes-horizon shorts (the user's pasted blueprint), committed before any round-9 code runs

## Source and scope
- **Source:** a blueprint the user pasted on 2026-11-11. It proposes short-only trades in a bear regime,
  held 5–45 minutes, with maker execution and a time-decay profit ratchet.
- **What can be tested now:** 1m OHLCV covers 2016–2026, so the price, volume and VWAP triggers, the
  execution choice and the ratchet are tested here.
- **What has to wait:** the order-book imbalance (OBI), VPIN/Hawkes and Coinbase–Binance lead-lag filters
  need tick and book data. Recording started 2026-11-11 (commit ec03842). Those filters get their own
  pre-registration once at least 4 weeks are recorded.
- **What the earlier data already says:**
  - The blueprint's 42.5% breakeven assumes maker fills that are not adversely selected. Round 8 measured
    the opposite on 1m bars: resting limits filled mostly when price kept running through them.
  - Round 8 also found that 1m shocks tend to continue. That supports Strategy A's direction.

## Common rules
- **Data and splits:** same as PREREG8.

  | Split | Dates |
  |---|---|
  | discovery | 2022-12-17..2025-06 |
  | A | 2025-07.. |
  | B | 2021..2022-12-16 |
  | C | 2018–20 |
  | D | 2016–17 |

  No selection happens here: every variant's parameters come from the blueprint. Discovery is only
  reported.
- **Regime:** bear means the previous completed daily close < SMA50. Shorts only. No trades outside the bear
  regime.
- **Fills and costs:** the PREREG8 model.
  - Maker limit at the signal bar's close, live for 5 bars. It must trade through by 1 bp. Fee 2 bps.
  - Taker fill: next bar's open, with 1 bp slippage and a 6 bps fee.
  - Stops and time exits are taker orders.
  - TP limits are maker orders and must trade through by 1 bp.
  - If TP and SL are both touched in one bar, it counts as the stop.
  - Funding: 0.1 bp/h. Positions are 1x, one at a time.
- **Stop-loss:** 30 bps from entry, taker (L = 30 in the blueprint's own arithmetic).

## A: downside cascade short (two execution variants)
- **Trigger:** in the bear regime, a 1m return < −15 bps.
- **Ratchet:** the TP is the net target plus that variant's round-trip cost with a maker exit.

  | Minutes since fill | Net target |
  |---|---|
  | 0–5 | +65 bps |
  | 5–15 | +35 bps |
  | 15–25 | +12 bps |
  | 25–45 | 0 (breakeven) |

  Hard time stop at 45 minutes.
- **A-maker:** maker sell at the close, as the blueprint says. Round trip 4 bps.
- **A-taker:** sell at the next open. Round trip 8 bps.

## B: counter-trend rally fade
- **Trigger:** in the bear regime, dev = close / VWAP30 − 1, where VWAP30 is the trailing 30-bar volume-weighted
  mean of typical price (h+l+c)/3. Trade when dev ≥ +35 bps AND dev / (trailing 1440-bar std of dev) > 2.
- **Execution:** maker sell at the close.
- **Exits:** TP (maker) at VWAP30 as measured at the signal bar. SL 30 bps. Hard time stop at 30 minutes
  (maxHoldSec 1800).

## Pass rule (3 trials charged: 697 + 3 = 700)
Each variant is judged alone, with the same rule as PREREG8:
1. Net mean per trade > 0 on each of A, B, C and D, with ≥ 100 trades each (D is not required if it has
   fewer than 100).
2. Pooled holdout t > 2.
3. Pooled holdout 1x geometric monthly return > 0.

Also reported: win rate, exit mix, trades per month, gross bps, and the leverage (≤ 5x) needed for 7%/month.

**If a variant passes:** wire it into the Go engine's bear regime for paper only, with maxHoldSec and the
ratchet.

**If none passes:** no engine change, and the OBI/VPIN filters wait for the recorded tick data.

## Prediction (written now)
- A-taker has a positive gross edge, consistent with round 8's continuation, but probably below its 8 bps cost.
- A-maker is adversely selected. A maker sell after a drop fills only on a bounce.
- B has a high win rate and a negative mean.
- I expect all three to fail.

One run. No variants of failed tests.

## Result (run once, 2026-11-11): all three FAIL

| Variant | A | B | C | D | Pooled net / gross bps | Win rate | Exits SL / TP / time | 1x %/mo |
|---|---|---|---|---|---|---|---|---|
| A-maker | −10.1 | −8.7 | −14.3 | −12.7 | −11.5 / −5.1 (t −65) | 36% | 58 / 40 / 1 | −37.4 |
| A-taker | −13.3 | −11.4 | −11.5 | −4.9 | −11.1 / −0.9 (t −61) | 38% | 55 / 43 / 2 | −39.6 |
| B rally fade | −9.8 | −9.1 | −12.8 | −10.1 | −10.6 / −3.3 (t −22) | 30% | 62 / 17 / 21 | −8.9 |

Holdout columns are net bps per trade.

- No leverage up to 5x reaches 7%/month.
- **The blueprint's assumptions didn't hold.**
  - Maker shorts after a drop fill mainly when the bounce keeps going, so 58% of trades hit the stop.
  - The taker variant shows about zero gross edge, not the +50–75 bps the blueprint claimed. D (2016–17)
    alone was +5 bps gross.
  - Rally fades in downtrends keep rallying: 62% hit their stop.
- **What happens next:** no engine change. Bear-regime shorts stay blocked. The OBI/VPIN/lead-lag filters wait
  for ≥ 4 weeks of the tick data recorded since ec03842.
