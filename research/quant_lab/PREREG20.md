# Pre-registration 20: beta-neutral BTC–ETH book (committed before any round-20 code runs)

## Why
The owner asked (2026-11-11) whether a beta-neutral book avoids the jump losses that break drawdown floors: long
BTC against a beta-weighted short in ETH, held always or only during volatility spikes. The Phase 1 diagnostics
showed directional 99.9% daily losses of −17% (BTC) and −29% (ETH).

Hedging removes most of the common move, so the tail must shrink. The open question is whether anything is left to
earn: a hedged pair with no selection signal has no obvious source of return, and it pays costs on two legs.

- SOL and other high-beta coins are not in the daily history here, so the book is BTC against ETH only.
- Shorts are on futures (us-futures costs).

## Data
- Coinbase daily closes for BTC-USD and ETH-USD (`lab.load_candles` resampled to 1D), 2016-06-01 to the latest
  complete day. ETH needs 60 days of history before the first hedge ratio.

## Book (fixed)
- **Hedge ratio:** β_t = the slope of BTC's daily return on ETH's daily return over the last 60 days through
  close t, clipped to [0, 1.5].
- **Position over day t+1:** +1 BTC and −β_t ETH, per 1 of capital. Rebalanced at each close.
- **Costs:** turnover on each leg × (6 bps fee + 1 bp slippage); funding 0.1 bp/hour on the absolute size of each
  leg. Stress: 1 bp/hour.

## Rules (2 fixed trials, no search)
- **N1, always on:** hold the book every day.
- **N2, only in volatility spikes:**
  - hold the book over day t+1 only when BTC's 30-day realised volatility at close t is at or above the 90th
    percentile of its own previous 365 daily values;
  - cash otherwise;
  - entering and leaving pay full costs on both legs.

## Pass rule (2 trials: 734 + 2 = 736)
A rule passes only if all of these hold, over the full window:
1. Net annual Sharpe ratio ≥ 1.0 (daily returns, 365 days).
2. Net return > 0 in at least 4 of the 5 periods: D 2016–17, C 2018–20, B 2021..2022-12-16, discovery
   2022-12-17..2025-06, A 2025-07 onward.
3. Stress net annual Sharpe > 0.5.
4. Its 99.9% daily loss is no worse than half of long BTC's over the same days (N2: over the days it holds).

## Also reported (descriptive, not part of the pass rule)
- The 99%, 99.9% and worst daily loss of: long BTC, long ETH, an equal-weight long of both, and the hedged book.
  This shows how much netting cuts the 1-in-1,000-day loss.
- Annual return, volatility and max drawdown of each rule; N2's share of days held; the distribution of β.

**If one passes:** it becomes a paper ledger. It needs ETH futures shorts, which the engine's paper model supports.

**If none pass:** the beta-neutral BTC–ETH book with these rules is closed. The tail-reduction numbers stay as a
description.

## Prediction (written now)
- Netting cuts the 99.9% daily loss by about half to two thirds against long BTC.
- N1: Sharpe between −0.3 and +0.3. There is no reason for BTC to outperform beta-weighted ETH on average, and two
  legs pay double costs and funding. Fails check 1.
- N2: few days held, noisy, Sharpe near 0. Fails check 1.
- A hedged book lowers risk by removing the exposure that earned the return. It does not raise the Sharpe ratio
  without a signal for which leg to favour.

## Result (run once, 2026-11-11): both FAIL

Window 2016-06-01 to 2026-11-10. Hedge ratio β: median 0.62 (10th–90th percentile 0.29–0.82).

| Rule | Net Sharpe | Stress Sharpe | Return/yr | Volatility | Max drawdown | Days held | Periods > 0 |
|---|---|---|---|---|---|---|---|
| N1 always on | +0.42 | −2.36 | +9.5% | 44.7% | −80% | 100% | 3/5 (B −70%, A −34%) |
| N2 volatility spikes only | +0.27 | −0.37 | +3.4% | 18.5% | −40% | 9% | 4/5 |

**Daily loss tails (same days):**

| | 99% | 99.9% | Worst day |
|---|---|---|---|
| Long BTC | −9.9% | −15.0% | −38.8% |
| Long ETH | −12.4% | −19.9% | −43.3% |
| Equal-weight long | −10.7% | −16.4% | −41.1% |
| Hedged book (N1) | n/a | **−12.0%** | **−16.7%** |
| Hedged book on spike days (N2) | n/a | −10.5% (BTC same days −12.7%) | −11.1% (BTC −13.2%) |

**Reading:**
- **Netting cuts the worst day by 57%** (−38.8% → −16.7%), because the market-wide crash days cancel.
- **It cuts the 1-in-1,000 day by only 20%** (−15.0% → −12.0%). Check 4 needed a cut of half.
  - ETH's own moves are large: β is only about 0.6, and ETH has days of ±15% that BTC does not share.
  - The hedged book therefore still has 45% annual volatility and an −80% drawdown, from ETH outrunning BTC in
    2021 and since mid-2025.
- **Nothing is earned for it.** Sharpe is 0.42 before stress and strongly negative with stress funding on two legs.
  The +9.5% a year is BTC beating beta-weighted ETH over this sample, not a repeatable edge: it lost 70% in 2021–22.
- A beta-neutral BTC–ETH book swaps market risk for pair risk, which is nearly as large here. It does not solve the
  floor problem.

Trials charged: 736.
