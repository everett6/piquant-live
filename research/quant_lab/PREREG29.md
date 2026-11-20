# Pre-registration 29: cross-sectional momentum across 16 coins, long the strongest third and short the weakest third (committed before any round-29 code reads real data)

## Why
The owner asked again for money made while crypto falls, and said to keep his situation in view: a US retail
account, futures on Coinbase Derivatives, paper money, daily decisions.

Everything tested so far bets on the market's direction (trend, shorts, BTC against alts as a block) or on options
(PREREG26–28, closed). One family has never been tested here: **ranking coins against each other.** A book that is
long the recent winners and short the recent losers has no net position in the market, so in principle it can earn
in a falling month from the gap between coins.

It is the best-documented return in the crypto literature not yet tried in this repo: Liu, Tsyvinski and Wu, "Common
Risk Factors in Cryptocurrency" (Journal of Finance, 2022) find a momentum factor that is strongest at a
three-week formation and one-week holding period among larger coins.

**How it differs from closed tests:**
- PREREG25 was long BTC and short all alts, always the same sides. Here the sides are chosen each week by rank, and
  BTC and ETH are ranked like any other coin.
- PREREG7 shorted a coin when it was below its own average. Here a coin is shorted only relative to the others, and
  every short is paired with an equal long.
- PREREG20 was a two-coin beta-neutral book. This is a 16-coin ranking.

## Limits stated now
- **The short leg can be squeezed.** A coin in the short third can jump (LTC rose 83.6% in a day in 2017). The loss
  is limited by the weight of one coin, 1 ÷ (coins in the leg), not by contract. With 2 coins per leg that is 50%.
- **Survivorship:** only coins listed on Coinbase today are in the data. Dead coins would mostly have been shorts,
  so their absence should make the result look worse, not better.
- **I have seen these coins' prices in PREREG25** (yearly pair results and each coin's mean contribution to a
  short-alt basket). I have not looked at relative rankings or any momentum sort.
- **Feasibility:** it needs futures on each coin. Coinbase Derivatives lists most but not all 16 for US accounts;
  this was assumed in PREREG25 and is assumed again. Paper only.
- **No pass is "it works".** A pass starts a paper ledger. After 750 trials, only forward results count.

## Data (already on disk; nothing is downloaded)
- Coinbase daily closes for LTC, BCH, ETC, XLM, LINK, XRP, ADA, DOGE, SOL, DOT, AVAX, UNI, SHIB, HBAR
  (`~/piquant-data/alts`, fetched for PREREG25) plus BTC and ETH (`lab.load_candles` → daily).
- P1 exactly as PREREG13/23–28.

