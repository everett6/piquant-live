# Pre-registration 25: long BTC / short an altcoin basket in BTC downtrends (committed before any round-25 code runs or any altcoin data is downloaded)

## Why
The owner wants money made in bear markets. P1 (BTC+ETH, 2x while close > SMA50) sits in cash during crypto
downtrends and still loses −8.0%/month in bear months.

A common quant bear-market trade is to buy BTC and short a basket of altcoins:
- in downturns, money leaves high-beta altcoins faster than BTC;
- the pair profits from that gap with little net exposure to market direction.

It has not been tested here. PREREG20 paired BTC with ETH only, as a beta-neutral book, and failed. US retail can
trade the short leg with Coinbase Derivatives (US) altcoin futures.

## Data (free, Coinbase public API; downloaded once to `~/piquant-data/alts/`, not committed)
- Daily candles (UTC days) for these USD pairs, from each pair's Coinbase listing to the run date. The list is fixed
  now: all are trading on Coinbase today and have no stablecoin or wrapped tokens.
  - LTC, BCH, ETC, XLM, LINK, XRP, ADA, DOGE, SOL, DOT, AVAX, UNI, SHIB, HBAR.
- BTC and ETH: P1 exactly as PREREG13/23/24 (`lab.load_candles` → daily).
- **Survivorship:** only coins still listed today are included. Delisted coins that collapsed are missing. That makes
  shorting altcoins look **worse** than it really was, so the bias works against a pass.
- XRP was suspended on Coinbase from 2021-01 to 2023-07. Days without a price simply leave it out of the basket.

## Rules (2 fixed trials, 744 + 2 = 746)
**Basket at close t:**
- every listed alt with a close on day t and at least 60 earlier daily closes;
- equal weights summing to 1;
- held over day t+1 (`lab.position_returns` timing: decided at close t, earns day t+1's return).
- A coin missing on day t+1 earns 0 that day. The basket rebalances daily.
- Turnover per asset is |w_t − w_{t−1}|; drift between rebalances is ignored, a stated simplification.

**Pair overlay:** long 1.0x BTC, short 1.0x basket (gross 2x, net 0), added to P1's return. It is margin-funded
futures, so it needs no cash.
- **B1, bear-gated:** the pair is on only when BTC's close at t < BTC's SMA50, i.e. when P1's BTC sleeve is flat.
- **B2, always on:** the pair is on every day, as the control. It shows whether the gap is a downtrend effect.
- No pair before the basket has at least 1 coin. LTC is listed from 2016-08, so the 2016–17 basket is LTC alone.
- Costs: us-futures per asset leg, 6 + 1 bps per unit of turnover and funding 0.1 bp/h on |position|. Stress
  (funding 1 bp/h) as before.

## Pass rule (each variant on its own, against P1 rebuilt in the same run; the same five checks as PREREG23/24)
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points (P1: −8.0, so ≥ −5.0).
2. **Target kept:** full 2016–2026 geometric return ≥ 7.0%/month.
3. **Drawdown:** full max drawdown no worse than P1's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** full geometric return at stress costs > 0.

**Also reported:**
- every period, flat and bull months, and 2022;
- the pair alone: full geometric return, Sharpe, mean in bear/flat/bull months, worst day (squeeze risk), and days on;
- basket size over time;
- each coin's mean contribution.

**If one passes:** it joins the trend paper ledger beside P1 (paper only). **If both fail:** BTC-vs-alts is closed for
the daily core.

## Run
Once, after the synthetic tests pass.

## Prediction (written now)
- Alts lose more than BTC in most downtrends, so B1's pair earns in bear months: about +1.5 to +3 points.
- The short leg is exposed to sudden alt squeezes. B1 is borderline on check 1 and probably just short of it.
- B2 loses in alt seasons (2017, early 2021) and fails.

## Result (run once, 2026-11-13): both FAIL. The pair LOST money in bear months.

Data: Coinbase daily closes for all 14 coins (first closes: LTC 2016-08, BCH 2017-12, ETC 2018-08, XRP 2019-02 with
the 2021–23 suspension gap, XLM 2019-03, LINK 2019-06, UNI 2020-09, ADA 2021-03, DOGE/SOL/DOT 2021-06, SHIB/AVAX
2021-09, HBAR 2022-10), to 2026-11-12. Average basket size: 1 coin (2017), 2 (2018), 5 (2019), 6 (2020), 8 (2021),
12–14 (2022 on).

| | full %/mo | max DD | bear | flat | bull | 2022 | D | C | B | discovery | A | stress | checks failed |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| B1 pair in BTC downtrend | +6.58 | −89% | **−13.2** | −2.0 | +37.1 | −9.02 | +19.99 | +4.80 | +0.14 | +5.92 | +1.95 | −5.86 | 1, 2, 3, 5 |
| B2 pair always on | +3.56 | −95% | −11.2 | −4.2 | +29.4 | −10.09 | +7.08 | +6.43 | −6.51 | +5.81 | +2.29 | −14.58 | 1, 2, 3, 5 |
| P1 (comparator, reproduced exactly) | +8.50 | −84% | −8.0 | −3.9 | +38.2 | −9.57 | +27.99 | +6.24 | +0.77 | +6.25 | +3.32 | +1.19 | – |

**The pair alone:**

| | full %/mo | Sharpe | max DD | bear / flat / bull mean %/mo | worst day | corr. with P1 | days on |
|---|---|---|---|---|---|---|---|
| B1 | −1.85 | −0.18 | −93% | −2.53 / +1.82 / −0.56 | −83.6% (2017-03-30) | −0.21 | 1631 |
| B2 | −5.38 | −0.46 | −100% | −0.40 / +0.07 / −4.92 | −83.6% (2017-03-30) | −0.37 | 3678 |

**Reading:**
- The prediction was wrong in direction. Shorting altcoins against BTC in BTC downtrends did not earn in bear months:
  −2.5%/month on its own, which made P1's bear months 5 points worse.
- **Checked after the result (descriptive only; nothing re-run or changed):**
  - The −83.6% day is real. LTC, then the only coin in the basket, rose from $4.30 to $7.89 on 2017-03-30 (the SegWit
    rally).
  - The bear-month loss is not just the thin early basket. From 2019 it is −1.6%/month (31 bear months); from 2022,
    with 12–14 coins, it is −2.1% (18 bear months).
  - By year, the pair earned in 2022 (+13.8%) and 2023 (+23.8%) but lost in 2017–2021 and 2024–2026. Altcoins
    falling faster than BTC was a 2022–23 effect, not a reliable one. In other downtrends, short squeezes and
    rotations into altcoins cost more than the gap paid.
- Survivorship bias (only coins listed today) should have helped the short leg look worse. Even so, the pair's losses
  are too large and too steady for a missing-coin effect to reverse them.
- **Long BTC / short altcoins is closed for the daily core.** Trials charged: 746. P1 stays the live paper variant.
