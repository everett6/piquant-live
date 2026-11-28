# PREREG60: sequential (CUSUM) decay detector for shadow-paper alphas (pre-registered, synthetic only)

Written and pushed 2026-11-28 before `decay_cusum.py` or `run_round60.py` exist or run.

## Why
`shadow_paper_eval.classify_health` labels candidates from the Sharpe of all live days since admission:
DECAYING below 0.3 and KILLED below -0.5 once 30 days exist, plus KILLED at a -15% drawdown. With daily
returns the Sharpe of 30 days has a standard error of about sqrt(365/30) = 3.5, so those labels are mostly
noise: a candidate still earning its expected return is often labelled DECAYING or KILLED, and a dead one
can look HEALTHY. This replaces the two Sharpe rules with a one-sided CUSUM test against each candidate's
own pre-admission returns, which has a known false-alarm rate.

This is a monitoring-method test, not a strategy trial. It reads no market data (synthetic paths only),
calls no `register()`, and does not change the repo trial counter (840) or the discovery counter.

## Detector (fixed before any run)
Inputs: the candidate's daily net returns from `_compute_candidate_net_returns` (unchanged), split at the
admission day: reference = days <= entry day, live = days > entry day.
- Reference window: the last 1095 reference days (fewer if not available). If fewer than 180 days, or
  the scale is ~0, there is no CUSUM and the old rules apply unchanged (`cusum: null`).
- Scale: sigma = std (ddof 1) of the reference after winsorizing it at its own 0.5% / 99.5% quantiles.
- Expected live mean mu0 = 0.5 x reference mean (the usual ~50% backtest-to-live drop). Decayed mean
  mu1 = 0 (no edge). k = (mu0 - mu1) / (2 sigma). If mu0 <= 0 (no edge even in sample): k = 0.01.
- Daily step: z_t = clip((mu0 - r_t) / sigma, -4, 4); S_t = max(0, S_{t-1} + z_t - k); S_0 = 0.
- Threshold h: solved from Siegmund's approximation so the average run length with no decay is
  ARL0 = 1825 days (one false alarm per 5 years per candidate performing at mu0):
  ARL0 = (exp(2k(h + 1.166)) - 1 - 2k(h + 1.166)) / (2k^2), solved by bisection on h in [0, 1e4].
- Alarm when S_t >= h. Once alarmed, it stays alarmed (state is recomputed from the full live series).

## New health rule (applied only if the calibration below passes)
- KILLED: live max drawdown < -15% (unchanged), OR the CUSUM has alarmed. Applies from day 1.
- WARMUP: otherwise, if live days < 14.
- DECAYING: otherwise, if the current S >= 0.5 h.
- HEALTHY: otherwise.
- No CUSUM available: the old rules, unchanged.
If the calibration fails, the CUSUM fields are written to `shadow_paper_state.json` for information only
and `classify_health` stays as it is.

The Go package `internal/quant/alpha` gets the same detector as a function (`CusumDecay`) with the same
constants, tested to match the Python on fixed inputs. The live allocator is NOT rewired by this test;
wiring it into sizing is a separate, later change.

## Calibration (run once, `run_round60.py`, seed 60, 2000 paths per scenario)
Each path: reference of 1095 days and live of 730 days of iid Student-t (3 degrees of freedom) returns,
scaled to daily sigma 0.94% (about 18% a year). Reference mean gives an annual Sharpe of 1.5.
Live scenarios, by true live annual Sharpe:
- H (healthy at the haircut): 0.75.
- Z (edge gone): 0.0.
- N (turned negative): -1.0.
- C (crash): -3.0.
Both detectors include the unchanged -15% drawdown kill. "Old" = the current `classify_health` evaluated on
every live day (KILLED at any day counts); "new" = the rule above. Recorded per scenario: fraction killed
within 365 and within 730 days, and median days to kill.

## Pass rules (all must hold for adoption)
1. H: new kill rate within 365 days <= 20%.
2. H: new kill rate within 365 days < old kill rate within 365 days.
3. N: new kill rate within 730 days >= 70%.
4. C: new median days to kill <= 120.

Z is reported but has no pass rule: going from Sharpe 0.75 to 0 at daily resolution is expected to take
years to detect with any method, and the result is recorded as the honest detection floor.

Results go to `results/round60.json` (written by the script) and a "Result" section appended here.
No variants of this detector (other haircuts, ARL0, windows) under any number if it fails.
