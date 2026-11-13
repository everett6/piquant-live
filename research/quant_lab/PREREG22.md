# Pre-registration 22: PREREG18's lead-lag and flow rules on Tardis.dev free days, 2023–2026 (committed before any round-22 code runs on real data)

## Why
The owner does not want to wait for the 2026-12-09 run of PREREG18 on our own recording. Tardis.dev publishes
the first day of every month free: Coinbase, Kraken and Binance.US quotes (best bid/ask on every change), trades
and full books. This gives about 47 separate days across bull, bear and flat markets.

The three PREREG18 rules (L1, L2, F1) are run here **unchanged**, through the same code (`strategies18.py`),
on this independent data. Nothing is fitted or tuned. PREREG18 itself still runs once on the recorded window on
or after 2026-12-09, and is not changed by this result.

## Data
- **Source:** `https://datasets.tardis.dev/v1/<exchange>/<type>/<YYYY>/<MM>/01/<symbol>.csv.gz`, the free days
  only (no API key, nothing paid). Streamed and reduced on Kaggle (or the Pi). Raw files are not kept.
- **Days:** the 1st of each month from 2023-01-01 to 2026-11-01 (47 days).
  - **Discovery:** 2023-01-01 to 2024-12-01 (24 days).
  - **Holdout:** 2025-01-01 to 2026-11-01 (23 days).
- **Symbols:**
  - Coinbase `BTC-USD`, `ETH-USD`;
  - Kraken `XBT-USD` up to 2026-07-01, then `BTC-USD`; `ETH-USD` throughout;
  - Binance.US `BTCUSD`, `ETHUSD` (lead measurements only; not traded).
- **Clock:** Tardis `local_timestamp` (its capture time, one clock for all venues), cut to ms.
- **Books:** each venue's `quotes` file is turned into 1 s snapshots, to match our recorder.
  - A snapshot at every whole second holds the latest best bid and ask at or before it.
  - mid = (bid + ask) / 2. Seconds before the first quote of the day, or with bid ≥ ask, have no snapshot.
- **Trades (F1):** Coinbase `trades`. `side` is the aggressor side; size is `amount`.

## What carries over from PREREG18, and what had to change
- **Unchanged:** signals, thresholds (5 bps in 1 s; flow60 ≥ 0.50), holds (30 s; 5 min), fills (first 1 s
  snapshot ≥ 200 ms after the decision; buy at the ask, sell at the bid), costs and stress costs, one open trade
  per coin per rule, and the pass rule's four checks.
