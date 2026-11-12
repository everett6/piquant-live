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
| Email | Gmail app password, kept in an env file on the Pi (owner sets it up; never in git). **Sender built 2026-11-11:** reads `~/.config/piquant/alert.env` (`PIQUANT_SMTP_USER`, `PIQUANT_SMTP_PASS`, optional `PIQUANT_ALERT_TO`); without it alerts go to the log only. Pi alerts: kill switch, clock not OK, market feed stalled 5+ min, daily summary after 00:15 UTC. Desktop alerts: backup or ledger push failed, tick venue down 5+ min, stalled trades, new trade-ID gaps, writing paused, clock. One email per alert type per hour. Test with `piquant alert-test` |
| Updates | Claude deploys with the owner's OK and keeps the previous binary for rollback |
| Tick data retention | Keep everything; revisit when free disk nears the 20 GB guard |
| Backups | Nightly copy of Pi state to the desktop, plus the ledgers to a private GitHub repo. **Private repo live 2026-11-11:** `everett6/piquant-ledgers` (clone at ~/piquant-backups/ledgers, collector flag `--backup-git-dir`). **Restore drill passed 2026-11-11:** the engine resumed from the 2026-11-11 archive with the same start time, equity and trend history |
| Tax records | Yearly CSV of every fill (date, price, fee, cost basis). **Trend ledgers done 2026-11-11:** `piquant tax --trend [--year Y] [--export f.csv]` lists each closed position per coin: spot variants on Form 8949 (FIFO, short/long term), futures variants as Section 1256 contracts on Form 6781 (60/40, open positions marked at each year's last close). Fills are at daily closes, sized at $200 per variant unless the variant sets its own capital. Paper record for a tax professional, not advice |
| Clock sync | Halt gap trades if the clock is off by more than 250 ms. **Done 2026-11-11:** the engine and collector check the clock against 3 public time servers (SNTP) every 10 minutes, using the median offset. The engine blocks new entries (exits still run) when the offset is over 250 ms, when no check has succeeded yet, or when the last good check is over 30 minutes old. Every check is logged to `clock_offsets.jsonl` (collector dir, and `<data>/paper` on the Pi), so tick tests can drop bad-clock windows. Flags: `--clock-servers`, `--clock-max-ms` |
| Rate limits | Back off, keep positions, email if it repeats |
| Old engine code | Quarantine market making, Bybit and the global region behind an off switch; keep the tests. **Done 2026-11-11:** `--mft-entries` (maker quoting) and `--auto-recalibrate` default off, and they and `--region global` need `--allow-quarantined`; the Bybit ingestor builds only with `-tags quarantine` (its tests run with that tag) |

## Carry paper ledger (PREREG19 C1, added 2026-11-11)
- **What:** always-on funding carry on BTC and ETH (long spot, short perpetual at 2x on the short leg), paper only.
- **Where:** the desktop collector, in `~/piquant-collect/carry/carry_state.json`. It opened at the 2026-11-11 close.
- **Updates:** once a month. Binance publishes funding only as monthly files, and its live API is blocked in the US.
- **Tracks:** base and stress equity, max drop and liquidations. Print with `piquant carry`; advance with `--once`.
- **Backup:** the nightly ledger push copies it to the private repo under `desktop/`.
- **Limits:** no basis risk is modelled, and Binance funding is a proxy for a US venue.

## Problems found in the code (subagent review, 2026-11-11)
- ~~The auto-recalibrator can never find data.~~ Quarantined 2026-11-11 instead of fixed: it is an unregistered
  in-sample grid search that hot-reloads live parameters, which the pre-registration rules forbid. Now off unless
  `--auto-recalibrate --allow-quarantined`. Original note: It looks for monthly `*.pqbin` files
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
