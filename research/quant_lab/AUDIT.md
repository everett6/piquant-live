# Algorithm audit vs US retail conditions (2026-11-11)

Conditions: Binance.US spot (1x, long-only, ~0.8% of Coinbase's BTC volume) or Coinbase CFM nano futures
(2 bps maker / 6 bps taker, hourly funding); orders ~30 ms, Binance.US feed ~130 ms; $200 paper account.
Evidence: pre-registered backtests in `results/run1.json` and `results/s4.json` (see PREREG.md) plus code reading.

## 1. Short-horizon momentum core: no edge (backtest)
The candle-observable part of the live engine (blended 1/5/15-minute momentum -> conviction -> vol-scaled
TP/SL brackets, 45-minute time stop) was tested over 96 configurations, momentum and fade, on 2.5 years of
Coinbase 1m data. **Average gross edge per trade was -0.11 bps; 0 of 96 configurations were profitable even
before compounding; the final candidate lost 100% on the holdout.** Costs of 9-14 bps per round trip are
40-70% of a typical 15-minute move (median sigma 20 bps), so any signal at this horizon needs a real gross
edge of several bps per trade, and this one has none. The order-book-imbalance and Coinbase lead-lag inputs
cannot be tested on candles; they are the only parts that could still carry edge, and they are now being
recorded (1 s book snapshots) so they can be tested after a few weeks of data.

## 2. Payoff geometry: 60% win rate is only breakeven
With live defaults TP 35 bps / SL 30 bps, maker entry (2 bps) and taker exit (6 + 1 bps):
breakeven win rate = (SL + cost) / (TP + SL) = (30 + 9) / 65 = **60%**. The 60% target is the cost of staying
flat, not a profit level. Any profitable configuration with this geometry needs >60% wins, or a wider TP/SL
ratio with a lower (but sufficient) win rate. Trend strategies that did show discovery edge win only 20-25%
of trades; high-win-rate structures (small TP, wide SL) have negative skew and fail on gaps.

## 3. Microstructure inputs read the wrong venue (code)
Flow features (order-book imbalance, trade-flow imbalance, VPIN, Hawkes, Kyle lambda) come from the Binance.US
stream, which carries ~1.3 BTC/hour (~$98k/hour) versus ~$11.7M/hour on Coinbase measured the same hour. The
`us-futures` profile executes on Coinbase CFM but simulates fills against the Binance.US book. Signals built
on <1% of US flow are mostly noise, and fill realism is venue-mismatched. Recommendation: drive flow features
and fills from the Coinbase stream (already ingested for lead-lag) when the region is `us-futures`.

## 4. VPIN toxicity filter could never engage (fixed, commit 0848597)
Buckets were sized in coins (25 BTC; the comment said $25k). At Binance.US volume a 50-bucket window needed
~40 days, so the VPIN > 0.70 entry guard never fired during a 30-day run, and the global calculator summed BTC
and ETH coin counts. Now sized in USD notional ($10k per symbol). Not yet deployed to the Pi (would change the
running 30-day evaluation mid-way).

## 5. News signal (fixed earlier, commit a821ff4)
Substring keyword matching produced false exploit bid-kills ("hackathon") and false sentiment; fixed with
whole-word matching and a crypto relevance gate. Separately, 25,406 katelabs headline scores showed ~0
correlation with BTC 15m-4h returns: news is a volatility/risk input, not a direction signal.

## 6. Funding cost assumption
`USFuturesFundingBpsPerH = 1.0` (= ~88%/yr) is a cap-style figure. For the live bot's <=45-minute holds it is a
harmless overcharge (<1 bp/trade); for any multi-hour strategy it dominates results. Treat as a stress case.

## 7. Overfitting
- The repo's tick backtester and the Kaggle cron's `optimal_params.json` (reported Sharpe 739, 99.8% wins) are
  not credible: fills are unrealistic. `--cloud-params=false` stays on for the live run.
- In the new harness, PBO for 1h trend following was 0.09 (low): its discovery edge was not a fitting artefact,
  yet it still lost on the 2025-07..2026-11 holdout as BTC fell 31%. Regime risk, not overfitting.
- Better optimisers do not create edge: simulated quantum annealing found marginally better in-sample
  parameters than random search (Sharpe 1.74 vs 1.73) and both failed the holdout.

## Verdict
Mechanically the engine is now sound (honest exits, costs, risk caps, latency regimes, fixed news/VPIN), but its
alpha is not demonstrated: the testable core has zero gross edge after US costs, and the 7%/month + 60% win-rate
targets are not supported by any strategy tested here. The live paper run should be judged on its own trades;
the most promising untested lever is Coinbase-venue order flow, which needs the L2 data now being collected.