## Rules (1 fixed trial, 750 + 1 = 751; nothing is searched)
- **Eligible at close t:** a coin with a close at t and at least 60 earlier closes (PREREG25's rule), and a close 21
  days earlier.
- **Score:** `close_t / close_{t−21} − 1` (three weeks, from the paper).
- **Rebalance:** at the close of every Monday (UTC), if at least 6 coins are eligible and their scores are not all
  equal. `k = floor(eligible / 3)`.
  - long the `k` highest scores, `+1/k` each;
  - short the `k` lowest, `−1/k` each;
  - gross 2x, net 0. Ties are broken by the fixed coin order above (BTC, ETH first).
- **Between rebalances** the weights stay as set (one week, from the paper). A coin with no price on a day earns 0
  that day. With fewer than 6 eligible coins on a Monday the book is flat for that week.
- Positions decided at close t earn day t+1's return (`lab.position_returns` timing).
- **Costs per coin leg:** us-futures, 6 + 1 bps per unit of turnover and funding 0.1 bp/h on |weight|; stress 1 bp/h.
  Drift in weights inside the week is ignored (as in PREREG25).
- **Overlay:** the book's return is added to P1's. It is margin-funded futures.

21 days, 7 days, thirds, 6 coins, 60 closes and Monday are fixed here.

## Pass rule (the five overlay checks, against P1 rebuilt in the same run)
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points (P1: −8.0, so ≥ −5.0).
2. **Target kept:** full 2016–2026 geometric return ≥ 7.0%/month.
3. **Drawdown:** full max drawdown no worse than P1's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** full geometric return at stress costs > 0.

**Also reported:**
- the book alone: geometric return, Sharpe, max drawdown, worst day and worst month; mean in bear, flat and bull
  months (the owner's aim is a positive bear month; reported, not required); correlation with P1; long leg and short
  leg separately; return by year; coins per leg over time; the Deflated Sharpe Ratio at 751 trials;
- every period and 2022;
- outside any verdict: what P1's idle cash would have earned at the 3-month Treasury bill rate (FRED DTB3, on disk),
  by regime. This is bookkeeping, not a strategy and not a trial.

**If it passes:** a paper ledger beside P1, paper only. **If it fails:** cross-sectional momentum on this universe is
closed.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- The paper's effect was measured mostly on 2014–2018 and on hundreds of small coins. On 16 large coins since 2019
  I expect it to be much weaker: the book alone at about 0 to +1%/month before stress, Sharpe 0 to 0.5.
- Bear months: the book alone between −1 and +2. Not enough for check 1.
- The worst day is a squeeze of a shorted coin, 15–30% of the book.
- I expect a FAIL on check 1.
- Check 5 almost certainly fails as well: stress funding of 1 bp/h on 2x gross is 1 × 24 × 30 × 2 = 14% a month,
  more than any plausible edge. The stress profile was built for books that are flat half the time.

## Result (run once, 2026-11-13): FAIL on all five checks. The ranking book lost money in bear months.

Book active from 2019-04-29. Coins per leg: 1–2 (2019–20), 3 (2021), 4 (2022), 5 (2023 on).

| | full %/mo | max DD | Sharpe | bear | flat | bull | 2022 | D | C | B | discovery | A | stress |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| X1 P1 + momentum book | +6.59 | −92% | 1.24 | −10.0 | −1.4 | +35.7 | −9.85 | +27.99 | +6.52 | −2.83 | +5.38 | −4.36 | −9.30 |
| P1 (comparator, reproduced exactly) | +8.50 | −84% | 1.49 | −8.0 | −3.9 | +38.2 | −9.57 | +27.99 | +6.24 | +0.77 | +6.25 | +3.32 | +1.19 |

**The book alone:**

| %/mo | Sharpe | max DD | worst day | worst month | bear / flat / bull | corr. with P1 | long leg bear / bull | short leg bear / bull |
|---|---|---|---|---|---|---|---|---|
| −1.62 | −0.17 | −92% | −30.9% (2021-02-13) | −50% | −1.9 / +2.2 / −1.0 | −0.07 | −11.8 / +16.3 | +8.7 / −12.9 |

By year: 2019 −40%, 2020 +101%, 2021 −58%, 2022 +1%, 2023 +48%, 2024 −44%, 2025 −19%, 2026 −67%. Deflated Sharpe
Ratio at 751 trials: 0.00. Costs averaged 6.4 bps a day at base.

**Reading:**
- The prediction was right on the verdict and a little too kind on the size: I expected the book alone at 0 to
  +1%/month; it made −1.6.
- In bear months the shorts made +8.7 and the longs lost −11.8. Recent winners fell harder than recent losers when
  the market turned, so the book was not neutral where it mattered.
- It is uncorrelated with P1 (−0.07) but has nothing to add, and its years swing between +101% and −67%. With 1–5
  coins per leg, one coin decides the week; the worst day (−30.9%) was a squeeze of a shorted coin.
- **Cross-sectional momentum on this universe is closed.** Trials charged: 751. P1 stays the live paper variant.

**Bookkeeping, not a test:** Treasury-bill interest on the sleeves P1 leaves in cash (47% of the time on average)
would have added +0.12%/month in bear months, +0.14 in flat and +0.05 in bull over 2016–2026. It is real and free of
market risk, but it is a tenth of a point.
