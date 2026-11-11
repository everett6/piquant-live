# Pre-registration: quant lab block 2026-11-11 (written and committed before any backtest ran)

## Question
Are PiQuant's signal families sound under US retail conditions, do any of them (or quantum-inspired
alternatives) have net edge after realistic costs, and how much of any edge is overfitting?

## Data
- Coinbase Exchange BTC-USD 1-minute candles collected by `piquant collect` (public API), 2022-12-17 onward.
- Discovery (in-sample) period: start .. 2025-06-30 23:59 UTC. All tuning, walk-forward and PBO use this only.
- Holdout (out-of-sample): 2025-07-01 .. end of data. Each final candidate is run on it exactly ONCE.
  Failed holdout results are recorded; no variants of a failed final candidate are re-tested on the holdout.

## Execution and cost model (US conditions, from internal/paper/region.go)
- Signal computed on bar close t; entry fills at the open of bar t+1 (no same-bar look-ahead).
- Profile `us-futures` (Coinbase CFM nano BTC, leverage available): taker 6 bps/side + 1 bp slippage;
  maker 2 bps/side (only where a resting limit is plausibly filled: price trades through the limit);
  funding/financing 1 bp per hour held, charged regardless of side (conservative).
- Profile `us-spot` (Binance.US, 1x, long-only): taker 2 bps + 1 bp slippage; no shorting.
- Stops/targets checked on later bars' high/low; if both are touched in the same bar the STOP is assumed first.
- Leverage is applied only as a post-hoc scaling of a unit-leverage return series (reported separately);
  it never changes signals.

## Strategies (fixed list)
- S0 buy-and-hold BTC (benchmark).
- S1 "engine-style" short-horizon momentum: blended 1m/5m/15m returns -> conviction; enter on threshold;
  vol-scaled TP/SL (live defaults TP 35 bps, SL 30 bps), 45-minute time stop on losers. Mirrors the live
  engine's candle-observable parts (order-book imbalance cannot be tested on candles).
- S2 classical trend following on 1h bars: fast/slow EMA or breakout, volatility-targeted, long/short (futures)
  or long/flat (spot).
- S3 quantum harmonic oscillator (QHO) levels (Lee, "Quantum Finance"): rolling return distribution mapped to a
  QHO potential; energy-level price bands E_n ~ (n + 1/2); fade moves beyond the outer level back toward the
  ground state, gated off in trending regimes.
- S4 simulated quantum annealing (path-integral Monte Carlo over Trotter replicas with a transverse-field
  schedule) as a parameter-search method for S2/S3, compared with random search at equal evaluation budget.

## Metrics
Net return, annualized Sharpe from daily P&L, max drawdown, trades, win rate, average win/loss, profit factor,
share of calendar months with return >= 7%.

## Overfitting controls
- Walk-forward on discovery: rolling 12-month train / 3-month test windows; parameters chosen on train only.
- Deflated Sharpe Ratio (Bailey & Lopez de Prado) using the total number of configurations tried.
- Probability of Backtest Overfitting via CSCV with 10 blocks over each strategy's parameter grid.
- Trial budget: at most 400 parameter configurations in total across S1-S4; every one counts in the DSR.

## Pass rule (to recommend a strategy for paper deployment)
All of: holdout net Sharpe > 1.0 at unit leverage; DSR > 0.95; PBO < 0.30; >= 100 holdout trades;
positive holdout return in the `us-futures` cost profile. Anything else is reported as FAIL, with numbers.
