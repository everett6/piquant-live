# Pre-registration 4: a ≥55% win-rate geometry for the live entry, and an LFM3 applicability test

This was written, committed and pushed before round 3's results were read and before any round-4 code ran.

## G1: live-engine entry with a win-rate-first geometry

BREAKEVEN.md eq. (3): for an entry with no edge, the gross win rate is S / (T + S). A ≥55% win rate is
therefore a geometry choice. It is not evidence of profit. G1 picks the geometry that reaches ≥55% while
losing the least, and tests both claims on holdouts.

**Setup**
- Entry: the round-1 S1 live-engine momentum score, mode +1.
- Bars and exits: 1m bars, max hold 240 bars, loser time stop 45 bars (same as round-1 S1).
- Costs: us-futures (taker both legs, 14 bps round trip).

**Grid**
- Threshold th: {0.65, 0.75}.
- Gross (T, S) in bps: (20,40), (20,60), (20,80), (30,60), (30,80), (40,60), (40,80). These are all pairs
  with S / (T + S) ≥ 0.58. The margin covers the stop-first and gap bias.
- 14 configs in total.

**Comparator (not selected; charged as 1 trial)**
- The current live geometry in gross terms: T = 39 (35 net + 4 maker fees), S = 30.
- Same entry and same th as the G1 pick.

**Selection**
- On discovery, keep configs whose win rate is ≥ 55%.
- Pick the highest net Sharpe among them.

**Hypotheses (one run each on holdouts A, B, C as in PREREG3)**

| Hypothesis | Rule | Prediction |
|---|---|---|
| H-win | G1 pick win rate ≥ 55% on A, B and C | holds |
| H-profit | the round-1 pass rule: PBO < 0.30; on each holdout Sharpe > 1, DSR > 0.95, positive return, ≥ 100 trades | fails (round 1 measured ~0 gross edge for this entry) |

**Live adoption rule (decided now)**

The live flags change to the G1 pick only if all three hold:
- (a) H-win holds on A, B and C.
- (b) G1's mean net bps per trade ≥ the comparator's on at least 2 of 3 holdouts.
- (c) G1's total return ≥ the comparator's on at least 2 of 3 holdouts.

If adopted, the flags become `--target-profit (T − 4)` and `--stop-loss S`. Otherwise the live geometry stays.
Either way, the report states the expected net bps per trade next to the win rate.

**Trials:** 15 new configs; DSR is charged with 675 trials.

## L1: does the distilled LFM3 news model carry usable information?

**Sample**
- 4,000 headlines drawn with seed 11 from labels.parquet.
- Split "val" (2024-07-01 .. 2025-02), sources cryptonewsimpact and coindesk.
- Must have a 60m return label.
- Excludes every headline the Jan teacher labeled. That excludes all LFM3 training rows and the earlier
  eval sample.
- Never scored by LFM3 before.

**Model and metrics**
- Model: piquant-news-lfm3-v1, merged HF weights, greedy decoding with the training prompt.
- Horizons h in {60, 240, 1440} minutes of BTC-USD return after the headline timestamp. 1440m is computed
  from the 1m candles.
- Correlations:
  - Spearman(direction, ret_h).
  - Spearman(magnitude, |ret_h|).
  - That is 6 correlations. p-values use the normal approximation; Bonferroni α = 0.01 / 6.
- Tradability: the mean signed return sign(direction) × ret_h on headlines with |direction| ≥ 0.5, minus
  14 bps.

**Verdict**

| Verdict | Condition | What it means |
|---|---|---|
| Applicable as a signal | a direction ρ ≥ 0.05, significant, and positive net tradability at that horizon | — |
| Applicable as a risk filter only | a magnitude ρ ≥ 0.05, significant | no direction result passes |
| Scrap | anything else | LFM3 is not integrated into the engine; the files stay on disk; the result is recorded |

The CNI dataset's own LLM labels (teacher_direction / teacher_magnitude) on the same rows are reported for
reference. They are not part of the rule.
