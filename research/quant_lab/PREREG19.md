# Pre-registration 19: funding-rate carry (long spot, short perpetual) on BTC and ETH (committed before any round-19 code runs)

## Why
The owner asked for the one untested idea from the "institutional pillars" note (2026-11-11): market-neutral
funding carry. Perpetual futures usually trade above spot in bull and flat markets. Longs then pay shorts a
funding rate, so holding spot and shorting the same amount of perpetual collects that funding with little price
exposure.

**Literature:** Schmeling, Schrimpf & Todorov (2023), *Crypto Carry* (BIS Working Paper 1087). Carry was large on
average (often over 10% a year) and highly variable. It collapsed or turned negative in sell-offs, and it is
linked to crash risk.

It is not risk-free:
- funding goes negative;
- the short leg needs margin and can be liquidated in a spike;
- the two legs sit at different venues or accounts;
- US retail access to perpetuals is limited (Coinbase's US perp-style futures since 2025).

## Data
- **Funding:** Binance USDT-margined perpetual funding history, BTCUSDT and ETHUSDT (8-hourly). These are the
  public monthly files at data.binance.vision, 2020-01 through the latest complete month.
  - **Proxy note:** this is the deepest free funding history. A US venue's funding differs (USD vs USDT
    margining, caps), so the stress case cuts all funding received by 25%.
- **Prices:** Coinbase daily OHLC (lab.load_candles resampled to 1D), used to mark the legs and for the daily-high
  liquidation check.
- **Window:** 2020-01-01 to the last day of the latest complete funding month.

## Position model (identical for both rules)
- Each coin is half of capital. Within a coin, equity E holds spot worth N and a short perpetual of notional N
  with margin N/2 (2x on the short leg), so N = E / 1.5.
- **Funding:** each 8-hourly payment adds rate × N (positive rate: the short receives; negative: it pays).
- **Price:** the legs offset, so P&L comes only from funding and costs. Spot and perpetual are treated as the same
  price. Basis changes are ignored, because daily basis data is not free; this is a stated simplification.
- **Liquidation:**
  - if a day's high is ≥ 1.5 × the previous close (the last rebalance), the short leg is liquidated;
  - its loss (the whole margin, 0.5N) is offset by the spot leg's gain;
  - the event costs a 1% liquidation fee on N plus reopening the perpetual leg at the close (perp costs below).
- **Rebalance:** daily at the close, back to the target N with the legs restored. The cost is turnover × the costs
  below (turnover = |price change| × N on each leg).
- **Costs per side:** spot 2 bps fee + 1 bp slippage; perpetual 6 bps + 1 bp (us-futures). Opening or closing the
  pair costs both legs.
  - **Stress:** costs doubled, and funding received × 0.75. Funding paid is not reduced.
- **Cash:** idle capital earns 0, which is conservative versus T-bills.

## Rules (2 fixed trials, no search)
- **C1, always on:** hold the carry on both coins at all times.
- **C2, only when funding is high:**
  - hold a coin's carry only while its trailing 7-day mean 8-hour funding rate is ≥ 0.01%, which is about 11% a
    year;
  - otherwise that half is cash;
  - the check runs daily at the close, using funding through that close, and each switch pays the open or close
    cost.

## Pass rule (2 trials: 732 + 2 = 734)
A rule passes only if all of these hold:
1. Full-window net annualised return ≥ 5%, at stress costs. That is above cash, about the T-bill rate.
2. Full-window max drawdown no worse than −10% at stress costs.
3. Net return > 0 in at least 4 of 5 periods: 2020, 2021, 2022, 2023–24, and 2025 to the end.
4. No more than one liquidation event per coin per year on average.

**Also reported:**
- annual return by year;
- share of time funding was negative;
- longest losing stretch;
- monthly return (for comparison with the 7%/month goal);
- the C2 time-in-market.

**If one passes:**
- it becomes a paper ledger beside the trend ledgers;
- live use would need a US venue with perpetuals and a check that its funding matches the proxy;
- carry is a yield sleeve, not a route to 7%/month.

**If none pass:** carry with these rules is closed.

## Prediction (written now)
- **C1:** about 8–12% a year in 2020–21, near 0 or negative in 2022, mid single digits since. Full window about
  6–9% a year at base costs and about 4–6% at stress. Drawdown small (−3 to −6%) unless a liquidation day hits.
  Borderline on check 1.
- **C2:** higher return while in, but less time in. The full-window return is similar or a little lower, and the
  drawdown is smaller.
- About 0.5–0.8% a month at best: a real but modest yield, far from 7% a month.
