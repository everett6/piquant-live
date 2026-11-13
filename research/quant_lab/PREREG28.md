# Pre-registration 28: a capped-loss variance harvest, a daily ladder of sold 30-day iron butterflies (committed before `run_round28.py` runs)

Approved by the owner on 2026-11-13. Only synthetic tests had run when this was committed.

## Why
The owner asked for a new algorithm, not another wrapper on P1's trend rule: a return stream that does not depend
on price direction, raises the portfolio's Sharpe ratio above P1's 1.49, and has a bounded loss.

**What a daily futures position cannot do.** `lab.position_returns` earns `p_t × r_{t+1}`. The tail-risk study found
that the sign of `r_{t+1}` cannot be forecast (autocorrelation −0.03) while its size can (0.20–0.26). A forecast of
size alone gives a linear position nothing: `E[p_t·r_{t+1}] = p_t·E[r_{t+1}]` whatever the variance is. Only a payoff
that depends on `r²` can earn from volatility, and that needs options.

**What the repo's own data shows.** In PREREG26 and 27, option buyers got back 53–81 cents per dollar of premium on
Deribit DVOL prices. Implied volatility ran above the volatility that followed (BTC 60% against 51%, ETH 74% against
69%). The seller's side of those trades is the one premium measured here that has not been tested.

**Why it fits beside P1.** A trend rule behaves like a bought straddle: it wins on big moves and bleeds in sideways
markets, and PREREG26 showed P1's −84% drawdown is that bleed. A sold straddle is the opposite. Held together,
the two cancel on direction and keep the gap between implied and realized volatility.

## Limits stated to the owner before approval
- **This is the other side of two tests that just failed, on the same data.** I already know buyers lost. A pass is
  close to expected and proves little. The trial counter cannot correct for that. The test that would mean something
  is a forward paper record on live quotes.
- **Selling volatility loses in crashes.** That is the opposite of the bear-market request. The wings cap the loss by
  contract, but the capped loss arrives in the worst months.
- **It cannot go through `lab.position_returns`.** The option legs are not a position times a return. Decisions are
  still made at close t and earn from day t+1. P1's leg uses `lab` unchanged.
- **Premiums are modelled from DVOL**, with no real quotes and no skew. Real puts below the money cost more than the
  model says, which makes the bought put wing dearer than modelled. The stress case is there for this.
- **A US retail account** can hold this shape only as a defined-risk spread (spot-ETF or CME options). Paper only.
- **Only 2021-04 onward can be tested.** Before that there is no implied volatility, and a stand-in built from past
  realized volatility would hand the seller a profit by construction. Periods D and C are not tested.

## Mathematics
For a tranche opened at close i: `S = S_i`, `σ = DVOL_i`, `τ = 30/365`, `v = σ√τ`.

    sell 1 put and 1 call at K  = S
    buy  1 put  at K_d = S·exp(−2v)
    buy  1 call at K_u = S·exp(+2v)

    payoff owed at expiry per coin  L(S_T) = min(|S_T − K|, wing),  wing = K_u − K above, K − K_d below
    credit received per coin        c = 2·P(K; σ(1−m)) − P(K_d; σ(1+m)) − C(K_u; σ(1+m)) − fees
    profit per coin                 c − L(S_T)        ≥ −(K_u − K − c)   whatever the price does

`P` is the Black–Scholes put on the futures price and `C = P + S − K`. To first order `c ≈ 0.78·v·S` and the profit is
`c − |S_T − K|`: the tranche earns when the 30-day move is smaller than the market priced.

- **Wings at two implied standard deviations:** they bound the loss while giving away little of the credit (each wing
  costs about 1% of the body's premium in the model).
- **Size by worst case, not by notional.** Each tranche is sized so its maximum loss is `f/30` of equity:

      n_i = (f/30) / [(K_u − K − c)/S]      (notional per unit of equity)

  so the 30 open tranches can lose at most `f` between them, measured from the day each was opened. One day's
  mark can be larger than `f` by the gains already booked on those tranches (in the synthetic −30% test: −27%). `f = 0.25`, the owner's stated drawdown limit
  (`piquant-owner-requirements`: 25%). It is not searched.
- **Ladder:** one tranche every day, held to expiry, never closed early. No tranche pays the spread twice and the
  result does not depend on the roll day.

Daily return, with `V_i(t)` the model value of the butterfly the sleeve is short:

    h_t = −Σ_{a=1..30} n_{t−a}·[V_{t−a}(t) − V_{t−a}(t−1)] / S_{t−a}  −  n_t·[V_t(t) − c_t] / S_t

Portfolio = P1 (`lab.position_returns`, unchanged) + `h_t`. The sleeve's collateral is its maximum loss, 25% of
equity; P1's 2x futures margin is assumed to fit in the rest.

## Data (already on disk; nothing is downloaded)
- Deribit DVOL daily closes, BTC and ETH, 2021-03-24 on (`~/piquant-data/dvol`).
- Prices and P1 as PREREG13/23–27. Equal halves on BTC and ETH.
- Window for the verdict: 2021-04-01 to the run date. Live Pi tick data is not used.

## Rules (1 fixed trial, 749 + 1 = 750)
At every daily close with a DVOL value, for each coin, open one tranche as above and hold it to expiry. Mark daily at
the day's DVOL. No other decision exists.

Costs are PREREG26/27's, unchanged and now working against a seller: base `m = 0.10` (sell at 0.9×DVOL, buy at
1.1×DVOL), fee min(3 bps of spot, 12.5% of premium) on each of 4 legs; stress `m = 0.25`, fee min(6 bps, 25%), and
1 bp/h futures funding on P1.

30 days, the three strikes, `f = 0.25`, daily tranches and the costs are fixed here.

## Pass rule (window 2021-04 on; P1 rebuilt in the same run)
1. **It earns:** the sleeve alone has a geometric return > 0.
2. **It is a different return:** correlation of its monthly returns with P1's is between −1 and +0.30.
3. **It raises the Sharpe ratio:** portfolio Sharpe ≥ P1's + 0.20.
4. **Drawdown:** portfolio max drawdown no worse than P1's.
5. **Recent data:** the sleeve alone is > 0 in period A (2025-07 on).
6. **Stress:** the sleeve alone is > 0 at stress costs.

**Also reported:** the sleeve's Sharpe, max drawdown, worst day and worst month; its mean in bear, flat and bull
months; periods B, discovery and A; credit received and paid out; each coin separately; the Deflated Sharpe Ratio at
750 trials; and the same portfolio with P1 at 1x and 1.5x.

**If it passes:** a paper ledger with live Deribit quotes, paper only, and the pass is treated as a reason to start
that record, not as proof. **If it fails:** selling options is closed too, and options are closed for this repo.

## Run
Once, after this file is pushed and the synthetic tests pass.

## Prediction (written now)
- The sleeve earns on BTC and about nothing on ETH, whose implied–realized gap (5 points of 74) is smaller than the
  10% spread. Combined: +0.3 to +1.0%/month at base costs.
- Check 2 passes (correlation with P1 near zero or negative). Check 3 is likely. Check 4 is likely.
- Check 6 fails: a 25% spread on volatility is larger than the edge. So I expect a FAIL on stress alone.
- Its worst months are the crash months of 2021-05 and 2022, each bounded by the wings.
