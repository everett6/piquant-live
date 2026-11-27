# PREREG58: first ETF alpha-discovery round on QQQ (pre-registered, no ETF data read)

Written and pushed 2026-11-27 before any real ETF price is downloaded or read by `etf_discover.py`.

## Question
Do any of the 12 fixed ETF overlay formulas in `etf_discover.ETF_TEMPLATES` beat buy-and-hold QQQ
on a risk-adjusted basis, after costs and after allowing for the number of ideas tried?

## Exact run (once)
```
python3 auto_discover.py --asset-class etf --etf-target QQQ --role overlay \
    --mode symbolic --seed 42 --n-candidates 12 --etf-refresh
```
Code: commit 99773a9 (`etf_discover.py`, `auto_discover.py`). Default state, shadow and log paths.
`--mode symbolic --seed 42` fixes the candidates to these 12 (no LLM, nothing chosen after seeing data):

1. `tanh(tgt_sma200_dist * 8.0) - 2.0 * max(tgt_vol20_ann - 0.30, 0.0)`
2. `tanh(tgt_sma200_dist * 6.0) - 2.0 * max(tgt_tlt_corr63, 0.0) * max(-tlt_ret21, 0.0) * 10.0`
3. `where(spy_vol20_ann < 0.25, 1.0 + tanh(tgt_ret21 * 5.0), -0.5 - 5.0 * max(spy_vol20_ann - 0.25, 0.0))`
4. `1.0 - 8.0 * max(uup_ret21 - 0.02, 0.0) - 6.0 * max(-hyg_minus_ief_21d - 0.01, 0.0)`
5. `where(tgt_rsi14 < -0.4, 1.5, tanh(tgt_sma200_dist * 8.0)) - max(tgt_vol20_ann - 0.35, 0.0) * 3.0`
6. `tanh(ema(tgt_ret63, 10) * 4.0) - 3.0 * max(tgt_vol_ratio_5_20 - 1.4, 0.0)`
7. `tanh(ema(smh_minus_qqq_21d, 10) * 10.0) + tanh(tgt_sma50_dist * 10.0)`
8. `ema(hyg_minus_ief_21d, 10) * 20.0 + 2.0 * (sector_breadth_50 - 0.5)`
9. `2.0 * (sector_breadth_50 - 0.4) - 10.0 * max(defensive_minus_cyclical_21d, 0.0)`
10. `roll_mean(sector_breadth_50, 10) * 3.0 - 1.5 - 4.0 * max(sector_dispersion_21 - 0.05, 0.0)`
11. `where(tgt_sma200_dist > 0.0, 1.5 - 4.0 * max(-tgt_dd_252 - 0.10, 0.0), -1.0)`
12. `ema(iwm_minus_spy_21d + rsp_minus_spy_21d, 10) * 10.0 + tanh(tgt_sma200_dist * 5.0)`

## Data
- 28 ETFs (`ETF_UNIVERSE`), dividend-adjusted daily closes from Yahoo's free chart API, cached in
  `~/piquant-data/etf/`. Funds that did not exist yet (e.g. XLC 2018, XLRE 2015) give empty features,
  which the evaluator treats as 0.
- Evaluation window 2008-01-01 to 2024-12-31. **Data from 2025-01-01 on is not read** (sealed holdout).
- Cash at the 3-month T-bill rate (DTB3, available from 2014; 0% before that, which understates
  cash-heavy candidates).

## Model
Overlay: QQQ exposure = 0.40 + 0.85 x sigmoid(formula), so 0.40 to 1.25, set at the close and held the
next day. Cost 2 bps per unit traded (stress 10 bps). Exposure above 1.0 pays T-bill + 1%.

## Gates (per candidate, all must hold; code `etf_purged_cv` and `check_etf_gates`)
1. AST allowlist and future-shock causality test.
2. Purged 5-fold CV: median fold Sharpe >= 0.50 and >= 3 of 5 folds positive.
3. Deflated Sharpe >= 0.80 at the discovery state's trial count (818 before this run, +1 per candidate
   that reaches CV).
4. Sharpe and Calmar both above buy-and-hold QQQ over the same window; max drawdown no deeper than
   buy-and-hold.
5. Monthly geometric return > 0 at 10 bps stress costs and over the last 2 years of the window.
6. Mean daily turnover <= 0.10.

## Decision
- **PASS** if at least one candidate passes every gate. Passing candidates go to the paper-only shadow
  queue and need a separate pre-registered confirmation on 2025-01-01 onward before any paper use.
- **FAIL** otherwise. No re-run with other seeds, weights, targets or windows under this number.

## Counters
The discovery state counter (818) charges each candidate that reaches CV. The repo-wide trial counter
is 814 (shared with piquant-stocks); this round adds 12 -> 826.

## Run log
- **Run 1 (2026-11-27), INVALID, data bug.** Yahoo's chart API returned monthly bars for `range=max`
  despite `interval=1d`, so the evaluator treated 534 monthly bars as days (buy-and-hold QQQ showed
  Sharpe 1.92, ~97%/yr, which is impossible). All 12 were rejected anyway (output kept in
  `results/round58_invalid_monthly.log`, local only: logs are gitignored). No result is taken from it.
- **Fix:** the loader now asks for an explicit date range (`period1/period2`) and refuses any series
  whose median bar gap exceeds 4 days (new test `test_loader_refuses_monthly_bars`). The re-downloaded
  data has 4,279 QQQ days in 2008-2024 (43.26 -> 506.92). No formula was evaluated on it.
- **Run 2:** the identical registered command, once, on the fixed loader. This replaces run 1; nothing
  else changes (same 12 formulas, gates, window, costs). Both runs are charged to the counters:
  discovery state 818 -> 830 (run 1) -> 842 (run 2); repo counter 814 -> 838.

## Result (run 2, 2026-11-27): **FAIL**
Buy-and-hold QQQ 2008-01-02..2024-12-31: Sharpe 0.760, Calmar 0.316, max DD -49.4%.
All 12 passed the AST, causality and purged-CV gates. 11 of 12 beat buy-and-hold on Sharpe, Calmar
and max drawdown (#4, the dollar/credit filter, did not). **None reached DSR 0.80** at 831-842 trials,
so none passed. Best: #5 (RSI dip-buy + 200-day trend + vol brake), Sharpe 0.877, Calmar 0.459,
max DD -34.0%, DSR 0.659. Next: #3 (SPY vol regime) Sharpe 0.858, DSR 0.630.
Reading: these overlays cut QQQ's worst drawdown by about a third for a small Sharpe gain (+0.05 to
+0.12), too small to stand out from the number of ideas already tried. Nothing enters the shadow queue.
The 2025+ holdout stays unread. Counters: discovery state 842; repo 838.
