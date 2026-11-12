# Pre-registration 21: the PREREG9 cascade short with a VPIN filter, on a year of real Binance ticks (committed before any round-21 code runs on real data)

## Why, and what is not tested
- **Source:** the owner's paste of 2026-11-12. It asks for three tests on 2024 Binance ticks:
  1. cascade momentum (VPIN > 0.65 and a 1-minute return below −12 bps, enter with the move, exit in 5–15 min);
  2. a 30-minute time-decay exit;
  3. maker fill reality.
- **What PREREG9 already closed:**
  - PREREG9 A was the downside cascade short: bear regime, 1-minute return below −15 bps, taker entry, and a
    time-decay target ladder (65 / 35 / 12 / 0 bps over 45 minutes). It failed on 2016–2026 1m bars with about
    zero gross edge (A-taker −11.1 bps net per trade).
  - PREREG9 deferred one thing: "the OBI, VPIN/Hawkes and lead-lag filters need tick and book data".
- **So the only new test allowed is A-taker unchanged plus the VPIN filter.**
  - The paste's −12 bps trigger and 5–15 minute exits would be a variant of the failed A, so they are not used.
  - Ask 2 (the time-decay exit) is already inside A: its ladder is a 45-minute time decay. A separate ladder
    test would also be a variant.
  - Ask 3 is measured descriptively below. There is no pass rule and no trial for it.

## Data
- **Ticks:** Binance BTCUSDT spot aggTrades, 2024-01 to 2024-12, from data.binance.vision, imported with
  `piquant import-ticks` (the real-source marker is set; the reader checks it).
- **1m bars from trades:** open is the first trade, high the max, low the min, close the last, and volume the
  sum of quantities. A minute with no trades takes the previous close for all four prices with zero volume.
- **Regime (as PREREG9):** bear means the previous completed daily close is below its SMA50. Daily closes come
  from `data/historical/btc_daily_full.csv` so the SMA has its 2023 warm-up.
- **Split:** discovery 2024-01 to 06, holdout 2024-07 to 12, by entry time. Nothing is fitted.

## VPIN (fixed now)
- **Aggressor side:** a trade with isBuyerMaker = false is a buy aggressor; otherwise it is a sell aggressor.
- **Buckets:** the bucket volume for a UTC day is V_d = the previous UTC day's total BTC volume / 1600 (the
  first day uses its own).
  - 50 buckets then span about 45 minutes of average flow. That matches the engine's Coinbase setting ("50
    buckets ~ 45 min (BTC)"), so a pass would carry to the engine's horizon.
  - Buckets do not cross UTC midnight; each day's last partial bucket is dropped.
- **Computation:** buy and sell volume are first summed per second. A bucket boundary inside a second splits
  that second's buy and sell volume in proportion.
- **VPIN at time t:** the mean of |buy − sell| / V_d over the last 50 buckets completed at or before t. It is
  undefined until 50 buckets exist.
- **When it is read:** at the end of the signal minute, which is the close of the bar whose return triggers.

## Rule V1 (1 trial: 736 + 1 = 737)
- **Trigger:** both of these, at the bar's close:
  - PREREG9 A: bear regime and a 1-minute close-to-close return below −15 bps;
  - VPIN > 0.65.
- **Execution, exits and costs:** exactly PREREG9 A-taker, run by the same function
  (`strategies9.sim_short`, taker entry, ratchet schedule, 45-bar hold):
  - entry at the next bar's open, with 1 bp slippage and a 6 bps fee;
  - stop 30 bps above entry, taker;
  - targets from the 65 / 35 / 12 / 0 ladder, maker 2 bps, must trade through by 1 bp;
  - time exit at 45 minutes, taker;
  - funding 0.1 bp per hour;
  - one position at a time; if a stop and a target touch in one bar, it counts as the stop.
- **Stress:** net minus 6 bps per trade (3 bps more on each side).

## Pass rule
V1 passes only if all of these hold:
1. Holdout: at least 100 trades.
2. Holdout: mean net bps per trade > 0, with t ≥ 2.0 (trades treated as independent).
3. Discovery: mean net bps per trade > 0.
4. Holdout at stress: mean net bps per trade > 0.

