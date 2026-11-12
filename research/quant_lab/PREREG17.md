# Pre-registration 17: maker entries with pre-fill defense, confluence and a breakeven ratchet, on tick data (committed before any round-17 code runs on real data)

## Why
The owner pasted a four-part plan to raise the win rate (2026-11-11):
1. cancel resting bids when flow turns adverse before they fill;
2. enter only when trend, book imbalance and flow toxicity agree;
3. move the stop to breakeven at +15 bps;
4. shrink the target and stop with holding time, and force an exit at 30 minutes.

The live engine already has versions of all four (internal/paper/dryrun.go: the toxic-pull cancels, the VPIN and
OBI gates, the breakeven lock and the decaying target, and the 45-minute time stop). That engine layer failed
PREREG8–10 at about −10 to −14 bps per trade, and those tests had only 1m bars. Book imbalance, VPIN and
sub-second cancels could not be tested on 1m bars. Tick trades and 1-second top-10 books have been recorded since
2026-11-11, so this round tests the pasted rules with the pasted thresholds on that data.

**What a breakeven lock does and does not do** (BREAKEVEN.md). It turns some losers into scratches, which raises
the win rate and lowers the loss rate. It does not create edge: the mean net per trade depends on the entries.
So the pass rule is on mean net per trade, and win rate is reported beside it.

## Data
- Coinbase BTC-USD and ETH-USD trades (ms timestamps, aggressor side) and 1 s top-10 books, from
  ~/piquant-collect. Trades pooled across both coins.
- **Window:** 2026-11-12 00:00 UTC to 2026-12-09 00:00 UTC (27 days).
  - **Discovery:** the first 13 days, to 2026-11-25 00:00.
  - **Holdout:** the last 14 days.
  - No parameter is fitted, so discovery is a second check, not a tuning set.
- **Excluded:** any minute with a trade_id gap or a reconnect in the collector logs, and any 10-minute window
  where the desktop clock log (clock_offsets.jsonl, started 2026-11-12) shows |offset| > 250 ms or no OK check.
- **Trend:** the daily trend uses Coinbase daily closes (data/historical). The gate is "previous UTC day's
  close > SMA50", per coin.

## Simulator (identical for all variants)
- **Decisions:** at each 1 s book snapshot. Long only (US spot/futures paper, trend gate long-only).
- **Entry quote:** a buy limit at the best bid, live 100 ms after the decision (order latency). One position or
  one resting quote per coin at a time.
- **Maker fill (queue model):**
  - queue ahead = the bid size at our price in the snapshot we quoted from;
  - sell-aggressor trades at our price after the live time reduce the queue;
  - we fill once the queue is used up, or at once if a trade prints below our price;
  - the fill price is our limit. An unfilled quote is cancelled after 60 s.
- **Cancels** take effect 100 ms after the decision; trades before then can still fill us.
- **Exits** (the pasted ladder; hold time h in minutes since the fill; bps vs entry):
  - **Target:** 55 bps at h = 0, linear to 30 at h = 10, linear to 10 at h = 25. A maker sell, filled when a
    trade prints at or above the target.
  - **Stop:** −35 at h = 0, linear to −25 at h = 10, then linear to the breakeven level (below) at h = 25.
    Taker, at the first trade at or below the stop, with 1 bp slippage.
  - **Breakeven lock:** once a trade has printed ≥ +15 bps, the stop is raised to entry + 9 bps (maker entry
    2 + taker exit 6 + 1 slippage) if that is higher.
  - **Time stop:** at h = 30, taker exit at the next trade with 1 bp slippage.
- **Costs:** maker 2 bps, taker 6 bps + 1 bp slippage (us-futures, as in PREREG8).
  - **Stress:** maker 4 bps, and fills need a trade strictly below our price (no queue credit).

## Signals
- **OBI:** (Σ bid size − Σ ask size) / (Σ bid size + Σ ask size) over the top 10 levels of the snapshot.
- **VPIN:**
  - volume buckets of 1/50 of the previous UTC day's volume of that coin (the first day uses its own volume);
  - each bucket's |buy − sell| / bucket volume, averaged over the last 50 completed buckets;
  - undefined until 50 buckets exist (no entries then).
- **Ret5s:** mid change over the last 5 s (book snapshots), in bps.
- **Flow5s:** (buy − sell aggressor volume) / total volume over the last 5 s of trades (0 if no trades).

## Variants (3 fixed, no search)
| Variant | Post a bid when | Cancel a resting bid when |
|---|---|---|
| K1 trend only | trend gate up | 60 s timeout |
| K2 confluence | trend gate up and OBI > +0.25 and VPIN < 0.50 | 60 s timeout, or the entry condition stops holding |
| K3 confluence + pre-fill defense | as K2 | as K2, or Ret5s < −3.0 bps, or Flow5s < −0.60 |

K3's thresholds are the pasted ones. The engine's blendedSkewBps cannot be rebuilt offline, so Flow5s replaces
it.

## Pass rule (3 trials: 726 + 3 = 729)
A variant passes only if all of these hold:
1. Holdout: at least 200 trades, pooled.
2. Holdout: mean net bps per trade > 0, with a one-sided t-stat ≥ 2.0 (trades treated as independent).
3. Discovery: mean net bps per trade > 0.
4. Holdout at stress costs: mean net bps per trade > 0.

**Also reported:**
- trades, win / scratch / loss rates (scratch = |net| < 2 bps), mean gross and net;
- the exit-reason mix and fill rate;
- for K3 vs K2: how many fills the cancels avoided, and the mean outcome of those avoided fills (would they
  have lost?).

**If one passes:** the best holdout t-stat goes to forward paper in the engine, behind the existing
--mft-entries quarantine. It is still not live money (go-live bar in STRATEGY_SPEC).

**If none pass:** maker entries with these rules are closed. Win rate alone is never a reason to adopt.

## Run
- Once, on or after 2026-12-09, after the data checks above.
- The evaluator code and its synthetic tests may be written before then. It may not touch real tick data before
  the run.

## Prediction (written now)
- K1: negative net (−5 to −12 bps), like PREREG8. The win rate may still be 50–60% from the ladder and the lock.
- K2: fewer trades, slightly better gross, still negative net.
- K3: the cancels avoid many fills that would have lost. But skipping them also skips the queue position on the
  good fills, so the net improves by a few bps at most.
- I expect none to pass. The scratch share should be 20–35%: the breakeven lock raises the win rate without
  making the mean positive.
