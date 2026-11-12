# Phase 1 findings: tail risk, regimes, and what a 7%/month + 25% max-drawdown target requires (2026-11-11)

Descriptive analysis only. No strategy was tested and no trial was charged (still 734). Script:
`research/quant_lab/diag_tails.py`; numbers in `research/quant_lab/results/diag_tails.json`. Window 2016-01-01 to
2026-11.

## Data audit
- **The `.pqbin` "tick archives" are not market data.** Each monthly file holds about 41,000 machine-generated
  ticks covering 5.5 minutes (09:30:00–09:35:30 UTC on the 1st), with trade IDs in one unbroken sequence across
  files. They cannot support any tail, VPIN or order-flow estimate. Real ticks exist only from 2026-11-11
  (`~/piquant-collect`).
- **1-minute horizon:** real Coinbase 1m candles (5.7 M bars for BTC, 5.3 M for ETH), with the one known bad BTC bar
  ($0.06, 2017-04-15) removed by the PREREG15 rule.
- **4h and daily CSVs** (`data/historical`) agree with Coinbase daily closes to 0.07% (median).
- `cmd/piquant/unlock.go` clears the kill-switch lock. It has nothing to do with token unlocks.

## Tail risk (log returns; losses)
| Coin, horizon | Std | VaR 99% | CVaR 99% | VaR 99.9% | CVaR 99.9% | Worst |
|---|---|---|---|---|---|---|
| BTC 1m | 0.10% | −0.28% | −0.47% | −0.71% | −1.08% | −21.8% |
| BTC 4h | 1.45% | −4.40% | −6.30% | −8.64% | −11.85% | −22.9% |
| BTC 1d | 3.50% | −10.45% | −14.19% | −16.65% | −27.48% | −50.3% |
| ETH 1m | 0.16% | −0.41% | −0.70% | −1.08% | −1.74% | −41.6% |
| ETH 4h | 1.83% | −5.64% | −7.92% | −11.35% | −14.49% | −25.0% |
| ETH 1d | 5.06% | −14.32% | −20.23% | −29.43% | −38.65% | −59.1% |

- **Jumps:** moves beyond 4 trailing standard deviations happen about 2 times a year each way on daily bars and
  about 10 times a year on 4h bars. A normal distribution predicts 0.01 and 0.07, so jumps are roughly 150 times
  more frequent than normal.
- **Volatility clusters; direction does not.** The autocorrelation of absolute returns is 0.20–0.26 at lag 1 on
  4h and daily bars and still 0.09–0.17 at lag 20. The autocorrelation of returns themselves is −0.03 to −0.04.
  So volatility can be forecast, but the next move's direction cannot.
- **Hurst exponent** (rolling 256 bars): median 0.485–0.495 on both 4h and daily. It is above 0.56 in only 2–6% of
  windows and below 0.46 in 17–31%. A rule that trades trend only when H > 0.56 would be active a few percent of
  the time.

## Gap risk: what a 25% floor can and cannot guarantee
A floor is only as good as the fall that can happen before a de-risking order fills.

| Reaction time | BTC 99.9% | BTC 99.99% | BTC worst | ETH 99.9% | ETH 99.99% | ETH worst |
|---|---|---|---|---|---|---|
| 1 minute | −0.7% | −1.5% | −19.6% | −1.1% | −2.7% | −34.0% |
| 5 minutes | −1.6% | −3.4% | −23.0% | −2.2% | −4.8% | −34.9% |
| 1 hour | −5.0% | −9.8% | −22.8% | −6.8% | −13.0% | −34.9% |
| 6 hours | −9.9% | −17.1% | −34.2% | −13.9% | −23.0% | −37.6% |
| Daily (prev close to low) | −23.5% | n/a | −41.5% | −39.6% | n/a | −99.97% |

- With a 25% cushion at the high-water mark, the floor holds through the worst observed 1-minute gap only if
  exposure is at most 0.25 / 0.196 = **1.27x on BTC** and 0.25 / 0.34 = **0.73x on ETH**. Halfway into the drawdown
  the limits halve.
