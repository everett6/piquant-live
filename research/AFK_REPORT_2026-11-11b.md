# AFK report — 2026-11-11, block 2 (08:21–09:25 PST)

## Your three targets

| Target | Result |
|---|---|
| ≥55% win rate | **Reached, but it doesn't make money.** The pre-registered geometry (TP 20 / SL 40 bps gross) won 55.7%, 62.9% and 57.2% on three holdouts (2025–26, 2021–22, 2018–20). Every geometry of the live entry loses about its round-trip cost per trade (≈14.4 bps), because the entry has zero gross edge. The 55% version lost slightly more per trade than the current one, so the pre-registered rule keeps the live setting. To force it anyway: `--target-profit 16 --stop-loss 40`. |
| ≥60% monthly market-beat rate | **Not reached under the pre-registered rule.** SMA50 trend filter at 2x: 56% / 52% / 64% / 62% of months on the four holdouts (pooled 59.6%, p = 0.035). It beat buy-and-hold total return on all four, including untouched 2016–17, but with 47–85% drawdowns and a strong dependence on funding costs. The 1x spot version was positive on all four holdouts (+10% / +14% / +430% / +1782% vs buy-and-hold −31% / −43% / +109% / +3121%). It is now in a pre-registered 12-month forward paper test (PREREG6). |
| LFM3 applicable or scrapped | **Scrapped.** On 4,000 fresh headlines, no output predicted BTC returns at 1h, 4h or 1 day (all \|ρ\| ≤ 0.03). Trading on it loses 15–34 bps per trade. CryptoNewsImpact's own labels score ~0 too. The GPU headline services are disabled; the model files are kept. |

## Theory confirmed on real data (round 3, T0)
- **Claim tested:** with random entries, the win rate equals SL / (TP + SL) and gross edge is zero.
- **Result:** confirmed in 24 of 24 TP×SL cells (worst miss 1.9 pp, gross within ±2.9 bps).
- **Strategies:** both wider-bracket strategies (S7, S8) failed their holdouts.
- All matrices are in research/quant_lab/results/REPORT.md.

## Fund mechanics and Go
- **Why the Pi made 0 trades in 11 hours:** VPIN buckets were sized for Binance.US.
  - On Coinbase, $10k buckets read ordinary flow as "toxic". Live measurement: 0.81 at $10k buckets vs 0.24 at $1M.
  - That cut leverage below the 3.7x one BTC nano contract needs on $200.
  - Fixed with per-venue buckets (641292e).
- **Hot path:** allocations per Coinbase event fell 92% (4.8 KB → 0.36 KB); time per event fell 4.6 → 4.0 µs (52af76b).
  - The news report is cached for 1 s and invalidated instantly by any new headline.
  - Order-book copies no longer allocate.
- **Expectancy breaker:** on by default (50 trades) (252781c). New entries stop once the run's own trades confirm negative expectancy at the upper 95% bound; open positions keep their exits.
- **Forward tracker:** `piquant trend`, a standalone daily ledger for PREREG6 (62a35ac). It is installed at ~/.local/bin/piquant-trend.

## Tests
- Go: 30/30 packages pass (full suite plus race on paper/macro).
- Python: all 7 test files pass.

## Waiting on you
1. **Deploy 252781c to the Pi.** Blocked by the permission classifier. It contains the VPIN fix that lets BTC trade, plus the speed-ups and breaker:
   `cd ~/PiQuant/piquant-live && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 ~/.local/go/bin/go build -ldflags "-X main.version=252781c" -o /tmp/piquant-arm64 ./cmd/piquant && scp /tmp/piquant-arm64 everett@raspberrypi.local:/tmp/piquant.new && ssh everett@raspberrypi.local 'sudo cp /usr/local/bin/piquant /usr/local/bin/piquant.prev-29a368d && sudo install -m 755 /tmp/piquant.new /usr/local/bin/piquant && sudo systemctl restart piquant'`
2. **Start the forward test service.** Blocked as "unauthorized persistence". Create `~/.config/systemd/user/piquant-trend.service` with `ExecStart=%h/.local/bin/piquant-trend trend --dir %h/piquant-trend` and `Restart=always`, then run `mkdir -p ~/piquant-trend && systemctl --user enable --now piquant-trend`. Or run `piquant-trend trend --once` once a day.
3. **Optional:** force ≥55% win rate live with `--target-profit 16 --stop-loss 40`, knowing it costs about 0.5–1.5 bps more per trade.
