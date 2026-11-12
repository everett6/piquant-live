# Pre-registration 8: medium-frequency trades with maker entries (committed and pushed before any round-8 code runs)

## Why this round
- **The problem:** every short-hold entry tested so far (S1, S7, G1, S8) has about zero gross edge. With taker
  execution (6 bps fee + 1 bp slippage per side, about 14 bps per round trip), each trade loses about its
  cost.
- **What's new:** two mechanisms not tested before, each with an economic reason to exist. They enter with
  maker limit orders (2 bps, internal/paper/region.go), which cuts the round trip to about 4–9 bps.
- **What the strategy must deliver:** revenue comes from individual trades that hold from minutes to a few
  hours, not from holding a position for weeks.

## Honest limits
- All Coinbase BTC history 2016–2026 has been seen by earlier rounds. It has not been seen for these
  mechanisms, but a pass is still weaker than one on untouched data.
- Forward paper trading is the real test.

## Data and splits (Coinbase BTC-USD 1m)

| Split | Dates | Role |
|---|---|---|
| discovery | 2022-12-17..2025-06-30 | selection |
| A | 2025-07..now | holdout |
| B | 2021-01..2022-12-16 | holdout |
| C | 2018–2020 | holdout |
| D | 2016–2017 | holdout |

## Fill and cost model (conservative, 1m OHLC)
- **Entry:** limit order at the signal bar's close, live for 5 bars. It fills only if price trades through
  it by 1 bp (low < P·(1−1e-4) for a buy). Fee 2 bps. No fill means no trade.
- **Take-profit:** a maker limit. It fills only if price trades through by 1 bp. Fee 2 bps.
- **Stop-loss:** a taker order at the stop price, with 1 bp slippage and a 6 bps fee. If TP and SL are both
  touched in one bar, the bar counts as the stop.
- **Time stop:** a taker exit at the bar's close, with 1 bp slippage and a 6 bps fee.
- **Funding:** 0.1 bp/h on the notional held.
- **Positions:** one at a time, 1x notional. A new signal is taken only after the previous trade exits.
- **Volatility estimate:** σ1 is the trailing 1440-bar std of 1m log returns, causal (shifted one bar).

## M1: liquidation-shock fade
- **Rationale:** forced liquidations overshoot, and liquidity providers get paid to absorb them.
- **Trigger:** a 1m return r with |r| > k·σ1. Fade it with a limit order: buy after a down shock, sell
  after an up shock.
- **Exits:** TP = 0.5·|r| from entry, SL = 1.0·|r| from entry, max hold H bars.
- **Grid:** k ∈ {4, 6} × H ∈ {30, 120}, so 4 cells.

## M2: trend pullback with a maker bid
- **Regime:** the previous day's close vs its SMA50 (the core's filter).
- **Trigger:** in an UP regime, the 1m close makes a new 240-bar low → buy limit. In a DOWN regime, a new
  240-bar high → sell limit.
- **Exits:** σh = σ1·√60. TP = q·σh, SL = 2q·σh, max hold H bars.
- **Grid:** q ∈ {1, 2} × H ∈ {120, 720}, so 4 cells.

## Selection and pass rule (8 trials charged: 689 + 8 = 697)
**Selection.** For each mechanism, pick the cell with the highest discovery net mean per trade, among cells
with ≥ 300 discovery trades. Only the selected cells go to the holdouts.

**Pass.** A selected cell passes only if all of these hold:
1. Net mean per trade > 0 on each of A, B, C and D. Each must have ≥ 100 trades; D may have fewer, and if
   so it is reported and not required.
2. Pooled holdout net mean per trade has t > 2.
3. Pooled holdout monthly geometric return at 1x is > 0.

**Also reported:**
- win rate and trades per month;
- the leverage needed to reach 7%/month geometric on the pooled holdouts, capped at 5x;
- the max drawdown at that leverage.

7% is a target, not a pass condition. One run. Results are recorded as they come out. No variants of a
failed cell.

## Prediction (written now)
- M1 has a real chance of positive gross edge, but trades are rare at k = 6.
- M2 probably has a high win rate (TP:SL = 1:2) with a near-zero mean. It is the pullback version of S8,
  which failed.
- I expect neither mechanism to reach 7%/month at ≤ 5x.

## Result (run once, 2026-11-11): both mechanisms FAIL

### Discovery, all cells (net / gross bps per trade, win rate)

| Mechanism | Cell | Trades | Net bps | Gross bps | Win rate |
|---|---|---|---|---|---|
| M1 | k=4, H=30 | 6197 | −10.0 | −4.2 | 55% |
| M1 | k=4, H=120 | 6182 | −10.1 | −4.4 | 56% |
| M1 | k=6, H=30 | 1722 | −10.8 | −5.0 | 57% |
| M1 | k=6, H=120 | 1718 | −11.0 | −5.3 | 58% |
| M2 | q=1, H=120 | 4041 | −8.5 | −2.0 | 52% |
| M2 | q=1, H=720 | 3290 | −8.0 | −2.3 | 63% |
| M2 | q=2, H=120 | 3419 | −10.4 | −2.8 | 46% |
| M2 | q=2, H=720 | 1904 | −8.4 | −1.6 | 57% |

### Holdouts for the selected cells (net bps per trade, win rate, 1x %/month)

| Selected cell | A | B | C | D | Pooled |
|---|---|---|---|---|---|
| M1 k=4, H=30 | −9.6, 55%, −18.1 | −12.4, 58%, −20.2 | −16.1, 51%, −26.7 | −14.5, 50%, −26.1 | −13.8 bps, t = −39 |
| M2 q=1, H=720 | −5.7, 64%, −6.3 | −7.5, 65%, −8.9 | −8.2, 62%, −8.1 | −22.5, 60%, −14.2 | −10.4 bps, t = −6.7 |

- No leverage up to 5x reaches 7%/month. Both lose money at any leverage.
- **Lesson.** Gross edge is negative even before fees. A resting limit order on 1m bars gets filled mainly
  when price keeps running through it (adverse selection). That costs more than the maker discount saves.
- Shocks continue more often than they revert at the 1m scale.
- Win rates of 55–65% again come with negative expectancy: the loss size outweighs them.
- No Go changes. No variants.
