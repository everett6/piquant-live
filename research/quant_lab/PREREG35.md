# Pre-registration 35: three more sizing rules shaped like quantum mechanics: entanglement, coherence time and entropy (committed before any round-35 code reads real data)

Asked for by the owner on 2026-11-13 ("continue improving it with more quantum inspired ideas"). Only synthetic tests
had run when this was committed.

## Stated plainly first
- Nothing runs on a quantum computer. The label adds no edge. Each rule is a market hypothesis in borrowed notation.
- **This is not a tuning of QB.** PREREG34's QB passed; changing its exponent or window now would be a search on data
  already seen. The three rules below use different information: the other coin, the age of the trend, and the
  whole market's correlation structure.
- **The pass rule is stricter than PREREG34's.** QA passed there on differences inside the noise, because the rule
  asked only for "no worse". Here a pass needs a margin.
- **Three more trials on the same base rule.** With three tries, one clearing the bar by luck is more likely than
  with one. The trial counter (765) is charged, and a pass still only earns a paper ledger.

## QC, entanglement sizing
Two entangled particles have one joint state; measuring one tells you about the other. Here the two coins are read
as one state. For coin i, with `a_i = 1` when its close is above its SMA50 and `a_j` the same for the other coin:

    position_i = 2 × a_i × (1 + a_j) / 2      →  2x when both are on, 1x when only coin i is on, 0 when it is off

**Hypothesis:** a trend in one coin that the other does not confirm is weaker. Before the other coin has an SMA50
(ETH's first weeks), the factor is 1 and the rule is P1.

## QD, coherence-time sizing
A quantum state settles over a characteristic time as `e^(−t/τ)`. Here the trend "settles in" the same way. With `n`
the number of consecutive daily closes above SMA50, today included:

    position = 2 × (1 − e^(−n/τ)),   τ = 5 days      →  0.36x on day 1, 1.26x on day 5, 1.73x on day 10, 1.96x on day 20

**Hypothesis:** PREREG34 showed exposure close to the line earns less than its share. QB measured closeness in
price. QD measures it in time: a trend that has just started is the one most likely to whipsaw. τ = 5 is one trading
week, fixed here; it is a judgment, not a derived number.

## QE, entropy gate (von Neumann entropy of the market)
In quantum information a system is described by a density matrix ρ, and its entropy `S = −Σ λ_k ln λ_k` (λ the
eigenvalues of ρ) says how mixed it is. A pure state has `S = 0`.

Here `ρ = C / N`, with `C` the correlation matrix of the last 60 daily returns of the N coins that have them (BTC, ETH
and the 14 altcoins of PREREG25; at least 8 needed). `s = S / ln N` runs from 0 (every coin moves as one) to 1 (no
common movement). Gate: on while `s` is below the 20th percentile of its own previous 365 days (today not included;
at least 350 values). While the gate is on, a 2x sleeve holds 1x.

**Hypothesis:** when the whole market moves as one block there is no diversification left and shocks spread, so
leverage is most dangerous then. This is the "absorption ratio" idea from the systemic-risk literature.

## Limits stated before the run
- **What I already know:** everything in PREREG26–34, including that P1's drawdown is whipsaw bleed, that the clocks
  and QB results put the weak returns near the crossing, and that correlations in crypto rise in sell-offs. I have
  not looked at returns by trend age, by the other coin's state, or by cross-coin correlation.
- **QD is close to QB in spirit.** If both work they are probably measuring the same thing, and stacking them later
  would need its own test.
- **QE's signal starts late.** It needs 8 coins with 60 days and then a year of its own history; the altcoin files
  begin between 2017 and 2021. QE is judged from the first full month its gate can be on. PREREG31–32 failed two
  gates of this form on other signals.
- All three hold less than P1 on average, so each is judged against its own constant-size line.

## Data (already on disk; nothing is downloaded)
Coinbase BTC-USD and ETH-USD daily candles (`lab.load_candles`); the 14 altcoin daily closes in
`~/piquant-data/alts` (Coinbase, fetched for PREREG25). Live Pi tick data is not used.

## Rules (3 fixed trials, 762 + 3 = 765; nothing is searched)
Each rule is computed at the daily close and held over the next day (`lab.position_returns`), BTC and ETH in equal
halves. Costs: 6 + 1 bps per unit of turnover, funding 0.1 bp/h; stress 1 bp/h. Base rule: P1.
**The line** for a variant: P1's positions × `mean(variant position) / mean(P1 position)` over the variant's window
(average of the two coins). QC and QD: 2016–2026. QE: from the first day of the first month in which the gate is
defined for the whole month, to the end; P1 and the line are rebuilt on that window.

## Pass rule (each variant alone, against its line and P1 on its window)
1. **Return:** geometric monthly return ≥ the line's + 0.10 points.
2. **Drawdown:** max drawdown at least 1.0 point better than the line's.
3. **Less noise:** Sharpe ratio ≥ P1's + 0.02.
4. **Bad markets:** mean bear-month return no worse than the line's.
5. **Recent data:** period A (2025-07 on) geometric return > 0.
6. **Stress:** geometric return at stress costs ≥ the line's at stress costs.

**Also reported:** average leverage; turnover; flat and bull months; every period; 2022; for QC the share of trend-on
days without the other coin; for QD the mean next-day return by trend age (1–5, 6–10, 11–20, over 20 days); for QE
the share of days gated and by year; QB's numbers beside them for scale.

**If one passes:** it can join the paper ledger, paper only, asked first. **If all fail:** these three are closed.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- QC: BTC and ETH trend together most of the time, so it differs from P1 on perhaps 15% of trend-on days. Small
  effect; fails the margins. Chance of a pass: about 1 in 6.
- QD: improves drawdown and bear months by about as much as QB, but pays more turnover and gives up the first days
  of strong breakouts. Return check is the risk. Chance of a pass: about 1 in 4.
- QE: gated about 20% of days by construction. As with funding and stablecoins, I expect the signal to arrive with
  the fall and not before it. Chance of a pass: about 1 in 8.

## Result (run once, 2026-11-13): QD PASSES all six checks. QC FAILS one (drawdown margin). QE FAILS five.

P1 reproduced exactly on 2016–2026 (+8.50 / −83.9% / 1.489). QE's window starts 2020-08-01 (first signal
2019-08-26, 16 coins by the end).