- So "mathematically inviolable" is not available. A CPPI rule bounds the drawdown only up to the gap it assumed,
  and the worst gap sets leverage near 1x at best.
- This rule was already tested as Grossman–Zhou sizing (PREREG14 and 15): the drawdown held near −30%, the return
  fell to about +1%/month, and one gap pinned it in cash permanently.

## What the target requires
For a strategy with normally distributed daily returns (the best case: no jumps, no costs), levered so the median
path compounds at 7%/month, simulated over 5 years:

| Annual Sharpe | Annual volatility needed | Median max drawdown | 99th-percentile max drawdown | Paths within 25% |
|---|---|---|---|---|
| 1.0 | not reachable at any leverage | | | |
| 1.5 | 71% | 60% | 87% | 0% |
| 2.0 | 46% | 38% | 63% | 2% |
| 3.0 | 28% | 20% | 35% | 83% |
| 4.0 | 21% | 12% | 22% | 99.9% |

- **7%/month with a 25% cap needs a Sharpe ratio of about 4.**
- Measured here: BTC buy-and-hold 1.05, ETH 1.21, the P1 trend core 1.49 (in-sample, at 97% volatility).
- Sizing rules (CPPI, Kelly fractions, volatility targets, ratchets, brakes) move a strategy along its own
  return-versus-drawdown line. They do not raise its Sharpe ratio. Only new, uncorrelated sources of return do.

## Blueprint: each requested component against the evidence
| Requested | Status |
|---|---|
| CPPI floor at 0.75 × high-water mark, drawdown-velocity brake | Tested as PREREG14/15 (continuous) and PREREG12 Q1 (step brake). Failed; closed |
| Volatility-targeted fractional Kelly, EWMA covariance | Volatility targeting tested as PREREG11 P2: −88%. Failed; closed. BTC–ETH daily correlation is 0.64 since 2016 and 0.82 since 2021, so covariance weighting has little to diversify in a two-coin book |
| VPIN / order-flow circuit breaker | Needs real ticks. Covered by PREREG17 (VPIN and book filters), due 2026-12-09 |
| Profit ratchets at +4.5% and +7% | A ratchet changes when gains are kept, not whether the entries have edge |
| Regime classifier (BOCPD + Hurst + volatility) | Not tested. Hurst is above 0.56 only 2–6% of the time, so the "bull trend" state would rarely switch on |
| Multi-horizon momentum ensemble, Kalman pullback entries | Daily ensemble tested in PREREG14/15 S3 (failed with the floor). The engine's intraday Kalman layer failed PREREG8–10 |
| Predictive funding carry | Always-on carry passed PREREG19 narrowly (+5%/yr stress). The conditional version failed. Paper ledger running |
| Avellaneda–Stoikov execution, impact model | Engine has a GLFT quoting model already (quarantined). Maker fills are tested in PREREG17 |
| Walk-forward optimisation, CPCV, 10,000-path bootstrap | Allowed only inside a pre-registered protocol; every configuration tried counts as a trial |
| Zero-allocation hot path | Engineering only; does not change returns. The hot path is not the bottleneck while entries are quarantined |
| "Empirically calibrated" config values | Not changed. Calibrating production parameters to the history is the overfitting the project rules forbid |

## What is still open
1. **2026-12-09:** PREREG17 (maker entries with order-book filters) and PREREG18 (lead-lag, flow momentum) on real
   ticks. These are the only tests that could add a new return source.
2. **Forward records:** the trend ledgers (P1, R3, D2) and the carry ledger.
3. **A target that the evidence supports.** With the measured Sharpe of about 1.5, a 25% drawdown cap is consistent
   with about 1.3%/month (99th-percentile drawdown 23–26% at 10–12% volatility, in the same best-case simulation), and
   7%/month is consistent with drawdowns of 60% or more.
