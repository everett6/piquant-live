# Pre-registration 32: a stablecoin supply gate on P1's leverage (committed before the supply data is downloaded or any round-32 code reads real data)

Approved by the owner on 2026-11-13 ("Yes, stablecoin gate"). Only synthetic tests had run when this was committed.

## Why
The owner asked for ways to beat the market that need no order-book data. PREREG31 tried the first non-price gate
(funding) and it was wrong in sign. This is the second and last idea of that kind on the list.

**The idea:** the total supply of dollar stablecoins is the amount of dollars sitting inside crypto. When it grows,
new money is arriving. When it shrinks, money is leaving. The gate holds each sleeve at 1x instead of 2x while the
supply has shrunk over the last 30 days.

## Limits stated before the run
- **What I already know.** It is public knowledge that stablecoin supply grew through 2020–21, peaked around
  May 2022, shrank until late 2023 and has grown since. So I expect the gate to be on from about mid-2022 to late
  2023. P1's drawdown began in November 2021, six months earlier. I have not seen the series against P1's returns.
- **PREREG31 lowered my confidence** in "cut leverage on a non-price signal". There the signal marked strength, not
  danger.
- **The series is not point-in-time.** DefiLlama publishes today's version of history. Past values may have been
  revised, and coins added later are back-filled. The signal is lagged one day; revisions cannot be undone.
- **One slow signal.** A 30-day supply change switches a handful of times in the whole window. Few switches means
  the result rests on a few episodes.

## Data
- **Supply:** DefiLlama `stablecoins.llama.fi/stablecoincharts/all` (free, no key), total circulating USD-pegged
  supply, daily. Downloaded once to `~/piquant-data/stablecoins/`, not committed.
- **Prices and P1:** as PREREG13/23–31.
- **Window:** 2019-01-01 to 2026-10-31. If the supply series gives no signal by 2019-01-01, the window starts on the
  first day of the first month that has one for the whole month; that is the only permitted change.
- Live Pi tick data is not used.

## Rule (1 fixed trial, 757 + 1 = 758; nothing is searched)
At each daily close t, with `U_d` the supply stamped on day d:

    growth_t = U_{t−1} / U_{t−31} − 1

P1's position is computed as always. If it is 2x and `growth_t ≤ 0`, the sleeve holds 1x. If P1 is flat it stays
flat. The gate never raises a position. A day with no signal is not gated. Both coins use the same signal.

**H1** is this rule. 30 days, the zero threshold, the one-day lag and the 1x floor are fixed here. Costs are P1's:
6 + 1 bps per unit of turnover, funding 0.1 bp/h; stress 1 bp/h.

## Pass rule (PREREG31's five checks; every comparator rebuilt in the same run on the same window)
Let `L̄` be H1's average leverage on trend-on days, and "the line" the P1 rule run at constant `L̄`.
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points.
2. **Beats the line on return:** geometric monthly return ≥ the line's.
3. **Beats the line on drawdown:** max drawdown no worse than the line's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** geometric return at stress costs ≥ P1's at stress costs.

**Also reported:** share of trend-on days gated per coin; `L̄`; the dates the gate switched; P1, 1x and 1.5x on the
window; flat and bull months; 2022; worst day; mean next-day and next-7-day return on gated and ungated trend-on
days.

**If it passes:** it joins the paper ledger beside P1, paper only. **If it fails:** stablecoin supply as a leverage
gate is closed, and with it non-price leverage gates on the daily core.

## Run
Once, after this file is pushed and the synthetic tests pass. The download happens after the push.

## Prediction (written now)
- The gate is on for 10–20% of trend-on days, almost all of them between mid-2022 and late 2023.
- It misses the first half of P1's 2021–22 drawdown, and it halves the 2023 rally. The drawdown barely changes.
- Bear months improve by less than 1 point. Checks 1 and 3 fail; check 2 probably fails.
- Chance of a pass: about 1 in 6.

## Result (run once, 2026-11-13): FAIL. The signal pointed the right way, but too late and too weakly to pay.

Supply data: DefiLlama, 3,273 daily rows from 2017-11-29, no missing day in the window. Window 2019-01-01 to
2026-10-31 as registered: 94 months (31 bear, 19 flat, 44 bull).

| | %/mo | max DD | Sharpe | worst day | bear | flat | bull | 2022 | A | stress |
|---|---|---|---|---|---|---|---|---|---|---|
| H1 supply gate (average 1.80x) | +5.34 | −79.2% | 1.15 | −32.5% | −12.6 | −2.0 | +29.4 | −7.64 | +1.92 | −1.28 |
| the line at 1.80x | +5.50 | −80.3% | 1.19 | −29.3% | −11.2 | −2.6 | +27.9 | −8.51 | +3.14 | −1.12 |
| P1 (2x) | +5.74 | −83.9% | 1.19 | −32.5% | −12.5 | −3.0 | +31.1 | −9.57 | +3.32 | −1.59 |
| 1.5x | +4.99 | −73.2% | 1.19 | −24.4% | −9.2 | −2.0 | +23.0 | −6.91 | +2.78 | −0.52 |
| 1x | +3.76 | −56.9% | 1.19 | −16.2% | −6.1 | −1.2 | +15.0 | −4.42 | +2.05 | +0.10 |

Checks: 1 fails (bear −12.6 against P1's −12.5; −9.5 needed). 2 fails (+5.34 against the line's +5.50). 3 passes
(−79.2% against −80.3%). 4 passes. 5 passes (−1.28 against −1.59).

**What the gate saw:** it was on for 26% of all days and 19–20% of trend-on days: 65% of 2022, 74% of 2023, 48% of
2026, and almost never in 2019–21 or 2024–25. It switched 63 times.

| trend-on days | next day, gated / not | next 7 days, gated / not |
|---|---|---|
| BTC | +0.25% / +0.30% | +1.1% / +2.1% |
| ETH | +0.27% / +0.32% | +1.0% / +2.4% |

**Reading:**
- Unlike funding, the sign was right: returns after shrinking-supply days were about half those after growing-supply
  days. But they were still positive, so holding 1x on them gave up return. It did not avoid loss.
- Bear months did not move at all. The gate was off through 2021 and the first months of 2022, when the drawdown
  began, as predicted.
- It beat the line on drawdown by 1 point and lost to it on return, Sharpe (1.15 against 1.19) and period A (+1.92
  against +3.14; the gate was on for half of 2026).
- 63 switches is far more than the "handful" I expected: the 30-day change hovered around zero in 2023 and 2026.
- The prediction held on checks 1 and 2 and on where the gate was on. It was wrong on check 3, which passed.
- **Stablecoin supply as a leverage gate is closed, and with it non-price leverage gates on the daily core**
  (PREREG31–32). Trials charged: 758. P1 stays the live paper variant.
