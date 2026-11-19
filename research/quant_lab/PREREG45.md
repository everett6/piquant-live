# Pre-registration 45: what a 21% drawdown shutoff does to the three best blends (committed before any round-45 code reads real data)

Asked for by the owner on 2026-11-18 ("Yes, test the shutoff"), after stating a target of 60% a year with an average
largest drawdown of 20% and a shutoff at 21%. Only synthetic tests had run when this was committed.

## The question
Can a hard stop at a 21% drawdown turn a higher-return blend into one that fits the owner's limit, or does it only
cut the return? And on the blend that already sits near −20%, how often would a 21% stop fire?

## What I told the owner before this test (and still expect)
- Every strategy here earns about 1.2–1.4 times its worst drawdown per year. At a 20% drawdown that is about 25–30% a
  year; 60% a year has come only with drawdowns of 40% or more.
- A hard stop set just beyond the usual worst drawdown fires often and leaves the book flat for the recovery, where
  trend strategies make their money.
- The dual-speed blend's backtest drawdown was −19.8%, so a 21% stop would not have fired on its own history. The
  bootstrap below asks how often it would fire on histories like it.

## Strategies (rebuilt exactly as PREREG44 built them, base and stress costs, 2020-01-01 to 2026-10-31)
- **A** `Q42_dualspeed_60_40_carry`: 30% QD + 30% fast-exit trend + 40% funding carry, with its 6%→20% taper.
  PREREG42/44: +2.12%/month, max drawdown −19.8%.
- **B** `Q44_vol_balanced_1x_2p5x_blend`: 70% QD at 1x–2.5x by volatility + 30% carry. PREREG44: +2.76%, −32.2%.
- **C** `Q44_bh_vol_harvester_1x_2p5x`: 55% leveraged QD + 20% buy-and-hold harvester + 25% carry. PREREG44: +2.89%,
  −43.6%.
The runner checks that it reproduces these three base results before applying any rule.

## Shutoff rules (applied to each strategy's daily portfolio return series)
At each daily close, the overlay's own equity is compared with its own peak.
- **H21, hard stop:** when the drawdown reaches 21%, the whole book is sold at that close and stays in T-bills for good.
  This is the owner's rule taken literally.
- **R21, stop and restart:** as H21, then the strategy is restarted after 30 calendar days; the peak is reset to the
  equity at the restart.
- **R30, emergency stop:** as R21 at a 30% drawdown (the level I suggested instead).

Costs: 15 bps of equity on every stop and every restart (selling and rebuying the trend and carry legs). While stopped
the book earns the 3-month T-bill rate (FRED DTB3, on disk). On a restart the strategy rejoins its own return stream as
if it had kept running; its internal taper reflects its own drawdown, not the overlay's. That is a stated
simplification. Stops act on daily closes; a live stop would act intraday at about the same level or better.

**9 trials** (3 strategies × 3 rules), 787 + 9 = 796. Nothing is searched: 21%, 30%, 30 days and 15 bps are fixed here.

