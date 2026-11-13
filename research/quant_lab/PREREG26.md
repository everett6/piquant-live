# Pre-registration 26: convex leverage, the borrowed half of the trend core held as an option priced from implied volatility (committed before any round-26 code reads real candles or any implied-volatility data is downloaded)

Approved by the owner on 2026-11-13. Only synthetic tests had run when this was committed.

## Why
The owner wants about 5%/month with a much smaller maximum drawdown, without repeating a closed test.

Every closed test (PREREG1–25) chose a position `p_t` from past data and earned `p_t × r_{t+1}`
(`lab.position_returns`). Given the past, that payoff is a straight line through zero in tomorrow's return. A rule of
this kind can only change the slope of the line:
- cutting the slope after a loss (stops, brakes, volatility exits, Grossman–Zhou) arrives after the gap and misses the
  rebound;
- cutting it all the time (1x, 1.5x) gives up return in proportion.

The measured line for the trend core, 2016–2026:

| leverage | return %/mo | max DD | bear months |
|---|---|---|---|
| 1x (R1) | +5.18 | −57% | −4.2 |
| 1.5x (R2) | +7.09 | −73% | −6.1 |
| 2x (P1) | +8.50 | −84% | −8.0 |

To get off this line the payoff has to bend: lose like 1x when the market falls and earn like 2x when it rises. An
option does that by contract. A stop-loss tries to do it by trading, and pays the gap and the whipsaw. An option
buyer pays a premium set in advance by implied volatility, and the seller carries the gap.

## Mathematics
**Payoff.** A sleeve holds `L = 2` units of futures while close > SMA50 (P1 exactly), plus `q` puts per unit of
equity, struck at the money when bought. Over the life of one put, with `R = S_T / K − 1` and premium `π` (fraction
of spot):

    sleeve payoff = L·R + q·max(−R, 0) − q·π

- `q = L − 1 = 1`: payoff = `R + max(R, 0) − π`. Below the strike it loses like 1x. Above it earns like 2x.
- `q = L = 2`: payoff = `2·max(R, 0) − 2π`. Below the strike the loss is the premium only.

**Why leverage needs this (Kelly with jumps).** For daily drift `μ`, variance `σ²` and a jump of size `−J` with
probability `λ`, the log-growth of an L-times linear book is

    g(L) = L·μ − L²σ²/2 + λ·ln(1 − L·J)

The last term goes to −∞ as `L·J → 1`. The worst daily closes on record are −39.5% (BTC) and −44.6% (ETH) (log
−50.3% and −59.1%, `docs/PHASE1_TAIL_RISK_2026-11.md`), so a 2x book loses 79–89% in one such day. With `q = L − 1`
the jump term becomes `λ·ln(1 − J − π)`: the borrowed unit cannot lose more than its premium.

**Price.** Black–Scholes on the futures price (no interest term):

    P(S, K, σ, τ) = K·N(−d2) − S·N(−d1),   d1 = [ln(S/K) + σ²τ/2] / (σ√τ),   d2 = d1 − σ√τ

At the money, `π ≈ 0.4·σ·√τ`: about 6.9% of spot for 30 days at 60% volatility. The expected cost is not the
premium. It is the gap between implied and later realized volatility, plus the spread. If realized volatility is 85%
of implied and the spread adds 10%, the drag is about 25% of the premium: roughly 1.7%/month per unit of `q` while
the trend is on, and about half that over all days.

**Why 30 days and at the money.** These are the only tenor and strike that the free data prices directly. Deribit's
DVOL index is a 30-day implied volatility. Cost per day of protection falls with tenor (`π/τ ∝ 1/√τ`): daily
at-the-money puts would cost about 5%/month and leave nothing. A drawdown is a multi-week fall, so a 30-day put
covers the thing being measured.

## Data (free, public, no key; downloaded once to `~/piquant-data/dvol/`, not committed)
- **Implied volatility:** Deribit `public/get_volatility_index_data`, currency BTC and ETH, resolution 1D, from the
  first available day (2021-03) to the run date. The daily close is used, divided by 100.