## Reported, not judged (no trials)
- **The unfiltered A-taker on the same 2024 Binance data:** a replication of the closed rule, to show what
  the filter changes.
- **VPIN distribution:** the share of minutes with VPIN > 0.65, and its percentiles.
- **Maker markout (ask 3):**
  - **Passive side of every trade:** the volume-weighted mean signed move of the trade price 1 s, 10 s, 1 min,
    5 min and 30 min later, from the passive side's view. A sell aggressor fills a resting bid; a buy aggressor
    fills a resting ask.
  - **A simple resting bid:**
    - At each whole minute, a bid sits at the last trade price.
    - It is filled if a trade prints strictly below it within 60 s.
    - Reported: the fill rate, and the mean move from the bid price 1, 5 and 30 minutes after the fill.
    - Also net of a 2 bps maker entry and a 7 bps taker exit (6 fee + 1 slippage).
  - Without book data this ignores queue position, so it is an upper bound on fills at the touch.

## If V1 passes
- No engine change from this alone.
- It must also hold on the recorded Coinbase ticks (2026-11-12 to 12-09) with the same rule and Coinbase's own
  VPIN buckets.
- After that, forward paper only, behind a flag.

## If V1 fails
The VPIN cascade filter on the PREREG9 short is closed, and no variant of it is retried.

## Run
Once, after the 12 months are imported and the evaluator's synthetic tests pass.

## Prediction (written now)
- VPIN above 0.65 on 45-minute windows is rare, so V1 will have few trades (likely under 100 in the holdout).
- Gross edge will stay near zero, as in PREREG9.
- The unfiltered rule will be negative by about the cost.
- Resting bids will show negative markout after fills (adverse selection), as in PREREG8.
- I expect V1 to fail.

## Result (run once, 2026-11-12): V1 FAILS

531 million real BTCUSDT trades (2024). Bear regime held 33% of minutes.

| Rule | Discovery n / net bps | Holdout n / net bps (t) | Holdout gross | Holdout stress | Win rate (holdout) |
|---|---|---|---|---|---|
| V1 (A-taker + VPIN > 0.65) | 9 / +11.2 | 10 / −0.8 (t −0.07) | +8.8 | −6.8 | 60% |
| A-taker unfiltered (replication, not judged) | 1,036 / −10.9 | 1,445 / −10.5 (t −11.0) | −0.3 | −16.5 | 41% |

- **Why V1 fails:** 10 holdout trades, against a minimum of 100, and a negative holdout mean.
  - VPIN is above 0.65 in 0.7% of minutes (median 0.36, p99 0.63, p99.9 0.74).
  - Only 19 of the 4,254 A-trigger minutes (0.45%) had it.
  - The 19 filtered trades had a higher gross (+8.8 to +20 bps) than the unfiltered rule (about 0), but 19 trades
    cannot tell that apart from noise (t about 1 in discovery, 0 in the holdout).
- **Replication:** the closed PREREG9 A-taker on independent data (Binance 2024 ticks, not Coinbase 1m) gives
  the same answer: about zero gross, −10.5 bps net, 41% wins.
- **Maker fills (descriptive):**
  - The passive side of every trade loses about 1.0–1.2 bps within a second, and the loss persists at 30
    minutes (−1.0 bp).
  - A bid resting at the last price fills 86% of minutes (no queue model). The move after the fill is −0.7 bps
    at 1 minute and −0.2 at 30 minutes.
  - Net of a 2 bps maker entry and a 7 bps taker exit, that is about −9.2 to −9.7 bps per round trip.
  - Resting orders at the touch are adversely selected on average. Maker fees alone do not make them
    profitable.
- **What happens next:** the VPIN cascade filter on the PREREG9 short is closed, with no variants. Trials
  charged: 737. PREREG17's K-variants (maker entries with order-book filters, due 2026-12-09) are the remaining
  test of whether filtering can beat this adverse selection.
