# These .pqbin files are synthetic placeholders

Every `.pqbin` in this folder was produced by the generator in `cmd/piquant/backtest_ticks.go`: about 41,000
made-up ticks covering 5.5 minutes per "month". They exist so the tick replay code and its tests have something to
read. They are **not market data** and must not be used for research, tail estimates or strategy tests.

Real ticks:
- `piquant import-ticks --symbol BTCUSDT --from 2024-01 --to 2024-03` downloads real Binance aggTrades and writes
  partitions to `~/piquant-collect/pqbin/` (about 1.7 GB per BTC month, so they stay out of git). Real partitions
  carry the byte `R` at header offset 25; `backtest.PQBinIsReal` checks it.
- Binance does not publish spot best-bid/ask (`bookTicker`) archives. The futures ones are about 7 GB per month
  zipped and are not imported.
- Live Coinbase, Kraken and Binance.US trades and 1-second books are recorded in `~/piquant-collect/ticks` and
  `book` since 2026-11-11. PREREG17 and PREREG18 are registered on that data and run on or after 2026-12-09.
