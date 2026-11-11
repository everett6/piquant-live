# Pre-registration 14: never get near the 40% kill switch (committed before any round-14 code runs)

## Why
The owner set the kill switch at a 40% drop from peak (2026-11-11) and then asked for a system that never needs
it. Every trend variant so far has had drawdowns of −57% to −84% (PREREG11–13).

**Where P1's −84% came from.** I looked at P1's already-published equity curve before writing this, so this
design is informed by the full sample:
- The worst stretch ran from 2021-11-08 to 2023-01-05.
- Days where the portfolio lost more than 10% account for all of its log loss (108%; the other days netted
  slightly positive).
- The five worst days alone (−15% to −25% each, 2x on a coin down 8–13% in one day) account for 63%.
- Close-based exits (R3 fast exit, R4 trailing stop) cannot react inside such a day.

## Methods (from the literature)
- **Intraday stop.** Han, Zhou & Zhu (2016), *Taming Momentum Crashes: A Simple Stop-Loss Strategy*: stops cut
  momentum crash losses. Kaminski & Lo (2014), *When do stop-loss rules stop losses?*: stops add value when
  returns are serially correlated, as crypto crash days are intraday.
- **Drawdown-constrained sizing.** Grossman & Zhou (1993), *Optimal investment strategies for controlling
  drawdowns*: the optimal risky exposure under a max-drawdown constraint is proportional to the cushion
  (W − αM)/W, where W is equity, M its running peak, and α the floor ratio. In continuous time the drawdown never
  passes 1 − α. Only overnight/intraday gaps can push past it, and the stop limits those.
- **Trend ensemble.** Levine & Pedersen (2016), *Which Trend Is Your Friend?*; Hurst, Ooi & Pedersen (2017), *A
  Century of Evidence on Trend-Following*: averaging several lookbacks gives smoother, less whipsaw-prone signals
  than one moving-average switch.

**Relation to failed tests.**
- PREREG12 Q1 was a step brake (2x → 1x at a 40% drawdown). It failed the 7%/month bar, not on drawdown.
- This round has a different objective (drawdown first, bar −35%) and a different mechanism (continuous cushion
  sizing plus an intraday stop).
- The intraday stop is new; R4's stop was close-based and trailed the high.

## Variants (BTC and ETH halves, long-or-flat, us-futures costs; 3 fixed variants, no parameter search)

### Common rules
- **Signal at day t's close:** decided from closes through t. It sets the leverage held over day t+1 (UTC days,
  as in the lab).
- **Intraday stop while held on day t+1:**
  - level = C_t × (1 − s_t), with s_t = clip(2 × σ_t, 3%, 12%);
  - σ_t is the standard deviation of the last 30 daily close-to-close returns through t;
  - it triggers at the first 1-minute bar of day t+1 whose low ≤ level;
  - fill = min(that bar's open, level), so a bar that gaps through fills at its open; taker 6 bps + slip 1 bp;
  - the sleeve is then flat for the rest of the day;
  - at day t+1's close the signal decides again; re-entering costs the normal taker + slip.
- **Costs:** turnover × (taker + slip) at every change; funding 0.1 bp/h on held leverage for the whole day
  (stress: 1 bp/h), even on a stopped day (conservative).
- **Portfolio:** equal halves, rebalanced daily. ETH's half is cash before ETH has data.

### S1: P1 + intraday stop
2x while close > SMA50 (the P1 rule), with the stop.

### S2: S1 + Grossman–Zhou sizing
- Each sleeve's leverage = signal × min(2, m × (1 − α·M_t/W_t)), floored at 0.
- α = 0.70 (floor at 70% of peak) and m = 2/0.30 ≈ 6.67, so leverage is 2x at a new peak.
- W_t and M_t are portfolio equity and its running peak since the start of the run, through close t.

### S3: S2 with a trend ensemble
- signal = the fraction of the SMAs (20, 50, 100, 200 days) that the close is above, using those SMAs with
  enough history. No position until SMA50 exists (same start as P1).
- leverage = signal × the S2 cap.

## Pass rule (3 trials: 717 + 3 = 720)
A variant passes only if all of these hold:
1. Full 2016–2026 max drawdown no worse than −35% at us-futures costs, and no worse than −35% at stress costs.
2. Full 2016–2026 geometric monthly return ≥ +2.5%.
3. Geometric monthly return > 0 in at least 4 of the 5 periods (D 2016–17, C 2018–20, B 2021..2022-12-16,
   discovery 2022-12-17..2025-06, A 2025-07..).
4. Stress full-history geometric monthly return > 0.

**Choosing among passers:** the highest full-history geometric monthly return.

**Also reported:** P1 as comparator; each variant's worst day, number of stops, share of days held, days spent
within 5 points of the floor (S2/S3), losing months, worst month, months ≥ 7%, and the distance from 7%/month.

**If one passes:**
- It is added to the trend ledger as a paper variant, with the intraday stop checked on live prices.
- It becomes the recommended core. P1 and R3 stay tracked for comparison.

**If none pass:** results are recorded and no variant of these rules is retried.

## Prediction (written now)
- S1: drawdown about −55% to −65% (stops help, whipsaw remains), return close to P1's.
- S2: drawdown about −32% to −38%, return 2–4%/month.
- S3: drawdown about −30%, return 2–3%/month.
- I expect S2 or S3 to pass. None will reach 7%/month: the earlier rounds show 7% has needed −73% or worse.
- **Caveat:** the design was informed by the full sample, so a pass here is not out-of-sample evidence. Forward
  paper trading is the real test.
