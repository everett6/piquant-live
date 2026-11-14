# Pre-registration 34: two sizing rules borrowed from quantum mechanics, interference and tunnelling (committed before any round-34 code reads real data)

Asked for by the owner on 2026-11-13: new strategies, novel, and inspired by quantum computers. This lifts the
pause agreed earlier the same day. Only synthetic tests had run when this was committed.

## What "quantum-inspired" means here, stated plainly
- Nothing runs on a quantum computer. These are ordinary formulas whose shape is borrowed from quantum mechanics.
  The label adds no edge. Each rule stands or falls on its market hypothesis.
- **Four quantum-inspired ideas are already closed** and are not repeated: oscillator price levels (PREREG1 S3),
  simulated quantum annealing as an optimiser (S4), the anharmonic coefficient as a volatility feature (PREREG2 Q1)
  and the quantum-walk diffusion regime (S6). All failed.
- **The hypothesis both rules share:** P1 loses its money near the SMA crossing. PREREG26 found the −84% drawdown is
  whipsaw bleed, not crash days. Both rules hold less when the price is close to the line and full size when it is
  far above. Neither has been tested here.

## QA, interference sizing (the Born rule)
In quantum mechanics a state's probability is the **square** of the sum of its amplitudes. Paths that agree add up;
paths that disagree cancel.

The four clocks of PREREG30 are the paths. Each clock k votes `a_k = 1` (its close is above its SMA50) or 0.
PREREG30's split adds the votes: position `= 2 × (Σ a_k / 4)`. QA squares the sum:

    position = 2 × (Σ a_k / 4)²        →   0, 0.125, 0.5, 1.125 or 2x for 0..4 clocks on

Full agreement is 2x, as in P1. Disagreement, which happens only when the price is within a day's move of the SMA,
is cut hard: two clocks of four give 0.5x where the split gives 1x.

## QB, tunnelling sizing
A particle meets a barrier of height and width `d`. The chance it passes through falls exponentially: `T = e^(−2κd)`.

Here the barrier is the gap between the price and its SMA50, measured in units of one day's typical move:

    d = ln(close / SMA50),   σ = standard deviation of the last 30 daily returns,   T = exp(−2·d/σ)
    position = 2 × (1 − T)   while close > SMA50, else 0

`T` stands for the chance of falling back through the line. One σ above the line gives 1.73x; a quarter σ gives
0.79x; right at the line the position is near zero. The trend decision is P1's, unchanged.

## Limits stated before the run
- **What I already know:** PREREG30's numbers (the split +8.61%/mo, DD −82.3%, Sharpe 1.499; clocks 98.3%
  correlated; the split's flat months were worse than P1's). I have not looked at returns by distance from the SMA
  or by clock agreement.
- **The clocks agree almost always,** so QA differs from the split on few bars. Any effect will be small.
- **Both rules hold less on average.** PREREG31–32 showed a lower-leverage rule can look good only because it is
  smaller. So both are judged against "the line": the same base rule at a constant size equal to the variant's
  average.
- **QB trades a little every day.** Its turnover is charged in full.
- Both are price rules from the same family as P1. They cannot fix bear months and are not asked to.

## Data (already on disk)
Coinbase BTC-USD and ETH-USD 1-minute candles (`lab.load_candles`), as PREREG30. Full sample 2016–2026.

## Rules (2 fixed trials, 760 + 2 = 762; nothing is searched)
- **QA** on PREREG30's 6-hour grid: each coin's position is `2 × (clocks on / 4)²`, re-set every 6 hours
  (`strategies30.clock_held`, `grid_returns`). Base rule and comparator: the split E4.
- **QB** on daily bars: the formula above at each daily close, held over the next day (`lab.position_returns`).
  Base rule and comparator: P1.
- BTC and ETH in equal halves. Costs: 6 + 1 bps per unit of turnover, funding 0.1 bp/h; stress 1 bp/h.
- **The line:** the base rule's positions multiplied by `mean(variant position) / mean(base position)` over
  2016–2026 (average of the two coins).

The square, the four clocks, the factor 2 in the exponent, 30 days and SMA50 are fixed here.

## Pass rule (each variant alone, 2016–2026, against its own line and base rule)
1. **Return:** geometric monthly return ≥ the line's.
2. **Drawdown:** max drawdown no worse than the line's.
3. **Less noise:** Sharpe ratio higher than the base rule's.
4. **Bad markets:** mean bear-month return no worse than the line's.
5. **Recent data:** period A (2025-07 on) geometric return > 0.
6. **Stress:** full-sample geometric return at stress costs > 0.

**Also reported:** average leverage; share of bars where QA differs from the split; turnover; flat and bull months;
every period; 2022; base rule and line beside each variant.

**If one passes:** it can join the paper ledger beside P1, paper only (a Go change and a Pi deploy, asked first).
**If both fail:** sizing by distance from the SMA is closed.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- QA: average leverage a few percent under the split's; return and drawdown within a few tenths of its line. The
  six checks are close to coin flips on tiny differences. Chance of a pass: about 1 in 5.
- QB: average leverage about 1.6–1.7x on trend-on days. If days just above the line really are the losing ones, it
  beats its line; trend research says returns are also thinnest there, so I lean yes on drawdown, unsure on return.
  Chance of a pass: about 1 in 4.
