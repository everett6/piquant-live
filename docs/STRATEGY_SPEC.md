# PiQuant requirements (from the user's quiz answers, 2026-11-11)

## What the user wants

| Topic | Answer |
|---|---|
| Exchanges | Coinbase, Binance.US, Kraken Pro, plus using all three for price gaps |
| Instruments | Spot on all three, plus futures on Coinbase |
| Profit sources | All four: trend (days to weeks), short trades (seconds to hours), price gaps between exchanges, market making |
| Worst drop the user can sit through | About 25% |
| When the drop limit and the 7%/month target clash | **The 7% target wins** |
| Eventual real capital | $500 to $5,000 |
| Build first | Price gaps across exchanges |
| Autonomy once live | Fully automatic, with alerts |
| Coins | BTC, ETH, SOL |
| Price-gap style | Leader-follower signal: trade the slower exchange when the leader moves |
| Follower exchange | Whichever lags most, measured from recorded data |
| News | Trade the news, but only in the direction a pre-registered test supports |
| Alerts | Email: a daily summary plus urgent alerts (kill switch, crash, dead feed) |
| Go-live bar | One month of paper profit (user's choice) |

## Tensions to keep visible
- **7% vs the 25% drop limit.** In all backtests, ~7%/month has come only with drops of about −73% to −84%.
  The user chose 7% over the limit. Live sizing must still show the expected drawdown before any real money.
- **One month of paper profit.** At these trade counts, one month is mostly noise. Rules still apply:
  - Every strategy must pass a pre-registered test first (CLAUDE.md hard rule).
  - Going live is the user's decision, outside this tool. The project stays paper-only.
- **Capital and contract size.** $500–5,000 at Coinbase futures: one BTC nano contract is about $740 of BTC.
  Spot fees at low volume are 0.25–1.2% per trade. Strategies must be tested with these costs.

## Build order
1. **Record Binance.US and Kraken trades and top-10 books, plus SOL on all three venues, next to Coinbase.**
   Public data, no keys. Status: in progress 2026-11-11.
2. **Around 2026-12-09:** pre-register and run two tests:
   - **Lead-lag:** measure which venue lags the others, and the gross edge net of each venue's fees.
   - **Order book (Coinbase):** the existing NEXT_TESTS plan.
3. **News test on 1m candles:** pre-register "follow" vs "fade" around CPI/FOMC/NFP releases since 2016 (the
   release calendar is public). Build only what passes.
4. **Trend core in the engine:**
   - route BTC+ETH(+SOL) trend orders through the order simulator, with contract sizes, real funding and fees;
   - make the expected drawdown visible.
5. **Alerts by email:** a daily summary plus urgent alerts, from inside the existing Pi process (no new
   timers). SMTP credentials go in an env file, never in git.
6. **Exchange connectors for order placement (paper first):**
   - Binance.US and Kraken: spot.
   - Coinbase: spot and futures.
   - No real orders without the user's explicit go-ahead.
