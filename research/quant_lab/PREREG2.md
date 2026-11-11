# Pre-registration 2: new algorithms (written and committed before any code for them ran)

Context: round 1 (PREREG.md) failed all candidates; its holdout (2025-07..) has been seen (BTC -31%,
trend lost). Round 2 therefore adds an UNTOUCHED holdout and keeps the same harness, costs and pass rule.

## Data and splits
- Discovery: 2022-12-17 .. 2025-06-30 (same as round 1). All parameter choice happens here.
- Holdout A (seen regime): 2025-07-01 .. end of data.
- Holdout B (untouched, primary): Coinbase BTC-USD 1m from the earliest backfilled day (target 2020-01-01)
  to 2022-12-16 (includes the 2021 bull and 2022 bear). Never loaded by any test before this run. If less than
  12 months are available at run time, B is reported as insufficient rather than replaced.
- Each final candidate runs once on A and once on B, us-futures base costs, plus spot and stress profiles.

## Hypotheses
- Q1 (quantum-inspired feature, not a strategy): the anharmonic-oscillator coefficient lambda fitted to the
  trailing 60-day daily-return histogram (S3's QPL fit) adds predictive power for next-7-day realised
  volatility beyond trailing 30-day realised volatility. Test: OLS of log(next-7d RV) on log(trailing RV) with
  and without log(lambda); report the R^2 gain and lambda's t-stat on discovery, then on A and B.
  "Supported" requires R^2 gain >= 0.02 with the same sign on discovery, A and B.
- S5 volatility-managed long BTC (Moreira & Muir): weight = min(cap, target_vol / trailing realised vol),
  daily rebalance at the next open, long-only; cap 1.0 (spot) or 2.0 (futures, leverage). Grid: target_vol
  {30%, 50%, 70%} x lookback {20, 60} days = 6 configs. Benchmark: buy-and-hold.
- S6 quantum-walk-inspired diffusion regime: a quantum walk spreads ballistically (sigma ~ t), a classical walk
  diffusively (sigma ~ sqrt t). Estimate the diffusion regime with the variance ratio VR(k) of 1h returns over a
  rolling window; VR > 1 + delta -> follow the sign of the last k-hour return; VR < 1 - delta -> fade it;
  otherwise flat. Grid: delta {0.1, 0.2} x window {14, 30} days x k {12, 24} hours = 8 configs.

## Trials and pass rule
Round-2 trials: 14 strategy configs (Q1 is a single pre-specified regression). DSR is charged with all trials
so far (586 + 14 = 600). Pass rule as round 1 (holdout A AND holdout B must each satisfy: net Sharpe > 1.0,
DSR > 0.95, positive return, >= 100 trades for S6 / >= 12 months for S5; PBO < 0.30 on discovery).
For S5 the relevant comparison is risk-adjusted: it "improves on buy-and-hold" if its Sharpe beats
buy-and-hold's on discovery, A and B; this is reported separately from the pass rule.