- **Prices:** P1 exactly as PREREG13/23/24/25 (`lab.load_candles` → daily, `strategies5.m1_positions(·, 50, 2.0)`,
  `strategies5.two_sleeve`).
- **Window for the verdict:** 2021-04-01 to the run date, the months where DVOL exists for both coins. It contains
  P1's worst drawdown (−84%, 2021–22). SMA50 uses earlier closes for warm-up.
- **Limit of this test:** no free history of real option quotes exists. Premiums are modelled from DVOL. That is the
  main threat to the result, and the stress case exists for it.

## Rules (2 fixed trials, 746 + 2 = 748; nothing is searched)
For each coin, at each daily close t (`on_t` = P1's sleeve is long, decided at close t):
1. **Buy** when `on_t` and no put is held: a put with strike `K = S_t` and 30 days to expiry, `q` per unit of sleeve
   equity. Paid at volatility `DVOL_t × (1 + m)`.
2. **Hold** to expiry. Each day it is marked at `P(S_t, K, DVOL_t, days left / 365)`, and the day's change
   `q·(V_t − V_{t−1}) / S_{t−1}` is added to the sleeve's return for day t.
3. **Expiry** (30 days after purchase): settles at `max(K − S_t, 0)`. If `on_t`, a new put is bought at that close.
4. **Trend off** (`on_t` false while a put is held): the put is sold at that close at volatility `DVOL_t × (1 − m)`,
   never below its intrinsic value.
5. No strike changes, no early rolls, no sizing rule. The futures leg is P1 unchanged.

**Known gap:** the strike stays where it was bought for up to 30 days. Gains made since then are not covered, and a
fall gives them back at the full 2x before the put starts to pay. In the synthetic crash test (−60% in a day, strike
4% below the last close) the 2x book lost 120%, K1 lost 68% and K2 lost 15%.

- **K1, convex leverage:** `q = L − 1 = 1`.
- **K2, full call:** `q = L = 2`.

**Costs:**

| | spread `m` on volatility | fee per trade | futures |
|---|---|---|---|
| base | 0.10 | min(3 bps of spot, 12.5% of premium) (Deribit's schedule) | us-futures, funding 0.1 bp/h |
| stress | 0.25 | min(6 bps of spot, 25% of premium) | funding 1 bp/h |

**Stated simplifications:**
- the put count is kept at `q × equity / spot` each day and the small rebalancing trades are not charged (as in
  PREREG25);
- 30-day DVOL prices a put with fewer days left; in a crash short-dated volatility is usually higher, so this
  under-marks the put and works against a pass;
- a missing DVOL day uses the last value; no put is bought before the first DVOL value.

30 days, at the money, `q`, `m` and the fees are fixed here.

## Pass rule (each variant on its own; all comparators rebuilt in the same run on the same window)
The claim is that the bent payoff beats the straight line: **1.5x's return with 1x's drawdown.**
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points.
2. **Return kept:** geometric monthly return ≥ the 1.5x book's (R2).
3. **Drawdown:** max drawdown no worse than the 1x book's (R1).
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** geometric return at stress costs > 0.

Checks 1, 4 and 5 are the standard overlay checks. Checks 2 and 3 replace "≥ 7%/month" and "no worse than P1":
P1 itself does not make 7%/month in this window, and the owner now asks for a smaller drawdown, not an equal one.

**Also reported:**
- every period inside the window, flat and bull months, 2022, the worst day and the worst month;
- premium paid, payoff received and their ratio; days protected; mean DVOL against later realized volatility;
- R1, R2 and P1 on the same window;
- outside the verdict: 2016–2021 with premiums priced from trailing 30-day realized volatility × 1.2, labelled as
  a model with no market price behind it.

**If one passes:** it joins the trend paper ledger beside P1, paper only, with the put legs marked from live Deribit
quotes (public API). How a US account would trade the put (CME micro options or spot-ETF options) is decided only
then. **If both fail:** bought-option protection is closed for the daily core.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- K1: bear months about −5 (1x's −4.2 less premium), so check 1 is a coin flip. Return lands between 1x and 1.5x,
  just short of check 2. Drawdown close to 1x's, a few points worse from premium paid in sideways months. I expect K1
  to fail check 2 or 3 narrowly. Chance of a pass: about 1 in 4.
- K2: smallest drawdown of all, but the premium (about 14% of equity per month while on) costs more than the trend
  earns in this window. Fails check 2.
- Either way the single-day loss is bounded, and the measured gap between implied and realized volatility is new
  information for this repo.

## Result (run once, 2026-11-13): both FAIL. Buying the puts cost more than the protection was worth.

Data: Deribit DVOL daily closes for BTC and ETH, 2021-03-24 to 2026-11-13 (2,061 days each, no gaps). Window
2021-04-01 on: 67 complete months (23 bear, 16 flat, 28 bull).

| | %/mo | max DD | worst day | bear | flat | bull | 2022 | B | discovery | A | stress | checks failed |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| K1 convex leverage (q = 1) | +0.18 | −83% | −22.5% | −10.7 | −5.1 | +17.2 | −9.46 | −5.82 | +3.92 | +1.17 | −8.21 | 1, 2, 3, 5 |
| K2 full call (q = 2) | −1.78 | −86% | −17.3% | −10.3 | −6.2 | +11.6 | −9.55 | −7.01 | +1.46 | −1.01 | −11.89 | 1, 2, 3, 4, 5 |
| 1x (R1) | +1.62 | −57% | −13.8% | −5.5 | −1.8 | +11.0 | −4.42 | −1.60 | +3.67 | +2.05 | −1.69 | – |
| 1.5x (R2) | +1.97 | −73% | −20.7% | −8.3 | −2.9 | +16.9 | −6.91 | −3.02 | +5.10 | +2.78 | −2.98 | – |
| 2x (P1) | +2.01 | −84% | −27.6% | −11.2 | −4.1 | +23.0 | −9.57 | −4.85 | +6.25 | +3.32 | −4.54 | – |

Pass bars in this window: bear ≥ −8.2, return ≥ +1.97, drawdown no worse than −57%. All worst days are 2021-05-19.

**The puts alone (base costs, per coin sleeve):**

| | puts bought | days held | proceeds ÷ premium | overlay %/mo (K1) | mean DVOL / next-30-day realized, trend on |
|---|---|---|---|---|---|
| BTC | 79 | 1,042 | 0.76 | −2.35 | 57% / 49% |
| ETH | 71 | 1,012 | 0.81 | −2.56 | 72% / 68% |

**Reading:**
- The prediction was too kind. I expected K1 between 1x and 1.5x on return with a drawdown near 1x's. It returned
  less than 1x (+0.18 against +1.62) and its drawdown stayed at 2x's (−83%).
- The puts did what the contract says on the worst day: −27.6% for P1 became −22.5% (K1) and −17.3% (K2). That was
  not where the drawdown came from. P1's −84% is a long run of small losses from trend whipsaws in 2021–22, and no
  single day. A put bought at each entry and sold at each exit adds a premium loss to every whipsaw.
- The puts returned 76–81 cents per dollar of premium. Implied volatility ran 4–8 points above what followed, and
  about 75 purchases per coin each paid the spread twice. The drag was about 2.4%/month for K1, three times my
  estimate, because the trend flips far more often than once per 30-day put.
- In this window the linear line itself is flat: 1x, 1.5x and 2x all made 1.6–2.0%/month, so the only thing extra
  leverage bought was drawdown.
- Outside the verdict (2016–2021-03, premiums from trailing realized volatility × 1.2, no market price behind it):
  K1 +11.27%/mo with a −79% drawdown, against 1x +9.03 / −50% and P1 +15.73 / −79%. Same picture.
- **Bought-option protection is closed for the daily core.** Trials charged: 748. P1 stays the live paper variant.
