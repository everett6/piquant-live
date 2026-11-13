# Pre-registration 24: a multi-market trend overlay ("crisis alpha") on the BTC+ETH core (committed before any round-24 code runs or any round-24 data is downloaded)

## Why
The owner asked how quant firms make large returns, and to apply what fits this setup. The main families are:
- market making, which earns the spread and rebates at scale;
- statistical arbitrage;
- carry;
- trend following across many markets, as practised by the big managed-futures firms.

Most of these are already tested here or don't fit a US retail account:
- market making and short-horizon flow: PREREG8/17/18/21/22;
- carry: PREREG19, where C1 passed and now runs on a paper ledger;
- pairs and beta-neutral: PREREG20;
- selling volatility loses in crashes, the opposite of what the owner asked for.

What the trend firms do and this repo has not:
- **Diversify the trend rule across unrelated markets.** Hurst, Ooi and Pedersen (AQR, "A Century of Evidence on
  Trend-Following") find 12-month time-series momentum across stocks, bonds, commodities and currencies has paid
  since 1880, with low correlation to other assets. It paid most in long bear markets, for example 2008 and 2022,
  when it is short stocks and bonds.
- Our core P1 is trend on two coins that crash together. It sits in cash in crypto bear markets (−8.0%/month in
  bear months). The idea here: in those months, other markets' trends can still earn.

US retail can trade these markets as CME micro futures:
- MES (S&P 500);
- 10-year Treasury futures (ZN, or the micro 10Y);
- MGC (gold);
- the dollar index futures (DX, or 6E inverted).

ETFs stand in for their prices in the test.

## Data (free, no key; downloaded once to `~/piquant-data/macro/`, not committed)
- **ETF adjusted daily closes, 2014-01-01 to the run date, from Yahoo Finance's chart API:**
  - SPY (US stocks);
  - TLT (long Treasuries);
  - GLD (gold);
  - UUP (US dollar).

  Adjusted closes include dividends. Stooq is the fallback if Yahoo fails, with the same symbols. A source switch is
  recorded, and nothing else changes.
- **3-month T-bill rate:** FRED `DTB3`, daily, in percent per year.
- **Crypto:** P1 exactly as PREREG13/23 (`lab.load_candles` Coinbase BTC-USD/ETH-USD → daily, `strategies5`).

## Rules (2 fixed trials, 742 + 2 = 744)
**Overlay signal (textbook time-series momentum, Moskowitz–Ooi–Pedersen 2012; nothing fitted).** For each of the 4
markets, at its close t:
- s = sign(adjclose_t / adjclose_{t−252} − 1);
- σ = std of the last 60 daily returns × √252;
- w = s × min(4, target_t / σ). w is 0 until 252 returns exist;
- w is held over the next trading day, earning the **excess** return: ETF return − T-bill rate (previous day's DTB3 / 100 / 252). Futures earn the excess return, because they need no cash;
- cost: 2 bps per unit of turnover (micro-futures fee + 1 tick), stress 5 bps.

The overlay's day return is added to the crypto day with the same UTC date. US closes are at 20:00–21:00 UTC, inside
that crypto day. Weekends and holidays earn 0. The overlay is margin-funded, so the portfolio return is P1's plus the
overlay's.

- **M1, always-on overlay:** target_t = 10% per market, every day.
- **M2, idle-capital overlay:** the overlay is used only when crypto is flat.
  - target_t = 40% × k_t. 40% is the per-market target in Moskowitz–Ooi–Pedersen.
  - k_t = 0.5 × [P1's BTC sleeve flat] + 0.5 × [P1's ETH sleeve flat or before ETH data]. Both are read from P1's
    positions decided at the crypto close of day t−1, the last crypto close before the US close on day t.
  - So both coins flat gives 40% per market, one flat gives 20%, and both long gives 0.

252, 60, 4, 10%, 40%, 2 bps and the four markets are fixed here, before any data is seen, and are not searched.

## Pass rule (each variant on its own, against P1 rebuilt in the same run; same five checks as PREREG23)
1. **Bad markets:** mean bear-month return ≥ P1's + 3.0 points (P1: −8.0, so ≥ −5.0). Bear means BTC buy-and-hold
   month < −5%.
2. **Target kept:** full 2016–2026 geometric return ≥ 7.0%/month.
3. **Drawdown:** full max drawdown no worse than P1's.
4. **Recent data:** period A (2025-07 on) geometric return > 0.
5. **Stress:** full geometric return at stress costs (crypto stress funding and 5 bps overlay) > 0.

**Also reported:**
- every period, flat and bull months, losing months, worst month and 2022;
- the overlay alone: full geometric return, Sharpe, its mean in bear months, and its correlation with P1's monthly
  returns;
- each market's contribution.

**If one passes:** it joins the trend paper ledger beside P1 (paper only). The overlay legs are paper-traded on ETF
prices, and P1 stays live until the variant also beats it forward. **If both fail:** the multi-market trend overlay
is closed for the daily core.

## Run
Once, after the synthetic tests pass and the data is downloaded with the owner's OK.

## Prediction (written now)
- Across 2016–2026 the overlay alone earns a little: +0.2 to +0.6%/month, Sharpe 0.3–0.7. Trend funds had weak
  years from 2016 to 2019 and a strong 2022.
- M1 is too small to move crypto's numbers: bear months improve by about 0.5 points. It fails check 1 and passes the
  others.
- M2 helps bear months more, about 1–2.5 points, mostly in 2022. It most likely still misses the +3.0 bar.
- I expect both to fail check 1, with M2 the closer of the two.
