# Pre-registration 18: cross-venue lead-lag and trade-flow momentum on tick data (committed before any round-18 code runs on real data)

## Why
The owner's first priority was the price gap between exchanges ("lead-lag first", owner requirements). Two other
things need recorded trades and books, which 1m bars cannot give:
- whether one US venue's price moves first;
- whether short-term signed trade flow predicts the next minutes.

Tick and 1-second top-10 book recording has run since 2026-11-11 for Coinbase, Kraken and Binance.US. These rules
are fixed now, before the data needed to judge them exists, so none can be tuned to it.

## Data (same window, split and exclusions as PREREG17)
- **Window:** 2026-11-12 00:00 UTC to 2026-12-09 00:00 UTC.
  - Discovery to 2026-11-25 00:00, holdout after.
  - Nothing is fitted, so discovery is a second check.
- **Coins:** BTC and ETH; trades pooled across coins.
- **Books:** mid = (best bid + best ask) / 2 from the 1 s snapshots (Coinbase `BTC-USD`, Kraken `kraken-BTC-USD`, and
  so on).
- **Excluded:** no new entries in a minute with a trade_id gap or a book gap over 5 s on any venue the rule uses,
  or in a 10-minute window without an OK clock check (|offset| ≤ 250 ms) in clock_offsets.jsonl. Open trades still
  exit.
- **Entry and exit fills:**
  - at the first book snapshot of the traded venue that is at least 200 ms after the decision time;
  - a buy pays that snapshot's best ask, a sell receives its best bid, so the spread is always paid;
  - plus the fees below.

## Rules (3 fixed trials, no parameter search)
- **L1, Coinbase leads Kraken (trade Kraken spot, long only):**
  - **Signal:** at a Coinbase snapshot, the Coinbase mid is up ≥ 5 bps vs its snapshot 1 s earlier, while
    Kraken's latest mid is up less than half as much over the same second.
  - **Trade:** buy on Kraken and exit 30 s after the entry decision.
  - **Costs:** Kraken Pro lowest retail tier, 40 bps taker per side. Stress: 45.
  - Spot shorting is not available to US retail, so this is long only.
- **L2, Kraken leads Coinbase (trade the Coinbase nano-futures proxy, long and short):**
  - **Signal:** the Kraken mid moves ≥ 5 bps in 1 s, while Coinbase's latest mid moves less than half as much in
    the same direction.
  - **Trade:** trade Coinbase in Kraken's direction and exit 30 s after the entry decision.
  - **Costs:** us-futures, 6 bps taker + 1 bp per side. Stress: 6 + 3.
  - Coinbase spot books stand in for the futures book, as in the engine. This is a known approximation.
- **F1, flow momentum (Coinbase futures proxy, long and short):**
  - **Signal:** at each whole minute, flow60 = (buy − sell aggressor volume) / total over the last 60 s.
    - It needs |flow60| ≥ 0.50.
    - It also needs that minute's volume ≥ the median 1-minute volume of the previous UTC day (the first day
      uses its own median).
  - **Trade:** trade in the flow's direction and exit 5 minutes after the entry decision.
  - **Costs:** us-futures, 6 + 1 per side. Stress: 6 + 3.

**For all three:** one open trade per coin per rule. Signals during a trade are ignored.

## Pass rule (3 trials: 729 + 3 = 732)
A rule passes only if all of these hold:
1. Holdout: at least 200 trades.
2. Holdout: mean net bps per trade > 0, with a one-sided t-stat ≥ 2.0 (trades treated as independent).
3. Discovery: mean net bps per trade > 0.
4. Holdout at stress costs: mean net bps per trade > 0.

**Also reported:**
- for L1 and L2, the mean signed move of the lagging venue's mid 1, 5 and 30 s after the signal (gross, before
  spread and fees). This shows whether a lead exists even if it cannot be traded;
- win rate, trades per day, the share of the gross lost to spread, and the worst trade.

**If one passes:** forward paper only, behind a new flag, until the go-live bar in STRATEGY_SPEC. L1 would also
need a Kraken order connector, which does not exist.

**If none pass:** these three rules are closed. The lead measurements stay as a description of the venues.

## Run
- Once, on or after 2026-12-09, together with PREREG17, after the data checks.
- The evaluator and its synthetic tests may be written before then, without touching real tick data.

## Prediction (written now)
- **L1:** a real lead exists. Kraken follows Coinbase by a few bps within seconds, but 80 bps of round-trip fees
  plus the spread swamp it. Strongly negative net.
- **L2:** a weaker lead in that direction (Coinbase is the bigger venue). Net about −10 to −15 bps.
- **F1:** gross a few bps either way. Net about −12 bps.
- I expect none to pass. If any lead is tradable, it is the futures-cost route (L2), not Kraken spot.
