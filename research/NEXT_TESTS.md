# Scheduled research

## 2026-12-09: order-flow test on recorded tick data
- **Data:** tick and book recording started 2026-11-11 (commit ec03842). Files are in
  `~/piquant-collect/ticks/<product>/` and `~/piquant-collect/book/<product>/`. Status is in
  `ticks_status.json`.
- **What to test:** the filters that 1m OHLC can't test (PREREG9 deferred them):
  - order-book imbalance (top-10);
  - VPIN / trade-flow toxicity;
  - Coinbase-vs-Binance lead-lag;
  - signed trade-flow momentum at 30 s–15 min horizons.
- **Before any code runs:** pre-register them, splitting the recorded weeks into a discovery half and a holdout
  half, with taker costs of 14 bps round trip and a maker model based on queue position from the book.
- **Check first:** `trade_id_gaps` and reconnects in ticks_status.json. Exclude gap periods.

## Live state as of 2026-11-11
- **Pi:** runs c23fe4a with `--mft-entries=false` (drop-in /etc/systemd/system/piquant.service.d/mft-off.conf).
  The in-process 2x/1x trend core ledger runs at ~/piquant-data/trend.
- **Rollback:** /usr/local/bin/piquant.prev-b5fe076.
- **Re-enable MFT:** remove the drop-in, then run `daemon-reload` and restart.
