# PREREG61: drawdown kill scaled to each candidate's own volatility (pre-registered, synthetic only)

Written and pushed 2026-11-28 before any code for it exists or runs.

## Why
PREREG60 (FAIL) showed, in a diagnostic after its verdict, that the fixed -15% drawdown kill in
`shadow_paper_eval.classify_health` alone kills 39.8% of healthy candidates (live Sharpe 0.75, 18% annual vol)
within a year. A fixed percentage ignores volatility: it is far too tight for a volatile candidate and too
loose for a calm one. This replaces it with a drawdown limit that a healthy version of the same candidate
would breach only rarely.

Monitoring-method test, not a strategy trial: synthetic paths only, no market data, no `register()`, no
change to the repo trial counter (840) or the discovery counter. The PREREG60 CUSUM is used exactly as it
was registered (no change to any of its constants).

## Drawdown limit (fixed before any run)
From the same reference as PREREG60 (`decay_cusum.reference_params`: last <= 1095 pre-admission days,
>= 180 required; mu0 = 0.5 x reference mean):
- Healthy paths: 2000 paths of 730 days, each day an iid draw (with replacement) from the reference returns
  shifted so their mean is mu0 (r - mean(ref) + mu0). numpy `default_rng(61)` per call, so it is
  deterministic.
- For each path, the max drawdown over the 730 days (equity = cumprod(1 + r), peak starts at 1).
- Limit = the 5th percentile of those max drawdowns (a healthy candidate breaches it within 2 years about
  5% of the time), clamped to [-35%, -10%].
- No reference: no limit, old rules unchanged.

## Health rule (adopted only if the calibration passes)
- KILLED: live max drawdown below the limit, OR the PREREG60 CUSUM has alarmed. From day 1.
- WARMUP: otherwise, if live days < 14.
- DECAYING: otherwise, if the current CUSUM S >= 0.5 h.
- HEALTHY: otherwise.
- No reference: the old `classify_health`, unchanged.
On a pass, `shadow_paper_eval` uses this rule; on a fail, the limit is written for information only and
`classify_health` stays as it is. Python only: the Go `CusumDecay` is unchanged and the Go side gets no
drawdown limit in this test (a bootstrap cannot be matched across languages exactly).

## Calibration (run once, `run_round61.py`, seed 61, 2000 paths per scenario)
Same synthetic setup as PREREG60: reference 1095 days, live 730 days, iid Student-t (3 dof), daily sigma
0.94%, reference annual Sharpe 1.5; live scenarios H 0.75, Z 0.0, N -1.0, C -3.0. "Old" = current
`classify_health` evaluated every live day; "new" = the rule above. Recorded per scenario: killed within
365 / 730 days and median days to kill, for the new rule, for its drawdown limit alone, and for the old rule.

## Pass rules (all must hold)
1. H: new kill rate within 365 days <= 20% (PREREG60's bar, unchanged).
2. H: the drawdown limit alone kills <= 7% within 730 days (5% by design, plus estimation error).
3. N: new kill rate within 730 days >= 70%.
4. C: new median days to kill <= 120.

Results to `results/round61.json` (written by the script) and a "Result" section appended here.
No variants (other percentiles, horizons, clamps) under any number if it fails.
