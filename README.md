# PiQuant: crypto trading research and paper-trading engine

PiQuant is a personal crypto trading system with two parts:

- a **Go engine** (`cmd/piquant`, `internal/`), which runs paper trading, records market data and sends alerts on a
  Raspberry Pi 5 and a Linux desktop;
- a **Python research lab** (`research/quant_lab`), where every strategy idea is pre-registered, tested once on
  history and kept or closed by fixed rules.

> **Paper money only.** Nothing here places real trades. None of this is financial advice. The results below are
> backtests, and they come with large drawdowns.

**Goal and where it stands (2026-11-13):** the owner's target is 7% a month, with drawdowns under 25%.
- 29 pre-registrations and **751 charged trials** have found one strategy that reaches 7% a month in backtests:
  P1, the BTC+ETH trend core at 2x. Its maximum drawdown is −84%.
- One market-neutral strategy passed: C1, funding carry, at about +5–8% a year with a −1% drawdown.
- Every attempt to cut P1's bear-market losses without cutting more profit has failed.
- See [Results of every test](#results-of-every-test) and [What the research shows](#what-the-research-shows).

---

## Contents
- [What runs where](#what-runs-where)
- [Strategies in paper trading](#strategies-in-paper-trading)
- [How the research works](#how-the-research-works)
- [Results of every test](#results-of-every-test)
- [What the research shows](#what-the-research-shows)
- [Scheduled and open tests](#scheduled-and-open-tests)
- [Data pipeline](#data-pipeline)
- [Daily operations](#daily-operations)
- [Repository layout](#repository-layout)
- [Engine, installation and hardware](#architecture--operational-modes)

---

## What runs where

| Machine | Service | What it does |
|---|---|---|
| **Raspberry Pi 5** (wired Ethernet) | `piquant.service` | Paper engine on Coinbase. The medium-frequency entries are off (`--mft-entries=false`, drop-in `mft-off.conf`). The in-process 2x/1x trend ledger writes to `~/piquant-data/trend`. |
| Pi | `piquant-collect.service` | Records Coinbase/Kraken/Binance.US trades and 1 s top-10 order books into `~/piquant-collect`. Unit copy: `deploy/piquant-collect-pi.service`. |
| Pi | `wifi-watchdog` | Restarts networking when 1.1.1.1 and 8.8.8.8 both stop answering (`scripts/wifi-watchdog.sh`). |
| **Desktop** | `piquant-collect` (user unit, `--ticks=false`) | Collects news, candles and the carry ledger, backs up the Pi every night, and mirrors the Pi's tick data to `~/piquant-backups/pi-collect`. |
| Desktop | `piquant trend` | The daily trend paper ledger (PREREG6 + P1), `~/.local/bin/piquant-trend`. |

Email alerts (venue down, mirror failed, …) need `~/.config/piquant/alert.env` on each machine. The owner fills this
in; it is never committed.

---

## Strategies in paper trading

All are paper only. Each one passed its pre-registered test, or was added at the owner's request and is labelled as
such.

### P1: BTC+ETH trend core at 2x (the main strategy)
- **Rule:** at each daily close (00:00 UTC), for BTC and for ETH separately:
  - if close > 50-day simple moving average, hold **2x long** over the next day;
  - otherwise hold **cash**.
- Two equal sleeves. ETH's half is cash before ETH had data.
- **Costs modelled:** US futures, with 6 bps taker + 1 bp slippage per unit of turnover and funding 0.1 bp/hour. The
  stress test uses 1 bp/hour.
- **Source:** PREREG5 picked SMA50 from a grid of 20/50/100/200 days × 1/1.5/2x. PREREG11 tested and passed the
  BTC+ETH pair.

| Period | %/month (geometric) |
|---|---|
| 2016–17 | +27.99 |
| 2018–20 | +6.24 |
| 2021–2022-12 | +0.77 |
| 2022-12 → 2025-06 (discovery) | +6.25 |
| 2025-07 → now (A) | +3.32 |
| **Full 2016–2026** | **+8.50** |

- **Risk:** max drawdown −84%, worst month −49%, 43% of months losing.
  - Bear months (BTC month < −5%) average **−8.0%**, flat months −3.9% and bull months +38.2%.
  - At stress costs it makes only +1.19%/month.
- **Lower-leverage versions of the same rule (PREREG13):**

  | Version | %/month | Max drawdown |
  |---|---|---|
  | 1x (R1) | +5.18 (about 83%/year) | −57% |
  | 1.5x (R2) | +7.09 | −73% |

  Since 2018 the 1x version made about 22–64% a year, depending on the period.

### R1: the same rule at 1x (forward comparison, added 2026-11-13)
- BTC+ETH, each sleeve 1x while its close is above its 50-day average, otherwise cash.
- Backtest 2016–2026: +5.18%/month, drawdown −57%, bear months −4.2 (P1: +8.50, −84%, −8.0).
- Since 2021-04 it made +1.62%/month against P1's +2.01, with a third less drawdown.
- Ledger name `R1_btc_eth_1x`. It failed PREREG13's −50% drawdown bar; it is tracked for comparison only.

### T1 / T2: forward test of the BTC trend filter (PREREG6)
- **T1:** 1x BTC spot when the last daily close > SMA50, else flat (2 bps fee + 1 bp slippage).
- **T2:** 2x under the same rule, on futures.
- **Verdict:** at 12 complete months (2027-11-30). T2 is stopped early as a FAIL if it falls 50% from its peak.

### PAPER50: $50 profile (owner request, not a pre-registered strategy)
- The P1 rule on spot at 20% of NAV in total, with a 1.2% retail fee, a 2% daily loss stop and a 5% drawdown kill.
- **Historical diagnostic:** the kill switch fired from every one of 38 start dates, after a median of 111 days. The
  1.2% fee costs more than the trend earns at 0.2x.

### C1: funding-rate carry (PREREG19, passed)
- **Rule:** long spot and short the perpetual future on BTC and ETH, always on, collecting the funding rate.

| Base %/yr | Stress %/yr | Max DD (stress) | 2020 | 2021 | 2022 | 2023–24 | 2025–Oct 2026 |
|---|---|---|---|---|---|---|---|
| +8.15 | +5.06 | −1.0% | +9.8% | +16.3% | −0.6% | +8.7% | +1.6% |

- **Not modelled:** basis swings and exchange failure (for example FTX, 2022).
- **Command:** `piquant carry`.

### Engine short-horizon core (live on the Pi, entries off)
- The microstructure engine (order-book signals, VPIN, inventory skew) measured **zero gross edge**. Its trades lose
  about their cost, roughly 14 bps a round trip (rounds 1–4).
- Its medium-frequency entries are switched off, and an expectancy breaker (on by default, 50 trades) stops new entries
  once the run's own trades show negative expectancy.

---

## How the research works

Rules (from `research/quant_lab/PREREG.md` and the owner's `CLAUDE.md`):

1. **Pre-register first.** Each test is written as `PREREGnn.md` (rules, data, pass rule and a prediction),
   committed and pushed **before** any code touches real data.
2. **Run once.** The real run happens a single time, after synthetic tests pass. Failures are recorded with their
   numbers.
3. **No variants of a failed test.** A failed idea is closed. Re-running it with different parameters is not allowed.
4. **Count every trial.** A running trial counter (now **751**) is used for multiple-testing corrections.
5. **Never hand-edit results.** `results/roundNN.json` files are written only by the run scripts.

**Standard daily-strategy setup** (PREREG7/11–16/23–25):
- **Data:** Coinbase BTC-USD/ETH-USD 1-minute candles, resampled to daily.
- **Timing:** decide at the close, hold over the next day (`lab.position_returns`).
- **Periods:**

  | Code | Dates |
  |---|---|
  | D | 2016–17 |
  | C | 2018–20 |
  | B | 2021-01 → 2022-12-16 |
  | discovery | 2022-12-17 → 2025-06 |
  | A | 2025-07 → now |

- **Bear/flat/bull months:** BTC buy-and-hold month < −5% / between / > +5%.
- **Usual pass rule for P1 overlays** (all five must hold):
  1. bear months improve by at least 3 points over P1;
  2. full return ≥ 7%/month;
  3. max drawdown no worse than P1's;
  4. period A > 0;
  5. stress return > 0.

**Python environments:**

| Where | Python | Notes |
|---|---|---|
| Desktop | `~/miniconda3/envs/nerfstudio/bin/python` | Has pyarrow, which the parquet candle cache needs. |
| Desktop | system `python3` | Runs the synthetic tests (`python3 -m pytest -q test_strategiesNN.py`). |
| Pi | `~/piquant-research-env` | numpy, pandas, pytest. Run heavy jobs with `nice -n 19 ionice -c3`. |

---

## Results of every test

| # | Idea | Result | Key numbers |
|---|---|---|---|
| 1 | Live engine core, 1h trend, vol-managed BTC, 4 "quantum-inspired" methods (10 families) | **FAIL** (all) | No holdout pass in 600 trials. The engine core has zero gross edge, and 1h trend died in the 2025–26 bear market. |
| 2 | New algorithms with an untouched holdout | **FAIL** | – |
| 3 | Wider take-profit/stop-loss brackets (S7, S8) | **FAIL** | Confirmed that with random entries the win rate = SL/(TP+SL) and the gross edge is 0 (24/24 cells). |
| 4 | ≥55% win-rate geometry; LFM3 news model | Win rate **reached** (55.7–62.9%) but **loses money**; LFM3 **scrapped** | Every geometry loses about its 14.4 bps cost. No news output predicts returns (all \|ρ\| ≤ 0.03). |
| 5 | ≥60% of months beating buy-and-hold (SMA trend grid) | **FAIL** the 60% rule | 56/52/64/62% of months (pooled 59.6%). Beat buy-and-hold total return on all holdouts. SMA50 2x chosen. |
| 6 | Forward paper test of the trend filter (T1/T2) | **Running** | Verdict 2027-11-30. |
| 7 | Long/short trend (short in downtrends) | **FAIL** | Bear months −12.7 → −6.6, but flat months −10 to −14, DD −87 to −93%. |
| 8 | Medium-frequency maker entries | **FAIL** | Negative net in every cell. |
| 9 | Bear-market minutes-horizon shorts | **FAIL** | −11.5 bps net per trade (t −65). |
| 10 | Time-of-day effects | **FAIL** | Discovery picks lost −1.5 to −14 bps on every holdout. |
| 11 | **BTC+ETH 2x trend (P1)**; vol targeting (P2) | **P1 PASS**; P2 fail | P1 +8.50%/mo, DD −84%. P2 +8.75%/mo, DD −88%. |
| 12 | Drawdown brake (Q1); add SOL (Q2) | **FAIL** | Q1 DD −70% but only +5.88%/mo. Q2 DD −81%. |
| 13 | Lower leverage (1x, 1.5x), fast exit, trailing stop | **FAIL** (DD ≤ 50% rule) | 1x +5.18%/mo, DD −57%. 1.5x +7.09, −73%. Fast exit +8.58, −76%. Trailing stop +6.29, −82%. |
| 14 | Stop-loss + Grossman–Zhou sizing + ensemble | **FAIL** | Stops gap through: S1 DD −97%. GZ cut DD to −56% but return ≈ 0. |
| 15 | #14 re-run with one bad print removed | **FAIL** | Closed. |
| 16 | Buy the dip at the daily scale | **FAIL** | +200 bps/trade but t 1.34, and 3 of 5 periods lost. |
| 17 | Maker entries with pre-fill defense, on tick data | **Scheduled** | Runs once on or after 2026-12-09 on the recorded ticks. |
| 18 | Cross-venue lead-lag and trade-flow momentum, on tick data | **Scheduled** | Same date. |
| 19 | **Funding carry (C1)**; high-funding-only (C2) | **C1 PASS**; C2 fail | C1 +5.06%/yr at stress, DD −1%. |
| 20 | Beta-neutral BTC–ETH book | **FAIL** | Sharpe 0.42 (stress −2.36), DD −80%. |
| 21 | Cascade short + VPIN filter, 531 M real Binance ticks (2024) | **FAIL** | 10 holdout trades, −0.8 bps. Passive fills lose about 1 bp to adverse selection. |
| 22 | #18's rules on 47 Tardis.dev free days, 2023–2026 | **FAIL** (all 3) | Coinbase leads Kraken by 4–5 bps within 30 s; nothing leads Coinbase. Kraken's 80 bps round trip kills it (−78 bps net). |
| 23 | Volatility-expansion exit (10/100-day vol > 1.5) | **FAIL** | Bear months got **worse** (−10.1 vs −8.0). The exit fires after the drop and misses rebounds. |
| – | Partial exit (halve on the #23 gate) | **Not run** | A variant of a failed test. Its result is about the average of P1 and #23, so bear months ≈ −9.0. |
| 24 | Multi-market trend overlay (SPY/TLT/GLD/UUP, the trend-fund "crisis alpha" method) | **FAIL** | The overlay earned ≈ 0 over 2016–26 (Sharpe 0.06). The idle-capital version made bear months −12.9. |
| 25 | Long BTC / short a 14-altcoin basket | **FAIL** | The pair lost −2.5%/mo in bear months. It earned only in 2022–23. Worst day −83.6% (LTC squeeze, 2017-03-30). |
| 26 | Convex leverage: P1 plus bought 30-day at-the-money puts priced from Deribit DVOL (2021-04 on) | **FAIL** | K1 +0.18%/mo, DD −83% against 1x +1.62, −57%. The puts paid back 76–81% of their premium. The drawdown is whipsaw bleed, not single-day gaps. |
| 27 | Always-on ladder of 30-day put spreads beside P1 (capped loss, no short) | **FAIL** | Sleeve alone +1.1 in bear months but −2.0%/mo overall. Portfolio +6.80%/mo, bear −6.6 (needed −5.0). Spreads paid back 53–71% of premium. |
| 28 | Sold 30-day iron butterflies, loss capped at 25% (the seller's side of #26–27) | **FAIL** | Sleeve −1.59%/mo, paid out 109–118% of the credit. Correlation with P1 −0.70, but no premium to harvest. Options closed, bought and sold. |
| 29 | Cross-sectional momentum: 16 coins, weekly, long the strongest third and short the weakest | **FAIL** (all 5) | Book alone −1.62%/mo, bear −1.9, DD −92%, worst day −30.9%. In bear months the longs lost more (−11.8) than the shorts made (+8.7). |

Full rules, numbers and readings are in `research/quant_lab/PREREGnn.md`; machine output is in
`research/quant_lab/results/`.

---

## What the research shows

1. **Short-horizon direction has no edge at retail costs.** Minutes-to-hours signals all measured gross ≈ 0:
   order flow, VPIN, time of day, news, brackets and lead-lag. Fees and spread decide the result.
2. **Daily trend works but is fragile.** P1's return comes from leverage on a trend filter. Drawdown grows with
   leverage: −57% (1x), −73% (1.5x), −84% (2x).
3. **Bear markets: nothing tested makes money in them.** These were all tested and failed:
   - shorting;
   - volatility exits, full and partial;
   - stops;
   - drawdown brakes;
   - trend in other markets;
   - BTC vs altcoins.

   The large drops start before any signal reacts, and the defences cost more in rebounds and sideways months than
   they save.
4. **The math of the goal.** 7%/month (about 125%/year) with drawdowns ≤ 25% needs a Sharpe ratio of about 4. P1
   measures 1.5. At Sharpe 1.5, a 25% drawdown limit supports about 1.3%/month (`docs/PHASE1_TAIL_RISK_2026-11.md`).
   80%/year (5%/month) is roughly P1 at 1x, with a −57% drawdown in history.
5. **What professional funds do differently:**
   - they trade 50–150 markets, not 4;
   - they get maker rebates and very low fees;
   - they run at 10–15% volatility;
   - they buy crash insurance (put options), which has not been tested here yet.

---

## Scheduled and open tests

| When | What |
|---|---|
| On or after **2026-12-09** | PREREG17 and PREREG18 run once, unchanged, on the Pi, using the recorded Coinbase/Kraken ticks from 2026-11-12 onward. `KEEP_MONTHS` blocks archiving those months. |
| **2027-11-30** | PREREG6 (T1/T2) forward verdict. |
| Ongoing | P1, PAPER50 and C1 paper ledgers. |
| Open idea, not pre-registered | Crash insurance: monthly protective puts (for example on IBIT), priced from Deribit's DVOL index (free, 2021 on). |
| Open idea, not pre-registered | A combined trend + carry portfolio. |

Notes on scheduled work live in `research/NEXT_TESTS.md`.

---

## Data pipeline

| Data | Where | Notes |
|---|---|---|
| Coinbase 1-minute candles (BTC, ETH) | `~/piquant-collect/candles/<product>/` → parquet cache `~/piquant-collect/train/` | Used by every daily test (`lab.load_candles`). |
| Ticks + 1 s top-10 books (Coinbase, Kraken, Binance.US) | **Pi** `~/piquant-collect/ticks/<feed>/YYYY-MM-DD.jsonl.gz`, `book/<feed>/…` | Recorder moved from the desktop to the Pi on 2026-11-13 00:04 UTC. Multi-member gzip, flushed every 60 s. Data from 11-13 was merged by `scripts/merge_desktop_ticks.py`. |
| Clock checks, recorder status | Pi `~/piquant-collect/clock_offsets.jsonl`, `ticks_status.json` | Trade-ID gaps and reconnects are read before any tick test. |
| Second copy of the Pi data | Desktop `~/piquant-backups/pi-collect` | Daily rsync after 03:00, no delete. Alert "Pi tick-data mirror failed" on errors. |
| Monthly archive | Pi `~/piquant-collect/archive/YYYY-MM.tar.zst` | `scripts/collect_archive.sh pack/restore/drop`. zstd -19 --long=27 is about 25% smaller than daily gzip. md5 is checked before the originals are deleted. |
| Binance 2024 BTCUSDT trades | `~/piquant-collect/pqbin/spot/` (531 M ticks, about 21 GB) | `piquant import-ticks`. Used by PREREG21. |
| Tardis.dev free first-of-month days | Streamed, not kept | PREREG22. |
| ETF prices + T-bill rate | `~/piquant-data/macro/` | Yahoo adjusted closes and FRED DTB3. PREREG24. |
| Altcoin daily closes (14 coins) | `~/piquant-data/alts/` | Coinbase public candles. PREREG25. |

`data/historical/*_daily_full.csv` holds daily BTC/ETH history for the Go backtester. Files in
`data/historical/ticks/*.pqbin` are **synthetic** and must never be used as market data.

---

## Daily operations

**Check the whole system on one screen** (Pi services, recorder age, venues, book gaps, clock, alert.env and the
desktop mirror):

```bash
scripts/pipeline_status.sh
```

**Sync committed code to the Pi before any research run there.** It refuses if research/ or scripts/ have
uncommitted changes:

```bash
scripts/sync_pi.sh
```

Both scripts take `PI=everett@<ip>` if `raspberrypi.local` doesn't resolve.

**Trend paper ledger:**

```bash
piquant trend --dir ~/piquant-data/trend
```

**Carry paper ledger:**

```bash
piquant carry
```

**Pack a finished month of tick data:**

```bash
scripts/collect_archive.sh pack 2026-10
```

**Run a lab test's synthetic checks** (desktop):

```bash
cd research/quant_lab && python3 -m pytest -q test_strategies25.py
```

Other CLI commands: `setup`, `doctor`, `dashboard`, `monitor`, `dry-run`, `backtest-history`, `backtest-ticks`,
`bench`, `secrets`, `tax`, `import-ticks`, `alert-test`, `collect`, `news` (see `piquant help`).

---

## Repository layout

| Path | Contents |
|---|---|
| `cmd/piquant/` | The CLI: paper engine, collector, trend/carry ledgers, backtests, alerts, tax. |
| `internal/` | Engine packages, among them `collect` (recorder, backups, mirror), `trend`, `carry`, `paper`, `orderbook`, `models`, `alert` and `secrets`. |
| `research/quant_lab/` | `PREREGnn.md` (pre-registrations with results), `strategiesNN.py` + `test_strategiesNN.py`, `run_roundNN.py`, `fetch_roundNN.py`, `lab.py` (data, costs, simulator), `results/`. |
| `research/NEXT_TESTS.md` | Scheduled tests and operating notes. |
| `research/AFK_REPORT_*.md` | Reports from unattended work blocks. |
| `scripts/` | `pipeline_status.sh`, `sync_pi.sh`, `collect_archive.sh`, `merge_desktop_ticks.py`, `wifi-watchdog.sh`, installers, kernel tuning. |
| `deploy/` | systemd unit copies. |
| `docs/` | Deployment, dry run, kernel tuning, remote access, strategy notes (`STRATEGIES.md`, `TRADING_EXPLAINED.md`), tail-risk and tranche analyses. |

---

## Engine, installation and hardware

The sections below describe the Go engine, its installation and its hardware tuning. The engine's models (lead-lag,
VPIN, inventory decay, Kyle's lambda, adaptive ensemble, and so on) are components. Whether any of them makes money is
decided only by the tests above, and the short-horizon ones measured no edge at retail costs.

## Architecture & Operational Modes

```
 +-------------------------------------------------------------------------------+
 |                       MODE 1: RASPBERRY PI 5 STANDALONE                       |
 |                                                                               |
 |   [Cores 0 & 1: Housekeeping]                  [Cores 2 & 3: ISOLATED RT]     |
 |   Linux OS, Avahi mDNS, Disk I/O,    ==>       Binance L2 Stream -> SPSC Ring |
 |   HTTPS / PWA Web Dashboard (:8080)            Microstructure Signals & MM    |
 |                                                Zero-Alloc Execution & Paper   |
 +-------------------------------------------------------------------------------+

 +-------------------------------------------------------------------------------+
 |                    MODE 2: DUAL-NODE CLUSTER (PI 5 + PI 3B)                   |
 |                                                                               |
 |   Raspberry Pi 5 (Trading Core)             Raspberry Pi 3B (Worker/Display)  |
 |   - Core 2 & 3: Microstructure Engine       - Hosts Dashboard UI (:8080)     |
 |   - L2 Order Book & Real-Time Trading <==>  - Offloads Purged K-Fold CV & GS  |
 |   - Isolated Private Secrets Store          - Sanitized HMAC mDNS Peering     |
 +-------------------------------------------------------------------------------+
```

---

## Hardware & OS Prerequisites

| Component | Recommendation | Minimum Supported |
|---|---|---|
| **Core Device** | **Raspberry Pi 5** (8 GB or 4 GB LPDDR4X) | Raspberry Pi 5 (4 GB) |
| **Worker (Optional)** | **Raspberry Pi 3B / 3B+ / 4B** (Cooperative Dual-Node) | Raspberry Pi 3B (1 GB) |
| **Operating System** | **Raspberry Pi OS (Bookworm 64-bit)** with Linux 6.6+ | Debian 12 (Bookworm `arm64`) |
| **Power Supply** | Official 27W USB-C PD Power Supply (5V/5A) | 15W USB-C Supply |
| **Cooling** | Official Raspberry Pi 5 Active Cooler | Passive aluminum heatsink |
| **Storage** | A2-rated MicroSD card or NVMe PCIe SSD | Class 10 MicroSD (16 GB+) |
| **Development Machine** | Linux / macOS / Windows with Go 1.22+ and `make` | Any modern OS with Git |

---

## Step-by-Step Installation Guide

### Option 1: Fast Automated Release Install (Recommended on Pi)

If you have downloaded the release archive `piquant-release.tar.gz` onto your Raspberry Pi:

```bash
# 1. Extract the release package
tar -xzvf piquant-release.tar.gz
cd piquant-release

# 2. Run the automated installer (auto-detects architecture: arm64 or armv7)
sudo ./scripts/install.sh auto standalone

# 3. Apply low-latency Linux kernel tuning (locks 2.4 GHz, isolcpus, busy polling)
sudo ./scripts/tune-kernel.sh apply

# 4. Reboot once to activate kernel cmdline core isolation (isolcpus=2,3)
sudo reboot
```

---

### Option 2: Build from Source on Development Computer

You can build and test on your development PC, cross-compile for the Raspberry Pi, and copy the binaries over:

```bash
# 1. Clone repository
git clone https://github.com/everett6/piquant-live.git
cd piquant-live
git checkout release/production-v1

# 2. Verify all unit tests and zero-allocation benchmarks
make test
make race

# 3. Cross-compile for ARM64 (Pi 5) and ARMv7 (Pi 3B)
make cross
make package
# Generates:
#   bin/piquant-linux-arm64  (Raspberry Pi 5)
#   bin/piquant-linux-armv7  (Raspberry Pi 3B)
#   bin/piquant-linux-amd64  (x86_64 PC)
#   bin/piquant-release.tar.gz

# 4. Transfer release package to your Raspberry Pi 5
scp bin/piquant-release.tar.gz pi@piquant-core.local:/home/pi/
```

---

### Option 3: Manual Installation on Raspberry Pi 5

#### Step 1: Install Binary & Directory Hierarchy
```bash
# Place the binary in system path
sudo install -Dm755 bin/piquant-linux-arm64 /usr/local/bin/piquant

# Create dedicated unprivileged system user
sudo useradd -r -s /usr/sbin/nologin -d /var/lib/piquant -M piquant || true

# Setup secure directories
sudo mkdir -p /etc/piquant /var/lib/piquant/data /var/log/piquant
sudo chown -R piquant:piquant /etc/piquant /var/lib/piquant /var/log/piquant
sudo chmod 0700 /etc/piquant
sudo chmod 0755 /var/lib/piquant/data /var/log/piquant
```

#### Step 2: Initialize Node Secrets & Cryptographic Tokens
```bash
sudo -u piquant piquant setup --role agent --dir /etc/piquant --data /var/lib/piquant/data --no-discovery
sudo chmod 0600 /etc/piquant/secrets

# Retrieve your Web Dashboard login token:
sudo piquant secrets get dashboard_token --dir /etc/piquant
```

#### Step 3: Low-Latency Linux Kernel Optimization
Run the automated kernel tuning utility:
```bash
sudo ./scripts/tune-kernel.sh apply
```
This automatically configures:
- **`arm_boost=1`** in `/boot/firmware/config.txt` (locks Cortex-A76 clock at 2.4 GHz).
- **Core Isolation** in `/boot/firmware/cmdline.txt`: `isolcpus=2,3 nohz_full=2,3 rcu_nocbs=2,3`.
- **IRQ Steering**: Hardware PCIe/RP1 Ethernet interrupts steered to Cores 0 & 1 (`irqaffinity=0,1`).
- **Network Busy-Polling**: Socket latency reduction via `net.core.busy_poll=50` and `net.core.busy_read=50`.
- **Memory Locking**: Unlimited `mlockall` and zero-swap pressure (`vm.swappiness=1`).

Reboot to apply the kernel parameters:
```bash
sudo reboot
```

---

## Starting the 14-Day Paper Money Dry Run

PiQuant includes a continuous, autonomous 14-day ($1,000.00 USDT) dry-run engine that streams public live Binance L2 market data (no API keys required).

### Option A: Interactive Foreground Run
```bash
# Standard 14-day dry run on pure live Binance feeds (fallback disabled by default):
piquant dry-run --capital 1000 --duration-hours 336 --addr :8080

# Opt-in to local synthetic fallback when offline (freezes mark prices & pauses quoting while down):
piquant dry-run --fallback

# Custom WebSocket URL (e.g. vision or US Binance feeds):
piquant dry-run --ws-url "wss://data-stream.binance.vision:9443/stream?streams=btcusdt@depth20@100ms/ethusdt@depth20@100ms/btcusdt@trade/ethusdt@trade"
```
Open `http://<your-pi-ip>:8080` in your browser, enter your dashboard token, and watch live order book execution with the **DATA SOURCE: LIVE** indicator.

### Option B: 24/7 Background Systemd Service (Recommended)
```bash
# 1. Install and enable the systemd service
sudo cp deploy/piquant-dryrun.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now piquant-dryrun.service

# 2. Check service status
sudo systemctl status piquant-dryrun.service

# 3. View live progress report in terminal anytime:
piquant dry-run --status
```

Terminal status report output:
```text
=======================================================
 PIQUANT 2-WEEK AUTONOMOUS DRY RUN STATUS REPORT
=======================================================
 Status:          ACTIVE (OK)
 Data Source:     live
 Feed Down Time:  0.0s
 Progress:        Day 1 of 14 (335.8 hours remaining)
 Start Time:      2026-11-04T19:29:03-08:00
 Target End:      2026-11-18T19:29:03-08:00
 Initial Capital: $100,000.00 USDT
 Current Equity:  $100,248.50 USDT (+0.25%)
 Realized PnL:    $248.50 USDT
 Peak Equity:     $100,280.00 USDT
 Max Drawdown:    0.04%
 Total Trades:    18 (Win Rate: 66.7%)
=======================================================
```

---

## Raspberry Pi 5 Micro-Latency Stress Harness

PiQuant includes a hardware stress and latency benchmark harness (`scripts/pi5-stress.sh`) that evaluates the full execution pipeline (`ring -> book -> features -> models -> paper engine`) under simulated Pi 5 constraints (`taskset -c 0-3 GOMAXPROCS=4 GOMEMLIMIT=2GiB`) with a 4.0x Pi factor applied to latency budgets.

```bash
# Run full suite (1x, 10x, and burst)
./scripts/pi5-stress.sh all

# Or run directly via CLI
./bin/piquant bench --events 5000 --mode all
```

### Verified Performance Benchmarks (Pi 5 4x Slowdown Applied)

| Scenario | Ingestion Rate | p50 (Pi 5) | p99 (Pi 5) | p99.9 (Pi 5) | Max GC Pause | Drops | Pass Criterion |
|---|---|---|---|---|---|---|---|
| **1x Baseline** | 100 ev/s | 1.2 µs | 20.3 µs | 40.2 µs | 4.4 µs | 0 | **PASS (<1ms)** |
| **10x Pressure** | 1,003 ev/s | 1.1 µs | 13.5 µs | 64.7 µs | 6.1 µs | 0 | **PASS (<1ms, 0 drops)** |
| **Burst Saturation** | 2,428,721 ev/s | 1.1 µs | 2.3 µs | 22.4 µs | 9.2 µs | 0 | **PASS (<1ms)** |

---

## Installing the Web App as an App (PWA)

PiQuant has an embedded **Progressive Web App (PWA)** with native-like windowing and offline status caching:

* **iPhone / iPad**:
  1. Open `http://<your-pi-ip>:8080` in Safari.
  2. Tap the **Share** button $\rightarrow$ **"Add to Home Screen"**.
* **Android**:
  1. Open `http://<your-pi-ip>:8080` in Chrome.
  2. Tap **Menu** $\rightarrow$ **"Install App"** (or **"Add to Home Screen"**).
* **Mac / Windows / Linux Desktop**:
  1. Open `http://<your-pi-ip>:8080` in Chrome, Edge, or Brave.
  2. Click the **Install** icon in the address bar to create a standalone dock/taskbar app.

---

## Pairing Raspberry Pi 5 + Pi 3B (Dual-Node Cluster)

When running both a Pi 5 and a Pi 3B:

1. **On Raspberry Pi 3B (Dashboard & Worker)**:
   ```bash
   sudo ./scripts/install.sh dashboard
   ```
2. **Synchronize Peer Authentication**:
   ```bash
   # On Pi 5:
   sudo piquant secrets get peer_token --dir /etc/piquant

   # On Pi 3B:
   sudo piquant secrets set peer_token "<TOKEN_FROM_PI_5>" --dir /etc/piquant
   sudo systemctl restart piquant-dashboard.service
   ```
3. Both devices discover each other automatically via mDNS (`piquant-core.local` and `piquant.local`). Access your dashboard at `http://piquant.local:8080`.

---

## Verification & Health Check

Run `piquant doctor` at any time to verify system integrity, isolated CPU cores, memory limits, and hardware telemetry:

```bash
piquant doctor
```

```text
PiQuant doctor: OK (all checks passed)
  CPU Cores:        4 (Cores 2 & 3 Isolated via isolcpus)
  Governor:         performance (Locked 2.4 GHz)
  Thermal Zone:     44.2°C (BCM2712 Throttle Bitmask: 0x0 Nominal)
  Memory Lock:      Unlimited (mlockall enabled)
  Secrets File:     /etc/piquant/secrets (mode 0600)
```

---

## Complete Institutional Quantitative Model Suite

PiQuant provides a comprehensive quantitative trading, statistical arbitrage, and microstructure risk framework engineered specifically for zero-allocation ARM64 Cortex-A76 performance:

### 1. Bybit V5 Dual-Venue Market Streaming (`internal/paper/bybit_stream.go`)
- Ingests high-frequency Bybit V5 Linear WebSocket feeds (`orderbook.50.BTCUSDT`, `publicTrade.BTCUSDT`) concurrently with Binance.
- Custom zero-allocation byte scanner (`FastExtractBybitOrderbook`, `FastExtractBybitTrades`) parsing JSON payload byte streams directly into ring buffer memory without heap escaping.

### 2. Cross-Venue Lead-Lag Arbitrage Engine (`internal/models/common/cross_venue_leadlag.go`)
- High-frequency price discovery monitor across Binance (leader) and Bybit (follower).
- Computes Hasbrouck Information Share and tick-lead correlation via recursive online innovation covariance tracking, predicting immediate follower price drift $\Delta \hat{P}_{\text{follower}}$.

### 3. Dynamic Inventory Aging & Alpha-Decay Liquidation Policy (`internal/models/common/inventory_decay.go`)
- Multi-tier inventory decay policy applying quadratic reservation skew penalties $r_{\text{aging}}(q, \tau)$ as position holding duration increases.
- Automatic transition through `Nominal`, `Elevated Decay`, and `Critical Liquidation` states to eliminate stale inventory risk.

### 4. Volume-Synchronized Probability of Toxicity (VPIN) (`internal/models/common/vpin.go`)
- Easley-López de Prado-O'Hara (2012) volume-clock toxicity estimator tracking order imbalance in constant-volume buckets ($V = 5.0\text{ BTC}$, $N = 50$).
- Normal CDF percentile mapping with Bulk Volume Classification (BVC) and dynamic quote widening (`AdjustQuote`) to preempt adverse selection shocks.

### 5. Kyle's Lambda & Market Impact Sizer (`internal/models/common/kyles_lambda.go`)
- Kyle (1985) microstructural price impact coefficient $\lambda_{\text{Kyle}} = \frac{\operatorname{Cov}(\Delta P, Q_{\text{signed}})}{\operatorname{Var}(Q_{\text{signed}}) + \epsilon}$ tracked via recursive online EWMA covariance.
- Sizes passive maker quotes and aggressive taker orders under an explicit adverse selection impact budget in $0\text{ B/op}$.

### 6. Dynamic Risk Safeguard & Circuit Breaker Engine (`internal/paper/circuit_breaker.go`)
- 4-state finite risk state machine (`Normal`, `Warning`, `ReduceOnly`, `Tripped`).
- Real-time gating on portfolio drawdown, inventory exposure caps, consecutive loss streaks, spread blowout ratios, and toxicity alerts with automatic cooldown recovery.

### 7. Regime-Switching Adaptive Meta-Ensemble (`internal/models/common/adaptive_ensemble.go`)
- Online Multiplicative Weights / Discounted Hedge Meta-Learner combining 6 distinct alpha families:
  1. `glft_queue`: Queue-reactive L1 imbalance & microprice deviation.
  2. `obi_momentum`: Multi-horizon order flow imbalance momentum.
  3. `lead_lag`: Cross-venue lead-lag innovation signal.
  4. `mean_reversion`: Microprice deviation and short-term return reversal.
  5. `vpin_defense`: Order flow toxicity adverse selection damping.
  6. `hawkes_cascade`: Self-exciting point-process cascade momentum.
- Classifies microstructure state into 3 regimes (`RegimeLowVolRange`, `RegimeTrending`, `RegimeHighVolCascade`) based on realized volatility, jump ratio, and Hawkes branching ratio.
- Maintains per-regime discounted expert weights updated online via exponential updates:
  $$w_{r,i}^{(t+1)} = \frac{w_{r,i}^{(t)\gamma} \cdot \exp(\eta \cdot R_{i,t})}{Z_t}$$
- Zero-allocation hot paths for `Predict`, `UpdateEdge`, `Regime`, and `TopExpert`.

### 8. Perpetual Funding Rate & Carry Harvesting Engine (`internal/models/common/funding_carry.go`)
- Tracks spot-perpetual premium divergence $P_{\text{premium}} = \frac{P_{\text{perp}} - P_{\text{spot}}}{P_{\text{spot}}}$ with exchange damping clamp $\text{clamp}(I - P, -0.05\%, +0.05\%)$.
- Predicts 8h EWMA funding rate $F_{\text{pred}}$ and annualized carry yield APR %.
- Computes funding-aware reservation price skew (`FundingSkewBps(inventory, timeToSettlementSec)`) that harvests positive funding rebates while aggressively penalizing inventory held on the paying side as settlement approaches.

### 9. Post-Trade TCA & Multi-Horizon Markout Engine (`internal/paper/tca_markout.go`)
- Fixed-capacity ring buffer (`[256]PendingMarkout`) evaluating post-fill execution quality at **+1s**, **+5s**, **+10s**, and **+60s** horizons:
  $$\text{Markout}_{\tau} = \operatorname{side} \cdot \frac{\text{Mid}_{t+\tau} - \text{FillPrice}_t}{\text{FillPrice}_t} \times 10,000\text{ bps}$$
- Decomposes execution performance into:
  - `MeanSpreadCaptureBps`: Half-spread captured at fill time.
  - `MeanMarkout1sBps`, `MeanMarkout5sBps`, `MeanMarkout10sBps`, `MeanMarkout60sBps`.
  - `AdverseSelectionBps`: Execution edge degradation from fill to +10s.
  - `ToxicFillRatio`: Percentage of fills suffering adverse 1s markout $< -2.0\text{ bps}$.
- 100% zero heap allocations across `RecordFill`, `OnTick`, and `Snapshot`.

### 11. High-Resolution 2-Year Microsecond Tick-by-Tick Simulation Engine (`cmd/piquant/backtest_ticks.go`)
- Replays actual microsecond trade ticks directly into PiQuant's `EventArena`, `SPSCRing`, `L2OrderBook`, `HawkesOnlineTracker`, `VPIN`, `KylesLambda`, `GLFT`, `FundingCarry`, and `TCAMarkoutTracker`.
- Replays true order queue dynamics (FIFO queue priority, queue survival probability, adverse selection on market orders, and post-fill markouts at +1s, +5s, +10s).
- Run high-resolution 2-year simulation:
  ```bash
  piquant backtest-ticks --symbol BTCUSDT --capital 1000 --months 24
  ```
- Evaluates VIP Maker Rebate (-0.50 bps) capture vs Taker fee hedging costs, volatility-adjusted fractional Kelly leverage (strictly bounded to 0.5x–3.0x), and generates verified Markdown/JSON performance reports in `data/reports/tick_simulation_2yr.md`.

---

## Standalone Single-Pi 5 Mode (`cluster_mode: pi5_only`)

> **You do NOT need a Raspberry Pi 3B right now.**
> A single Raspberry Pi 5 has 4 high-performance Cortex-A76 cores and easily runs **both** the quantitative quoting engine and the web dashboard concurrently with zero configuration.

```bash
# Start standalone mode on a single Pi 5:
piquant dry-run --capital 1000 --addr :8080
```
- Cores 0 & 1 handle Linux OS housekeeping, web dashboard HTTP serving, and telemetry.
- Cores 2 & 3 run the isolated real-time market data ingestion, Hawkes point-process, VPIN toxicity, and GLFT quoting loops.

---

## Headless Setup (No Monitor Required)

To run your Raspberry Pi 5 24/7 in a closet or desk without a monitor, keyboard, or mouse:

1. **Flash OS**: Flash **Raspberry Pi OS Lite (64-bit)** via Raspberry Pi Imager. Set hostname (`piquant.local`), enable SSH, and set your WiFi credentials under OS customization settings (`Ctrl+Shift+X`).
2. **Connect via SSH**:
   ```bash
   ssh everett@piquant.local
   ```
3. **Prevent Sleep, CPU Throttling & WiFi Spikes (`/boot/firmware/config.txt`)**:
   Add to `/boot/firmware/config.txt`:
   ```ini
   arm_boost=1
   temp_limit=80
   ```
   Apply performance governor and disable WiFi power-saving:
   ```bash
   echo "performance" | sudo tee /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor
   sudo iwconfig wlan0 power off
   ```
4. **Deploy Autonomous 24/7 Background Service (`piquant-dryrun.service`)**:
   ```bash
   sudo cp deploy/piquant-dryrun.service /etc/systemd/system/
   sudo systemctl daemon-reload
   sudo systemctl enable --now piquant-dryrun.service
   ```
   The engine now starts automatically on boot, restarts on failure, and serves the dashboard continuously at `http://piquant.local:8080`.

---

## API Keys & Security Architecture: Public vs Live Capital

### Does having an API key make market data faster?
**NO.** Public market data WebSockets (Binance `data-stream.binance.vision:9443` and Bybit Linear WebSocket) broadcast to all participants simultaneously on the same multicast push engine. An API key does not give your packets higher physical priority.
- **What API Keys Provide:** Private order placement, order cancellation, and account balance queries with dedicated rate limit weights.

### Zero-API-Key Paper Dry Run
PiQuant requires **ZERO API keys** to run autonomous paper money dry runs (`piquant dry-run`) and historical backtests (`piquant backtest-ticks`, `piquant backtest-history`). It runs out of the box on live public exchange feeds.

### Security Blueprint for Live Real-Money Trading
When connecting real exchange accounts:
1. **Exchange Key Permissions:**
   - [x] Enable Reading
   - [x] Enable Spot & Margin Trading (or Futures Trading)
   - [ ] **NEVER Enable Withdrawals** (Unchecked by default. Never enable withdrawal permissions on trading API keys.)
2. **IP Whitelisting:** Lock keys to your static home IP or VPS IP address.
3. **Sub-Accounts:** Always trade on an isolated sub-account funded with test capital ($10–$50).
4. **Encrypted Storage:** Use `piquant setup` to store keys in `~/.config/piquant/secrets` with strict `0600` root-only read permissions.

For the complete quantitative and mathematical architecture guide, see [docs/TRADING_EXPLAINED.md](docs/TRADING_EXPLAINED.md).



