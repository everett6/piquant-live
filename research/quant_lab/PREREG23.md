# Pre-registration 23: volatility-expansion exit for the BTC+ETH trend core, aimed at bad markets (committed before any round-23 code runs)

## Why
The owner asked for better results in bad markets, using volatility. The live paper core P1 (BTC+ETH, 2x while
close > SMA50) earns +8.50%/month over 2016–2026 but loses −8.0%/month on average in bear months (PREREG11). Its
bear losses come from staying long into the first days of a crash and from false breakouts (PREREG7).

**Already tested and closed** (so not repeated here): sizing by the *level* of volatility (PREREG11 P2), a
drawdown brake (PREREG12 Q1), lower leverage (PREREG13 R1/R2), price-based fast exits and trailing stops (R3/R4),
volatility-scaled intraday stops and Grossman–Zhou sizing (PREREG14/15), shorting in bear markets (PREREG7), and a
beta-neutral book in volatility spikes (PREREG20).

**New here:** the *change* in volatility used as an on/off exit. Crashes usually start with short-term volatility
jumping well above its long-run level. The rule steps aside while that is true, whatever the price trend says.

## Data, periods, costs (all as PREREG13, same code)
- Coinbase BTC-USD and ETH-USD 1m candles resampled to daily (`lab.load_candles`, `lab.resample`).
- Two equal sleeves (`strategies5.two_sleeve`); ETH's half is cash before ETH has data.
- Positions are decided at each daily close from closes through that day and held over the next day
  (`lab.position_returns`).
- Costs: us-futures (taker 6 + slip 1 bps per unit of turnover, funding 0.1 bp/h). Stress: 1 bp/h funding.
- Periods: D 2016–17, C 2018–20, B 2021-01..2022-12-16, discovery 2022-12-17..2025-06, A 2025-07..now, full.
- Bear / flat / bull months: BTC buy-and-hold month < −5% / between / > +5% (`strategies5.regime_months`).

## Rules (2 fixed trials, no parameter search)
For each coin at close t, with r = daily close-to-close returns through t:
- **V1, volatility-expansion exit:**
  - ratio = std(last 10 r) / std(last 100 r);
  - 2x long when close > SMA50 **and** ratio < 1.5; otherwise flat.
- **V2, downside volatility-expansion exit:**
  - semi(N) = sqrt(mean(min(r, 0)²)) over the last N returns (the downside deviation);
  - ratio = semi(10) / semi(100);
  - 2x long when close > SMA50 **and** ratio < 1.5; otherwise flat.
- No position until 100 returns exist (P1 starts after 50 closes, so the comparison window is the same as P1's
  from day 101 on; the full-period P1 comparator is kept as published).
- 10, 100 and 1.5 are set here, before any code runs, and are not searched.
- If a denominator is 0 (no down days in 100), the ratio counts as below 1.5.

## Pass rule (2 trials: 740 + 2 = 742)
Each variant is judged on its own, against P1 rebuilt in the same run:
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points (P1: −8.0, so ≥ −5.0).
2. **Target kept:** full 2016–2026 geometric return ≥ 7.0%/month (the owner's target).
3. **Drawdown:** full max drawdown no worse than P1's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** full geometric return at stress costs > 0.

**Also reported:** flat and bull months, every period, losing months, worst month, days flat because of the
volatility gate, and the 2022 bear year.

**If one passes:** it joins the trend paper ledger as a new variant beside P1 (paper only). P1 stays live until the
new variant also beats it forward.
**If both fail:** volatility-expansion exits are closed for the daily core.

## Run
Once, on the desktop or the Pi, after synthetic tests pass.

## Prediction (written now)
- V1 also exits in sharp rallies, so it gives up bull-month return. It probably fails check 2.
- V2 reacts only to down moves, so it keeps more of the bull months. Volatility usually jumps after the first big
  drop, not before, so I expect a bear-month gain of about 1–2 points: short of check 1.
- I expect both to fail, V2 closer than V1.

## Result (run once, 2026-11-13): both FAIL check 1 (bad markets got worse, not better)

| | full %/mo | max DD | bear | flat | bull | 2022 | D | C | B | discovery | A | stress | gate-flat days BTC/ETH |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| V1 total-vol exit | +8.01 | −83% | **−10.1** | −1.0 | +35.6 | −8.94 | +26.48 | +6.23 | +0.67 | +5.08 | +3.71 | +1.47 | 225 / 156 |
| V2 downside-vol exit | +7.98 | −84% | **−10.0** | −2.3 | +35.6 | −9.57 | +24.27 | +5.69 | +2.74 | +5.52 | +3.57 | +1.37 | 190 / 149 |
| P1 (comparator, reproduced exactly) | +8.50 | −84% | −8.0 | −3.9 | +38.2 | −9.57 | +27.99 | +6.24 | +0.77 | +6.25 | +3.32 | +1.19 | – |

Checks 2–5 pass for both (≥ 7%/month, drawdown no worse than P1, A > 0, stress > 0). Check 1 fails: bear months
are about 2 points **worse** than P1 instead of 3 points better.

**Reading:**
- The prediction was wrong in direction. I expected a small bear-month gain; the gate made bear months worse.
- Where it helped: flat months (−3.9 → −1.0 for V1), period A (+3.32 → +3.71) and stress costs. Where it hurt:
  bear and bull months, discovery, and the full return (−0.5%/month).
- A likely cause, not tested: the gate fires after a big move, so inside bear months it mostly cuts the sharp
  rebounds that P1 catches, while the large drops that start a bear month happen before volatility has risen.
- The drawdown did not move (−83/−84%). Volatility-expansion exits are closed for the daily core.
- **Process note:** the runner's first run wrote its output over `results/round13.json` (a copied path not
  changed). It was moved unchanged to `results/round23.json` and round13.json restored from git before this
  commit. The run itself happened once.
- Trials charged: 742. P1 stays the live paper variant.
