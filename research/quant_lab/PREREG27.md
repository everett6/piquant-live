# Pre-registration 27: an always-on ladder of put spreads as a bear-market sleeve beside P1 (committed before `run_round27.py` runs)

Approved by the owner on 2026-11-13, including reopening the bought-option family that PREREG26 closed. Only
synthetic tests had run when this was committed.

## Why
The owner wants profit in bear months (BTC month < −5%) without squeeze risk and without backward-looking triggers.

Every earlier bear-market idea was one of three things:
- **a short position** (PREREG7, 9, 25): its loss in a squeeze has no limit (−83.6% in one day in PREREG25);
- **a trigger on past prices** (PREREG14, 23): it fires after the fall and misses the rebound;
- **another asset's return** (PREREG24, carry in PREREG19): there was none to add in bear months.

PREREG26 bought puts, but only while the trend was on, and sold them at every trend exit. The puts were gone in the
months when the bear market continued, and each of about 75 round trips per coin paid the spread twice.

What is left is a position whose payoff is a fixed function of the month's fall, held all the time, with a loss
capped by contract: a put spread. A squeeze costs the premium and no more. Nothing is triggered by past prices.

## Limits stated to the owner before approval
- **This reopens a family I closed.** PREREG26 ended with "bought-option protection is closed for the daily core".
  This is the same instrument family with a different rule (always on, held to expiry, not tied to the trend). The
  project rule is "no variants of failed tests". I think the mechanism is different enough to be a new test, but it
  is the owner's call.
- **The design was made after seeing PREREG26's numbers** on the same DVOL data: 23 of the 67 months since 2021-04
  are bear months, and implied volatility ran 4–8 points above realized. A window that bear-heavy flatters any
  put-buyer. A pass here is weaker evidence than a pass on unseen data, and the trial counter cannot correct for it.
- **Before 2021-03 there is no implied volatility.** Those years use a stand-in (below). Checks on the full sample
  rest partly on a model.
- **Premiums are modelled, not quoted.** Same limit as PREREG26.

## Mathematics
For a tranche opened at close i, with `S = S_i`, `σ = IV_i`, `τ = 30/365` and `v = σ√τ` (the one-standard-deviation
30-day move the market is pricing):

    long put  K1 = S            (at the money)
    short put K2 = S·exp(−v)    (one implied standard deviation below)

    payoff at expiry per coin = max(K1 − S_T, 0) − max(K2 − S_T, 0)   ∈ [0, K1 − K2]
    premium per coin          = P(S, K1, σ(1+m), τ) − P(S, K2, σ(1−m), τ) + fees

`P` is the Black–Scholes put on the futures price (`strategies26.bs_put`). To first order the premium is about
`0.32·v·S`: 5.4% of spot at 60% volatility. The most it can pay is `(1 − e^(−v))·S`, about 16%.

- **Loss cap:** a tranche can lose its premium and nothing else, whatever the price does.
- **Why the lower strike is sold:** a bear month is usually a fall of 5–25%, which is inside the spread. The far
  tail is where option sellers charge most. The model prices both strikes at the same volatility, so it under-credits
  the sold put when the real skew is steeper. That works against a pass.
- **Ladder:** one tranche is opened every day and held to expiry, each for 1/30 of the size. No tranche is ever sold
  early, so each pays the spread once. The result does not depend on which day of the month the roll falls.
- **Size:** `q = 0.5` units of notional per unit of equity in total (so `q/30` per tranche). This is set by
  arithmetic from the pass rule, before any result: a full bear month pays about +10 points per unit after premium,
  so +3 points needs `q ≥ 0.3`; the expected drag is about 2%/month per unit, so keeping 7%/month needs `q ≤ 0.75`.
  0.5 is the middle. It is not searched.
- **Worst case:** all 30 tranches expire worthless: about `q × 5.4% ≈ 2.7%` of equity per month at 60% volatility.

Daily return of the sleeve, with `V_i(t)` the spread's model value at close t:

    ov_t = Σ_{a=1..30} (q/30)·[V_{t−a}(t) − V_{t−a}(t−1)] / S_{t−a}  −  (q/30)·[premium_t − V_t(t)] / S_t

Portfolio return = P1's return (`lab.position_returns`, unchanged) + `ov_t`.

## Data (all already on disk; nothing new is downloaded)
- **Implied volatility:** Deribit DVOL daily closes for BTC and ETH, 2021-03-24 on (`~/piquant-data/dvol`, fetched for
  PREREG26).
- **Before 2021-03-24:** stand-in `IV = 1.2 × trailing 30-day realized volatility` (`strategies26.realized_vol_iv`).
  1.2 was fixed in PREREG26 before any DVOL was seen; the measured ratio since is 1.16 (BTC) and 1.06 (ETH).
- **Prices and P1:** as PREREG13/23–26. BTC and ETH sleeves in equal halves; each coin's ladder sits on its half.
- Live Pi tick data is not used.

## Rules (1 fixed trial, 748 + 1 = 749)
At every daily close t, for each coin, if an implied volatility exists: open one tranche as above. Hold every tranche
to expiry. Mark daily at the day's implied volatility (last value if a day is missing). No other decision exists.

Costs are PREREG26's, unchanged: base `m = 0.10` on volatility and fee min(3 bps of spot, 12.5% of premium) per leg;
stress `m = 0.25`, fee min(6 bps, 25%), and futures funding 1 bp/h on P1. Daily drift in the option count is ignored
(as in PREREG25/26).

30 days, the two strikes, `q = 0.5`, daily tranches and the costs are fixed here.

## Pass rule (the five overlay checks, against P1 rebuilt in the same run)
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points, on the full sample **and** on 2021-04 onward
   (the months priced from real implied volatility). Both must hold.
2. **Target kept:** full 2016–2026 geometric return ≥ 7.0%/month.
3. **Drawdown:** full max drawdown no worse than P1's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** full geometric return at stress costs > 0.

**Also reported:**
- the sleeve alone: mean return in bear, flat and bull months (full sample and 2021-04 on), geometric return, Sharpe,
  max drawdown, worst day, worst month, correlation with P1's months, premium paid and paid back;
- every period (D, C, B, discovery, A), 2022, and the Deflated Sharpe Ratio at 749 trials;
- the owner's aim of a positive bear month for the whole portfolio is reported, not required.

**If it passes:** it joins the trend paper ledger beside P1, paper only, with the spreads marked from live Deribit
quotes; given the notes above, that forward record is the real test. **If it fails:** always-on put spreads are
closed, and I will not propose another bought-option test on this data.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- The sleeve alone: about +4 to +5 points in bear months at `q = 0.5`, about −2.5 in bull months, about −1 in flat
  months, and about −0.8%/month overall.
- Check 1 passes on both samples. Check 3 passes, because P1's −84% sits in the bear-heavy 2021–22 stretch.
- Check 2 is the risk: P1's +8.50 less the drag lands near +7.5. Check 5 is close to zero either way.
- Chance of a pass: about even. If it passes, most of the credit goes to how bear-heavy 2021–22 was.