| | size vs P1 | %/mo | max DD | Sharpe | bear | flat | bull | 2022 | D | C | B | discovery | A | stress |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **QD coherence time** | 0.872 | +8.45 | −76.5% | 1.525 | −5.3 | −3.3 | +33.9 | −6.65 | +28.73 | +5.92 | +2.80 | +4.96 | +2.05 | +2.05 |
| QD's line | 0.872 | +7.84 | −79.0% | 1.489 | −7.1 | −3.3 | +32.5 | −8.18 | +24.78 | +5.94 | +1.25 | +5.70 | +3.07 | +1.48 |
| **QC entanglement** | 0.922 | +9.10 | −80.9% | 1.587 | −5.5 | −3.9 | +36.6 | −9.20 | +27.13 | +8.56 | +1.53 | +6.04 | +3.20 | +2.32 |
| QC's line | 0.922 | +8.11 | −81.1% | 1.489 | −7.4 | −3.5 | +34.7 | −8.72 | +26.05 | +6.08 | +1.09 | +5.92 | +3.18 | +1.38 |
| P1 | 1 | +8.50 | −83.9% | 1.489 | −8.0 | −3.9 | +38.2 | −9.57 | +27.99 | +6.24 | +0.77 | +6.25 | +3.32 | +1.19 |
| QB tunnelling (PREREG34, for scale) | 0.929 | +8.55 | −79.8% | 1.516 | −6.2 | −4.0 | +35.8 | −8.21 | +28.05 | +6.15 | +2.54 | +5.59 | +2.36 | +1.74 |
| **QE entropy gate** (2020-08 on) | 0.913 | +4.59 | −84.0% | 1.064 | −11.6 | −2.8 | +26.8 | −8.74 | – | – | −0.43 | +6.24 | +3.74 | −1.89 |
| QE's line | 0.913 | +4.71 | −80.7% | 1.091 | −11.2 | −3.7 | +26.8 | −8.63 | – | – | +1.11 | +5.89 | +3.16 | −1.77 |
| P1 on QE's window | 1 | +4.88 | −83.9% | 1.091 | −12.3 | −4.1 | +29.4 | −9.57 | – | – | +0.77 | +6.25 | +3.32 | −2.21 |

**QD, pass.** Checks: return +0.61 over its line (0.10 needed); drawdown 2.5 points better (1.0 needed); Sharpe
+0.036 over P1 (0.02 needed); bear −5.3 against −7.1; A positive; stress +2.05 against +1.48.
- Mean next-day return by trend age: BTC +0.24% (days 1–5), +0.41% (6–10), +0.28% (11–20), +0.37% (21+); ETH
  +0.04%, +0.50%, +0.62%, +0.55%. The first five days are the weak ones, clearly for ETH and mildly for BTC.
- Turnover fell to 0.061/day from P1's 0.099: the ramp enters in small steps and a whipsaw exits a small position.
- **Against it:** worse than P1 in the two latest periods (discovery +4.96 against +6.25, A +2.05 against +3.32),
  the same pattern as QB. The whole gain is 2016–17 and 2021–22.

**QC, fail by one check.** It beat its line on return by +0.99, Sharpe by +0.098, bear months by 1.9 and stress, and
even beat P1's return outright (+9.10 against +8.50) at 8% less size. Its drawdown was −80.9% against the line's
−81.1%: 0.2 points better where 1.0 was required. A coin was on without the other on 13–18% of trend-on days.
- The registered rule stands: QC is not adopted. Its return is the best of any variant tested here, and most of the
  gap to P1 is period C (2018–20, +8.56 against +6.24). That is worth knowing and is not a pass.

**QE, fail.** Gated 28% of days (0% of 2020, 39% of 2022, 2% of 2023, 42–50% of 2024–25). It lost to its line on
return, drawdown, bear months and stress. As with funding and stablecoin supply, the signal was on in the wrong
places. Market-wide gates on P1's leverage are closed.

**Reading:**
- Three rules now point the same way (QB by distance, QD by age, the clocks by agreement): P1 is paid least just
  after it enters. QB and QD pass on that; they are probably two measurements of one effect.
- QC is a different effect (confirmation by the other coin) and was the strongest on return, but did nothing for
  the drawdown.
- **Prediction check:** QD 1 in 4 (passed), QC 1 in 6 (failed, though far closer than I expected and on the check I
  did not flag), QE 1 in 8 (failed).
- **Caution on the count:** five sizing variants of one base rule have now been tried in two rounds, and three
  passed or nearly passed. They share one data set and one base rule, so they are not independent confirmations.
  None changes the size of the problem: drawdowns are still −76% to −81% and bear months −5% to −6%.
- **Per the pass rule,** QD can join the paper ledger, paper only, asked first. Trials charged: 765.
- **Added to the paper ledger on 2026-11-14 UTC** at the owner's request: `QD_btc_eth_coherence_2x`, paper only, daily
  (`internal/trend/trend.go`, `coherenceShare`). It starts flat at equity 1. QC and QE were not added.