## Pass rules (each of the 9 alone, against its own base strategy on the same window)
**Owner's target** (adopt only if all five hold):
1. Geometric return ≥ 3.99%/month (60% a year).
2. Max drawdown no worse than −25% (21% plus room for one day's move past the stop).
3. Return at least the base strategy's.
4. Period A (2025-07 on) geometric return > 0.
5. Return at stress costs > 0.

**Value of the stop** (reported as its own verdict): return ≥ the base's − 0.05%/month **and** max drawdown at least
2 points better than the base's. If it holds, the stop is worth adding to a paper ledger even though the 60% target is
not met.

**Also reported:** number of stops and restarts and their dates; days in cash; the base strategies beside each rule;
Sharpe, Calmar, worst month, bear months.

**Bootstrap (descriptive, not a trial):** for each base strategy, 2,000 one-year paths built from 20-day blocks of its
own daily returns (seed 45). Reported: the mean and median of each year's largest drawdown, and the share of years in
which it reaches 21% and 30%.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- Owner's target: all 9 fail check 1. None reaches 60% a year.
- A: no stop fires in the backtest, so A's three variants equal A less nothing. The bootstrap puts its mean one-year
  largest drawdown near 12–15% and a 21% stop firing in about 10–20% of years.
- B and C with H21: the stop fires once, early (2021 or 2022), and they finish far below their base return.
- B and C with R21: drawdown held near −22% to −26%, return cut by a third or more. Value-of-the-stop verdict: fail.
- R30 on C: the best chance of a "value of the stop" pass, about 1 in 3.

## Result (run once, 2026-11-18): the owner's target fails on all 9. The stop adds value only on blend B.

All three base blends reproduced PREREG44 exactly (+2.12, +2.76, +2.89%/month).

| | %/mo | %/yr | max DD | Sharpe | Calmar | A | bear | stress | stops | days in cash | owner target | value of stop |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **A dual-speed (base)** | +2.12 | +29 | −19.8% | 1.36 | 1.29 | +0.56 | −1.2 | +0.71 | – | – | – | – |
| A + H21 / R21 / R30 | +2.12 | +29 | −19.8% | 1.36 | 1.29 | +0.56 | −1.2 | +0.92 / +0.71 / +0.71 | 0 | 0 | fail | fail |
| **B vol-balanced (base)** | +2.76 | +39 | −32.2% | 1.20 | 1.03 | +0.25 | −3.1 | +0.69 | – | – | – | – |
| B + H21 | +2.24 | +30 | −21.4% | 1.22 | 1.25 | +0.32 | −1.2 | +0.34 | 1 (2022-04-06) | 1,669 | fail | fail |
| B + R21 | +2.83 | +40 | −29.2% | 1.22 | 1.16 | +0.24 | −2.9 | +0.33 | 3 | 90 | fail | **pass** |
| B + R30 | +2.80 | +39 | −30.1% | 1.21 | 1.11 | +0.25 | −2.9 | +0.76 | 1 | 30 | fail | **pass** |
| **C harvester (base)** | +2.89 | +41 | −43.6% | 1.15 | 0.80 | −0.03 | −5.5 | −0.04 | – | – | – | – |
| C + H21 | +0.35 | +4 | −28.4% | 0.47 | 0.15 | +0.32 | 0.0 | +0.37 | 1 (2020-03-12) | 2,424 | fail | fail |
| C + R21 | +2.77 | +39 | −39.1% | 1.12 | 0.85 | −0.04 | −5.5 | −0.01 | 7 | 210 | fail | fail |
| C + R30 | +2.85 | +40 | −43.3% | 1.14 | 0.79 | −0.25 | −5.6 | −0.11 | 2 | 60 | fail | fail |

**Bootstrap (2,000 one-year paths of 20-day blocks):**

| | mean largest drawdown in a year | median | worst 10% of years | years reaching 21% | years reaching 30% |
|---|---|---|---|---|---|
| A dual-speed | 12.7% | 11.9% | 18.9% or worse | 5% | 0.3% |
| B vol-balanced | 21.7% | 20.4% | 32.1% or worse | 46% | 14% |
| C harvester | 25.0% | 23.7% | 36.9% or worse | 63% | 25% |

**Reading:**
- **No blend reached 60% a year, with or without a stop** (best: +40%). The prediction held on check 1 for all nine.
- **A literal hard stop at 21% is destructive on anything that can reach 21%.** C stopped on 2020-03-12 (the COVID
  crash) and sat in cash for 6.6 years: +4% a year instead of +41%. B stopped in April 2022 and missed 2023–26.
- **Stop-and-restart does not cap the drawdown.** B with R21 still fell −29.2% overall: it stopped at −21%, restarted
  30 days later, and lost more from the new start. A 30-day pause cannot know when the fall is over.
- **B with a restart rule is the one place the stop helped:** about the same return (+2.80 to +2.83 against +2.76)
  with 2–3 points less drawdown. R21 halved B's stress return (+0.33 against +0.69); R30 did not (+0.76). R30 on B is
  the version worth knowing about, and the margin is small (2.1 points).
- **Blend A already lives where the owner wants to be.** Its drawdown averaged 12.7% a year in the bootstrap and
  reached 21% in 1 year in 20. The 21% stop never fired on its history. It earns about 29% a year, not 60%.
- **The owner's own spec contradicts itself on B and C:** with an average largest yearly drawdown of 20–25%, a 21%
  stop fires in roughly half to two thirds of years.
- **Prediction check:** A matched (no stop fired; bootstrap 5% of years against my 10–20%). I expected B/C with R21 to
  hold near −22% to −26%; they held −29% and −39%. I gave R30 on C the best chance of a value pass; it failed, and B
  passed with both restart rules instead.
- **Verdict:** no variant meets the owner's target, so none is adopted for that goal. Per the value rule, R30 on B
  may be added to a paper ledger beside A, paper only, asked first. Trials charged: 796.
