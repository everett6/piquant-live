# AFK report — 2026-11-11

## Bottom line
- **No strategy passed its pre-registered holdout.** That covers 10 strategy families, including 4 quantum-inspired ones, across 600 trials.
- **The 60% win-rate target is only breakeven** under the live TP/SL geometry: (30 + 9 bps cost) / 65 = 60%.
- **The custom news model works.** It is fast and agrees with the teacher about 80% of the time. Like the teacher, it has no measurable predictive power for 15m–4h BTC returns.

## Backtests (full tables: research/quant_lab/results/REPORT.md)

| Strategy | Discovery Sharpe | Holdout Sharpe | Verdict |
|---|---|---|---|
| Buy & hold BTC | 1.76 | -0.45 | benchmark |
| Live engine short-horizon core | -24.4 | -27.2 | FAIL (zero gross edge) |
| 1h trend following | 1.73 | -0.71 | FAIL (real edge, died in 2025-26 bear market) |
| QHO price levels | 0.24 | -0.95 | FAIL |
| Simulated quantum annealing optimizer | 1.74 | -0.16 | FAIL (no better than random search: 1.73) |
| QHO λ as volatility feature | R² gain +0.004 | +0.002 / +0.004 | FAIL (no information) |
| Vol-managed BTC | 1.66 | -0.76 (A), -0.81 (B) | FAIL (PBO 0.49 = overfit) |
| Quantum-walk diffusion regime | -2.69 | -3.68 / -1.72 | FAIL |

Holdout B (2021-01 .. 2022-12, 23.5 months) had never been touched by any test before round 2.

## Distilled news model (piquant-news-lfm3-v1, LFM3-1.7B + LoRA)
- Files: `~/models/piquant-news-lfm3-v1/` (Q4_K_M GGUF 1.0 GB, bf16 GGUF, merged HF, adapter, eval.json).
- Test set, n = 600:
  - JSON validity: 100%.
  - Event-type accuracy vs Jan-4B teacher: 80%.
  - Direction-sign agreement with the teacher: 78.5%.
  - Direction MAE: 0.147. Magnitude MAE: 0.112.
- Speed: 0.085 s per headline on GPU; ~350 tok/s with llama.cpp.
- Direction vs katelabs human labels:

  | Model | Correlation |
  |---|---|
  | Student | 0.72 |
  | Teacher | 0.77 |

- Correlation with BTC returns at 15m / 60m / 240m: all between -0.11 and +0.01. Headlines carry no tradable short-horizon signal in this data.

## Done after "do it all"
1. **Venue fix (commit 0d88e5d).** `--feed-venue auto` makes Coinbase the primary feed for us-futures.
   - Fills, VPIN, trade flow and the L1 book now come from Coinbase instead of Binance.US.
   - The ticker `side` field was verified live as the taker side.
2. **Pushed** `release/production-v1` to origin.
3. **Deployed to the Pi** with the VPIN warm-up fix and the venue fix.
   - Health OK, app = 0d88e5d, quoting active on the Coinbase feed.
   - Backup: `/usr/local/bin/piquant.prev-4f58741`.
4. **Desktop jobs are now persistent user services:** `piquant-ollama`, `piquant-collect`, `piquant-extract`. All three are enabled and active, and linger is already on.

## What this means for the fund
- Live paper trading on the Pi continues as a measurement. Nothing tested so far shows edge after US costs.
- **Options that could plausibly help:**
  - Widen TP relative to SL so breakeven drops below 50%.
  - Use the trend model only in confirmed uptrend regimes.
  - Size buy & hold by volatility on spot (no funding cost).
- Each of these needs a new pre-registration before it is tested.
- **Overfitting budget:** the trial count of 600 is past the 400 pre-registered. Further searches on this data will mostly find noise.
