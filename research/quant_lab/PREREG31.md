# Pre-registration 31: a funding crowding gate on P1's leverage (committed before any round-31 code reads real data)

Approved by the owner on 2026-11-13 ("Yes, funding gate"). Only synthetic tests had run when this was committed.

## Why
The owner asked for ways to beat the market that need no order-book data.

Thirty tests are closed. Every rule that cut P1's leverage did it from past prices (stops, brakes, volatility exits,
PREREG12–14, 23): it acted after the fall and missed the rebound. Lower constant leverage (PREREG13) gave up return in
proportion. No test has used a variable that is not a price.

**The idea:** perpetual funding is what longs pay shorts to hold leverage. When it is high, longs are crowded and
borrowed, and that is the state in which liquidation cascades start. The gate cuts each sleeve from 2x to 1x while
its coin's funding is high. It is set before a fall, by a quantity that is not the price.

## Limits stated before the run
- **Funding history starts in 2020.** Nothing can be checked on 2016–19. The verdict window is 2021-01-01 to
  2026-10-31, about 70 months.
- **What I already know about this data.** From PREREG19: mean funding was +11.6% a year (BTC) and +13.7% (ETH), it
  was negative 14% of the time, and the 7-day mean was at or above 0.01% per 8 hours about 30% of the time. From
  PREREG26: on 2021-04 onward 1x, 1.5x and 2x made +1.62, +1.97 and +2.01%/month with drawdowns of −57%, −73% and
  −84%. I have not looked at when funding was high against when P1 lost.
- **The window flatters any leverage cut.** In it, extra leverage bought almost no return. That is why the pass rule
  compares the gate with a constant leverage of the same average size, and not only with P1.
- **Proxy:** Binance USDT-margined funding (free, deepest history). A US venue's funding differs in level.

## Data (already on disk; nothing is downloaded)
- Funding: Binance BTCUSDT and ETHUSDT 8-hourly funding, 2020-01 to 2026-10 (`~/piquant-collect/funding/csv`,
  fetched for PREREG19).
- Prices and P1: as PREREG13/23–30 (`lab.load_candles` → daily, `strategies5.m1_positions(·, 50, 2.0)`,
  `strategies5.two_sleeve`, `lab.position_returns`).
- Live Pi tick data is not used.

## Rules (2 fixed trials, 755 + 2 = 757; nothing is searched)
For each coin, at each daily close t:
- `F_t` = mean of that UTC day's funding payments; `S_t` = mean of `F` over the last 7 days (t included). All of
  these payments are stamped before the close.
- P1's position is computed as always. If it is 2x and the gate is on, the sleeve holds 1x. If P1 is flat it stays
  flat. The gate never raises a position. A day with no signal is not gated.

**G1, fixed level:** the gate is on while `S_t > 0.02%` per 8 hours (about 22% a year). 0.01% is the neutral rate
built into the exchange's formula; the gate is twice that.

**G2, relative level:** the gate is on while `S_t` is above the 80th percentile of `S` over the previous 365 days
(t not included; at least 350 values needed).

7 days, 0.02%, the 80th percentile, 365 days and the 1x floor are fixed here. Costs are P1's: 6 + 1 bps per unit of
turnover, funding 0.1 bp/h; stress 1 bp/h.

## Pass rule (each variant alone; every comparator rebuilt in the same run on the same window)
Let `L̄` be the variant's average leverage on trend-on days, and "the line" the P1 rule run at constant `L̄`.
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points.
2. **Beats the line on return:** geometric monthly return ≥ the line's.
3. **Beats the line on drawdown:** max drawdown no worse than the line's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** geometric return at stress costs ≥ P1's at stress costs. (P1 itself was negative at stress on
   2021-04 onward, so "> 0" would only measure the window.)

**Also reported:** share of trend-on days gated per coin; `L̄`; P1, 1x and 1.5x on the window; flat and bull months;
2022; worst day; mean next-day and next-7-day return of each coin on gated and ungated trend-on days.

