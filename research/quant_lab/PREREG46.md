# Pre-registration 46: puts, shorts and hedges again, each built to fix the cause of an earlier failure, plus volatility harvesting (committed before any round-46 code reads real data)

Asked for by the owner on 2026-11-18: "how can you take advantage of volatility, and try again on buying puts,
shorting and hedges; try new strategies and analyse what went wrong". Only synthetic tests had run when this was
committed.

## What went wrong before (from the recorded results; no new data was read for this section)

| Family | Tests | What happened | Root cause |
|---|---|---|---|
| Bought puts, tied to the trend | PREREG26 | Puts returned 76–81 cents per dollar of premium; about 75 purchases per coin | **Timing and spread.** Bought at each trend entry, sold at each exit, so every whipsaw paid the spread twice; sold exactly when a falling market made them valuable |
| Bought put spreads, always on | PREREG27 | Paid back 53–71%; +1.1 to +1.8 points in bear months for −2%/month overall | **Paying for insurance in every month.** 70% of months are not bear months, and implied volatility ran 4–8 points above what followed (the buyer pays the variance premium) |
| Sold iron butterflies | PREREG28 | Paid out 109–118% of the credit | **Costs ate the premium.** The model's spread (10% of volatility each way) is about the size of the whole variance premium (4–8 points on 57–72%), and jumps did the rest. Selling was unconditional, including when options were cheap |
| Long/short trend | PREREG7 | Bear months better (−12.7 → −6.6) but flat months −10 to −14, drawdown −87 to −93% | **Whipsaw.** A short at the first close under SMA50 is hit by every bear-market rally; crypto drifts up |
| Minute-horizon bear shorts | PREREG9 | −11.5 bps per trade | **Costs** larger than the edge at that horizon |
| Long BTC / short altcoins | PREREG25 | −2.5%/month in bear months; one day −83.6% | **Squeeze.** A thin altcoin can double in a day |
| Trend in stocks, bonds, gold, dollar | PREREG24 | Earned about 0 | **No crisis alpha** in crypto bear months |
| Funding, stablecoin and entropy gates | PREREG31, 32, 35 | Wrong in sign or late | **The signal moved with the fall,** not before it |

**Three causes cover all of it:**
1. **Insurance bought at the ask in every month costs more than crashes pay back,** because option prices already
   include crash fear. It only pays when it is cheap or when it is held through the whole fall.
2. **Crypto bear markets are slow grinds with violent rallies,** which is the worst case for a short opened at the
   first sign of weakness and for a fixed-strike put that expires before the grind ends.
3. **P1's drawdown is mostly whipsaw bleed, not crash days** (PREREG26). Crash insurance protects against the rarer
   risk. The trend-age ramp (QD, PREREG35) worked because it attacks the whipsaw directly.

## The four new strategies (each aims at one cause)

**V1, volatility harvesting by rebalancing (cause 2, turned around).** Hold 25% BTC, 25% ETH and 50% T-bills on spot,
and rebalance back to those weights whenever a coin's weight drifts more than 5 points. Selling after rises and buying
after falls turns volatility itself into return (the "rebalancing premium"): the more the price swings without going
anywhere, the more it earns. No options, no shorts, no leverage. Not tested here before (PREREG44's harvester scaled
exposure by volatility; it did not rebalance against cash).

**V2, sell put spreads only when they are expensive (fixes PREREG28).** Each day, for each coin, open a 30-day put
spread *sold* (short at the money, long one implied standard deviation below), but only when implied volatility
(Deribit DVOL) exceeds the last 30 days' realised volatility by at least 15 points **and** the coin is above its SMA50.
In plain terms: get paid to promise to buy the dip, only when the market overpays for that promise and the trend is
up. Loss per tranche is capped by the long put.

**V3, buy put spreads only when they are cheap (fixes PREREG26–27).** The same spread *bought*, opened only when implied
volatility is at or below the last 30 days' realised volatility (insurance priced below recent moves) **and** the coin
is above its SMA50 (the book is long). Every tranche is held to expiry; none is sold at a trend exit.

**V4, short only established downtrends, ramped in (fixes PREREG7 and 25).** BTC and ETH only (no thin altcoins).
Short while the close is below both SMA50 and SMA200, sized `−1 × (1 − e^(−n/5))` with `n` the number of consecutive
closes below SMA50: the QD ramp, mirrored. A downtrend that has just started (the rally-prone part) gets a small short;
an established one gets up to 1x. Funding is paid or received as it falls; costs as P1 (futures).

## Limits stated before the run
- **This reopens two closed families at the owner's request.** PREREG27 said I would not propose another
  bought-option test on this data, and PREREG28 closed option selling. V2 and V3 are conditional versions of those
  instruments, designed after seeing why they failed. A pass on them is weaker evidence than a pass on new data.
- **What I already know:** everything in the table above, including that DVOL ran 4–8 points over realised on average.
  I have not looked at how often the 15-point and 0-point gates are met, or at returns when they are.
- **The option model is PREREG26's** (Black–Scholes on DVOL, 10% of volatility as the spread each way, fees). It is not
  re-tuned. If the real spread is narrower, V2 is understated and V3 overstated; that cannot be checked without real
  quotes.
- Four more trials (796 → 800). Some pass by chance is expected across 800.

## Data (all on disk; nothing is downloaded)
Coinbase BTC/ETH daily candles; Deribit DVOL (2021-03-24 on); Binance funding; FRED DTB3.

## How each is judged: as a 20% sleeve beside blend A
Blend A is the dual-speed blend that fits the owner's drawdown limit (`Q42_dualspeed_60_40_carry`, +2.12%/month,
−19.8%), rebuilt exactly as in PREREG45. Each variant's portfolio is **80% blend A + 20% the new sleeve**, rebalanced
daily, compared with blend A alone on the same window. V1 and V4: 2020-01-01 to 2026-10-31. V2 and V3 (implied
volatility needed): 2021-04-01 to 2026-10-31, with A rebuilt on that window. Each option sleeve trades on equal BTC and
ETH halves at q = 0.5 units of notional per unit of sleeve equity in total (PREREG27's size), 1/30 per daily tranche.

## Pass rule (each variant alone, against blend A alone on its window)
1. **Bad markets:** mean bear-month return at least 1.0 point better than A's.
2. **Drawdown:** max drawdown no worse than A's.
3. **Return:** geometric monthly return no more than 0.10 points below A's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** return at stress costs no more than 0.10 points below A's at stress.

**Also reported:** each sleeve alone (return, drawdown, Sharpe, bear/flat/bull months, worst day, correlation with A);
for V2/V3 the share of days the gate was open, premium paid or received and paid back; for V1 the number of
rebalances; for V4 days short and funding paid.

**If one passes:** it can join the paper ledger beside blend A, paper only, asked first. **If all fail:** each family
is closed again, with the cause recorded.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- V1: alone about +1.5–2.5%/month with a drawdown near −45%. As a 20% sleeve it worsens A's drawdown and bear months.
  Fail. 1 in 10.
- V2: gate open on perhaps 15–25% of trend-on days. Alone small positive, with occasional sharp losses. As a sleeve it
  adds a little return but does not improve bear months by a point. Fail. 1 in 6.
- V3: gate rarely open (options are seldom cheaper than recent moves). Too little exposure to move bear months a full
  point. Fail. 1 in 8.
- V4: the best chance. Short in 2022 and parts of 2025–26 should lift bear months; the ramp and SMA200 filter limit
  the whipsaw. Risk: bear-market rallies and the 2026 chop. About 1 in 3.