- **Days are separate.** Each day is run on its own; nothing (an open trade, a signal's "1 s earlier") crosses
  midnight. F1's volume floor uses **that day's own** median 1-minute volume, because the previous UTC day is not
  in the data. (PREREG18 already uses the day's own median on its first day.)
- **Exclusions** (no new entries; open trades still exit):
  - minutes with a gap in Coinbase trade IDs (Coinbase IDs are sequential);
  - minutes inside a gap of more than 60 s between quote updates on any venue the rule uses. Quotes arrive only
    on change, so the recorder's 5 s snapshot rule cannot be used; 60 s without any top-of-book change on BTC or
    ETH means a feed problem;
  - there is no clock check in this data, so that rule is dropped.
- **Trades per day** are reported per data day (47 days, not 30 per month).

## Rules (3 trials: 737 + 3 = 740)
- **L1:** Coinbase leads Kraken; buy Kraken spot, long only, exit after 30 s. Costs 40 bps per side (stress 45).
- **L2:** Kraken leads Coinbase; trade the Coinbase futures proxy long and short, exit after 30 s. Costs 6 + 1 bps
  per side (stress 6 + 3).
- **F1:** 60 s Coinbase flow momentum; long and short, exit after 5 min. Costs 6 + 1 (stress 6 + 3).
- Full definitions: PREREG18.md. Code: `strategies18.py`, used as is.

## Pass rule (same as PREREG18)
A rule passes only if all of these hold:
1. Holdout: at least 200 trades.
2. Holdout: mean net bps per trade > 0, with a one-sided t-stat ≥ 2.0.
3. Discovery: mean net bps per trade > 0.
4. Holdout at stress costs: mean net bps per trade > 0.

## Also reported (descriptive, no trial)
- **Lead matrix:** for each ordered pair of the three venues (6 pairs) and each coin, the number of PREREG18-style
  lead signals (leader moves ≥ 5 bps in 1 s, follower less than half) and the follower's mean signed mid move 1, 5
  and 30 s later. This shows which venue follows, as the owner asked ("follower chosen by data").
- Per rule: win rate, trades per day, share of gross lost to the spread, worst trade, and results by year.

**If one passes:** it still has to pass PREREG18 on our recording (2026-12-09) before forward paper trading.
**If none pass:** these rules fail on 2023–2026 history. PREREG18 still runs as registered.

## Run
- Once. Code is tested on synthetic data first; the real run happens a single time.
- The run reads only the free files listed above.

## Prediction (written now)
Same as PREREG18. L1: a real lead (Kraken follows Coinbase) but strongly negative net after 80 bps of fees. L2:
a weaker lead, net about −10 to −15 bps. F1: net about −12 bps. I expect none to pass. In the lead matrix,
Coinbase leads both other venues.

## Amendment (2026-11-13 00:12 UTC, before any result existed)
- **First attempt stopped for a code bug.** It processed 14 day-coin pairs (2023-01 to 2023-07 BTC/ETH), then every
  later day failed: Binance.US's `BTCUSD`/`ETHUSD` quote files are empty from 2023-08 (Binance.US lost USD
  banking). The code dropped the whole day when any venue's file was empty. It was stopped before writing any
  result or verdict; only data-count lines were printed. No rule, threshold, cost or split changed.
- **Fix:** Binance.US (lead matrix only) uses the `USDT` pair when the `USD` pair is empty. If both fail, the day
  runs without Binance.US. Coinbase and Kraken are still required. The run then starts again from the first day.
- The run moved from Kaggle (no internet in its notebooks) to the Pi.

## Result (run once on the Pi, 2026-11-13 00:12–00:25 UTC): all three FAIL
47 days, 94 day-coin pairs, no failed days. Binance.US used the USDT pair from 2023-08 (lead matrix only).

| Rule | Discovery n / net / gross (bps) | Holdout n / net / gross | Holdout stress net | Verdict |
|---|---|---|---|---|
| L1 Coinbase→Kraken, Kraken spot long | 2412 / −77.2 / +3.46 | 1783 / −78.0 / +2.50 | −88.0 | FAIL (net < 0; win rate 0%) |
| L2 Kraken→Coinbase, futures proxy | 8121 / −14.4 / +0.28 | 5439 / −14.4 / −0.10 | −18.4 | FAIL |
| F1 60 s flow momentum, futures proxy | 5536 / −14.5 / −0.15 | 5891 / −14.1 / +0.08 | −18.1 | FAIL |

**Lead matrix** (follower's mean signed mid move after a ≥ 5 bps leader second, bps at 1 s / 5 s / 30 s):

| Leader → follower | BTC | ETH |
|---|---|---|
| Coinbase → Kraken | 1.96 / 3.77 / 5.29 | 2.09 / 3.65 / 4.65 |
| Coinbase → Binance.US | 1.59 / 2.68 / 4.00 | 1.82 / 3.10 / 4.58 |
| Kraken → Binance.US | 0.54 / 1.29 / 2.75 | 0.95 / 1.99 / 3.52 |
| Binance.US → Kraken | 0.49 / 1.03 / 1.72 | 0.51 / 0.92 / 1.42 |
| Kraken → Coinbase | 0.10 / 0.23 / 0.38 | 0.20 / 0.29 / 0.36 |
| Binance.US → Coinbase | 0.06 / 0.05 / 0.09 | 0.10 / 0.15 / 0.19 |

**Reading:**
- The prediction held. **Coinbase leads**, and Kraken and Binance.US follow by about 4–5 bps within 30 s.
  Nothing leads Coinbase (≤ 0.4 bps). So the follower venue, chosen by data as the owner asked, is Kraken (Binance.US
  close behind).
- The lead is real but small. It is far below Kraken's 80 bps round-trip retail fee (L1), and L1's gross capture
  shrank from +4.2 bps (2023) to +0.9 (2026). Trading Coinbase on Kraken's signal (L2) has no edge, because
  Coinbase does not follow.
- Flow momentum (F1) has about 0 gross edge, like every directional short-horizon signal so far.
- Trials charged: 740. PREREG18 still runs once on our recording on or after 2026-12-09, unchanged.
- **Not tested, and it would need its own pre-registration:** catching the 4–5 bps follower move at a cost near
  zero (for example a maker rebate tier on the follower). PREREG21 found passive fills lose about 1 bp to adverse
  selection within 1 s, so the net margin would be thin even at zero fees.
- **Runtime:** the full run took 12.5 minutes on the Pi 5 (2 workers, lowest priority), streaming every file.
