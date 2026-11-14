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