**If one passes:** it joins the paper ledger beside P1, paper only, fed by live funding from a public API.
**If both fail:** funding as a leverage gate is closed.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- G1 is on for roughly 10–20% of trend-on days, mostly early 2021 and early 2024. G2 is on for about 20–25%.
- Both improve the drawdown by a few points and bear months by about 1 point. Check 1 (+3.0) fails for both.
- Checks 2 and 3 are the real question: about even for G1, a little worse for G2.
- Chance of a pass: about 1 in 4 for G1, 1 in 6 for G2.

## Result (run once, 2026-11-13): both FAIL. High funding came before higher returns, not lower.

Window 2021-01-01 to 2026-10-31: 70 months (23 bear, 16 flat, 31 bull).

| | %/mo | max DD | Sharpe | worst day | bear | flat | bull | 2022 | A | stress | checks failed |
|---|---|---|---|---|---|---|---|---|---|---|---|
| G1 fixed level (average 1.81x) | +3.70 | −82.6% | 0.97 | −24.9% | −9.7 | −4.9 | +24.1 | −9.57 | +3.32 | −2.56 | 1, 3 |
| the line at 1.81x | +3.56 | −80.5% | 0.93 | −29.5% | −10.1 | −3.7 | +23.2 | −8.56 | +3.15 | −2.69 | – |
| G2 relative level (average 1.70x) | +2.48 | −83.9% | 0.77 | −27.6% | −10.7 | −4.1 | +21.2 | −9.57 | +3.71 | −3.33 | 1, 2, 3, 5 |
| the line at 1.70x | +3.49 | −78.1% | 0.93 | −27.6% | −9.5 | −3.4 | +21.7 | −7.95 | +3.02 | −2.38 | – |
| P1 (2x) | +3.64 | −83.9% | 0.93 | −32.5% | −11.2 | −4.1 | +25.6 | −9.57 | +3.32 | −3.24 | – |
| 1.5x | +3.30 | −73.2% | 0.93 | −24.4% | −8.3 | −2.9 | +19.1 | −6.91 | +2.78 | −1.88 | – |
| 1x | +2.57 | −56.9% | 0.93 | −16.2% | −5.5 | −1.8 | +12.6 | −4.42 | +2.05 | −0.89 | – |

Bear months needed −8.2 or better; G1 reached −9.7 and G2 −10.7.

**What the gate saw** (trend-on days only, mean return of the coin after the day):

| | days gated | next day, gated / not | next 7 days, gated / not |
|---|---|---|---|
| G1 BTC | 17% | +0.25% / +0.19% | +1.2% / +1.1% |
| G1 ETH | 20% | +0.36% / +0.26% | +3.1% / +1.3% |
| G2 BTC | 29% | +0.37% / +0.13% | +2.1% / +0.8% |
| G2 ETH | 30% | +0.50% / +0.18% | +3.1% / +1.0% |

**Reading:**
- The idea was wrong in sign. Days with high funding were followed by higher returns than other trend-on days, in
  all eight cells. High funding marked the strong part of a rally, not its end. Cutting leverage there gave up the
  best days.
- The gate was never on in 2022: both variants' 2022 is P1's −9.57 exactly. The drawdown and most bear months came
  when funding was low, so the gate could not touch them.
- G1's one real effect was the worst day (2021-05-19): −32.5% became −24.9%. Its return and Sharpe edge over P1
  (+0.06 points, 0.97 against 0.93) is too small to mean anything, and it failed the drawdown check against the line.
- G2, with the lower and adaptive threshold, cut more of the good days and lost 1.2 points a month.
- The prediction held on check 1 and on the share of days gated. I expected checks 2 and 3 to be a coin flip for
  G1; it split them.
- **Not to be tested next:** the opposite rule (more leverage when funding is high) was suggested by this result on
  this data. Running it here would be fitting to what was just seen. It could only be judged on new data.
- **Funding as a leverage gate is closed.** Trials charged: 757. P1 stays the live paper variant.
