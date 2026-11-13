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

## PREREG22 done (2026-11-13): PREREG18's rules on Tardis free days 2023–2026 all FAIL
- L1 −78 bps net (gross +2.5), L2 −14.4, F1 −14.1 on the holdout. Coinbase leads Kraken and Binance.US by 4–5 bps
  within 30 s; nothing leads Coinbase. Full result in quant_lab/PREREG22.md. Trials: 740.

## Live state as of 2026-11-11
- **Pi:** runs c23fe4a with `--mft-entries=false` (drop-in /etc/systemd/system/piquant.service.d/mft-off.conf).
  The in-process 2x/1x trend core ledger runs at ~/piquant-data/trend.
- **Rollback:** /usr/local/bin/piquant.prev-b5fe076.
- **Re-enable MFT:** remove the drop-in, then run `daemon-reload` and restart.

## Recorder moved to the Pi (2026-11-13 00:04 UTC)
- **Where:** the Pi records ticks and top-10 books (`piquant-collect.service`, system unit, copy in
  `deploy/piquant-collect-pi.service`) into `~/piquant-collect` on the Pi. The Pi is on wired Ethernet (eth0).
- **Desktop:** still runs its collector for news, candles, the carry ledger and the nightly Pi backup, with
  `--ticks=false` (drop-in `~/.config/systemd/user/piquant-collect.service.d/ticks-off.conf`; delete it and
  restart to record ticks there again).
- **History moved:** 2026-11-11 and -12 copied unchanged (36 files, md5 identical). 2026-11-13 merged in time order
  by `scripts/merge_desktop_ticks.py` (trades deduplicated by ID). The desktop copy stays in
  `~/piquant-collect-desktop-import` on the Pi and in the desktop's `~/piquant-collect`.
- **Research runs on the Pi:** repo copy in `~/PiQuant/piquant-live` (rsync from the desktop, no .git), Python env
  `~/piquant-research-env` (numpy, pandas, pytest). PREREG17/18 read `~/piquant-collect` there unchanged. Run heavy
  jobs with `nice -n 19 ionice -c3` so the paper bot keeps priority.
- **Alerts:** the Pi recorder sends the "venue down" emails now, but only once `~/.config/piquant/alert.env` exists
  on the Pi as well.
- **Second copy:** the desktop collector rsyncs the Pi's `ticks/`, `book/`, `archive/` and clock checks into
  `~/piquant-backups/pi-collect` once a day after 03:00 (no delete; alert "Pi tick-data mirror failed" on errors).
  Before this, the Pi's SD card was the only copy of the Dec 9 test data.
- **Daily checks:** `scripts/pipeline_status.sh` (one screen: Pi services, recorder age, venues, today's book gaps,
  clock, alert.env, desktop mirror). Before any Pi run: `scripts/sync_pi.sh` (committed code only; writes
  `.synced_commit` on the Pi). Both take `PI=everett@<ip>` if `raspberrypi.local` does not resolve.
- **Monthly archive:** `scripts/collect_archive.sh pack YYYY-MM` packs a finished month into
  `archive/YYYY-MM.tar.zst` (zstd -19 --long; ~25% smaller than the daily gzip files, checked before the
  originals are deleted). `restore` unpacks it for a test and `drop` removes the unpacked copy again.
  `~/piquant-collect/KEEP_MONTHS` (2026-11, 2026-12) blocks packing months that PREREG17/18 still read.

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
  - The recorder is the desktop (piquant-collect.service), not the Pi. On 11-12 it was on WiFi (wlp7s0) with
    the Ethernet port (enp6s0) unplugged, and it had no `~/.config/piquant/alert.env`, so its "tick venue down
    5+ min" alert went to the log only. Owner to-do: plug in Ethernet and create alert.env on the desktop.
  - `scripts/wifi-watchdog.sh` counted a router ping as "online", so it would have missed this outage too. It now
    pings 1.1.1.1/8.8.8.8 only. No watchdog can fix an ISP outage; only a hotspot failover would.
  - The PREREG17/18 rules already exclude these minutes: the book gaps are over 5 s, and the 16 failed clock
    checks fall in the same window.
- **No clock checks before 02:51 UTC** (collector restarts for deploys), so 00:00–02:51 on 11-12 is excluded by
  the clock rule. Clock offsets otherwise peak at 30 ms against the 250 ms limit.
- **Trade IDs:** Kraken's run in sequence, so the trade_id gap exclusion works for Kraken as well as Coinbase.
  Outside the outage there are 5–9 short gaps per product per day (1–93 IDs each); each excludes a minute or two.
- **About 6 of the first day's 24 hours are excluded** (roughly 1% of the 27-day window). This does not threaten
  the 200-trade minimums unless outages recur.

## Partial volatility exit (halve 2x→1x on a PREREG23 gate): not run (2026-11-13)
- The owner picked "partial exit, not all" after PREREG23 failed. It was not pre-registered or run, for two reasons:
  1. **It is a variant of a failed test** (same 10/100 > 1.5 gate, different size), which the rules forbid.
  2. **The result is already known from arithmetic.** Daily returns are linear in the position
     (`lab.position_returns`), and halving on the gate is exactly the average of the P1 and V1 positions. So its daily
     return equals the average of theirs, plus at most a little saved cost. Bear months would be about
     (−8.0 + −10.1) / 2 ≈ −9.0%/month: worse than P1, far from the −5.0 needed. It cannot pass.
- No trial charged (742). Any partial de-risk built on the same volatility-expansion gate is closed with it.
