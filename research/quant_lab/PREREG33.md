# Pre-registration 33: funding carry on the trend core's idle capital (committed before any round-33 code reads real data)

Approved by the owner on 2026-11-13 ("Yes, idle carry"). Only synthetic tests had run when this was committed.

## Why
Two things have passed in this repo: the trend core (P1, PREREG11) and the funding carry (C1, PREREG19). P1 sits in
cash whenever a coin is below its SMA50, about half the time. The carry needs no view on direction. The idea is to
run each coin's carry on that coin's sleeve while the sleeve is flat, and nothing else.

## Limits stated before the run, and a correction
- **I told the owner this was "likely to work" and worth +0.2–0.4%/month. Writing the arithmetic down, that was too
  generous.** P1 flips about 14 times a year per coin. Every flip opens or closes the carry pair (6.7 bps of sleeve
  equity at base costs), about 1.9% a year. C1 earned +8.2% a year when always on; half the time is about +4%; and
  PREREG31 showed funding is high in rallies, which is when the sleeve is *not* idle. Idle-time carry is probably
  +1 to +3% a year before switching costs. Net: about zero to +0.1%/month.
- **What I already know:** C1's results by year (PREREG19: 2022 was −0.6%, 2025–26 +1.6% at stress), and that high
  funding goes with rising prices (PREREG31).
- **The fair comparison is not "P1 with idle cash at 0".** Idle money can sit in T-bills for free. The carry has to
  beat that.
- **Proxy:** Binance USDT funding, as PREREG19. Basis moves between spot and perpetual are ignored, as there.
- **Funding history starts in 2020.** Window 2020-01-01 to 2026-10-31.

## Data (all on disk; nothing is downloaded)
- Funding: Binance BTCUSDT/ETHUSDT 8-hourly (`~/piquant-collect/funding/csv`). Prices and P1: as PREREG13/23–32.
- T-bill rate: 3-month Treasury bill, FRED DTB3 (`~/piquant-data/macro/DTB3.csv`, fetched for PREREG24).
- Live Pi tick data is not used.

## Rules (2 fixed trials, 758 + 2 = 760; nothing is searched)
For each coin, at each daily close t, P1's position is computed as always.
- **Trend on:** the sleeve holds 2x futures (P1 exactly). No carry.
- **Trend off:** the sleeve holds the PREREG19 carry book on its own equity: spot worth N, short perpetual N with
  margin N/2, N = E/1.5. Funding, daily leg rebalancing, the liquidation rule and all costs are PREREG19's,
  unchanged (`strategies19`). Opening and closing the pair is charged at every switch, on top of P1's own costs.

**I1, immediate:** the carry is on whenever the sleeve is flat.
**I2, patient:** the carry opens only once the sleeve has been flat for 5 consecutive closes, and closes when the
trend turns on. The 5 days are there to skip short whipsaws; the number is fixed here.

Costs: base = P1's (6 + 1 bps, funding 0.1 bp/h) with PREREG19 base (spot 3 bps, perpetual 7 bps per side);
stress = P1 stress (funding 1 bp/h) with PREREG19 stress (costs doubled, funding received × 0.75).

## Pass rule (each variant alone; all comparators rebuilt in the same run on the same window)
"P1 + bills" is P1 with each flat sleeve earning the T-bill rate, at no cost.
1. **Beats free cash:** geometric monthly return ≥ P1 + bills.
2. **Drawdown:** max drawdown no worse than P1's.
3. **Bad markets:** mean bear-month return no worse than P1's.
4. **Recent data:** the carry leg's own return in period A (2025-07 on) > 0.
5. **Stress:** the carry leg's own full-window return at stress costs > 0.

**Also reported:** the carry leg alone by year, its time in market, number of switches, switching cost paid,
funding earned, liquidations; P1 and P1 + bills; mean funding on idle days against trend-on days.

**If one passes:** it joins the paper ledger beside P1, paper only. **If both fail:** carry on idle capital is
closed, and idle sleeves should simply hold T-bills.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- Mean funding on idle days is well under half of that on trend-on days.
- I1's carry leg: about 0 at base costs, negative at stress. I2's: slightly positive at base.
- Both lose to T-bills (check 1). Checks 2 and 3 pass, because the leg is nearly riskless.
- Chance of a pass: about 1 in 5 for I1, 1 in 4 for I2.
