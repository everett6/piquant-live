# PREREG59: Q51 multi-asset vol-parity trend rotation on ETFs (pre-registered, no data read by this code)

Written and pushed 2026-11-27 before `run_round59.py` exists or reads any data.

## Why this is allowed
PREREG48 (piquant-stocks, 2026-11-18) closed the trend-on-stocks family after the dual-speed rule failed as a
20% SPY/QQQ sleeve. On 2026-11-27 the owner reopened it for exactly one test ("Yes, reopen once"). This is that
test. Whatever the result, the family closes again: no further ETF trend variants (universes, weights,
windows, settings) under any number.

## Hypothesis
Blend A's trend core with Q51's multi-asset inverse-volatility weighting (which passed on BTC+ETH+SOL in
PREREG51), run on its own across many ETFs, beats a passive benchmark on risk-adjusted return without the
return give-up that failed PREREG48. PREREG48 tested one ETF at a time beside crypto; this tests rotation
across a diversified ETF set, standalone.

## Rule (copied from crypto, not tuned)
For each ETF, on its own dividend-adjusted daily close (cache from `etf_discover.load_etf_closes`, the
daily-checked loader):
- position_i = 0.5 x `strategies35.coherence_positions(lev=1)` + 0.5 x `strategies40.r3_fastexit_qd_positions(lev=1)`
  (SMA50, exit SMA20, tau 5), so 0..1. Computed only on the ETF's own history; 0 before it has data.
- weight_i = (1 / 20-day return std) / sum over ETFs with a valid std (min 10 obs), as in `run_round51`.
- target exposure_i = weight_i x position_i x taper, taper = `strategies36.soft_taper(dd, 0.06, 0.20, floor 0.40)`
  on the strategy's own drawdown. Set at close t, held over t+1. Sum of exposures <= 1 (no margin).
- Idle capital earns the 3-month T-bill (DTB3 / 252 per trading day; 0 before 2014, when the file starts).
- Cost 2 bps per unit traded (stress 10 bps). No carry sleeve (crypto funding has no ETF counterpart).

## Trials (2; repo counter 838 -> 840)
- **E1, multi-asset:** the 28-ETF universe minus SHY (a T-bill substitute) = 27 ETFs: broad, Nasdaq/tech,
  sectors, IEF, TLT, HYG, LQD, GLD, SLV, DBC, UUP. Benchmark: 60% SPY / 40% IEF, rebalanced daily, no cost.
- **E2, equity-only:** SPY QQQ IWM DIA RSP XLK SMH SOXX IGV XLC XLF XLE XLV XLY XLP XLU XLI XLB XLRE (19).
  Benchmark: buy-and-hold SPY.

Window 2008-01-02 .. 2024-12-31. **2025-01-01 onward is not read.** "Recent" = 2023-01-01 .. 2024-12-31.

## Pass rules (each trial, all must hold)
1. Sharpe >= benchmark Sharpe + 0.10.
2. Max drawdown shallower than the benchmark's.
3. CAGR >= benchmark CAGR - 2.0 points a year.
4. Deflated Sharpe >= 0.80 at n_trials = 840.
5. CAGR > 0 at 10 bps stress costs, and recent-period return > 0.

## Reported, not judged
Average gross exposure, share of days with any position, trades per year, average weight by group,
calendar-year returns, 2008 and 2022 returns vs benchmark, correlation with the benchmark.

## Predictions (before running)
- E2: like PREREG48, the trend rule should cut SPY's -55% drawdown sharply but give up more than 2 points
  of CAGR (SPY ~13%/yr over 2008-2024). Likely fails rule 3. About 1 in 4 to pass.
- E1: inverse-vol weighting puts most weight in IEF, LQD, UUP and TLT, so returns may be bond-like.
  2022 (stocks and bonds both down) is where the trend exits should help vs 60/40. Sharpe edge of 0.10 and
  DSR 0.80 are the hard parts. About 1 in 3.

## Decision
PASS for a trial = all 5 rules. A passing trial still needs a separate pre-registered confirmation on
2025-01-01 onward before any paper use. FAIL = no reruns or variants.

## Result (run once, 2026-11-27; results/round59.json): **both FAIL**

| | CAGR | Sharpe | Max DD | DSR(840) | 2023-24 | Stress CAGR | Failed rules |
|---|---|---|---|---|---|---|---|
| E1 multi-asset (27 ETFs) | 3.8% | 0.876 | -7.6% | 0.653 | +17.5% | 2.2% | 3, 4 |
| 60% SPY / 40% IEF | 8.1% | 0.736 | -30.8% | | +33.6% | | |
| E2 equity-only (19 ETFs) | 3.9% | 0.616 | -10.7% | 0.257 | +23.4% | 2.4% | 1, 3, 4 |
| Buy-and-hold SPY | 10.6% | 0.604 | -51.9% | | +57.6% | | |

- The rotation is an excellent drawdown cutter: 2008 +0.1% (E1) and -6.3% (E2) vs -16.7% / -36.8%;
  2022 -0.8% / -4.3% vs -16.4% / -18.2%. Max DD falls from -31% to -8% and from -52% to -11%.
- The cost is return: average exposure is only 48% (inverse-vol weights x trend positions x taper), so
  CAGR is about 4% against 8-11%. E1 clears the Sharpe edge (+0.14) but not DSR 0.80; E2 has no Sharpe edge.
- Same lesson as PREREG48: the crypto trend rule protects on stocks but gives up too much return, because
  stock uptrends are slower and steadier than crypto's, so the SMA50 exits and re-entries cost more than
  they save outside 2008 and 2022.
- As pre-registered, the trend-on-ETFs family is closed again: no variants (more leverage to restore
  return, other universes or settings). Repo counter 838 -> 840. The 2025+ holdout stays unread.
