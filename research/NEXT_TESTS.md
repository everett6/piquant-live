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
- **Pre-registered already:** PREREG17 (maker entries with the owner's pre-fill defense, confluence and
  breakeven ladder; 3 trials, 726 → 729). Run it once on or after 2026-12-09.
- **Also pre-registered:** PREREG18 (lead-lag Coinbase→Kraken and Kraken→Coinbase, and 60 s trade-flow
  momentum; 3 trials, 729 → 732). Same window, run with PREREG17. Its evaluator still needs writing (synthetic
  tests only before the run).
- **Check first:** `trade_id_gaps` and reconnects in ticks_status.json. Exclude gap periods.

## Live state as of 2026-11-11
- **Pi:** runs c23fe4a with `--mft-entries=false` (drop-in /etc/systemd/system/piquant.service.d/mft-off.conf).
  The in-process 2x/1x trend core ledger runs at ~/piquant-data/trend.
- **Rollback:** /usr/local/bin/piquant.prev-b5fe076.
- **Re-enable MFT:** remove the drop-in, then run `daemon-reload` and restart.

## Paper $50 profile (added 2026-11-11, user request; not a pre-registered strategy)
- **Ledger variant:** `PAPER50_btc_eth_0p2x`. It uses the P1 rule on spot at 20% of NAV in total ($5 per
  sleeve).
- **Costs:** 1.2% retail taker fee + 5 bps slippage.
- **Limits:** a 2% daily loss stop (flat for one close) and a 5% drawdown kill switch. Restart with
  `piquant trend --dir ~/piquant-data/trend --reset PAPER50_btc_eth_0p2x`.
- **Historical diagnostic (38 start dates, 2016–2025):** the kill switch fired from every start, after a median
  of 111 days, with $47–78 left. The 1.2% fee on small orders costs more than the trend earns at 0.2x.

## Recording check, 2026-11-12 (gaps only; no signals computed)
- **Script:** `research/quant_lab/dq_ticks.py YYYY-MM-DD`. Rerun it on every day before the Dec 9 run.
- **All nine feeds recorded on 11-12** (Coinbase, Kraken and Binance.US; BTC, ETH and SOL). Book snapshots
  arrive every 1.00 s (median), with no crossed or empty books and no duplicate or out-of-order trades.
- **One outage:** 07:00:33–10:04:04 UTC (3 h 3 min).
  - The home internet went down while the WiFi stayed associated (NetworkManager CONNECTED_SITE; DNS failed).
  - The collector retried every 64 s and resumed on its own, so there is no code fault.
  - The PREREG17/18 rules already exclude these minutes: the book gaps are over 5 s, and the 16 failed clock
    checks fall in the same window.
- **No clock checks before 02:51 UTC** (collector restarts for deploys), so 00:00–02:51 on 11-12 is excluded by
  the clock rule. Clock offsets otherwise peak at 30 ms against the 250 ms limit.
- **Trade IDs:** Kraken's run in sequence, so the trade_id gap exclusion works for Kraken as well as Coinbase.
  Outside the outage there are 5–9 short gaps per product per day (1–93 IDs each); each excludes a minute or two.
- **About 6 of the first day's 24 hours are excluded** (roughly 1% of the 27-day window). This does not threaten
  the 200-trade minimums unless outages recur.
