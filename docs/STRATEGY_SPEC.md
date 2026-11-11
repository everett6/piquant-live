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

## Technical answers (second quiz, 2026-11-11)

| Topic | Answer |
|---|---|
| Where it runs | Pi now; rent a VPS near the exchanges only if the lead-lag data shows the Pi is too slow |
| Reaction speed | Set from measured gap lifetimes (lead-lag test first) |
| Order style | Mixed by exchange: maker where fees are ~0, taker where speed matters |
| Exchange outage | Keep trading the other venues; the gap strategy pauses while a venue is missing |
| API keys (at go-live) | Trade-only (no withdrawal), with an IP allow-list |
| Kill switch | Flatten, email, stay off until the owner restarts it by hand |
| Kill-switch level | **40% drop from peak** (owner's choice over 25%). Pi set to `--max-drawdown 40` on 2026-11-11 (was 8); the pre-trade guard and leverage taper use the same limit |
| Go-live bar | A pre-registered pass, plus 1 month of paper profit, plus drop under 40%, plus ≥200 trades |
| Email | Gmail app password, kept in an env file on the Pi (owner sets it up; never in git) |
| Updates | Claude deploys with the owner's OK and keeps the previous binary for rollback |
| Tick data retention | Keep everything; revisit when free disk nears the 20 GB guard |
| Backups | Nightly copy of Pi state to the desktop, plus the ledgers to a private GitHub repo |
| Tax records | Yearly CSV of every fill (date, price, fee, cost basis) |
| Clock sync | Halt gap trades if the clock is off by more than 250 ms |
| Rate limits | Back off, keep positions, email if it repeats |
| Old engine code | Quarantine market making, Bybit and the global region behind an off switch; keep the tests |

## Problems found in the code (subagent review, 2026-11-11)
- **The auto-recalibrator can never find data.** It looks for monthly `*.pqbin` files
  (internal/backtest/auto_recalibrate.go:38), but the collector writes daily `*.jsonl.gz` files. It logs
  "no tick partitions found" every hour.
- **There is no email sender anywhere.** Alerts must be built.
- **Futures paper fills use the Coinbase spot feed as a stand-in for the futures book.** The review said
  Binance.US, from a stale comment (fixed). Basis and depth still differ from real nano futures fills.
- **No Kraken order connector exists.** Only market data has been recorded, since 2026-11-11.
- ~~The engine has no "needs a human restart" lock.~~ Fixed 2026-11-11: any circuit trip writes
  `paper/KILL_LOCK`; the engine starts halted while it exists. Human restart: stop the service, run
  `piquant unlock --data ~/piquant-data` (resets the drawdown peak), start it. `piquant unlock --status` shows it.
- Nightly backup: the desktop collector (`--backup-host everett@raspberrypi.local`) pulls a tar.gz of the Pi's
  paper/trend/config/reports/macro into ~/piquant-backups/pi after 03:00 local, keeping 30. The ledger push to a
  private GitHub repo (`--backup-git-dir`) stays off until the owner creates that repo.
- **Disk:** at 50–100 MB/day, 110 GB free lasts about 3–5 years, minus other desktop use.
