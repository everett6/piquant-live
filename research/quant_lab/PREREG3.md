# Pre-registration 3: wider TP/SL geometry (committed and pushed before any round-3 code runs)

Context:
- Rounds 1–2 failed. 600 trials have been charged.
- BREAKEVEN.md proves that bracket geometry alone gives zero expected gross return when the entry has no drift
  (optional stopping). Under that model the gross win rate is S / (T + S).
- This round tests that theory directly (T0), then tests wider brackets on (S7) the live entry signal and
  (S8) a trend-regime entry. S8 is the only entry where discovery showed drift.

## Data and splits (Coinbase BTC-USD 1m)
- Discovery: 2022-12-17 .. 2025-06-30. All parameter choice happens here.
- Holdout A: 2025-07-01 .. end of data. Seen in rounds 1–2.
- Holdout B: 2021-01-01 .. 2022-12-16. Seen once, in round 2 (S5/S6 only).
- Holdout C (untouched, primary): 2018-01-01 .. 2020-12-31.
  - Backfilled after this pre-registration was written. Never loaded by any test before this run.
  - If fewer than 24 months are available at run time, C is reported as insufficient and every candidate
    FAILS. No replacement window is used.
- Features are computed causally on the full continuous series, then split by entry time.

## T0: theory check (diagnostic, no selection, not charged as trials)
- Setup: random side (seeded, 50/50 long/short) entered at the next open whenever flat, on discovery.
- Bracket grid: TP in {20, 35, 60, 100, 150, 250} bps x SL in {20, 30, 50, 100} bps = 24 cells.
  Max hold 1440 bars, no time stop, us-futures costs.
- Matrices reported per cell:
  - realised gross win rate, excluding max-hold exits;
  - predicted S / (T + S);
  - mean gross and mean net bps per trade;
  - net breakeven p* = (S + c) / (T + S), with c = 14 bps taker round trip;
  - realised win rate minus p*.
- Theory "supported" if at least 90% of cells have both:
  - |win rate - S / (T + S)| <= 0.03;
  - |mean gross| <= 3 bps.

## S7: live entry signal with wider brackets (1m bars)
- Entry: the round-1 S1 momentum score (live-engine style), mode +1.
- Grid: threshold th in {0.65, 0.75} x TP in {60, 100, 150, 250} bps x SL in {20, 30, 50} bps = 24 configs.
- Exits: max hold 1440 bars, no time stop.
- Prediction from BREAKEVEN.md: fails (round 1 measured about 0 gross edge for this signal).

## S8: trend regime with wide brackets (5m bars)
- Entry: long when EMA(fast) > EMA(slow) of 1h closes. The regime uses the last completed hour only.
  Re-enter at the next open after an exit while the regime holds. Long only.
- Grid: fast in {12, 24} x slow in {168, 336} x TP in {150, 300, 600} bps x SL in {50, 100, 200} bps
  = 36 configs.
- Exits: max hold 14 days (4032 bars), no time stop.

## Trials and pass rule
- 60 new configs. DSR is charged with 660 trials.
- Candidate per strategy: the discovery-best net Sharpe (us-futures costs). Then one run each on A, B and C.
- PASS requires both:
  - PBO < 0.30 on discovery;
  - on each of A, B and C: us-futures net Sharpe > 1.0, DSR > 0.95, positive total return, >= 100 trades.
- us-spot (long-only, no funding) and us-futures-stress (1 bp/h funding) results are reported for the
  finals. They are not part of the rule.
- Costs are taker on both sides (14 bps round trip). That is conservative relative to the live maker entry;
  this is noted, not adjusted.
- One run. Failures are recorded. No variants of a failed config are tested on these holdouts.
