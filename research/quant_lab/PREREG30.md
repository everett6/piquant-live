# Pre-registration 30: the trend core decided on four clocks instead of one (committed before any round-30 code reads real data)

## Why
The owner asked for a new algorithm fitted to his setup, and for proof.

**His setup:** a US retail account, 2x futures on Coinbase, and a Raspberry Pi that is on 24 hours a day. P1 uses the
Pi once a day: it decides at 00:00 UTC and is blind for the next 24 hours.

**What the last tests showed:** P1's −84% drawdown is whipsaw bleed, not crash days (PREREG26). Every attempt to add
a second source of return has failed (PREREG24–29). The leverage curve says 2x is about the most the rule can carry.
So the remaining lever is not more return but less noise in the return P1 already has.

**The idea:** split the book into four equal tranches. Each runs the unchanged P1 rule on its own daily candle,
closing at 00:00, 06:00, 12:00 and 18:00 UTC. Nothing is fitted, and no tranche knows about the others.

## The proof (what must be true before any data)
Let `r_1..r_4` be the four tranches' returns over the same period, each with variance `σ_k²`, and let
`E = (r_1 + r_2 + r_3 + r_4)/4`.

- **Mean:** `mean(E)` is the average of the four means. Splitting cannot add or remove expected return.
- **Risk:** `sd(E) ≤ (σ_1 + σ_2 + σ_3 + σ_4)/4`, with equality only if the four are perfectly correlated
  (triangle inequality for standard deviations). With equal variances and pairwise correlation `ρ`,
  `sd(E) = σ·sqrt((1 + 3ρ)/4)`.
- **So** the split's Sharpe ratio is at least the risk-weighted average of the four tranches' Sharpe ratios, and
  strictly higher whenever `ρ < 1`. At `ρ = 0.9` the gain is 4%: P1's 1.49 becomes about 1.55.

What the proof does **not** say:
- It does not say the split beats the 00:00 clock. If 00:00 happens to be the best of the four, the split is worse
  than P1 while still being better than a clock picked at random.
- P1's settings were chosen on 00:00 closes (PREREG5 and 11), so the history is tilted in that clock's favour.
- It promises less noise, not more return. It will not move bear months by 3 points.

## Data (already on disk)
Coinbase BTC-USD and ETH-USD 1-minute candles (`lab.load_candles`), resampled to 6-hour bars and to four sets of
daily bars offset by 0, 6, 12 and 18 hours.

## Rules (4 trials: three new clocks and the split; 751 + 4 = 755)
For each coin and each clock `k ∈ {0, 6, 12, 18}`:
- daily candles that close at hour `k` UTC;
- at each such close: 2x long if close > SMA50 of that clock's closes, otherwise flat (P1's rule,
  `strategies5.m1_positions(·, 50, 2.0)`);
- the position is held for the next 24 hours.

**E4, the split:** each coin's position is the average of its four clocks (so 0, 0.5, 1, 1.5 or 2x), re-set every 6
hours. BTC and ETH in equal halves (`strategies5.two_sleeve`).

**Accounting:** everything is computed on the same 6-hour grid with `lab.position_returns`' formula: position held
over a bar × the bar's return, 6 + 1 bps per unit of turnover, funding 0.1 bp/h (stress 1 bp/h).

**Comparator:** the 00:00 clock on that same grid ("P1 on the grid"). P1 as published (daily bars) is printed beside
it to show the two agree.

**No clock will be picked.** The three other single clocks are reported so the split can be judged against all
four, and counted as trials. Whatever they show, only the split can be adopted.

## Pass rule (E4 against P1 on the grid, 2016–2026)
1. **Less noise:** Sharpe ratio (daily returns) higher than P1's.
2. **Drawdown:** max drawdown no worse than P1's.
3. **Return kept:** geometric return ≥ 7.0%/month and within 0.25 points of P1's.
4. **Bad markets:** mean bear-month return no worse than P1's.
5. **Recent data and stress:** period A geometric return > 0, and full return at stress costs > 0.

**Also reported:** each clock alone (return, Sharpe, drawdown, bear months); the correlation between clocks; E4's
Sharpe against the median and the best single clock; every period; turnover; volatility.

**If it passes:** a four-clock variant is added to the paper ledger beside P1 (paper only), which needs the Pi engine
to step the ledger every 6 hours. **If it fails:** the four-clock split is closed, and P1 stays on one clock.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- Correlation between clocks about 0.85–0.95. E4's volatility 3–6% below a single clock's, its return within half a
  point of the four-clock average.
- E4 beats the median clock on Sharpe (the proof says it must beat the average).
- Against P1 itself it is about a coin flip, tilted against E4 by the 00:00 selection: I put a pass at 40%.
- Checks 2 and 4 are the likeliest to fail: the drawdown and bear-month figures of one path are noisy.
